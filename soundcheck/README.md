# Soundcheck

`index.html` is a one-file page for checking a phone's speaker and microphone.
**Speaker** plays five notes. **Mic** records for a few seconds and then plays
back what it heard. `make test-e2e-iphone` uses it for the iPhone's microphone
(see [Testing](../docs/how-to/testing.md#testing-a-phone-instead)).
`?seconds=N` sets how long Mic records (2 to 30, default 8).

`white.html`, at `/white`, is white to the screen's edges, for saving a crop
in Roadie: auto-crop can't find the edges of a dark screen (see
[`/api/crop`](../API.md#get-put-post-delete-apicrop)).

`main.go` serves both. The pages are built into the binary, so deploying is
copying one file. It serves them at `/` and `/white` and nothing else, with
headers that let the pages use the microphone and nothing more. It has to sit behind
HTTPS, because Safari gives the microphone only to a secure page; on exe.dev,
the proxy provides that.

## Building it

From this directory, with Go installed:

```bash
make test                     # check it
make                          # build it for this machine
make run                      # serve it at http://localhost:8000/ (ADDR=:9000 to change)
```

The microphone works at `localhost` too, since browsers treat it as secure.
The pages are built in, so rebuild after changing them. `make help` lists
the targets.

## Hosting it on exe.dev

These steps put it at `https://soundcheck.vibium.dev` on an
[exe.dev](https://exe.dev) VM named `soundcheck`. If that name is taken,
choose another and use it everywhere `soundcheck` names the VM below.

Run them from this directory.

**1. Make the VM.**

```bash
ssh exe.dev new --name=soundcheck
```

**2. Build it for the VM, copy it over, and start it.**

```bash
make deploy
```

This runs the tests, asks the VM which processor it has (`amd64` or `arm64`)
and builds for that, as one file that needs nothing else installed. It then
copies the server and `soundcheck.service` over and starts the service, which
listens on port 8000. For a VM with another name, `make deploy VM=othername`.

**3. Point exe.dev's HTTPS proxy at port 8000, and make it public.** A VM's
site is private by default: anyone not logged in to exe.dev, like the
iPhone's Safari, gets a login page instead.

```bash
ssh exe.dev share port soundcheck 8000
ssh exe.dev share set-public soundcheck
```

The page is now at `https://soundcheck.exe.xyz/`. Open it in a private
window to check that it loads without a login.

**4. Give it the domain.** In vibium.dev's DNS, add:

```
soundcheck.vibium.dev.   CNAME   soundcheck.exe.xyz.
```

If the DNS is on Cloudflare, make the record **DNS only** (grey cloud), not
proxied. Once it resolves (`dig +short soundcheck.vibium.dev`), register it:

```bash
ssh exe.dev domain add soundcheck soundcheck.vibium.dev
```

exe.dev issues the certificate itself. Until the domain is registered,
requests for it get `421 Misdirected Request`.

## Checking it

```bash
make check
```

That should show `200` for `/`, with the server's `Permissions-Policy:
microphone=(self)`, and `200` for `/white`. Then
open the page on the iPhone and tap **Mic**: Safari asks for the microphone,
the page counts down, and plays back what it heard.

## Updating it

Edit `index.html` or `white.html` and try it locally (see
[Building it](#building-it)), then put it on the VM again:

```bash
make deploy
```

## When something's wrong

| Symptom | Fix |
|---|---|
| exe.dev login page instead of the page | `ssh exe.dev share set-public soundcheck` |
| `421 Misdirected Request` | the domain isn't registered: step 4's `domain add` |
| an error from exe.dev instead of the page | the server may be down: `ssh soundcheck.exe.xyz 'systemctl status soundcheck'` |
| "No microphone: this page has to be opened over HTTPS" | it was opened over `http://` |
| "Recorded silence" | the mic heard nothing; on the test rig, Roadie didn't play into it |
| the test's microphone check fails | look at `/tmp/roadie_e2e_mic.jpg`, the phone's screen at the end |
