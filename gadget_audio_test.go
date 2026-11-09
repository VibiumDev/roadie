package main

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReadGadgetAudioConfig(t *testing.T) {
	fg := newFakeGadget(t)
	dir := fg.paths.function("uac2.audio")
	for attr, v := range map[string]string{"p_chmask": "1", "p_srate": "48000", "c_chmask": "3", "c_srate": "48000"} {
		fg.write(filepath.Join(dir, attr), v+"\n")
	}
	cfg, err := readGadgetAudioConfig(fg.paths)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cardID != "UAC2Gadget" || cfg.micChannels != 1 || cfg.spkChannels != 2 || cfg.micRate != 48000 {
		t.Errorf("config %+v", cfg)
	}
	fg.write(filepath.Join(dir, "c_chmask"), "0\n")
	cfg, _ = readGadgetAudioConfig(fg.paths)
	if cfg.hasSpeaker() {
		t.Error("c_chmask 0 read as having a speaker")
	}
	if _, err := readGadgetAudioConfig(gadgetPaths{sys: t.TempDir()}); err == nil {
		t.Error("no gadget read as having audio")
	}
}

func TestAlsaCardIndex(t *testing.T) {
	proc := t.TempDir()
	os.Symlink("card6", filepath.Join(proc, "UAC2Gadget"))
	if n, err := alsaCardIndex(proc, "UAC2Gadget"); err != nil || n != 6 {
		t.Errorf("card %d, %v", n, err)
	}
	if _, err := alsaCardIndex(proc, "UAC1Gadget"); err == nil {
		t.Error("missing card found")
	}
}

// A stream whose device disappears is reopened, and one that cannot be
// opened is retried, without anything else restarting.
func TestGadgetAudioReopens(t *testing.T) {
	proc := t.TempDir()
	os.Symlink("card3", filepath.Join(proc, "UAC2Gadget"))
	cfg := gadgetAudioConfig{cardID: "UAC2Gadget", micRate: 48000, micChannels: 1}
	var mu sync.Mutex
	opens := 0
	open := func(card int, playback bool, want pcmParams) (pcmStream, error) {
		mu.Lock()
		defer mu.Unlock()
		opens++
		if card != 3 || !playback || want.periodFrames != 480 {
			t.Errorf("open(card %d, playback %v, %+v)", card, playback, want)
		}
		switch opens {
		case 1:
			return nil, errors.New("busy")
		case 2:
			return &fakePCM{params: want, errs: []error{errPCMGone}}, nil
		}
		return &fakePCM{params: want}, nil
	}
	a := newGadgetAudio(cfg, nil, proc, open)
	saveMin := pcmRetryMin
	defer func() { pcmRetryMin = saveMin }()
	pcmRetryMin = 10 * time.Millisecond
	a.Start()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := opens
		mu.Unlock()
		if n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("opened %d times, want a retry and a reopen", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.Shutdown()
}
