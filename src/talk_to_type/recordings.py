"""Save session WAVs and list recent clips (24 h retention)."""

from __future__ import annotations

import json
import time
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path

from .constants import recordings_dir
from .pipeline import save_wav


@dataclass
class RecordingEntry:
    id: str
    path: str
    created_at: str
    duration_sec: float
    text: str


def _index_path() -> Path:
    return recordings_dir() / "index.json"


def load_index() -> list[RecordingEntry]:
    path = _index_path()
    if not path.is_file():
        return []
    data = json.loads(path.read_text(encoding="utf-8"))
    return [RecordingEntry(**row) for row in data]


def save_index(entries: list[RecordingEntry]) -> None:
    path = _index_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        json.dumps([asdict(e) for e in entries], indent=2) + "\n",
        encoding="utf-8",
    )


def prune_old(retention_hours: int) -> None:
    cutoff = time.time() - retention_hours * 3600
    kept: list[RecordingEntry] = []
    for entry in load_index():
        p = Path(entry.path)
        if p.is_file() and p.stat().st_mtime >= cutoff:
            kept.append(entry)
        elif p.is_file():
            p.unlink(missing_ok=True)
    save_index(kept)


def add_recording(pcm: bytes, text: str, duration_sec: float) -> RecordingEntry:
    recordings_dir().mkdir(parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    wav_path = recordings_dir() / f"{stamp}.wav"
    save_wav(wav_path, pcm)
    entry = RecordingEntry(
        id=stamp,
        path=str(wav_path),
        created_at=datetime.now(timezone.utc).isoformat(),
        duration_sec=duration_sec,
        text=text,
    )
    entries = load_index()
    entries.insert(0, entry)
    save_index(entries)
    return entry