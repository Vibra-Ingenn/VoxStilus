"""Text injection for KDE Wayland (IBus socket or wtype fallback)."""

from __future__ import annotations

import logging
import os
import shutil
import socket
import subprocess
import time
from abc import ABC, abstractmethod
from pathlib import Path

from .constants import VOCALINUX_IBUS_SOCKET

logger = logging.getLogger(__name__)


class TextInjector(ABC):
    @abstractmethod
    def inject(self, text: str) -> bool:
        ...


class VocalinuxIbusInjector(TextInjector):
    """Send UTF-8 commits to the Vocalinux IBus engine Unix socket.

    Protocol studied from Vocalinux 0.18.6 (AGPL-3.0); this file does not copy
    their source — only speaks the same socket format so Paul's existing engine
    keeps working without restarting Vocalinux.
    """

    def __init__(self, socket_path: Path | None = None):
        self.socket_path = socket_path or VOCALINUX_IBUS_SOCKET

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
            logger.debug("IBus ping failed: %s", exc)
            return None

    def inject(self, text: str) -> bool:
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
                logger.error("IBus inject response: %s", response)
                return False
        except OSError as exc:
            logger.error("IBus inject failed: %s", exc)
            return False


class WtypeInjector(TextInjector):
    """Wayland typing via wtype (no IBus engine required)."""

    def inject(self, text: str) -> bool:
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


class CompositeInjector(TextInjector):
    def __init__(self, prefer_ibus: bool = True):
        self.ibus = VocalinuxIbusInjector()
        self.wtype = WtypeInjector()
        self.prefer_ibus = prefer_ibus

    def inject(self, text: str) -> bool:
        if self.prefer_ibus:
            if self.ibus.ping() in ("OK", "FOCUSED"):
                if self.ibus.inject(text):
                    return True
        if self.wtype.inject(text):
            return True
        return self.ibus.inject(text)


def build_injector(use_ibus: bool = True) -> TextInjector:
    return CompositeInjector(prefer_ibus=use_ibus)


def switch_vocalinux_engine_for_commit() -> bool:
    """Best-effort: activate vocalinux engine before inject (matches their flow)."""
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