package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Config struct {
	HoldKey                 string            `json:"hold_key"`
	TriggerMode             string            `json:"trigger_mode"`
	MicDevice               string            `json:"mic_device"`
	AutoDuckMedia           bool              `json:"auto_duck_media"`
	AudioFeedback           bool              `json:"audio_feedback"`
	NotifyFeedback          bool              `json:"notify_feedback"`
	PreserveClipboard       bool              `json:"preserve_clipboard"`
	RemoveFillerWords       bool              `json:"remove_filler_words"`
	EnableVoiceCommands     bool              `json:"enable_voice_commands"`
	CustomWordsPrompt       string            `json:"custom_words_prompt"`
	CustomDictionary        map[string]string `json:"custom_dictionary"`
	VoiceSnippets           map[string]string `json:"voice_snippets"`
	STTEndpoint             string            `json:"stt_endpoint"`
	WhisperModelPath        string            `json:"whisper_model_path"`
	VoskModelDir            string            `json:"vosk_model_dir"`
	SentencePauseMs         int               `json:"sentence_pause_ms"`
	MicSilenceThreshold     float64           `json:"mic_silence_threshold"`
	RecordingRetentionHours int               `json:"recording_retention_hours"`
	JanusEnabled            bool              `json:"janus_enabled"`
	JanusURL                string            `json:"janus_url"`
	JanusModel              string            `json:"janus_model"`
}

func configFilePath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "talk-to-type", "config.json")
}

func defaultWhisperModelPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local/share/vocalinux/models/whispercpp/ggml-small.en.bin")
}

func defaultConfig() Config {
	return Config{
		HoldKey:                 "KEY_RIGHTALT",
		TriggerMode:             "hold",
		AutoDuckMedia:           true,
		AudioFeedback:           true,
		NotifyFeedback:          false,
		PreserveClipboard:       true,
		RemoveFillerWords:       true,
		EnableVoiceCommands:     true,
		CustomWordsPrompt:       "Adeptus Camini, adeptuscamini.com, Vibe Engine, Vibra-Ingenn, Janus, Terminus, Vox Vena, Vibecut, Composer, Cursor, Antigravity, Gemini, Claude, Grokbot, Kvantum, KDE Plasma, CachyOS, Gravity, Capricorn, WordPress, recipe.",
		CustomDictionary:        map[string]string{},
		VoiceSnippets:           map[string]string{},
		STTEndpoint:             os.Getenv("VOXSTILUS_STT_URL"),
		WhisperModelPath:        defaultWhisperModelPath(),
		SentencePauseMs:         700,
		MicSilenceThreshold:     0.003,
		RecordingRetentionHours: 24,
		JanusURL:                "http://127.0.0.1:8080/v1/chat/completions",
	}
}

func loadConfig() Config {
	cfg := defaultConfig()
	data, err := os.ReadFile(configFilePath())
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg.HoldKey == "" {
		cfg.HoldKey = "KEY_RIGHTALT"
	}
	if cfg.TriggerMode == "" {
		cfg.TriggerMode = "hold"
	}
	if envURL := os.Getenv("VOXSTILUS_STT_URL"); envURL != "" {
		cfg.STTEndpoint = envURL
	}
	if cfg.STTEndpoint == "" {
		cfg.STTEndpoint = "http://127.0.0.1:18080/v1/audio/transcriptions"
	}
	if envModel := os.Getenv("VOXSTILUS_WHISPER_MODEL"); envModel != "" {
		cfg.WhisperModelPath = envModel
	}
	if cfg.WhisperModelPath == "" {
		cfg.WhisperModelPath = defaultWhisperModelPath()
	}
	if cfg.SentencePauseMs <= 0 {
		cfg.SentencePauseMs = 700
	}
	if cfg.MicSilenceThreshold <= 0 {
		cfg.MicSilenceThreshold = 0.003
	}
	if cfg.RecordingRetentionHours <= 0 {
		cfg.RecordingRetentionHours = 24
	}
	if cfg.JanusURL == "" {
		cfg.JanusURL = "http://127.0.0.1:8080/v1/chat/completions"
	}
	if cfg.CustomDictionary == nil {
		cfg.CustomDictionary = map[string]string{}
	}
	if cfg.VoiceSnippets == nil {
		cfg.VoiceSnippets = map[string]string{}
	}
	return cfg
}

