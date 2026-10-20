# Linux USB Gadget Backend — Plan

Roadie gets a second hardware backend. A Raspberry Pi 4 plugs straight into the
phone's hub and *is* the USB peripheral: keyboard, mouse, touchscreen,
microphone, headset and Ethernet adapter, all from one composite gadget. The
RP2040 relay/HID pair stays the default and is untouched.

How to install and operate it is in
[Linux Gadget How-To](how-to/linux-gadget.md); this page records what was
built and why.

## What exists today

| Concern | RP2040 backend (unchanged) |
|---|---|
| Input path | `server.go`/`bidi.go` → `*HIDController` (`hid.go`) → JSON over USB serial → relay board → UART → HID board → USB HID |
| HID devices | One USB HID interface, three report IDs: boot keyboard (1), `absolute_mouse` (11), 2-contact digitizer (12) |
| Coordinates | 0–32767 absolute on both axes, computed by the viewer/BiDi; the boards only clamp |
| Microphone | `/api/hid/audio` takes 16 kHz S16LE mono; relay frames it to the HID board's `usb_audio` mic |
| Phone audio out | Phone sends audio over HDMI; the capture dongle's audio feeds `AudioBroadcaster` → `/audio` (f32 interleaved) |
| Recovery | Relay ping round trip, soft reboot, USB re-enumeration (`hid.go`) |
| Config | Command-line flags only; no config file |

`*HIDController` is used through 13 methods. That method set is the seam.

## Architecture

```
                 Browser / agent
                       │ HTTP · WebSocket (unchanged)
                 roadie (one process)
                       │ HIDBackend interface
          ┌────────────┴─────────────┐
   rp2040 (hid.go)            linux-gadget (gadget_*.go)
   JSON → relay → UART        report bytes → /dev/hidgN
   → HID board                PCM → ALSA "UAC2Gadget"
                                     │
                       configfs gadget "roadie" (roadie-gadget, boot)
                       ├─ hid.keyboard  hid.mouse  hid.touch
                       ├─ uac2.audio   (mic in, headset out)
                       └─ ecm.phone ── phone0 ── roadie-phone-net (DHCP/DNS/NAT)
```

- **Selection:** `--hid-backend rp2040|linux-gadget`, default `rp2040`.
  Flags are Roadie's configuration mechanism, so the YAML example in the brief
  becomes a flag (and `ROADIE_ARGS` in `/etc/default/roadie` under systemd).
- **Smallest abstraction:** `HIDBackend`, an interface with exactly the methods
  the server already calls on `*HIDController`, plus `Backend()` for status.
  `Server.HID` changes type; no handler, page or WebSocket message changes.
- **Capabilities** are discovered from configfs at runtime (which functions
  exist, their rates and channel masks), logged at startup, and surfaced as
  errors from the method that needs them. No new public commands.
- **Public API:** unchanged except two additive status fields:
  `/api/hid/status` gains `backend`, and `usb_state` for the gadget.

## Lifecycle: gadget outlives Roadie

```
boot → roadie-gadget.service  (oneshot, idempotent "up", no ExecStop)
     → roadie-phone-net@phone0 (bound to the phone0 netdev: addr, nft, dnsmasq)
     → roadie.service         (non-root; opens /dev/hidg*, ALSA, video)
```

- Roadie never creates, binds or tears down the gadget. Restarting or crashing
  Roadie cannot re-enumerate USB, drop the phone's network, or reset audio.
- `roadie-gadget up` is idempotent: if the bound gadget matches the config's
  fingerprint it only re-applies permissions. A changed config, or a partial
  gadget from a failed run, is torn down and rebuilt — and logged as such.
- Exceptional recovery is explicit: `roadie-gadget reset` (or
  `POST /api/hid/reset`) toggles the UDC's `soft_connect`, which makes the phone
  see an unplug/replug without destroying device nodes. Roadie never does
  this on its own after an application error.

## USB gadget (configfs)

**Controller:** BCM2711's DWC2 OTG core on the USB-C port, enabled with
`dtoverlay=dwc2,dr_mode=peripheral`. Its overlay gives 7 TX FIFOs, so **7 IN
endpoints** besides EP0. The budget decides the design:

| Function | IN | OUT | Notes |
|---|---|---|---|
| hid.keyboard | 1 int | 0 | `no_out_endpoint=1`; LED reports arrive by SET_REPORT on EP0 |
| hid.mouse | 1 int | 0 | |
| hid.touch | 1 int | 0 | |
| uac2.audio | 1 iso | 1 iso | `c_sync=adaptive` (no feedback EP), volume/mute off (no interrupt EP) |
| ecm.phone | 2 (bulk, int) | 1 bulk | |
| **Total** | **6 of 7** | **2** | one IN spare |

