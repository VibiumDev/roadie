# shared protocol constants and helpers
# used by both relay and hid boards over UART

MSG_SIZE = 32
BAUD = 921600

# command types
CMD_NOOP          = 0x00
CMD_PING          = 0x01
CMD_PONG          = 0x02
CMD_KEY_PRESS     = 0x10
CMD_KEY_RELEASE   = 0x11
CMD_KEY_TYPE      = 0x12
CMD_MOUSE_MOVE    = 0x20
CMD_MOUSE_CLICK   = 0x21
CMD_MOUSE_PRESS   = 0x22
CMD_MOUSE_RELEASE = 0x23
CMD_MOUSE_SCROLL  = 0x24

CMD_TOUCH         = 0x30

CMD_RESET         = 0x40

# status codes (returned in 2-byte response)
STATUS_OK   = 0x00
STATUS_ERR  = 0x01
STATUS_BUSY = 0x02

RESP_SIZE = 2


def pack_msg(cmd, seq, payload=b""):
    """Pack a command into a MSG_SIZE-byte buffer."""
    buf = bytearray(MSG_SIZE)
    buf[0] = cmd
    buf[1] = seq
    buf[2] = len(payload)
    buf[3:3 + len(payload)] = payload
    return buf


def unpack_msg(buf):
    """Unpack a MSG_SIZE-byte buffer into (cmd, seq, payload)."""
    cmd = buf[0]
    seq = buf[1]
    length = buf[2]
    payload = bytes(buf[3:3 + length])
    return cmd, seq, payload


def pack_resp(status, seq):
    """Pack a 2-byte response."""
    return bytes([status, seq])


# command-specific pack helpers

def pack_key_press(seq, keycode):
    return pack_msg(CMD_KEY_PRESS, seq, bytes([keycode]))

def pack_key_release(seq, keycode):
    return pack_msg(CMD_KEY_RELEASE, seq, bytes([keycode]))

def pack_key_type(seq, text):
    return pack_msg(CMD_KEY_TYPE, seq, text.encode("ascii")[:29])

def pack_mouse_move(seq, x, y):
    return pack_msg(CMD_MOUSE_MOVE, seq, bytes([x >> 8, x & 0xFF, y >> 8, y & 0xFF]))

def pack_mouse_click(seq, buttons):
    return pack_msg(CMD_MOUSE_CLICK, seq, bytes([buttons]))

def pack_mouse_press(seq, buttons):
    return pack_msg(CMD_MOUSE_PRESS, seq, bytes([buttons]))

def pack_mouse_release(seq, buttons):
    return pack_msg(CMD_MOUSE_RELEASE, seq, bytes([buttons]))

def pack_mouse_scroll(seq, amount):
    return pack_msg(CMD_MOUSE_SCROLL, seq, bytes([amount & 0xFF]))

def pack_touch(seq, contacts):
    """Pack a touch command with up to 2 contacts.
    Each contact: (id, tip, x, y) where tip is 0 or 1, x/y are 0-32767."""
    payload = bytearray([len(contacts)])
    for cid, tip, x, y in contacts:
        payload.append(cid)
        payload.append(tip)
        payload.extend([x >> 8, x & 0xFF, y >> 8, y & 0xFF])
    return pack_msg(CMD_TOUCH, seq, bytes(payload))
