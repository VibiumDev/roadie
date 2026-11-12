# How to Harden the Pi

A Roadie Pi has a phone on one side that you do not control and the
Internet on the other, so the Pi itself should offer as little as
possible to either. Most of this is already in place after
[`machine-setup`](configure-os.md): the service runs in a sandbox and
the phone's network link is walled off. What remains is a handful of
choices about the host that a set-up script should not make for you,
each a single command with a way back.

## What is already done

**The service.** `roadie.service` runs as the `roadie` account, a system
user with no login shell whose home is `/var/lib/roadie`. The unit gives
it a read-only view of the system, hides every other account's home,
keeps `/tmp` private, drops every capability but the one that binds ports
below 1024, filters system calls, and opens only the device nodes it
drives: video capture, the media controller, ALSA, the gadget's HID nodes,
the relay board's serial port and the capture dongle's USB device. It
restarts within two seconds if it dies, and because roadie answers a
systemd watchdog only while its own port answers, a wedged roadie is
restarted after thirty seconds instead of kept. `systemd-analyze security
roadie` scores the result.

Its files live in `/var/lib/roadie/.config/roadie`: the local admin
token, the rig key and policy once enrolled, and saved crops. Commands
that touch them run as that account:

```bash
sudo -u roadie roadie token
sudo -u roadie roadie enroll https://station.example --code ABCD-1234-7F3A
```

Run as yourself, those two commands stop with a reminder, because they
would otherwise read or write your own home.

**The phone's network.** With the Pi as the phone's USB devices, the
phone gets an Ethernet link to the Pi and nothing more than an address, a
name server and a route out through `eth0`. Everything else to the Pi is
dropped, so are the LAN and every other private range, and nothing can
open a connection to the phone. [The phone's network](linux-gadget.md#network)
has the details. Two options in `/etc/roadie/phone-net.conf` guard your
own network against a phone that misbehaves, both off by default: a cap
on what the phone sends out, and a block on port 25 so it cannot send
mail through your address.

```bash
EGRESS_MBPS=20
BLOCK_SMTP=1
```

A change takes effect with `sudo systemctl restart 'roadie-phone-net@*'`.
`sudo roadie-phone-net status phone0` shows the firewall counters, so a
phone that is sending a great deal shows up there.

## The choices that are yours

`setup/harden` in the checkout makes them, one subcommand each.
`~/Projects/roadie/setup/harden status`
shows the current state first: whether SSH takes passwords, whether a
key could get in, which unneeded services run and what is listening.

### SSH with keys only

Password logins are the one door on the Pi worth closing first, and the
one that must not be closed on yourself. The script refuses to go ahead
until a key can log in, either an `authorized_keys` entry for your
account or Tailscale SSH, and even then the change undoes itself after
five minutes unless you confirm from a fresh login.

From your own computer, give the Pi your key, or turn on Tailscale SSH:

```bash
ssh-copy-id you@roadie-01.local        # or: sudo tailscale set --ssh (on the Pi)
```

Then, on the Pi:

```bash
sudo ~/Projects/roadie/setup/harden ssh-keys-only
```

Open a new terminal and log in. If that works, confirm within the five
minutes:

```bash
sudo ~/Projects/roadie/setup/harden ssh-keys-only --keep
```

If it does not, do nothing: the timer puts password logins back, and the
drop-in it wrote, `/etc/ssh/sshd_config.d/10-roadie-keys-only.conf`, is
gone again. Removing that file by hand and reloading `ssh` has the same
effect at any later time.

### Services a Roadie does not need

Raspberry Pi OS starts a few services that a Pi serving only as a Roadie
never uses, and some of them listen on every address: `rpcbind` and
`nfs-blkmap` for NFS, `cups` for printing, `ModemManager` and
`bluetooth`. Stopping and disabling them is reversible:

```bash
sudo ~/Projects/roadie/setup/harden services          # off
sudo ~/Projects/roadie/setup/harden services --undo   # on again
```

The desktop's VNC server stays unless you add `--vnc`, since on a Pi with
a screen it may well be how you reach the desktop. Avahi stays too: it is
what makes `roadie-01.local` resolve.

### The hardware watchdog

The Pi 4 has a hardware watchdog that reboots the board when nothing
pets it. With `sudo ~/Projects/roadie/setup/harden watchdog`, systemd takes on that job,
so a Pi whose init hangs comes back by itself within fifteen seconds
rather than waiting for someone to pull the plug. It does not react to a
hung roadie; the service's own watchdog covers that. `--undo` turns it
off.

## What a port scan should find

From the phone, nothing. The phone's link accepts DHCP, DNS and ping
addressed to the Pi and drops everything else, including SSH and
Roadie's own port, and it forwards only to the Internet. From the LAN,
the Pi answers on SSH, on Roadie's port (which needs the token, or the
tailnet, to do anything) and on whatever you left running: before the
services step above, that includes `rpcbind` and VNC.
`~/Projects/roadie/setup/harden status` lists what is listening, and
`nmap -p- <pi>` from another machine on the LAN is the check that counts.

If you add a firewall on the Pi, allow TCP and UDP on Roadie's port from
the LAN. WebRTC media uses UDP on the same number as the HTTP port, unless
`--rtc-udp` names another.

## A read-only root filesystem

Not set up by any script here. Raspberry Pi OS can run from a read-only
root with an overlay in memory (`sudo raspi-config nonint
enable_overlayfs`, with the boot partition read-only as well), which
protects the SD card from corruption on power loss and keeps a tampered
Pi from staying tampered. Before turning it on, keep in mind:

- `/var/lib/roadie` holds the rig key, the policy and the token. On an
  overlay they would be lost at every boot, so they need a writable
  partition of their own, mounted there, or the Pi re-enrolls after
  each restart.
- The journal lives in memory anyway on Raspberry Pi OS; the persistent
  journal from [How to Run Roadie](run-roadie.md#the-log) would need the
  same writable partition.
- `machine-setup` and `gadget/install` write to `/etc`, `/usr/local` and
  `/boot/firmware`, so updating means disabling the overlay, updating,
  and enabling it again.
- `roadie-phone-net` keeps the phone's DHCP leases in
  `/var/lib/roadie-phone-net`; losing them is harmless, since the phone's
  address is pinned to its MAC.