UAC2's defaults (async feedback + feature-unit interrupt) would add two IN
endpoints and overflow the budget, which is why they are switched off. HID
functions are separate interfaces rather than one interface with report IDs
(the RP2040 layout): one IN endpoint each, but each host-side HID driver binds
only its own device, so hid-multitouch never swallows the mouse — the
conflict `board/multitouch.sh` exists to work around.

**Identifiers:** VID:PID `1d6b:0104`, the Linux Foundation's "Multifunction
Composite Gadget" ID that the kernel's own `g_multi` uses — it names the device
for what it is and imitates no product. Configurable; a production fleet
should use its own allocation. Serial number is the Pi's serial, so the phone
side can tell Pis apart. Device class EF/02/01 (IAD), self-powered.

**Descriptors** (`gadget/hid/*.desc`, one source of truth read by the setup
script and checked by Go tests):

| Device | Source | Report |
|---|---|---|
| Keyboard | CircuitPython's `usb_hid.Device.KEYBOARD` (what the HID board runs), report ID removed | 8 bytes: mods, reserved, 6 keys; boot subclass/protocol |
| Mouse | `absolute_mouse` library (MIT, what the HID board runs), report ID removed | 6 bytes: 5 buttons, X, Y (0–32767), wheel |
| Touch | Roadie's own `board/hid/digitizer.py`, report ID removed | 13 bytes: 2 × (id, tip, X, Y), contact count |

These are kept rather than new ones written: they are proven against
Android and iOS, support two contacts, and match Roadie's 0–32767
coordinates. A 7-byte mouse layout was tried on the board in `2a2c9b8` and
reverted in `336e425`.

**Device mapping:** never assume `/dev/hidg0`. Each function's configfs `dev`
attribute gives `major:minor`; `/sys/dev/char/<maj:min>/uevent` gives the node
name. ALSA is found by card id (`UAC2Gadget`/`UAC1Gadget`), never by number.
`roadie-gadget devices` prints the mapping.

## HID backend

- Writes report bytes straight to `/dev/hidgN` (`O_NONBLOCK` + `poll`), no
  helper process. f_hid queues one report per device: `EAGAIN` means the host
  hasn't collected the last one, `ESHUTDOWN` means no host has configured us.
- **Full-state reports.** Keyboard, buttons and contacts are state, and every
  report carries the whole state. A failed write leaves the device *dirty*;
  the monitor resends the current state until it lands. A release that times
  out therefore still arrives, in order, once the host is reading again.
- **Clean slate** on start (crash recovery) and on shutdown: all keys up,
  buttons up, fingers lifted. When the UDC re-enters `configured`, internal
  state resets so stale keys are never re-sent.
- Semantics copied from the HID board: `type` is US layout via press/release
  per character; a repeated absolute position is preceded by a one-unit nudge
  so Linux's input core doesn't drop it; scroll is `-amount` on the wheel,
  chunked to ±127; touch slots always carry ids 0 and 1, count = contacts
  given. Text that can't be typed on a US layout is rejected up front.
- Startup checks each function's configfs `report_length` against the
  encoder, so a mismatched descriptor is a clear error, not garbled input.
