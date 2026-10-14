# roadie relay board — receives JSON commands over USB serial,
# translates to binary protocol, forwards over UART to HID board
import time
import json
import board
import busio
import usb_cdc
import digitalio
import microcontroller
import neopixel_write
from protocol import (
    MSG_SIZE, RESP_SIZE, BAUD, STATUS_OK,
    CMD_AUDIO, AUDIO_MAGIC, AUDIO_LEAD, AUDIO_MAX, AUDIO_HDR,
    CMD_PING, CMD_KEY_TYPE, CMD_RESET,
    pack_msg, pack_key_press, pack_key_release, pack_key_type,
    pack_mouse_move, pack_mouse_click, pack_mouse_press, pack_mouse_release,
    pack_mouse_scroll, pack_touch,
)

# NeoPixel setup
power = digitalio.DigitalInOut(board.NEOPIXEL_POWER)
power.direction = digitalio.Direction.OUTPUT
power.value = True

pixel_pin = digitalio.DigitalInOut(board.NEOPIXEL)
pixel_pin.direction = digitalio.Direction.OUTPUT

RED = bytearray([0, 255, 0])  # GRB order
OFF = bytearray([0, 0, 0])

# UART to HID board on TX/RX pins (D6/D7)
uart = busio.UART(board.TX, board.RX, baudrate=BAUD, timeout=1)

# USB serial data port (separate from REPL)
data = usb_cdc.data
data.timeout = 0.1        # bound reads so a truncated frame can't hang the loop
data.write_timeout = 0.2  # never block if the host isn't reading

seq = 0
line_buf = ""
last_ping = time.monotonic()
HEARTBEAT_INTERVAL = 5

# flush stale UART bytes
while uart.read(64):
    pass

print("roadie relay board ready (uart + serial)")
neopixel_write.neopixel_write(pixel_pin, RED)


def next_seq():
    global seq
    s = seq
    seq = (seq + 1) & 0xFF
    return s


def send_and_recv(msg, wait=True):
    """Send a 32-byte message over UART, optionally read 2-byte response."""
    while uart.in_waiting:
        uart.read(uart.in_waiting)
    uart.write(msg)
    if not wait:
        return STATUS_OK, msg[1]  # assume success, return seq from message
    # Read until the ack for *this* message: a late ack from an earlier
    # fire-and-forget command can arrive after the flush above and must not
    # be taken as ours — the echoed seq exists to catch exactly that.
    deadline = time.monotonic() + 1.0
    while time.monotonic() < deadline:
        resp = uart.read(RESP_SIZE)
        if not resp or len(resp) != RESP_SIZE:
            return None, None  # timeout
        if resp[1] == msg[1]:
            return resp[0], resp[1]  # status, echo_seq
        # stale ack from an earlier command; keep reading
    return None, None


def translate(d):
    """Translate a JSON command dict to one or more binary messages."""
    cmd = d.get("cmd")
    if cmd == "ping":
        return [pack_msg(CMD_PING, next_seq())]
    elif cmd == "type":
        text = d.get("text", "")
        msgs = []
        for i in range(0, max(1, len(text)), 29):
            msgs.append(pack_key_type(next_seq(), text[i:i + 29]))
        return msgs
    elif cmd == "key_press":
        # "quiet": wait for the HID board's ack (sent after the USB report
        # is out) so the next command can't overtake the keystroke, but skip
        # the 100 ms LED blink — typing speed must not depend on it (#5).
        return [("quiet", pack_key_press(next_seq(), d["keycode"]))]
    elif cmd == "key_release":
        return [("quiet", pack_key_release(next_seq(), d["keycode"]))]
    elif cmd == "mouse_move":
        return [("nowait", pack_mouse_move(next_seq(), d["x"], d["y"]))]
    elif cmd == "mouse_click":
        return [pack_mouse_click(next_seq(), d.get("buttons", 1))]
    elif cmd == "mouse_press":
        return [pack_mouse_press(next_seq(), d.get("buttons", 1))]
    elif cmd == "mouse_release":
        return [pack_mouse_release(next_seq(), d.get("buttons", 1))]
    elif cmd == "mouse_scroll":
        return [("nowait", pack_mouse_scroll(next_seq(), d["amount"]))]
    elif cmd == "touch":
        contacts = []
        for c in d.get("contacts", []):
            contacts.append((c["id"], 1 if c["tip"] else 0, c["x"], c["y"]))
        return [("nowait", pack_touch(next_seq(), contacts))]
    elif cmd == "reset_hid":
        return [pack_msg(CMD_RESET, next_seq())]
    else:
        print("unknown cmd: %s" % cmd)
        return []


