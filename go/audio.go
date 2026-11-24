package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	liveChunkBytes      = 3200        // 100ms at 16kHz 16-bit mono
	defaultMaxRingBytes = 32000 * 300 // 5 minutes at 16kHz s16le mono (~9.6 MB fixed cap)
)

// pcmRingBuffer stores PCM audio in a pre-allocated fixed-size circular byte slice so long sessions
// maintain a completely flat, bounded memory footprint without dynamic slice growth.
type pcmRingBuffer struct {
	data     []byte
	writePos int
	size     int
}

func newPCMRingBuffer(capacity int) *pcmRingBuffer {
	if capacity < 2 {
		capacity = defaultMaxRingBytes
	}
	if capacity%2 != 0 {
		capacity--
	}
	return &pcmRingBuffer{data: make([]byte, capacity)}
}

func (b *pcmRingBuffer) Reset() {
	b.writePos = 0
	b.size = 0
}

func (b *pcmRingBuffer) Write(p []byte) {
	capLen := len(b.data)
	if capLen == 0 || len(p) == 0 {
		return
	}
	if len(p) >= capLen {
		// Retain the newest full ring window, aligned to the incoming 2-byte frame parity.
		tail := p[len(p)-capLen:]
		if len(p)%2 != 0 && capLen > 2 {
			tail = p[len(p)-(capLen-1):]
			copy(b.data, tail)
			b.writePos = capLen - 1
			b.size = capLen - 1
			return
		}
		copy(b.data, tail)
		b.writePos = 0
		b.size = capLen
		return
	}
	first := capLen - b.writePos
	if first >= len(p) {
		copy(b.data[b.writePos:], p)
	} else {
		copy(b.data[b.writePos:], p[:first])
		copy(b.data[0:], p[first:])
	}
	b.writePos = (b.writePos + len(p)) % capLen
	b.size += len(p)
	if b.size > capLen {
		b.size = capLen
	}
}

// Bytes returns a sample-aligned (even-length) linearized copy of the audio currently in the ring.
func (b *pcmRingBuffer) Bytes() []byte {
	if b.size < 2 || len(b.data) == 0 {
		return nil
	}
	capLen := len(b.data)
	var start, usable int
	if b.size < capLen {
		start = 0
		usable = b.size &^ 1
	} else if b.writePos%2 == 0 {
		start = b.writePos
		usable = capLen
	} else {
		start = (b.writePos + 1) % capLen
		usable = capLen - 2
	}
	if usable <= 0 {
		return nil
	}
	out := make([]byte, usable)
	if start+usable <= capLen {
		copy(out, b.data[start:start+usable])
	} else {
		first := capLen - start
		copy(out[:first], b.data[start:])
		copy(out[first:], b.data[:usable-first])
	}
	return out
}

// AudioRecorder captures 16kHz mono s16le raw PCM by spawning pw-record --raw into a fixed ring buffer.
type AudioRecorder struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	ring   *pcmRingBuffer
	doneCh chan struct{}
}

func (r *AudioRecorder) Start(micDevice string, onChunk func([]byte)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		return nil
	}
	if r.ring == nil {
		r.ring = newPCMRingBuffer(defaultMaxRingBytes)
	} else {
		r.ring.Reset()
	}
	args := []string{"--raw", "--latency=40ms", "--format=s16", "--rate=16000", "--channels=1"}
	if micDevice != "" {
		args = append(args, "--target="+micDevice)
	}
	args = append(args, "-")
	cmd := exec.Command("pw-record", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pw-record stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pw-record: %w", err)
	}
	r.cmd = cmd
	r.doneCh = make(chan struct{})
	go func() {
		defer close(r.doneCh)
		tmp := make([]byte, liveChunkBytes)
		var pending []byte
		for {
			n, err := stdout.Read(tmp)
			if n > 0 {
				r.mu.Lock()
				r.ring.Write(tmp[:n])
				r.mu.Unlock()
				if onChunk != nil {
					pending = append(pending, tmp[:n]...)
					for len(pending) >= liveChunkBytes {
						chunkCopy := append([]byte(nil), pending[:liveChunkBytes]...)
						pending = pending[liveChunkBytes:]
						onChunk(chunkCopy)
					}
				}
			}
			if err != nil {
				if onChunk != nil && len(pending) >= 2 {
					aligned := len(pending) &^ 1
					if aligned > 0 {
						chunkCopy := append([]byte(nil), pending[:aligned]...)
						onChunk(chunkCopy)
					}
				}
				return
			}
		}
	}()
	return nil
}

