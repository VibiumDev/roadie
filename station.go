package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/VibiumDev/station"
	"github.com/VibiumDev/station/enroll"
	"github.com/VibiumDev/station/rig"
)

// Roadie as a rig: enrolled with a Vibium Station, it keeps a tunnel to
// the station and serves the endpoints the station may reach behind the
// rig library's gate. The files live next to the local token:
// rig.key (the rig's key), rig.json (the policy: stations and what each
// may do) and rig-state.json (the hold state).

func rigPath(name string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "roadie", name), nil
}

// rigAllowlist is what a station may reach on this Roadie, by scope, with
// how many connections one hold may keep open on each. Everything else is
// refused through the tunnel. The admin endpoints need the admin scope,
// which the rig policy grants or not (`roadie enroll --admin`); the
// resets, the shell-like pages and the local login never appear here.
// The MJPEG stream is capped for relayed viewers (--relay-fps).
var rigAllowlist = rig.Allowlist{
	{Path: "/api/webrtc/signal", Scope: station.ScopeView, FirstMessage: true},
	{Method: http.MethodGet, Path: "/health", Scope: station.ScopeView},
	{Method: http.MethodGet, Path: "/viewer/extras.js", Scope: station.ScopeView},
	{Method: http.MethodGet, Path: "/stream", Scope: station.ScopeView, Max: 2}, // two: a page reloading while its old stream closes
	{Method: http.MethodGet, Path: "/snapshot", Scope: station.ScopeView},
	{Path: "/api/hid/ws", Scope: station.ScopeInput, Max: 2},
	{Path: "/audio", Scope: station.ScopeView, Max: 1},
	{Path: "/api/mic", Scope: station.ScopeInput, Max: 1},
	{Path: "/session", Scope: station.ScopeBidi, Max: 2}, // two: one closing while the next opens
	{Method: http.MethodGet, Path: "/api/hid/status", Scope: station.ScopeView},
	{Path: "/api/hid/", Scope: station.ScopeInput},
	{Path: "/api/settings", Scope: station.ScopeAdmin},
	{Path: "/api/crop", Scope: station.ScopeAdmin},
	{Path: "/api/rotate", Scope: station.ScopeInput}, // turning the screen is a controller's move
	{Path: "/api/capture/reset", Scope: station.ScopeAdmin},
	{Path: "/api/usb/reset", Scope: station.ScopeAdmin},
	{Path: "/api/relay/reset", Scope: station.ScopeAdmin},
}

// deviceOffer describes the target as a device for the station: its kind,
// what this Roadie can do with it, the pointer mode viewers should start
// in (--input, else the platform's), and the toolbar buttons the shared
// viewer should show, as the keyboard shortcuts the target takes.
func deviceOffer(platform Platform, hasHID, hasAudio bool, input string) enroll.Offer {
	o := enroll.Offer{Name: "phone", Kind: "phone", Caps: []string{station.CapVideo, station.CapBidi, station.CapMJPEG}, Input: input, Extras: "/viewer/extras.js"}
	if hasAudio {
		o.Caps = append(o.Caps, station.CapAudio)
	}
	if hasHID {
		o.Caps = append(o.Caps, station.CapHID)
	}
	switch platform {
	case PlatformIOS:
		o.Shortcuts = []station.Shortcut{{Label: "Home", Keys: "GUI+h"}, {Label: "Lock", Keys: "Ctrl+GUI+q"}, {Label: "Wake", Keys: "Shift"}}
	case PlatformAndroid:
		o.Shortcuts = []station.Shortcut{{Label: "Home", Keys: "GUI+h"}, {Label: "Back", Keys: "Escape"}, {Label: "Recents", Keys: "Alt+Tab"},
			{Label: "Vol +", Keys: "128"}, {Label: "Vol -", Keys: "129"}, {Label: "Power", Keys: "102"}}
	default:
		o.Name, o.Kind = "computer", "computer"
	}
	return o
}

// rigRunner is the running rig, for /health and shutdown.
type rigRunner struct {
	rig    *rig.Rig
	cancel context.CancelFunc
	done   chan struct{}
}

