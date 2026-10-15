# roadie hid board — receives commands over UART, executes USB HID actions,
# and streams received audio frames out the USB microphone (voice input)
import time
import board
import busio
import digitalio
import neopixel_write
import usb_hid
import audiocore
from adafruit_hid.keyboard import Keyboard
from adafruit_hid.keyboard_layout_us import KeyboardLayoutUS
from absolute_mouse import Mouse as AbsoluteMouse
import microcontroller
from protocol import (
    MSG_SIZE, BAUD, STATUS_OK, STATUS_ERR,
    CMD_AUDIO, AUDIO_MAGIC, AUDIO_MAX, AUDIO_HDR, AUDIO_RATE,
    CMD_NOOP, CMD_PING, CMD_KEY_PRESS, CMD_KEY_RELEASE, CMD_KEY_TYPE,
    CMD_MOUSE_MOVE, CMD_MOUSE_CLICK, CMD_MOUSE_PRESS, CMD_MOUSE_RELEASE,
    CMD_MOUSE_SCROLL, CMD_TOUCH, CMD_RESET,
    unpack_msg, pack_resp,
)
from digitizer import Digitizer

try:
    import usb_audio
    mic = usb_audio.usb_microphone
except (ImportError, AttributeError):
    mic = None  # usb_audio not enabled in boot.py (CircuitPython < 10.3)

# NeoPixel setup
power = digitalio.DigitalInOut(board.NEOPIXEL_POWER)
power.direction = digitalio.Direction.OUTPUT
power.value = True

pixel_pin = digitalio.DigitalInOut(board.NEOPIXEL)
pixel_pin.direction = digitalio.Direction.OUTPUT

GREEN = bytearray([255, 0, 0])  # GRB order
OFF = bytearray([0, 0, 0])

# UART from relay board on TX/RX pins (D6/D7).
# Audio streams at ~32 KB/s: the short timeout keeps the mic queue pumping,
# and the large receive buffer absorbs frames that arrive while a chunk is
# being queued (the 64-byte default overflows in under a millisecond).
uart = busio.UART(board.TX, board.RX, baudrate=BAUD, timeout=0.005,
                  receiver_buffer_size=8192)

# USB HID devices
kbd = Keyboard(usb_hid.devices)
layout = KeyboardLayoutUS(kbd)
mouse = AbsoluteMouse(usb_hid.devices)
try:
    digitizer = Digitizer(usb_hid.devices)
except ValueError:
    digitizer = None

# receive buffer
recv_buf = bytearray(MSG_SIZE)

# Audio playout: a looping ring buffer behind the USB mic. Playing each
# received frame as its own RawSample leaves a few ms of silence at every
# restart (~25% gaps at 10 ms frames), so instead one looping sample plays
# a 200 ms ring forever and incoming PCM is copied in ahead of the read
# position. The writer starts half a ring ahead; the stream stops (and the
# ring is zeroed) once frames stop arriving, before the reader can run past
# the audio it was given and replay it.
RING_BYTES = 2 * 6400  # 400 ms of 16 kHz S16 mono
# How much real audio is queued ahead of the reader at any moment.
LEAD_BYTES = 2 * 1600  # 100 ms
# How long to wait before deciding the stream has ended. It has to outlast
# the gaps a real sender leaves: audio arriving over a WebSocket is not
# paced as evenly as audio written straight to the serial port, and a
# threshold tighter than that jitter makes playback stop and restart
# mid-tune -- heard as a break and then a fragment.
QUIET_SECS = 0.2
# Silence held ahead of the writer. If the reader ever runs past what it was
# given it meets this instead of whatever the ring held last time round, so
# an overrun is quiet rather than a replayed scrap of earlier audio. Keeping
# it filled costs one frame's worth of zeroing per frame, not a whole sweep.
GUARD_BYTES = 2 * 3200  # 200 ms
ring = bytearray(RING_BYTES)
ring_mv = memoryview(ring)
ring_sample = audiocore.RawSample(
    ring_mv.cast("h"), channel_count=1, sample_rate=AUDIO_RATE)
wpos = 0
streaming = False
last_frame_t = 0.0

print("roadie hid board ready (uart + hid)")
neopixel_write.neopixel_write(pixel_pin, GREEN)


def _zero_ring(at, count):
    """Blank count bytes at an offset, wrapping round the end."""
    end = at + count
    if end <= RING_BYTES:
        ring_mv[at:end] = b"\x00" * count
    else:
        first = RING_BYTES - at
        ring_mv[at:] = b"\x00" * first
        ring_mv[:end - RING_BYTES] = b"\x00" * (count - first)


def feed_audio(payload):
    """Copy one PCM frame into the ring, starting playback if needed."""
    global wpos, streaming, last_frame_t
    if not mic:
        return
    if not streaming:
        ring_mv[:] = bytes(RING_BYTES)
        wpos = LEAD_BYTES  # give the writer a head start
        mic.play(ring_sample, loop=True)
        streaming = True
    n = len(payload)
    end = wpos + n
    if end <= RING_BYTES:
        ring_mv[wpos:end] = payload
    else:
        k = RING_BYTES - wpos
        ring_mv[wpos:] = payload[:k]
        ring_mv[:end - RING_BYTES] = payload[k:]
    wpos = end % RING_BYTES
    # Keep the silence ahead of the writer topped up: the writer moved on by
    # n, so n bytes at the far edge of the guard need blanking again.
    _zero_ring((wpos + GUARD_BYTES) % RING_BYTES, n)
    last_frame_t = time.monotonic()


