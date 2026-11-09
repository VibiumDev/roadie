package main

import (
	"encoding/binary"
	"errors"
	"github.com/VibiumDev/roadie/internal/alsa"
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

// A stream whose device disappears is reopened, and one that cannot be
// opened is retried, without anything else restarting.
func TestGadgetAudioReopens(t *testing.T) {
	proc := t.TempDir()
	os.Symlink("card3", filepath.Join(proc, "UAC2Gadget"))
	cfg := gadgetAudioConfig{cardID: "UAC2Gadget", micRate: 48000, micChannels: 1}
	var mu sync.Mutex
	opens := 0
	open := func(card int, playback bool, want alsa.PCMParams) (alsa.PCMStream, error) {
		mu.Lock()
		defer mu.Unlock()
		opens++
		if card != 3 || !playback || want.PeriodFrames != 480 {
			t.Errorf("open(card %d, playback %v, %+v)", card, playback, want)
		}
		switch opens {
		case 1:
			return nil, errors.New("busy")
		case 2:
			return &fakePCM{params: want, errs: []error{alsa.ErrPCMGone}}, nil
		}
		return &fakePCM{params: want}, nil
	}
	a := newGadgetAudio(cfg, nil, proc, open)
	saveMin := alsa.PcmRetryMin
	defer func() { alsa.PcmRetryMin = saveMin }()
	alsa.PcmRetryMin = 10 * time.Millisecond
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

// fakePCM is a PCM stream that fails on cue; the alsa package keeps its own.
type fakePCM struct {
	params alsa.PCMParams

	mu        sync.Mutex
	written   []int16
	toRead    [][]byte
	errs      []error // returned by successive transfers, nil = normal
	recovered int
	closed    bool
}

func (f *fakePCM) Params() alsa.PCMParams { return f.params }

func (f *fakePCM) Wait(time.Duration) (bool, error) {
	time.Sleep(time.Millisecond)
	return true, nil
}

func (f *fakePCM) nextErr() error {
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return err
}

func (f *fakePCM) WriteFrames(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.nextErr(); err != nil {
		return 0, err
	}
	for i := 0; i+1 < len(b); i += 2 {
		f.written = append(f.written, int16(binary.LittleEndian.Uint16(b[i:])))
	}
	return len(b) / (2 * f.params.Channels), nil
}

func (f *fakePCM) ReadFrames(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.nextErr(); err != nil {
		return 0, err
	}
	if len(f.toRead) == 0 {
		return 0, alsa.ErrPCMAgain
	}
	n := copy(b, f.toRead[0])
	f.toRead = f.toRead[1:]
	return n / (2 * f.params.Channels), nil
}

func (f *fakePCM) Recover(error) error {
	f.mu.Lock()
	f.recovered++
	f.mu.Unlock()
	return nil
}

func (f *fakePCM) Close() error { f.closed = true; return nil }
