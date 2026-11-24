package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var terminalKeywords = []string{
	"cosmicterm",
	"cosmic-term",
	"ghostty",
	"konsole",
	"kitty",
	"alacritty",
	"wezterm",
	"foot",
	"xterm",
	"terminator",
	"tilix",
	"ptyxis",
	"rio",
}

func isTerminalAppID(appID string) bool {
	cls := strings.ToLower(strings.TrimSpace(appID))
	if cls == "" {
		return false
	}
	for _, term := range terminalKeywords {
		if strings.Contains(cls, term) {
			return true
		}
	}
	return false
}

var (
	cosmicExtAppIDRe = regexp.MustCompile(`ext_foreign_toplevel_handle_v1@(\d+)\.app_id,\s*\(Some\("([^"]+)"\)\)`)
	cosmicMapRe      = regexp.MustCompile(`get_cosmic_toplevel\(zcosmic_toplevel_handle_v1@(\d+),\s*ext_foreign_toplevel_handle_v1@(\d+)\)`)
	cosmicStateRe    = regexp.MustCompile(`zcosmic_toplevel_handle_v1@(\d+)\.state,\s*\(\[([^\]]*)\]\)`)
)

func parseCosmicActiveAppID(debugLog string) string {
	extToApp := map[string]string{}
	for _, m := range cosmicExtAppIDRe.FindAllStringSubmatch(debugLog, -1) {
		extToApp[m[1]] = m[2]
	}
	cosToExt := map[string]string{}
	for _, m := range cosmicMapRe.FindAllStringSubmatch(debugLog, -1) {
		cosToExt[m[1]] = m[2]
	}
	for _, m := range cosmicStateRe.FindAllStringSubmatch(debugLog, -1) {
		cosID := m[1]
		rawBytes := strings.Split(m[2], ",")
		for i := 0; i+3 < len(rawBytes); i += 4 {
			b0 := strings.TrimSpace(rawBytes[i])
			b1 := strings.TrimSpace(rawBytes[i+1])
			b2 := strings.TrimSpace(rawBytes[i+2])
			b3 := strings.TrimSpace(rawBytes[i+3])
			// ZCOSMIC_TOPLEVEL_HANDLE_V1_STATE_ACTIVATED = 2 (little-endian uint32: 2, 0, 0, 0)
			if b0 == "2" && b1 == "0" && b2 == "0" && b3 == "0" {
				if extID, ok := cosToExt[cosID]; ok {
					return extToApp[extID]
				}
			}
		}
	}
	return ""
}

func focusedAppID() string {
	// 1. Niri compositor (when NIRI_SOCKET is active or niri msg succeeds)
	if os.Getenv("NIRI_SOCKET") != "" || strings.EqualFold(os.Getenv("XDG_CURRENT_DESKTOP"), "niri") {
		if niriPath, err := exec.LookPath("niri"); err == nil {
			if out, err := exec.Command(niriPath, "msg", "-j", "focused-window").Output(); err == nil {
				var win struct {
					AppID string `json:"app_id"`
				}
				if json.Unmarshal(out, &win) == nil && win.AppID != "" {
					return win.AppID
				}
			}
		}
	}

	// 2. COSMIC compositor (via cosmic-toplevel + WAYLAND_DEBUG zcosmic_toplevel_info_v1 inspection)
	const cosmicToplevelBin = "/usr/lib/pop-launcher/plugins/cosmic_toplevel/cosmic-toplevel"
	if _, err := os.Stat(cosmicToplevelBin); err == nil {
		cmd := exec.Command(cosmicToplevelBin)
		cmd.Env = append(os.Environ(), "WAYLAND_DEBUG=1")
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if stdin, err := cmd.StdinPipe(); err == nil {
			if err := cmd.Start(); err == nil {
				time.Sleep(35 * time.Millisecond)
				_, _ = io.WriteString(stdin, "{\"Search\":\"\"}\n")
				time.Sleep(35 * time.Millisecond)
				_, _ = io.WriteString(stdin, "\"Exit\"\n")
				_ = stdin.Close()
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				select {
				case <-done:
				case <-time.After(150 * time.Millisecond):
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
					}
				}
				if appID := parseCosmicActiveAppID(out.String()); appID != "" {
					return appID
				}
			}
		}
	}

	// 3. Fallback Niri check even if XDG_CURRENT_DESKTOP wasn't set to niri
	if niriPath, err := exec.LookPath("niri"); err == nil && os.Getenv("NIRI_SOCKET") != "" {
		if out, err := exec.Command(niriPath, "msg", "-j", "focused-window").Output(); err == nil {
			var win struct {
				AppID string `json:"app_id"`
			}
			if json.Unmarshal(out, &win) == nil && win.AppID != "" {
				return win.AppID
			}
		}
	}

	// 4. KDE KWin fallback (kdotool)
	if path, err := exec.LookPath("kdotool"); err == nil {
		if idOut, err := exec.Command(path, "getactivewindow").Output(); err == nil {
			winID := strings.TrimSpace(string(idOut))
			if winID != "" {
				if clsOut, err := exec.Command(path, "getwindowclassname", winID).Output(); err == nil {
					return strings.TrimSpace(string(clsOut))
				}
			}
		}
	}
	return ""
}

