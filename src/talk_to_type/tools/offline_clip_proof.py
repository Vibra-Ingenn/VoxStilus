"""Offline proof for Paul's dictation clips (mock injector, no IBus session)."""

from __future__ import annotations

import json
import os
import sys
import time
from pathlib import Path

from talk_to_type.audio_resample import resample_wav_to_16k
from talk_to_type.config import Settings
from talk_to_type.history import append_failed
from talk_to_type.inject import build_injector
from talk_to_type.ibus_protocol import preedit_trace_path
from talk_to_type.pipeline import CHUNK_BYTES, DictationPipeline
from talk_to_type.recording_injector import RecordingInjector
from talk_to_type.tools.live_ibus_proof import EXPECTED, word_error_count
from talk_to_type.whisper_refine import read_wav_pcm16


def run_clip_offline(settings: Settings, wav_path: Path) -> dict:
    injector = RecordingInjector()
    pipeline = DictationPipeline(settings, injector=injector)
    pcm = read_wav_pcm16(resample_wav_to_16k(wav_path))
    t0 = time.time()
    offset = 0
    chunk_s = CHUNK_BYTES / (16000 * 2)
    bytes_per_s = 16000 * 2

    def _realtime_sleep(fed_bytes: int) -> None:
        fed_s = fed_bytes / bytes_per_s
        target_s = time.time() - t0
        if fed_s >= target_s:
            time.sleep(chunk_s)

    fast_through = int(1.5 * bytes_per_s)
    while offset < len(pcm):
        chunk = pcm[offset : offset + CHUNK_BYTES]
        offset += len(chunk)
        pipeline.feed_pcm(chunk)
        if offset >= fast_through:
            _realtime_sleep(offset)

    pipeline.finish_session()
    pipeline.drain_refine()
    pipeline.close()

    final_text = " ".join(injector.commits).strip()
    preedits = [ev for ev in injector.events if ev.kind == "preedit"]
    first_pre = preedits[0].t - t0 if preedits else None
    gaps: list[float] = []
    for i in range(1, len(preedits)):
        gaps.append(preedits[i].t - preedits[i - 1].t)
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


def failure_path_test() -> dict:
    sock = preedit_trace_path().parent / "inject.sock"
    backup = None
    if sock.is_socket():
        backup = sock.with_suffix(".bak")
        sock.rename(backup)
    try:
        inj = build_injector(use_ibus=True)
        ok = inj.commit("engine stopped test line")
        from talk_to_type.history import history_path

        has_row = False
        path = history_path()
        if path.is_file():
            has_row = "engine stopped test line" in path.read_text(encoding="utf-8")
        return {"commit_ok": ok, "history_saved": has_row}
    finally:
        if backup and backup.is_socket():
            if sock.exists():
                sock.unlink()
            backup.rename(sock)


def main() -> None:
    clip_dir = Path.home() / "Dev/Vibecut/my_voice/raw/dictation"
    settings = Settings()
    results = []
    for name in EXPECTED:
        wav = clip_dir / name
        if not wav.is_file():
            print(f"MISSING {wav}")
            sys.exit(2)
        results.append(run_clip_offline(settings, wav))

    print("=== offline clip results ===")
    all_ok = True
    for row in results:
        ok_first = row["first_preedit_s"] is not None and row["first_preedit_s"] <= 1.0
        ok_gap = row["max_preedit_gap_s"] <= 0.5 or row["preedit_count"] <= 1
        ok_wer = row["word_errors"] == 0
        all_ok = all_ok and ok_first and ok_gap and ok_wer
        print(json.dumps(row, indent=2))
        print(f"  gates: first<={1.0}s:{ok_first} gap<={0.5}s:{ok_gap} wer=0:{ok_wer}")

    fail = failure_path_test()
    print("=== failure path ===")
    print(json.dumps(fail, indent=2))
    all_ok = all_ok and (not fail["commit_ok"]) and fail["history_saved"]
    print("ALL_PASS" if all_ok else "FAIL")
    sys.exit(0 if all_ok else 1)


if __name__ == "__main__":
    main()