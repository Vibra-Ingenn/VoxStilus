"""Microphone capture via sounddevice."""

from __future__ import annotations

import queue
import threading
from typing import Callable

import sounddevice as sd

from .constants import SAMPLE_RATE


class MicrophoneStream:
    def __init__(self, on_pcm: Callable[[bytes], None]):
        self.on_pcm = on_pcm
        self._q: queue.Queue[bytes] = queue.Queue()
        self._stream: sd.RawInputStream | None = None
        self._thread: threading.Thread | None = None
        self._running = False

    def start(self) -> None:
        if self._running:
            return
        self._running = True

        def callback(indata, frames, _time_info, status):  # noqa: ANN001
            if status:
                pass
            self._q.put(bytes(indata))

        self._stream = sd.RawInputStream(
            samplerate=SAMPLE_RATE,
            channels=1,
            dtype="int16",
            blocksize=SAMPLE_RATE // 10,
            callback=callback,
        )
        self._stream.start()

        def worker() -> None:
            while self._running:
                try:
                    chunk = self._q.get(timeout=0.2)
                except queue.Empty:
                    continue
                self.on_pcm(chunk)

        self._thread = threading.Thread(target=worker, daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._running = False
        if self._stream is not None:
            self._stream.stop()
            self._stream.close()
            self._stream = None
        if self._thread is not None:
            self._thread.join(timeout=1.0)
            self._thread = None