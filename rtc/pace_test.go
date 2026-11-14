package rtc

import (
	"image"
	"sync"
	"testing"
	"time"

	"github.com/VibiumDev/roadie/encoder"
)

const phonePixels = 444 * 972

// frames counts how many of n frames, arriving every interval, the pacer
// takes at limit.
func framesTaken(p *pacer, n int, interval time.Duration, limit int) int {
	taken := 0
	for i := 0; i < n; i++ {
		if p.take(time.Duration(i+1)*interval, limit) {
			taken++
		}
	}
	return taken
}

func TestPacerSendsTheLimitEvenly(t *testing.T) {
	thirtieth := time.Second / 30
	for _, c := range []struct {
		limit, want int
	}{{0, 300}, {30, 300}, {20, 200}, {15, 150}, {10, 100}} {
		if got := framesTaken(&pacer{}, 300, thirtieth, c.limit); got < c.want-1 || got > c.want+1 {
			t.Errorf("limit %d over 10 s of 30 fps: %d frames, want %d", c.limit, got, c.want)
		}
	}
	// Frames that come slower than the limit all go out.
	if got := framesTaken(&pacer{}, 100, time.Second/10, 20); got != 100 {
		t.Errorf("10 fps under a limit of 20: %d of 100 frames", got)
	}
	// Stamps a millisecond or two off do not change the count.
	p, taken := &pacer{}, 0
	for i := 0; i < 300; i++ {
		ts := time.Duration(i+1)*thirtieth + time.Duration(i%3-1)*2*time.Millisecond
		if p.take(ts, 20) {
			taken++
		}
	}
	if taken < 195 || taken > 205 {
		t.Errorf("limit 20 with uneven stamps: %d frames, want about 200", taken)
	}
}

// A blank frame is stamped on another clock than the capture's, days
// apart. The frame at a jump goes out and the count starts again.
func TestPacerSurvivesAStampThatJumps(t *testing.T) {
	p := &pacer{}
	framesTaken(p, 30, time.Second/30, 15)
	base := time.Second
	if !p.take(base-500*time.Hour, 15) {
		t.Error("the frame after a jump back was held")
	}
	if !p.take(base+time.Second/30, 15) {
		t.Error("the frame after a jump forward was held")
	}
	if p.take(base+2*time.Second/30, 15) {
		t.Error("the frame right after one that went out was taken at 15 fps")
	}
}

func TestRateMeter(t *testing.T) {
	var m rateMeter
	if got := m.fps(); got != encoderFPS {
		t.Errorf("before any frame: %v", got)
	}
	ts := time.Duration(0)
	for i := 0; i < 90; i++ {
		ts += 100 * time.Millisecond
		m.add(ts)
	}
	if got := m.fps(); got < 9.5 || got > 10.5 {
		t.Errorf("stamps 100 ms apart: %.1f fps", got)
	}
	// A stall is not a slower capture.
	ts += 20 * time.Second
	m.add(ts)
	if got := m.fps(); got < 9.5 || got > 10.5 {
		t.Errorf("after a 20 s gap: %.1f fps", got)
	}
}

func TestEncoderBitrate(t *testing.T) {
	for _, c := range []struct {
		bitrate int
		fps     float64
		halve   bool
		want    int
	}{
		{600_000, 30, false, 600_000},
		{600_000, 20, false, 900_000},
		{600_000, 15, false, 1_200_000},
		{600_000, 10, false, 1_800_000},
		{600_000, 60, false, 300_000},
		{300_000, 15, true, 300_000},
		{4_000_000, 1, false, maxEncoderBitrate},
		{600_000, 0, false, 600_000},
	} {
		if got := encoderBitrate(c.bitrate, c.fps, c.halve); got != c.want {
			t.Errorf("%d bps at %v fps, halve %v: %d, want %d", c.bitrate, c.fps, c.halve, got, c.want)
		}
	}
}

// feed gives the governor n readings half a second apart.
func feed(g *governor, now *time.Time, n int, r reading) (changed bool) {
	for i := 0; i < n; i++ {
		*now = now.Add(500 * time.Millisecond)
		r.now = *now
		changed = g.update(r) || changed
	}
	return changed
}

