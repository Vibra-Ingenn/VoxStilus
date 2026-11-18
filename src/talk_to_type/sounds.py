"""Short start/stop UI sounds."""

from __future__ import annotations

import logging
import shutil
import subprocess
from pathlib import Path

from .constants import data_home

logger = logging.getLogger(__name__)


def _assets_dir() -> Path:
    return data_home() / "sounds"


def ensure_default_sounds() -> tuple[Path, Path]:
    """Create tiny WAV cues with espeak-ng if missing."""
    d = _assets_dir()
    d.mkdir(parents=True, exist_ok=True)
    start = d / "start.wav"
    stop = d / "stop.wav"
    espeak = shutil.which("espeak-ng") or shutil.which("espeak")
    if espeak and (not start.is_file() or not stop.is_file()):
        subprocess.run(
            [espeak, "-w", str(start), "go"],
            check=False,
            capture_output=True,
        )
        subprocess.run(
            [espeak, "-w", str(stop), "done"],
            check=False,
            capture_output=True,
        )
    return start, stop


def play(path: Path) -> None:
    if not path.is_file():
        return
    for player in ("paplay", "aplay", "ffplay"):
        exe = shutil.which(player)
        if not exe:
            continue
        cmd = [exe, str(path)] if player != "ffplay" else [exe, "-nodisp", "-autoexit", str(path)]
        try:
            subprocess.run(cmd, check=False, timeout=3, capture_output=True)
            return
        except (subprocess.SubprocessError, OSError) as exc:
            logger.debug("%s failed: %s", player, exc)