# Roadie: standing rules for coding agents

Roadie is a USB KVM for phones and computers. It captures a target's HDMI
output from a USB 3 capture dongle (YUYV or MJPEG, via pion/mediadevices)
and sends keyboard, mouse and multi-touch input to the target as a USB
gadget (Raspberry Pi 4) or through the QT Py relay and HID boards. It serves
an MJPEG stream and `/view` on the LAN, takes input over `/api/hid/ws`, a
REST API and a WebDriver BiDi endpoint (`/session`), and handles USB audio
both ways. All of that works and must keep working.

## Where things are

- Vibium Station (`VibiumDev/station`) is the sibling repo at `../station`.
  The spec for the remote work (WebRTC, tunnel, leases, Station) is
  `../station/docs/remote-spec.md`. It is the source of truth; read it in
  full before writing code.
- Progress, decisions, measured numbers and known issues live in
  `../station/docs/progress.md`. Update it at every checkpoint so a fresh
  session can continue.
- Station owns the rig library (tunnel client, enrollment, token checks,
  endpoint allowlist); Roadie imports it. Commit to Station first, then bump
  Roadie's dependency. The `go.work` in the parent directory that ties the
  two modules together is never committed (it is in `.gitignore`).
- `.claude/CLAUDE.md` also applies: never `git push` without asking first,
  even when told to "commit and push". Commits stay per repo. Commit messages are short and to the point,
  no essays. No Co-Authored-By or other attribution lines, in any repo.

## Open source boundaries

- Everything here must make sense for one person running their own Pis. No
  vendor accounts, email services, billing, multi-tenant users or
  provider-only code.
- Station is device-neutral; phone-specific code lives here in Roadie.
- Not Pi-only. "Pi" means the Roadie host: a Raspberry Pi 4 is the reference,
  but a Mac mini and a Linux PC must keep working. Feature-detect the HID
  backend (USB gadget or the QT Py boards), audio paths, Ethernet gadget and
  encoder, and announce them to the station as capabilities. Never assume
  `/dev/video11` or USB gadget mode exist. CI builds linux/arm64,
  linux/amd64 and darwin/arm64.
- Nostr-ready, as the spec describes: all keys are secp256k1 with BIP-340
  Schnorr, shown as npub/nsec (NIP-19); lease grants are NIP-01 events with
  Station's documented kinds; use fiatjaf.com/nostr or btcec, pure Go only.
  The transport stays the tunnel; never publish private events; no NIP-90.
- Rig policy: a local file on the rig lists accepted stations (by npub, more
  than one allowed) and, per station, allowed scopes, whether admin grants
  are accepted, and maximum session length. One active lease across all
  stations; the Pi arbitrates.

## Security rules

- The Pi is the final gate. Verify every lease grant locally: signature by a
  station in the rig policy, this rig and one of its devices, current lease
  or higher seq, scopes, NIP-40 expiration (30 s skew), event id as the
  replay nonce, and the rig policy. Refuse older seq. Apply revocations
  within 1 s. Fail closed; never fall back to unauthenticated mode while
  enrolled.
- Never add a remote endpoint that runs commands, reads or writes files, or
  changes Pi configuration for non-admin tokens. User and agent tokens reach
  only an allowlist: HID input, video, audio, screenshots, MJPEG, BiDi and
  read-only status. Settings, crop, rotate, capture settings and resets
  (`/api/settings`, `/api/crop`, `/api/rotate`, `/api/capture/reset`,
  `/api/usb/reset`, `/api/relay/reset`) are admin-only remotely. Admin
  tokens are a separate type that user and agent tokens can never mint.
- Per-lease rate limits on the Pi, not only in the station (for example
  200 HID events/s and 2,000 keystrokes/min).
- Tokens travel only in headers, the first WebSocket message or the data
  channel, never in URLs. Never log secrets. Secrets come from env vars or
  files outside the repo; never commit keys.
- Local auth is chosen with `--auth` (unset: localhost only; `token`;
  `tailscale`; `none` with a warning). Keep it simple: no pop-ups, no Basic
  Auth, no QR codes. Remote sessions always need a station lease grant.
- When changing the Pi's firewall, SSH or systemd (R6), apply changes with
  an automatic rollback timer so a mistake cannot cut off your own session.

## Engineering rules

- Prefer the standard library and small dependencies. No cgo; never
  GStreamer. No cgo for Opus (G.722 and PCMU in pure Go) or H.264 (V4L2 M2M
  from Go, or an ffmpeg subprocess). Note: pion/mediadevices already pulls
  cgo into Linux builds through its camera and microphone drivers, so
  `CGO_ENABLED=0` does not build today; the decision on that is tracked in
  `../station/docs/progress.md`.
- Keep existing behaviour and tests passing. `/stream`, `/snapshot`,
  `/raw-stream`, `/raw-snapshot`, `/view`, `/grid`, `/api/hid/ws`, `/audio`,
  `/api/mic`, the REST API and `/session` stay unchanged on the LAN and are
  the relayed fallback.
- Adapt the spec's names to what exists rather than adding parallel ones:
  Roadie takes `--port` (auto from 8080), so a bind address goes alongside
  it; mDNS is `_roadie._tcp` with hashicorp/mdns; the mic WebSocket is
  `/api/mic`; `--hid-backend` defaults to `rp2040` with `linux-gadget`
  opt-in; `setup/roadie.service` is what R6 builds on.
- The viewer keeps every existing control: the configurable toolbar, Home,
  Back, Recents, Lock, volume, mute, mic, keyboard and Screenshot buttons,
  iPhone rotation, and `/grid`.
- Coordinates stay in Roadie's absolute HID units (0 to 32767 across the
  cropped frame). The WebRTC `hid` data channel carries exactly the JSON
  `/api/hid/ws` accepts today, through one handler for both transports.
- Add tests for token checks (valid, expired, wrong device, wrong signer,
  older seq, missing scope, endpoint allowlist, replayed event id, grant
  outside the rig policy, station not in the policy), enrollment (wrong
  fingerprint, reused or expired code), the tunnel protocol and HID
  validation. `govulncheck` runs in CI.
- Pages stay extremely minimal, like exe.dev: server-rendered HTML, system
  fonts, monochrome plus one accent colour, no frameworks, no build step.
- Update `README.md`, `API.md` and `docs/` for every user-visible change.
  `docs/` follows tutorials, how-to, reference, explanation and plans.

## Working on the dev Pi

- This checkout is on the dev Pi: a Raspberry Pi 4 with a UGREEN 15389
  dongle and a phone attached through the USB gadget. Test Roadie changes
  directly on it.
- `roadie.service` runs `/usr/local/bin/roadie` with the args in
  `/etc/default/roadie` and holds the capture dongle. Stop it while a test
  needs `/dev/video0`, and start it again afterwards.
- `make build`, `make test-unit`, `make test` (hardware tests too when the
  boards are connected). The Go toolchain comes from `go.mod` via
  `GOTOOLCHAIN=auto`.

## How to work

- Milestones R0 to R6 in order, each with a gate (see the spec). Plan the
  steps inside a milestone yourself. At the end of each milestone, stop,
  report what changed, how it was tested, measured numbers and any deviation
  from the spec, and wait for approval.
- If something in the spec is wrong or impossible on this hardware, stop,
  explain, and propose the smallest alternative.
