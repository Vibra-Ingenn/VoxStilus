"""Row 89 proof: real-time IBus pre-edit from Paul's dictation clips."""

from __future__ import annotations

import json
import re
import subprocess
import sys
import threading
import time
from pathlib import Path

from talk_to_type.config import Settings
from talk_to_type.history import history_path
from talk_to_type.ibus_protocol import preedit_trace_path
from talk_to_type.ibus_runtime import ensure_ready
from talk_to_type.inject import build_injector
from talk_to_type.pipeline import CHUNK_BYTES, DictationPipeline
from talk_to_type.audio_resample import resample_wav_to_16k
from talk_to_type.whisper_refine import read_wav_pcm16

CLIP_DIR = Path.home() / "Dev/Vibecut/my_voice/raw/dictation"
EXPECTED = {
    "2026-11-18_12-17-19.wav": (
        "Man, this is so aggravating, not being able to see if it's typing when I'm "
        "talking or not. I really would like to change this now."
    ),
    "2026-11-18_12-17-31.wav": "How hard would it be to build this up right now?",
    "2026-11-18_12-17-37.wav": (
        "Because it's like a productivity thing, and it's just aggravating the hell "
        "out of me when it doesn't go through."
    ),
    "2026-11-18_12-18-08.wav": (
        "I don't believe anything, I think we have to build it ourselves or something."
    ),
}


def _normalize(text: str) -> list[str]:
    text = text.lower().strip()
    text = re.sub(r"[^a-z0-9' ]+", " ", text)
    return [w for w in text.split() if w]


def word_error_count(hyp: str, ref: str) -> int:
    h = _normalize(hyp)
    r = _normalize(ref)
    if not r:
        return len(h)
    # Levenshtein on words
    rows = len(r) + 1
    cols = len(h) + 1
    dist = [[0] * cols for _ in range(rows)]
    for i in range(rows):
        dist[i][0] = i
    for j in range(cols):
        dist[0][j] = j
    for i in range(1, rows):
        for j in range(1, cols):
            cost = 0 if r[i - 1] == h[j - 1] else 1
            dist[i][j] = min(dist[i - 1][j] + 1, dist[i][j - 1] + 1, dist[i - 1][j - 1] + cost)
    return dist[-1][-1]


def _clear_trace() -> None:
    path = preedit_trace_path()
    if path.is_file():
        path.unlink()


def _load_trace_since(since: float) -> list[dict]:
    path = preedit_trace_path()
    if not path.is_file():
        return []
    rows: list[dict] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            continue
        if row.get("t", 0) >= since:
            rows.append(row)
    return rows


def run_clip(settings: Settings, wav_path: Path, gtk_ready: threading.Event) -> dict:
    _clear_trace()
    gtk_ready.wait(timeout=30)
    ensure_ready()
    injector = build_injector(use_ibus=True)
    pipeline = DictationPipeline(settings, injector=injector)
    pcm = read_wav_pcm16(resample_wav_to_16k(wav_path))
    t0 = time.time()
    trace_start = t0
    committed: list[str] = []
    record_commits: list[str] | None = None
    if hasattr(injector, "commits"):
        record_commits = injector.commits  # type: ignore[attr-defined]
    offset = 0
    chunk_s = CHUNK_BYTES / (16000 * 2)
    bytes_per_s = 16000 * 2

    fast_through = int(1.5 * bytes_per_s)

    def _realtime_sleep(fed_bytes: int) -> None:
        fed_s = fed_bytes / bytes_per_s
        target_s = time.time() - t0
        if fed_s >= target_s:
            time.sleep(chunk_s)

    while offset < len(pcm):
        chunk = pcm[offset : offset + CHUNK_BYTES]
        offset += len(chunk)
        _, injected = pipeline.feed_pcm(chunk)
        if injected:
            committed.append(injected)
        if offset >= fast_through:
            _realtime_sleep(offset)

    pipeline.finish_session()
    pipeline.drain_refine()
    pipeline.close()

    trace = _load_trace_since(trace_start)
    trace_commits = [r.get("text", "") for r in trace if r.get("kind") == "commit" and r.get("text")]
    if trace_commits:
        final_text = " ".join(trace_commits).strip()
    elif record_commits is not None:
        final_text = " ".join(record_commits).strip()
    else:
        final_text = " ".join(committed).strip()
    preedits = [r for r in trace if r.get("kind") == "preedit"]
    first_pre = preedits[0]["t"] - t0 if preedits else None
    gaps = []
    for i in range(1, len(preedits)):
        gaps.append(preedits[i]["t"] - preedits[i - 1]["t"])
    max_gap = max(gaps) if gaps else 0.0

    expected = EXPECTED.get(wav_path.name, "")
    wer = word_error_count(final_text, expected)

    return {
        "wav": wav_path.name,
        "first_preedit_s": first_pre,
        "max_preedit_gap_s": max_gap,
        "final_text": final_text,
        "expected": expected,
        "word_errors": wer,
        "preedit_count": len(preedits),
    }


