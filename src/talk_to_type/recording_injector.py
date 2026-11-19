"""In-process injector that records pre-edit and commit events with timestamps."""

from __future__ import annotations

import time
from dataclasses import dataclass, field

from .inject import TextInjector


@dataclass
class RecordedEvent:
    kind: str
    text: str
    t: float


@dataclass
class RecordingInjector(TextInjector):
    commits: list[str] = field(default_factory=list)
    events: list[RecordedEvent] = field(default_factory=list)

    def set_preedit(self, text: str, *, force: bool = False) -> bool:
        if text:
            self.events.append(RecordedEvent(kind="preedit", text=text, t=time.time()))
        return True

    def commit(self, text: str) -> bool:
        if not text or not text.strip():
            return True
        self.events.append(RecordedEvent(kind="commit", text=text, t=time.time()))
        self.commits.append(text.strip())
        return True