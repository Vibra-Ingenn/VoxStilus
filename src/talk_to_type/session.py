"""One hold-to-talk session."""

from __future__ import annotations

import logging
import threading
import time

from .audio_capture import MicrophoneStream
from .config import Settings
from .inject import build_injector, switch_vocalinux_engine_for_commit
from .overlay import OverlayController
from .pipeline import DictationPipeline, pcm_rms
from .recordings import add_recording, prune_old
from .sounds import ensure_default_sounds, play

logger = logging.getLogger(__name__)


class SessionController:
    def __init__(self, settings: Settings, overlay: OverlayController):
        self.settings = settings
        self.overlay = overlay
        self._active = False
        self._pipeline: DictationPipeline | None = None
        self._mic: MicrophoneStream | None = None
        self._started = 0.0
        self._lock = threading.Lock()
        self._start_sound, self._stop_sound = ensure_default_sounds()
        prune_old(settings.recording_retention_hours)

    def on_press(self) -> None:
        with self._lock:
            if self._active:
                return
            self._active = True
            self._started = time.monotonic()
            switch_vocalinux_engine_for_commit()
            injector = build_injector(self.settings.use_ibus_injection)
            self._pipeline = DictationPipeline(self.settings, injector=injector)
            self.overlay.recording()
            play(self._start_sound)

            def on_pcm(pcm: bytes) -> None:
                if not self._active or self._pipeline is None:
                    return
                bubble, _ = self._pipeline.feed_pcm(pcm)
                if bubble:
                    self.overlay.partial(bubble)
                elapsed = time.monotonic() - self._started
                if elapsed >= 1.0 and not self._pipeline._got_audio:  # noqa: SLF001
                    if pcm_rms(pcm) < self.settings.mic_silence_threshold:
                        self.overlay.warn("Mic not picking you up")

            self._mic = MicrophoneStream(on_pcm)
            try:
                self._mic.start()
            except Exception as exc:
                logger.error("Microphone start failed: %s", exc)
                self.overlay.warn("Mic not picking you up")
                self._active = False
                return

            def mic_watch() -> None:
                time.sleep(1.0)
                if (
                    self._active
                    and self._pipeline is not None
                    and not self._pipeline._got_audio  # noqa: SLF001
                ):
                    self.overlay.warn("Mic not picking you up")

            threading.Thread(target=mic_watch, daemon=True).start()

    def on_release(self) -> None:
        with self._lock:
            if not self._active:
                return
            self._active = False
            if self._mic is not None:
                self._mic.stop()
                self._mic = None
            text = ""
            pcm = b""
            if self._pipeline is not None:
                text = self._pipeline.finish_session()
                pcm = self._pipeline.session_pcm()
            duration = time.monotonic() - self._started
            if pcm:
                add_recording(pcm, text, duration)
            play(self._stop_sound)
            self.overlay.idle()
            self._pipeline = None