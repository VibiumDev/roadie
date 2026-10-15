#!/usr/bin/env python3
"""
Audio path test: stream a 1 kHz tone through the relay and verify it
comes out the HID board's USB microphone.

  Pi → serial audio frames → Relay → UART → HID → usb_audio mic → Pi (arecord)

Usage:
    python3 board/test_audio.py

Requires both boards plugged into the Pi via USB and connected over
UART (TX/RX/GND). The HID board's mic must enumerate (ALSA card
"RoadieHID" — CircuitPython 10.3+ with usb_audio enabled in boot.py).
"""

import glob
import math
import os
import struct
import subprocess
import sys
import time
import wave

import serial

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "shared"))
from protocol import AUDIO_LEAD, AUDIO_RATE, pack_audio  # noqa: E402

TONE_HZ = 1000
TONE_SECS = 2.5
FRAME_SAMPLES = 160  # 10 ms at 16 kHz
WAV_PATH = "/tmp/roadie_audio_test.wav"


def make_tone():
    """Full tone as S16LE mono PCM."""
    n = int(AUDIO_RATE * TONE_SECS)
    amp = 12000
    return b"".join(
        struct.pack("<h", int(amp * math.sin(2 * math.pi * TONE_HZ * i / AUDIO_RATE)))
        for i in range(n)
    )


def analyze(path):
    """Return (rms, dominant_hz) of the loudest 1s window in the wav."""
    with wave.open(path) as w:
        raw = w.readframes(w.getnframes())
        rate = w.getframerate()
        nch = w.getnchannels()
    samples = struct.unpack("<%dh" % (len(raw) // 2), raw)[::nch]

    win = rate  # 1 second
    best_rms, best_off = 0.0, 0
    for off in range(0, max(1, len(samples) - win), rate // 4):
        chunk = samples[off:off + win]
        rms = math.sqrt(sum(s * s for s in chunk) / len(chunk))
        if rms > best_rms:
            best_rms, best_off = rms, off

    chunk = samples[best_off:best_off + win]
    crossings = sum(
        1 for a, b in zip(chunk, chunk[1:]) if (a < 0) != (b < 0)
    )
    hz = crossings * rate / (2.0 * len(chunk))
    return best_rms, hz


def playback_hint():
    """Say how to listen to the capture, including from another machine.

    aplay plays on whichever machine runs it, so over ssh the sound comes
    out of the Pi -- easy to mistake for an empty recording.
    """
    print(f"  🔊 Play it:  aplay {WAV_PATH}")
    if os.environ.get("SSH_CONNECTION"):
        user = os.environ.get("USER", "pi")
        host = os.uname().nodename
        print("     You're over ssh, so that plays on the Pi, not where you")
        print(f"     are. To listen locally:  scp {user}@{host}:{WAV_PATH} .")


def main():
    data_port = glob.glob("/dev/serial/by-id/usb-Adafruit_Roadie-Relay_*-if02")
    if not data_port:
        print("❌ Relay data port not found.")
        sys.exit(1)

    pcm = make_tone()
    data = serial.Serial(data_port[0], 115200, timeout=2)
    time.sleep(0.3)

    print(f"  🎙️  Recording {TONE_SECS + 1.5:.0f}s from ALSA card RoadieHID...")
    rec = subprocess.Popen(
        ["arecord", "-q", "-D", "plughw:RoadieHID", "-f", "S16_LE",
         "-r", str(AUDIO_RATE), "-c", "1",
         "-d", str(int(TONE_SECS + 2)), WAV_PATH],
    )
    time.sleep(0.8)  # let capture spin up

    print(f"  🔊 Streaming {TONE_SECS}s of {TONE_HZ} Hz tone in 10 ms frames...")
    frame_bytes = FRAME_SAMPLES * 2
    t0 = time.monotonic()
    for i in range(0, len(pcm), frame_bytes):
        data.write(bytes([AUDIO_LEAD]) + pack_audio(pcm[i:i + frame_bytes]))
        # pace to real time so the board's playout queue never overflows
        target = t0 + (i // frame_bytes + 1) * FRAME_SAMPLES / AUDIO_RATE
        delay = target - time.monotonic()
        if delay > 0:
            time.sleep(delay)

    rec.wait()
    data.close()

    rms, hz = analyze(WAV_PATH)
    print(f"  📈 Loudest 1s window: RMS={rms:.0f}, dominant ≈{hz:.0f} Hz")

    if rms < 500:
        print("  ❌ No signal captured — audio path is not working.")
        print(f"     The capture is still at {WAV_PATH} if you want to hear")
        print("     what did arrive.")
        playback_hint()
        sys.exit(1)
    if abs(hz - TONE_HZ) > 60:
        print(f"  ❌ Signal present but wrong frequency (expected ~{TONE_HZ} Hz).")
        playback_hint()
        sys.exit(1)
    print(f"  ✅ Tone came through the full path. Capture: {WAV_PATH}")
    playback_hint()


if __name__ == "__main__":
    main()
