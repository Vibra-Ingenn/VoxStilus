package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	wlDisplayID  uint32 = 1
	wlRegistryID uint32 = 2

	wlDispSyncOp        uint16 = 0
	wlDispGetRegistryOp uint16 = 1

	wlRegistryBindOp uint16 = 0

	// Shared manager/source/offer opcodes between ext_data_control_v1 and zwlr_data_control_v1
	dcMgrCreateSourceOp  uint16 = 0
	dcMgrGetDataDeviceOp uint16 = 1
	dcDevSetSelectionOp  uint16 = 0
	dcSourceOfferOp      uint16 = 0
	dcSourceDestroyOp    uint16 = 1
	dcOfferReceiveOp     uint16 = 0
	dcOfferDestroyOp     uint16 = 1

	// Device events
	dcDevEvtDataOffer        uint16 = 0
	dcDevEvtSelection        uint16 = 1
	dcDevEvtFinished         uint16 = 2
	dcDevEvtPrimarySelection uint16 = 3

	// Source events
	dcSrcEvtSend      uint16 = 0
	dcSrcEvtCancelled uint16 = 1

	maxClipReadBytes = 8 << 20 // 8 MiB per MIME cap during save
	maxClipSaveMimes = 6
)

type ClipboardEntry struct {
	Mime string
	Data []byte
}

type wlGlobal struct {
	Name    uint32
	Iface   string
	Version uint32
}

type clipOffer struct {
	id    uint32
	mimes []string
}

type activeClipSource struct {
	id           uint32
	entries      []ClipboardEntry
	byMime       map[string][]byte
	cancelled    bool
	consumedCh   chan struct{}
	consumedOnce sync.Once
}

func (s *activeClipSource) markConsumed() {
	if s == nil {
		return
	}
	s.consumedOnce.Do(func() {
		if s.consumedCh != nil {
			close(s.consumedCh)
		}
	})
}

func (s *activeClipSource) lookupMime(mime string) ([]byte, bool) {
	if data, ok := s.byMime[mime]; ok {
		return data, true
	}
	low := strings.ToLower(strings.TrimSpace(mime))
	if strings.HasPrefix(low, "text/plain") || low == "utf8_string" || low == "string" || low == "text" {
		for _, fallback := range []string{"text/plain;charset=utf-8", "text/plain", "UTF8_STRING"} {
			if data, ok := s.byMime[fallback]; ok {
				return data, true
			}
		}
	}
	return nil, false
}

// WaylandClipboard implements persistent zero-Cgo clipboard management via
// ext_data_control_manager_v1 or zwlr_data_control_manager_v1 over the Wayland socket.
type WaylandClipboard struct {
	connMu  sync.Mutex
	writeMu sync.Mutex
	stateMu sync.Mutex

	conn      *net.UnixConn
	nextObjID uint32
	deviceID  uint32
	mgrID     uint32
	seatID    uint32
	useExt    bool
	closed    bool

	globals        []wlGlobal
	syncCallbacks  map[uint32]chan struct{}
	offers         map[uint32]*clipOffer
	curSelOffer    uint32
	sources        map[uint32]*activeClipSource
	retiredSources map[uint32]struct{}
	latestOwnSrc   uint32
}

var globalClipboard = newWaylandClipboard()

func newWaylandClipboard() *WaylandClipboard {
	return &WaylandClipboard{
		syncCallbacks:  make(map[uint32]chan struct{}),
		offers:         make(map[uint32]*clipOffer),
		sources:        make(map[uint32]*activeClipSource),
		retiredSources: make(map[uint32]struct{}),
	}
}

func wlStringPayload(s string) []byte {
	n := len(s) + 1
	padded := (n + 3) &^ 3
	out := make([]byte, 4+padded)
	binary.LittleEndian.PutUint32(out[0:4], uint32(n))
	copy(out[4:], s)
	return out
}

func wlParseString(p []byte) (string, int, bool) {
	if len(p) < 4 {
		return "", 0, false
	}
	slen := int(binary.LittleEndian.Uint32(p[0:4]))
	if slen <= 0 || 4+slen > len(p) {
		return "", 0, false
	}
	padded := (slen + 3) &^ 3
	if 4+padded > len(p) {
		padded = len(p) - 4
	}
	s := strings.TrimRight(string(p[4:4+slen]), "\x00")
	return s, 4 + padded, true
}