// isTerminalFocused checks env override or Wayland window class (COSMIC, Niri, KDE) when available.
func isTerminalFocused() bool {
	if os.Getenv("VOXSTILUS_TERMINAL") == "1" {
		return true
	}
	return isTerminalAppID(focusedAppID())
}

func ensureWaylandEnv() {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
		_ = os.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	}
	matches, _ := filepath.Glob(filepath.Join(runtimeDir, "wayland-*"))
	for _, m := range matches {
		if !strings.HasSuffix(m, ".lock") {
			_ = os.Setenv("WAYLAND_DISPLAY", filepath.Base(m))
			return
		}
	}
}

func ydotoolCmd(ydoPath string, args ...string) *exec.Cmd {
	cmd := exec.Command(ydoPath, args...)
	if os.Getenv("YDOTOOL_SOCKET") == "" {
		sock := fmt.Sprintf("/run/user/%d/.ydotool_socket", os.Getuid())
		if _, err := os.Stat(sock); err == nil {
			cmd.Env = append(os.Environ(), "YDOTOOL_SOCKET="+sock)
		}
	}
	return cmd
}

var (
	pasteMu sync.Mutex
	clipGen atomic.Uint64
)

func cancelPendingClipboardRestore() {
	clipGen.Add(1)
}

func emitPasteKeystroke(terminal bool) error {
	if err := globalUInput.SendPaste(terminal); err == nil {
		return nil
	} else {
		log.Printf("[PASTE] direct /dev/uinput failed (%v), trying fallbacks", err)
	}

	var ydoErr error
	if ydoPath, err := exec.LookPath("ydotool"); err == nil {
		// Linux input keycodes: 29=LEFTCTRL, 42=LEFTSHIFT, 47=V. Always release modifiers (:0).
		var args []string
		if terminal {
			args = []string{"key", "--key-delay=12", "29:1", "42:1", "47:1", "47:0", "42:0", "29:0"}
		} else {
			args = []string{"key", "--key-delay=12", "29:1", "47:1", "47:0", "29:0"}
		}
		if err := ydotoolCmd(ydoPath, args...).Run(); err == nil {
			return nil
		} else {
			ydoErr = err
			log.Printf("ydotool paste failed (%v), falling back to wtype", err)
		}
	}

	if wtypePath, err := exec.LookPath("wtype"); err == nil {
		var args []string
		if terminal {
			args = []string{"-M", "ctrl", "-M", "shift", "-k", "v", "-m", "shift", "-m", "ctrl"}
		} else {
			args = []string{"-M", "ctrl", "-k", "v", "-m", "ctrl"}
		}
		if err := exec.Command(wtypePath, args...).Run(); err != nil {
			return fmt.Errorf("wtype paste failed: %w (ydotool err: %v)", err, ydoErr)
		}
		return nil
	}

	if ydoErr != nil {
		return fmt.Errorf("ydotool paste failed: %w", ydoErr)
	}
	return fmt.Errorf("no uinput, ydotool, or wtype paste mechanism available")
}

