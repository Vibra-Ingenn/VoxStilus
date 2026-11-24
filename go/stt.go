package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type sttResponse struct {
	Text string `json:"text"`
}

type managedWhisperServer struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	endpoint  string
	modelPath string
	prompt    string
}

var (
	globalWhisperServer = &managedWhisperServer{}
	sttHTTPClient       = &http.Client{Timeout: 45 * time.Second}
)

func findWhisperServerBin() (string, string) {
	home, _ := os.UserHomeDir()
	unslothBin := filepath.Join(home, ".unsloth", "whisper.cpp", "build", "bin", "whisper-server")
	if st, err := os.Stat(unslothBin); err == nil && !st.IsDir() {
		cudaLib := filepath.Join(home, ".unsloth", "studio", "unsloth_studio", "lib", "python3.13", "site-packages", "nvidia", "cu13", "lib")
		ldParts := []string{filepath.Dir(unslothBin)}
		if _, err := os.Stat(cudaLib); err == nil {
			ldParts = append(ldParts, cudaLib)
		}
		if cur := os.Getenv("LD_LIBRARY_PATH"); cur != "" {
			ldParts = append(ldParts, cur)
		}
		return unslothBin, strings.Join(ldParts, ":")
	}
	if p, err := exec.LookPath("whisper-server"); err == nil {
		return p, os.Getenv("LD_LIBRARY_PATH")
	}
	return "", ""
}

func isTCPOpen(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func ensureWhisperServer(cfg Config) error {
	if cfg.STTEndpoint == "" {
		return fmt.Errorf("empty STT endpoint")
	}
	u, err := url.Parse(cfg.STTEndpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid STT endpoint %q", cfg.STTEndpoint)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	hostPort := net.JoinHostPort(host, port)

	globalWhisperServer.mu.Lock()
	defer globalWhisperServer.mu.Unlock()

	if globalWhisperServer.cmd != nil && globalWhisperServer.cmd.ProcessState == nil &&
		globalWhisperServer.endpoint == cfg.STTEndpoint &&
		globalWhisperServer.modelPath == cfg.WhisperModelPath &&
		globalWhisperServer.prompt == cfg.CustomWordsPrompt &&
		isTCPOpen(hostPort) {
		return nil
	}

	// If an external STT server is already listening there and we didn't spawn it, use it directly.
	if globalWhisperServer.cmd == nil && isTCPOpen(hostPort) {
		return nil
	}

	// Only auto-spawn for local loopback endpoints.
	if host != "127.0.0.1" && host != "localhost" {
		return nil
	}

	globalWhisperServer.stopLocked()

	binPath, ldPath := findWhisperServerBin()
	if binPath == "" {
		return fmt.Errorf("whisper-server binary not found")
	}
	if _, err := os.Stat(cfg.WhisperModelPath); err != nil {
		return fmt.Errorf("whisper model missing at %s: %w", cfg.WhisperModelPath, err)
	}

	infPath := u.Path
	if infPath == "" {
		infPath = "/v1/audio/transcriptions"
	}
	args := []string{
		"-m", cfg.WhisperModelPath,
		"--host", host,
		"--port", port,
		"--inference-path", infPath,
		"-t", "8",
		"-nt",
		"-sns",
	}
	if strings.TrimSpace(cfg.CustomWordsPrompt) != "" {
		args = append(args, "--prompt", strings.TrimSpace(cfg.CustomWordsPrompt))
	}

	cmd := exec.Command(binPath, args...)
	env := os.Environ()
	if ldPath != "" {
		env = append(env, "LD_LIBRARY_PATH="+ldPath)
	}
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start whisper-server: %w", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if isTCPOpen(hostPort) {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return fmt.Errorf("whisper-server timed out waiting for %s", hostPort)
	}

	globalWhisperServer.cmd = cmd
	globalWhisperServer.endpoint = cfg.STTEndpoint
	globalWhisperServer.modelPath = cfg.WhisperModelPath
	globalWhisperServer.prompt = cfg.CustomWordsPrompt
	log.Printf("[STT] native CUDA whisper-server ready at %s (%s)", cfg.STTEndpoint, cfg.WhisperModelPath)
	return nil
}

func (s *managedWhisperServer) stopLocked() {
	if s.cmd != nil {
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		_ = s.cmd.Wait()
		s.cmd = nil
	}
}

func stopSTTServer() {
	globalWhisperServer.mu.Lock()
	defer globalWhisperServer.mu.Unlock()
	globalWhisperServer.stopLocked()
}

func prewarmSTT(cfg Config) {
	if err := ensureWhisperServer(cfg); err != nil {
		log.Printf("[STT] prewarm note: %v", err)
	}
}

// transcribeWAV sends WAV audio to an OpenAI-compatible STT endpoint (e.g. native CUDA whisper-server / Janus),
// or falls back to local whisper-cli if installed in PATH.
func transcribeWAV(wav []byte, cfg Config) (string, error) {
	if len(wav) <= 44 {
		return "", nil
	}

	if cfg.STTEndpoint != "" {
		_ = ensureWhisperServer(cfg)
		text, err := transcribeHTTP(wav, cfg.STTEndpoint)
		if err == nil {
			return text, nil
		}
		if _, lookErr := exec.LookPath("whisper-cli"); lookErr != nil {
			return "", err
		}
	}

	if cliPath, err := exec.LookPath("whisper-cli"); err == nil {
		return transcribeCLI(cliPath, wav, cfg.WhisperModelPath)
	}

	return "", fmt.Errorf("no STT backend reachable at %s and no whisper-cli in PATH", cfg.STTEndpoint)
}

func transcribeHTTP(wav []byte, endpoint string) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(wav); err != nil {
		return "", err
	}
	_ = mw.WriteField("response_format", "json")
	_ = mw.Close()

	req, err := http.NewRequest(http.MethodPost, endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := sttHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("STT request to %s failed: %w", endpoint, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("STT HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var parsed sttResponse
	if err := json.Unmarshal(raw, &parsed); err == nil && parsed.Text != "" {
		return strings.TrimSpace(parsed.Text), nil
	}
	return strings.TrimSpace(string(raw)), nil
}

func transcribeCLI(cliPath string, wav []byte, modelPath string) (string, error) {
	tmp, err := os.CreateTemp("", "voxstilus-*.wav")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(wav); err != nil {
		_ = tmp.Close()
		return "", err
	}
	_ = tmp.Close()

	out, err := exec.Command(cliPath, "-m", modelPath, "-f", tmpName, "--no-timestamps", "-nt").Output()
	if err != nil {
		return "", fmt.Errorf("whisper-cli failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
