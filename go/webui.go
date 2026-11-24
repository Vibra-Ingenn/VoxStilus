package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var calibrationPhrases = []string{
	"Alabama, Mississippi, and Minnesota.",
	"Massachusetts, Connecticut, and Illinois.",
	"Hawaii, Arkansas, and Albuquerque.",
	"Pennsylvania, Tennessee, and Oklahoma.",
	"Wyoming, Wisconsin, and Oregon.",
	"Their code is over there, and they're testing it.",
	"You're right about your Linux setup.",
	"I'm pushing VoxStilus to GitHub and the AUR.",
	"Running CUDA on Wayland with COSMIC and Niri.",
	"Check systemd and Wayland in the terminal.",
	"Janus, Vibra-Ingenn, and Talk-n' Type.",
	"Open the settings file and save config dot json.",
	"Use camel case user ID and snake case max retries.",
	"Anyway, basically, I just wanted to get that working right now.",
	"So instead of waiting until the end, keep live streaming internal.",
	"Wednesday, February, and thoroughly.",
	"Specifically, statistics, and architecture.",
	"Entrepreneurship and bureaucracy.",
	"Parallel processing and asynchronous callbacks.",
	"She sells seashells, crisp speech, and clear pronunciation.",
}

var trainerTokenRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9'_-]*`)

