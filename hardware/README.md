# Hardware

This folder holds a carrier PCB for the two QT Py RP2040 boards. It runs
the UART and ground between them in copper.

For prototyping, a breadboard and three wires does the same job. That is
what the [parts list](../README.md#parts-needed-86--host-computer) buys and
what [docs/reference/boards.md](../docs/reference/boards.md) describes.

## The connection it carries

| | |
|---|---|
| UART | Relay `TX` (D6) → HID `RX` (D7), Relay `RX` (D7) ← HID `TX` (D6) |
| Ground | Common `GND`, required whenever the two boards are powered from different USB hosts |
| Baud | 921600 |

The pin assignments come from the firmware, in `board/shared/protocol.py`
and each board's `code.py`.

Power is per-board and deliberately not shared: the relay runs off the host
it is plugged into, and the HID board runs off the target device it is
driving. The carrier connects data and ground, not supply.

## Contents

- `pcb/` — KiCad project: schematic, layout, footprints.

Gerbers, drill files and BOMs are not kept in the repo. They are generated
from the KiCad sources and attached to releases.
