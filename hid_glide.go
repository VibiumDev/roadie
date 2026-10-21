package main

import (
	"math"
	"time"
)

// A pointer that jumps from one spot to the next is nothing a person
// does, and it makes a recording hard to follow. With glide on (--glide,
// or /api/settings), a jump from rest glides instead: along a slight arc,
// slow at both ends and quickest in the middle, as a hand moves a mouse.
// It takes 100 ms, plus up to 200 ms more the further it goes. It is off
// by default: clients expect a move to be instant, and on a computer a
// glide can open hover menus on its way.
//
// Only a jump glides. Short moves, moves with a button held (a drag's
// path is the client's), moves that come less than glideRest apart (the
// viewer, or a client's own animation), and taps -- a finger lands, it
// doesn't travel -- go straight there.
const (
	glideMin   = hidCoordMax / 32       // about 3% of the screen
	glideRest  = 100 * time.Millisecond // a move this long after the last starts from rest
	glideFrame = 16 * time.Millisecond  // iOS takes pointer input about this often
	glideBase  = 100 * time.Millisecond // the shortest glide
	glideSpan  = 200 * time.Millisecond // added for one the width of the screen
	glideBow   = 0.12                   // how far the arc bows out, as a share of the distance
)

// glidePath returns the points a pointer passes through going from
// (x0, y0) to (x1, y1), one per glideFrame, ending at (x1, y1).
func glidePath(x0, y0, x1, y1 int) [][2]int {
	dx, dy := float64(x1-x0), float64(y1-y0)
	dist := math.Hypot(dx, dy)
	d := glideBase + time.Duration(math.Min(dist/hidCoordMax, 1)*float64(glideSpan))
	steps := max(int(d/glideFrame), 2)

	// A quadratic Bézier whose middle point sits off the straight line,
	// to one side of the direction of travel.
	mx := float64(x0) + dx/2 - dy*glideBow
	my := float64(y0) + dy/2 + dx*glideBow

	path := make([][2]int, steps)
	for i := range steps {
		t := float64(i+1) / float64(steps)
		// Minimum jerk: the speed profile of a person reaching for
		// something.
		s := t * t * t * (10 - 15*t + 6*t*t)
		a, b, c := (1-s)*(1-s), 2*(1-s)*s, s*s
		path[i] = [2]int{
			clampInt(int(math.Round(a*float64(x0)+b*mx+c*float64(x1))), 0, hidCoordMax),
			clampInt(int(math.Round(a*float64(y0)+b*my+c*float64(y1))), 0, hidCoordMax),
		}
	}
	path[steps-1] = [2]int{x1, y1}
	return path
}
