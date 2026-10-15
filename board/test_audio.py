#!/usr/bin/env python3
"""
Audio path test: stream a tune to the target and verify it comes out the
HID board's USB microphone.

The same audio can be sent three ways, so a failure says which layer broke:

  --via serial   Pi → relay's serial port → UART → HID → mic → Pi
  --via api      Pi → roadie's /audio-in WebSocket → (as above)
  --via browser  a real browser's microphone → /audio-in → (as above)

Usage:
    python3 board/test_audio.py --via serial

Requires both boards plugged into the Pi via USB and connected over
UART (TX/RX/GND). The HID board's mic must enumerate (ALSA card
"RoadieHID" — CircuitPython 10.3+ with usb_audio enabled in boot.py).
"""

import argparse
import base64
import glob
import math
import os
import socket
import struct
import subprocess
import sys
import time
import wave
from pathlib import Path

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
    ("G4", 392.00, 1.70),
)
GAP_SECS = 0.15
TONE_SECS = sum(secs for _, _, secs in MOTIF) + len(MOTIF) * GAP_SECS
FRAME_SAMPLES = 160  # 10 ms at 16 kHz
# One capture per path, so a browser run cannot overwrite the serial one and
# the two can be listened to side by side.
WAV_PATHS = {
    "serial": "/tmp/roadie_audio_serial.wav",
    "api": "/tmp/roadie_audio_api.wav",
    "browser": "/tmp/roadie_audio_browser.wav",
}
WAV_PATH = WAV_PATHS["serial"]  # replaced at startup by --via

VERBOSE = False


def detail(*args):
    """Print running commentary, which --verbose turns on.

    A passing test only needs to say so. Anything that failed prints in
    full regardless, since that is when the detail is worth having.
    """
    if VERBOSE:
        print(*args)


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


def _pitch(chunk, rate, lo=150.0, hi=900.0):
    """Fundamental of a chunk, by autocorrelation.

    Counting zero crossings is cheaper but reads low whenever a window
    catches part of the quiet either side of a note, and the edges of a note
    are not crisp on every path -- a browser hands audio over in chunks of
    its own. Autocorrelation looks for the repeat period instead, which is
    the same whether or not the window is trimmed neatly.
    """
    n = len(chunk)
    if n < 64:
        return 0.0
    mean = sum(chunk) / n
    x = [s - mean for s in chunk]
    best_lag, best = 0, 0.0
    for lag in range(int(rate / hi), int(rate / lo) + 1):
        if lag >= n:
            break
        total = 0.0
        for i in range(n - lag):
            total += x[i] * x[i + lag]
        if total > best:
            best, best_lag = total, lag
    return rate / best_lag if best_lag else 0.0


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
        return 0.0, [], []

    peak = max(levels)
    if peak <= 0:
        return 0.0, [], []

    # Group runs of loud hops into notes, ignoring anything too short to be
    # one (the brief unsteady moment between two notes, say).
    threshold = peak * 0.25
    min_hops = max(1, int(0.15 / 0.02))
    notes, start = [], None
    for i, level in enumerate(levels + [0.0]):
        if level >= threshold and start is None:
            start = i
        elif level < threshold and start is not None:
            if i - start >= min_hops:
                a, b = start * hop, i * hop
                pad = (b - a) // 5  # keep clear of the edges
                seg = samples[a + pad:b - pad]
                rms = math.sqrt(sum(s * s for s in seg) / max(1, len(seg)))
                # Autocorrelation over a slice is enough and keeps it quick.
                middle = seg[:int(rate * 0.2)] if len(seg) > rate * 0.2 else seg
                notes.append((_pitch(middle, rate), rms,
                              a / rate, b / rate))
            start = None

    # A dropout splits one note into two pieces at the same pitch. Join them
    # back up, but keep the lost time: the tune did arrive, and separately
    # it had a gap in it, and those are two different things to report.
    joined, dropouts = [], []
    for note in notes:
        hz, rms, began, ended = note
        if joined:
            prev_hz, prev_rms, prev_began, prev_ended = joined[-1]
            gap = began - prev_ended
            if gap < 0.5 and abs(hz - prev_hz) <= max(20.0, hz * 0.06):
                dropouts.append(gap)
                joined[-1] = (prev_hz, max(rms, prev_rms), prev_began, ended)
                continue
        joined.append(note)
    return peak, [(hz, rms) for hz, rms, _, _ in joined], dropouts


