package rtc

import "time"

// How many frames a second go to viewers, and what the encoder is told to
// spend on them. The measurements behind the numbers are in
// docs/explanation/frame-rate.md.
//
// Every encoder is opened for encoderFPS frames a second and spends its
// bitrate by the frame, not by the second: given half the frames it sends
// half the bits, and no frame comes out sharper. So when frames reach it
// at another rate, for whatever reason, its bitrate is scaled to match
// and the link's bits are all used (encoderBitrate).
const encoderFPS = 30

const (
	// fullRateBits is what a moving picture needs to stay sharp at every
	// captured frame, in bits per pixel per second. Under it the same
	// bits make a picture at reducedFPS that is as sharp or sharper for
	// every kind of motion measured.
	fullRateBits = 0.81
	reducedFPS   = 20

	// reliefFPS is sent, with half the bits, while the link's estimate
	// sits at the lowest bitrate there is: the link may carry less.
	reliefFPS = 15

	// The host is hot from hotC and cool again under coolC. A Raspberry
	// Pi 4 starts slowing its CPU at 80 °C.
	hotC, coolC = 80.0, 77.0

	// maxEncoderBitrate is the most an encoder is asked for.
	maxEncoderBitrate = 25_000_000
)

// stressFPS is the most frames a second sent at each level of host
// stress; level 0 sends them all.
var stressFPS = []int{0, 20, 15, 10}

const (
	// Readings in a row, half a second apart, before the plan changes.
	// Sending less waits longer than the few seconds an estimate takes to
	// climb back after a hiccup on a good link.
	thinTicks  = 12 // a thin link: 6 s
	floorTicks = 8  // the link at its lowest: 4 s
	upTicks    = 6  // and before sending more: 3 s
	// An encoder that is behind shows at once whether fewer frames help.
	// A hot host takes far longer to cool, so heat alone steps slowly.
	behindEvery = 5 * time.Second
	hotEvery    = time.Minute
	calmFor     = 30 * time.Second
	busyDrops   = 3 // frames the encoder skipped in one reading
)

// reading is what the governor is told every half second.
type reading struct {
	now     time.Time
	bitrate int     // what the link carries, bits a second
	sent    int     // what the encoder made in the last second, bits
	floor   int     // the lowest bitrate there is
	pixels  int     // of the picture being encoded; 0 with no encoder
	tempC   float64 // the host's temperature, where hasTemp
	hasTemp bool
	dropped int // frames the encoder skipped since the last reading
}

// governor decides how many of the captured frames are sent. With auto
// off it holds back only at the floor.
//
// A low estimate of the link counts only while the stream presses against
// it. A still screen sends almost nothing, and the estimate then falls to
// what is sent at the first hiccup, which says nothing about the link.
type governor struct {
	auto bool

	reduced bool // the link is too thin for every frame
	relief  bool // the link's estimate is at the floor
	stress  int  // index into stressFPS

	thin, wide        int // readings in a row under and over fullRateBits
	atFloor, offFloor int
	busy              int       // readings in a row with the host hot or the encoder behind
	stressAt          time.Time // when stress last changed
	calmSince         time.Time
}