func TestGovernorFollowsTheLink(t *testing.T) {
	g := &governor{auto: true}
	now := time.Unix(1000, 0)
	thin := reading{bitrate: 330_000, sent: 320_000, floor: 300_000, pixels: phonePixels}
	wide := reading{bitrate: 600_000, sent: 500_000, floor: 300_000, pixels: phonePixels}
	between := reading{bitrate: 380_000, sent: 370_000, floor: 300_000, pixels: phonePixels}

	feed(g, &now, thinTicks-1, thin)
	if limit, _, _ := g.plan(); limit != 0 {
		t.Fatalf("held back after %d thin readings: limit %d", thinTicks-1, limit)
	}
	if !feed(g, &now, 1, thin) {
		t.Error("the change to fewer frames was not reported")
	}
	if limit, halve, why := g.plan(); limit != reducedFPS || halve || why != "link" {
		t.Fatalf("a thin link: limit %d, halve %v, why %q", limit, halve, why)
	}
	// Just over the line is not enough to go back: no flapping.
	feed(g, &now, 20, between)
	if limit, _, _ := g.plan(); limit != reducedFPS {
		t.Errorf("went back to every frame at %d bps", between.bitrate)
	}
	feed(g, &now, upTicks-1, wide)
	if limit, _, _ := g.plan(); limit != reducedFPS {
		t.Errorf("went back after %d wide readings", upTicks-1)
	}
	feed(g, &now, 1, wide)
	if limit, _, why := g.plan(); limit != 0 || why != "" {
		t.Errorf("a wide link: limit %d, why %q", limit, why)
	}

	// The same bits are plenty for a small picture and thin for a big one.
	small, big := &governor{auto: true}, &governor{auto: true}
	feed(small, &now, 20, reading{bitrate: 350_000, sent: 340_000, floor: 300_000, pixels: 320 * 240})
	feed(big, &now, 20, reading{bitrate: 1_500_000, sent: 1_400_000, floor: 300_000, pixels: 1920 * 1080})
	if limit, _, _ := small.plan(); limit != 0 {
		t.Errorf("320x240 at 350 kbps: limit %d", limit)
	}
	if limit, _, _ := big.plan(); limit != reducedFPS {
		t.Errorf("1920x1080 at 1.5 Mbps: limit %d", limit)
	}

	// With auto off a thin link holds nothing back.
	fixed := &governor{}
	feed(fixed, &now, 20, thin)
	if limit, _, _ := fixed.plan(); limit != 0 {
		t.Errorf("auto off on a thin link: limit %d", limit)
	}

	// A still screen sends almost nothing. A low estimate then is not a
	// thin link, and nothing is held back until the stream needs the bits.
	still := &governor{auto: true}
	idle := reading{bitrate: 330_000, sent: 40_000, floor: 300_000, pixels: phonePixels}
	feed(still, &now, 40, idle)
	if limit, _, _ := still.plan(); limit != 0 {
		t.Errorf("a low estimate with 40 kbps sent: limit %d", limit)
	}
	// Nor is a dip of a few seconds, as after a hiccup on a good link.
	feed(still, &now, thinTicks-2, thin)
	feed(still, &now, 4, wide)
	feed(still, &now, thinTicks-2, thin)
	if limit, _, _ := still.plan(); limit != 0 {
		t.Errorf("two dips of %d readings: limit %d", thinTicks-2, limit)
	}
	feed(still, &now, thinTicks, thin)
	if limit, _, _ := still.plan(); limit != reducedFPS {
		t.Errorf("the same estimate with the stream pressing: limit %d", limit)
	}
}

