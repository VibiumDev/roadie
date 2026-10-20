package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func sine(freq, rate float64, n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*freq*float64(i)/rate))
	}
	return out
}

// goertzel returns the power of one frequency in a block.
func goertzel(x []int16, freq, rate float64) float64 {
	k := 2 * math.Cos(2*math.Pi*freq/rate)
	var s1, s2 float64
	for _, v := range x {
		s := float64(v) + k*s1 - s2
		s2, s1 = s1, s
	}
	return s1*s1 + s2*s2 - k*s1*s2
}

func rms(x []int16) float64 {
	var sum float64
	for _, v := range x {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(x)))
}

func TestUpsamplerPolyphase(t *testing.T) {
	u := newUpsampler(16000, 48000, 1)
	in := sine(1000, 16000, 16000, 10000)
	out := u.process(in)
	if len(out) != 3*len(in) {
		t.Fatalf("%d samples out of %d in, want 3x", len(out), len(in))
	}
	body := out[480 : len(out)-480]
	if g := rms(body) / rms(in); g < 0.97 || g > 1.03 {
		t.Errorf("gain %.3f, want ~1", g)
	}
	tone := goertzel(body, 1000, 48000)
	// Zero-stuffing makes images at 16k±1k; the filter has to remove them.
	for _, img := range []float64{15000, 17000, 31000, 33000} {
		if db := 10 * math.Log10(goertzel(body, img, 48000)/tone); db > -50 {
			t.Errorf("image at %.0f Hz only %.1f dB down", img, db)
		}
	}
}

func TestUpsamplerBlocksJoin(t *testing.T) {
	in := sine(440, 16000, 3200, 12000)
	whole := newUpsampler(16000, 48000, 1).process(in)
	u := newUpsampler(16000, 48000, 1)
	var parts []int16
	for off := 0; off < len(in); off += 333 {
		parts = append(parts, u.process(in[off:min(off+333, len(in))])...)
	}
	if len(parts) != len(whole) {
		t.Fatalf("chunked %d samples, whole %d", len(parts), len(whole))
	}
	for i := range whole {
		if parts[i] != whole[i] {
			t.Fatalf("sample %d differs: %d vs %d (blocks must join seamlessly)", i, parts[i], whole[i])
		}
	}
}

func TestUpsamplerChannelsAndOtherRates(t *testing.T) {
	stereo := newUpsampler(16000, 48000, 2).process(sine(440, 16000, 160, 5000))
	if len(stereo) != 960 {
		t.Fatalf("stereo: %d samples, want 960", len(stereo))
	}
	for i := 0; i < len(stereo); i += 2 {
		if stereo[i] != stereo[i+1] {
			t.Fatalf("frame %d: left %d right %d", i/2, stereo[i], stereo[i+1])
		}
	}
	if same := newUpsampler(16000, 16000, 1).process([]int16{1, 2, 3}); len(same) != 3 {
		t.Errorf("16 kHz to 16 kHz changed the length: %d", len(same))
	}
	u := newUpsampler(16000, 44100, 1)
	n := 0
	for i := 0; i < 100; i++ {
		n += len(u.process(sine(440, 16000, 160, 5000)))
	}
	if want := 44100; n < want-3 || n > want+3 {
		t.Errorf("1 s at 16 kHz became %d samples at 44.1 kHz", n)
	}
}

func pcmBytes(samples []int16) []byte {
	b := make([]byte, len(samples)*2)
	for i, v := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

func TestMicPrefillAndSilence(t *testing.T) {
	m := newMicStream(48000, 1)
	period := make([]int16, 480)
	m.take(period)
	if rms(period) != 0 {
		t.Error("audio from an empty queue")
	}
	m.Write(pcmBytes(sine(500, 16000, 320, 8000))) // 20 ms: under the prefill
	m.take(period)
	if rms(period) != 0 {
		t.Error("playback started before the prefill built up")
	}
	m.Write(pcmBytes(sine(500, 16000, 320, 8000))) // 40 ms queued
	m.take(period)
	if rms(period) == 0 {
		t.Fatal("no audio once the prefill was reached")
	}
	for i := 0; i < 3; i++ {
		m.take(period)
	}
	m.take(period) // queue ran dry: silence, and wait for a new prefill
	if rms(period) != 0 || m.playing {
		t.Errorf("after the queue emptied: rms %.0f playing %v", rms(period), m.playing)
	}
}

func TestMicQueueBoundedAndStale(t *testing.T) {
	m := newMicStream(48000, 1)
	for i := 0; i < 50; i++ { // a second of audio with nothing taking it
		m.Write(make([]byte, 640))
	}
	if max := m.frames(micMaxQueue); len(m.queue) > max {
		t.Errorf("queue holds %d frames, bound is %d", len(m.queue), max)
	}
	m.lastTaken = time.Now().Add(-2 * micStaleAfter)
	m.idle()
	if len(m.queue) != 0 {
		t.Error("stale audio kept after the phone stopped taking it")
	}
}

// Ten minutes of a sender running 0.5% fast with bursty delivery: latency
// must stay bounded rather than growing for as long as the call lasts.
func TestMicLongRunningDrift(t *testing.T) {
	m := newMicStream(48000, 1)
	rng := rand.New(rand.NewSource(1))
	period := make([]int16, 480)
	sentMs, takenMs := 0.0, 0.0
	maxQueue := 0
	for takenMs < 10*60*1000 {
		for sentMs < takenMs*1.005+float64(rng.Intn(60)) { // up to 60 ms of jitter
			m.Write(make([]byte, 640))
			sentMs += 20
		}
		m.take(period)
		takenMs += 10
		maxQueue = max(maxQueue, len(m.queue))
	}
	if limit := m.frames(micMaxQueue); maxQueue > limit {
		t.Errorf("queue reached %d frames, bound is %d", maxQueue, limit)
	}
}

// fakePCM is a scripted PCM stream.
type fakePCM struct {
	params pcmParams

	mu        sync.Mutex
	written   []int16
	toRead    [][]byte
	errs      []error // returned by successive transfers, nil = normal
	recovered int
	closed    bool
}

func (f *fakePCM) Params() pcmParams { return f.params }

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
	return len(b) / (2 * f.params.channels), nil
}

