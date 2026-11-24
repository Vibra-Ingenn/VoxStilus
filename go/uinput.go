package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Linux kernel input & uinput constants for x86_64/arm64 (verified against <linux/uinput.h>).
const (
	uinputDevicePath = "/dev/uinput"

	uiSetEvBit   = uintptr(0x40045564) // _IOW('U', 100, int)
	uiSetKeyBit  = uintptr(0x40045565) // _IOW('U', 101, int)
	uiDevSetup   = uintptr(0x405c5503) // _IOW('U', 3, struct uinput_setup[92])
	uiDevCreate  = uintptr(0x5501)     // _IO('U', 1)
	uiDevDestroy = uintptr(0x5502)     // _IO('U', 2)

	uinputEvSyn  uint16 = 0x00
	uinputEvKey  uint16 = 0x01
	uinputSynRpt uint16 = 0

	uinputKeyBackspace uint16 = 14
	uinputKeyLeftCtrl  uint16 = 29
	uinputKeyLeftShift uint16 = 42
	uinputKeyV         uint16 = 47

	uinputBusUSB uint16 = 0x03
)

type keyAction struct {
	Code uint16
	Down bool
}

func buildPasteKeySteps(terminal bool) []keyAction {
	if terminal {
		return []keyAction{
			{Code: uinputKeyLeftCtrl, Down: true},
			{Code: uinputKeyLeftShift, Down: true},
			{Code: uinputKeyV, Down: true},
			{Code: uinputKeyV, Down: false},
			{Code: uinputKeyLeftShift, Down: false},
			{Code: uinputKeyLeftCtrl, Down: false},
		}
	}
	return []keyAction{
		{Code: uinputKeyLeftCtrl, Down: true},
		{Code: uinputKeyV, Down: true},
		{Code: uinputKeyV, Down: false},
		{Code: uinputKeyLeftCtrl, Down: false},
	}
}

func encodeInputEvent(typ, code uint16, value int32) [24]byte {
	var ev [24]byte
	// Bytes [0:16] are kernel struct timeval (0 leaves kernel timestamp fill).
	binary.LittleEndian.PutUint16(ev[16:18], typ)
	binary.LittleEndian.PutUint16(ev[18:20], code)
	binary.LittleEndian.PutUint32(ev[20:24], uint32(value))
	return ev
}

func encodeUInputSetup(name string) [92]byte {
	var setup [92]byte
	binary.LittleEndian.PutUint16(setup[0:2], uinputBusUSB)
	binary.LittleEndian.PutUint16(setup[2:4], 0x1d6b) // Linux Foundation test vendor ID
	binary.LittleEndian.PutUint16(setup[4:6], 0x0002)
	binary.LittleEndian.PutUint16(setup[6:8], 1)
	copy(setup[8:87], name) // Leave at least one trailing NUL inside name[80]
	return setup
}

// UInputKeyboard owns a persistent pure-Go /dev/uinput virtual keyboard device.
// Using one persistent device avoids per-paste libinput enumeration latency.
type UInputKeyboard struct {
	mu   sync.Mutex
	file *os.File
}

var globalUInput = &UInputKeyboard{}

func uinputIoctl(fd uintptr, req, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg)
	if errno != 0 {
		return errno
	}
	return nil
}

func (u *UInputKeyboard) Init() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.file != nil {
		return nil
	}

	f, err := os.OpenFile(uinputDevicePath, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", uinputDevicePath, err)
	}
	cleanup := func(cause error) error {
		_ = f.Close()
		return cause
	}

	fd := f.Fd()
	for _, ev := range []uintptr{uintptr(uinputEvSyn), uintptr(uinputEvKey)} {
		if err := uinputIoctl(fd, uiSetEvBit, ev); err != nil {
			return cleanup(fmt.Errorf("UI_SET_EVBIT(%d): %w", ev, err))
		}
	}
	for _, key := range []uint16{uinputKeyBackspace, uinputKeyLeftCtrl, uinputKeyLeftShift, uinputKeyV} {
		if err := uinputIoctl(fd, uiSetKeyBit, uintptr(key)); err != nil {
			return cleanup(fmt.Errorf("UI_SET_KEYBIT(%d): %w", key, err))
		}
	}

	setup := encodeUInputSetup("voxstilus-uinput")
	if err := uinputIoctl(fd, uiDevSetup, uintptr(unsafe.Pointer(&setup[0]))); err != nil {
		return cleanup(fmt.Errorf("UI_DEV_SETUP: %w", err))
	}
	if err := uinputIoctl(fd, uiDevCreate, 0); err != nil {
		return cleanup(fmt.Errorf("UI_DEV_CREATE: %w", err))
	}

	// Give libinput / Wayland compositor time once at startup to attach the new seat keyboard.
	time.Sleep(120 * time.Millisecond)
	u.file = f
	return nil
}

func (u *UInputKeyboard) Close() {
	u.mu.Lock()
	f := u.file
	u.file = nil
	u.mu.Unlock()
	if f == nil {
		return
	}
	_ = uinputIoctl(f.Fd(), uiDevDestroy, 0)
	_ = f.Close()
}

func (u *UInputKeyboard) writeRaw(typ, code uint16, value int32) error {
	ev := encodeInputEvent(typ, code, value)
	_, err := u.file.Write(ev[:])
	return err
}

func (u *UInputKeyboard) emitKey(code uint16, value int32) error {
	if err := u.writeRaw(uinputEvKey, code, value); err != nil {
		return err
	}
	return u.writeRaw(uinputEvSyn, uinputSynRpt, 0)
}

func (u *UInputKeyboard) SendPaste(terminal bool) error {
	if err := u.Init(); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.file == nil {
		return fmt.Errorf("uinput device closed")
	}

	steps := buildPasteKeySteps(terminal)
	var held []uint16
	defer func() {
		// Always release any remaining pressed keys on panic or error so modifiers never stick down.
		for i := len(held) - 1; i >= 0; i-- {
			_ = u.emitKey(held[i], 0)
		}
	}()

	for i, step := range steps {
		val := int32(0)
		if step.Down {
			val = 1
		}
		if err := u.emitKey(step.Code, val); err != nil {
			return err
		}
		if step.Down {
			held = append(held, step.Code)
		} else {
			for j := len(held) - 1; j >= 0; j-- {
				if held[j] == step.Code {
					held = append(held[:j], held[j+1:]...)
					break
				}
			}
		}
		if i < len(steps)-1 {
			time.Sleep(12 * time.Millisecond)
		}
	}
	return nil
}

func (u *UInputKeyboard) SendBackspaces(n int) error {
	if n <= 0 {
		return nil
	}
	if err := u.Init(); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.file == nil {
		return fmt.Errorf("uinput device closed")
	}
	for i := 0; i < n; i++ {
		if err := u.emitKey(uinputKeyBackspace, 1); err != nil {
			return err
		}
		time.Sleep(2 * time.Millisecond)
		if err := u.emitKey(uinputKeyBackspace, 0); err != nil {
			_ = u.emitKey(uinputKeyBackspace, 0)
			return err
		}
		if i < n-1 {
			time.Sleep(2 * time.Millisecond)
		}
	}
	return nil
}