// diffHeardVsExpected aligns tokens between heard and expected using LCS and extracts
// short misheard replacement spans [(heard, expected)].
func diffHeardVsExpected(expected, heard string) [][2]string {
	expToks := trainerTokenRe.FindAllString(expected, -1)
	hrdToks := trainerTokenRe.FindAllString(heard, -1)
	if len(expToks) == 0 || len(hrdToks) == 0 {
		return nil
	}
	m, n := len(hrdToks), len(expToks)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		hi := strings.ToLower(hrdToks[i])
		for j := n - 1; j >= 0; j-- {
			if hi == strings.ToLower(expToks[j]) {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	// Collect matched index pairs
	type match struct{ i, j int }
	var matches []match
	i, j := 0, 0
	for i < m && j < n {
		if strings.ToLower(hrdToks[i]) == strings.ToLower(expToks[j]) {
			matches = append(matches, match{i, j})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	matches = append(matches, match{m, n})

	var pairs [][2]string
	prevI, prevJ := 0, 0
	for _, cur := range matches {
		if cur.i > prevI && cur.j > prevJ {
			di := cur.i - prevI
			dj := cur.j - prevJ
			if di <= 4 && dj <= 3 {
				hrdSpan := strings.TrimSpace(strings.Join(hrdToks[prevI:cur.i], " "))
				expSpan := strings.TrimSpace(strings.Join(expToks[prevJ:cur.j], " "))
				if hrdSpan != "" && expSpan != "" && !strings.EqualFold(hrdSpan, expSpan) {
					pairs = append(pairs, [2]string{strings.ToLower(hrdSpan), expSpan})
				}
			}
		}
		prevI = cur.i + 1
		prevJ = cur.j + 1
	}
	return pairs
}

func customHeardToPairs(expected, heard string) [][2]string {
	exp := strings.TrimSpace(expected)
	if exp == "" {
		return nil
	}
	cleaned := strings.TrimSpace(soundTagRe.ReplaceAllString(heard, " "))
	cleaned = multiSpaceRe.ReplaceAllString(cleaned, " ")
	if !strings.HasSuffix(exp, ".") && !strings.HasSuffix(exp, "!") && !strings.HasSuffix(exp, "?") && !strings.HasSuffix(exp, ",") {
		cleaned = strings.TrimRight(cleaned, ".,!?;:\"'")
	}
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" || cleaned == exp {
		return nil
	}
	return [][2]string{{strings.ToLower(cleaned), exp}}
}

type previewEvent struct {
	State string `json:"state"`
	Text  string `json:"text"`
}

var (
	sseMu      sync.Mutex
	sseClients = map[chan string]struct{}{}
	lastEvt    = previewEvent{State: "idle", Text: ""}
)

type webUIPreviewSink struct{}

func (webUIPreviewSink) OnPreview(state, text string) {
	publishInternalPreview(state, text)
}

func init() {
	registerPreviewSink(webUIPreviewSink{})
}

func publishInternalPreview(state, text string) {
	evt := previewEvent{State: state, Text: text}
	raw, err := json.Marshal(evt)
	if err != nil {
		return
	}
	msg := string(raw)
	sseMu.Lock()
	lastEvt = evt
	for ch := range sseClients {
		select {
		case ch <- msg:
		default:
		}
	}
	sseMu.Unlock()
}

func saveFullConfig(cfg Config) error {
	path := configFilePath()
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &doc)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	var updated map[string]any
	if err := json.Unmarshal(b, &updated); err != nil {
		return err
	}
	for k, v := range updated {
		doc[k] = v
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func listMicSources() []string {
	var sources []string
	if p, err := exec.LookPath("pactl"); err == nil {
		if out, err := exec.Command(p, "list", "short", "sources").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 && !strings.Contains(fields[1], ".monitor") {
					sources = append(sources, fields[1])
				}
			}
		}
	}
	return sources
}

func startWebUI(addr string) error {
	var trainMu sync.Mutex
	var trainRec AudioRecorder

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(webUIHTML))
	})

	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch := make(chan string, 16)
		sseMu.Lock()
		sseClients[ch] = struct{}{}
		initData, _ := json.Marshal(lastEvt)
		sseMu.Unlock()

		defer func() {
			sseMu.Lock()
			delete(sseClients, ch)
			sseMu.Unlock()
		}()

		fmt.Fprintf(w, "data: %s\n\n", initData)
		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case msg := <-ch:
				fmt.Fprintf(w, "data: %s\n\n", msg)
				flusher.Flush()
			}
		}
	})

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"config":  loadConfig(),
				"mics":    listMicSources(),
				"phrases": calibrationPhrases,
			})
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Config  Config `json:"config"`
			Restart bool   `json:"restart"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := saveFullConfig(req.Config); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if req.Restart {
			go func() {
				time.Sleep(150 * time.Millisecond)
				_ = exec.Command("systemctl", "--user", "restart", "talk-to-type.service").Run()
			}()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/train/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		trainMu.Lock()
		defer trainMu.Unlock()
		cfg := loadConfig()
		if err := trainRec.Start(cfg.MicDevice, nil); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/train/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Expected string `json:"expected"`
			Mode     string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		trainMu.Lock()
		pcm := trainRec.Stop()
		trainMu.Unlock()

		cfg := loadConfig()
		heard, err := transcribeWAV(encodeWAV(pcm), cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var learned [][2]string
		if req.Mode == "custom" {
			learned = customHeardToPairs(req.Expected, heard)
		} else {
			learned = diffHeardVsExpected(req.Expected, heard)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"heard":   heard,
			"learned": learned,
		})
	})

	mux.HandleFunc("/api/train/save", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Pairs       [][2]string `json:"pairs"`
			PromptTerms []string    `json:"prompt_terms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfg := loadConfig()
		if cfg.CustomDictionary == nil {
			cfg.CustomDictionary = map[string]string{}
		}
		existingPrompt := []string{}
		seen := map[string]bool{}
		for _, p := range strings.Split(cfg.CustomWordsPrompt, ",") {
			t := strings.TrimSpace(p)
			if t != "" {
				existingPrompt = append(existingPrompt, t)
				seen[strings.ToLower(t)] = true
			}
		}
		added := 0
		for _, pair := range req.Pairs {
			src := strings.ToLower(strings.TrimSpace(pair[0]))
			dst := strings.TrimSpace(pair[1])
			if src != "" && dst != "" {
				if cfg.CustomDictionary[src] != dst {
					cfg.CustomDictionary[src] = dst
					added++
				}
				if !seen[strings.ToLower(dst)] {
					existingPrompt = append(existingPrompt, dst)
					seen[strings.ToLower(dst)] = true
				}
			}
		}
		for _, rawTerm := range req.PromptTerms {
			term := strings.TrimSpace(rawTerm)
			if term != "" && !seen[strings.ToLower(term)] {
				existingPrompt = append(existingPrompt, term)
				seen[strings.ToLower(term)] = true
			}
		}
		cfg.CustomWordsPrompt = strings.Join(existingPrompt, ", ")
		if err := saveFullConfig(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		go prewarmSTT(cfg)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "added": added})
	})

	log.Printf("[UI] Pure-Go Settings & Live Preview UI listening on http://%s", addr)
	return http.ListenAndServe(addr, mux)
}