// update takes a reading and reports whether the plan changed.
func (g *governor) update(r reading) bool {
	limit, halve, _ := g.plan()

	allowed := r.bitrate
	if halve {
		allowed /= 2
	}
	pressing := r.sent*5 >= allowed*3

	if r.pixels > 0 && r.bitrate > 0 {
		bits := float64(r.bitrate) / float64(r.pixels)
		switch {
		case bits < fullRateBits && pressing:
			g.thin, g.wide = g.thin+1, 0
		case bits >= fullRateBits*1.15:
			g.thin, g.wide = 0, g.wide+1
		default:
			g.thin, g.wide = 0, 0
		}
		if !g.reduced && g.thin >= thinTicks {
			g.reduced = true
		}
		if g.reduced && g.wide >= upTicks {
			g.reduced = false
		}
	}
	if r.bitrate > 0 && r.floor > 0 {
		switch {
		case r.bitrate*50 <= r.floor*51 && (pressing || g.relief):
			g.atFloor, g.offFloor = g.atFloor+1, 0
		case r.bitrate*10 >= r.floor*11:
			g.atFloor, g.offFloor = 0, g.offFloor+1
		default:
			g.atFloor, g.offFloor = 0, 0
		}
		if !g.relief && g.atFloor >= floorTicks {
			g.relief = true
		}
		if g.relief && g.offFloor >= upTicks {
			g.relief = false
		}
	}

	hot := r.hasTemp && r.tempC >= hotC
	cool := !r.hasTemp || r.tempC < coolC
	behind := r.dropped >= busyDrops
	switch {
	case hot || behind:
		g.busy++
		g.calmSince = time.Time{}
		every := hotEvery
		if behind {
			every = behindEvery
		}
		if g.busy >= 2 && g.stress < len(stressFPS)-1 && (g.stress == 0 || r.now.Sub(g.stressAt) >= every) {
			g.stress++
			g.stressAt = r.now
		}
	case cool && r.dropped == 0:
		g.busy = 0
		if g.calmSince.IsZero() {
			g.calmSince = r.now
		}
		if g.stress > 0 && r.now.Sub(g.calmSince) >= calmFor && r.now.Sub(g.stressAt) >= calmFor {
			g.stress--
			g.stressAt = r.now
		}
	default:
		g.busy = 0
		g.calmSince = time.Time{}
	}

	nowLimit, nowHalve, _ := g.plan()
	return nowLimit != limit || nowHalve != halve
}

// plan is what to send now: at most limit frames a second, 0 for every
// captured frame, at half the link's bitrate when halve. why names what
// holds the rate down: link, host or floor.
func (g *governor) plan() (limit int, halve bool, why string) {
	if g.auto {
		if g.reduced {
			limit, why = reducedFPS, "link"
		}
		if s := stressFPS[g.stress]; s > 0 && (limit == 0 || s < limit) {
			limit, why = s, "host"
		}
	}
	if g.relief {
		if limit == 0 || reliefFPS < limit {
			limit = reliefFPS
		}
		halve, why = true, "floor"
	}
	return limit, halve, why
}

// forgetLink drops what was learned about a link that is gone.
func (g *governor) forgetLink() {
	g.reduced, g.relief = false, false
	g.thin, g.wide, g.atFloor, g.offFloor = 0, 0, 0, 0
}

// encoderBitrate is what the encoder is asked for so that frames arriving
// at fps a second add up to bitrate.
func encoderBitrate(bitrate int, fps float64, halve bool) int {
	if fps <= 0 {
		fps = encoderFPS
	}
	bps := float64(bitrate) * encoderFPS / fps
	if halve {
		bps /= 2
	}
	return int(min(bps, maxEncoderBitrate))
}

// pacer picks which frames go out when fewer are wanted than arrive.
type pacer struct {
	credit float64
	last   time.Duration
	begun  bool
}

// take says whether the frame stamped ts is one of limit a second; a
// limit of 0 takes every frame. A stamp that jumps starts the count again.
func (p *pacer) take(ts time.Duration, limit int) bool {
	dt := ts - p.last
	p.last = ts
	if limit <= 0 || !p.begun || dt <= 0 || dt > time.Second {
		p.begun, p.credit = true, 0
		return true
	}
	p.credit = min(p.credit+dt.Seconds()*float64(limit), 1.5)
	if p.credit < 0.95 {
		return false
	}
	p.credit = max(p.credit-1, 0)
	return true
}

// rateMeter follows how fast frames arrive from their stamps.
type rateMeter struct {
	last     time.Duration
	interval time.Duration
}

func (m *rateMeter) add(ts time.Duration) {
	if dt := ts - m.last; m.last != 0 && dt > 0 && dt < 500*time.Millisecond {
		if m.interval == 0 {
			m.interval = dt
		} else {
			m.interval += (dt - m.interval) / 16
		}
	}
	m.last = ts
}

// fps is the arrival rate, or encoderFPS before two frames have come.
func (m *rateMeter) fps() float64 {
	if m.interval <= 0 {
		return encoderFPS
	}
	return float64(time.Second) / float64(m.interval)
}
