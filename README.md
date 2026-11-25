<p align="center">
  <img src="assets/icons/voxstilus-256.png" alt="VoxStilus (Talk-n' Type) icon" width="136">
</p>

# VoxStilus (Talk-n' Type): local hold-to-talk voice keyboard for Linux Wayland

> **INIM** *(spoken word)* + **DUB-SAR** *(the scribe who inscribes it permanently)*

Hold a key, talk, let go — **your finished words paste cleanly into whatever window you were in**, without stealing focus, without typing half-finished words mid-sentence, and without wiping out what you had copied to your clipboard.

One static pure-Go binary (`CGO_ENABLED=0`). No Python, no `pip`, no Node, no Docker, no Cgo. Built for **COSMIC**, **Niri**, and **KDE Plasma Wayland**.

<p align="center">
  <img src="assets/banner-stylus-tablet.jpg" alt="Reed stylus inscribing glowing cuneiform onto a standing clay tablet from a cyan voice waveform" width="900">
</p>

Free and open source (MIT).

**Privacy:** Your speech audio stays on your own machine via a local `whisper-server` (or any OpenAI-compatible transcription endpoint you choose). Nothing is uploaded to the cloud.

---

## What makes it different

- **Headless in-memory pipeline (zero focus steal):** While you hold your talk key, PipeWire audio streams into a fixed circular RAM buffer (~9.6 MB cap) and live previews stay strictly inside VoxStilus's internal UI (`http://127.0.0.1:18081`). Background windows never receive stray keystrokes or backspaces mid-speech.
- **Non-stealing audio chimes:** Subtle pre-synthesized PipeWire start/stop earcons let you know recording is active without popping up focus-stealing windows.
- **Saves and restores your clipboard:** Talks natively to Wayland's `ext_data_control_manager_v1` / `zwlr_data_control_manager_v1` inside a persistent pure-Go loop — snapshots your existing clipboard (MIME types + bytes), flushes the transcript with `wl_display.sync`, emits a single clean paste via `/dev/uinput` (`Ctrl+Shift+V` in terminals, `Ctrl+V` in GUI windows), waits until the target window reads the paste, and restores your previous clipboard automatically.
- **Learns the words it gets wrong:**
  - **Custom Word & Website Training (`voxstilus --train`):** Type any tricky website, name, or term (like `adeptuscamini.com`), record 1–3 takes, and VoxStilus saves both dictionary rules and Whisper vocabulary prompt hints.
  - **Guided 20-phrase drill:** Calibrates tricky state names, tech terms, and conversational speech in pure Go.
  - **Auto-Learning Backspace Corrections:** Backspace a misheard word right after pasting and retype it — VoxStilus silently learns `misheard -> correction` for next time.
