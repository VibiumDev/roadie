package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VibiumDev/roadie/internal/platform"
	"github.com/coder/websocket"
)

// menuPhone is an iPhone with AssistiveTouch's menu: button 5 opens it on
// the main page centered on the pointer, or closes it, and its items lead
// from page to page. The picture shows the page that is open. Any click with
// no menu showing lands on the app, which the tests count.
type menuPhone struct {
	keyHID
	t        *testing.T
	s        *Server
	mu       sync.Mutex
	src      *menuScreen
	page     string // "" when closed, or "elsewhere": open, near an edge
	mid      image.Point
	shift    image.Point // where the menu opens from the pointer
	at       [2]int
	opens    bool // button 5 opens the menu
	clicks   []string
	onApp    int // clicks that landed on the app
	turnedTo string
	appTurns bool // the app on screen turns with the phone
	// leftways: the phone takes itself to be on its Left side while its
	// screen is upright, and reads the pointer that way: (u, v) lands at
	// (1-v, u).
	leftways bool
	// drop says whether the phone ignores a click on that item.
	drop func(click string) bool
	// backToTop: button 5 on another page goes back to the main page
	// instead of closing the menu, as an iPhone has done.
	backToTop bool
}

func (h *menuPhone) MouseMove(x, y int) error {
	h.mu.Lock()
	if h.leftways {
		x, y = hidCoordMax-y, x
	}
	h.at = [2]int{x, y}
	h.mu.Unlock()
	return nil
}

func (h *menuPhone) MouseClick(b int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	screen := h.src.contentRect
	close := func(x, y int) bool {
		return math.Abs(float64(x-h.at[0])) < 500 && math.Abs(float64(y-h.at[1])) < 500
	}
	near := func(at menuSpot) bool {
		return close(menuAt(menuSeen{mid: h.mid, frame: screen}, at))
	}
	if b == menuButton {
		h.clicks = append(h.clicks, "5")
		switch {
		case h.backToTop && (h.page == pageDevice || h.page == pageRotate):
			// Back to the main page, around the pointer.
			h.page = pageTop
			h.mid = screen.Min.Add(image.Pt(screen.Dx()/2, screen.Dy()/2)).Add(h.shift)
		case h.page != "":
			h.page = ""
		case h.opens && close(hidCoordMax/2, hidCoordMax/2):
			h.page = pageTop
			h.mid = screen.Min.Add(image.Pt(screen.Dx()/2, screen.Dy()/2)).Add(h.shift)
		case h.opens:
			h.page = "elsewhere"
		}
		h.draw()
		return nil
	}
	dropped := func(click string) bool {
		if h.drop != nil && h.drop(click) {
			h.clicks = append(h.clicks, click)
			return true
		}
		return false
	}
	switch {
	case h.page == pageTop && near(menuDevice) && dropped("Device"):
	case h.page == pageTop && near(menuDevice):
		h.clicks, h.page = append(h.clicks, "Device"), pageDevice
	case h.page == pageDevice && near(menuRotate):
		h.clicks, h.page = append(h.clicks, "Rotate Screen"), pageRotate
	case h.page == pageRotate && near(menuLeft):
		h.clicks = append(h.clicks, "Left")
		h.turn(OrientationLandscape)
	case h.page == pageRotate && near(menuPortrait):
		h.clicks = append(h.clicks, "Portrait")
		h.turn(OrientationPortrait)
	case h.page == "":
		h.clicks = append(h.clicks, "app")
		h.onApp++
	default:
		h.clicks = append(h.clicks, "outside")
		h.page = "" // a click outside the menu closes it
	}
	h.draw()
	return nil
}

func (h *menuPhone) turn(o string) {
	h.turnedTo = o
	if !h.appTurns {
		h.leftways = o == OrientationLandscape
		return
	}
	face(h.s, o)
	h.src.contentRect = screenFacing(o)
	r := h.src.contentRect // the menu stays open, in the middle
	h.mid = r.Min.Add(image.Pt(r.Dx()/2, r.Dy()/2)).Add(h.shift)
}

