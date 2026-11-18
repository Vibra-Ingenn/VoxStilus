"""Generate test WAVs and run offline pipeline proof."""

from __future__ import annotations

import argparse
import shutil
import subprocess
import sys
import urllib.request
import wave
from pathlib import Path

from talk_to_type.config import Settings, load_settings
from talk_to_type.constants import SAMPLE_RATE, data_home
from talk_to_type.models_download import ensure_vosk_model
from talk_to_type.pipeline import run_wav_file

VOSK_DEMO_WAV = (
    "https://github.com/alphacep/vosk-api/raw/master/python/example/test.wav"
)


def _resample_to_16k_mono(src: Path, dest: Path) -> None:
    subprocess.run(
        [
            "ffmpeg",
            "-y",
            "-i",
            str(src),
            "-ar",
            str(SAMPLE_RATE),
            "-ac",
            "1",
            "-sample_fmt",
            "s16",
            str(dest),
        ],
        check=True,
        capture_output=True,
    )


def _espeak_to_wav(text: str, out: Path, amplitude: int = 100) -> bool:
    exe = shutil.which("espeak-ng") or shutil.which("espeak")
    if not exe:
        return False
    subprocess.run(
        [exe, "-s", "160", "-a", str(amplitude), "-w", str(out), text],
        check=True,
        capture_output=True,
    )
    _resample_to_16k_mono(out, out)
    return True


def _download_demo(base: Path) -> Path:
    base.parent.mkdir(parents=True, exist_ok=True)
    urllib.request.urlretrieve(VOSK_DEMO_WAV, base)
    return base


def generate_fixtures(dest: Path) -> list[Path]:
    dest.mkdir(parents=True, exist_ok=True)
    paths: list[Path] = []

    if shutil.which("espeak-ng") or shutil.which("espeak"):
        specs = [
            ("01_short.wav", "Hello Janus", 100),
            ("02_adeptus.wav", "Adeptus Camini and Vibe Engine", 100),
            ("03_terminus.wav", "Terminus parses documents", 100),
            ("04_quiet.wav", "quiet microphone test", 35),
            (
                "05_long_25s.wav",
                " ".join(
                    [
                        "This is a longer dictation test for talk to type.",
                        "We measure latency and never drop speech.",
                        "Janus Terminus Vibra Ingenn Adeptus Camini.",
                    ]
                    * 6
                ),
                100,
            ),
        ]
        for name, text, amp in specs:
            path = dest / name
            _espeak_to_wav(text, path, amplitude=amp)
            paths.append(path)
        return paths

    raw = dest / "_vosk_demo_src.wav"
    _download_demo(raw)
    _resample_to_16k_mono(raw, dest / "01_short.wav")
    paths.append(dest / "01_short.wav")
    for name in ("02_adeptus.wav", "03_terminus.wav"):
        shutil.copy(dest / "01_short.wav", dest / name)
        paths.append(dest / name)
    subprocess.run(
        [
            "ffmpeg",
            "-y",
            "-i",
            str(dest / "01_short.wav"),
            "-filter:a",
            "volume=0.12",
            str(dest / "04_quiet.wav"),
        ],
        check=True,
        capture_output=True,
    )
    paths.append(dest / "04_quiet.wav")
    subprocess.run(
        [
            "ffmpeg",
            "-y",
            "-stream_loop",
            "14",
            "-i",
            str(dest / "01_short.wav"),
            "-t",
            "25",
            "-ar",
            str(SAMPLE_RATE),
            "-ac",
            "1",
            str(dest / "05_long_25s.wav"),
        ],
        check=True,
        capture_output=True,
    )
    paths.append(dest / "05_long_25s.wav")
    raw.unlink(missing_ok=True)
    return paths


def pad_long_wav(path: Path, min_sec: float = 25.0) -> None:
    with wave.open(str(path), "rb") as wf:
        pcm = wf.readframes(wf.getnframes())
        rate = wf.getframerate()
    dur = len(pcm) / 2 / rate
    if dur >= min_sec:
        return
    extra = int((min_sec - dur) * rate) * 2
    with wave.open(str(path), "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(SAMPLE_RATE)
        wf.writeframes(pcm + (b"\x00" * extra))


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--fixtures", type=Path, default=data_home() / "test-fixtures")
    parser.add_argument("--skip-download", action="store_true")
    args = parser.parse_args()

    settings = load_settings()
    if not settings.vosk_model_dir:
        if args.skip_download and not Path(settings.resolved_vosk_dir()).is_dir():
            print("Vosk model missing", file=sys.stderr)
            sys.exit(2)
        dir_path = ensure_vosk_model()
        settings.vosk_model_dir = str(dir_path)
    if not Path(settings.resolved_whisper_path()).is_file():
        print("Whisper model missing:", settings.resolved_whisper_path(), file=sys.stderr)
        sys.exit(2)

    paths = generate_fixtures(args.fixtures)
    pad_long_wav(args.fixtures / "05_long_25s.wav", 25.0)

    print("file\ttext\tfirst_partial_ms\tnever_empty")
    failures = 0
    max_first: float | None = None
    for path in sorted(paths):
        result = run_wav_file(settings, path)
        fp = result.first_partial_ms
        if fp is not None:
            if max_first is None or fp > max_first:
                max_first = fp
        print(f"{path.name}\t{result.text!r}\t{fp}\t{result.never_empty}")
        if not result.never_empty:
            failures += 1
    if failures:
        sys.exit(1)
    if max_first is not None and max_first > 700:
        print(f"FAIL first partial latency {max_first:.0f} ms > 700 ms", file=sys.stderr)
        sys.exit(1)
    print(f"PASS all texts non-empty; worst first_partial_ms={max_first}")


if __name__ == "__main__":
    main()