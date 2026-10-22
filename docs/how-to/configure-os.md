# How to Configure Raspberry Pi OS for Roadie

Step 2 of 2, after [Install Raspberry Pi OS for Roadie](install-os.md). One
script installs what Roadie needs, builds it, and sets the Pi up for your
hardware.

## 1. Connect

With the username and hostname you gave Imager:

```bash
ssh <username>@roadie-01.local
```

## 2. Get the set-up script

```bash
curl -fsSL https://raw.githubusercontent.com/VibiumDev/roadie/main/setup/machine-setup -o machine-setup
```

## 3. Run it

Pick the hardware that drives the target:

```bash
bash machine-setup --gadget    # this Pi's USB-C port is the phone's keyboard, mouse,
                               # touch, mic, speaker and Ethernet (no boards)
bash machine-setup --boards    # the RP2040 relay and HID boards
```

Run it as your normal user, not with `sudo`: Roadie runs as whoever runs
it, and the script uses `sudo` where it needs to. A first run takes around
ten minutes, most of it upgrading packages and fetching Go. It reboots at
the end; add `--noreboot` to read the output first.

| Option | Default | |
|---|---|---|
| `--platform OS` | `android` | Target OS: `android`, `ios`, `mac`, `windows`, `linux` |
| `--name NAME` | hostname | Roadie's name on the network and in the browser tab |
| `--port N` | `8080` | HTTP port |
| `--no-network` | | `--gadget`: don't share this Pi's Ethernet with the phone |
| `--dev` | | Also install the test tooling (`make setup`: browser, X server, VNC) |
| `--dir PATH` | `~/Projects/roadie` | Where the checkout lives |
| `--branch NAME` | `main` | Branch to check out |
| `--no-upgrade` | | Skip `apt-get upgrade` |
| `--noreboot` | | Never reboot |
| `--dry-run` | | Print what it would do; change nothing |

To try a branch, put its name in the URL instead of `main` and pass
`--branch` with the same name.

## What the script does

Every run, both kinds:

- Upgrades installed packages (skip with `--no-upgrade`).
- Installs packages: `git`, `make`, `build-essential`, `golang-go`, `curl`,
  `python3-venv`, `v4l-utils`, `alsa-utils`. Go fetches the exact toolchain
  `go.mod` names (checksum-verified) on the first build.
- Clones Roadie into `~/Projects/roadie`, or fast-forwards it to the latest
  `main`; a checkout with local changes is left alone. Builds it.
- Creates the `roadie` group and adds you to `roadie`, `audio`, `video`,
  `dialout` and `plugdev`.
- Installs the capture dongle's udev rule (for `POST /api/capture/reset`).
- Writes `/etc/default/roadie` with your options, the first time only:
  ```bash
  ROADIE_ARGS="--hid-backend linux-gadget --platform android --name roadie-01 --port 8080"
  ```
  After that it keeps the file, apart from `--hid-backend`, which follows
  `--gadget` or `--boards`.
- Restarts Roadie if it was running.

`--gadget` also:

- Installs `dnsmasq-base` and `nftables`.
- Runs `gadget/install`: the USB-C port in peripheral mode, the composite
  USB gadget at boot, DHCP/DNS/NAT for the phone, and `roadie.service`.
  Details: [Drive a Phone from a Raspberry Pi 4](linux-gadget.md#install).

`--boards` also:

- Installs the HID board's udev rule and the board tooling (`.venv` with
  `circup`) used to flash the boards.
- Installs `roadie` into `/usr/local/bin` and enables `roadie.service`.

Nothing else is touched: not NetworkManager, eth0, Wi-Fi or SSH.

## 4. Check it

After the reboot, SSH back in:

```bash
systemctl status roadie        # active (running)
roadie-gadget check            # --gadget: everything green
```

Then open `http://roadie-01.local:8080/view`. To start, stop or restart
Roadie, read its log, or change its flags, see
[How to Run Roadie](run-roadie.md).

## 5. Next

- `--gadget`: power the Pi through a USB-C splitter and wire the phone —
  [Wiring and power](linux-gadget.md#wiring-and-power) — then work through
  the [hardware checklist](linux-gadget.md#hardware-validation-checklist).
- `--boards`: [flash the boards](flash-boards.md) and
  [connect the phone](connect-a-phone.md).

## Switching between boards and gadget

A Pi set up either way can switch, once the gadget is installed
(`--gadget`, or `sudo ~/Projects/roadie/gadget/install --user $USER` later):

```bash
roadie-mode                  # current mode
sudo roadie-mode boards      # or: sudo roadie-mode gadget
```

Recable the phone's hub to match; see
[Switching backends](linux-gadget.md#switching-backends).

## Updating

Run the script again, with the same options:

```bash
bash ~/Projects/roadie/setup/machine-setup --gadget
```

It pulls, rebuilds and restarts Roadie without rebooting (unless the system
asks for a reboot), and keeps your settings in `/etc/default/roadie` and
`/etc/roadie`.

## Undoing it

`--gadget`: `sudo ~/Projects/roadie/gadget/uninstall` (see
[Uninstall and rollback](linux-gadget.md#uninstall-and-rollback)).
`--boards`: `sudo systemctl disable --now roadie` and
`sudo rm /etc/systemd/system/roadie.service`.