func screenFacing(o string) image.Rectangle {
	if o == OrientationLandscape {
		return image.Rect(0, 0, 400, 184)
	}
	return image.Rect(0, 0, 184, 400)
}

// draw puts the open page in the picture.
func (h *menuPhone) draw() {
	h.src.show(menuFrame(h.t, h.src.contentRect, h.page, h.mid, 40))
}

// menuFrame is a picture of a screen r with a page of the menu open around
// mid, as its grid of white on dark, and its top left corner at that level.
func menuFrame(t *testing.T, r image.Rectangle, page string, mid image.Point, corner uint8) []byte {
	img := image.NewGray(r)
	for i := range img.Pix {
		img.Pix[i] = 40
	}
	for y := range 30 {
		for x := range 30 {
			img.Pix[img.PixOffset(r.Min.X+x, r.Min.Y+y)] = corner
		}
	}
	var grid []bool
	for _, ref := range menuRefs {
		if ref.page == page {
			grid = ref.grid
		}
	}
	if grid != nil {
		side := int(math.Round(menuSide * float64(min(r.Dx(), r.Dy()))))
		x0, y0 := mid.X-side/2, mid.Y-side/2
		for y := range side {
			for x := range side {
				if grid[y*menuGrid/side*menuGrid+x*menuGrid/side] {
					img.Pix[img.PixOffset(x0+x, y0+y)] = grayWhite
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// menuScreen is the phone's picture as Roadie gets it. With lag, a change
// shows only after that many more looks.
type menuScreen struct {
	frame, old  []byte
	lag, behind int
	contentRect image.Rectangle
}

func (m *menuScreen) show(frame []byte) {
	if m.frame != nil && m.lag > 0 {
		m.old, m.behind = m.frame, m.lag
	}
	m.frame = frame
}

func (m *menuScreen) Latest() []byte {
	if m.behind > 0 {
		m.behind--
		return m.old
	}
	return m.frame
}
func (m *menuScreen) LatestRaw() []byte            { return m.frame }
func (m *menuScreen) Status() CaptureStatus        { return StatusConnected }
func (m *menuScreen) CropRect() image.Rectangle    { return m.contentRect }
func (m *menuScreen) ContentRect() image.Rectangle { return m.contentRect }

const grayWhite = 250

// newMenuPhone is an iPhone's server whose picture faces o, with the waits
// shrunk.
func newMenuPhone(t *testing.T, o string) (*Server, http.Handler, *menuPhone) {
	t.Helper()
	clock, shows, sleep := rotateClock, rotateShows, rotateSleep
	rotateClock = settleClock{now: func() time.Time { return time.Now().Add(time.Hour) }, sleep: realSettleClock.sleep}
	rotateShows, rotateSleep = 30*time.Millisecond, func(time.Duration) {}
	t.Cleanup(func() { rotateClock, rotateShows, rotateSleep = clock, shows, sleep })

	s, _ := newTestServer()
	s.Platform = platform.PlatformIOS
	src := &menuScreen{contentRect: screenFacing(o)}
	h := &menuPhone{t: t, s: s, src: src, opens: true, appTurns: true}
	h.mid = src.contentRect.Min.Add(image.Pt(src.contentRect.Dx()/2, src.contentRect.Dy()/2))
	h.draw()
	s.Source, s.HID = src, h
	face(s, o)
	return s, NewMux(s), h
}

// face makes the picture face o.
func face(s *Server, o string) {
	s.Buf.mu.Lock()
	s.Buf.orientation = o
	s.Buf.mu.Unlock()
}

func postRotate(t *testing.T, mux http.Handler, body string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/rotate", strings.NewReader(body)))
	return w.Code, w.Body.String()
}

// The pages Roadie reads are the ones it was shown, where they were shown.
func TestMenuPagesRead(t *testing.T) {
	_, _, h := newMenuPhone(t, OrientationPortrait)
	middle := h.mid
	for _, shift := range []image.Point{{}, {-12, 0}, {6, -9}} {
		h.mid = middle.Add(shift)
		for _, page := range []string{"", pageTop, pageDevice, pageRotate} {
			h.page = page
			h.draw()
			seen := findMenu(decodeGray(h.src.frame))
			if seen.page != page {
				t.Errorf("showing %q at %v, read %q", page, h.mid, seen.page)
			}
			if d := seen.mid.Sub(h.mid); page != "" && (d.X*d.X+d.Y*d.Y > 9) {
				t.Errorf("%q shown at %v, found at %v", page, h.mid, seen.mid)
			}
		}
	}
	white := image.NewGray(image.Rect(0, 0, 184, 400))
	for i := range white.Pix {
		white.Pix[i] = 255
	}
	if seen := findMenu(white); seen.page != "" {
		t.Errorf("an all-white screen read as %q", seen.page)
	}
}

// On its side an iPhone opened the menu off the middle: Roadie finds it
// there and clicks its items where they are.
func TestRotateWithTheMenuOffTheMiddle(t *testing.T) {
	_, mux, h := newMenuPhone(t, OrientationLandscape)
	h.shift = image.Pt(-15, 0)
	if code, body := postRotate(t, mux, `{"to":"portrait"}`); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if want := []string{"5", "Device", "Rotate Screen", "Portrait", "5"}; !reflect.DeepEqual(h.clicks, want) {
		t.Errorf("clicks = %v, want %v", h.clicks, want)
	}
}

// Turning to landscape opens the menu with button 5, clicks Device, Rotate
// Screen and Left, each once its page shows, and closes the menu.
func TestRotateToLandscape(t *testing.T) {
	s, mux, h := newMenuPhone(t, OrientationPortrait)
	code, body := postRotate(t, mux, `{"to":"landscape"}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var resp struct{ Orientation string }
	json.Unmarshal([]byte(body), &resp)
	if resp.Orientation != OrientationLandscape {
		t.Errorf("answer = %s", body)
	}
	if want := []string{"5", "Device", "Rotate Screen", "Left", "5"}; !reflect.DeepEqual(h.clicks, want) {
		t.Errorf("clicks = %v, want %v", h.clicks, want)
	}
	if h.page != "" {
		t.Errorf("menu left open on %q", h.page)
	}
	if !s.turn.landscape.Load() || s.pointerTurned() {
		t.Errorf("landscape %v, pointer turned %v", s.turn.landscape.Load(), s.pointerTurned())
	}
}

// Back upright from a wide screen: the menu's items are found on it too.
func TestRotateToPortrait(t *testing.T) {
	s, mux, h := newMenuPhone(t, OrientationLandscape)
	s.turn.landscape.Store(true)
	if code, body := postRotate(t, mux, `{"to":"portrait"}`); code != 200 || !strings.Contains(body, `"portrait"`) {
		t.Fatalf("status %d: %s", code, body)
	}
	if want := []string{"5", "Device", "Rotate Screen", "Portrait", "5"}; !reflect.DeepEqual(h.clicks, want) {
		t.Errorf("clicks = %v, want %v", h.clicks, want)
	}
	if s.turn.landscape.Load() {
		t.Error("still taken to be on its side")
	}
}

// Whatever state the menu is in to begin with, Roadie gets it to the main
// page in the middle before clicking an item, and never clicks the app.
func TestRotateFromAnyMenu(t *testing.T) {
	for _, tc := range []struct {
		page string
		want []string
	}{
		{pageTop, []string{"Device", "Rotate Screen", "Left", "5"}},
		{pageDevice, []string{"Rotate Screen", "Left", "5"}},
		{pageRotate, []string{"Left", "5"}},
		{"elsewhere", []string{"5", "5", "Device", "Rotate Screen", "Left", "5"}},
	} {
		t.Run(tc.page, func(t *testing.T) {
			_, mux, h := newMenuPhone(t, OrientationPortrait)
			h.page = tc.page
			h.draw()
			if code, body := postRotate(t, mux, `{"to":"landscape"}`); code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			if !reflect.DeepEqual(h.clicks, tc.want) || h.onApp != 0 {
				t.Errorf("clicks = %v, want %v", h.clicks, tc.want)
			}
		})
	}
}

// When button 5 doesn't open the menu, nothing else is clicked: it would
// land on the app.
func TestRotateWithoutTheMenu(t *testing.T) {
	s, mux, h := newMenuPhone(t, OrientationPortrait)
	h.opens = false
	code, body := postRotate(t, mux, `{"to":"landscape"}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "button 5") {
		t.Errorf("status %d: %s", code, body)
	}
	if want := []string{"5", "5", "5"}; !reflect.DeepEqual(h.clicks, want) {
		t.Errorf("clicks = %v, want only button 5", h.clicks)
	}
	if s.turn.landscape.Load() {
		t.Error("taken to be on its side")
	}
}

// The home screen doesn't turn: the phone is on its side all the same, and
// the pointer is turned to match.
func TestRotateAnAppThatDoesNotTurn(t *testing.T) {
	s, mux, h := newMenuPhone(t, OrientationPortrait)
	h.appTurns = false
	if code, body := postRotate(t, mux, `{"to":"landscape"}`); code != 200 || !strings.Contains(body, `"portrait"`) {
		t.Fatalf("status %d: %s", code, body)
	}
	if h.turnedTo != OrientationLandscape || !s.pointerTurned() {
		t.Errorf("turned to %q, pointer turned %v", h.turnedTo, s.pointerTurned())
	}
}

// After a restart Roadie takes the phone to be upright, while it took itself
// to be on its side on a screen that doesn't turn. The first click into the
// menu misses and closes it; Roadie then takes the phone to be on its side,
// and the rest land.
func TestRotateWhenRoadieForgotThePhoneIsOnItsSide(t *testing.T) {
	s, mux, h := newMenuPhone(t, OrientationPortrait)
	h.appTurns, h.leftways = false, true
	settled := newSettledHID(h)
	settled.sleep = func(time.Duration) {}
	settled.turned = s.pointerTurned
	s.HID = settled

	if code, body := postRotate(t, mux, `{"to":"portrait"}`); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	want := []string{"5", "outside", "5", "Device", "Rotate Screen", "Portrait", "5"}
	if !reflect.DeepEqual(h.clicks, want) || h.onApp != 0 {
		t.Errorf("clicks = %v, want %v", h.clicks, want)
	}
	if h.leftways || s.turn.landscape.Load() {
		t.Errorf("phone on its side %v, taken to be %v", h.leftways, s.turn.landscape.Load())
	}
}

// The picture shows a click a look late: Roadie waits for it rather than
// taking the click to have missed.
func TestRotateWhileThePictureLags(t *testing.T) {
	s, mux, h := newMenuPhone(t, OrientationPortrait)
	h.src.lag = 1
	if code, body := postRotate(t, mux, `{"to":"landscape"}`); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if want := []string{"5", "Device", "Rotate Screen", "Left", "5"}; !reflect.DeepEqual(h.clicks, want) {
		t.Errorf("clicks = %v, want %v", h.clicks, want)
	}
	if !s.turn.landscape.Load() {
		t.Error("not taken to be on its side")
	}
}

// A click that doesn't take is made again.
func TestRotateClicksAgain(t *testing.T) {
	_, mux, h := newMenuPhone(t, OrientationPortrait)
	dropped := false
	h.drop = func(click string) bool {
		if click == "Device" && !dropped {
			dropped = true
			return true
		}
		return false
	}
	if code, body := postRotate(t, mux, `{"to":"landscape"}`); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if want := []string{"5", "Device", "Device", "Rotate Screen", "Left", "5"}; !reflect.DeepEqual(h.clicks, want) {
		t.Errorf("clicks = %v, want %v", h.clicks, want)
	}
}

// Button 5 may take the menu back to its main page rather than close it:
// Roadie presses again until it's gone.
func TestRotateClosesTheMenuFromAnyPage(t *testing.T) {
	_, mux, h := newMenuPhone(t, OrientationPortrait)
	h.backToTop = true
	if code, body := postRotate(t, mux, `{"to":"landscape"}`); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if want := []string{"5", "Device", "Rotate Screen", "Left", "5", "5"}; !reflect.DeepEqual(h.clicks, want) || h.page != "" {
		t.Errorf("clicks = %v, want %v; menu %q", h.clicks, want, h.page)
	}
}

// When Roadie stops, it leaves no menu open.
func TestRotateGivesUpWithTheMenuClosed(t *testing.T) {
	_, mux, h := newMenuPhone(t, OrientationLandscape)
	h.leftways = true // the picture is wide, so this can't be put right
	if code, body := postRotate(t, mux, `{"to":"portrait"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", code, body)
	}
	if h.page != "" || h.onApp != 0 {
		t.Errorf("menu %q left open, %d clicks on the app; clicks %v", h.page, h.onApp, h.clicks)
	}
}

// While a turn is under way /health says so, for viewers to hold back.
func TestHealthSaysRotating(t *testing.T) {
	s, mux, h := newMenuPhone(t, OrientationPortrait)
	var during []string
	h.drop = func(string) bool {
		_, _, body := get(t, mux, "/health")
		during = append(during, body)
		return false
	}
	if code, body := postRotate(t, mux, `{"to":"landscape"}`); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var health struct{ Rotating bool }
	if len(during) == 0 || json.Unmarshal([]byte(during[0]), &health) != nil || !health.Rotating {
		t.Errorf("/health during a turn: %v", during)
	}
	if _, _, body := get(t, mux, "/health"); strings.Contains(body, "rotating") || s.turn.rotating.Load() {
		t.Errorf("/health after: %s", body)
	}
}

// While a turn is under way, a viewer's input is held back but for
// releases, which go through so that nothing stays held.
func TestViewerInputHeldBackWhileRotating(t *testing.T) {
	s, mux, _ := newMenuPhone(t, OrientationPortrait)
	in := &inputLog{}
	s.HID = in
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/hid/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	say := func(msg string) { conn.Write(ctx, websocket.MessageText, []byte(msg)) }

	s.turn.rotating.Store(true)
	say(`{"cmd":"mouse_move","x":100,"y":200}`)
	say(`{"cmd":"key_press","keycode":4}`)
	say(`{"cmd":"touch","contacts":[{"id":0,"tip":true,"x":5,"y":5}]}`)
	say(`{"cmd":"key_release","keycode":4}`)
	say(`{"cmd":"mouse_release","buttons":7}`)
	say(`{"cmd":"touch","contacts":[]}`)
	say(`{"cmd":"done"}`)
	in.waitFor(t, 3)
	s.turn.rotating.Store(false)
	say(`{"cmd":"mouse_move","x":300,"y":400}`)
	in.waitFor(t, 4)

	want := []string{"-4", "release 7", "touch 0", "move 300,400"}
	in.mu.Lock()
	defer in.mu.Unlock()
	if !reflect.DeepEqual(in.log, want) {
		t.Errorf("got %v, want %v", in.log, want)
	}
}

// inputLog records the input that reaches the phone.
type inputLog struct {
	keyHID
	mu sync.Mutex
}

func (l *inputLog) add(s string) error {
	l.mu.Lock()
	l.log = append(l.log, s)
	l.mu.Unlock()
	return nil
}
func (l *inputLog) MouseMove(x, y int) error { return l.add(fmt.Sprintf("move %d,%d", x, y)) }
func (l *inputLog) MouseRelease(b int) error { return l.add(fmt.Sprintf("release %d", b)) }
func (l *inputLog) Touch(c []TouchContact) error {
	return l.add(fmt.Sprintf("touch %d", len(c)))
}
func (l *inputLog) KeyPress(c int) error   { return l.add(fmt.Sprintf("+%d", c)) }
func (l *inputLog) KeyRelease(c int) error { return l.add(fmt.Sprintf("-%d", c)) }

func (l *inputLog) waitFor(t *testing.T, n int) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		l.mu.Lock()
		got := len(l.log)
		l.mu.Unlock()
		if got >= n {
			return
		}
	}
	t.Fatalf("only %v arrived", l.log)
}

// One turn at a time.
func TestRotateBusy(t *testing.T) {
	s, mux, _ := newMenuPhone(t, OrientationPortrait)
	s.turn.busy.Lock()
	defer s.turn.busy.Unlock()
	if code, body := postRotate(t, mux, `{"to":"landscape"}`); code != http.StatusConflict {
		t.Errorf("status %d: %s", code, body)
	}
}

func TestRotateRequests(t *testing.T) {
	s, mux, _ := newMenuPhone(t, OrientationPortrait)
	if code, _ := postRotate(t, mux, `{"to":"sideways"}`); code != http.StatusBadRequest {
		t.Errorf("bad way: status %d", code)
	}
	if code, _, _ := get(t, mux, "/api/rotate"); code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d", code)
	}
	s.Platform = platform.PlatformAndroid
	if code, _ := postRotate(t, mux, `{"to":"landscape"}`); code != http.StatusNotImplemented {
		t.Errorf("Android: status %d", code)
	}
}

// While the phone takes itself to be on its side and its screen is upright,
// the pointer is turned so that it lands where it is aimed: aiming at
// (a, b), a quarter turn sends (b, 1-a).
func TestPointerTurnsWhileThePhoneThinksItIsOnItsSide(t *testing.T) {
	inner := &posHID{}
	h := newSettledHID(inner)
	h.sleep = func(time.Duration) {}
	turned := false
	h.turned = func() bool { return turned }

	h.MouseMove(1000, 2000)
	turned = true
	h.MouseMove(1000, 2000)
	h.touchAsPointer = true
	h.Touch([]TouchContact{{X: 30000, Y: 100, Tip: true}})

	want := [][2]int{{1000, 2000}, {2000, hidCoordMax - 1000}, {100, hidCoordMax - 30000}}
	if !reflect.DeepEqual(inner.at, want) {
		t.Errorf("moves = %v, want %v", inner.at, want)
	}
}

// posHID records where the pointer was sent.
type posHID struct {
	keyHID
	at [][2]int
}

func (p *posHID) MouseMove(x, y int) error { p.at = append(p.at, [2]int{x, y}); return nil }

// inputAt is a HID whose last input was at a time.
type inputAt struct {
	keyHID
	at time.Time
}

func (h *inputAt) LastInput() time.Time { return h.at }

// A look that knows which page it waits for takes it once two frames in a
// row show it, while the menu is still animating; one that doesn't waits
// for the screen to settle.
func TestMenuLookTakesThePageItWaitsFor(t *testing.T) {
	start := time.Unix(1000, 0)
	r := screenFacing(OrientationPortrait)
	mid := r.Min.Add(image.Pt(r.Dx()/2, r.Dy()/2))
	frames := []timedFrame{{0, menuFrame(t, r, "", mid, 40)}}
	for i := range 90 { // the page from 350 ms, beside something that never stops changing
		at := 350*time.Millisecond + time.Duration(i)*33*time.Millisecond
		frames = append(frames, timedFrame{at, menuFrame(t, r, pageDevice, mid, uint8(40+i%2*160))})
	}
	for _, tc := range []struct {
		name string
		want func(menuSeen) bool
		took time.Duration
	}{
		{"waiting for the page", showing(pageDevice), 383 * time.Millisecond},
		{"waiting for nothing", nil, settleLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := start
			saved := rotateClock
			rotateClock = fakeSettleClock(&clock)
			t.Cleanup(func() { rotateClock = saved })
			s, _ := newTestServer()
			s.Source = &timedSource{clock: &clock, start: start, frames: frames}
			s.HID = &inputAt{at: start}
			if seen := s.lookAtMenu(tc.want); seen.page != pageDevice {
				t.Errorf("saw %s, want the device page", seen)
			}
			if took := clock.Sub(start); took < tc.took || took > tc.took+settlePoll {
				t.Errorf("took %v, want %v", took, tc.took)
			}
		})
	}
}