def pump_audio():
    """Stop playback once the stream has gone quiet."""
    global streaming
    if streaming and time.monotonic() - last_frame_t > QUIET_SECS:
        mic.stop()
        streaming = False


def read_exact(n, deadline=0.5):
    """Read exactly n bytes from UART, pumping audio while waiting.
    Returns None on timeout (caller drops the partial frame and resyncs)."""
    buf = bytearray()
    t0 = time.monotonic()
    while len(buf) < n:
        chunk = uart.read(n - len(buf))
        if chunk:
            buf += chunk
        elif time.monotonic() - t0 > deadline:
            return None
        pump_audio()
    return buf


def handle_command(cmd, seq, payload):
    """Execute a HID command. Returns status byte."""
    try:
        if cmd == CMD_NOOP:
            # An unpowered or idle peer leaves the line low, which arrives as
            # all-zero frames. Swallow them: printing here spams the console,
            # and a print with no reader draining it blocks the board.
            return STATUS_OK

        elif cmd == CMD_PING:
            return STATUS_OK

        elif cmd == CMD_KEY_TYPE:
            text = payload.decode("ascii")
            layout.write(text)
            return STATUS_OK

        elif cmd == CMD_KEY_PRESS:
            kbd.press(payload[0])
            return STATUS_OK

        elif cmd == CMD_KEY_RELEASE:
            kbd.release(payload[0])
            return STATUS_OK

        elif cmd == CMD_MOUSE_MOVE:
            x = (payload[0] << 8) | payload[1]
            y = (payload[2] << 8) | payload[3]
            mouse.move(x, y)
            return STATUS_OK

        elif cmd == CMD_MOUSE_CLICK:
            mouse.click(payload[0])
            return STATUS_OK

        elif cmd == CMD_MOUSE_PRESS:
            mouse.press(payload[0])
            return STATUS_OK

        elif cmd == CMD_MOUSE_RELEASE:
            mouse.release(payload[0])
            return STATUS_OK

        elif cmd == CMD_MOUSE_SCROLL:
            amount = payload[0]
            if amount > 127:
                amount -= 256  # unsigned byte to signed int8
            mouse.move(wheel=-amount)
            mouse.move(wheel=0)
            return STATUS_OK

        elif cmd == CMD_TOUCH:
            if not digitizer:
                return STATUS_ERR
            count = payload[0]
            contacts = []
            for i in range(count):
                off = 1 + i * 6
                cid = payload[off]
                tip = payload[off + 1]
                x = (payload[off + 2] << 8) | payload[off + 3]
                y = (payload[off + 4] << 8) | payload[off + 5]
                contacts.append((cid, tip, x, y))
            digitizer.touch(contacts)
            return STATUS_OK

        elif cmd == CMD_RESET:
            return STATUS_OK  # response sent by caller, then main loop resets

        else:
            print("unknown cmd: 0x%02x" % cmd)
            return STATUS_ERR

    except Exception as e:
        print("hid err: %s" % e)
        return STATUS_ERR


while True:
    pump_audio()
    first = uart.read(1)
    if not first:
        continue

    if first[0] == CMD_AUDIO:
        # variable-length audio frame: 0x50 0xA5 len_hi len_lo <PCM>
        hdr = read_exact(AUDIO_HDR - 1)
        if hdr is None or hdr[0] != AUDIO_MAGIC:
            continue  # desync: drop this byte and rescan
        n = (hdr[1] << 8) | hdr[2]
        if n == 0 or n > AUDIO_MAX or n % 2:
            continue
        payload = read_exact(n)
        if payload is None:
            continue
        feed_audio(payload)
        continue

    # fixed 32-byte command; first[0] is the cmd byte
    rest = read_exact(MSG_SIZE - 1)
    if rest is None:
        continue
    recv_buf[0] = first[0]
    recv_buf[1:] = rest
    cmd, seq, payload = unpack_msg(recv_buf)

    status = handle_command(cmd, seq, payload)
    uart.write(pack_resp(status, seq))

    if cmd == CMD_RESET:
        time.sleep(0.05)  # let UART flush
        microcontroller.reset()

    # skip LED blink for high-frequency commands to avoid back-pressure
    # (CMD_NOOP included: an undriven line delivers these continuously)
    if cmd not in (CMD_NOOP, CMD_KEY_PRESS, CMD_KEY_RELEASE, CMD_MOUSE_MOVE, CMD_MOUSE_SCROLL, CMD_TOUCH):
        neopixel_write.neopixel_write(pixel_pin, OFF)
        time.sleep(0.1)
        neopixel_write.neopixel_write(pixel_pin, GREEN)