class AudioSocket:
    """Minimal WebSocket client for /audio-in.

    Only what this needs: an upgrade handshake and masked binary frames.
    roadie never replies on this endpoint, so no read path is required.
    """

    def __init__(self, host, port, path="/audio-in"):
        key = base64.b64encode(os.urandom(16)).decode()
        self.sock = socket.create_connection((host, port), timeout=10)
        self.sock.sendall(
            f"GET {path} HTTP/1.1\r\n"
            f"Host: {host}:{port}\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\n"
            "Sec-WebSocket-Version: 13\r\n\r\n".encode()
        )
        response = b""
        while b"\r\n\r\n" not in response:
            chunk = self.sock.recv(4096)
            if not chunk:
                raise OSError("server closed during the handshake")
            response += chunk
        if b"101" not in response.split(b"\r\n")[0]:
            raise OSError(f"upgrade refused: {response.splitlines()[0]!r}")

    def send(self, payload):
        """Send one binary frame."""
        header = bytearray([0x82])  # FIN + binary
        n = len(payload)
        if n < 126:
            header.append(0x80 | n)
        elif n < 1 << 16:
            header.append(0x80 | 126)
            header += struct.pack(">H", n)
        else:
            header.append(0x80 | 127)
            header += struct.pack(">Q", n)
        mask = os.urandom(4)
        header += mask
        masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        self.sock.sendall(bytes(header) + masked)

    def close(self):
        try:
            self.sock.sendall(b"\x88\x80" + os.urandom(4))  # close frame
        except OSError:
            pass
        self.sock.close()


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
    if os.environ.get("SSH_CONNECTION"):
        user = os.environ.get("USER", "pi")
        host = os.uname().nodename
        detail("     You're over ssh, so playing it here would come out of"
               " the Pi.")
        print(f"     listen:  scp {user}@{host}:{WAV_PATH} .")
    else:
        print(f"     listen:  {player}")


def pace(index, t0):
    """Hold each frame to real time, so the board's queue can't overflow."""
    target = t0 + (index + 1) * FRAME_SAMPLES / AUDIO_RATE
    delay = target - time.monotonic()
    if delay > 0:
        time.sleep(delay)