// sendBackspaces is strictly used after stop when the user speaks a complete "scratch that" command.
func sendBackspaces(n int) error {
	if n <= 0 {
		return nil
	}
	ensureWaylandEnv()
	if err := globalUInput.SendBackspaces(n); err == nil {
		return nil
	}
	if ydoPath, err := exec.LookPath("ydotool"); err == nil {
		args := make([]string, 0, 2+n*2)
		args = append(args, "key", "--key-delay=2")
		for i := 0; i < n; i++ {
			args = append(args, "14:1", "14:0")
		}
		if err := ydotoolCmd(ydoPath, args...).Run(); err == nil {
			return nil
		}
	}
	if wtypePath, err := exec.LookPath("wtype"); err == nil {
		args := make([]string, 0, n*2)
		for i := 0; i < n; i++ {
			args = append(args, "-k", "BackSpace")
		}
		return exec.Command(wtypePath, args...).Run()
	}
	return fmt.Errorf("no uinput, ydotool, or wtype found")
}

func atomicPaste(text string) error {
	return atomicPasteWithPreserve(text, false)
}

// atomicPasteWithPreserve snapshots the user's current Wayland CLIPBOARD selection,
// sets & flushes the transcribed text cleanly over Wayland data-control (or wl-copy fallback),
// emits a single /dev/uinput paste keystroke, and restores the saved clipboard after ~220ms.
func atomicPasteWithPreserve(text string, preserve bool) error {
	if text == "" {
		return nil
	}
	ensureWaylandEnv()

	pasteMu.Lock()
	defer pasteMu.Unlock()

	myGen := clipGen.Add(1)

	// Rule 3: allow Wayland compositor focus to settle on the target input box,
	// and detect terminal focus BEFORE writing clipboard so there is zero delay before Ctrl+V.
	time.Sleep(50 * time.Millisecond)
	terminal := isTerminalFocused()

	var saved []ClipboardEntry
	var tempSrcID uint32
	usedWaylandProto := false

	if preserve {
		if snap, err := globalClipboard.Snapshot(); err != nil {
			log.Printf("[CLIPBOARD] snapshot note: %v", err)
		} else {
			saved = snap
		}
	}

	if srcID, err := globalClipboard.SetText(text); err == nil {
		tempSrcID = srcID
		usedWaylandProto = true
	} else {
		log.Printf("[CLIPBOARD] wayland data-control unavailable (%v), falling back to wl-copy", err)
		copyCmd := exec.Command("wl-copy")
		copyCmd.Stdin = strings.NewReader(text)
		if copyErr := copyCmd.Run(); copyErr != nil {
			return fmt.Errorf("wl-copy failed: %w (data-control err: %v)", copyErr, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := emitPasteKeystroke(terminal); err != nil {
		return err
	}

	if preserve && len(saved) > 0 && usedWaylandProto {
		go func(gen uint64, src uint32, prev []ClipboardEntry) {
			consumed := globalClipboard.WaitConsumed(src, 900*time.Millisecond)
			if consumed {
				// Allow multi-request apps / XWayland bridges to finish reading before restoring.
				time.Sleep(250 * time.Millisecond)
			} else {
				log.Printf("[CLIPBOARD] target did not read src=%d within 900ms; restoring previous clipboard", src)
			}
			if clipGen.Load() != gen {
				return
			}
			if globalClipboard.WasCancelled(src) {
				// User or another app replaced clipboard during the paste window; do not overwrite it.
				return
			}
			if _, err := globalClipboard.SetEntries(prev); err != nil {
				log.Printf("[CLIPBOARD] restore failed: %v", err)
			}
		}(myGen, tempSrcID, saved)
	}
	return nil
}
