# Third-party notices

## Vocalinux (reference only)

Paul's PC runs **Vocalinux 0.18.6** (AGPL-3.0-only) for IBus text injection on KDE Wayland.
This project studied the Unix socket protocol at `~/.local/share/vocalinux-ibus/inject.sock`
and implements a small client in `src/talk_to_type/inject.py` **without copying Vocalinux source**.

If you copy Vocalinux source into this tree, keep their AGPL-3.0 license and copyright notices.

## Models

- **Vosk** `vosk-model-small-en-us-0.15` — Apache 2.0 (Alphacephei)
- **whisper.cpp** `ggml-small.en.bin` — MIT (default path reuses Paul's Vocalinux download)