"""Hold-to-talk key listener via evdev (Wayland-safe)."""

from __future__ import annotations

import logging
import threading
from pathlib import Path
from typing import Callable

from evdev import InputDevice, ecodes, list_devices

logger = logging.getLogger(__name__)


def _key_const(name: str) -> int:
    if not name.startswith("KEY_"):
        name = f"KEY_{name}"
    return getattr(ecodes, name, ecodes.KEY_RIGHTALT)


class HoldToTalkListener:
    def __init__(
        self,
        key_name: str,
        on_press: Callable[[], None],
        on_release: Callable[[], None],
        device_glob: str | None = None,
    ):
        self.target_code = _key_const(key_name)
        self.on_press = on_press
        self.on_release = on_release
        self.device_glob = device_glob
        self._held = False
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None

    def start(self) -> None:
        if self._thread and self._thread.is_alive():
            return
        self._stop.clear()
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        if self._thread:
            self._thread.join(timeout=2.0)

    def _pick_devices(self) -> list[Path]:
        paths = [Path(p) for p in list_devices()]
        keyboards = []
        for path in paths:
            try:
                dev = InputDevice(str(path))
            except OSError:
                continue
            caps = dev.capabilities(verbose=False)
            if ecodes.EV_KEY not in caps:
                continue
            if self.target_code not in caps[ecodes.EV_KEY]:
                continue
            if self.device_glob and self.device_glob not in dev.name:
                continue
            keyboards.append(path)
        return keyboards

    def _run(self) -> None:
        paths = self._pick_devices()
        if not paths:
            logger.error("No evdev keyboard with %s found", self.target_code)
            return
        devices = []
        try:
            for path in paths:
                dev = InputDevice(str(path))
                dev.grab()
                devices.append(dev)
        except OSError as exc:
            logger.warning("evdev grab failed (%s); listening without grab", exc)
            devices = [InputDevice(str(p)) for p in paths]

        from select import select

        while not self._stop.is_set():
            r, _, _ = select([d.fd for d in devices], [], [], 0.25)
            for dev in devices:
                if dev.fd not in r:
                    continue
                for event in dev.read():
                    if event.type != ecodes.EV_KEY:
                        continue
                    if event.code != self.target_code:
                        continue
                    if event.value == 1 and not self._held:
                        self._held = True
                        self.on_press()
                    elif event.value == 0 and self._held:
                        self._held = False
                        self.on_release()

        for dev in devices:
            try:
                dev.ungrab()
            except OSError:
                pass
            dev.close()