func TestGovernorAtTheFloor(t *testing.T) {
	for _, auto := range []bool{true, false} {
		g := &governor{auto: auto}
		now := time.Unix(1000, 0)
		// At the floor with little sent, the link is not what limits it.
		feed(g, &now, 20, reading{bitrate: 300_000, sent: 30_000, floor: 300_000, pixels: phonePixels})
		if _, halve, _ := g.plan(); halve {
			t.Errorf("auto %v at the floor with a still screen: halved", auto)
		}
		feed(g, &now, floorTicks-1, reading{bitrate: 300_000, sent: 290_000, floor: 300_000, pixels: phonePixels})
		if _, halve, _ := g.plan(); halve {
			t.Errorf("auto %v: halved after %d readings at the floor", auto, floorTicks-1)
		}
		feed(g, &now, 1, reading{bitrate: 300_000, sent: 290_000, floor: 300_000, pixels: phonePixels})
		if limit, halve, why := g.plan(); limit != reliefFPS || !halve || why != "floor" {
			t.Errorf("auto %v at the floor: limit %d, halve %v, why %q", auto, limit, halve, why)
		}
		// Half the bits go out now, and it stays so while the estimate does.
		feed(g, &now, 20, reading{bitrate: 300_000, sent: 140_000, floor: 300_000, pixels: phonePixels})
		if _, halve, _ := g.plan(); !halve {
			t.Errorf("auto %v: left the floor while the estimate sat there", auto)
		}
		feed(g, &now, 20, reading{bitrate: 320_000, sent: 150_000, floor: 300_000, pixels: phonePixels})
		if _, halve, _ := g.plan(); !halve {
			t.Errorf("auto %v: left the floor at 320 kbps", auto)
		}
		// A viewer's cap just over the floor must be enough to leave it.
		feed(g, &now, upTicks, reading{bitrate: 340_000, floor: 300_000, pixels: phonePixels})
		if _, halve, _ := g.plan(); halve {
			t.Errorf("auto %v: still at the floor at 340 kbps", auto)
		}
	}
}

func TestGovernorSparesAStressedHost(t *testing.T) {
	g := &governor{auto: true}
	now := time.Unix(1000, 0)
	fine := reading{bitrate: 2_000_000, floor: 300_000, pixels: phonePixels, tempC: 65, hasTemp: true}
	hot := fine
	hot.tempC = 81
	warm := fine
	warm.tempC = 78

	feed(g, &now, 1, hot)
	if limit, _, _ := g.plan(); limit != 0 {
		t.Fatalf("one hot reading: limit %d", limit)
	}
	feed(g, &now, 1, hot)
	if limit, _, why := g.plan(); limit != 20 || why != "host" {
		t.Fatalf("a hot host: limit %d, why %q", limit, why)
	}
	// Heat is slow to answer: one more step for each minute it stays hot.
	feed(g, &now, 118, hot)
	if limit, _, _ := g.plan(); limit != 20 {
		t.Errorf("59 s on: limit %d, want 20 still", limit)
	}
	feed(g, &now, 2, hot)
	if limit, _, _ := g.plan(); limit != 15 {
		t.Errorf("a minute on: limit %d, want 15", limit)
	}
	feed(g, &now, 130, hot)
	if limit, _, _ := g.plan(); limit != 10 {
		t.Errorf("a host that stays hot: limit %d, want 10", limit)
	}
	// Warm is neither: nothing changes.
	feed(g, &now, 100, warm)
	if limit, _, _ := g.plan(); limit != 10 {
		t.Errorf("a warm host: limit %d, want 10 still", limit)
	}
	// Cool for half a minute gives one step back, and so on.
	feed(g, &now, 59, fine)
	if limit, _, _ := g.plan(); limit != 10 {
		t.Errorf("cool for 29.5 s: limit %d", limit)
	}
	feed(g, &now, 2, fine)
	if limit, _, _ := g.plan(); limit != 15 {
		t.Errorf("cool for 30 s: limit %d, want 15", limit)
	}
	feed(g, &now, 130, fine)
	if limit, _, why := g.plan(); limit != 0 || why != "" {
		t.Errorf("cool for long: limit %d, why %q", limit, why)
	}

	// An encoder that falls behind counts as stress; one bad reading does not.
	busy := reading{bitrate: 2_000_000, floor: 300_000, pixels: phonePixels, dropped: busyDrops}
	calm := reading{bitrate: 2_000_000, floor: 300_000, pixels: phonePixels}
	g = &governor{auto: true}
	feed(g, &now, 1, busy)
	feed(g, &now, 1, calm)
	feed(g, &now, 1, busy)
	if limit, _, _ := g.plan(); limit != 0 {
		t.Errorf("two separate bad readings: limit %d", limit)
	}
	feed(g, &now, 2, busy)
	if limit, _, why := g.plan(); limit != 20 || why != "host" {
		t.Errorf("an encoder behind, with no temperature to read: limit %d, why %q", limit, why)
	}
	// Fewer frames show at once whether they help, so these steps are quick.
	feed(g, &now, 10, busy)
	if limit, _, _ := g.plan(); limit != 15 {
		t.Errorf("an encoder still behind 5 s on: limit %d, want 15", limit)
	}

	// With auto off heat holds nothing back.
	fixed := &governor{}
	feed(fixed, &now, 40, hot)
	if limit, _, _ := fixed.plan(); limit != 0 {
		t.Errorf("auto off on a hot host: limit %d", limit)
	}
}

