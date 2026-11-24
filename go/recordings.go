package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type RecordingEntry struct {
	ID          string  `json:"id"`
	Path        string  `json:"path"`
	CreatedAt   string  `json:"created_at"`
	DurationSec float64 `json:"duration_sec"`
	Text        string  `json:"text"`
}

var recordingsMu sync.Mutex

func dataHomeDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "talk-to-type")
}

func saveSessionRecording(pcm []byte, text string, durationSec float64, retentionHours int) {
	if len(pcm) == 0 {
		return
	}
	recordingsMu.Lock()
	defer recordingsMu.Unlock()

	dir := filepath.Join(dataHomeDir(), "recordings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}

	now := time.Now().UTC()
	stamp := now.Format("20060102-150405")
	wavPath := filepath.Join(dir, stamp+".wav")
	if err := os.WriteFile(wavPath, encodeWAV(pcm), 0o644); err != nil {
		return
	}

	indexPath := filepath.Join(dir, "index.json")
	var entries []RecordingEntry
	if data, err := os.ReadFile(indexPath); err == nil {
		_ = json.Unmarshal(data, &entries)
	}

	cutoff := now.Add(-time.Duration(retentionHours) * time.Hour)
	kept := make([]RecordingEntry, 0, len(entries)+1)
	kept = append(kept, RecordingEntry{
		ID:          stamp,
		Path:        wavPath,
		CreatedAt:   now.Format(time.RFC3339),
		DurationSec: durationSec,
		Text:        text,
	})
	for _, e := range entries {
		info, err := os.Stat(e.Path)
		if err != nil {
			continue
		}
		if retentionHours > 0 && info.ModTime().Before(cutoff) {
			_ = os.Remove(e.Path)
			continue
		}
		kept = append(kept, e)
	}
	if out, err := json.MarshalIndent(kept, "", "  "); err == nil {
		_ = os.WriteFile(indexPath, append(out, '\n'), 0o644)
	}
}

func appendDeliveryFailure(text, reason string) {
	if text == "" {
		return
	}
	recordingsMu.Lock()
	defer recordingsMu.Unlock()

	dir := dataHomeDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	row := map[string]any{
		"ts":     float64(time.Now().UnixNano()) / 1e9,
		"text":   text,
		"reason": reason,
	}
	line, err := json.Marshal(row)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "delivery_history.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}