def process_command(line):
    """Parse a JSON line and send the resulting messages."""
    try:
        d = json.loads(line)
    except ValueError:
        print("json err: %s" % line)
        return

    if d.get("cmd") == "reset_self":
        microcontroller.reset()
        return  # unreachable, but clear

    msgs = translate(d)
    status = None
    for i, item in enumerate(msgs):
        if i > 0:
            time.sleep(0.05)  # inter-chunk delay for multi-message command
        if isinstance(item, tuple):
            mode, msg = item  # "nowait" or "quiet"
        else:
            mode, msg = "blink", item
        status, echo = send_and_recv(msg, wait=(mode != "nowait"))
        if mode != "blink":
            pass  # no LED blink: fire-and-forget, or waited-but-unblinked keys
        elif status == STATUS_OK:
            neopixel_write.neopixel_write(pixel_pin, OFF)
            time.sleep(0.1)
            neopixel_write.neopixel_write(pixel_pin, RED)
        elif status is None:
            neopixel_write.neopixel_write(pixel_pin, OFF)

    # Ping gets a reply on the data port so the host can verify the
    # UART round-trip without a console reader (prints were removed —
    # they block when nothing drains the console).
    if d.get("cmd") == "ping":
        try:
            data.write(b'{"ok":true}\n' if status == STATUS_OK else b'{"ok":false}\n')
        except OSError:
            pass  # host not reading; drop the reply rather than stall


def read_exact(n, deadline=0.5):
    """Read exactly n bytes from the data port, or None on timeout."""
    buf = bytearray()
    t0 = time.monotonic()
    while len(buf) < n:
        chunk = data.read(min(n - len(buf), data.in_waiting or 1))
        if chunk:
            buf += chunk
        elif time.monotonic() - t0 > deadline:
            return None
    return buf


def forward_audio():
    """Read one audio frame from the data port and forward it over UART.

    Called after the 0x00 lead-in byte. No ACK — audio is fire-and-forget.
    """
    hdr = read_exact(AUDIO_HDR)
    if hdr is None or hdr[0] != CMD_AUDIO or hdr[1] != AUDIO_MAGIC:
        return  # garbage; drop and resync on the next lead-in
    n = (hdr[2] << 8) | hdr[3]
    if n == 0 or n > AUDIO_MAX:
        return
    payload = read_exact(n)
    if payload is None:
        return
    uart.write(hdr)
    uart.write(payload)


while True:
    # check for incoming JSON commands or audio frames on USB data port
    if data.in_waiting:
        byte = data.read(1)
        if byte == bytes([AUDIO_LEAD]):
            forward_audio()
            last_ping = time.monotonic()
            line_buf = ""
        elif byte == b"\n" or byte == b"\r":
            if line_buf.strip():
                process_command(line_buf.strip())
                last_ping = time.monotonic()
            line_buf = ""
        else:
            line_buf += byte.decode("ascii", "ignore")

    # heartbeat ping when idle
    elif time.monotonic() - last_ping >= HEARTBEAT_INTERVAL:
        status, echo = send_and_recv(pack_msg(CMD_PING, next_seq()))
        if status == STATUS_OK:
            neopixel_write.neopixel_write(pixel_pin, OFF)
            time.sleep(0.1)
            neopixel_write.neopixel_write(pixel_pin, RED)
        else:
            neopixel_write.neopixel_write(pixel_pin, OFF)
        last_ping = time.monotonic()
