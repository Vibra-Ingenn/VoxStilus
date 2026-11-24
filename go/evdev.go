package main

import (
	"encoding/binary"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Linux evdev constants (64-bit input_event = 24 bytes: 16B timeval + 2B type + 2B code + 4B value).
const (
	evKey           = 0x01
	eviocgbitKey    = 0x80604521 // _IOC(READ, 'E', 0x21, 96)
	inputEventBytes = 24
)

var keyNameMap = map[string]uint16{
	"KEY_LEFTCTRL":   29,
	"KEY_LEFTSHIFT":  42,
	"KEY_LEFTALT":    56,
	"KEY_CAPSLOCK":   58,
	"KEY_SCROLLLOCK": 70,
	"KEY_RIGHTCTRL":  97,
	"KEY_RIGHTALT":   100,
	"KEY_PAUSE":      119,
	"KEY_RIGHTMETA":  126,
	"KEY_COMPOSE":    127,
	"KEY_F13":        183,
	"KEY_F14":        184,
	"KEY_F15":        185,
}

func resolveKeyCode(name string) uint16 {
	norm := strings.ToUpper(strings.TrimSpace(name))
	if !strings.HasPrefix(norm, "KEY_") {
		norm = "KEY_" + norm
	}
	if code, ok := keyNameMap[norm]; ok {
		return code
	}
	return 100 // KEY_RIGHTALT
}

func isVirtualDevice(eventPath string) bool {
	sysName := filepath.Join("/sys/class/input", filepath.Base(eventPath), "device", "name")
	raw, err := os.ReadFile(sysName)
	if err != nil {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(string(raw)))
	for _, kw := range []string{"voxstilus", "ydotool", "uinput", "virtual", "fake"} {
		if strings.Contains(name, kw) {
			return true
		}
	}
	return false
}

func hasKeyCapability(f *os.File, code uint16) bool {
	var bits [96]byte
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(eviocgbitKey), uintptr(unsafe.Pointer(&bits[0])))
	if errno != 0 {
		return false
	}
	byteIdx := code / 8
	bitMask := byte(1 << (code % 8))
	if int(byteIdx) >= len(bits) {
		return false
	}
	return (bits[byteIdx] & bitMask) != 0
}

type KeyListener struct {
	targetCode  uint16
	triggerMode string
	onPress     func()
	onRelease   func()

	mu        sync.Mutex
	held      bool
	toggledOn bool
	active    map[string]struct{}
}

func NewKeyListener(keyName, triggerMode string, onPress, onRelease func()) *KeyListener {
	mode := "hold"
	if triggerMode == "toggle" {
		mode = "toggle"
	}
	return &KeyListener{
		targetCode:  resolveKeyCode(keyName),
		triggerMode: mode,
		onPress:     onPress,
		onRelease:   onRelease,
		active:      make(map[string]struct{}),
	}
}

func (k *KeyListener) isRecording() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.held || k.toggledOn
}

func (k *KeyListener) Run(stopCh <-chan struct{}) {
	k.scanAndAttach(stopCh)
	ticker := time.NewTicker(2500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			k.scanAndAttach(stopCh)
		}
	}
}

func (k *KeyListener) scanAndAttach(stopCh <-chan struct{}) {
	paths, err := filepath.Glob("/dev/input/event*")
	if err != nil {
		return
	}
	for _, p := range paths {
		if isVirtualDevice(p) {
			continue
		}
		k.mu.Lock()
		_, already := k.active[p]
		k.mu.Unlock()
		if already {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		if !hasKeyCapability(f, k.targetCode) {
			_ = f.Close()
			continue
		}
		k.mu.Lock()
		k.active[p] = struct{}{}
		k.mu.Unlock()
		log.Printf("listening on %s (passive, no grab)", p)
		go k.readLoop(p, f, stopCh)
	}
}

func (k *KeyListener) readLoop(path string, f *os.File, stopCh <-chan struct{}) {
	defer func() {
		_ = f.Close()
		k.mu.Lock()
		delete(k.active, path)
		wasHeld := k.held
		if wasHeld {
			k.held = false
		}
		k.mu.Unlock()
		if wasHeld {
			k.onRelease()
		}
	}()

	buf := make([]byte, inputEventBytes*16)
	for {
		select {
		case <-stopCh:
			return
		default:
		}
		n, err := f.Read(buf)
		if err != nil {
			log.Printf("input device disconnected: %s (%v)", path, err)
			return
		}
		for off := 0; off+inputEventBytes <= n; off += inputEventBytes {
			evType := binary.LittleEndian.Uint16(buf[off+16 : off+18])
			evCode := binary.LittleEndian.Uint16(buf[off+18 : off+20])
			evVal := int32(binary.LittleEndian.Uint32(buf[off+20 : off+24]))
			if evType != evKey {
				continue
			}
			if evCode == k.targetCode {
				k.handleKey(evVal)
				continue
			}
			if !k.isRecording() {
				globalEditTracker.OnEvdevKey(evCode, evVal)
			}
		}
	}
}

func (k *KeyListener) handleKey(val int32) {
	k.mu.Lock()
	if k.triggerMode == "toggle" {
		if val != 1 {
			k.mu.Unlock()
			return
		}
		k.toggledOn = !k.toggledOn
		start := k.toggledOn
		k.mu.Unlock()
		if start {
			k.onPress()
		} else {
			k.onRelease()
		}
		return
	}

	if val == 1 && !k.held {
		k.held = true
		k.mu.Unlock()
		k.onPress()
	} else if val == 0 && k.held {
		k.held = false
		k.mu.Unlock()
		k.onRelease()
	} else {
		k.mu.Unlock()
	}
}