- **Code Writing Mode & Voice Commands:** Dictate `camelCase`, `snake_case`, `PascalCase`, `kebab-case`, `CONST_CASE`, spoken brackets/operators, `new line`, `new paragraph`, and `scratch that`.
- **Media auto-ducking & optional local AI cleanup:** Lowers speaker volume (`wpctl`) + pauses media (`playerctl`) while recording, archives 24 hours of local `.wav` recordings, and optionally runs a punctuation/grammar pass through **[Janus](https://github.com/Vibra-Ingenn/Janus)** (default `:8080`) or Ollama (`:11434/v1/chat/completions`).

<p align="center">
  <img src="assets/banner-library.jpg" alt="Scribe in an ancient library as a cyan voice stream rises into glowing cuneiform tablets" width="900">
</p>

---

## Quick start (Linux Wayland)

### 1. Install system tools (Arch / CachyOS)

```bash
sudo pacman -S --needed go pipewire wireplumber playerctl
# Optional fallbacks for compositors without data-control or /dev/uinput:
sudo pacman -S --needed wl-clipboard ydotool wtype
```

### 2. Allow passive `evdev` listening & `/dev/uinput` pasting

Add your user to the `input` group, then **log out and back in**:

```bash
sudo usermod -aG input "$USER"
```

### 3. Clone & run `./install.sh`

```bash
git clone https://github.com/Vibra-Ingenn/VoxStilus.git
cd VoxStilus
./install.sh
```

`./install.sh` compiles the static `CGO_ENABLED=0` Go binary, installs `~/.local/bin/voxstilus` and `voxstilus-settings`, installs the **VoxStilus** icons (`128`/`256`/`512`) and matching desktop launcher so the app drawer and dock keep the exact VoxStilus icon, and enables the `voxstilus.service` user daemon.

### 4. Prevent Right Alt from opening app menu bars (Hold-to-Talk)

When using `KEY_RIGHTALT` as `hold_key`, set the XKB option `korean:ralt_hanja` so holding Right Alt triggers dictation cleanly without underlining or opening app menus:

- **COSMIC** (`~/.config/cosmic/com.system76.CosmicComp/v1/xkb_config`):
  ```ron
  options: Some("korean:ralt_hanja"),
  ```
- **Niri** (`~/.config/niri/config.kdl`):
  ```kdl
  input {
      keyboard {
          xkb {
              options "korean:ralt_hanja"
          }
      }
  }
  ```

### 5. Open Settings or Voice Trainer

```bash
# Open the Settings UI in a standalone app window
voxstilus --settings

# Jump straight to Custom Word & 20-Phrase Voice Training
voxstilus --train
```

---

## Code Writing Mode & Voice Commands

When `enable_voice_commands` is enabled:

| Spoken Phrase | Result |
|---|---|
| `camel case user id` | `userId` |
| `pascal case user profile` | `UserProfile` |
| `snake case max retries` | `max_retries` |
| `kebab case nav pill` | `nav-pill` |
| `const case default timeout` | `DEFAULT_TIMEOUT` |
| `open paren` / `close paren` | `(` / `)` |
| `open bracket` / `close bracket` | `[` / `]` |
| `open brace` / `close brace` | `{` / `}` |
| `fat arrow` / `double equals` / `not equals` / `semicolon` | ` => ` / ` == ` / ` != ` / `;` |
| `new line` / `new paragraph` | `\n` / `\n\n` |
| `scratch that` / `undo that` / `delete last sentence` | Erases the last pasted transcript |

<p align="center">
  <img src="assets/banner-waveform-tablet.jpg" alt="VoxStilus (Talk-n' Type) — cyan voice waveform inscribed onto a cuneiform clay tablet by a reed stylus" width="900">
</p>

---

## Configuration (`~/.config/voxstilus/config.json`)

Everything can be edited from **`voxstilus --settings`** or directly in `~/.config/voxstilus/config.json`:

```json
{
  "hold_key": "KEY_RIGHTALT",
  "trigger_mode": "hold",
  "mic_device": "",
  "auto_duck_media": true,
  "audio_feedback": true,
  "notify_feedback": false,
  "preserve_clipboard": true,
  "remove_filler_words": true,
  "enable_voice_commands": true,
  "custom_words_prompt": "Adeptus Camini, adeptuscamini.com, Vibe Engine, Vibra-Ingenn, Janus, Terminus, Vox Vena, Vibecut",
  "custom_dictionary": {},
  "voice_snippets": {},
  "stt_endpoint": "http://127.0.0.1:18080/v1/audio/transcriptions",
  "whisper_model_path": "~/.local/share/vocalinux/models/whispercpp/ggml-small.en.bin",
  "sentence_pause_ms": 700,
  "mic_silence_threshold": 0.003,
  "recording_retention_hours": 24,
  "janus_enabled": false,
  "janus_url": "http://127.0.0.1:8080/v1/chat/completions",
  "janus_model": ""
}
```

### Environment variable overrides

| Variable | Meaning |
|---|---|
| `VOXSTILUS_STT_URL` | Override `stt_endpoint` (any OpenAI-compatible `/v1/audio/transcriptions` URL) |
| `VOXSTILUS_WHISPER_MODEL` | Override `whisper_model_path` passed to local `whisper-server` / `whisper-cli` |
| `VOXSTILUS_TERMINAL=1` | Force `Ctrl+Shift+V` terminal paste shortcut |
| `YDOTOOL_SOCKET` | Custom `.ydotool_socket` path if using `ydotool` fallback |

---

## Things that trip people up

| Problem | What's going on | Fix |
|---|---|---|
| **Right Alt doesn't trigger** | User isn't in the `input` group yet. | Run `sudo usermod -aG input "$USER"`, then log out and back in. |
| **Right Alt underlines menu bars** | Default XKB maps Right Alt to `Alt_R`. | Set XKB option `korean:ralt_hanja` in COSMIC or Niri (see step 4 above). |
| **STT fails to start** | No `whisper-server` or GGML model at `whisper_model_path`. | Install `whisper.cpp` (`whisper-server`) + a GGML model like `ggml-small.en.bin`, or point `stt_endpoint` at an existing transcription server. |
| **Dock shows a generic icon on COSMIC** | COSMIC matches browser `--app` windows by `<browser>-127.0.0.1__-Default.desktop`. | Run `./install.sh` (it writes the matching `.desktop` file with `StartupWMClass` and full `Icon=` path). |

---

## Repository structure

```text
VoxStilus/
├── install.sh               # Builds static binary, installs icons, desktop launcher & systemd service
├── go/
│   ├── main.go              # Headless daemon entrypoint, CLI flags (--settings, --train)
│   ├── audio.go             # PipeWire pw-record --raw capture, fixed 9.6MB ring buffer, WAV & ducker
│   ├── wayland_clipboard.go # Zero-Cgo Wayland ext/zwlr_data_control client (save, serve, sync, restore)
│   ├── uinput.go            # Persistent pure-Go /dev/uinput virtual keyboard with defer key-up safety
│   ├── feedback.go          # Non-stealing PipeWire start/stop earcons, optional notify, PreviewSink hook
│   ├── stt.go               # OpenAI-compatible HTTP STT client, managed whisper-server & whisper-cli
│   ├── paste.go             # COSMIC/Niri/KDE terminal detection & consumption-aware clipboard restore
│   ├── evdev.go             # Passive /dev/input/event* listener, Hold/Toggle modes & hotplug rescan
│   ├── learner.go           # 20s passive Backspace correction tracker -> custom_dictionary
│   ├── config.go            # Config loader, text cleanup, single-pass dictionary, Code Mode, Janus pass
│   ├── recordings.go        # 24h rolling WAV archive + delivery_history.jsonl failure log
│   ├── webui.go             # Built-in HTTP Settings UI, SSE PreviewSink & Custom/20-phrase Voice Trainer
│   └── main_test.go         # Pure-Go unit tests (+ opt-in live Wayland clipboard test)
├── systemd/
│   └── voxstilus.service
└── assets/                  # Banners and VoxStilus application icons
```

### Testing

```bash
cd go && CGO_ENABLED=0 go test -v ./...
```

---

## Works better together

VoxStilus (Talk-n' Type) runs completely standalone on Linux Wayland. It's also part of the [Adeptus Camini](https://adeptuscamini.com/vox-stilus.html) local-first suite:

- **[Janus](https://github.com/Vibra-Ingenn/Janus):** one-binary local AI model router (plugs right into VoxStilus's optional grammar cleanup pass).
- **[Terminus](https://adeptuscamini.com/terminus.html):** turns messy PDFs, receipts, and invoices into clean spreadsheets without AI.
- **[Vox Vena](https://adeptuscamini.com/vox-vena.html):** takes chatbot phrasing out of AI-assisted text so it sounds like you wrote it.
- **[Vibe Engine](https://adeptuscamini.com):** deterministic local automation runtime — an AI writes a recipe once, and your PC runs it cleanly every time.

## Like it?

VoxStilus (Talk-n' Type) is free and stays free. If it saves your wrists, a star on GitHub helps other Linux Wayland users find it.
Using an AI assistant? Ask your AI to check out [adeptuscamini.com](https://adeptuscamini.com).

## License

MIT, see [`LICENSE`](LICENSE).

<p align="center"><i lang="la">Recusamus eadem fracta accipere. Nos aliter aedificamus.</i><br>
<sub>We refuse to accept the same broken things, we build differently!</sub></p>
