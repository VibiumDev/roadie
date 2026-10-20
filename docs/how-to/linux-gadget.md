# Drive a Phone from a Raspberry Pi 4 (linux-gadget backend)

With `--hid-backend linux-gadget`, the Pi's own USB-C port *is* the
phone's keyboard, mouse, touchscreen, microphone, speaker and Ethernet
adapter. No RP2040 boards. The web UI, API, WebSocket protocol and video
capture are unchanged; the RP2040 boards stay the default.

Design and decisions: [linux-gadget-plan.md](../linux-gadget-plan.md).

## Supported hardware

| | Status |
|---|---|
| Raspberry Pi 4 Model B, Raspberry Pi OS (Debian 13 trixie), 64-bit, kernel 6.18 | Built and tested here (Rev 1.4, `6.18.50+rpt-rpi-v8`) |
| Other Pis with a DWC2 USB-C/OTG port (Zero 2 W, CM4) | Untested; endpoint budget may differ |
| Android phone with USB-C DisplayPort alt mode | Validate with the [checklist](#hardware-validation-checklist) |
| iPhone (USB-C) | Mouse and phone audio to `/view` work; audio switches to USB by itself on plugging in. Keyboard, microphone and Ethernet not yet checked; touch never works (iOS limitation) |

## Wiring and power

The Pi 4's USB-C connector is both its **power input** and the gadget port.
The phone, acting as USB host, also puts 5 V on that cable. Never join the
two supplies: use a **USB-C power/data splitter whose data side has VBUS
cut** (the kind sold for PiKVM on the Pi 4).

```
                  ┌── HDMI ──────────> capture dongle ──> Pi USB-A (video, as before)
phone ── USB-C ───┤   PD hub
 (USB host,       ├── USB-A data ───> splitter DATA in ─┐
  power sink)     └── USB-C PD in <── phone charger     ├─> Pi USB-C
                                     Pi PSU ─> splitter PWR in ┘
                                     5.1 V 3 A
Pi Ethernet ──> switch ──> router ──> internet
```

- The Pi is powered only by its own PSU; it declares itself self-powered and
  draws nothing from the phone.
- The phone charges from the hub's PD input while being the data host. Not
  every hub does both; check it ([checklist](#hardware-validation-checklist)
  step 1) rather than assuming.
- Without a splitter, don't connect the Pi's USB-C to the hub at all.
  Powering the Pi from GPIO 5 V bypasses its input protection and is not
  recommended.

## Install

On a fresh Pi, [Install Raspberry Pi OS](install-os.md) and run
`setup/machine-setup --gadget` ([Configure Raspberry Pi OS](configure-os.md)):
it installs the packages, builds roadie and does everything below. By hand,
from a checkout:

```bash
make gadget-install   # builds roadie and installs everything (asks for sudo)
sudo poweroff         # first time only; the Pi has to be rewired anyway
```

Then, with the Pi off, wire it as in [Wiring and power](#wiring-and-power):
its supply into the splitter's power input, the splitter into the Pi's USB-C,
and the hub's USB-A into the splitter's data input — where the HID board
used to go. The relay and HID board can stay on the Pi's USB-A ports;
gadget mode ignores them. Power on.

Nothing needs starting after boot: the gadget, the phone's network and
roadie come up by themselves. Open `http://<pi>.local:8080/view`, and check:

```bash
roadie-mode           # mode gadget, gadget up, phone attached
roadie-gadget check   # everything green?
journalctl -b -u roadie -u roadie-gadget -u 'roadie-phone-net@*'
```

The phone shows an Ethernet connection, and its sound arrives through the
viewer's speaker button instead of HDMI.

`gadget/install` adds `dtoverlay=dwc2,dr_mode=peripheral` to
`/boot/firmware/config.txt` (in a marked block, after a backup), the scripts
in `/usr/local/lib/roadie`, settings in `/etc/roadie`, a udev rule, and these
units:

| Unit | What | Lifecycle |
|---|---|---|
| `roadie-gadget.service` | Builds the configfs gadget and binds it | Boot. Oneshot, no stop action |
| `roadie-phone-net@phone0.service` | Address, firewall/NAT, DHCP+DNS (dnsmasq) for the phone | Follows the `phone0` interface |
| `roadie-phone-net-check.timer` | Re-applies anything that drifted, every 30 s; logs what it fixed | Always |
| `roadie.service` | The server, as your user (groups roadie, audio, video, dialout) | Restartable at will |

Restarting or crashing roadie never re-enumerates USB: the phone keeps its
keyboard, audio and network. Arguments live in `/etc/default/roadie`:

```bash
ROADIE_ARGS="--hid-backend linux-gadget --platform android --port 8080"
```

### Switching backends

One Pi can be wired for both: the capture dongle and the relay use its USB-A
ports, the gadget its USB-C port.

```bash
roadie-mode                # which mode, and whether everything agrees with it
sudo roadie-mode boards    # RP2040 relay and HID boards
sudo roadie-mode gadget    # this Pi's USB-C port
```

A switch sets `--hid-backend` in `/etc/default/roadie`, brings the gadget up
or removes it (and keeps it that way across reboots), and restarts roadie.
Removed, not just idle: an idle gadget's USB speaker would still take the
phone's audio away from HDMI.

The phone's hub must be cabled for the mode — its USB-A port to the HID
board, or through the splitter to the Pi. With a hub (or an extra small hub)
that has room for both, nothing needs moving; the phone then also has the
HID board's microphone, so unplug the board if the gadget's mic is the one
that matters.

`roadie-mode` with no argument also warns when the pieces disagree, such as
boards mode with the gadget still up.

On a Pi that also runs the board tests: `roadie.service` holds port 8080
in either mode, and the tests use whatever roadie answers there. Stop it
first (`sudo systemctl stop roadie`) to have the tests start their own.

## Settings

### Which phone: `/etc/default/roadie`

Roadie's own arguments, for `roadie.service`:

```bash
ROADIE_ARGS="--hid-backend linux-gadget --platform android --port 8080"
```

For an iPhone, change `--platform android` to `--platform ios` (mouse input,
coordinates mapped to the phone's screen), then
`sudo systemctl restart roadie`; the phone is not re-enumerated. An iPhone
also needs AssistiveTouch on: see
[Connect a Phone → iPhone](connect-a-phone.md#iphone).

The installer writes this file only when it doesn't exist, so it can be
written first to start out on an iPhone:

```bash
echo 'ROADIE_ARGS="--hid-backend linux-gadget --platform ios --port 8080"' | sudo tee /etc/default/roadie
make gadget-install
```

### The gadget: `/etc/roadie/gadget.conf`

Every default is listed in the file:

| Setting | Default | Notes |
|---|---|---|
| `AUDIO` | `uac2` | `uac1` for phones with poor UAC2 support; `none` |
| `AUDIO_SPEAKER` | `1` | Phone plays audio over USB, roadie's `/audio` carries it. `0` keeps the phone's audio on HDMI (the dongle) |
| `AUDIO_MIC` | `1` | Phone gets a USB microphone fed by `/api/hid/audio` |
| `NET` | `ecm` | `ncm`, `rndis` (deprecated; disabled in recent Android kernels), `none` |
| `NET_HOST_MAC` | this Pi's eth0 MAC | Must be universally administered or Android ignores the link (see below) |
| `USB_VENDOR_ID:USB_PRODUCT_ID` | `0x1d6b:0x0104` | Linux Foundation "Multifunction Composite Gadget"; use your own for a fleet |

Changing a setting: `sudo systemctl restart roadie-gadget` (the phone sees a
replug), then `sudo systemctl restart roadie` if audio settings changed.

### The phone's network: `/etc/roadie/phone-net.conf`

Uplink (`eth0`), phone subnet
(`192.168.42.0/24`, Pi `.1`, phone always `.2`), DNS, and isolation
exceptions (`ALLOW_DESTS`, `ALLOW_PI_TCP_PORTS`).

## What the phone gets

| Function | Phone sees | Pi side |
|---|---|---|
| Keyboard | USB boot keyboard | `/dev/hidg*` (from configfs, never assumed) |
| Mouse | absolute mouse, 5 buttons, wheel | `/dev/hidg*` |
| Touch | 2-contact touchscreen | `/dev/hidg*` |
| Microphone | USB audio input, 48 kHz mono | ALSA `UAC2Gadget` playback |
| Speaker | USB audio output, 48 kHz stereo | ALSA `UAC2Gadget` capture → `/audio` |
| Ethernet | CDC-ECM adapter, DHCP from the Pi | `phone0`, NAT out of `eth0` |

`roadie-gadget devices` prints the mapping.

### Audio

- Mic: the browser still sends 16 kHz mono to `/api/hid/audio`; roadie
  upsamples to 48 kHz. A 40–200 ms jitter buffer smooths network bursts;
  silence is sent whenever nobody is talking, and an underrun plays
  silence, never a replayed buffer.
- Speaker: with `AUDIO_SPEAKER=1`, `/audio` follows whichever way the phone
  sends its sound: the gadget's USB speaker whenever the phone uses it (the
  default, and what calls need), the capture dongle's HDMI audio otherwise.
  Android picks USB. iOS lists both — the gadget as **"Playback
  Inactive"** with a headphones icon (the kernel's fixed name for it), the
  dongle by its own name — and can stay on HDMI; switch it in Control
  Center's output picker, or `roadie-gadget reset`, after which iOS picks
  USB again. The viewer's speaker button works as before.
- The dongle's audio is read straight from its own ALSA card, the one on
  the same USB device as the video (or `--audio NAME`), in both backends.
- Streams reopen by themselves after a replug or a gadget rebuild.

### Network

- The phone gets `192.168.42.2`, gateway and DNS `192.168.42.1`.
- It can reach the internet, through eth0 only. It cannot reach this Pi
  (beyond DHCP, DNS and ping), its SSH, roadie, the LAN, or any private,
  CGNAT or link-local range. Nothing can open a connection to the phone,
  except from the Pi itself (`adb connect 192.168.42.2:5555` works).
- Forwarding is enabled on `phone0` and `eth0` only, not globally, and
  NetworkManager is not involved (it ignores gadget interfaces). When it was
  off on eth0 before, nothing else is forwarded from eth0, so the LAN can't
  route through the Pi.
- Whenever the service is not running — stopped, restarting, failed — the
  phone link is closed: no address, no forwarding, every packet dropped.
  The phone keeps its lease, so the link fails closed, not open.
- A host firewall with a DROP forward policy (ufw, Docker) also needs to
  allow `phone0 → eth0`.

## Status and logs

| Where | What |
|---|---|
| `GET /api/hid/status` | `backend: linux-gadget`, `usb_state` (`configured` = phone enumerated us), `port` (controller), `serial` |
| `GET /health` | `hid_board: not_answering` when no phone is attached or it suspended the bus |
| `roadie-gadget status` / `check` | functions, nodes, sound card, interface, USB state |
| `sudo roadie-phone-net status phone0` | carrier, address, leases, firewall counters |
| `journalctl -u roadie -u roadie-gadget -u 'roadie-phone-net@*'` | everything, with state changes logged once |

## Recovery

Roadie recovers by itself from a phone unplugging, suspending, or
re-enumerating, and from the gadget being rebuilt. A failed input command
never leaves a key, button or finger down: the release is resent until the
phone takes it, and a press whose caller got an error never lands later.
Starting roadie releases anything a killed roadie left held.

When something is still wrong, escalate in this order:

| Step | Effect on the phone |
|---|---|
| `POST /api/hid/release` | Lets go of buttons and fingers |
| `POST /api/relay/reset` | Roadie reopens its device nodes; phone sees nothing |
| `sudo systemctl restart roadie` | Same, plus audio; phone sees nothing |
| `POST /api/hid/reset` or `roadie-gadget reset` | Phone sees an unplug/replug of every function |
| `sudo roadie-gadget rebind` | Harder replug: controller unbound and rebound |
| `sudo systemctl restart roadie-gadget` | Rebuilds the gadget only if the config changed |
| `sudo reboot` | Last resort |

Roadie never re-enumerates on its own after an application error.

## Troubleshooting

| Symptom | Check | Fix |
|---|---|---|
| `roadie-gadget check`: no USB device controller | `ls /sys/class/udc` | `dtoverlay=dwc2,dr_mode=peripheral` under `[all]`, reboot |
| Binding failed | `dmesg \| tail` | Out of endpoints: don't re-enable UAC2 async feedback or volume controls; disable a function |
| `usb_state: not attached` with a phone plugged in | Phone locked? Cable/splitter data path; phone USB role | iPhone: unlock it and allow the accessory, then `roadie-gadget reset`. Otherwise the phone must be host: the hub's USB-A port, not a charge-only path |
| `usb_state` never reaches `configured`; `dmesg` repeats `new device is high-speed` with no `new address` | Phone unlocked, accessory allowed? | Reboot the Pi. Seen once, after `roadie-gadget down`/`up` while the iPhone was enumerating; `reset`, `rebind`, replugs and another cable didn't help |
| `usb_state: suspended` | Phone screen off | Wake the phone; see remote wakeup under [limitations](#android-compatibility-and-limitations) |
| API: "no phone has enumerated the USB gadget" | `roadie-gadget status` | As above |
| API: "cannot open keyboard: permission denied" | `ls -l /dev/hidg*` | User in group `roadie`, re-login or `systemctl restart roadie` |
| No mic on the phone | `adb shell dumpsys audio \| grep -i usb` | `AUDIO=uac1` if the phone mishandles UAC2 |
| Mic audio stutters | `journalctl -u roadie \| grep 'USB audio'` (drops, xruns) | Network jitter between browser and Pi |
| No sound in the viewer | `curl :8080/health` has `audio`? | Phone playing? `AUDIO_SPEAKER=1`? |
| Desktop Pi: "device busy" on the sound card | `fuser -v /dev/snd/*` | udev rule keeps PipeWire off `UAC2Gadget`; reinstall it |
| Phone shows no Ethernet | `adb shell ip link` names it `eth0`? | Host MAC must not be locally administered (see below) |
| Phone has Ethernet, no internet | `sudo roadie-phone-net status phone0`, `sudo nft list table inet roadie_phone` | Uplink name (`UPSTREAM_IF`), host firewall forward policy |
| `roadie-phone-net` refuses to start: overlaps | `ip route` | Pick another `PHONE_SUBNET` |

## Android compatibility and limitations

- **Ethernet must be named `eth<N>` on the phone.** Android only adopts
  interfaces matching `eth\d`, and the kernel names a USB link `usb<N>` when
  its MAC is locally administered. The default phone-side MAC is the Pi's
  own eth0 address (unique per board, never visible past the NAT).
- **ECM** is the default (`cdc_ether`, in every Android kernel). **NCM**
  is supported by Android's kernel too; **RNDIS** is disabled in recent
  Android kernels.
- **UAC2** is the default; Android's `snd-usb-audio` handles both UAC2 and
  UAC1. Volume/mute controls are off (endpoint budget), so Android uses
  software volume.
- **Audio routing:** with the speaker function, the phone's audio leaves by
  USB, not HDMI.
- **Remote wakeup** is declared, but whether a sleeping phone wakes for
  gadget input has not been tested.
- **Stylus:** not exposed — Roadie's API has no pen commands. One spare IN
  endpoint would allow a pen digitizer later.
- **One phone per Pi:** the Pi 4 has one device controller.
- **Pointer flash on restart:** when roadie starts with a phone attached it
  sends each device one neutral report, to release anything a killed roadie
  left held. Android may show its mouse pointer at the top-left until the
  next touch or its idle timeout.

## Testing

| Command | Covers | Needs |
|---|---|---|
| `make test-unit` | Report encoding vs. the descriptor files, state and cleanup rules, retries, reconnects, device mapping, audio buffering, resampling, ALSA ABI | nothing |
| `go test -run TestALSALoopback` | the ALSA client against the real kernel, including silence on underrun | `sudo modprobe snd-aloop` |
| `make test-gadget` | installed gadget: functions, descriptors, nodes, permissions, sound card, interface | the gadget up |
| `make test-gadget-loopback` | the gadget through this Pi's own USB host stack (kernel `dummy_hcd`): HID through hid-multitouch, DHCP/DNS/NAT/isolation, roadie restart/kill/replug | kernel headers, sudo; no phone. Run once per boot: reloading `dummy_hcd` can leave the kernel unable to bind it again until a reboot |
| `make test-phone-net` | the phone network on a virtual link: DHCP, DNS, NAT, isolation, router guard, closed while stopped | sudo |
| `make test-gadget-install` | install and uninstall in a scratch root; `machine-setup` dry runs | nothing |
| [Checklist](#hardware-validation-checklist) | everything a phone has to confirm | phone, hub, splitter |

`dummy_hcd` has no isochronous transfers, so USB audio streaming is only
tested against a phone.

## Hardware validation checklist

Record pass/fail and notes for each.

1. **Power and roles.** Pi on its own PSU through the splitter, phone through
   the PD hub. Meter: no continuity between the splitter's data-side VBUS
   and the Pi's 5 V. `vcgencmd get_throttled` → `0x0` under load. Phone shows
   charging. `roadie-gadget status` → `usb state: configured`.
2. **Enumeration.** `adb shell getevent -lp` lists three "Vibium Roadie"
   devices; the touchscreen has `INPUT_PROP_DIRECT`.
3. **Keyboard.** In a text field:
   `curl -X POST localhost:8080/api/hid/type -d '{"text":"Hello, Roadie 123!"}'`
   types exactly that.
4. **Mouse.** `/view?input=mouse`: pointer follows clicks, click opens an
   app, wheel scrolls.
5. **Touch.** `/view?input=touch`: taps land under the cursor, swipes
   scroll, two-finger pinch zooms Maps. `make test-e2e-android` passes.
6. **No stuck input.** Start a drag in `/view`, then
   `sudo systemctl kill -s KILL roadie`; the drag ends within 5 s of roadie
   restarting.
7. **Microphone recognized.**
   `adb shell dumpsys audio | grep -iE 'usb.*(device|headset)'` shows a USB
   input.
8. **Microphone injection.** Voice recorder on the phone; speak through
   `/view`'s mic button for 30 s with pauses. Playback: speech clear, pauses
   silent (no buzz, no repeats). Repeat with `AUDIO=uac1` if it fails.
9. **Headset capture.** Play a video on the phone; unmute `/view`: it is
   heard. `/health` shows `audio` 48000 Hz, 2 channels.
10. **Internet.** Phone Wi-Fi and mobile data off. Settings shows
    Ethernet connected; `adb shell ip addr show eth0` → `192.168.42.2`;
    a website loads. `sudo roadie-phone-net status phone0` shows the lease.
11. **Isolation.** From the phone's browser, `http://192.168.42.1:8080`,
    `http://<Pi LAN IP>:8080` and the LAN router's page all fail.
12. **Video.** `/view` shows the phone over HDMI throughout 3–11.
13. **Roadie restarts.** `for i in $(seq 10); do sudo systemctl restart roadie; sleep 5; done`
    while the phone streams a video: Ethernet stays up, no input devices
    removed (`adb shell getevent` shows none), `usb state` stays `configured`.
14. **Replug.** Pull the data cable for 10 s and replug: input, audio and
    `192.168.42.2` are back within 10 s, no reboot. Then `roadie-gadget reset`
    (soft re-enumeration) recovers the same way.
15. **Everything at once, for hours.** Video viewer open, continuous mic
    injection, headset audio playing, `adb shell ping 1.1.1.1`, a tap every
    minute, for 8 h. Afterwards: `journalctl -u roadie` drop/xrun counts,
    roadie's memory (`ps -o rss -C roadie`) flat, `/health` temperature.
16. **Sleeping phone.** Screen off 5 min; send a key
    (`{"keycode":102,"action":"click"}`, 0x66 power). Note whether the phone
    wakes (remote wakeup is untested).
17. **RP2040 unchanged.** With `--hid-backend rp2040` and the boards,
    `make test` passes.
18. **iPhone.** AssistiveTouch on, Wi-Fi on the Pi's network, phone
    unlocked: `make test-e2e-iphone` passes.

## Uninstall and rollback

```bash
sudo gadget/uninstall           # stop, remove units, gadget, firewall, boot block
sudo reboot                     # USB-C back to a plain power input
sudo gadget/uninstall --purge   # also /etc/roadie, leases, binary, group
```

Only Roadie's own files, its nftables table, the two forwarding sysctls it
set (restored to their previous values) and its marked `config.txt` block
are touched. NetworkManager, eth0, wlan0 and SSH are not, so a remote session
survives. To go back to the RP2040 boards without uninstalling, change
`--hid-backend` (see [Switching backends](#switching-backends)).

## Where the descriptors come from

The HID descriptors are the RP2040 HID board's own: CircuitPython's
keyboard (MIT), Neradoc's `absolute_mouse` (MIT) and Roadie's digitizer,
each without its report ID. The US typing table is adafruit_hid's (MIT).
