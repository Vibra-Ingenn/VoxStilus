"""Status dot and live partial text bubble (PyQt6)."""

from __future__ import annotations

import sys
from enum import Enum

from PyQt6.QtCore import Qt, QTimer
from PyQt6.QtGui import QColor, QFont
from PyQt6.QtWidgets import QApplication, QLabel, QWidget


class DotState(Enum):
    IDLE = "idle"
    RECORDING = "recording"
    WARNING = "warning"


class StatusOverlay(QWidget):
    def __init__(self) -> None:
        super().__init__()
        self.setWindowFlags(
            Qt.WindowType.FramelessWindowHint
            | Qt.WindowType.WindowStaysOnTopHint
            | Qt.WindowType.Tool
        )
        self.setAttribute(Qt.WidgetAttribute.WA_TranslucentBackground)
        self.dot = QLabel(self)
        self.dot.setFixedSize(18, 18)
        self.dot.move(0, 0)
        self.bubble = QLabel(self)
        self.bubble.setWordWrap(True)
        self.bubble.setMaximumWidth(420)
        font = QFont()
        font.setPointSize(11)
        self.bubble.setFont(font)
        self.bubble.setStyleSheet(
            "background: rgba(20,20,24,210); color: #f2f2f2; padding: 8px; border-radius: 6px;"
        )
        self.bubble.hide()
        self.resize(440, 120)
        self._place_bottom_right()
        self.set_state(DotState.IDLE)

    def _place_bottom_right(self) -> None:
        screen = QApplication.primaryScreen()
        if screen is None:
            return
        geo = screen.availableGeometry()
        self.move(geo.right() - self.width() - 24, geo.bottom() - self.height() - 48)

    def set_state(self, state: DotState) -> None:
        color = {
            DotState.IDLE: "#888888",
            DotState.RECORDING: "#e02020",
            DotState.WARNING: "#e0a020",
        }[state]
        self.dot.setStyleSheet(
            f"background: {color}; border-radius: 9px; border: 1px solid #222;"
        )

    def set_partial_text(self, text: str) -> None:
        if text:
            self.bubble.setText(text)
            self.bubble.adjustSize()
            self.bubble.move(24, 0)
            self.bubble.show()
        else:
            self.bubble.hide()

    def set_warning(self, message: str) -> None:
        self.set_state(DotState.WARNING)
        self.set_partial_text(message)


class OverlayController:
    """Thread-safe bridge: schedule UI updates on the Qt main thread."""

    def __init__(self) -> None:
        self._app = QApplication.instance()
        if self._app is None:
            self._app = QApplication(sys.argv)
        self.widget = StatusOverlay()
        self.widget.show()

    def pump(self) -> None:
        self._app.processEvents()

    def idle(self) -> None:
        QTimer.singleShot(0, lambda: self.widget.set_state(DotState.IDLE))
        QTimer.singleShot(0, lambda: self.widget.set_partial_text(""))

    def recording(self) -> None:
        QTimer.singleShot(0, lambda: self.widget.set_state(DotState.RECORDING))

    def partial(self, text: str) -> None:
        QTimer.singleShot(0, lambda: self.widget.set_partial_text(text))

    def warn(self, msg: str) -> None:
        QTimer.singleShot(0, lambda: self.widget.set_warning(msg))