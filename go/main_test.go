package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestEncodeWAVHeader(t *testing.T) {
	pcm := make([]byte, 3200)
	wav := encodeWAV(pcm)
	if len(wav) != 44+len(pcm) {
		t.Fatalf("expected %d bytes, got %d", 44+len(pcm), len(wav))
	}
	if string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("invalid RIFF/WAVE header")
	}
	dataSize := binary.LittleEndian.Uint32(wav[40:44])
	if dataSize != uint32(len(pcm)) {
		t.Fatalf("expected data size %d, got %d", len(pcm), dataSize)
	}
	if !bytes.Equal(wav[44:], pcm) {
		t.Fatalf("PCM payload mismatch")
	}
}

func TestProcessTextDictionaryAndFillers(t *testing.T) {
	cfg := Config{
		RemoveFillerWords:   true,
		EnableVoiceCommands: true,
		CustomDictionary:    map[string]string{"kuda": "CUDA", "get hub": "GitHub"},
	}
	got := processText("Um, push kuda to get hub new line please.", cfg)
	want := "push CUDA to GitHub\nplease."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestProcessTextStripsSoundTagsAndScratchThat(t *testing.T) {
	cfg := Config{
		RemoveFillerWords:   true,
		EnableVoiceCommands: true,
	}
	if got := processText("[blank_audio]", cfg); got != "" {
		t.Fatalf("expected empty for [blank_audio], got %q", got)
	}
	if got := processText("Scratch that.", cfg); got != "" {
		t.Fatalf("expected empty for Scratch that, got %q", got)
	}
	if got := processText("[Music] Hello world (laughs)", cfg); got != "Hello world" {
		t.Fatalf("expected 'Hello world', got %q", got)
	}
}

func TestStutterRemovalAndCodeMode(t *testing.T) {
	cfg := Config{
		RemoveFillerWords:   true,
		EnableVoiceCommands: true,
	}
	if got := removeWordStutters("the the quick brown fox"); got != "the quick brown fox" {
		t.Fatalf("stutter removal failed: got %q", got)
	}
	if got := applyCodeFormattingCommands("camel case user id"); got != "userId" {
		t.Fatalf("camel case failed: got %q", got)
	}
	if got := applyCodeFormattingCommands("snake case max retries"); got != "max_retries" {
		t.Fatalf("snake case failed: got %q", got)
	}
	if got := applyCodeFormattingCommands("pascal case voice trainer"); got != "VoiceTrainer" {
		t.Fatalf("pascal case failed: got %q", got)
	}
	if got := applyCodeFormattingCommands("kebab case talk to type"); got != "talk-to-type" {
		t.Fatalf("kebab case failed: got %q", got)
	}
	if got := applyCodeFormattingCommands("const case api key"); got != "API_KEY" {
		t.Fatalf("const case failed: got %q", got)
	}
	if got := processText("the the camel case user id double equals 5 semicolon", cfg); got != "the userId == 5;" {
		t.Fatalf("combined processText failed: got %q", got)
	}
}

func TestScratchCommandDetection(t *testing.T) {
	for _, s := range []string{"Scratch that.", "undo that!", "Undo last", "delete last sentence."} {
		if !isScratchCommand(s) {
			t.Fatalf("expected %q to be scratch command", s)
		}
	}
	if isScratchCommand("please scratch that bug later") {
		t.Fatalf("unexpected scratch match on normal sentence")
	}
}

func TestResolveKeyCode(t *testing.T) {
	if resolveKeyCode("KEY_RIGHTALT") != 100 {
		t.Fatalf("expected 100 for KEY_RIGHTALT")
	}
	if resolveKeyCode("RIGHTALT") != 100 {
		t.Fatalf("expected 100 for RIGHTALT")
	}
}

func TestIsTerminalAppID(t *testing.T) {
	if !isTerminalAppID("com.system76.CosmicTerm") {
		t.Fatalf("expected CosmicTerm to be recognized as terminal")
	}
	if !isTerminalAppID("com.mitchellh.ghostty") {
		t.Fatalf("expected ghostty to be recognized as terminal")
	}
	if isTerminalAppID("antigravity-ide") {
		t.Fatalf("did not expect antigravity-ide to be recognized as terminal")
	}
}

func TestParseCosmicActiveAppID(t *testing.T) {
	sample := `
[ 1.0][rs] <- ext_foreign_toplevel_handle_v1@4278190083.app_id, (Some("com.system76.CosmicTerm"))
[ 1.1][rs] <- ext_foreign_toplevel_handle_v1@4278190084.app_id, (Some("antigravity-ide"))
[ 1.2][rs] -> zcosmic_toplevel_info_v1@5.get_cosmic_toplevel(zcosmic_toplevel_handle_v1@10, ext_foreign_toplevel_handle_v1@4278190083)
[ 1.3][rs] -> zcosmic_toplevel_info_v1@5.get_cosmic_toplevel(zcosmic_toplevel_handle_v1@11, ext_foreign_toplevel_handle_v1@4278190084)
[ 1.4][rs] <- zcosmic_toplevel_handle_v1@10.state, ([0, 0, 0, 0, 2, 0, 0, 0])
[ 1.5][rs] <- zcosmic_toplevel_handle_v1@11.state, ([1, 0, 0, 0])
`
	if got := parseCosmicActiveAppID(sample); got != "com.system76.CosmicTerm" {
		t.Fatalf("expected com.system76.CosmicTerm, got %q", got)
	}
}

func TestEditTrackerTailAndFuzzyLearn(t *testing.T) {
	var learned [][2]string
	et := NewEditTracker(func(oldWord, newWord string) {
		learned = append(learned, [2]string{oldWord, newWord})
	}, 20*time.Second)

	et.OnCommit("I am using kuda")
	for i := 0; i < 4; i++ {
		et.OnEvdevKey(evKeyBackspace, 1)
	}
	et.OnEvdevKey(evKeyLeftShift, 1)
	for _, kc := range []uint16{46, 22, 32, 30} { // C U D A
		et.OnEvdevKey(kc, 1)
	}
	et.OnEvdevKey(evKeyLeftShift, 0)
	oldW, newW, ok := et.OnEvdevKey(evKeySpace, 1)
	if !ok || oldW != "kuda" || newW != "CUDA" {
		t.Fatalf("expected tail learn (kuda, CUDA), got (%q, %q, %v)", oldW, newW, ok)
	}

	et.OnCommit("I am using kuda on Linux")
	for i := 0; i < 4; i++ {
		et.OnEvdevKey(evKeyBackspace, 1)
	}
	et.OnEvdevKey(evKeyLeftShift, 1)
	for _, kc := range []uint16{46, 22, 32, 30} { // C U D A
		et.OnEvdevKey(kc, 1)
	}
	et.OnEvdevKey(evKeyLeftShift, 0)
	oldW, newW, ok = et.OnEvdevKey(evKeyEnter, 1)
	if !ok || oldW != "kuda" || newW != "CUDA" {
		t.Fatalf("expected fuzzy learn (kuda, CUDA), got (%q, %q, %v)", oldW, newW, ok)
	}
	if len(learned) != 2 {
		t.Fatalf("expected 2 learned pairs, got %d", len(learned))
	}
}

func TestDiffHeardVsExpected(t *testing.T) {
	pairs := diffHeardVsExpected(
		"I'm pushing VoxStilus to GitHub and the AUR.",
		"I'm pushing vox stylus to get hub and the a you are.",
	)
	got := map[string]string{}
	for _, p := range pairs {
		got[p[0]] = p[1]
	}
	if got["vox stylus"] != "VoxStilus" {
		t.Fatalf("expected vox stylus -> VoxStilus, got %#v", got)
	}
	if got["get hub"] != "GitHub" {
		t.Fatalf("expected get hub -> GitHub, got %#v", got)
	}
	if got["a you are"] != "AUR" {
		t.Fatalf("expected a you are -> AUR, got %#v", got)
	}
}

func TestPCMRMS(t *testing.T) {
	silence := make([]byte, 3200)
	if got := pcmRMS(silence); got != 0 {
		t.Fatalf("expected 0 RMS for silence, got %f", got)
	}
	tone := make([]byte, 200)
	for i := 0; i < len(tone); i += 2 {
		binary.LittleEndian.PutUint16(tone[i:i+2], uint16(16384))
	}
	if got := pcmRMS(tone); math.Abs(got-0.5) > 0.001 {
		t.Fatalf("expected ~0.5 RMS, got %f", got)
	}
}

func TestPCMRingBuffer(t *testing.T) {
	ring := newPCMRingBuffer(10) // 10 bytes capacity (5 16-bit samples)
	ring.Write([]byte{1, 2, 3, 4, 5, 6, 7})
	got := ring.Bytes()
	want := []byte{1, 2, 3, 4, 5, 6}
	if !bytes.Equal(got, want) {
		t.Fatalf("pre-wrap aligned bytes got %v, want %v", got, want)
	}

	// Write 6 more bytes (total 13 written into 10-byte cap: 1..13; oldest whole sample starts at byte 5).
	ring.Write([]byte{8, 9, 10, 11, 12, 13})
	got = ring.Bytes()
	wantWrapOdd := []byte{5, 6, 7, 8, 9, 10, 11, 12}
	if !bytes.Equal(got, wantWrapOdd) {
		t.Fatalf("odd-wrap aligned bytes got %v, want %v", got, wantWrapOdd)
	}

	// Write 1 more byte (total 14 written: bytes 5..14 fit cleanly as 10 aligned bytes).
	ring.Write([]byte{14})
	got = ring.Bytes()
	wantWrapEven := []byte{5, 6, 7, 8, 9, 10, 11, 12, 13, 14}
	if !bytes.Equal(got, wantWrapEven) {
		t.Fatalf("even-wrap aligned bytes got %v, want %v", got, wantWrapEven)
	}

	// Verify oversized write keeps newest aligned window without expanding buffer.
	ring.Write([]byte{21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32})
	got = ring.Bytes()
	wantOver := []byte{23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	if !bytes.Equal(got, wantOver) {
		t.Fatalf("oversized write got %v, want %v", got, wantOver)
	}
}

func TestCustomPhraseDictionaryAndLiterals(t *testing.T) {
	cfg := Config{
		RemoveFillerWords:   true,
		EnableVoiceCommands: true,
		CustomDictionary: map[string]string{
			"adapt us camini dot com": "adeptuscamini.com",
			"camini":                  "Kamini",
			"dollar path":             "$PATH",
		},
	}
	got := processText("Check out Adapt us Kamini dot com? Wait— adapt us camini dot com and dollar path.", cfg)
	// Longest match ("adapt us camini dot com") wins in a single pass without re-triggering "camini".
	want := "Check out Adapt us Kamini dot com? Wait— adeptuscamini.com and $PATH."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCustomHeardToPairs(t *testing.T) {
	pairs := customHeardToPairs("adeptuscamini.com", "[blank_audio] Adapt us camini dot com.")
	if len(pairs) != 1 || pairs[0][0] != "adapt us camini dot com" || pairs[0][1] != "adeptuscamini.com" {
		t.Fatalf("unexpected custom pairs: %#v", pairs)
	}
	if exact := customHeardToPairs("adeptuscamini.com", "adeptuscamini.com."); len(exact) != 0 {
		t.Fatalf("expected no dictionary override on exact lowercase match, got %#v", exact)
	}
	if cased := customHeardToPairs("adeptuscamini.com", "Adeptuscamini.com."); len(cased) != 1 || cased[0][1] != "adeptuscamini.com" {
		t.Fatalf("expected case-forcing rule when Whisper capitalizes URL, got %#v", cased)
	}
}

func TestUInputSequenceAndLayout(t *testing.T) {
	guiSteps := buildPasteKeySteps(false)
	if len(guiSteps) != 4 || guiSteps[0] != (keyAction{uinputKeyLeftCtrl, true}) || guiSteps[3] != (keyAction{uinputKeyLeftCtrl, false}) {
		t.Fatalf("unexpected GUI paste key sequence: %#v", guiSteps)
	}
	termSteps := buildPasteKeySteps(true)
	if len(termSteps) != 6 || termSteps[1] != (keyAction{uinputKeyLeftShift, true}) || termSteps[4] != (keyAction{uinputKeyLeftShift, false}) || termSteps[5] != (keyAction{uinputKeyLeftCtrl, false}) {
		t.Fatalf("unexpected terminal paste key sequence: %#v", termSteps)
	}
	ev := encodeInputEvent(uinputEvKey, uinputKeyLeftCtrl, 1)
	if len(ev) != 24 || binary.LittleEndian.Uint16(ev[16:18]) != uinputEvKey || binary.LittleEndian.Uint16(ev[18:20]) != uinputKeyLeftCtrl || binary.LittleEndian.Uint32(ev[20:24]) != 1 {
		t.Fatalf("unexpected input_event wire bytes: %x", ev)
	}
	setup := encodeUInputSetup("voxstilus-uinput")
	if len(setup) != 92 || !bytes.HasPrefix(setup[8:], []byte("voxstilus-uinput\x00")) {
		t.Fatalf("unexpected uinput_setup buffer")
	}
}

func TestWaylandWireFramingAndMimeFilter(t *testing.T) {
	bind := wlRegistryBindPayload(42, "zwlr_data_control_manager_v1", 2, 7)
	// name(4) + strLen(4) + "zwlr_data_control_manager_v1\0"(29 padded->32) + ver(4) + id(4) = 48
	if len(bind) != 48 {
		t.Fatalf("expected 48-byte bind payload, got %d", len(bind))
	}
	if binary.LittleEndian.Uint32(bind[0:4]) != 42 {
		t.Fatalf("expected global name 42")
	}
	iface, adv, ok := wlParseString(bind[4:])
	if !ok || iface != "zwlr_data_control_manager_v1" || adv != 36 {
		t.Fatalf("wlParseString got (%q, %d, %v)", iface, adv, ok)
	}
	if binary.LittleEndian.Uint32(bind[4+adv:8+adv]) != 2 || binary.LittleEndian.Uint32(bind[8+adv:12+adv]) != 7 {
		t.Fatalf("version/new_id mismatch in bind payload")
	}

	frame := wlBuildFrame(7, 1, []byte{1, 2, 3, 4})
	if binary.LittleEndian.Uint32(frame[0:4]) != 7 || binary.LittleEndian.Uint32(frame[4:8]) != (uint32(12)<<16|1) {
		t.Fatalf("unexpected frame header: %x", frame[:8])
	}

	if isSnapshotableMime("TARGETS") || isSnapshotableMime("SAVE_TARGETS") {
		t.Fatalf("expected X11 meta targets to be skipped during snapshot")
	}
	if !isSnapshotableMime("text/plain;charset=utf-8") || !isSnapshotableMime("image/png") || !isSnapshotableMime("UTF8_STRING") {
		t.Fatalf("expected standard MIME and UTF8_STRING to be snapshotable")
	}

	src := &activeClipSource{
		id:         99,
		byMime:     map[string][]byte{"text/plain;charset=utf-8": []byte("hello")},
		consumedCh: make(chan struct{}),
	}
	if got, ok := src.lookupMime("text/plain; charset=utf-8"); !ok || string(got) != "hello" {
		t.Fatalf("expected fuzzy text/plain mime lookup to succeed, got (%q, %v)", string(got), ok)
	}
	src.markConsumed()
	clip := newWaylandClipboard()
	clip.sources[99] = src
	if !clip.WaitConsumed(99, 50*time.Millisecond) {
		t.Fatalf("expected WaitConsumed to return true after markConsumed")
	}
}

type mockPreviewSink struct {
	events []string
}

func (m *mockPreviewSink) OnPreview(state, text string) {
	m.events = append(m.events, state+":"+text)
}

func TestFeedbackAndPreviewSinkHook(t *testing.T) {
	if len(startChimePCM) == 0 || len(startChimePCM)%2 != 0 || len(stopChimePCM) == 0 || len(stopChimePCM)%2 != 0 {
		t.Fatalf("chime PCM buffers must be non-empty and 2-byte aligned")
	}
	sink := &mockPreviewSink{}
	registerPreviewSink(sink)
	emitPreviewState("recording", "hello")
	if len(sink.events) == 0 || sink.events[len(sink.events)-1] != "recording:hello" {
		t.Fatalf("preview sink did not receive state update: %#v", sink.events)
	}
	if !strings.Contains(webUIHTML, "Talk-n' Type") || strings.Contains(webUIHTML, "Talk-n' Text") {
		t.Fatalf("webUIHTML should use Talk-n' Type brand name cleanly")
	}
}

func TestLiveWaylandClipboardRoundtrip(t *testing.T) {
	if os.Getenv("VOXSTILUS_LIVE_WAYLAND_TEST") != "1" {
		t.Skip("set VOXSTILUS_LIVE_WAYLAND_TEST=1 to run live compositor clipboard test")
	}
	clip := newWaylandClipboard()
	if err := clip.EnsureConnected(); err != nil {
		t.Fatalf("EnsureConnected failed: %v", err)
	}
	defer clip.Close()

	origSnap, err := clip.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	if len(origSnap) > 0 {
		defer func() {
			// Restore original text synchronously via wl-copy so short-lived test process exit doesn't drop offer.
			for _, e := range origSnap {
				if e.Mime == "text/plain;charset=utf-8" || e.Mime == "text/plain" {
					cmd := exec.Command("wl-copy")
					cmd.Stdin = bytes.NewReader(e.Data)
					_ = cmd.Run()
					break
				}
			}
		}()
	}

	const probe = "voxstilus-live-wayland-clipboard-probe"
	srcID, err := clip.SetText(probe)
	if err != nil {
		t.Fatalf("SetText failed: %v", err)
	}
	if clip.WasCancelled(srcID) {
		t.Fatalf("source unexpectedly marked cancelled")
	}

	out, err := exec.Command("wl-paste", "--no-newline").Output()
	if err != nil {
		t.Fatalf("wl-paste failed reading from pure-Go data-control source: %v", err)
	}
	if string(out) != probe {
		t.Fatalf("wl-paste got %q, want %q", string(out), probe)
	}

	// Verify SetEntries restores multi-MIME entries cleanly back onto compositor.
	if len(origSnap) > 0 {
		restSrc, err := clip.SetEntries(origSnap)
		if err != nil {
			t.Fatalf("SetEntries restore failed: %v", err)
		}
		if clip.WasCancelled(restSrc) {
			t.Fatalf("restored source unexpectedly marked cancelled")
		}
		if !clip.WasCancelled(srcID) {
			t.Fatalf("expected temporary probe source %d to be cancelled after restore", srcID)
		}
	}
}
