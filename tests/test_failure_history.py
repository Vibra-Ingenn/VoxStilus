"""Delivery failure must always append to history when IBus commit fails."""

from __future__ import annotations

from pathlib import Path

import pytest

from talk_to_type.history import history_path
from talk_to_type.ibus_protocol import preedit_trace_path
from talk_to_type.inject import build_injector


@pytest.fixture
def isolated_data(monkeypatch, tmp_path: Path):
    data = tmp_path / "data"
    monkeypatch.setenv("TALK_TO_TYPE_DATA_HOME", str(data))
    yield data


def test_commit_failure_appends_history(isolated_data):
    sock = preedit_trace_path().parent / "inject.sock"
    sock.parent.mkdir(parents=True, exist_ok=True)
    backup = None
    if sock.is_socket():
        backup = sock.with_suffix(".bak")
        sock.rename(backup)

    try:
        inj = build_injector(use_ibus=True)
        text = "engine stopped test line pytest"
        ok = inj.commit(text)
        assert ok is False
        hist = history_path()
        assert hist.is_file()
        assert text in hist.read_text(encoding="utf-8")
    finally:
        if backup and backup.is_socket():
            if sock.exists():
                sock.unlink()
            backup.rename(sock)