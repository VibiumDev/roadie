package main

import "image"

// A capture dongle with nothing on its HDMI input (the phone locked, its
// screen off, the cable out) sends a picture of its own: the UGREEN 15389
// sends dark grey with "无信号 No Signal" in yellow at the centre. Shown,
// it replaces the phone's outline with a stranger's screen; detected as
// content, its small, wide block of text reads as the phone turned on its
// side. So Roadie recognises it, sends a dark frame in its place, and
// counts it as no signal.

// darkFrame reports whether a frame has nothing bright enough to be content
// anywhere on a sparse grid (cropThreshold, as detection uses). Dark frames
// say nothing about where the picture is or which way it is turned. The
// dongle's own black is not 0 but about 16, so isBlackFrame alone misses
// them.
func darkFrame(img image.Image) bool {
	b := img.Bounds()
	stepX, stepY := max(b.Dx()/64, 1), max(b.Dy()/36, 1)
	for y := b.Min.Y + stepY/2; y < b.Max.Y; y += stepY {
		for x := b.Min.X + stepX/2; x < b.Max.X; x += stepX {
			if brightAt(img, x, y) {
				return false
			}
		}
	}
	return true
}

func brightAt(img image.Image, x, y int) bool {
	r, g, bl, _ := img.At(x, y).RGBA()
	return r>>8 >= cropThreshold || g>>8 >= cropThreshold || bl>>8 >= cropThreshold
}

// dongleNoSignal reports whether a frame is the dongle's "No Signal"
// screen: dark everywhere outside a box at the centre a quarter of the
// frame across, with yellow text inside it, itself centred. A phone's
// picture always reaches well outside that box, so the cheap check of the
// outside, first, turns nearly every frame away.
func dongleNoSignal(img image.Image) bool {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 64 || h < 36 {
		return false
	}
	cx, cy := b.Min.X+w/2, b.Min.Y+h/2
	inner := image.Rect(cx-w/8, cy-h/8, cx+w/8, cy+h/8)
	stepX, stepY := max(w/64, 1), max(h/36, 1)
	for y := b.Min.Y + stepY/2; y < b.Max.Y; y += stepY {
		for x := b.Min.X + stepX/2; x < b.Max.X; x += stepX {
			if !image.Pt(x, y).In(inner) && brightAt(img, x, y) {
				return false
			}
		}
	}
	// Inside: enough yellow (red and green well above blue), centred.
	var n int
	box := image.Rectangle{}
	for y := inner.Min.Y; y < inner.Max.Y; y += 2 {
		for x := inner.Min.X; x < inner.Max.X; x += 2 {
			r, g, bl, _ := img.At(x, y).RGBA()
			r, g, bl = r>>8, g>>8, bl>>8
			if r >= 100 && g >= 100 && bl+50 <= min(r, g) {
				n++
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	if n < 40 {
		return false
	}
	mid := box.Min.Add(box.Max).Div(2)
	return absInt(mid.X-cx)*25 <= w && absInt(mid.Y-cy)*25 <= h // within 4% of the centre
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// blankFrame is a dark frame the size of b, reused while the size holds.
type blankFrame struct{ img *image.Gray }

func (bf *blankFrame) of(b image.Rectangle) image.Image {
	if bf.img == nil || bf.img.Bounds() != b {
		bf.img = image.NewGray(b)
	}
	return bf.img
}
