"""Tray icon: Settings, Recent, Quit."""

from __future__ import annotations

import logging
import subprocess
import webbrowser
from pathlib import Path

from PIL import Image, ImageDraw
import pystray

from .config import load_settings, save_settings, settings_path
from .inject import build_injector
from .recordings import load_index, prune_old

logger = logging.getLogger(__name__)


def _icon_image(recording: bool = False) -> Image.Image:
    img = Image.new("RGBA", (64, 64), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)
    color = (224, 32, 32, 255) if recording else (120, 120, 120, 255)
    draw.ellipse((8, 8, 56, 56), fill=color, outline=(30, 30, 30, 255))
    return img


class TrayApp:
    def __init__(self, on_quit):
        self.on_quit = on_quit
        self.icon = pystray.Icon(
            "talk-to-type",
            _icon_image(),
            "talk-to-type",
            menu=pystray.Menu(
                pystray.MenuItem("Settings", self._open_settings),
                pystray.MenuItem("Recent", pystray.Menu(self._recent_items)),
                pystray.MenuItem("Quit", self._quit),
            ),
        )

    def _open_settings(self, _icon, _item) -> None:
        path = settings_path()
        path.parent.mkdir(parents=True, exist_ok=True)
        if not path.is_file():
            save_settings(load_settings())
        try:
            subprocess.Popen(["xdg-open", str(path)])
        except OSError:
            webbrowser.open(f"file://{path}")

    def _recent_items(self):
        prune_old(load_settings().recording_retention_hours)
        items = []
        injector = build_injector(load_settings().use_ibus_injection)
        for entry in load_index()[:15]:
            label = f"{entry.id}: {entry.text[:48] or '(no text)'}"

            def make_handler(text=entry.text):
                def handler(_icon, _item):
                    if text:
                        injector.inject(text)

                return handler

            items.append(pystray.MenuItem(label, make_handler()))
        if not items:
            items.append(pystray.MenuItem("(no recordings yet)", lambda *_: None))
        return items

    def _quit(self, _icon, _item) -> None:
        self.icon.stop()
        self.on_quit()

    def run_detached(self) -> None:
        self.icon.run_detached()

    def stop(self) -> None:
        try:
            self.icon.stop()
        except Exception:
            pass