- Status: `connected` when the device nodes are open; `BoardResponding()` is
  "UDC state is `configured`", so `/health`'s existing `hid_board:
  not_answering` means "phone not attached or asleep".
- Not done: stylus/pen. Roadie's API has no pen commands; one IN endpoint is
  spare if a pen digitizer is ever added.

## Audio (UAC2)

f_uac2 exposes an ALSA card on the Pi. Gadget-side **playback** is the phone's
**microphone**; gadget-side **capture** is what the phone **plays**.

- Format: 48 kHz, S16_LE; mic mono, headset stereo (configurable). UAC1 is a
  config option for phones with poor UAC2 support; both are standard in
  Android's kernel (`snd-usb-audio`).
- ALSA is driven with its kernel ioctl ABI from Go (`alsa_linux.go`) — no
  cgo, no new dependency, and full control of software parameters.
- **Mic:** `/api/hid/audio` still takes 16 kHz mono; a polyphase FIR upsamples
  ×3. A jitter buffer (bounded at 200 ms, oldest dropped) feeds ALSA one
  period at a time; when nothing is queued the writer sends silence, so the
  phone hears silence, not a replayed buffer. ALSA silence-fill and a
  never-stop threshold make an underrun silent rather than an XRUN.
- **Headset:** a reader pulls capture periods and broadcasts f32 interleaved
  through the existing `AudioBroadcaster`, so `/audio` and the viewer are
  unchanged. A phone with both outputs sends to one at a time — Android to
  USB, iOS to either — so `/audio` passes the USB speaker through whenever
  it carries audio and the dongle's HDMI audio otherwise (`audio_mux.go`),
  rather than mixing two clocks. Listeners reconnect if the format changes.
- **The dongle's audio** is read from its own ALSA card (matched to the
  video by USB device) with the same client. The microphone driver used
  before added about 30 clicks a second at its block joins and could pick
  ALSA's null device; it remains only where there is no ALSA card.
- Both streams reopen the device with backoff on `ENODEV`/disconnect, recover
  `EPIPE`/`ESTRPIPE` with PREPARE, and never require re-creating the gadget.

## Network (USB Ethernet)

- **ECM by default.** Evaluated: ECM (`cdc_ether`, the simplest, in every
  Android kernel), NCM (`cdc_ncm`, in Android GKI, faster, less field history with
  Android's Ethernet service), RNDIS (deprecated, disabled in recent Android
  kernels). NCM and RNDIS remain config options.
- **The phone-side MAC must be universally administered.** Android's
  EthernetTracker only adopts interfaces named `eth\d`, and the host's usbnet
  names a link `usb%d` when the MAC has the locally-administered bit set. The
  default host MAC is the Pi's own eth0 address, which is unique per board and
  never leaves the USB link (it is NATed).
- **Own the phone link explicitly** with standard Debian parts —
  `dnsmasq` (from `dnsmasq-base`), `nftables`, sysctl — in
  `roadie-phone-net@phone0.service`, bound to the netdev so it follows the
  gadget. The interface is named from the pattern `phone%d` (configfs only
  takes a pattern), never `usb0`, so a tethered phone on the Pi's USB-A ports
  can't be mistaken for it.
  NetworkManager is left alone: it already ignores gadget netdevs
  (`85-nm-unmanaged.rules`), so eth0/wlan0, SSH and the Pi's DNS are never
  touched. NetworkManager's shared mode was considered and rejected: it
  enables forwarding globally and masquerades out of every interface.
- Forwarding is enabled per interface (phone link and upstream only), not
  globally. When the uplink's forwarding was off until then, a guard rule
  drops anything forwarded from it except to the phone, so the LAN cannot
  route through the Pi into its other interfaces.
- **Fails closed.** The firewall goes in before the address and forwarding
  it guards, and when the service stops, restarts or fails, a deny-all table
  replaces it and the address and forwarding go. The phone keeps its lease
  throughout, so an open gap would be an open door.
- nftables table `inet roadie_phone`:
  - phone → Pi: DHCP, DNS, ICMP only (no SSH, no Roadie API, no IPv6);
  - phone → upstream only, with RFC 1918, CGNAT, link-local and loopback
    destinations dropped unless listed in `ALLOW_DESTS`;
  - nothing initiates connections to the phone;
  - masquerade out of the upstream interface only.
- The phone always gets the same address (static lease on its MAC). A
  30-second timer re-checks address, sysctls and rules and logs any drift it
  repairs.

## Power and cabling

The Pi 4's USB-C port is both its power input and the gadget port. Feeding it
from the phone's hub would back-power the Pi from the phone and vice versa.
Use a USB-C power/data splitter whose data side has **VBUS cut** (as PiKVM
does on the Pi 4): the Pi's PSU feeds the splitter's power input, and only
D+/D−/GND reach the hub. The phone is data host (DFP) and power sink through
its PD hub; that role split is what docks do, and it is checked in the
hardware checklist rather than assumed.

## Validation strategy

| Layer | How | Needs phone |
|---|---|---|
| Report encoding, state, cleanup, retries, mapping | Go unit tests with fake devices and a fake configfs tree | no |
| Descriptor ↔ report length | Go test parses `gadget/hid/*.desc` | no |
| ALSA ioctl layer | Go integration test on `snd-aloop` | no |
| Bind on the real DWC2, endpoint budget | runtime `dtoverlay dwc2` on this Pi 4, `roadie-gadget up/status/down` | no |
| HID, UAC2 enumeration, ECM + DHCP + NAT + isolation through a real host stack | kernel `dummy_hcd` loopback (built out of tree), host side in a network namespace | no |
| Android: input, mic, headset, internet, reconnect, power | [hardware checklist](how-to/linux-gadget.md#hardware-validation-checklist) | yes |

`dummy_hcd` has no isochronous support, so audio *streaming* through a real
host is a phone-only check.

## Validation results

On this repository's Raspberry Pi 4 (Rev 1.4, Debian 13, kernel
`6.18.50+rpt-rpi-v8`), with no phone attached:

| Check | Result |
|---|---|
| All six functions bind on the real DWC2 (`fe980000.usb`, "EPs: 8") | pass |
| Same gadget with f_uac2's defaults (async feedback, volume/mute) | fails to bind (`-19`), as the budget predicts |
| `roadie-gadget up` twice: second run touches nothing (no kernel messages) | pass |
| Failure part-way through a build (descriptor the kernel truncates) leaves no gadget | pass |
| Descriptor files = HID board's descriptors byte for byte; Go encoder = descriptor layout | pass (unit tests) |
| Roadie, non-root, opens all nodes and both ALSA streams on the real gadget | pass |
| ALSA client on `snd-aloop`: tone through; underrun plays silence, stream keeps running | pass; with silence-fill disabled the old audio replays (the test catches it) |
| PipeWire leaves the gadget's sound card alone with the udev rule | pass |
| `dummy_hcd` loopback: host binds hid-generic ×2 and **hid-multitouch**; typed text, keys, absolute mouse (incl. repeated position), click, wheel, two-finger touch, release all read back from evdev | pass |
| Loopback: phone namespace gets `192.168.42.2` by DHCP, DNS, internet via NAT out of eth0 | pass |
| Loopback: phone cannot reach the Pi's SSH (either address), roadie, or the LAN router; `all.forwarding` stays 0 | pass |
| Loopback: roadie stopped → no re-enumeration, phone network up; roadie SIGKILLed mid-touch → finger lifted on restart; controller rebind → roadie reopens new nodes, phone re-DHCPs to the same address | pass |
| Controller rebind under a running roadie on the real DWC2: the sound card disappears and returns; both audio streams and all HID nodes reopen by themselves (~3 s, ~1 s) | pass |
| Phone network on a veth link (`make test-phone-net`): router guard present, dnsmasq off loopback, a phone holding its lease reaches nothing (not even the Pi's SSH) while the service is stopped, back after `prepare` | pass |
| `rebind` ×3 then `down`/`up` ×3 on the real DWC2 | pass (reloading `dummy_hcd` itself can leave the kernel unable to register the interface on it until reboot; the real controller is never unloaded) |
| `roadie-phone-net@phone0` follows the netdev (stops on gadget removal, restarts on re-creation); drift timer repairs a deleted firewall table | pass |
| install/uninstall in a scratch root, `config.txt` restored byte for byte | pass |
| RP2040 backend after the refactor: `make test-hid`, `test-audio-serial`, `test-audio-api`, `test-e2e-pi` | 9/9, 5/5, 5/5, 8/8 |

With a phone (iPhone, USB-C, through a PD hub and the VBUS-cut splitter):

| Check | Result |
|---|---|
| Enumeration | pass, once the iPhone was unlocked and allowed the accessory; until then the controller saw no host at all |
| Mouse from `/view` | pass |
| iPhone audio to `/view` through the gadget speaker | pass; after a replug iOS routes audio to USB by itself |
| Keyboard (`/api/hid/type`), microphone (Voice Memos through `/view`) | pass |
| Ethernet with Wi-Fi off; DHCP lease, NAT, firewall counters | pass |
| `systemctl restart roadie` during playback | pass: iOS keeps the USB output, audio resumes within a second |
| iOS output choice | iOS can stay on HDMI ("UGREEN-UHD"); `/audio` then follows HDMI. A soft re-enumeration moves iOS back to USB within 2 s |
| HDMI audio quality | 0 clicks in 5 s (was ~30/s through the old microphone driver; the dongle read directly had none) |
| `make test-e2e-iphone`: pointer ×3, click, keyboard, drag of the Vibium mark (Wi-Fi on, for the test page) | 8/8, from a freshly started Safari and back to back; also under `--hud` |
| Typing: 1,286 characters (capitals, digits, symbols, doubled letters) into a text field | all exact, 195–240 characters/s |
| Clicks | iOS holds every tap about 100 ms whatever the button does; a move inside that window becomes a drag |
| Drags | each stroke of the mark arrives 96–98% complete at both paces (~380 and ~560 pt/s); iOS coalesces moves to its 60 Hz frames |
| `roadie-gadget down`/`up` while the iPhone was enumerating | once, the controller wedged: the phone reset it again and again but never gave it an address. `reset`, `rebind`, unbinding the controller, replugs and another cable didn't help; a reboot did. Cause unknown: the journal is kept in memory, so the logs went with the reboot |
| Microphone through the soundcheck page (`make test-e2e-iphone`) | not yet run: needs `https://soundcheck.vibium.dev` |
| Long runs, audio dropouts over time | not yet run |

Not yet checked (see the
[checklist](how-to/linux-gadget.md#hardware-validation-checklist)):
Android input, USB microphone and speaker recognition and streaming, Android
Ethernet adoption and internet, the splitter/hub power checks, remote
wakeup, and long-running operation with real traffic.
