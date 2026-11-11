# How to Run Roadie

On a Pi set up with [`machine-setup`](configure-os.md), Roadie runs as the
systemd service `roadie`. It starts at boot and runs as the `roadie`
account inside a sandbox, which [How to Harden the Pi](harden-the-pi.md)
describes. It starts again by itself within 2 s if it stops for any reason
other than a `stop`, and is restarted when it stops answering for 30 s.

## Start, stop, restart

```bash
systemctl status roadie          # is it running?
sudo systemctl stop roadie
sudo systemctl start roadie
sudo systemctl restart roadie
```

A restart doesn't disturb the phone: with the [Pi as the phone's USB
devices](linux-gadget.md), the phone keeps its keyboard, audio and network,
and a page open on `/drive` brings its picture back by itself within a second
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
ROADIE_ARGS="--hid-backend linux-gadget --platform ios --port 8080 --auth token"
```

Edit it, then `sudo systemctl restart roadie`. `--auth token` is what lets
other machines in; see [Who may connect](#who-may-connect). `roadie --help` lists every
flag; `--platform` (`android`, `ios`, `mac`, `windows`, `linux`) says what is
on the other end, and [Connect a Phone](connect-a-phone.md) covers what each
phone needs. [`machine-setup`](configure-os.md#updating) writes the file the
first time only, so your edits survive updates.

## Who may connect

Roadie's command line says who may use its pages and API:

| `--auth` | Who gets in |
|---|---|
| unset | Only this machine: Roadie listens on localhost. For a quick build on a laptop. |
| `token` | Whoever has the local admin token. Browsers sign in once at `/login` and keep a cookie for 30 days; agents and scripts send `Authorization: Bearer <token>`. The service on a Pi runs this way. |
| `tailscale` | Whoever the local `tailscaled` says a connection belongs to: anyone on your tailnet, or only the logins and tags in `--allow you@example.com,tag:ci`. Nothing to type; connections from the LAN that are not from the tailnet are refused. |
| `none` | Everyone who can reach the port, with a warning at every start. |

The token lives in `/var/lib/roadie/.config/roadie/token`, readable by the
`roadie` account only, so the commands that show or replace it run as that
account:

```bash
sudo -u roadie roadie token            # prints it; makes one on the first run
sudo -u roadie roadie token --rotate   # a new one; every browser signs in again
```

Run as yourself, `roadie token` stops with that reminder rather than make a
second token in your own home. A build run by hand, with no service, keeps
its token in your own `~/.config/roadie`.

A browser that opens `/drive` is sent to `/login`, where the token goes in
once. From a script:

```bash
curl -H "Authorization: Bearer $(sudo -u roadie roadie token)" http://roadie.local:8080/health
```

`--auth tailscale` asks `tailscaled` over its local socket, so the `roadie`
account must be allowed to: `sudo tailscale set --operator=roadie`. Behind
`tailscale serve` it reads a header instead; see [HTTPS](#https).

## HTTPS

The browser's microphone needs a secure page (https, or localhost), and so
does anything not on localhost that you would rather not send in the clear.
Two ways to get one.

### From your tailnet, with `tailscale serve`

The quickest on a tailnet, with no certificate files to manage: `tailscaled`
answers HTTPS on the machine's `*.ts.net` name with a certificate it fetches
and renews itself, and passes each request to Roadie on localhost with the
caller's login in a `Tailscale-User-Login` header. Bind Roadie to localhost
and it trusts that header:

```bash
# /etc/default/roadie
ROADIE_ARGS="--hid-backend linux-gadget --platform ios --port 8080 --auth tailscale --bind 127.0.0.1"
```

```bash
sudo systemctl restart roadie
sudo tailscale serve --bg 8080
```

The viewer is then at `https://<machine>.<tailnet>.ts.net/drive` for everyone
on the tailnet, or only the logins and tags in `--allow you@example.com`.
Agents and scripts on the tailnet use the same address and need no token.
`--bg` keeps the rule across reboots; `tailscale serve status` shows it and
`tailscale serve off` removes it. HTTPS certificates must be enabled for the
tailnet (the admin console, under DNS), and the first request after setup
waits while the certificate is issued, which can take a minute.