func wlRegistryBindPayload(name uint32, iface string, version, newID uint32) []byte {
	strPart := wlStringPayload(iface)
	out := make([]byte, 4+len(strPart)+8)
	binary.LittleEndian.PutUint32(out[0:4], name)
	copy(out[4:4+len(strPart)], strPart)
	off := 4 + len(strPart)
	binary.LittleEndian.PutUint32(out[off:off+4], version)
	binary.LittleEndian.PutUint32(out[off+4:off+8], newID)
	return out
}

func wlBuildFrame(objID uint32, op uint16, payload []byte) []byte {
	sz := 8 + len(payload)
	frame := make([]byte, sz)
	binary.LittleEndian.PutUint32(frame[0:4], objID)
	binary.LittleEndian.PutUint32(frame[4:8], uint32(sz)<<16|uint32(op))
	copy(frame[8:], payload)
	return frame
}

func isSnapshotableMime(mime string) bool {
	if mime == "" {
		return false
	}
	switch strings.ToUpper(mime) {
	case "TARGETS", "TIMESTAMP", "MULTIPLE", "DELETE", "SAVE_TARGETS", "APPLICATION/X-KDE-CUTSELECTION":
		return false
	}
	// Accept standard media types (e.g. text/plain;charset=utf-8, text/html, image/png)
	// plus standard X11 fallback text targets advertised through XWayland.
	if strings.Contains(mime, "/") {
		return true
	}
	switch mime {
	case "UTF8_STRING", "STRING", "TEXT":
		return true
	}
	return false
}

func resolveWaylandSocketPath() string {
	ensureWaylandEnv()
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	disp := os.Getenv("WAYLAND_DISPLAY")
	if disp == "" {
		return ""
	}
	if strings.HasPrefix(disp, "/") {
		return disp
	}
	return filepath.Join(rt, disp)
}

func (c *WaylandClipboard) allocID() uint32 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	id := c.nextObjID
	c.nextObjID++
	return id
}

