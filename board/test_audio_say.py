#!/usr/bin/env python3
"""
End-to-end audio injection test: drive /audio-in/play (and /audio-in/say
when a TTS engine is installed) and verify the audio comes out the HID
board's USB microphone.

  HTTP POST → roadie server → ffmpeg/TTS → paced playback → relay serial
            → UART → HID mic → Pi (arecord)

Usage (server must be running, both boards connected):
    .venv/bin/python board/test_audio_say.py [http://host:8080] [--sounds-dir DIR]

The tone file is written into the server's sounds dir (default
~/roadie/sounds — pass --sounds-dir if the server runs with a different
--sounds-dir). Needs the HID board plugged into the Pi so the ALSA card
"RoadieHID" enumerates.
"""

import json
import os
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.request
import wave

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from test_audio import make_tone, analyze, TONE_HZ, TONE_SECS  # noqa: E402
from protocol import AUDIO_RATE  # noqa: E402  (path added by test_audio)

WAV_PATH = "/tmp/roadie_audio_say_test.wav"
TONE_NAME = "roadie-e2e-tone.wav"


def api(base, method, path, body=None):
    """Call the server; returns (status, parsed JSON or None)."""
    req = urllib.request.Request(
        base + path, method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.status, json.loads(resp.read() or "null")
    except urllib.error.HTTPError as e:
        return e.code, None


def record(seconds, path):
    """Start arecord from the HID board's mic; returns the process."""
    return subprocess.Popen(
        ["arecord", "-q", "-D", "plughw:RoadieHID", "-f", "S16_LE",
         "-r", str(AUDIO_RATE), "-c", "1", "-d", str(seconds), path],
    )


def write_tone(path):
    """Write the shared test tone as a WAV file the server can play."""
    pcm = make_tone()
    with wave.open(path, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(AUDIO_RATE)
        w.writeframes(pcm)


def test_play(base, sounds_dir):
    tone_path = os.path.join(sounds_dir, TONE_NAME)
    os.makedirs(sounds_dir, exist_ok=True)
    write_tone(tone_path)
    try:
        code, sounds = api(base, "GET", "/audio-in/sounds")
        if code != 200 or TONE_NAME not in (sounds or {}).get("sounds", []):
            print(f"  ❌ {TONE_NAME} not listed by /audio-in/sounds — is the "
                  f"server's --sounds-dir {sounds_dir}?")
            return False

        print(f"  🎙️  Recording {int(TONE_SECS + 2)}s from ALSA card RoadieHID...")
        rec = record(int(TONE_SECS + 2), WAV_PATH)
        time.sleep(0.8)  # let capture spin up

        print(f"  🔊 POST /audio-in/play {TONE_NAME}")
        code, _ = api(base, "POST", "/audio-in/play", {"name": TONE_NAME})
        if code != 200:
            print(f"  ❌ /audio-in/play returned {code}")
            rec.wait()
            return False

        time.sleep(0.5)
        code, st = api(base, "GET", "/audio-in/status")
        if code != 200 or not (st or {}).get("playing"):
            print(f"  ❌ /audio-in/status does not show playback: {st}")
        elif st.get("source") != TONE_NAME:
            print(f"  ❌ status source = {st.get('source')!r}, want {TONE_NAME!r}")
        rec.wait()

        rms, hz = analyze(WAV_PATH)
        print(f"  📈 Loudest 1s window: RMS={rms:.0f}, dominant ≈{hz:.0f} Hz")
        if rms < 500:
            print("  ❌ No signal captured — play path is not working.")
            return False
        if abs(hz - TONE_HZ) > 60:
            print(f"  ❌ Signal present but wrong frequency (expected ~{TONE_HZ} Hz).")
            return False
        print(f"  ✅ /audio-in/play came through the full path. Capture: {WAV_PATH}")
        return True
    finally:
        os.remove(tone_path)


def test_say(base):
    """Speech can't be frequency-checked; assert playback starts and audio
    lands on the mic. Skipped (not failed) when no TTS engine is installed."""
    code, _ = api(base, "POST", "/audio-in/say",
                  {"text": "roadie end to end say test, one two three four five"})
    if code == 500:
        print("  ⚠️  /audio-in/say reports no TTS engine — skipping say test.")
        return True
    if code != 200:
        print(f"  ❌ /audio-in/say returned {code}")
        return False

    # TTS synthesis takes a few seconds before playback starts.
    for _ in range(30):
        _, st = api(base, "GET", "/audio-in/status")
        if (st or {}).get("playing"):
            break
        time.sleep(0.5)
    else:
        print("  ❌ say never reached playing state.")
        return False

    print("  🎙️  Recording 4s of speech from the mic...")
    rec = record(4, WAV_PATH)
    rec.wait()
    api(base, "POST", "/audio-in/stop")

    rms, _ = analyze(WAV_PATH)
    print(f"  📈 Speech RMS={rms:.0f}")
    if rms < 300:
        print("  ❌ No speech captured — say path is not working.")
        return False
    print("  ✅ /audio-in/say audible through the full path.")
    return True


def main():
    args = [a for a in sys.argv[1:]]
    sounds_dir = os.path.expanduser("~/roadie/sounds")
    if "--sounds-dir" in args:
        i = args.index("--sounds-dir")
        sounds_dir = args[i + 1]
        del args[i:i + 2]
    base = args[0] if args else "http://127.0.0.1:8080"

    ok = test_play(base, sounds_dir)
    ok = test_say(base) and ok
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