var (
	soundTagRe    = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|\*[^*]*\*|♪+`)
	fillerRe      = regexp.MustCompile(`(?i)\b(?:um+|uh+|er+|ah+)\b[,.]?\s*`)
	multiSpaceRe  = regexp.MustCompile(`[ \t]{2,}`)
	newParaRe     = regexp.MustCompile(`(?i)\s*\bnew paragraph\b[,.]?\s*`)
	newLineRe     = regexp.MustCompile(`(?i)\s*\bnew line\b[,.]?\s*`)
	scratchThatRe = regexp.MustCompile(`(?i)^\s*(?:scratch that|undo that|undo last|delete last sentence)[.!?]*\s*$`)
	pronounIRe    = regexp.MustCompile(`(?i)\bi(?:'(?:m|ll|ve|d))?\b`)
	codeCaseRe    = regexp.MustCompile(`(?i)\b(camel|snake|pascal|kebab|const)\s+case\s+([A-Za-z0-9]+(?:\s+[A-Za-z0-9]+){0,5})`)
)

func isScratchCommand(raw string) bool {
	cleaned := strings.TrimSpace(soundTagRe.ReplaceAllString(raw, " "))
	return scratchThatRe.MatchString(cleaned)
}

func removeWordStutters(text string) string {
	lines := strings.Split(text, "\n")
	for li, line := range lines {
		words := strings.Fields(line)
		if len(words) < 2 {
			continue
		}
		kept := make([]string, 0, len(words))
		for _, w := range words {
			if len(kept) > 0 {
				prev := strings.ToLower(strings.Trim(kept[len(kept)-1], ".,!?;:"))
				cur := strings.ToLower(strings.Trim(w, ".,!?;:"))
				if prev != "" && prev == cur && kept[len(kept)-1] == strings.TrimRight(kept[len(kept)-1], ".,!?;:") {
					kept[len(kept)-1] = w
					continue
				}
			}
			kept = append(kept, w)
		}
		lines[li] = strings.Join(kept, " ")
	}
	return strings.Join(lines, "\n")
}

func transformIdentifierCase(mode, phrase string) string {
	words := strings.Fields(strings.TrimSpace(phrase))
	if len(words) == 0 {
		return phrase
	}
	switch strings.ToLower(mode) {
	case "camel":
		for i, w := range words {
			low := strings.ToLower(w)
			if i == 0 {
				words[i] = low
			} else {
				words[i] = titleWord(low)
			}
		}
		return strings.Join(words, "")
	case "pascal":
		for i, w := range words {
			words[i] = titleWord(strings.ToLower(w))
		}
		return strings.Join(words, "")
	case "snake":
		for i, w := range words {
			words[i] = strings.ToLower(w)
		}
		return strings.Join(words, "_")
	case "kebab":
		for i, w := range words {
			words[i] = strings.ToLower(w)
		}
		return strings.Join(words, "-")
	case "const":
		for i, w := range words {
			words[i] = strings.ToUpper(w)
		}
		return strings.Join(words, "_")
	default:
		return phrase
	}
}

func titleWord(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

var codeSymbolReplacements = []struct {
	re  *regexp.Regexp
	rep string
}{
	{regexp.MustCompile(`(?i)\s*\bopen paren\b\s*`), "("},
	{regexp.MustCompile(`(?i)\s*\bclose paren\b\s*`), ")"},
	{regexp.MustCompile(`(?i)\s*\bopen bracket\b\s*`), "["},
	{regexp.MustCompile(`(?i)\s*\bclose bracket\b\s*`), "]"},
	{regexp.MustCompile(`(?i)\s*\bopen brace\b\s*`), "{"},
	{regexp.MustCompile(`(?i)\s*\bclose brace\b\s*`), "}"},
	{regexp.MustCompile(`(?i)\s*\bfat arrow\b\s*`), " => "},
	{regexp.MustCompile(`(?i)\s*\bdouble equals\b\s*`), " == "},
	{regexp.MustCompile(`(?i)\s*\bnot equals\b\s*`), " != "},
	{regexp.MustCompile(`(?i)\s*\bsemicolon\b[,.]?\s*`), ";"},
}

func applyCodeFormattingCommands(text string) string {
	out := text
	for _, item := range codeSymbolReplacements {
		out = item.re.ReplaceAllString(out, item.rep)
	}
	out = codeCaseRe.ReplaceAllStringFunc(out, func(match string) string {
		sub := codeCaseRe.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		return transformIdentifierCase(sub[1], sub[2])
	})
	return out
}

func isWordEdgeRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func applyCustomDictionary(text string, dict map[string]string) string {
	if text == "" || len(dict) == 0 {
		return text
	}
	lowerMap := make(map[string]string, len(dict))
	keys := make([]string, 0, len(dict))
	for k, v := range dict {
		trimmed := strings.TrimSpace(k)
		if trimmed == "" {
			continue
		}
		low := strings.ToLower(trimmed)
		if _, exists := lowerMap[low]; !exists {
			keys = append(keys, trimmed)
		}
		lowerMap[low] = v
	}
	if len(keys) == 0 {
		return text
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		runes := []rune(k)
		pat := regexp.QuoteMeta(k)
		if isWordEdgeRune(runes[0]) {
			pat = `\b` + pat
		}
		if isWordEdgeRune(runes[len(runes)-1]) {
			pat = pat + `\b`
		}
		parts = append(parts, pat)
	}
	combined, err := regexp.Compile(`(?i)(?:` + strings.Join(parts, "|") + `)`)
	if err != nil {
		return text
	}
	return combined.ReplaceAllStringFunc(text, func(match string) string {
		if rep, ok := lowerMap[strings.ToLower(strings.TrimSpace(match))]; ok {
			return rep
		}
		return match
	})
}

func processText(raw string, cfg Config) string {
	text := strings.TrimSpace(soundTagRe.ReplaceAllString(raw, " "))
	text = multiSpaceRe.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if cfg.EnableVoiceCommands {
		if scratchThatRe.MatchString(text) {
			return ""
		}
		text = newParaRe.ReplaceAllString(text, "\n\n")
		text = newLineRe.ReplaceAllString(text, "\n")
		text = applyCodeFormattingCommands(text)
	}
	if cfg.RemoveFillerWords {
		text = fillerRe.ReplaceAllString(text, "")
		text = removeWordStutters(text)
		text = multiSpaceRe.ReplaceAllString(text, " ")
	}
	norm := strings.ToLower(strings.TrimRight(strings.TrimSpace(text), ".!?"))
	for trigger, expansion := range cfg.VoiceSnippets {
		if strings.ToLower(strings.TrimSpace(trigger)) == norm {
			return expansion
		}
	}
	text = applyCustomDictionary(text, cfg.CustomDictionary)
	return strings.TrimSpace(text)
}

func refineWithJanus(text string, cfg Config) string {
	if !cfg.JanusEnabled || cfg.JanusURL == "" || strings.TrimSpace(text) == "" {
		return text
	}
	payload := map[string]any{
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "Clean up dictated text punctuation and grammar concisely without changing meaning or adding commentary. Output only the cleaned text.",
			},
			{
				"role":    "user",
				"content": text,
			},
		},
		"temperature": 0.1,
	}
	if cfg.JanusModel != "" {
		payload["model"] = cfg.JanusModel
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return text
	}
	req, err := http.NewRequest(http.MethodPost, cfg.JanusURL, bytes.NewReader(body))
	if err != nil {
		return text
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return text
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return text
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil || len(parsed.Choices) == 0 {
		return text
	}
	if out := strings.TrimSpace(parsed.Choices[0].Message.Content); out != "" {
		return out
	}
	return text
}
