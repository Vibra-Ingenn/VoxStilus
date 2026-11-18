"""Save a PNG of the live partial bubble for reports."""

from __future__ import annotations

import os
import sys
from pathlib import Path

from PyQt6.QtCore import QTimer
from PyQt6.QtWidgets import QApplication

from talk_to_type.overlay import DotState, StatusOverlay


def main() -> None:
    out = Path(sys.argv[1] if len(sys.argv) > 1 else "overlay-proof.png")
    os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")
    app = QApplication([])
    widget = StatusOverlay()
    widget.set_state(DotState.RECORDING)
    widget.set_partial_text("Janus and Vibe Engine (live partial)")
    widget.grab().save(str(out))

    def quit() -> None:
        app.quit()

    QTimer.singleShot(50, quit)
    app.exec()
    print(out)


if __name__ == "__main__":
    main()