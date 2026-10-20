package main

import (
	"context"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

// audioMux feeds /audio from whichever way the phone is sending its sound.
//
// With the gadget's speaker the phone has two outputs into this Pi: USB (the
// gadget) and HDMI (the capture dongle). Android picks USB; iOS picks either
// and can switch by itself -- it remembers the HDMI display as an output too.
// A phone sends to one at a time, so rather than mix them (two clocks), the
// mux passes USB through whenever USB is carrying audio and HDMI otherwise.
type audioMux struct {
	out  *AudioBroadcaster // what /audio serves
	usb  *AudioBroadcaster // the gadget speaker
	hdmi *AudioBroadcaster // the capture dongle; nil without one

	// usbHold is how long after the last USB chunk HDMI stays muted, so a
	// USB stream with a hiccup does not flick over to HDMI and back.
	usbHold time.Duration

	source atomic.Value // string: "usb", "hdmi", or "" while neither plays
}

// Source is the output /audio is following: "usb" (the gadget's speaker),
// "hdmi" (the capture dongle), or "" when the phone is sending neither.
func (m *audioMux) Source() string {
	s, _ := m.source.Load().(string)
	return s
}

func newAudioMux(out, usb, hdmi *AudioBroadcaster) *audioMux {
	return &audioMux{out: out, usb: usb, hdmi: hdmi, usbHold: 300 * time.Millisecond}
}

func (m *audioMux) run(ctx context.Context) {
	usbCh, unsubUSB := m.usb.Subscribe()
	defer unsubUSB()
	var hdmiCh <-chan []byte
	if m.hdmi != nil {
		c, unsub := m.hdmi.Subscribe()
		defer unsub()
		hdmiCh = c
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	var lastUSB time.Time
	current := "" // the source /audio is passing through
	use := func(name string, src *AudioBroadcaster) {
		if name == current {
			return
		}
		p := src.Params()
		switch {
		case !m.out.IsActive():
			m.out.SetParams(p)
		case m.out.Params() != p:
			// Listeners decode with the format they were told: make them
			// reconnect for the new one.
			m.out.Restart(p)
		}
		if current != "" {
			log.Printf("audio: following the phone's %s output", name)
		}
		current = name
		m.source.Store(strings.ToLower(name))
	}
	for {
		select {
		case <-ctx.Done():
			return
		case c, ok := <-usbCh:
			if !ok {
				usbCh = nil
				continue
			}
			lastUSB = time.Now()
			use("USB", m.usb)
			m.out.Broadcast(c)
		case c, ok := <-hdmiCh:
			if !ok {
				hdmiCh = nil
				continue
			}
			if time.Since(lastUSB) < m.usbHold {
				continue // the phone is using USB; HDMI is silence
			}
			use("HDMI", m.hdmi)
			m.out.Broadcast(c)
		case <-tick.C:
			// /audio is available while either source is.
			usbOn := m.usb.IsActive()
			hdmiOn := m.hdmi != nil && m.hdmi.IsActive()
			switch {
			case !usbOn && !hdmiOn:
				m.out.SetInactive()
				current = ""
				m.source.Store("")
			case !m.out.IsActive() && usbOn:
				m.out.SetParams(m.usb.Params())
			case !m.out.IsActive() && hdmiOn:
				m.out.SetParams(m.hdmi.Params())
			}
		}
	}
}
