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

# The five-note motif from Close Encounters of the Third Kind: re, mi, do,
# do an octave down, sol. A sequence beats a single tone as a test -- it
# checks that notes arrive in the right order and that none are dropped,
# rather than only that some sound got through.
# Name, frequency, and how long to hold it. The last note is sustained, the
# way it is in the film.
MOTIF = (
    ("D5", 587.33, 0.40),
    ("E5", 659.25, 0.40),
    ("C5", 523.25, 0.40),
    ("C4", 261.63, 0.40),
    ("G4", 392.00, 1.10),
)
GAP_SECS = 0.15
TONE_SECS = sum(secs for _, _, secs in MOTIF) + len(MOTIF) * GAP_SECS
FRAME_SAMPLES = 160  # 10 ms at 16 kHz
WAV_PATH = "/tmp/roadie_audio_test.wav"


def make_tone():
    """The motif as S16LE mono PCM, notes separated by short silences."""
    amp = 12000
    fade = int(AUDIO_RATE * 0.005)  # 5 ms, so note edges don't click
    out = bytearray()
    for _, hz, secs in MOTIF:
        n = int(AUDIO_RATE * secs)
        for i in range(n):
            level = amp
            if i < fade:
                level = amp * i / fade
            elif i > n - fade:
                level = amp * (n - i) / fade
            out += struct.pack(
                "<h", int(level * math.sin(2 * math.pi * hz * i / AUDIO_RATE)))
        out += b"\x00\x00" * int(AUDIO_RATE * GAP_SECS)
    return bytes(out)


def _pitch(chunk, rate):
    """Frequency of a chunk, from its zero-crossing rate."""
    if len(chunk) < 2:
        return 0.0
    crossings = sum(1 for a, b in zip(chunk, chunk[1:]) if (a < 0) != (b < 0))
    return crossings * rate / (2.0 * len(chunk))


def analyze(path):
    """Return (peak_rms, [(hz, rms), ...]) for the notes heard in the wav.

    Notes are found by energy rather than by assuming where they start: the
    recording begins before the stream does, so their position shifts.
    """
    with wave.open(path) as w:
        raw = w.readframes(w.getnframes())
        rate = w.getframerate()
        nch = w.getnchannels()
    samples = struct.unpack("<%dh" % (len(raw) // 2), raw)[::nch]

    hop = max(1, int(rate * 0.02))  # 20 ms
    levels = []
    for off in range(0, len(samples) - hop, hop):
        chunk = samples[off:off + hop]
        levels.append(math.sqrt(sum(s * s for s in chunk) / len(chunk)))
    if not levels:
        return 0.0, []

    peak = max(levels)
    if peak <= 0:
        return 0.0, []

    # Group runs of loud hops into notes, ignoring anything too short to be
    # one (a click between notes, say).
    threshold = peak * 0.25
    min_hops = max(1, int(0.15 / 0.02))
    notes, start = [], None
    for i, level in enumerate(levels + [0.0]):
        if level >= threshold and start is None:
            start = i
        elif level < threshold and start is not None:
            if i - start >= min_hops:
                a, b = start * hop, i * hop
                # Trim the edges, where the fades blur the pitch.
                pad = (b - a) // 5
                seg = samples[a + pad:b - pad]
                rms = math.sqrt(sum(s * s for s in seg) / max(1, len(seg)))
                notes.append((_pitch(seg, rate), rms))
            start = None
    return peak, notes


def playback_hint():
    """Say how to listen to the capture, including from another machine.

    aplay plays on whichever machine runs it, so over ssh the sound comes
    out of the Pi -- easy to mistake for an empty recording.
    """
    if subprocess.run(["pgrep", "-x", "pipewire"],
                      capture_output=True).returncode == 0:
        # aplay goes straight to ALSA, which on PipeWire cannot reach a
        # Bluetooth speaker; pw-play follows the default output.
        player = f"pw-play {WAV_PATH}"
    else:
        player = f"aplay {WAV_PATH}"
    print(f"  🔊 Play it:  {player}")
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

    # Round up: truncating here eats the margin, and the note that would be
    # clipped is the sustained one at the end.
    record_secs = math.ceil(TONE_SECS + 2)
    print(f"  🎙️  Recording {record_secs}s from ALSA card RoadieHID...")
    rec = subprocess.Popen(
        ["arecord", "-q", "-D", "plughw:RoadieHID", "-f", "S16_LE",
         "-r", str(AUDIO_RATE), "-c", "1",
         "-d", str(record_secs), WAV_PATH],
    )
    time.sleep(0.8)  # let capture spin up

    print(f"  🔊 Streaming {TONE_SECS:.1f}s — "
          f"{' '.join(n for n, _, _ in MOTIF)} — in 10 ms frames...")
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

    peak, heard = analyze(WAV_PATH)

    if peak < 500:
        print("  ❌ No signal captured — audio path is not working.")
        print(f"     The capture is still at {WAV_PATH} if you want to hear")
        print("     what did arrive.")
        playback_hint()
        sys.exit(1)

    print(f"  📈 Heard {len(heard)} of {len(MOTIF)} notes:")
    ok = len(heard) == len(MOTIF)
    for i, (name, want, secs) in enumerate(MOTIF):
        if i < len(heard):
            got, note_rms = heard[i]
            close = abs(got - want) <= want * 0.06
            ok = ok and close
            print(f"     {'✅' if close else '❌'} {name:<3} "
                  f"expected {want:6.1f} Hz, heard {got:6.1f} Hz "
                  f"({secs:.2f}s, RMS {note_rms:.0f})")
        else:
            ok = False
            print(f"     ❌ {name:<3} expected {want:6.1f} Hz, not heard")

    if not ok:
        print("  ❌ The motif did not arrive intact.")
        playback_hint()
        sys.exit(1)
    print(f"  ✅ All {len(MOTIF)} notes came through in order. "
          f"Capture: {WAV_PATH}")
    playback_hint()


if __name__ == "__main__":
    main()