func openWebUI(query string) int {
	const addr = "127.0.0.1:18081"
	startedLocal := false
	if !isTCPOpen(addr) {
		startedLocal = true
		go func() {
			if err := startWebUI(addr); err != nil {
				log.Printf("[UI] server error: %v", err)
			}
		}()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && !isTCPOpen(addr) {
			time.Sleep(50 * time.Millisecond)
		}
	}

	targetURL := "http://" + addr + "/" + query
	var cmd *exec.Cmd
	for _, browser := range []string{"brave-origin", "brave", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(browser); err == nil {
			cmd = exec.Command(p, "--app="+targetURL)
			cmd.Env = append(os.Environ(), "GSETTINGS_BACKEND=memory")
			break
		}
	}
	if cmd == nil {
		if p, err := exec.LookPath("xdg-open"); err == nil {
			cmd = exec.Command(p, targetURL)
		}
	}
	if cmd == nil {
		fmt.Printf("Open VoxStilus UI in your browser: %s\n", targetURL)
		if startedLocal {
			select {}
		}
		return 0
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open UI (%s): %v\n", targetURL, err)
		return 1
	}
	if startedLocal {
		_ = cmd.Wait()
	}
	return 0
}

const webUIHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>VoxStilus (Talk-n' Type)</title>
<link rel="icon" type="image/svg+xml" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'%3E%3Crect width='64' height='64' rx='14' fill='%23111112' stroke='%237170ff' stroke-width='3'/%3E%3Cpath d='M14 34h6l4-14 6 26 6-22 4 10h10' fill='none' stroke='%2300f3ff' stroke-width='4' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E">
<style>
  :root { --bg:#111112; --card:#161718; --border:#272727; --accent:#7170ff; --green:#27a644; --text:#f5f6f6; --muted:#8a8f98; }
  * { box-sizing:border-box; font-family:Inter,system-ui,sans-serif; }
  body { margin:0; background:var(--bg); color:var(--text); padding:20px; }
  .wrap { max-width:760px; margin:0 auto; }
  .banner { position:relative; overflow:hidden; background:var(--card); border:1px solid var(--border); border-bottom:2px solid var(--accent); border-radius:10px; padding:16px; text-align:center; margin-bottom:12px; }
  .banner.glitching::after {
    content:""; position:absolute; inset:0; pointer-events:none;
    background:repeating-linear-gradient(0deg, rgba(0,243,255,.06) 0 1px, transparent 1px 4px);
    animation:banner-scan .58s steps(1,end);
  }
  @keyframes banner-scan {
    0%, 100% { opacity:0; }
    15% { opacity:.9; transform:translateY(-2px); }
    45% { opacity:.6; transform:translateY(2px); }
    75% { opacity:.85; transform:translateY(-1px); }
  }
  /* Two-layer slice + RGB chromatic tear while swapping between real brand names */
  .banner h1 { position:relative; margin:0; font-size:24px; letter-spacing:.5px; display:inline-block; cursor:pointer; user-select:none; filter:drop-shadow(0 0 10px rgba(200,200,200,.18)); transition:filter .3s ease; }
  .banner h1:hover { filter:drop-shadow(0 0 18px rgba(113,112,255,.45)); }
  .banner h1::before, .banner h1::after {
    content:attr(data-text); position:absolute; left:0; top:0; width:100%; height:100%;
    pointer-events:none; opacity:0;
  }
  .banner h1.brand-swap { animation:brand-swap .58s steps(1,end); }
  .banner h1.brand-swap::before { color:#00f3ff; opacity:.92; animation:glitch-top .58s steps(1,end); }
  .banner h1.brand-swap::after  { color:#ff0055; opacity:.88; animation:glitch-bot .58s steps(1,end); }
  @keyframes brand-swap {
    0%   { transform:translate(0,0);               filter:drop-shadow(0 0 10px rgba(200,200,200,.18)); }
    12%  { transform:translate(-4px,1px) skewX(-6deg); filter:drop-shadow(-4px 0 #00f3ff) drop-shadow(4px 0 #ff0055); }
    26%  { transform:translate(5px,-1px) skewX(5deg);  filter:drop-shadow(4px 0 #00f3ff) drop-shadow(-4px 0 #ff0055); }
    42%  { transform:translate(-6px,0) skewX(-8deg);   filter:drop-shadow(-3px 0 var(--green)) drop-shadow(5px 0 #ff0055); }
    56%  { transform:translate(4px,1px) skewX(4deg);   filter:drop-shadow(3px 0 #00f3ff) drop-shadow(-3px 0 var(--accent)); }
    72%  { transform:translate(-3px,-1px) skewX(-3deg);filter:drop-shadow(-2px 0 #00f3ff) drop-shadow(2px 0 #ff0055); }
    86%  { transform:translate(2px,0) skewX(2deg);     filter:drop-shadow(-1px 0 var(--accent)); }
    100% { transform:translate(0,0);               filter:drop-shadow(0 0 10px rgba(200,200,200,.18)); }
  }
  @keyframes glitch-top {
    0%, 100% { clip-path:inset(0 0 100% 0); transform:translate(0,0); opacity:0; }
    12% { clip-path:inset(8% 0 58% 0);  transform:translate(-9px,-2px); opacity:.95; }
    26% { clip-path:inset(42% 0 28% 0); transform:translate(8px,1px);   opacity:.9; }
    42% { clip-path:inset(14% 0 48% 0); transform:translate(-11px,0);   opacity:.95; }
    56% { clip-path:inset(64% 0 8% 0);  transform:translate(7px,-1px);  opacity:.85; }
    72% { clip-path:inset(26% 0 46% 0); transform:translate(-5px,1px);  opacity:.75; }
    86% { clip-path:inset(5% 0 74% 0);  transform:translate(3px,0);     opacity:.5; }
  }
  @keyframes glitch-bot {
    0%, 100% { clip-path:inset(100% 0 0 0); transform:translate(0,0); opacity:0; }
    12% { clip-path:inset(54% 0 12% 0); transform:translate(9px,2px);   opacity:.9; }
    26% { clip-path:inset(18% 0 52% 0); transform:translate(-8px,-1px); opacity:.95; }
    42% { clip-path:inset(62% 0 6% 0);  transform:translate(11px,1px);  opacity:.9; }
    56% { clip-path:inset(22% 0 44% 0); transform:translate(-7px,2px);  opacity:.85; }
    72% { clip-path:inset(58% 0 18% 0); transform:translate(5px,-1px);  opacity:.75; }
    86% { clip-path:inset(72% 0 8% 0);  transform:translate(-3px,0);    opacity:.5; }
  }
  @media (prefers-reduced-motion: reduce) {
    .banner h1.brand-swap { animation:brand-fade .4s ease-in-out; }
    .banner h1.brand-swap::before, .banner h1.brand-swap::after, .banner.glitching::after { animation:none; opacity:0; }
    @keyframes brand-fade { 50% { opacity:.35; } }
  }
  .banner p { margin:4px 0 0; color:var(--muted); font-size:12px; }
  .preview { background:var(--card); border:1px solid var(--border); border-radius:10px; padding:12px 14px; margin-bottom:14px; display:flex; align-items:center; gap:10px; }
  .dot { width:12px; height:12px; border-radius:50%; background:#666; flex:none; }
  .dot.recording { background:#e02020; box-shadow:0 0 8px #e02020; }
  .dot.transcribing { background:#e0a020; }
  .preview-text { font-size:13px; color:var(--text); min-height:18px; }
  .tabs { display:flex; gap:6px; margin-bottom:10px; }
  .tab { background:var(--bg); color:var(--muted); border:1px solid var(--border); padding:8px 14px; border-radius:8px; cursor:pointer; font-size:13px; }
  .tab.active { background:var(--card); color:var(--text); border-color:#3a3a3a; }
  .panel { display:none; background:var(--card); border:1px solid var(--border); border-radius:10px; padding:16px; }
  .panel.active { display:block; }
  label { display:block; font-size:12px; color:var(--muted); margin:10px 0 4px; }
  input[type=text], input[type=number], select, textarea { width:100%; background:var(--bg); color:var(--text); border:1px solid #2c2c2c; border-radius:6px; padding:8px; font-size:13px; }
  textarea { min-height:78px; font-family:monospace; }
  .chk { display:flex; align-items:center; gap:8px; margin:8px 0; color:var(--text); font-size:13px; }
  .row { display:flex; gap:10px; align-items:center; margin-top:14px; }
  button { background:var(--card); color:var(--text); border:1px solid #2c2c2c; border-radius:6px; padding:8px 14px; cursor:pointer; font-weight:500; }
  button.primary { background:var(--accent); border-color:var(--accent); color:#fff; font-weight:600; }
  .status { font-size:12px; color:var(--green); margin-top:10px; min-height:16px; }
  .phrase { font-size:18px; font-weight:600; margin:10px 0; }
</style>
</head>
<body>
<div class="wrap">
  <div class="banner">
    <h1 id="brandTitle" data-text="VoxStilus">VoxStilus</h1>
    <p>Pure-Go Wayland Dictation &bull; Internal Preview &amp; Atomic Clipboard Paste</p>
  </div>
  <div class="preview">
    <div id="dot" class="dot"></div>
    <div id="previewText" class="preview-text">Idle — hold Right Alt to dictate</div>
  </div>
  <div class="tabs">
    <button class="tab active" onclick="showTab('input',this)">Input &amp; Audio</button>
    <button class="tab" onclick="showTab('cleanup',this)">Cleanup &amp; Dictionary</button>
    <button class="tab" onclick="showTab('models',this)">Models &amp; Prompt</button>
    <button class="tab" onclick="showTab('train',this)">Train My Voice</button>
  </div>

  <div id="tab-input" class="panel active">
    <label>Trigger Mode</label>
    <select id="trigger_mode"><option value="hold">Hold to Talk</option><option value="toggle">Tap to Toggle</option></select>
    <label>Trigger Key (evdev name)</label>
    <input type="text" id="hold_key">
    <label>Microphone Device (empty = System Default)</label>
    <select id="mic_device"><option value="">System Default</option></select>
    <label>Sentence Pause (ms)</label>
    <input type="number" id="sentence_pause_ms">
    <label>Recording Retention (hours)</label>
    <input type="number" id="recording_retention_hours">
    <label class="chk"><input type="checkbox" id="auto_duck_media"> Auto-duck speakers (40%) &amp; pause media while recording</label>
    <label class="chk"><input type="checkbox" id="preserve_clipboard"> Preserve existing clipboard (save before paste &amp; restore after)</label>
    <label class="chk"><input type="checkbox" id="audio_feedback"> Play subtle audio chime on recording start/stop (zero focus steal)</label>
    <label class="chk"><input type="checkbox" id="notify_feedback"> Show transient desktop notification on recording start/stop</label>
  </div>

  <div id="tab-cleanup" class="panel">
    <label class="chk"><input type="checkbox" id="remove_filler_words"> Remove filler words (um, uh) &amp; word stutters</label>
    <label class="chk"><input type="checkbox" id="enable_voice_commands"> Enable voice commands &amp; Code Mode (camel/snake case, new line, scratch that)</label>
    <label>Custom Dictionary (spoken = replacement, one per line)</label>
    <textarea id="custom_dictionary"></textarea>
    <label>Voice Snippets (trigger = expansion, one per line)</label>
    <textarea id="voice_snippets"></textarea>
    <label class="chk"><input type="checkbox" id="janus_enabled"> Enable local AI grammar cleanup (Recommended: Janus API Gateway)</label>
    <label>Chat Completions Endpoint (Janus default :8080, or Ollama :11434/v1/chat/completions)</label>
    <input type="text" id="janus_url" placeholder="http://127.0.0.1:8080/v1/chat/completions">
    <label>Model Name (optional with Janus; set for Ollama / LM Studio)</label>
    <input type="text" id="janus_model" placeholder="leave blank for Janus auto-routing, e.g. llama3.2">
  </div>

  <div id="tab-models" class="panel">
    <label>STT HTTP Endpoint</label>
    <input type="text" id="stt_endpoint">
    <label>Whisper Model Path (.bin)</label>
    <input type="text" id="whisper_model_path">
    <label>Custom Words Prompt (comma-separated)</label>
    <textarea id="custom_words_prompt"></textarea>
  </div>

  <div id="tab-train" class="panel">
    <div style="border:1px solid var(--border);border-radius:8px;padding:14px;margin-bottom:16px;background:var(--bg)">
      <div style="font-size:11px;color:var(--accent);letter-spacing:1px;font-weight:600;margin-bottom:4px">CUSTOM WORD OR WEBSITE TRAINING</div>
      <p style="font-size:12px;color:var(--muted);margin:0 0 10px">Type any tricky website, name, or phrase (e.g., <code>adeptuscamini.com</code>), click <b>Record Take</b>, say it out loud, then click <b>Stop</b>. Record 2&ndash;3 takes if it hears slight variations, then save.</p>
      <label style="margin-top:0">Target Text to Type</label>
      <input type="text" id="customTarget" placeholder="e.g., adeptuscamini.com">
      <div id="customHeard" style="font-size:13px;color:var(--muted);margin:8px 0">Type your target above and click Record Take.</div>
      <textarea id="customLearned" placeholder="Staged rules appear here as: misheard = target (editable, one per line)"></textarea>
      <div class="row">
        <button id="customRecBtn" class="primary" onclick="toggleCustomRec()">Record Take</button>
        <button onclick="clearCustomStaged()">Clear</button>
        <button class="primary" onclick="saveCustomTraining()">Save Custom Rules</button>
      </div>
    </div>

    <div style="border-top:1px solid var(--border);padding-top:14px">
      <div style="font-size:11px;color:var(--muted);letter-spacing:1px;margin-bottom:4px">GUIDED 20-PHRASE CALIBRATION</div>
      <div id="trainProgress" style="font-size:11px;color:var(--muted);letter-spacing:1px">PHRASE 1</div>
      <div id="trainPhrase" class="phrase"></div>
      <div id="trainHeard" style="font-size:13px;color:var(--muted);margin-bottom:8px">Click Record, speak clearly, then Stop.</div>
      <textarea id="trainLearned" readonly placeholder="Learned corrections will appear here..."></textarea>
      <div class="row">
        <button id="recBtn" class="primary" onclick="toggleTrainRec()">Record</button>
        <button onclick="nextPhrase()">Next / Skip</button>
        <button class="primary" onclick="saveTraining()">Finish &amp; Save Rules</button>
      </div>
    </div>
  </div>

  <div class="row">
    <button class="primary" onclick="saveCfg(false)">Save</button>
    <button class="primary" onclick="saveCfg(true)">Save &amp; Restart Service</button>
  </div>
  <div id="status" class="status"></div>
</div>
<script>
let phrases = [], pIdx = 0, recActive = false, customRecActive = false, learnedPairs = [], customTargets = [];
const es = new EventSource('/api/events');
es.onmessage = (e) => {
  const ev = JSON.parse(e.data);
  const dot = document.getElementById('dot');
  dot.className = 'dot ' + (ev.state || '');
  const txt = document.getElementById('previewText');
  if (ev.state === 'recording') txt.textContent = ev.text ? ev.text : 'Listening (internal preview)...';
  else if (ev.state === 'transcribing') txt.textContent = 'Finalizing...';
  else txt.textContent = ev.text ? 'Last pasted: ' + ev.text : 'Idle — hold Right Alt to dictate';
};
(function brandGlitch() {
  const el = document.getElementById('brandTitle');
  if (!el) return;
  const banner = el.closest('.banner');
  const NAMES = ['VoxStilus', "Talk-n' Type"];
  const SWAP_MS = 580, HOLD_MS = 4800;
  let i = 0;
  const setBrand = (name) => {
    el.textContent = name;
    el.setAttribute('data-text', name);
  };
  setBrand(NAMES[0]);
  const swap = () => {
    i = (i + 1) % NAMES.length;
    el.classList.remove('brand-swap');
    if (banner) banner.classList.remove('glitching');
    void el.offsetWidth;
    el.classList.add('brand-swap');
    if (banner) banner.classList.add('glitching');
    setTimeout(() => setBrand(NAMES[i]), SWAP_MS * 0.44);
    setTimeout(() => {
      el.classList.remove('brand-swap');
      if (banner) banner.classList.remove('glitching');
    }, SWAP_MS);
  };
  setInterval(swap, HOLD_MS);
  el.addEventListener('click', swap);
  el.title = "VoxStilus / Talk-n' Type (click to switch)";
})();
function showTab(name, btn) {
  document.querySelectorAll('.tab').forEach(b => b.classList.remove('active'));
  document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
  if (btn) btn.classList.add('active');
  document.getElementById('tab-' + name).classList.add('active');
}
function mapToLines(m) { return Object.entries(m || {}).map(([k,v]) => k + ' = ' + v).join('\n'); }
function linesToMap(t) {
  const out = {};
  for (const raw of t.split('\n')) {
    const line = raw.trim();
    if (!line || !line.includes('=')) continue;
    const idx = line.indexOf('=');
    const k = line.slice(0, idx).trim(), v = line.slice(idx + 1).trim();
    if (k) out[k] = v;
  }
  return out;
}
async function load() {
  const res = await fetch('/api/config');
  const data = await res.json();
  const c = data.config;
  phrases = data.phrases || [];
  const micSel = document.getElementById('mic_device');
  micSel.innerHTML = '<option value="">System Default</option>';
  (data.mics || []).forEach(m => {
    const opt = document.createElement('option'); opt.value = m; opt.textContent = m; micSel.appendChild(opt);
  });
  document.getElementById('trigger_mode').value = c.trigger_mode || 'hold';
  document.getElementById('hold_key').value = c.hold_key || 'KEY_RIGHTALT';
  micSel.value = c.mic_device || '';
  document.getElementById('sentence_pause_ms').value = c.sentence_pause_ms || 700;
  document.getElementById('recording_retention_hours').value = c.recording_retention_hours || 24;
  document.getElementById('auto_duck_media').checked = !!c.auto_duck_media;
  document.getElementById('preserve_clipboard').checked = c.preserve_clipboard !== false;
  document.getElementById('audio_feedback').checked = c.audio_feedback !== false;
  document.getElementById('notify_feedback').checked = !!c.notify_feedback;
  document.getElementById('remove_filler_words').checked = !!c.remove_filler_words;
  document.getElementById('enable_voice_commands').checked = !!c.enable_voice_commands;
  document.getElementById('custom_dictionary').value = mapToLines(c.custom_dictionary);
  document.getElementById('voice_snippets').value = mapToLines(c.voice_snippets);
  document.getElementById('janus_enabled').checked = !!c.janus_enabled;
  document.getElementById('janus_url').value = c.janus_url || '';
  document.getElementById('janus_model').value = c.janus_model || '';
  document.getElementById('stt_endpoint').value = c.stt_endpoint || '';
  document.getElementById('whisper_model_path').value = c.whisper_model_path || '';
  document.getElementById('custom_words_prompt').value = c.custom_words_prompt || '';
  refreshPhrase();
  if (new URLSearchParams(location.search).get('tab') === 'train') {
    showTab('train', document.querySelectorAll('.tab')[3]);
  }
}
async function saveCfg(restart) {
  const cfg = {
    trigger_mode: document.getElementById('trigger_mode').value,
    hold_key: document.getElementById('hold_key').value.trim() || 'KEY_RIGHTALT',
    mic_device: document.getElementById('mic_device').value,
    sentence_pause_ms: parseInt(document.getElementById('sentence_pause_ms').value, 10) || 700,
    recording_retention_hours: parseInt(document.getElementById('recording_retention_hours').value, 10) || 24,
    auto_duck_media: document.getElementById('auto_duck_media').checked,
    preserve_clipboard: document.getElementById('preserve_clipboard').checked,
    audio_feedback: document.getElementById('audio_feedback').checked,
    notify_feedback: document.getElementById('notify_feedback').checked,
    remove_filler_words: document.getElementById('remove_filler_words').checked,
    enable_voice_commands: document.getElementById('enable_voice_commands').checked,
    custom_dictionary: linesToMap(document.getElementById('custom_dictionary').value),
    voice_snippets: linesToMap(document.getElementById('voice_snippets').value),
    janus_enabled: document.getElementById('janus_enabled').checked,
    janus_url: document.getElementById('janus_url').value.trim(),
    janus_model: document.getElementById('janus_model').value.trim(),
    stt_endpoint: document.getElementById('stt_endpoint').value.trim(),
    whisper_model_path: document.getElementById('whisper_model_path').value.trim(),
    custom_words_prompt: document.getElementById('custom_words_prompt').value.trim(),
  };
  await fetch('/api/config', { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({config:cfg, restart}) });
  document.getElementById('status').textContent = restart ? 'Saved & restarted service.' : 'Saved settings.';
}
function refreshPhrase() {
  if (!phrases.length) return;
  if (pIdx >= phrases.length) {
    document.getElementById('trainProgress').textContent = 'CALIBRATION COMPLETE';
    document.getElementById('trainPhrase').textContent = 'All phrases done! Click Finish & Save Rules.';
    return;
  }
  document.getElementById('trainProgress').textContent = 'PHRASE ' + (pIdx + 1) + ' OF ' + phrases.length;
  document.getElementById('trainPhrase').textContent = phrases[pIdx];
  document.getElementById('trainHeard').textContent = 'Click Record, speak clearly, then Stop.';
}
async function toggleTrainRec() {
  const btn = document.getElementById('recBtn');
  if (!recActive) {
    recActive = true; btn.textContent = 'Stop';
    document.getElementById('trainHeard').textContent = 'Listening...';
    await fetch('/api/train/start', {method:'POST'});
  } else {
    recActive = false; btn.textContent = 'Record';
    const expected = phrases[pIdx] || '';
    const res = await fetch('/api/train/stop', { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({expected}) });
    const data = await res.json();
    document.getElementById('trainHeard').textContent = 'Heard: ' + (data.heard || '(empty)');
    (data.learned || []).forEach(p => {
      learnedPairs.push(p);
      document.getElementById('trainLearned').value += p[0] + ' = ' + p[1] + '\n';
    });
    pIdx++; refreshPhrase();
  }
}
async function toggleCustomRec() {
  const btn = document.getElementById('customRecBtn');
  const target = document.getElementById('customTarget').value.trim();
  if (!customRecActive) {
    if (!target) {
      document.getElementById('customHeard').textContent = 'Type the exact word or website first (for example: adeptuscamini.com).';
      return;
    }
    customRecActive = true; btn.textContent = 'Stop';
    document.getElementById('customHeard').textContent = 'Listening... say "' + target + '" now.';
    await fetch('/api/train/start', {method:'POST'});
  } else {
    customRecActive = false; btn.textContent = 'Record Take';
    const res = await fetch('/api/train/stop', {
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body:JSON.stringify({expected: target, mode: 'custom'})
    });
    const data = await res.json();
    const heard = data.heard || '';
    if (target && !customTargets.includes(target)) customTargets.push(target);
    if (!heard) {
      document.getElementById('customHeard').textContent = 'Heard: (empty) — try holding closer to mic.';
      return;
    }
    const pairs = data.learned || [];
    if (pairs.length === 0) {
      document.getElementById('customHeard').textContent = 'Heard exact match: "' + heard + '". Click Save Custom Rules to boost it in Whisper vocabulary.';
    } else {
      const box = document.getElementById('customLearned');
      const curLines = box.value.split('\n').map(s => s.trim()).filter(Boolean);
      pairs.forEach(p => {
        const line = p[0] + ' = ' + p[1];
        if (!curLines.includes(line)) curLines.push(line);
      });
      box.value = curLines.join('\n') + '\n';
      document.getElementById('customHeard').textContent = 'Heard: "' + heard + '" -> staged rule for "' + target + '". Record another take or click Save Custom Rules.';
    }
  }
}
function clearCustomStaged() {
  document.getElementById('customLearned').value = '';
  document.getElementById('customHeard').textContent = 'Cleared staged rules.';
  customTargets = [];
}
async function saveCustomTraining() {
  const target = document.getElementById('customTarget').value.trim();
  const promptTerms = [...customTargets];
  if (target && !promptTerms.includes(target)) promptTerms.push(target);
  const parsedMap = linesToMap(document.getElementById('customLearned').value);
  const pairs = Object.entries(parsedMap).map(([k, v]) => [k, v]);
  if (pairs.length === 0 && promptTerms.length === 0) {
    document.getElementById('status').textContent = 'Nothing staged yet — type a target and record a take first.';
    return;
  }
  const res = await fetch('/api/train/save', {
    method:'POST',
    headers:{'Content-Type':'application/json'},
    body:JSON.stringify({pairs, prompt_terms: promptTerms})
  });
  const d = await res.json();
  document.getElementById('status').textContent = 'Saved custom rules (' + (d.added || 0) + ' new dictionary entries) & updated Whisper prompt!';
  load();
}
function nextPhrase() { pIdx++; refreshPhrase(); }
async function saveTraining() {
  const res = await fetch('/api/train/save', { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({pairs:learnedPairs}) });
  const d = await res.json();
  document.getElementById('status').textContent = 'Saved ' + (d.added || 0) + ' voice calibration rules!';
  load();
}
load();
</script>
</body>
</html>`
