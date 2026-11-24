package main

import (
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--settings":
			os.Exit(openWebUI(""))
		case "--train":
			os.Exit(openWebUI("?tab=train"))
		}
	}

	log.SetFlags(log.Ltime)
	cfg := loadConfig()
	log.Printf("VoxStilus (Pure Go, CGO_ENABLED=0, headless) starting | key=%s mode=%s stt=%s", cfg.HoldKey, cfg.TriggerMode, cfg.STTEndpoint)

	if err := globalUInput.Init(); err != nil {
		log.Printf("[INPUT] direct /dev/uinput note (%v); ydotool/wtype fallback active", err)
	} else {
		log.Printf("[INPUT] persistent /dev/uinput virtual keyboard ready")
	}
	if err := globalClipboard.EnsureConnected(); err != nil {
		log.Printf("[CLIPBOARD] wayland data-control note (%v); wl-copy fallback active", err)
	} else {
		log.Printf("[CLIPBOARD] pure-Go Wayland data-control service active")
	}

	go prewarmSTT(cfg)
	go func() {
		if err := startWebUI("127.0.0.1:18081"); err != nil {
			log.Printf("[UI] server stopped: %v", err)
		}
	}()

	recorder := &AudioRecorder{}
	ducker := NewVolumeDucker(0.40)

	var sessionMu sync.Mutex
	var recording bool
	var sessionStart time.Time
	var lastCommittedRunes int
	var liveTranscriptBuf strings.Builder
	liveTranscriptBuf.Grow(4096)
	var stopPreview chan struct{}
	var previewDone chan struct{}

	onPress := func() {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		if recording {
			return
		}
		cancelPendingClipboardRestore()
		cfg = loadConfig()
		recording = true
		sessionStart = time.Now()
		liveTranscriptBuf.Reset()
		emitStatusFeedback(cfg, true)
		ducker.Duck(cfg.AutoDuckMedia)

		if err := recorder.Start(cfg.MicDevice, nil); err != nil {
			log.Printf("record start error: %v", err)
			ducker.Restore()
			recording = false
			return
		}

		// Accumulate live partials strictly in RAM — never touch window focus or inject keys mid-speech.
		curCfg := cfg
		sp := make(chan struct{})
		pd := make(chan struct{})
		stopPreview = sp
		previewDone = pd
		emitPreviewState("recording", "")
		log.Printf("[LISTENING] speak now (in-memory preview only — no focus steal or external typing)")

		go func() {
			defer close(pd)
			ticker := time.NewTicker(450 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-sp:
					return
				case <-ticker.C:
					snap := recorder.Snapshot()
					if len(snap) < 8000 {
						continue
					}
					raw, err := transcribeWAV(encodeWAV(snap), curCfg)
					if err != nil {
						continue
					}
					preview := processText(raw, curCfg)
					if preview == "" {
						continue
					}
					sessionMu.Lock()
					changed := preview != liveTranscriptBuf.String()
					if changed {
						liveTranscriptBuf.Reset()
						liveTranscriptBuf.WriteString(preview)
					}
					sessionMu.Unlock()
					if changed {
						log.Printf("[INTERNAL PREVIEW] %q", preview)
						emitPreviewState("recording", preview)
					}
				}
			}
		}()
	}

	onRelease := func() {
		sessionMu.Lock()
		if !recording {
			sessionMu.Unlock()
			return
		}
		recording = false
		sp := stopPreview
		pd := previewDone
		stopPreview = nil
		previewDone = nil
		if sp != nil {
			close(sp)
		}
		pcm := recorder.Stop()
		ducker.Restore()
		curCfg := cfg
		durSec := time.Since(sessionStart).Seconds()
		prevRunes := lastCommittedRunes
		sessionMu.Unlock()

		emitStatusFeedback(curCfg, false)

		if pd != nil {
			<-pd
		}
		emitPreviewState("transcribing", "")

		if len(pcm) < 3200 { // < 100ms
			emitPreviewState("idle", "")
			log.Printf("[IDLE] too short (%d bytes)", len(pcm))
			return
		}

		log.Printf("[TRANSCRIBING] %d bytes PCM...", len(pcm))
		rawText, err := transcribeWAV(encodeWAV(pcm), curCfg)
		if err != nil {
			emitPreviewState("idle", "")
			log.Printf("[ERROR] STT failed: %v", err)
			return
		}

		if isScratchCommand(rawText) {
			if prevRunes > 0 {
				if err := sendBackspaces(prevRunes); err != nil {
					log.Printf("[ERROR] scratch erase failed: %v", err)
				} else {
					sessionMu.Lock()
					lastCommittedRunes = 0
					sessionMu.Unlock()
				}
			}
			emitPreviewState("idle", "")
			return
		}

		finalText := refineWithJanus(processText(rawText, curCfg), curCfg)
		if finalText == "" {
			emitPreviewState("idle", "")
			log.Printf("[IDLE] empty transcript")
			return
		}

		sessionMu.Lock()
		liveTranscriptBuf.Reset()
		liveTranscriptBuf.WriteString(finalText)
		sessionMu.Unlock()

		emitPreviewState("idle", finalText)
		log.Printf("[PASTING] %q", finalText)
		if err := atomicPasteWithPreserve(finalText, curCfg.PreserveClipboard); err != nil {
			log.Printf("[ERROR] paste failed: %v", err)
			appendDeliveryFailure(finalText, "PASTE_FAIL")
		} else {
			sessionMu.Lock()
			lastCommittedRunes = len([]rune(finalText))
			sessionMu.Unlock()
			globalEditTracker.OnCommit(finalText)
		}
		go saveSessionRecording(pcm, finalText, durSec, curCfg.RecordingRetentionHours)
	}

	stopCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	listener := NewKeyListener(cfg.HoldKey, cfg.TriggerMode, onPress, onRelease)
	go listener.Run(stopCh)

	<-sigCh
	close(stopCh)
	ducker.Restore()
	stopSTTServer()
	globalClipboard.Close()
	globalUInput.Close()
	log.Printf("VoxStilus exiting cleanly")
}
