# talk-to-type

Hold-to-talk dictation for Linux (KDE Wayland first). Default **Right Alt** held = record; release = finish.

- **Live partials:** Vosk streaming in an on-screen bubble.
- **Refine:** whisper.cpp `small.en` (GPU when available) replaces each finished sentence; falls back to Vosk — never drops speech.
- **Status:** red/grey dot + short start/stop sounds; warns within 1 s if the mic is silent.
- **Recordings:** `~/.local/share/talk-to-type/recordings/` (24 h); tray **Recent** re-pastes or re-transcribes.
- **Injection:** Vocalinux IBus socket on this PC (see `NOTICES.md`); `wtype` fallback.

## Setup

```bash
cd ~/Dev/talk-to-type
uv sync
uv run python -c "from talk_to_type.models_download import ensure_vosk_model; ensure_vosk_model()"
```

Whisper model: uses `~/.local/share/vocalinux/models/whispercpp/ggml-small.en.bin` when present.

Config: `~/.config/talk-to-type/config.json`

## systemd user service

```bash
mkdir -p ~/.config/systemd/user
cp ~/Dev/talk-to-type/systemd/talk-to-type.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now talk-to-type.service
```

Does not stop or replace Vocalinux — uses its IBus engine when available.

## Proof

```bash
uv run talk-to-type-test-pipeline
uv run pytest -q
```