func (f *fakePCM) ReadFrames(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.nextErr(); err != nil {
		return 0, err
	}
	if len(f.toRead) == 0 {
		return 0, errPCMAgain
	}
	n := copy(b, f.toRead[0])
	f.toRead = f.toRead[1:]
	return n / (2 * f.params.channels), nil
}

func (f *fakePCM) Recover(error) error {
	f.mu.Lock()
	f.recovered++
	f.mu.Unlock()
	return nil
}

func (f *fakePCM) Close() error { f.closed = true; return nil }

func TestMicPumpFeedsAudioThenSilence(t *testing.T) {
	m := newMicStream(48000, 1)
	m.Write(pcmBytes(sine(500, 16000, 1600, 8000))) // 100 ms
	dev := &fakePCM{params: pcmParams{rate: 48000, channels: 1, periodFrames: 480, periods: 4},
		errs: []error{nil, errPCMXrun, nil, errPCMAgain}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- m.pump(ctx, dev) }()
	time.Sleep(60 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	dev.mu.Lock()
	defer dev.mu.Unlock()
	if dev.recovered != 1 {
		t.Errorf("recovered %d times from one xrun", dev.recovered)
	}
	if len(dev.written) < 4800+480 {
		t.Fatalf("only %d samples written", len(dev.written))
	}
	if rms(dev.written[:4800]) < 1000 {
		t.Error("the queued audio did not reach the device")
	}
	if tail := dev.written[len(dev.written)-480:]; rms(tail) != 0 {
		t.Error("device not fed silence once the audio ran out")
	}
}

func TestMicPumpStopsWhenDeviceGone(t *testing.T) {
	m := newMicStream(48000, 1)
	dev := &fakePCM{params: pcmParams{rate: 48000, channels: 1, periodFrames: 480, periods: 4},
		errs: []error{fmt.Errorf("x: %w", errPCMGone)}}
	if err := m.pump(context.Background(), dev); !errors.Is(err, errPCMGone) {
		t.Errorf("pump returned %v, want the device-gone error", err)
	}
	wrong := &fakePCM{params: pcmParams{rate: 44100, channels: 1, periodFrames: 441, periods: 4}}
	if err := m.pump(context.Background(), wrong); err == nil {
		t.Error("pump accepted a device at the wrong rate")
	}
}

func TestHeadsetPumpBroadcasts(t *testing.T) {
	ab := NewAudioBroadcaster()
	ch, unsub := ab.Subscribe()
	defer unsub()
	frames := pcmBytes([]int16{16384, -16384, 0, 32767})
	dev := &fakePCM{params: pcmParams{rate: 48000, channels: 2, periodFrames: 2, periods: 8},
		toRead: [][]byte{frames, frames}, errs: []error{nil, errPCMXrun}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- (&headsetStream{ab: ab}).pump(ctx, dev) }()

	var chunk []byte
	select {
	case chunk = <-ch:
	case <-time.After(time.Second):
		t.Fatal("nothing broadcast")
	}
	if p := ab.Params(); !ab.IsActive() || p.SampleRate != 48000 || p.Channels != 2 {
		t.Errorf("broadcaster params %+v active %v", p, ab.IsActive())
	}
	want := []float32{0.5, -0.5, 0, 32767.0 / 32768}
	for i, w := range want {
		if got := math.Float32frombits(binary.LittleEndian.Uint32(chunk[4*i:])); got != w {
			t.Errorf("sample %d = %v, want %v", i, got, w)
		}
	}
	cancel()
	<-done
	if ab.IsActive() {
		t.Error("broadcaster still active after the stream stopped")
	}
	if dev.recovered != 1 {
		t.Errorf("recovered %d times from one overrun", dev.recovered)
	}
}

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
