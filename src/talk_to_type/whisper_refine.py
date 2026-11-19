"""Whisper.cpp refinement pass (keeps Vosk text when Whisper fails)."""

from __future__ import annotations

import logging
import wave
from pathlib import Path
from typing import Any

import numpy as np

from .constants import SAMPLE_RATE

logger = logging.getLogger(__name__)

WHISPER_MIN_PCM_MS = 200
WHISPER_JUNK_MARKERS = ("[blank_audio]", "[ silence ]", "[silence]")


def pcm16_duration_ms(pcm: bytes) -> float:
    if not pcm:
        return 0.0
    samples = len(pcm) // 2
    return samples * 1000.0 / SAMPLE_RATE


def trim_trailing_silence_pcm(pcm: bytes, rms_threshold: float = 0.003) -> bytes:
    if not pcm:
        return pcm
    step = SAMPLE_RATE // 20
    end = len(pcm)
    while end > step * 2:
        tail = pcm[end - step : end]
        arr = np.frombuffer(tail, dtype=np.int16).astype(np.float32)
        rms = float(np.sqrt(np.mean(arr * arr)) / 32768.0)
        if rms < rms_threshold:
            end -= step
        else:
            break
    return pcm[:end]


def is_whisper_junk(text: str) -> bool:
    low = text.strip().lower()
    if not low:
        return True
    return any(marker in low for marker in WHISPER_JUNK_MARKERS)


def pad_pcm16_min_duration(pcm: bytes, min_ms: float = WHISPER_MIN_PCM_MS) -> bytes:
    """Pad with silence so whisper.cpp never rejects short audio."""
    need_ms = max(0.0, min_ms - pcm16_duration_ms(pcm))
    if need_ms <= 0.0:
        return pcm
    pad_samples = int(SAMPLE_RATE * need_ms / 1000.0)
    return pcm + (b"\x00\x00" * pad_samples)


def _float32_from_pcm16(pcm: bytes) -> np.ndarray:
    arr = np.frombuffer(pcm, dtype=np.int16).astype(np.float32) / 32768.0
    return arr


def _try_whisper_params() -> dict[str, Any]:
    """GPU when Vulkan/CUDA is available (same knobs Vocalinux uses)."""
    params: dict[str, Any] = {
        "language": "en",
        "no_context": True,
        "single_segment": True,
        "print_progress": False,
        "print_realtime": False,
    }
    try:
        from pywhispercpp.constants import AVAILABLE_BACKENDS

        if "vulkan" in AVAILABLE_BACKENDS:
            params["use_gpu"] = True
            params["gpu_device"] = 0
        elif "cuda" in AVAILABLE_BACKENDS:
            params["use_gpu"] = True
            params["gpu_device"] = 0
    except Exception:
        pass
    return params


_MODEL_CACHE: dict[tuple[str, str], object] = {}


class WhisperRefiner:
    def __init__(self, model_path: Path, initial_prompt: str = ""):
        self.model_path = model_path
        self.initial_prompt = initial_prompt.strip()
        self._model: object | None = None
        self._params = _try_whisper_params()
        if self.initial_prompt:
            self._params["initial_prompt"] = self.initial_prompt

    def _ensure_model(self) -> None:
        if not self.model_path.is_file():
            raise FileNotFoundError(f"Whisper model not found: {self.model_path}")
        key = (str(self.model_path), self.initial_prompt)
        if key in _MODEL_CACHE:
            self._model = _MODEL_CACHE[key]
            return
        from pywhispercpp.model import Model

        logger.info("Loading whisper.cpp model %s", self.model_path)
        model = Model(str(self.model_path), **self._params)
        _MODEL_CACHE[key] = model
        self._model = model

    def transcribe_pcm(self, pcm: bytes, fallback: str) -> str:
        if not pcm:
            return fallback.strip()
        pcm = pad_pcm16_min_duration(pcm)
        try:
            self._ensure_model()
        except FileNotFoundError:
            logger.warning("Whisper model missing; keeping Vosk text")
            return fallback.strip()
        audio = _float32_from_pcm16(pcm)
        try:
            segments = self._model.transcribe(audio, n_threads=4)  # type: ignore[union-attr]
            text = " ".join(seg.text.strip() for seg in segments if seg.text.strip())
            text = text.strip()
            if text and not is_whisper_junk(text):
                return text
        except Exception as exc:
            logger.warning("Whisper transcribe failed: %s", exc)
        kept = fallback.strip()
        return kept


def read_wav_pcm16(path: Path) -> bytes:
    with wave.open(str(path), "rb") as wf:
        if wf.getnchannels() != 1:
            raise ValueError(f"Expected mono WAV: {path}")
        if wf.getsampwidth() != 2:
            raise ValueError(f"Expected 16-bit WAV: {path}")
        if wf.getframerate() != SAMPLE_RATE:
            raise ValueError(f"Expected {SAMPLE_RATE} Hz WAV: {path}")
        return wf.readframes(wf.getnframes())