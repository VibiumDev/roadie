package web

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/png"
	"math"
	"path"
	"strings"
)

// Reading AssistiveTouch's menu off the screen, so that Roadie clicks an
// item only once the page holding it is showing where Roadie opened it.
//
// The menu is white icons and labels on a dark panel, whatever is behind it.
// What is white in the square the menu fills, in a coarse grid, says which
// page is showing; atmenu/ holds that grid for each page Roadie uses, taken
// from an iPhone in each orientation. A page counts as showing when its grid
// overlaps what is on screen by at least half (pages Roadie opened scored
// 0.65 to 0.96; other pages, screens without the menu, all-white screens and
// a menu opened near an edge, 0.3 at most).

//go:embed atmenu/*.png
var atmenuFiles embed.FS

const (
	menuSide  = 0.684 // the menu's width, in short sides of the screen
	menuGrid  = 32    // cells across the grid
	menuWhite = 170   // a pixel this bright is part of an icon or label
	menuMatch = 0.5   // overlap that counts as showing
	menuClear = 0.2   // by how much the best page must beat the next
	menuReach = 0.12  // how far from the middle it is looked for, in short sides
)

// Pages of the menu, named as in atmenu/.
const (
	pageTop    = "top"
	pageDevice = "device"
	pageRotate = "rotate"
)

type menuRef struct {
	page string
	grid []bool
}

var menuRefs = loadMenuRefs()

func loadMenuRefs() []menuRef {
	entries, err := atmenuFiles.ReadDir("atmenu")
	if err != nil {
		panic(err)
	}
	var refs []menuRef
	for _, e := range entries {
		data, err := atmenuFiles.ReadFile(path.Join("atmenu", e.Name()))
		if err != nil {
			panic(err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			panic(e.Name() + ": " + err.Error())
		}
		grid := make([]bool, menuGrid*menuGrid)
		for y := range menuGrid {
			for x := range menuGrid {
				r, _, _, _ := img.At(x, y).RGBA()
				grid[y*menuGrid+x] = r > 0x8000
			}
		}
		page, _, _ := strings.Cut(e.Name(), "-")
		refs = append(refs, menuRef{page, grid})
	}
	return refs
}

// menuSeen is the page of the menu found near the middle of the screen, ""
// for none, and where the menu's middle is in the frame.
type menuSeen struct {
	page  string
	mid   image.Point
	frame image.Rectangle
}

func (m menuSeen) String() string {
	if m.page == "" {
		return "not seen"
	}
	f := m.frame
	return fmt.Sprintf("%s page, %+d,%+d px from the middle", m.page,
		m.mid.X-f.Min.X-f.Dx()/2, m.mid.Y-f.Min.Y-f.Dy()/2)
}

// findMenu looks for a page of the menu near the middle of the screen. The
// menu doesn't always open exactly where the pointer is (on its side, an
// iPhone opened it a twentieth of the screen to one side), so every place
// within menuReach of the middle is tried, a half cell apart.
func findMenu(g *image.Gray) menuSeen {
	if g == nil {
		return menuSeen{}
	}
	b := g.Bounds()
	short := min(b.Dx(), b.Dy())
	side := int(math.Round(menuSide * float64(short)))
	step := side / menuGrid / 2
	if step < 1 {
		return menuSeen{frame: b}
	}
	n := int(menuReach*float64(short)) / step
	mid := b.Min.Add(image.Pt(b.Dx()/2, b.Dy()/2))
	reach := n*step + side/2
	area := image.Rect(mid.X-reach, mid.Y-reach, mid.X+reach+1, mid.Y+reach+1).Intersect(b)

	// White pixels in the area, summed so that any rectangle's count is
	// four lookups.
	w, h := area.Dx(), area.Dy()
	sum := make([]int32, (w+1)*(h+1))
	for y := range h {
		var row int32
		for x := range w {
			if g.Pix[g.PixOffset(area.Min.X+x, area.Min.Y+y)] > menuWhite {
				row++
			}
			sum[(y+1)*(w+1)+x+1] = sum[y*(w+1)+x+1] + row
		}
	}
	white := func(x0, y0, x1, y1 int) bool {
		return sum[y1*(w+1)+x1]-sum[y0*(w+1)+x1]-sum[y1*(w+1)+x0]+sum[y0*(w+1)+x0] > 0
	}

	best := map[string]float64{}
	at := map[string]image.Point{}
	grid := make([]bool, menuGrid*menuGrid)
	for j := -n; j <= n; j++ {
		for i := -n; i <= n; i++ {
			c := mid.Add(image.Pt(i*step, j*step))
			x0, y0 := c.X-side/2-area.Min.X, c.Y-side/2-area.Min.Y
			if x0 < 0 || y0 < 0 || x0+side > w || y0+side > h {
				continue
			}
			for gy := range menuGrid {
				ya, yb := y0+gy*side/menuGrid, y0+(gy+1)*side/menuGrid
				for gx := range menuGrid {
					grid[gy*menuGrid+gx] = white(x0+gx*side/menuGrid, ya, x0+(gx+1)*side/menuGrid, yb)
				}
			}
			for _, r := range menuRefs {
				if s := overlap(grid, r.grid); s > best[r.page] {
					best[r.page], at[r.page] = s, c
				}
			}
		}
	}
	page, first, second := "", 0.0, 0.0
	for p, s := range best {
		switch {
		case s > first:
			page, first, second = p, s, first
		case s > second:
			second = s
		}
	}
	if first < menuMatch || first-second < menuClear {
		return menuSeen{frame: b}
	}
	return menuSeen{page: page, mid: at[page], frame: b}
}

// overlap is how much two grids agree: cells white in both, over cells
// white in either.
func overlap(a, b []bool) float64 {
	both, either := 0, 0
	for i := range a {
		if a[i] && b[i] {
			both++
		}
		if a[i] || b[i] {
			either++
		}
	}
	if either == 0 {
		return 0
	}
	return float64(both) / float64(either)
}
