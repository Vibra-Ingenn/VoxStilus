"""Streaming Vosk partial results."""

from __future__ import annotations

import json
import logging
import time
from dataclasses import dataclass
from pathlib import Path

from vosk import KaldiRecognizer, Model, SetLogLevel

from .constants import SAMPLE_RATE

SetLogLevel(-1)
logger = logging.getLogger(__name__)


@dataclass
class PartialEvent:
    text: str
    is_final: bool
    elapsed_ms: float


class VoskStreamer:
    def __init__(self, model_dir: Path):
        if not model_dir.is_dir():
            raise FileNotFoundError(f"Vosk model directory missing: {model_dir}")
        self._model = Model(str(model_dir))
        self._rec = KaldiRecognizer(self._model, SAMPLE_RATE)
        self._rec.SetWords(True)
        self._started = time.monotonic()
        self._first_partial_ms: float | None = None

    def accept_pcm(self, pcm: bytes) -> list[PartialEvent]:
        events: list[PartialEvent] = []
        if not pcm:
            return events
        if self._rec.AcceptWaveform(pcm):
            data = json.loads(self._rec.Result())
            text = (data.get("text") or "").strip()
            if text:
                events.append(
                    PartialEvent(text=text, is_final=True, elapsed_ms=self._elapsed_ms())
                )
        else:
            data = json.loads(self._rec.PartialResult())
            text = (data.get("partial") or "").strip()
            if text:
                if self._first_partial_ms is None:
                    self._first_partial_ms = self._elapsed_ms()
                events.append(
                    PartialEvent(text=text, is_final=False, elapsed_ms=self._elapsed_ms())
                )
        return events

    def flush(self) -> str:
        data = json.loads(self._rec.FinalResult())
        return (data.get("text") or "").strip()

    def first_partial_ms(self) -> float | None:
        return self._first_partial_ms

    def reset(self) -> None:
        self._rec = KaldiRecognizer(self._model, SAMPLE_RATE)
        self._rec.SetWords(True)

    def _elapsed_ms(self) -> float:
        return (time.monotonic() - self._started) * 1000.0