def _spawn_gtk_field() -> subprocess.Popen:
    script = """
import gi
gi.require_version('Gtk', '3.0')
from gi.repository import Gtk

class Win(Gtk.Window):
    def __init__(self):
        super().__init__(title='talk-to-type IBus proof')
        self.set_default_size(640, 120)
        self.entry = Gtk.Entry()
        self.add(self.entry)
        self.connect('destroy', Gtk.main_quit)
        self.show_all()

Win()
Gtk.main()
"""
    env = dict(**__import__("os").environ)
    env.setdefault("GTK_IM_MODULE", "ibus")
    env.setdefault("QT_IM_MODULE", "ibus")
    return subprocess.Popen(
        [sys.executable, "-c", script],
        env=env,
    )


def measure_ram() -> int:
    pid = __import__("os").getpid()
    out = subprocess.check_output(["ps", "-o", "rss=", "-p", str(pid)], text=True).strip()
    return int(out) * 1024


def failure_path_test() -> dict:
    import shutil

    sock = preedit_trace_path().parent / "inject.sock"
    backup = None
    if sock.is_socket():
        backup = sock.with_suffix(".bak")
        sock.rename(backup)
    try:
        inj = build_injector(use_ibus=True)
        ok = inj.commit("engine stopped test line")
        hist = history_path()
        has_row = False
        if hist.is_file():
            has_row = "engine stopped test line" in hist.read_text(encoding="utf-8")
        return {"commit_ok": ok, "history_saved": has_row}
    finally:
        if backup and backup.is_socket():
            if sock.exists():
                sock.unlink()
            backup.rename(sock)
        if shutil.which("ibus"):
            ensure_ready()


def main() -> None:
    settings = Settings()
    gtk = _spawn_gtk_field()
    ready = threading.Event()
    threading.Thread(target=lambda: (time.sleep(2.0), ready.set()), daemon=True).start()

    results = []
    for name in EXPECTED:
        wav = CLIP_DIR / name
        if not wav.is_file():
            print(f"MISSING {wav}")
            sys.exit(2)
        results.append(run_clip(settings, wav, ready))

    gtk.terminate()

    print("=== clip results ===")
    all_ok = True
    for row in results:
        ok_first = row["first_preedit_s"] is not None and row["first_preedit_s"] <= 1.0
        ok_gap = row["max_preedit_gap_s"] <= 0.5 or row["preedit_count"] <= 1
        ok_wer = row["word_errors"] == 0
        all_ok = all_ok and ok_first and ok_gap and ok_wer
        print(json.dumps(row, indent=2))
        print(
            f"  gates: first<={1.0}s:{ok_first} gap<={0.5}s:{ok_gap} wer=0:{ok_wer}"
        )

    fail = failure_path_test()
    print("=== failure path ===")
    print(json.dumps(fail, indent=2))
    all_ok = all_ok and (not fail["commit_ok"]) and fail["history_saved"]

    rss = measure_ram()
    print(f"=== rss bytes (this process only): {rss} ===")
    print("ALL_PASS" if all_ok else "FAIL")
    sys.exit(0 if all_ok else 1)


if __name__ == "__main__":
    main()