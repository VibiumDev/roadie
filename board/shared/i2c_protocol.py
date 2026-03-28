# shared I2C protocol constants and helpers
# used by both relay (controller) and hid (target) boards

I2C_ADDR = 0x42
MSG_SIZE = 32

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
CMD_ACK           = 0xFF

# status codes
STATUS_OK   = 0x00
STATUS_ERR  = 0x01
STATUS_BUSY = 0x02


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
