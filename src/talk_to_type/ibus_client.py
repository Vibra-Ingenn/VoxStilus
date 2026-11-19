"""Client side of talk-to-type IBus engine socket (pre-edit + commit)."""

from __future__ import annotations

import logging
import socket
import time
from collections.abc import Callable

from .ibus_protocol import (
    CMD_CLEAR,
    CMD_COMMIT,
    CMD_PING,
    CMD_PREEDIT,
    RESP_ERROR,
    RESP_NO_ENGINE,
    RESP_NO_FOCUS,
    RESP_OK,
    RESP_TIMEOUT,
    RESP_UNAUTHORIZED,
    inject_socket_path,
)

logger = logging.getLogger(__name__)

OnPreeditAck = Callable[[str, float], None]


class TalkToTypeIbusClient:
    def __init__(self, on_preedit_ack: OnPreeditAck | None = None):
        self.on_preedit_ack = on_preedit_ack
        self._last_preedit = ""

    def ping(self) -> str | None:
        return self._exchange(CMD_PING, b"", recv_size=64)

    def set_preedit(self, text: str, *, force: bool = False) -> bool:
        if not force and text == self._last_preedit:
            return True
        self._last_preedit = text
        if not text:
            ok = self._cmd_ok(CMD_CLEAR, b"")
        else:
            ok = self._cmd_ok(CMD_PREEDIT, text.encode("utf-8"))
        if ok and self.on_preedit_ack is not None:
            self.on_preedit_ack(text, time.time())
        return ok

    def clear_preedit(self) -> bool:
        self._last_preedit = ""
        return self._cmd_ok(CMD_CLEAR, b"")

    def commit(self, text: str) -> tuple[bool, str]:
        if not text or not text.strip():
            return True, RESP_OK.decode()
        self._last_preedit = ""
        resp = self._exchange(CMD_COMMIT, text.encode("utf-8"))
        if resp == RESP_OK.decode():
            return True, resp
        if resp in (RESP_NO_FOCUS.decode(), RESP_NO_ENGINE.decode(), RESP_ERROR.decode(), RESP_TIMEOUT.decode()):
            return False, resp or "ERROR"
        return False, resp or "ERROR"

    def _cmd_ok(self, prefix: bytes, payload: bytes) -> bool:
        resp = self._exchange(prefix, payload)
        return resp == RESP_OK.decode()

    def _exchange(self, prefix: bytes, payload: bytes, recv_size: int = 32) -> str | None:
        path = inject_socket_path()
        if not path.is_socket():
            logger.warning("talk-to-type IBus socket missing at %s", path)
            return None
        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
                sock.settimeout(5.0)
                sock.connect(str(path))
                sock.sendall(prefix + payload)
                data = sock.recv(recv_size)
                return data.decode("utf-8", errors="replace") if data else None
        except OSError as exc:
            logger.error("IBus socket I/O failed: %s", exc)
            return None


def delivery_ack_ok(response: str | None) -> bool:
    return response == RESP_OK.decode()