func (c *WaylandClipboard) sendMsg(objID uint32, op uint16, payload []byte, fds ...int) error {
	frame := wlBuildFrame(objID, op, payload)
	var oob []byte
	if len(fds) > 0 {
		oob = syscall.UnixRights(fds...)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.stateMu.Lock()
	conn := c.conn
	closed := c.closed
	c.stateMu.Unlock()
	if conn == nil || closed {
		return fmt.Errorf("wayland socket not connected")
	}

	off := 0
	for off < len(frame) {
		var curOOB []byte
		if off == 0 {
			curOOB = oob
		}
		n, _, err := conn.WriteMsgUnix(frame[off:], curOOB, nil)
		if n > 0 {
			off += n
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Roundtrip sends wl_display.sync, which flushes queued clipboard requests on the socket
// and waits until the compositor confirms receipt before returning.
func (c *WaylandClipboard) Roundtrip(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	cbID := c.allocID()
	done := make(chan struct{})
	c.stateMu.Lock()
	c.syncCallbacks[cbID] = done
	c.stateMu.Unlock()

	var p [4]byte
	binary.LittleEndian.PutUint32(p[:], cbID)
	if err := c.sendMsg(wlDisplayID, wlDispSyncOp, p[:]); err != nil {
		c.stateMu.Lock()
		delete(c.syncCallbacks, cbID)
		c.stateMu.Unlock()
		return err
	}

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		c.stateMu.Lock()
		delete(c.syncCallbacks, cbID)
		c.stateMu.Unlock()
		return fmt.Errorf("wayland sync timeout")
	}
}

func (c *WaylandClipboard) EnsureConnected() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	c.stateMu.Lock()
	ready := c.conn != nil && !c.closed && c.deviceID != 0
	c.stateMu.Unlock()
	if ready {
		return nil
	}
	return c.connectLocked()
}

func (c *WaylandClipboard) connectLocked() error {
	c.resetLocked()

	sock := resolveWaylandSocketPath()
	if sock == "" {
		return fmt.Errorf("no WAYLAND_DISPLAY socket found")
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return fmt.Errorf("dial wayland socket %s: %w", sock, err)
	}

	c.stateMu.Lock()
	c.conn = conn
	c.closed = false
	c.nextObjID = 3
	c.globals = nil
	c.syncCallbacks = make(map[uint32]chan struct{})
	c.offers = make(map[uint32]*clipOffer)
	c.curSelOffer = 0
	c.sources = make(map[uint32]*activeClipSource)
	c.retiredSources = make(map[uint32]struct{})
	c.latestOwnSrc = 0
	c.deviceID = 0
	c.mgrID = 0
	c.seatID = 0
	c.stateMu.Unlock()

	go c.readLoop(conn)

	var regPayload [4]byte
	binary.LittleEndian.PutUint32(regPayload[:], wlRegistryID)
	if err := c.sendMsg(wlDisplayID, wlDispGetRegistryOp, regPayload[:]); err != nil {
		c.resetLocked()
		return err
	}
	if err := c.Roundtrip(800 * time.Millisecond); err != nil {
		c.resetLocked()
		return err
	}

	var seatGlobal, extGlobal, wlrGlobal *wlGlobal
	c.stateMu.Lock()
	for i := range c.globals {
		g := &c.globals[i]
		switch g.Iface {
		case "wl_seat":
			if seatGlobal == nil {
				seatGlobal = g
			}
		case "ext_data_control_manager_v1":
			if extGlobal == nil {
				extGlobal = g
			}
		case "zwlr_data_control_manager_v1":
			if wlrGlobal == nil {
				wlrGlobal = g
			}
		}
	}
	c.stateMu.Unlock()

	if seatGlobal == nil {
		c.resetLocked()
		return fmt.Errorf("compositor did not advertise wl_seat")
	}
	var mgrGlobal *wlGlobal
	useExt := false
	if extGlobal != nil {
		mgrGlobal = extGlobal
		useExt = true
	} else if wlrGlobal != nil {
		mgrGlobal = wlrGlobal
	} else {
		c.resetLocked()
		return fmt.Errorf("compositor supports neither ext_data_control_manager_v1 nor zwlr_data_control_manager_v1")
	}

	seatID := c.allocID()
	if err := c.sendMsg(wlRegistryID, wlRegistryBindOp, wlRegistryBindPayload(seatGlobal.Name, seatGlobal.Iface, 1, seatID)); err != nil {
		c.resetLocked()
		return err
	}
	mgrVer := mgrGlobal.Version
	if useExt && mgrVer > 1 {
		mgrVer = 1
	} else if !useExt && mgrVer > 2 {
		mgrVer = 2
	}
	mgrID := c.allocID()
	if err := c.sendMsg(wlRegistryID, wlRegistryBindOp, wlRegistryBindPayload(mgrGlobal.Name, mgrGlobal.Iface, mgrVer, mgrID)); err != nil {
		c.resetLocked()
		return err
	}
	devID := c.allocID()
	var getDevPayload [8]byte
	binary.LittleEndian.PutUint32(getDevPayload[0:4], devID)
	binary.LittleEndian.PutUint32(getDevPayload[4:8], seatID)
	if err := c.sendMsg(mgrID, dcMgrGetDataDeviceOp, getDevPayload[:]); err != nil {
		c.resetLocked()
		return err
	}

	c.stateMu.Lock()
	c.seatID = seatID
	c.mgrID = mgrID
	c.deviceID = devID
	c.useExt = useExt
	c.stateMu.Unlock()

	// Sync so the compositor delivers the initial clipboard selection offer right away.
	_ = c.Roundtrip(600 * time.Millisecond)
	return nil
}

func (c *WaylandClipboard) Close() {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.resetLocked()
}

func (c *WaylandClipboard) resetLocked() {
	c.stateMu.Lock()
	conn := c.conn
	c.conn = nil
	c.closed = true
	for id, ch := range c.syncCallbacks {
		close(ch)
		delete(c.syncCallbacks, id)
	}
	c.stateMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *WaylandClipboard) readLoop(conn *net.UnixConn) {
	defer c.resetLocked()
	chunk := make([]byte, 8192)
	oob := make([]byte, 4096)
	var recvBuf []byte
	var pendingFDs []int

	closeFDs := func(fds []int) {
		for _, fd := range fds {
			if fd >= 0 {
				_ = syscall.Close(fd)
			}
		}
	}
	defer func() { closeFDs(pendingFDs) }()

	for {
		n, oobn, _, _, err := conn.ReadMsgUnix(chunk, oob)
		if oobn > 0 {
			if scms, parseErr := syscall.ParseSocketControlMessage(oob[:oobn]); parseErr == nil {
				for _, scm := range scms {
					if fds, fdErr := syscall.ParseUnixRights(&scm); fdErr == nil {
						pendingFDs = append(pendingFDs, fds...)
					}
				}
			}
		}
		if n > 0 {
			recvBuf = append(recvBuf, chunk[:n]...)
			for len(recvBuf) >= 8 {
				objID := binary.LittleEndian.Uint32(recvBuf[0:4])
				opSize := binary.LittleEndian.Uint32(recvBuf[4:8])
				op := uint16(opSize & 0xFFFF)
				sz := int(opSize >> 16)
				if sz < 8 || sz > 65536 {
					log.Printf("[CLIPBOARD] invalid wayland packet size=%d", sz)
					return
				}
				if len(recvBuf) < sz {
					break
				}
				payload := append([]byte(nil), recvBuf[8:sz]...)
				if sz == len(recvBuf) {
					recvBuf = recvBuf[:0]
				} else {
					copy(recvBuf, recvBuf[sz:])
					recvBuf = recvBuf[:len(recvBuf)-sz]
				}
				c.dispatchEvent(objID, op, payload, &pendingFDs)
			}
		}
		if err != nil {
			if err != io.EOF {
				c.stateMu.Lock()
				closed := c.closed
				c.stateMu.Unlock()
				if !closed {
					log.Printf("[CLIPBOARD] wayland readLoop ended: %v", err)
				}
			}
			return
		}
	}
}

func (c *WaylandClipboard) popFD(pending *[]int) int {
	if len(*pending) == 0 {
		return -1
	}
	fd := (*pending)[0]
	*pending = (*pending)[1:]
	return fd
}

func (c *WaylandClipboard) destroyObjectAsync(id uint32, op uint16) {
	if id == 0 {
		return
	}
	go func() {
		_ = c.sendMsg(id, op, nil)
	}()
}

func writeClipboardPipe(fd int, data []byte) {
	f := os.NewFile(uintptr(fd), "wl-clip-send")
	if f == nil {
		_ = syscall.Close(fd)
		return
	}
	defer f.Close()

	off := 0
	for off < len(data) {
		n, err := f.Write(data[off:])
		if n > 0 {
			off += n
		}
		if err != nil {
			return
		}
	}
}

func (c *WaylandClipboard) dispatchEvent(objID uint32, op uint16, payload []byte, pendingFDs *[]int) {
	switch objID {
	case wlDisplayID:
		if op == 0 && len(payload) >= 12 {
			target := binary.LittleEndian.Uint32(payload[0:4])
			code := binary.LittleEndian.Uint32(payload[4:8])
			msg, _, _ := wlParseString(payload[8:])
			log.Printf("[CLIPBOARD] wl_display error obj=%d code=%d: %s", target, code, msg)
		} else if op == 1 && len(payload) >= 4 {
			delID := binary.LittleEndian.Uint32(payload[0:4])
			c.stateMu.Lock()
			delete(c.retiredSources, delID)
			c.stateMu.Unlock()
		}
		return

	case wlRegistryID:
		if op == 0 && len(payload) >= 12 {
			name := binary.LittleEndian.Uint32(payload[0:4])
			iface, adv, ok := wlParseString(payload[4:])
			if ok && 4+adv+4 <= len(payload) {
				ver := binary.LittleEndian.Uint32(payload[4+adv : 8+adv])
				c.stateMu.Lock()
				c.globals = append(c.globals, wlGlobal{Name: name, Iface: iface, Version: ver})
				c.stateMu.Unlock()
			}
		}
		return
	}

	c.stateMu.Lock()
	if ch, ok := c.syncCallbacks[objID]; ok && op == 0 {
		delete(c.syncCallbacks, objID)
		c.stateMu.Unlock()
		close(ch)
		return
	}

	if objID == c.deviceID {
		switch op {
		case dcDevEvtDataOffer:
			if len(payload) >= 4 {
				newOffer := binary.LittleEndian.Uint32(payload[0:4])
				c.offers[newOffer] = &clipOffer{id: newOffer}
			}
			c.stateMu.Unlock()
		case dcDevEvtSelection:
			var selID uint32
			if len(payload) >= 4 {
				selID = binary.LittleEndian.Uint32(payload[0:4])
			}
			prev := c.curSelOffer
			c.curSelOffer = selID
			if prev != 0 && prev != selID {
				delete(c.offers, prev)
				c.stateMu.Unlock()
				c.destroyObjectAsync(prev, dcOfferDestroyOp)
				return
			}
			c.stateMu.Unlock()
		case dcDevEvtPrimarySelection:
			var priID uint32
			if len(payload) >= 4 {
				priID = binary.LittleEndian.Uint32(payload[0:4])
			}
			if priID != 0 && priID != c.curSelOffer {
				delete(c.offers, priID)
				c.stateMu.Unlock()
				c.destroyObjectAsync(priID, dcOfferDestroyOp)
				return
			}
			c.stateMu.Unlock()
		case dcDevEvtFinished:
			c.closed = true
			c.stateMu.Unlock()
		default:
			c.stateMu.Unlock()
		}
		return
	}

	if off, ok := c.offers[objID]; ok {
		if op == 0 {
			if mime, _, okStr := wlParseString(payload); okStr && mime != "" {
				off.mimes = append(off.mimes, mime)
			}
		}
		c.stateMu.Unlock()
		return
	}

	if src, ok := c.sources[objID]; ok {
		switch op {
		case dcSrcEvtSend:
			mime, _, _ := wlParseString(payload)
			fd := c.popFD(pendingFDs)
			data, found := src.lookupMime(mime)
			if found {
				src.markConsumed()
			}
			c.stateMu.Unlock()
			if fd >= 0 {
				if found {
					go writeClipboardPipe(fd, data)
				} else {
					log.Printf("[CLIPBOARD] unmatched send mime %q for src=%d", mime, objID)
					_ = syscall.Close(fd)
				}
			}
		case dcSrcEvtCancelled:
			src.cancelled = true
			if c.latestOwnSrc == objID {
				c.latestOwnSrc = 0
			}
			delete(c.sources, objID)
			c.retiredSources[objID] = struct{}{}
			c.stateMu.Unlock()
			c.destroyObjectAsync(objID, dcSourceDestroyOp)
		default:
			c.stateMu.Unlock()
		}
		return
	}

	if _, retired := c.retiredSources[objID]; retired && op == dcSrcEvtSend {
		fd := c.popFD(pendingFDs)
		c.stateMu.Unlock()
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return
	}
	c.stateMu.Unlock()
}

func (c *WaylandClipboard) readOfferMime(offerID uint32, mime string) ([]byte, error) {
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer rPipe.Close()

	type readResult struct {
		data []byte
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		b, readErr := io.ReadAll(io.LimitReader(rPipe, maxClipReadBytes+1))
		ch <- readResult{data: b, err: readErr}
	}()

	mimePart := wlStringPayload(mime)
	sendErr := c.sendMsg(offerID, dcOfferReceiveOp, mimePart, int(wPipe.Fd()))
	_ = wPipe.Close()
	if sendErr != nil {
		_ = rPipe.Close()
		return nil, sendErr
	}
	if err := c.Roundtrip(300 * time.Millisecond); err != nil {
		_ = rPipe.Close()
		return nil, err
	}

	select {
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		if len(res.data) > maxClipReadBytes {
			return nil, fmt.Errorf("mime %s exceeds limit", mime)
		}
		return res.data, nil
	case <-time.After(400 * time.Millisecond):
		_ = rPipe.Close()
		return nil, fmt.Errorf("timeout reading mime %s", mime)
	}
}

func prioritizeSnapshotMimes(mimes []string) []string {
	var preferred, rest []string
	seen := make(map[string]struct{}, len(mimes))
	for _, m := range mimes {
		if !isSnapshotableMime(m) {
			continue
		}
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		switch m {
		case "text/plain;charset=utf-8", "text/plain", "UTF8_STRING", "text/html", "image/png":
			preferred = append(preferred, m)
		default:
			rest = append(rest, m)
		}
	}
	out := append(preferred, rest...)
	if len(out) > maxClipSaveMimes {
		out = out[:maxClipSaveMimes]
	}
	return out
}

// Snapshot reads and caches the current regular CLIPBOARD entries (MIME types + bytes).
func (c *WaylandClipboard) Snapshot() ([]ClipboardEntry, error) {
	if err := c.EnsureConnected(); err != nil {
		return nil, err
	}
	_ = c.Roundtrip(350 * time.Millisecond)

	c.stateMu.Lock()
	if c.latestOwnSrc != 0 {
		if src, ok := c.sources[c.latestOwnSrc]; ok && !src.cancelled {
			cloned := make([]ClipboardEntry, len(src.entries))
			for i, e := range src.entries {
				cloned[i] = ClipboardEntry{Mime: e.Mime, Data: append([]byte(nil), e.Data...)}
			}
			c.stateMu.Unlock()
			return cloned, nil
		}
	}
	offerID := c.curSelOffer
	var rawMimes []string
	if offerID != 0 {
		if off, ok := c.offers[offerID]; ok {
			rawMimes = append([]string(nil), off.mimes...)
		}
	}
	c.stateMu.Unlock()

	if offerID == 0 || len(rawMimes) == 0 {
		return nil, nil
	}
	mimes := prioritizeSnapshotMimes(rawMimes)
	var entries []ClipboardEntry
	for _, m := range mimes {
		data, err := c.readOfferMime(offerID, m)
		if err != nil {
			continue
		}
		entries = append(entries, ClipboardEntry{Mime: m, Data: data})
	}
	return entries, nil
}

// SetText sets the standard Wayland CLIPBOARD to plain UTF-8 text and flushes before returning.
func (c *WaylandClipboard) SetText(text string) (uint32, error) {
	b := []byte(text)
	return c.SetEntries([]ClipboardEntry{
		{Mime: "text/plain;charset=utf-8", Data: b},
		{Mime: "text/plain", Data: b},
		{Mime: "UTF8_STRING", Data: b},
		{Mime: "TEXT", Data: b},
		{Mime: "STRING", Data: b},
	})
}

// SetEntries registers a new long-lived Wayland data_source serving all MIME entries and flushes.
func (c *WaylandClipboard) SetEntries(entries []ClipboardEntry) (uint32, error) {
	if err := c.EnsureConnected(); err != nil {
		return 0, err
	}
	c.stateMu.Lock()
	mgrID := c.mgrID
	devID := c.deviceID
	c.stateMu.Unlock()
	if mgrID == 0 || devID == 0 {
		return 0, fmt.Errorf("wayland data_control device unavailable")
	}

	srcID := c.allocID()
	src := &activeClipSource{
		id:         srcID,
		entries:    make([]ClipboardEntry, 0, len(entries)),
		byMime:     make(map[string][]byte, len(entries)),
		consumedCh: make(chan struct{}),
	}
	for _, e := range entries {
		if e.Mime == "" {
			continue
		}
		if _, exists := src.byMime[e.Mime]; exists {
			continue
		}
		cp := append([]byte(nil), e.Data...)
		src.entries = append(src.entries, ClipboardEntry{Mime: e.Mime, Data: cp})
		src.byMime[e.Mime] = cp
	}

	c.stateMu.Lock()
	c.sources[srcID] = src
	c.latestOwnSrc = srcID
	c.stateMu.Unlock()

	var createPayload [4]byte
	binary.LittleEndian.PutUint32(createPayload[:], srcID)
	if err := c.sendMsg(mgrID, dcMgrCreateSourceOp, createPayload[:]); err != nil {
		c.dropSource(srcID)
		return 0, err
	}
	for _, e := range src.entries {
		if err := c.sendMsg(srcID, dcSourceOfferOp, wlStringPayload(e.Mime)); err != nil {
			c.dropSource(srcID)
			return 0, err
		}
	}
	var selPayload [4]byte
	binary.LittleEndian.PutUint32(selPayload[:], srcID)
	if err := c.sendMsg(devID, dcDevSetSelectionOp, selPayload[:]); err != nil {
		c.dropSource(srcID)
		return 0, err
	}

	// Rule 3: Flush and confirm compositor activation before triggering /dev/uinput Ctrl+V.
	if err := c.Roundtrip(500 * time.Millisecond); err != nil {
		return 0, err
	}
	return srcID, nil
}

func (c *WaylandClipboard) dropSource(srcID uint32) {
	c.stateMu.Lock()
	delete(c.sources, srcID)
	if c.latestOwnSrc == srcID {
		c.latestOwnSrc = 0
	}
	c.stateMu.Unlock()
}

func (c *WaylandClipboard) WasCancelled(srcID uint32) bool {
	if srcID == 0 {
		return true
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	src, ok := c.sources[srcID]
	return !ok || src.cancelled || c.latestOwnSrc != srcID
}

// WaitConsumed waits until a Wayland client actually requests the data for srcID (via send),
// or until timeout elapses. Returns true if the clipboard offer was read by a target app.
func (c *WaylandClipboard) WaitConsumed(srcID uint32, timeout time.Duration) bool {
	if srcID == 0 {
		return false
	}
	c.stateMu.Lock()
	src, ok := c.sources[srcID]
	var ch chan struct{}
	if ok && src != nil {
		ch = src.consumedCh
	}
	c.stateMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}
