# How to Watch the Tests Run

`make test-e2e-pi` drives a real browser on a display of its own, which you
cannot see. To watch it instead:

```bash
make test-e2e-pi-vnc
```

It prints where to connect and then **waits until a viewer actually
connects** before running anything, so there is no race to win:

```
  📺 Watch the test at:  10.1.10.105:5934
     password is in /home/you/.config/roadie/vncpasswd

  ⏳ Waiting for a viewer to connect
```

| Your machine | Connect with |
|--------------|--------------|
| macOS | `open vnc://10.1.10.105:5934` (built-in Screen Sharing) |
| Linux | `remmina`, `vinagre`, or `xtightvncviewer 10.1.10.105:5934` |
| Windows | RealVNC Viewer or TigerVNC |

The port is always **5934** and the password is generated once per machine, so
save both in your viewer and connecting is a single click from then on.

Worth watching: the test page draws what it receives — a cursor ring, a blob
where a touch lands, typed text, and an event log — so it is usually the
fastest way to see *why* a leg failed rather than just that it did.

## Notes

The connection is **view-only**. That is not only caution: a stray click from
whoever is watching would move focus on the test's display and break the run.
Set `ROADIE_E2E_VNC_CONTROL=1` if you really want to drive it, and don't touch
anything mid-test.

Other knobs, rarely needed:

| Variable | Does |
|----------|------|
| `ROADIE_E2E_VNC_WAIT=0` | start immediately instead of waiting for a viewer |
| `ROADIE_E2E_VNC_PORT=5940` | use a different port |
| `ROADIE_E2E_VNC_LISTEN=localhost` | refuse LAN connections; reach it through `ssh -L 5934:localhost:5934 you@your-pi` |
| `ROADIE_E2E_RESOLUTION=1920x1080` | make the display bigger |

## When it doesn't work

| Symptom | Cause |
|---------|-------|
| viewer shows a black screen | the run already finished and tore the display down |
| `connection refused` | the run hasn't started its VNC server yet, or has ended |
| it sits on "Waiting for a viewer" | your viewer isn't really connected |
| `x11vnc not installed` | run `make setup` |

See [Test the Input Chain](test-the-chain.md) for what the tests check.