// countingEncoder records what the broadcaster asks of it.
type countingEncoder struct {
	mu       sync.Mutex
	frames   int
	bitrates []int
	dropped  int
	samples  chan encoder.Sample
}

func (e *countingEncoder) Encode(encoder.Frame) error {
	e.mu.Lock()
	e.frames++
	e.mu.Unlock()
	return nil
}
func (e *countingEncoder) Samples() <-chan encoder.Sample { return e.samples }
func (e *countingEncoder) SetBitrate(bps int) error {
	e.mu.Lock()
	e.bitrates = append(e.bitrates, bps)
	e.mu.Unlock()
	return nil
}
func (e *countingEncoder) ForceKeyframe() error { return nil }
func (e *countingEncoder) Dropped() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dropped
}
func (e *countingEncoder) Close() error { close(e.samples); return nil }
func (e *countingEncoder) last() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.bitrates) == 0 {
		return 0
	}
	return e.bitrates[len(e.bitrates)-1]
}
func (e *countingEncoder) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.frames
}

type stillSource struct{}

func (stillSource) AddSink(FrameSink)    {}
func (stillSource) RemoveSink(FrameSink) {}

// paced makes a broadcaster that takes frames from the test alone, with
// the host's temperature at *temp.
func paced(t *testing.T, temp *float64) (*Broadcaster, *countingEncoder, *encoder.Config) {
	t.Helper()
	enc := &countingEncoder{samples: make(chan encoder.Sample)}
	opened := &encoder.Config{}
	b, err := New(stillSource{}, Options{
		Logf: t.Logf,
		OpenEncoder: func(cfg encoder.Config) (encoder.Encoder, string, error) {
			*opened = cfg
			return enc, "fake", nil
		},
		HostTemp: func() (float64, bool) { return *temp, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	b.encMu.Lock()
	b.sinking = true
	b.encMu.Unlock()
	return b, enc, opened
}

// play hands the broadcaster seconds of frames at fps, with a reading
// every half second, and returns how many reached the encoder.
func play(b *Broadcaster, enc *countingEncoder, ts *time.Duration, now *time.Time, fps, seconds int) int {
	img := image.NewYCbCr(image.Rect(0, 0, 444, 972), image.YCbCrSubsampleRatio420)
	before := enc.count()
	for i := 0; i < fps*seconds; i++ {
		*ts += time.Second / time.Duration(fps)
		b.Frame(img, img.Bounds(), *ts)
		if (i+1)%(fps/2) == 0 {
			*now = now.Add(500 * time.Millisecond)
			// The stream uses what the link carries.
			b.encMu.Lock()
			link := b.bitrate
			b.encMu.Unlock()
			b.statMu.Lock()
			b.kbps = link / 1000
			b.statMu.Unlock()
			b.govern(*now)
		}
	}
	return enc.count() - before
}

func near(got, want int) bool { return got*100 >= want*93 && got*100 <= want*107 }

func TestBroadcasterGivesASlowCaptureItsBits(t *testing.T) {
	temp := 60.0
	b, enc, opened := paced(t, &temp)
	ts, now := time.Duration(0), time.Unix(1000, 0)
	b.setBitrate(900_000)

	if got := play(b, enc, &ts, &now, 30, 3); got != 90 {
		t.Errorf("30 fps on a good link: %d of 90 frames reached the encoder", got)
	}
	if opened.FPS != encoderFPS {
		t.Errorf("the encoder was opened for %d fps", opened.FPS)
	}
	if got := enc.last(); got != 0 && !near(got, 900_000) {
		t.Errorf("30 fps at 900 kbps: the encoder was asked for %d", got)
	}
	// The capture drops to 10 fps: three times the bits a frame.
	if got := play(b, enc, &ts, &now, 10, 6); got != 60 {
		t.Errorf("10 fps: %d of 60 frames reached the encoder", got)
	}
	if got := enc.last(); !near(got, 2_700_000) {
		t.Errorf("10 fps at 900 kbps: the encoder was asked for %d, want about 2.7 Mbps", got)
	}
	if st := b.Status(); st.Bitrate != 900_000 || st.FPSLimit != 0 {
		t.Errorf("status %+v: the link's bitrate is 900 kbps and nothing is held back", st)
	}
}

func TestBroadcasterSendsFewerFramesOnAThinLink(t *testing.T) {
	temp := 60.0
	b, enc, _ := paced(t, &temp)
	ts, now := time.Duration(0), time.Unix(1000, 0)
	b.setBitrate(330_000)

	play(b, enc, &ts, &now, 30, 7)
	if got := play(b, enc, &ts, &now, 30, 3); got < 59 || got > 61 {
		t.Errorf("a thin link: %d frames in 3 s, want 60", got)
	}
	if got := enc.last(); !near(got, 495_000) {
		t.Errorf("20 fps at 330 kbps: the encoder was asked for %d, want about 495 kbps", got)
	}
	if st := b.Status(); st.FPSLimit != reducedFPS || st.LimitedBy != "link" {
		t.Errorf("status %+v", st)
	}

	// Told to send every frame, it does, and the encoder's bitrate follows.
	b.SetAuto(false)
	if got := play(b, enc, &ts, &now, 30, 3); got != 90 {
		t.Errorf("auto off: %d of 90 frames", got)
	}
	if got := enc.last(); !near(got, 330_000) {
		t.Errorf("auto off at 330 kbps: the encoder was asked for %d", got)
	}
	b.SetAuto(true)

	// The link recovers.
	b.setBitrate(1_500_000)
	play(b, enc, &ts, &now, 30, 4)
	if got := play(b, enc, &ts, &now, 30, 2); got != 60 {
		t.Errorf("a recovered link: %d of 60 frames", got)
	}
	if got := enc.last(); !near(got, 1_500_000) {
		t.Errorf("30 fps at 1.5 Mbps: the encoder was asked for %d", got)
	}
}

func TestBroadcasterAtTheFloorSendsHalf(t *testing.T) {
	temp := 60.0
	b, enc, _ := paced(t, &temp)
	ts, now := time.Duration(0), time.Unix(1000, 0)
	b.setBitrate(100_000) // clamped to the floor

	play(b, enc, &ts, &now, 30, 5)
	if got := play(b, enc, &ts, &now, 30, 2); got < 29 || got > 31 {
		t.Errorf("at the floor: %d frames in 2 s, want 30", got)
	}
	if got := enc.last(); got != 0 && !near(got, defaultMinBitrate) {
		t.Errorf("at the floor: the encoder was asked for %d, want %d for half the frames", got, defaultMinBitrate)
	}
	if st := b.Status(); st.FPSLimit != reliefFPS || st.LimitedBy != "floor" {
		t.Errorf("status %+v", st)
	}
}

func TestBroadcasterSparesAHotHost(t *testing.T) {
	temp := 82.0
	b, enc, _ := paced(t, &temp)
	ts, now := time.Duration(0), time.Unix(1000, 0)
	b.setBitrate(2_000_000)

	play(b, enc, &ts, &now, 30, 2)
	if got := play(b, enc, &ts, &now, 30, 2); got < 39 || got > 41 {
		t.Errorf("a hot host: %d frames in 2 s, want 40", got)
	}
	if st := b.Status(); st.FPSLimit != 20 || st.LimitedBy != "host" {
		t.Errorf("status %+v", st)
	}
	if got := enc.last(); !near(got, 3_000_000) {
		t.Errorf("20 fps at 2 Mbps: the encoder was asked for %d, want about 3 Mbps", got)
	}
}
