"""Text injection for KDE Wayland (talk-to-type IBus pre-edit or wtype fallback)."""

from __future__ import annotations

import logging
import os
import shutil
import socket
import subprocess
import time
from abc import ABC, abstractmethod
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path

from .constants import VOCALINUX_IBUS_SOCKET
from .history import append_failed
from .ibus_client import TalkToTypeIbusClient
from .ibus_runtime import ensure_ready

logger = logging.getLogger(__name__)

DeliveryFailedHandler = Callable[[str, str], None]


class TextInjector(ABC):
    @abstractmethod
    def set_preedit(self, text: str, *, force: bool = False) -> bool:
        ...

    @abstractmethod
    def commit(self, text: str) -> bool:
        ...

    def inject(self, text: str) -> bool:
        return self.commit(text)


class VocalinuxIbusInjector(TextInjector):
    """Legacy commit-only socket to Vocalinux (fallback when our engine is down)."""

    def __init__(self, socket_path: Path | None = None):
        self.socket_path = socket_path or VOCALINUX_IBUS_SOCKET

    def set_preedit(self, text: str, *, force: bool = False) -> bool:
        return bool(text)

    def ping(self) -> str | None:
        if not self.socket_path.is_socket():
            return None
        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
                sock.settimeout(2.0)
                sock.connect(str(self.socket_path))
                sock.sendall(b"\x00PING")
                return sock.recv(64).decode("utf-8", errors="replace")
        except OSError as exc:
            logger.debug("Vocalinux IBus ping failed: %s", exc)
            return None

    def commit(self, text: str) -> bool:
        if not text or not text.strip():
            return True
        if not self.socket_path.is_socket():
            logger.warning("Vocalinux IBus socket missing at %s", self.socket_path)
            return False
        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
                sock.settimeout(5.0)
                sock.connect(str(self.socket_path))
                sock.sendall(text.encode("utf-8"))
                response = sock.recv(64).decode("utf-8", errors="replace")
                if response == "OK":
                    return True
                logger.error("Vocalinux IBus inject response: %s", response)
                return False
        except OSError as exc:
            logger.error("Vocalinux IBus inject failed: %s", exc)
            return False


class WtypeInjector(TextInjector):
    """Wayland typing via wtype (no IBus engine required)."""

    def set_preedit(self, text: str, *, force: bool = False) -> bool:
        return True

    def commit(self, text: str) -> bool:
        if not text or not text.strip():
            return True
        exe = shutil.which("wtype")
        if not exe:
            return False
        try:
            subprocess.run(
                [exe, text],
                check=True,
                timeout=30,
                env=os.environ.copy(),
            )
            return True
        except (subprocess.SubprocessError, OSError) as exc:
            logger.error("wtype failed: %s", exc)
            return False


@dataclass
class LiveIbusInjector(TextInjector):
    """Live pre-edit + commit through talk-to-type's IBus engine."""

    on_delivery_failed: DeliveryFailedHandler | None = None
    _client: TalkToTypeIbusClient = field(init=False)
    _ready: bool = field(default=False, init=False)

    def __post_init__(self) -> None:
        self._client = TalkToTypeIbusClient()

    def prepare(self) -> None:
        self._ready = ensure_ready()
        if not self._ready:
            logger.warning("talk-to-type IBus engine not ready")

    def set_preedit(self, text: str, *, force: bool = False) -> bool:
        if not self._ready:
            return False
        return self._client.set_preedit(text, force=force)

    def clear_preedit(self) -> bool:
        return self._client.clear_preedit()

    def commit(self, text: str) -> bool:
        if not text or not text.strip():
            return True
        if not self._ready:
            self._record_failure(text, "NO_ENGINE")
            return False
        ok, reason = self._client.commit(text)
        if not ok:
            self._record_failure(text, reason or "NO_ENGINE")
        return ok

    def _record_failure(self, text: str, reason: str) -> None:
        append_failed(text, reason)
        if self.on_delivery_failed is not None:
            self.on_delivery_failed(text, reason)


class CompositeInjector(TextInjector):
    def __init__(
        self,
        prefer_ibus: bool = True,
        on_delivery_failed: DeliveryFailedHandler | None = None,
    ):
        self.live = LiveIbusInjector(on_delivery_failed=on_delivery_failed)
        self.vocalinux = VocalinuxIbusInjector()
        self.wtype = WtypeInjector()
        self.prefer_ibus = prefer_ibus
        self._use_live = False

    def prepare(self) -> None:
        if self.prefer_ibus:
            self.live.prepare()
            ping = self.live._client.ping()
            self._use_live = ping is not None and ping != "NO_ENGINE"

    def set_preedit(self, text: str, *, force: bool = False) -> bool:
        if self._use_live:
            return self.live.set_preedit(text, force=force)
        return True

    def commit(self, text: str) -> bool:
        if not text or not text.strip():
            return True
        if self.prefer_ibus:
            if self._use_live:
                return self.live.commit(text)
            ok, reason = self.live._client.commit(text)
            if ok:
                return True
            reason = reason or "NO_ENGINE"
            self.live._record_failure(text, reason)
            return False
        if self.vocalinux.ping() in ("OK", "FOCUSED"):
            if self.vocalinux.commit(text):
                return True
        ok = self.wtype.commit(text)
        if not ok:
            append_failed(text, "WTYPE_FAIL")
        return ok


def build_injector(
    use_ibus: bool = True,
    on_delivery_failed: DeliveryFailedHandler | None = None,
) -> CompositeInjector:
    inj = CompositeInjector(prefer_ibus=use_ibus, on_delivery_failed=on_delivery_failed)
    inj.prepare()
    return inj


def switch_talk_to_type_engine_for_session() -> bool:
    return ensure_ready()


def switch_vocalinux_engine_for_commit() -> bool:
    """Deprecated: kept for tray re-paste paths that still call Vocalinux."""
    if not shutil.which("ibus"):
        return False
    try:
        subprocess.run(
            ["ibus", "engine", "vocalinux"],
            capture_output=True,
            timeout=5,
            env=os.environ.copy(),
        )
        time.sleep(0.15)
        return True
    except (subprocess.SubprocessError, OSError):
        return False