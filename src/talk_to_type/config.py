"""Load and save talk-to-type settings."""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from pathlib import Path

from .constants import (
    CUSTOM_WORDS_PROMPT,
    DEFAULT_HOLD_KEY,
    config_home,
    default_whisper_model_path,
    models_dir,
)


@dataclass
class Settings:
    hold_key: str = DEFAULT_HOLD_KEY
    whisper_model_path: str = ""
    vosk_model_dir: str = ""
    custom_words_prompt: str = CUSTOM_WORDS_PROMPT
    mic_silence_threshold: float = 0.003
    sentence_pause_ms: int = 700
    recording_retention_hours: int = 24
    use_ibus_injection: bool = True
    show_status_dot: bool = True

    def resolved_whisper_path(self) -> Path:
        if self.whisper_model_path:
            return Path(self.whisper_model_path)
        return default_whisper_model_path()

    def resolved_vosk_dir(self) -> Path:
        if self.vosk_model_dir:
            return Path(self.vosk_model_dir)
        return models_dir() / "vosk-model-small-en-us-0.15"


def settings_path() -> Path:
    return config_home() / "config.json"


def load_settings() -> Settings:
    path = settings_path()
    if not path.is_file():
        return Settings()
    data = json.loads(path.read_text(encoding="utf-8"))
    allowed = {f.name for f in Settings.__dataclass_fields__.values()}  # type: ignore[attr-defined]
    filtered = {k: v for k, v in data.items() if k in allowed}
    return Settings(**filtered)


def save_settings(settings: Settings) -> None:
    path = settings_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(asdict(settings), indent=2) + "\n", encoding="utf-8")