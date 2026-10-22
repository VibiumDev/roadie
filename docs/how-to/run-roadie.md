# How to Run Roadie

On a Pi set up with [`machine-setup`](configure-os.md), Roadie runs as the
systemd service `roadie`. It starts at boot, runs as your user, and starts
again by itself within 2 s if it stops for any reason other than a `stop`.

## Start, stop, restart

```bash
systemctl status roadie          # is it running?
sudo systemctl stop roadie
sudo systemctl start roadie
sudo systemctl restart roadie
```

A restart doesn't disturb the phone: with the [Pi as the phone's USB
devices](linux-gadget.md), the phone keeps its keyboard, audio and network,
and a page open on `/view` brings its picture back by itself within a second
or two of Roadie answering again.

To check that it's serving:

```bash
curl -s http://localhost:8080/health
```

## The log

```bash
journalctl -u roadie -f                  # follow it
journalctl -u roadie --since "1 hour ago"
journalctl -u roadie -b                  # since the Pi started
```

Raspberry Pi OS keeps the log in memory only, so it starts afresh at every
boot. To keep it across reboots:

```bash
sudo mkdir -p /etc/systemd/journald.conf.d
printf '[Journal]\nStorage=persistent\n' | sudo tee /etc/systemd/journald.conf.d/persistent.conf
sudo systemctl restart systemd-journald
```

## Change its flags

Roadie's command-line flags live in `/etc/default/roadie`:

```bash
ROADIE_ARGS="--hid-backend linux-gadget --platform ios --port 8080"
```

Edit it, then `sudo systemctl restart roadie`. `roadie --help` lists every
flag; `--platform` (`android`, `ios`, `mac`, `windows`, `linux`) says what is
on the other end, and [Connect a Phone](connect-a-phone.md) covers what each
phone needs. [`machine-setup`](configure-os.md#updating) writes the file the
first time only, so your edits survive updates.

## What a restart keeps

| Kept | From |
|---|---|
| Flags | `/etc/default/roadie` |
| Saved crops and the crop choice | `~/.config/roadie/crop-<name>.json`, by `--name` ([`/api/crop`](../../API.md#get-put-post-delete-apicrop)) |

Everything changed through [`/api/settings`](../../API.md#put-apisettings),
such as quality, frame rate and resolution, goes back to the flags. A browser
that opens `/view` puts back the quality, frame rate and size it last chose
there.

## Run it by hand

To try a build or a flag without touching the service, stop the service
first: it holds port 8080 and the input devices. From the repository, with
the flags from `/etc/default/roadie` or others:

```bash
sudo systemctl stop roadie
make && ./roadie --hid-backend linux-gadget --platform ios
# Ctrl-C when done, then:
sudo systemctl start roadie
```

It needs the groups the service runs with (`roadie`, `audio`, `video`,
`dialout`); the set-up scripts add your user to them, from your next login.

## Put a new build in place

[`machine-setup`](configure-os.md#updating), run again, pulls, builds and
restarts. To install what you have built yourself:

```bash
make && sudo install -m 755 roadie /usr/local/bin/roadie && sudo systemctl restart roadie
```

## At boot

```bash
sudo systemctl disable roadie    # don't start it at boot
sudo systemctl enable roadie     # start it at boot again
```

`disable --now` and `enable --now` also stop or start it now.

## The gadget service

With the [Pi as the phone's USB devices](linux-gadget.md), `roadie-gadget`
is a separate service that makes the Pi's USB-C port the phone's keyboard,
mouse, touchscreen, microphone, speaker and network. It rarely needs
touching: restarting it rebuilds the gadget when its settings have changed,
which the phone sees as an unplug and replug. See
[Recovery](linux-gadget.md#recovery) for what to try when the phone stops
responding, and `roadie-gadget status` for what it has set up.
