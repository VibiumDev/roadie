package main

import (
	"context"
	"testing"
	"time"
)

func recvOne(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("listener channel closed")
		}
		return c
	case <-time.After(time.Second):
		t.Fatal("nothing forwarded")
	}
	return nil
}

func expectNothing(t *testing.T, ch <-chan []byte, what string) {
	t.Helper()
	select {
	case c := <-ch:
		t.Fatalf("%s: forwarded %q", what, c)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAudioMuxFollowsThePhone(t *testing.T) {
	out, usb, hdmi := NewAudioBroadcaster(), NewAudioBroadcaster(), NewAudioBroadcaster()
	p := AudioParams{SampleRate: 48000, Channels: 2, Format: "f32-interleaved"}
	usb.SetParams(p)
	hdmi.SetParams(p)
	m := newAudioMux(out, usb, hdmi)
	m.usbHold = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.run(ctx)
	time.Sleep(20 * time.Millisecond) // let the mux subscribe
	listener, unsub := out.Subscribe()
	defer unsub()

	if s := m.Source(); s != "" {
		t.Errorf("source %q before anything played, want none", s)
	}
	hdmi.Broadcast([]byte("hdmi-1"))
	if got := recvOne(t, listener); string(got) != "hdmi-1" {
		t.Fatalf("got %q, want HDMI while USB is idle", got)
	}
	if s := m.Source(); s != "hdmi" {
		t.Errorf("source %q, want hdmi", s)
	}
	usb.Broadcast([]byte("usb-1"))
	if got := recvOne(t, listener); string(got) != "usb-1" {
		t.Fatalf("got %q", got)
	}
	if s := m.Source(); s != "usb" {
		t.Errorf("source %q, want usb", s)
	}
	hdmi.Broadcast([]byte("hdmi-silence"))
	expectNothing(t, listener, "HDMI while USB is live")

	time.Sleep(150 * time.Millisecond) // USB has gone quiet: route moved to HDMI
	hdmi.Broadcast([]byte("hdmi-2"))
	if got := recvOne(t, listener); string(got) != "hdmi-2" {
		t.Fatalf("got %q, want HDMI after USB stopped", got)
	}
	if s := m.Source(); s != "hdmi" {
		t.Errorf("source %q, want hdmi again", s)
	}
	if out.Params() != p || !out.IsActive() {
		t.Errorf("out params %+v active %v", out.Params(), out.IsActive())
	}
}

// Switching to a source in another format makes listeners reconnect, so
// they never decode one format as another.
func TestAudioMuxRestartsOnFormatChange(t *testing.T) {
	out, usb, hdmi := NewAudioBroadcaster(), NewAudioBroadcaster(), NewAudioBroadcaster()
	usb.SetParams(AudioParams{SampleRate: 48000, Channels: 2, Format: "f32-interleaved"})
	hdmi.SetParams(AudioParams{SampleRate: 44100, Channels: 2, Format: "f32-interleaved"})
	m := newAudioMux(out, usb, hdmi)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.run(ctx)
	time.Sleep(20 * time.Millisecond)
	listener, unsub := out.Subscribe()
	usb.Broadcast([]byte("usb"))
	recvOne(t, listener)
	m.usbHold = 0
	time.Sleep(10 * time.Millisecond)
	hdmi.Broadcast([]byte("hdmi"))
	select {
	case _, ok := <-listener:
		if ok {
			t.Fatal("listener got 44.1 kHz audio while told 48 kHz")
		}
	case <-time.After(time.Second):
		t.Fatal("listener not disconnected on a format change")
	}
	unsub() // after Restart closed it: must not panic
	if out.Params().SampleRate != 44100 {
		t.Errorf("out params %+v", out.Params())
	}
}

func TestAudioMuxInactiveWithNoSource(t *testing.T) {
	out, usb := NewAudioBroadcaster(), NewAudioBroadcaster()
	m := newAudioMux(out, usb, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.run(ctx)
	time.Sleep(300 * time.Millisecond)
	if out.IsActive() {
		t.Error("/audio active with no source")
	}
	usb.SetParams(AudioParams{SampleRate: 48000, Channels: 2})
	time.Sleep(300 * time.Millisecond)
	if !out.IsActive() || out.Params().SampleRate != 48000 {
		t.Errorf("/audio not offered once the USB speaker is open: %+v", out.Params())
	}
	usb.SetInactive()
	time.Sleep(300 * time.Millisecond)
	if out.IsActive() {
		t.Error("/audio still active after its only source went away")
	}
}