// Snapshot returns a frame-aligned copy of PCM bytes captured so far in the ring buffer.
func (r *AudioRecorder) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ring == nil {
		return nil
	}
	return r.ring.Bytes()
}

func (r *AudioRecorder) Stop() []byte {
	r.mu.Lock()
	cmd := r.cmd
	done := r.doneCh
	r.cmd = nil
	r.mu.Unlock()

	if cmd == nil {
		return nil
	}
	if cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGINT)
		if done != nil {
			select {
			case <-done:
			case <-time.After(150 * time.Millisecond):
				_ = cmd.Process.Kill()
			}
		} else {
			_ = cmd.Process.Kill()
		}
	}
	_ = cmd.Wait()
	if done != nil {
		<-done
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ring == nil {
		return nil
	}
	return r.ring.Bytes()
}

// pcmRMS computes normalized RMS amplitude [0.0, 1.0] for 16-bit little-endian PCM.
func pcmRMS(pcm []byte) float64 {
	samples := len(pcm) / 2
	if samples == 0 {
		return 0
	}
	var sumSq float64
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int16(binary.LittleEndian.Uint16(pcm[i : i+2]))
		f := float64(v)
		sumSq += f * f
	}
	return math.Sqrt(sumSq/float64(samples)) / 32768.0
}

// encodeWAV wraps raw 16kHz mono s16le PCM bytes in a standard 44-byte RIFF/WAV header.
func encodeWAV(pcm []byte) []byte {
	const (
		sampleRate = 16000
		channels   = 1
		bitsPer    = 16
	)
	byteRate := uint32(sampleRate * channels * bitsPer / 8)
	blockAlign := uint16(channels * bitsPer / 8)
	dataLen := uint32(len(pcm))

	var out bytes.Buffer
	out.Grow(44 + len(pcm))
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(36+dataLen))
	out.WriteString("WAVEfmt ")
	_ = binary.Write(&out, binary.LittleEndian, uint32(16))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(&out, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&out, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&out, binary.LittleEndian, byteRate)
	_ = binary.Write(&out, binary.LittleEndian, blockAlign)
	_ = binary.Write(&out, binary.LittleEndian, uint16(bitsPer))
	out.WriteString("data")
	_ = binary.Write(&out, binary.LittleEndian, dataLen)
	out.Write(pcm)
	return out.Bytes()
}

var _ = io.EOF
var wpctlVolRe = regexp.MustCompile(`Volume:\s*([0-9]+(?:\.[0-9]+)?)`)

type VolumeDucker struct {
	mu          sync.Mutex
	savedVol    *float64
	duckTo      float64
	pausedMedia bool
}

func NewVolumeDucker(duckTo float64) *VolumeDucker {
	return &VolumeDucker{duckTo: duckTo}
}

func (d *VolumeDucker) Duck(enabled bool) {
	if !enabled {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.pausedMedia {
		if pPath, err := exec.LookPath("playerctl"); err == nil {
			if out, err := exec.Command(pPath, "status").Output(); err == nil {
				if strings.EqualFold(strings.TrimSpace(string(out)), "Playing") {
					if exec.Command(pPath, "pause").Run() == nil {
						d.pausedMedia = true
					}
				}
			}
		}
	}

	if d.savedVol != nil {
		return
	}
	out, err := exec.Command("wpctl", "get-volume", "@DEFAULT_AUDIO_SINK@").Output()
	if err != nil {
		return
	}
	m := wpctlVolRe.FindStringSubmatch(string(out))
	if len(m) < 2 {
		return
	}
	cur, err := strconv.ParseFloat(m[1], 64)
	if err != nil || cur <= d.duckTo {
		return
	}
	if err := exec.Command("wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", fmt.Sprintf("%.2f", d.duckTo)).Run(); err == nil {
		d.savedVol = &cur
	}
}

func (d *VolumeDucker) Restore() {
	d.mu.Lock()
	vol := d.savedVol
	d.savedVol = nil
	resume := d.pausedMedia
	d.pausedMedia = false
	d.mu.Unlock()

	if resume {
		if pPath, err := exec.LookPath("playerctl"); err == nil {
			_ = exec.Command(pPath, "play").Run()
		}
	}
	if vol != nil {
		_ = exec.Command("wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", strings.TrimSpace(fmt.Sprintf("%.2f", *vol))).Run()
	}
}
