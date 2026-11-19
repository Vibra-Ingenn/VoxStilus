"""Offline and live dictation pipeline: Vosk live + Whisper refine."""

from __future__ import annotations

import logging
import queue
import threading
import time
import wave
from dataclasses import dataclass, field
from pathlib import Path

from .config import Settings
from .constants import SAMPLE_RATE
from .inject import TextInjector
from .vosk_live import VoskStreamer
from .whisper_refine import (
    WhisperRefiner,
    is_whisper_junk,
    pad_pcm16_min_duration,
    pcm16_duration_ms,
    read_wav_pcm16,
    trim_trailing_silence_pcm,
)

logger = logging.getLogger(__name__)

CHUNK_MS = 100
CHUNK_SAMPLES = SAMPLE_RATE * CHUNK_MS // 1000
CHUNK_BYTES = CHUNK_SAMPLES * 2

MIN_SENTENCE_PCM_MS = 120
PREEDIT_HEARTBEAT_S = 0.32


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
    vosk_parts: list[str] = field(default_factory=list)


@dataclass
class _RefineJob:
    pcm: bytes
    vosk_text: str
    partial_fallback: str


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
        self._last_partial = ""
        self._trailing_silence_ms = 0.0
        self._last_preedit_wall = 0.0
        self._lead_trimmed = False
        self._heard_ms = 0.0
        self._refine_q: queue.Queue[_RefineJob | None] = queue.Queue()
        self._refine_done = threading.Event()
        self._refine_done.set()
        self._refine_thread = threading.Thread(target=self._refine_worker, daemon=True)
        self._refine_thread.start()

    def feed_pcm(self, pcm: bytes) -> tuple[str | None, str | None]:
        """Returns (partial_for_bubble, injected_sentence) if any."""
        injected: str | None = None
        pause_ms = self.settings.sentence_pause_ms
        silence_rms = self.settings.mic_silence_threshold

        if self.injector is not None:
            if pcm:
                dur_ms = pcm16_duration_ms(pcm)
                if pcm_rms(pcm) < silence_rms:
                    self._trailing_silence_ms += dur_ms
                else:
                    self._trailing_silence_ms = 0.0
            if self._sentence.pcm and self._trailing_silence_ms >= pause_ms:
                injected = self._finish_sentence()
                self._trailing_silence_ms = 0.0
        if pcm:
            self._got_audio = True
            self._last_audio_t = time.monotonic()
            self._all_pcm.extend(pcm)
            self._heard_ms += pcm16_duration_ms(pcm)
            if (
                self.injector is not None
                and not self._lead_trimmed
                and self._heard_ms >= 350.0
                and pcm_rms(pcm) < silence_rms
            ):
                self.vosk.reset()
                self._lead_trimmed = True
            is_silence = pcm_rms(pcm) < silence_rms
            if not is_silence or not self._sentence.pcm:
                self._sentence.pcm += pcm

        bubble: str | None = None

        for ev in self.vosk.accept_pcm(pcm):
            if ev.is_final:
                if ev.text:
                    self._sentence.vosk_parts.append(ev.text.strip())
                    self._sentence.vosk_text = " ".join(self._sentence.vosk_parts)
            else:
                bubble = ev.text
                self._last_partial = ev.text

        if self.injector is not None and (bubble or self._last_partial):
            show = bubble or self._last_partial
            now = time.monotonic()
            force = (now - self._last_preedit_wall) >= PREEDIT_HEARTBEAT_S
            if bubble or force:
                self.injector.set_preedit(show, force=force or bool(bubble))
                self._last_preedit_wall = now

        return bubble, injected

    def finish_session(self) -> str:
        if self.injector is not None:
            self._finish_sentence()
            self.drain_refine()
            return ""

        parts: list[str] = []
        tail = self._finish_sentence()
        self.drain_refine()
        if tail:
            parts.append(tail)
        if self._sentence.pcm:
            final_vosk = self.vosk.flush()
            pcm = bytes(self._sentence.pcm)
            if pcm and final_vosk:
                refined = self._transcribe_sentence(pcm, final_vosk, self._last_partial)
                if refined:
                    parts.append(refined)
            elif final_vosk:
                parts.append(final_vosk)
        return " ".join(parts).strip()

    def session_pcm(self) -> bytes:
        return bytes(self._all_pcm)

    def first_partial_ms(self) -> float | None:
        return self.vosk.first_partial_ms()

    def drain_refine(self, timeout: float = 120.0) -> None:
        if not self._refine_thread.is_alive():
            return
        self._refine_done.wait(timeout=timeout)
        self._refine_q.join()

    def close(self) -> None:
        self._refine_q.put(None)
        self._refine_thread.join(timeout=5.0)

    def _refine_worker(self) -> None:
        while True:
            job = self._refine_q.get()
            try:
                if job is None:
                    return
                self._refine_done.clear()
                text = self._transcribe_sentence(job.pcm, job.vosk_text, job.partial_fallback)
                if text and self.injector is not None:
                    self.injector.commit(text)
            finally:
                self._refine_q.task_done()
                self._refine_done.set()

    def _transcribe_sentence(self, pcm: bytes, vosk_text: str, partial: str) -> str:
        trimmed = trim_trailing_silence_pcm(pcm, self.settings.mic_silence_threshold)
        if not trimmed:
            return (vosk_text or partial).strip()
        padded = pad_pcm16_min_duration(trimmed)
        whisper_text = self.whisper.transcribe_pcm(padded, vosk_text)
        if whisper_text and is_whisper_junk(whisper_text):
            whisper_text = ""
        out = (whisper_text or vosk_text or partial).strip()
        return out

    def _finish_sentence(self) -> str | None:
        if not self._sentence.pcm:
            return None
        pcm = bytes(self._sentence.pcm)
        trimmed = trim_trailing_silence_pcm(pcm, self.settings.mic_silence_threshold)
        pcm_ms = pcm16_duration_ms(trimmed)
        vosk_text = self._sentence.vosk_text.strip()
        if not vosk_text:
            vosk_text = self.vosk.flush()
        partial = self._last_partial.strip()

        if pcm_ms < MIN_SENTENCE_PCM_MS and not vosk_text and not partial:
            self._sentence = SentenceChunk()
            self._trailing_silence_ms = 0.0
            self.vosk.reset()
            return None

        snapshot = _RefineJob(
            pcm=trimmed or pcm,
            vosk_text=vosk_text,
            partial_fallback=partial,
        )
        self._sentence = SentenceChunk()
        self._trailing_silence_ms = 0.0
        self._last_audio_t = time.monotonic()
        self.vosk.reset()

        if self.injector is not None:
            self._refine_q.put(snapshot)
            return None

        text = self._transcribe_sentence(snapshot.pcm, snapshot.vosk_text, snapshot.partial_fallback)
        return text or None


def run_wav_file(settings: Settings, wav_path: Path) -> PipelineResult:
    pcm = read_wav_pcm16(wav_path)
    pipeline = DictationPipeline(settings, injector=None)
    offset = 0
    partial_seen = ""
    committed: list[str] = []
    while offset < len(pcm):
        chunk = pcm[offset : offset + CHUNK_BYTES]
        offset += len(chunk)
        bubble, injected = pipeline.feed_pcm(chunk)
        if bubble:
            partial_seen = bubble
        if injected:
            committed.append(injected)
    trailing_silence = b"\x00\x00" * (SAMPLE_RATE * max(settings.sentence_pause_ms, 800) // 1000)
    tail_offset = 0
    while tail_offset < len(trailing_silence):
        chunk = trailing_silence[tail_offset : tail_offset + CHUNK_BYTES]
        tail_offset += len(chunk)
        bubble, injected = pipeline.feed_pcm(chunk)
        if bubble:
            partial_seen = bubble
        if injected:
            committed.append(injected)
    final = pipeline.finish_session()
    if committed:
        merged = " ".join(committed).strip()
        if merged:
            final = merged
    if not final and partial_seen:
        final = partial_seen
    vosk_only = pipeline.vosk.flush() or partial_seen
    pipeline.close()
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