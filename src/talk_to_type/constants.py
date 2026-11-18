"""Shared paths and default prompt text."""

from __future__ import annotations

import os
from pathlib import Path

DEFAULT_HOLD_KEY = "KEY_RIGHTALT"

CUSTOM_WORDS_PROMPT = (
    "Adeptus Camini, adeptuscamini.com, Vibe Engine, Vibra-Ingenn, Janus, Terminus, "
    "Vox Vena, Vibecut, Composer, Cursor, Antigravity, Gemini, Claude, Grokbot, Kvantum, "
    "KDE Plasma, CachyOS, Gravity, Capricorn, WordPress, recipe."
)

VOSK_MODEL_NAME = "vosk-model-small-en-us-0.15"
VOSK_MODEL_URL = (
    "https://alphacephei.com/vosk/models/vosk-model-small-en-us-0.15.zip"
)

SAMPLE_RATE = 16_000


def data_home() -> Path:
    override = os.environ.get("TALK_TO_TYPE_DATA_HOME")
    if override:
        return Path(override)
    base = os.environ.get("XDG_DATA_HOME")
    if base:
        return Path(base) / "talk-to-type"
    return Path.home() / ".local/share" / "talk-to-type"


def config_home() -> Path:
    override = os.environ.get("TALK_TO_TYPE_CONFIG_HOME")
    if override:
        return Path(override)
    base = os.environ.get("XDG_CONFIG_HOME")
    if base:
        return Path(base) / "talk-to-type"
    return Path.home() / ".config" / "talk-to-type"


def recordings_dir() -> Path:
    return data_home() / "recordings"


def models_dir() -> Path:
    return data_home() / "models"


def default_whisper_model_path() -> Path:
    vocalinux = (
        Path.home() / ".local/share/vocalinux/models/whispercpp/ggml-small.en.bin"
    )
    if vocalinux.is_file():
        return vocalinux
    return models_dir() / "whispercpp" / "ggml-small.en.bin"


# Vocalinux IBus engine socket (AGPL-3.0 engine must stay running separately).
VOCALINUX_IBUS_SOCKET = Path.home() / ".local/share" / "vocalinux-ibus" / "inject.sock"