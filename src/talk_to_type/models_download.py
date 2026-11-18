"""Download Vosk model if missing."""

from __future__ import annotations

import logging
import zipfile
from pathlib import Path

import requests

from .constants import VOSK_MODEL_NAME, VOSK_MODEL_URL, models_dir

logger = logging.getLogger(__name__)


def vosk_model_dir() -> Path:
    return models_dir() / VOSK_MODEL_NAME


def ensure_vosk_model() -> Path:
    target = vosk_model_dir()
    if (target / "am").is_dir() or (target / "graph").is_dir():
        return target
    models_dir().mkdir(parents=True, exist_ok=True)
    zip_path = models_dir() / f"{VOSK_MODEL_NAME}.zip"
    logger.info("Downloading Vosk model to %s", zip_path)
    with requests.get(VOSK_MODEL_URL, stream=True, timeout=120) as resp:
        resp.raise_for_status()
        with open(zip_path, "wb") as fh:
            for chunk in resp.iter_content(chunk_size=1 << 20):
                if chunk:
                    fh.write(chunk)
    with zipfile.ZipFile(zip_path, "r") as zf:
        zf.extractall(models_dir())
    zip_path.unlink(missing_ok=True)
    if not target.is_dir():
        # zip usually extracts with model name folder
        raise FileNotFoundError(f"Expected {target} after unzip")
    return target