Everything goes through serve: the pages, the API, the MJPEG stream and the
WebSockets (`/api/hid/ws`, `/audio`, `/api/mic`, `/session` and WebRTC
signaling). WebRTC media itself still travels directly between the browser
and Roadie over their tailnet addresses.

Why localhost only: serve connects to Roadie from the machine itself, so
Roadie cannot ask `tailscaled` who the caller is and reads the header
instead. A process on the machine could forge that header, so Roadie only
trusts it when nothing else can reach the port. `tailscaled` replaces any
`Tailscale-User-Login` a client sends with the caller's real login, and
leaves it out for a tagged machine (`tag:ci`), which Roadie then refuses: for
tags, use Roadie's own TLS below. The plain `http://<machine>:8080` address
stops answering once Roadie binds to localhost.

### Roadie's own TLS

Roadie serves TLS itself from a certificate and key:

```bash
ROADIE_ARGS="... --auth token --port 443 --tls-cert /etc/roadie/cert.pem --tls-key /etc/roadie/key.pem --http-redirect :80"
```

The certificate and key files are read again when they change, so a renewal
needs no restart; the service unit has the capability to bind ports below
1024 without root. `roadie tls status --tls-cert /etc/roadie/cert.pem` shows
the names and expiry. A wildcard certificate from your DNS provider, copied
to the Pi by whatever renews it, is the simplest arrangement; on a tailnet,
`sudo tailscale cert <machine>.<tailnet>.ts.net` writes a pair for that name
(run it again before the 90 days are up). A station can manage certificates
for its rigs later.

## Join a station

A [Vibium Station](https://github.com/VibiumDev/station) lets you reach
this Roadie from anywhere without opening a port: Roadie dials out to it
and keeps a tunnel, and the station signs holds that Roadie checks itself.

On the station's Settings page make an enrollment code, then on the Pi:

```bash
sudo -u roadie roadie enroll https://station.example --code ABCD-1234-7F3A --name "pi in the lab" --platform ios
sudo systemctl restart roadie
```

On the same network the URL can be left out. The code's last group is the
station key's fingerprint, which `roadie enroll` checks before it sends
anything, so a spoofed station on the LAN gets nothing. `--admin` lets the
station's admin grants through (settings, resets); by default they are
refused. `--record` has Roadie record every hold from that station, as a
screenshot after each burst of input in Vibium's recording format, and
upload it to the station's Record Store when the hold ends, where it is
listed on the station's Records page next to who held the device and when.
Manual sessions count as much as an agent's, and Roadie tells the station,
whose Devices page notes "records sessions" under the host and whose viewer shows REC while a session is live, saying on hover what is recorded.

A recorded hold also keeps a video of the screen and what the phone played:

- The Record Player shows the video in place of the screenshots, with its sound.
- The video costs no second encode. It is what Roadie already sends its viewers.
- One hold keeps at most 128 MB of video. `--record-video-mb` changes that, and `0` turns video and sound off.
- A session that Vibium records, such as a station's jam, has its video in Vibium's own recording. Roadie's recording of that hold then has none.

The [API reference](../../API.md) has the details under "Video and sound in a recording".

What the station may do is yours to edit in
`/var/lib/roadie/.config/roadie/rig.json`:
the scopes (`view`, `input`, `bidi`), `admin`, `record`, `max_session` and
`hours`.
`/health` shows the `station` block with the tunnel's state and the hold
held, and the service log says when it connects. To leave a station, remove
its entry from the file and restart; to be rid of a rig the station's owner
removes it on their Settings page, after which its key is refused.

## What a restart keeps

| Kept | From |
|---|---|
| Flags | `/etc/default/roadie` |
| Saved crops and the crop choice | `/var/lib/roadie/.config/roadie/crop-<name>.json`, by `--name` ([`/api/crop`](../../API.md#get-put-post-delete-apicrop)) |

Everything changed through [`/api/settings`](../../API.md#put-apisettings),
such as quality, frame rate and resolution, goes back to the flags. A browser
that opens `/drive` puts back the quality, frame rate and size it last chose
there.

## Run it by hand

To try a build or a flag without touching the service, stop the service
first: it holds port 8080 and the input devices. From the repository, with
the flags from `/etc/default/roadie` or others:

```bash
sudo systemctl stop roadie
make && ./roadie --hid-backend linux-gadget --platform ios --auth token
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
