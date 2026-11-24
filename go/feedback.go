package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"os/exec"
	"sync"
)

// PreviewSink receives internal recording state transitions and live in-memory transcripts
// without stealing Wayland window focus. A future zero-Cgo wlr-layer-shell overlay
// (with keyboard_interactivity: none) can implement PreviewSink and call registerPreviewSink().
type PreviewSink interface {
	OnPreview(state, text string)
}

var (
	previewSinksMu sync.RWMutex
	previewSinks   []PreviewSink
)

func registerPreviewSink(sink PreviewSink) {
	if sink == nil {
		return
	}
	previewSinksMu.Lock()
	previewSinks = append(previewSinks, sink)
	previewSinksMu.Unlock()
}

func emitPreviewState(state, text string) {
	previewSinksMu.RLock()
	sinks := append([]PreviewSink(nil), previewSinks...)
	previewSinksMu.RUnlock()
	for _, s := range sinks {
		s.OnPreview(state, text)
	}
}

// Pre-generated 16kHz s16le mono PCM earcons so start/stop chimes play with zero allocation.
var (
	startChimePCM = buildTwoToneChime(587.33, 880.0, 26, 36) // Crisp ascending D5 -> A5
	stopChimePCM  = buildTwoToneChime(659.25, 440.0, 24, 34) // Gentle descending E5 -> A4
)

func generateTonePCM(freqHz float64, durMs int, peak float64) []byte {
	const sampleRate = 16000
	samples := (sampleRate * durMs) / 1000
	if samples <= 0 {
		return nil
	}
	out := make([]byte, samples*2)
	fadeSamples := samples / 4
	if fadeSamples < 1 {
		fadeSamples = 1
	}
	for i := 0; i < samples; i++ {
		env := 1.0
		if i < fadeSamples {
			env = 0.5 * (1.0 - math.Cos(math.Pi*float64(i)/float64(fadeSamples)))
		} else if i >= samples-fadeSamples {
			rem := samples - 1 - i
			env = 0.5 * (1.0 - math.Cos(math.Pi*float64(rem)/float64(fadeSamples)))
		}
		t := float64(i) / float64(sampleRate)
		val := math.Sin(2.0*math.Pi*freqHz*t) * peak * env
		s16 := int16(math.Round(val * 32767.0))
		binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(s16))
	}
	return out
}

func buildTwoToneChime(freqA, freqB float64, msA, msB int) []byte {
	a := generateTonePCM(freqA, msA, 0.22)
	b := generateTonePCM(freqB, msB, 0.20)
	out := make([]byte, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func playRawChimeAsync(pcm []byte) {
	if len(pcm) == 0 {
		return
	}
	pwCat, err := exec.LookPath("pw-cat")
	if err != nil {
		return
	}
	go func() {
		cmd := exec.Command(pwCat, "--playback", "--raw", "--format=s16", "--rate=16000", "--channels=1", "--latency=20ms", "--media-role=Notification", "-")
		cmd.Stdin = bytes.NewReader(pcm)
		_ = cmd.Run()
	}()
}

func sendNonStealingNotifyAsync(active bool) {
	p, err := exec.LookPath("notify-send")
	if err != nil {
		return
	}
	body := "Recording started (hold key to dictate)"
	if !active {
		body = "Transcribing & pasting..."
	}
	go func() {
		_ = exec.Command(p, "-a", "VoxStilus", "-u", "low", "-t", "900", "-h", "int:transient:1", "VoxStilus", body).Run()
	}()
}

func emitStatusFeedback(cfg Config, active bool) {
	if cfg.AudioFeedback {
		if active {
			playRawChimeAsync(startChimePCM)
		} else {
			playRawChimeAsync(stopChimePCM)
		}
	}
	if cfg.NotifyFeedback {
		sendNonStealingNotifyAsync(active)
	}
}
