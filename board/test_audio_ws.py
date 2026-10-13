#!/usr/bin/env python3
"""
Full-stack audio test: stream a 1 kHz tone through the Go server's
/audio-in WebSocket and verify it comes out the HID board's USB mic.

  ws client → roadie server → relay serial → UART → HID mic → Pi (arecord)

Usage (server must be running, both boards connected):
    .venv/bin/python board/test_audio_ws.py [ws://host:8080/audio-in]
"""

import asyncio
import os
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from test_audio import make_tone, analyze, TONE_HZ, TONE_SECS  # noqa: E402
from protocol import AUDIO_RATE  # noqa: E402  (path added by test_audio)

import websockets  # noqa: E402

WAV_PATH = "/tmp/roadie_audio_ws_test.wav"
MSG_SAMPLES = 320  # 20 ms per WebSocket message, like the browser sends


async def stream_tone(url, pcm):
    async with websockets.connect(url) as ws:
        msg_bytes = MSG_SAMPLES * 2
        t0 = time.monotonic()
        sent = 0
        for i in range(0, len(pcm), msg_bytes):
            await ws.send(pcm[i:i + msg_bytes])
            sent += msg_bytes // 2
            delay = t0 + sent / AUDIO_RATE - time.monotonic()
            if delay > 0:
                await asyncio.sleep(delay)


def main():
    url = sys.argv[1] if len(sys.argv) > 1 else "ws://127.0.0.1:8080/audio-in"
    pcm = make_tone()

    print(f"  🎙️  Recording from ALSA card RoadieHID...")
    rec = subprocess.Popen(
        ["arecord", "-q", "-D", "plughw:RoadieHID", "-f", "S16_LE",
         "-r", str(AUDIO_RATE), "-c", "1",
         "-d", str(int(TONE_SECS + 2)), WAV_PATH],
    )
    time.sleep(0.8)

    print(f"  🔊 Streaming {TONE_SECS}s of {TONE_HZ} Hz tone via {url} ...")
    asyncio.run(stream_tone(url, pcm))
    rec.wait()

    rms, hz = analyze(WAV_PATH)
    print(f"  📈 Loudest 1s window: RMS={rms:.0f}, dominant ≈{hz:.0f} Hz")

    if rms < 500:
        print("  ❌ No signal captured — audio path is not working.")
        sys.exit(1)
    if abs(hz - TONE_HZ) > 60:
        print(f"  ❌ Signal present but wrong frequency (expected ~{TONE_HZ} Hz).")
        sys.exit(1)
    print(f"  ✅ Tone came through the full stack. Capture: {WAV_PATH}")


if __name__ == "__main__":
    main()
