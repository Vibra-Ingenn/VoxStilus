"""Offline and live dictation pipeline: Vosk live + Whisper refine."""

from __future__ import annotations

import logging
import time
import wave
from dataclasses import dataclass, field
from pathlib import Path

from .config import Settings
from .constants import SAMPLE_RATE
from .inject import TextInjector
from .vosk_live import VoskStreamer
from .whisper_refine import WhisperRefiner, read_wav_pcm16

logger = logging.getLogger(__name__)

CHUNK_MS = 100
CHUNK_SAMPLES = SAMPLE_RATE * CHUNK_MS // 1000
CHUNK_BYTES = CHUNK_SAMPLES * 2


@dataclass
class PipelineResult:
    text: str
    vosk_text: str
    whisper_text: str
    first_partial_ms: float | None
    never_empty: bool


@dataclass
class SentenceChunk:
    pcm: bytes = field(default_factory=bytes)
    vosk_text: str = ""


class DictationPipeline:
    def __init__(self, settings: Settings, injector: TextInjector | None = None):
        self.settings = settings
        self.injector = injector
        vosk_dir = settings.resolved_vosk_dir()
        self.vosk = VoskStreamer(vosk_dir)
        self.whisper = WhisperRefiner(
            settings.resolved_whisper_path(),
            initial_prompt=settings.custom_words_prompt,
        )
        self._sentence = SentenceChunk()
        self._all_pcm = bytearray()
        self._last_audio_t = time.monotonic()
        self._got_audio = False

    def feed_pcm(self, pcm: bytes) -> tuple[str | None, str | None]:
        """Returns (partial_for_bubble, injected_sentence) if any."""
        if pcm:
            self._got_audio = True
            self._last_audio_t = time.monotonic()
            self._all_pcm.extend(pcm)
            self._sentence.pcm += pcm

        bubble: str | None = None
        injected: str | None = None

        for ev in self.vosk.accept_pcm(pcm):
            if ev.is_final:
                self._sentence.vosk_text = ev.text
            else:
                bubble = ev.text

        pause_ms = self.settings.sentence_pause_ms
        if (
            self._sentence.pcm
            and (time.monotonic() - self._last_audio_t) * 1000.0 >= pause_ms
        ):
            injected = self._finish_sentence()

        return bubble, injected

    def finish_session(self) -> str:
        parts: list[str] = []
        tail = self._finish_sentence()
        if tail:
            parts.append(tail)
        final_vosk = self.vosk.flush()
        if final_vosk:
            pcm = bytes(self._sentence.pcm)
            if pcm:
                refined = self.whisper.transcribe_pcm(pcm, final_vosk)
                if refined and refined not in parts:
                    parts.append(refined)
            elif final_vosk not in " ".join(parts):
                parts.append(final_vosk)
        return " ".join(parts).strip()

    def session_pcm(self) -> bytes:
        return bytes(self._all_pcm)

    def first_partial_ms(self) -> float | None:
        return self.vosk.first_partial_ms()

    def _finish_sentence(self) -> str | None:
        if not self._sentence.pcm:
            return None
        pcm = self._sentence.pcm
        vosk_text = self._sentence.vosk_text or self.vosk.flush()
        whisper_text = self.whisper.transcribe_pcm(pcm, vosk_text)
        out = whisper_text or vosk_text
        if out and self.injector is not None:
            self.injector.inject(out)
        self._sentence = SentenceChunk()
        self._last_audio_t = time.monotonic()
        return out or None


def run_wav_file(settings: Settings, wav_path: Path) -> PipelineResult:
    pcm = read_wav_pcm16(wav_path)
    pipeline = DictationPipeline(settings, injector=None)
    offset = 0
    partial_seen = ""
    while offset < len(pcm):
        chunk = pcm[offset : offset + CHUNK_BYTES]
        offset += len(chunk)
        bubble, _ = pipeline.feed_pcm(chunk)
        if bubble:
            partial_seen = bubble
    trailing_silence = b"\x00\x00" * (SAMPLE_RATE * max(settings.sentence_pause_ms, 800) // 1000)
    tail_offset = 0
    while tail_offset < len(trailing_silence):
        chunk = trailing_silence[tail_offset : tail_offset + CHUNK_BYTES]
        tail_offset += len(chunk)
        bubble, _ = pipeline.feed_pcm(chunk)
        if bubble:
            partial_seen = bubble
    final = pipeline.finish_session()
    if not final and partial_seen:
        final = partial_seen
    vosk_only = pipeline.vosk.flush() or partial_seen
    return PipelineResult(
        text=final,
        vosk_text=vosk_only,
        whisper_text=final,
        first_partial_ms=pipeline.first_partial_ms(),
        never_empty=bool(final.strip()),
    )


def pcm_rms(pcm: bytes) -> float:
    if not pcm:
        return 0.0
    import numpy as np

    arr = np.frombuffer(pcm, dtype=np.int16).astype(np.float32)
    if arr.size == 0:
        return 0.0
    return float(np.sqrt(np.mean(arr * arr)) / 32768.0)


def save_wav(path: Path, pcm: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with wave.open(str(path), "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(SAMPLE_RATE)
        wf.writeframes(pcm)