// startRig connects to the stations in the policy, if any. handler is the
// server's mux without the local login, which the gate replaces.
func startRig(handler http.Handler, offer enroll.Offer, onEnd func(deviceID, holdID string)) (*rigRunner, error) {
	policyPath, err := rigPath("rig.json")
	if err != nil {
		return nil, err
	}
	policy, err := rig.LoadPolicy(policyPath)
	if err != nil {
		return nil, err
	}
	if len(policy.Stations) == 0 {
		return nil, nil
	}
	keyPath, _ := rigPath("rig.key")
	key, _, err := rig.LoadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	statePath, _ := rigPath("rig-state.json")
	r := &rig.Rig{Key: key, Policy: policy, Devices: []enroll.Offer{offer}, Handler: handler, Allow: rigAllowlist,
		StatePath: statePath, Logf: log.Printf,
		Gate: &rig.Gate{Policy: policy, Key: key.Public(), StatePath: statePath, OnEnd: onEnd, Logf: log.Printf}}
	ctx, cancel := context.WithCancel(context.Background())
	rr := &rigRunner{rig: r, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(rr.done)
		r.Run(ctx)
	}()
	var names []string
	for _, e := range policy.Stations {
		names = append(names, e.Name)
	}
	fmt.Printf("📡 Station: rig %s, connecting to %s\n", rig.Npub(key.Public()), strings.Join(names, ", "))
	return rr, nil
}

// status is the station block of /health.
func (rr *rigRunner) status() map[string]any {
	online := rr.rig.Online()
	if online == nil {
		online = []string{}
	}
	var stations []map[string]any
	for _, e := range rr.rig.Policy.Stations {
		up := false
		for _, n := range online {
			if n == e.Name {
				up = true
			}
		}
		st := map[string]any{"name": e.Name, "url": e.URL, "rig": e.RigID, "online": up}
		for _, d := range rr.rig.Devices {
			if hold, held := rr.rig.Gate.Current(station.DeviceID(e.RigID, d.Name)); held {
				st["hold"] = hold
			}
		}
		stations = append(stations, st)
	}
	return map[string]any{"npub": rig.Npub(rr.rig.Key.Public()), "stations": stations}
}

func (rr *rigRunner) shutdown() {
	rr.cancel()
	select {
	case <-rr.done:
	case <-time.After(3 * time.Second):
	}
}

// enrollCommand is `roadie enroll [URL] --code CODE`: it joins a station
// and writes the policy entry; the running service picks it up on its
// next start.
func enrollCommand(args []string) int {
	fs := flag.NewFlagSet("roadie enroll", flag.ContinueOnError)
	code := fs.String("code", "", "the one-time code from the station (required)")
	name := fs.String("name", "", "this rig's name (default: the host name)")
	platformFlag := fs.String("platform", "", "the target's platform, as for the server: ios, android, mac, windows, linux")
	admin := fs.Bool("admin", false, "accept admin grants from this station (settings, resets)")
	fs.Usage = func() {
		fmt.Println("usage: roadie enroll [https://station[:port]] --code ABCD-1234-7F3A [--name NAME] [--platform P] [--admin]\n" +
			"Without a URL the station is found on this network over mDNS.")
		fs.PrintDefaults()
	}
	var url string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		url, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if url == "" && fs.NArg() > 0 {
		url = fs.Arg(0)
	}
	if *code == "" {
		fmt.Fprintln(os.Stderr, "roadie enroll: --code is required (the station's Settings page or `station enroll` makes one)")
		return 2
	}
	if *name == "" {
		*name, _ = os.Hostname()
	}
	platform, err := ParsePlatform(*platformFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	keyPath, err := rigPath("rig.key")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	key, created, err := rig.LoadOrCreateKey(keyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if created {
		fmt.Printf("New rig key: %s\n", rig.Npub(key.Public()))
	}
	policyPath, _ := rigPath("rig.json")
	policy, err := rig.LoadPolicy(policyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if url == "" {
		fmt.Println("Looking for a station on this network...")
	}
	entry, err := rig.Enroll(context.Background(), rig.EnrollOptions{
		URL: url, Code: *code, Name: *name, Key: key, Admin: *admin,
		Devices: []enroll.Offer{deviceOffer(platform, true, true, platform.DefaultInputMode())},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "roadie enroll:", err)
		return 1
	}
	policy.Set(entry)
	if err := policy.Save(policyPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	scopes, _ := json.Marshal(entry.Scopes)
	fmt.Printf("Enrolled with %s at %s as %s.\n", entry.Name, entry.URL, entry.RigID)
	fmt.Printf("Policy written to %s: scopes %s, admin %v, sessions up to %s. Edit it to change what the station may do.\n", policyPath, scopes, entry.Admin, entry.MaxSession)
	fmt.Println("Restart roadie to connect.")
	return 0
}
