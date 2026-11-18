"""Main entry: tray + hold-to-talk + status overlay."""

from __future__ import annotations

import logging
import sys
import threading

from .config import load_settings
from .hotkey import HoldToTalkListener
from .overlay import OverlayController
from .session import SessionController
from .tray_app import TrayApp

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s: %(message)s",
)
logger = logging.getLogger("talk_to_type")


def main() -> None:
    settings = load_settings()
    overlay = OverlayController()
    session = SessionController(settings, overlay)
    stop = threading.Event()

    hotkey = HoldToTalkListener(
        settings.hold_key,
        on_press=session.on_press,
        on_release=session.on_release,
    )
    hotkey.start()

    tray = TrayApp(on_quit=stop.set)
    tray.run_detached()

    def ui_loop() -> None:
        while not stop.is_set():
            overlay.pump()
            stop.wait(0.05)
        hotkey.stop()
        tray.stop()

    threading.Thread(target=ui_loop, daemon=True).start()
    try:
        while not stop.is_set():
            stop.wait(0.5)
    except KeyboardInterrupt:
        stop.set()
    logger.info("talk-to-type exiting")
    sys.exit(0)


if __name__ == "__main__":
    main()