def stream_serial(pcm):
    """Straight at the relay's serial port: no roadie, no browser."""
    ports = glob.glob("/dev/serial/by-id/usb-Adafruit_Roadie-Relay_*-if02")
    if not ports:
        print("  ❌ Relay data port not found.")
        sys.exit(1)
    port = serial.Serial(ports[0], 115200, timeout=2)
    time.sleep(0.3)
    frame_bytes = FRAME_SAMPLES * 2
    t0 = time.monotonic()
    for i in range(0, len(pcm), frame_bytes):
        port.write(bytes([AUDIO_LEAD]) + pack_audio(pcm[i:i + frame_bytes]))
        pace(i // frame_bytes, t0)
    port.close()


def stream_api(pcm, base):
    """Through roadie's /audio-in WebSocket, as the viewer's mic does."""
    host, _, port = base.split("//", 1)[1].partition(":")
    ws = AudioSocket(host, int(port or 80))
    frame_bytes = FRAME_SAMPLES * 2
    t0 = time.monotonic()
    try:
        for i in range(0, len(pcm), frame_bytes):
            ws.send(pcm[i:i + frame_bytes])
            pace(i // frame_bytes, t0)
    finally:
        ws.close()


def stream_browser(pcm, base, notes, record_secs):
    """Through a real browser's microphone.

    Chromium can play a wav file in place of a microphone, so the "voice"
    is the same motif and still arrives the way a person's would: captured
    by getUserMedia, converted to PCM in the page, and sent to roadie over
    the /audio-in WebSocket.
    """
    import http.server
    import shutil
    import tempfile
    import threading

    page = Path(__file__).resolve().parent / "e2e" / "mic-page.html"
    if not page.exists():
        print(f"  ❌ Missing {page}")
        sys.exit(1)

    # Chromium starts playing the fake capture file as soon as the page is
    # granted the microphone, which is before it has connected and started
    # sending. A second of lead-in silence means the motif still begins
    # after we are recording. The trailing silence stops the file looping
    # back round into the tune mid-capture.
    lead = b"\x00\x00" * int(AUDIO_RATE * 1.0)
    fake_wav = Path(tempfile.mkdtemp(prefix="roadie-mic-")) / "voice.wav"
    with wave.open(str(fake_wav), "w") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(AUDIO_RATE)
        w.writeframes(lead + pcm + b"\x00\x00" * int(AUDIO_RATE * 8))

    events = []
    html = page.read_bytes()

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass

        def do_GET(self):
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(html)))
            self.end_headers()
            self.wfile.write(html)

        def do_POST(self):
            length = int(self.headers.get("Content-Length") or 0)
            body = self.rfile.read(length)
            self.send_response(204)
            self.end_headers()
            events.append(body)

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    host_port = base.split("//", 1)[1]
    url = (f"http://127.0.0.1:{server.server_address[1]}/"
           f"?roadie={host_port}")

    browser = shutil.which("chromium") or shutil.which("chromium-browser")
    if not browser:
        print("  ❌ chromium not found (needed to drive a real microphone)")
        sys.exit(1)

    from test_e2e import reap_old_profiles
    reap_old_profiles("roadie-mic-profile-")
    reap_old_profiles("roadie-mic-")
    profile = tempfile.mkdtemp(prefix="roadie-mic-profile-")
    detail(f"  🔊 Browser microphone plays {TONE_SECS:.1f}s — {notes}")
    proc = subprocess.Popen(
        [browser, "--kiosk", f"--user-data-dir={profile}",
         "--no-first-run", "--no-default-browser-check", "--noerrdialogs",
         # Without these Chromium reaches for the login keyring and the
         # desktop puts up an "Unlock Login Keyring" prompt, which steals
         # focus from whatever is being tested.
         "--password-store=basic", "--use-mock-keychain",
         # Grant the mic without a prompt, and make it play our file.
         "--use-fake-ui-for-media-stream",
         "--use-fake-device-for-media-stream",
         f"--use-file-for-fake-audio-capture={fake_wav}",
         "--autoplay-policy=no-user-gesture-required",
         url],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    rec = None
    try:
        detail("  ⏳ Waiting for the browser to open its microphone...")
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline and not events:
            time.sleep(0.2)
        if not events:
            print("  ⚠️  The page never reported in; is a display available?")
            return None
        # Now that audio is actually flowing, start listening.
        rec = record(record_secs)
        rec.wait()
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
        server.shutdown()
        shutil.rmtree(profile, ignore_errors=True)
        shutil.rmtree(fake_wav.parent, ignore_errors=True)
    return rec


def record(record_secs):
    """Start capturing from the HID board's microphone."""
    detail(f"  🎙️  Recording {record_secs}s from ALSA card RoadieHID...")
    rec = subprocess.Popen(
        ["arecord", "-q", "-D", "plughw:RoadieHID", "-f", "S16_LE",
         "-r", str(AUDIO_RATE), "-c", "1",
         "-d", str(record_secs), WAV_PATH],
    )
    time.sleep(0.8)  # let capture spin up
    return rec


def main():
    parser = argparse.ArgumentParser(
        description="Check the audio path to the target's microphone.")
    parser.add_argument("--via", default="serial",
                        choices=["serial", "api", "browser"],
                        help="which layer to send the audio through")
    parser.add_argument("--roadie", default="http://127.0.0.1:8080",
                        help="base URL of a running roadie (api/browser)")
    parser.add_argument("-v", "--verbose", action="store_true",
                        help="show each step and every note, not just the result")
    args = parser.parse_args()

    global VERBOSE
    VERBOSE = args.verbose

    global WAV_PATH
    WAV_PATH = WAV_PATHS[args.via]

    pcm = make_tone()
    # Round up: truncating here eats the margin, and the note that would be
    # clipped is the sustained one at the end.
    record_secs = math.ceil(TONE_SECS + 2)
    notes = " ".join(n for n, _, _ in MOTIF)

    roadie = None
    if args.via in ("api", "browser"):
        sys.path.insert(0, str(Path(__file__).resolve().parent))
        from test_e2e import Roadie
        roadie = Roadie(args.roadie, quiet=not VERBOSE)
        base = roadie.ensure()

    try:
        if args.via == "serial":
            detail(f"  🎯 Path: this script → relay serial → UART → HID → mic")
            rec = record(record_secs)
            detail(f"  🔊 Streaming {TONE_SECS:.1f}s — {notes} — in 10 ms frames...")
            stream_serial(pcm)
        elif args.via == "api":
            detail(f"  🎯 Path: this script → roadie /audio-in → relay → HID → mic")
            rec = record(record_secs)
            detail(f"  🔊 Streaming {TONE_SECS:.1f}s — {notes} — in 10 ms frames...")
            stream_api(pcm, base)
        else:
            detail(f"  🎯 Path: browser mic → roadie /audio-in → relay → HID → mic")
            # It records itself, once the browser is actually streaming.
            if stream_browser(pcm, base, notes, record_secs) is None:
                sys.exit(1)
            rec = None
        if rec is not None:
            rec.wait()
    finally:
        if roadie:
            roadie.stop()

    peak, heard, dropouts = analyze(WAV_PATH)

    if peak < 500:
        print("  ❌ No signal captured — audio path is not working.")
        print(f"     The capture is still at {WAV_PATH} if you want to hear")
        print("     what did arrive.")
        playback_hint()
        sys.exit(1)

    ok = len(heard) == len(MOTIF)
    rows = []
    for i, (name, want, secs) in enumerate(MOTIF):
        if i < len(heard):
            got, note_rms = heard[i]
            close = abs(got - want) <= want * 0.06
            ok = ok and close
            rows.append(f"     {'✅' if close else '❌'} {name:<3} "
                        f"expected {want:6.1f} Hz, heard {got:6.1f} Hz "
                        f"({secs:.2f}s, RMS {note_rms:.0f})")
        else:
            ok = False
            rows.append(f"     ❌ {name:<3} expected {want:6.1f} Hz, not heard")

    if not ok or VERBOSE:
        print(f"  📈 Heard {len(heard)} of {len(MOTIF)} notes:")
        for row in rows:
            print(row)

    # Report gaps whether or not they were enough to fail: a tune that
    # arrived with a hole in it is worth knowing about.
    lost = sum(dropouts)
    if dropouts:
        print(f"  ⚠️  {len(dropouts)} dropout(s) mid-tune, {lost * 1000:.0f} ms "
              f"lost — the audio had gaps in it")
    if lost > 0.5:
        ok = False
        print("  ❌ Too much audio was lost to call the path healthy.")

    if not ok:
        print("  ❌ The motif did not arrive intact.")
        playback_hint()
        sys.exit(1)
    print(f"  ✅ {args.via:<8} {len(MOTIF)}/{len(MOTIF)} notes   {WAV_PATH}")
    playback_hint()


if __name__ == "__main__":
    main()
