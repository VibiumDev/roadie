package web

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/VibiumDev/roadie/internal/capture"
	"github.com/VibiumDev/roadie/internal/hid"
	"github.com/VibiumDev/roadie/internal/platform"
	"image"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Turning an iPhone's screen from outside it.
//
// No key an iPhone takes from a USB keyboard says which way its screen
// should face, but AssistiveTouch's menu does: Device → Rotate Screen →
// Portrait, Left, Right or Upside Down turns it as if the phone had been
// turned in the hand. Mouse button 5, once the phone is set to open the menu
// with it (AssistiveTouch → Devices → Roadie → Customize Additional
// Buttons), opens the menu around the pointer: Roadie presses it with the
// pointer in the middle of the screen. Roadie's landscape is Left.
//
// Roadie looks before every click: it finds the menu near the middle of the
// screen and clicks an item, where it is, only once the page holding it shows
// (see atmenu.go). The menu doesn't always open exactly at the pointer,
// button 5 doesn't dependably toggle it (pressed with the menu open it has
// closed it, and moved it), the menu closes itself when left alone, and a
// click meant for the menu that finds none lands on the app.
//
// Full Keyboard Access's Rotate Device (Tab R) can't stand in for this: it
// names no direction, so Roadie couldn't know where it left the phone, and a
// Tab chord from Roadie's keyboard often arrives as a plain Tab.

// menuButton is mouse button 5, as a button mask.
const menuButton = 16

// menuSpot is where an item of the menu is from the menu's middle, in
// fractions of the screen's short side, measured on an iPhone 430 points
// wide. On a phone where the menu doesn't line up, Roadie doesn't see the
// page it expects, and stops.
type menuSpot struct{ x, y float64 }

var (
	menuDevice   = menuSpot{0.193, -0.074}
	menuRotate   = menuSpot{0.117, -0.224}
	menuPortrait = menuSpot{0, -0.213}
	menuLeft     = menuSpot{-0.206, 0}
)

// menuPark is where the pointer waits while Roadie looks at the menu, in
// a corner: it would cover part of it anywhere near the middle.
const menuPark = hid.HIDCoordMax / 50

var (
	// rotateClock paces the looks: each waits for the screen to settle
	// after the click before it.
	rotateClock = capture.RealSettleClock
	// rotateShows is how long an app gets to turn with the phone and
	// Roadie's reading of the picture to see it: the picture changes shape
	// half a second after the click, and is seen to in a frame or two (see
	// shapeWatch). A screen that can't turn waits it out.
	rotateShows = 2 * time.Second
	rotateSleep = time.Sleep
)

var (
	errRotateBusy = errors.New("already turning the screen")
	errNoPicture  = errors.New("no picture from the phone to look at")
	errNoMenu     = errors.New("AssistiveTouch's menu didn't open in the middle of the screen, so nothing was " +
		"clicked: set mouse button 5 to Open Menu (AssistiveTouch → Devices → Roadie → Customize Additional Buttons)")
	errMenuLost = errors.New("AssistiveTouch's menu didn't show the page expected, so Roadie stopped clicking")
	errMenuOpen = errors.New("turned the screen, but AssistiveTouch's menu didn't close")
)

// phoneTurn is which way the phone takes itself to be held.
type phoneTurn struct {
	busy sync.Mutex
	// rotating is set while a turn is under way, for /health: viewers hold
	// their input back meanwhile, since the pointer moving under Roadie's
	// clicks would spoil them.
	rotating atomic.Bool
	// landscape is set once Roadie has turned the phone on its side, and
	// cleared once it has turned it upright.
	landscape atomic.Bool
}

// orientation is which way the picture faces, as last detected.
func (s *Server) orientation() string {
	if s.Buf == nil {
		return ""
	}
	return s.Buf.Orientation()
}

// pointerTurned reports whether the pointer has to be turned to land where
// it is aimed. An iPhone reads an absolute pointer the way it believes it is
// held. An app that turned with it faces that way too, but one that can't
// turn (the home screen, Settings) stays upright, and there every tap lands
// a quarter turn away until the phone is turned upright again.
func (s *Server) PointerTurned() bool {
	return s.turn.landscape.Load() && s.orientation() == capture.OrientationPortrait
}

// handleRotate turns the target's screen: POST {"to": "portrait"} or
// {"to": "landscape"}.
func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		(body.To != capture.OrientationPortrait && body.To != capture.OrientationLandscape) {
		http.Error(w, `"to" must be "portrait" or "landscape"`, http.StatusBadRequest)
		return
	}
	if s.Platform != platform.PlatformIOS {
		http.Error(w, "turning the screen works on an iPhone (--platform ios), through AssistiveTouch's menu",
			http.StatusNotImplemented)
		return
	}
	if s.HID == nil {
		http.Error(w, "HID not available", http.StatusServiceUnavailable)
		return
	}
	switch err := s.rotate(body.To); {
	case errors.Is(err, errRotateBusy):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case errors.Is(err, errNoPicture):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case errors.Is(err, errNoMenu), errors.Is(err, errMenuLost):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "orientation": s.orientation()})
}

// rotate turns the phone through AssistiveTouch's menu, and waits for the
// picture to show it. An app that can't turn stays upright.
func (s *Server) rotate(to string) error {
	if !s.turn.busy.TryLock() {
		return errRotateBusy
	}
	defer s.turn.busy.Unlock()
	s.turn.rotating.Store(true)
	defer s.turn.rotating.Store(false)
	if capture.DecodeGray(s.Source.Latest()) == nil {
		return errNoPicture
	}
	land := to == capture.OrientationLandscape
	way := menuPortrait
	if land {
		way = menuLeft
	}

	presses, flipped, waits := 0, false, 0
	// The page last clicked on, and the one it leads to: anyMenu after
	// button 5.
	from, expect := "", ""
	for range 20 {
		var err error
		seen := s.lookAtMenu(showing(expect))
		log.Printf("rotate to %s: menu %s", to, seen)
		switch {
		case expect == "" || seen.page == expect || expect == anyMenu && seen.page != "":
		case seen.page == from && waits < 3:
			// Not changed yet: the picture lags the menu. Look again.
			waits++
			continue
		case seen.page == from:
			// The click didn't take; it is clicked again below.
		default:
			// The click missed the item, and the menu closed or moved on:
			// the phone reads the pointer another way than Roadie took it
			// to, as after a restart while it took itself to be on its
			// side. On an upright screen, try the other way once.
			if flipped || s.orientation() != capture.OrientationPortrait {
				if flipped {
					s.turn.landscape.Store(!s.turn.landscape.Load())
				}
				return s.giveUp(errMenuLost)
			}
			flipped = true
			s.turn.landscape.Store(!s.turn.landscape.Load())
			log.Printf("rotate to %s: the click missed; taking the phone to be held %s", to,
				map[bool]string{true: "on its side", false: "upright"}[s.turn.landscape.Load()])
		}
		waits, from, expect = 0, seen.page, ""
		switch seen.page {
		case pageTop:
			err, expect = s.clickMenu(seen, menuDevice), pageDevice
		case pageDevice:
			err, expect = s.clickMenu(seen, menuRotate), pageRotate
		case pageRotate:
			if err := s.clickMenu(seen, way); err != nil {
				return err
			}
			return s.turned(to)
		default:
			// No menu here: closed, on another page, or near an edge.
			// Button 5 opens it here, or closes it to be opened next time.
			if presses == 3 {
				return s.giveUp(errNoMenu)
			}
			presses++
			err, expect = s.pressMenuButton(), anyMenu
		}
		if err != nil {
			return err
		}
	}
	return s.giveUp(errMenuLost)
}

// anyMenu is what button 5 leads to: any page of the menu.
const anyMenu = "any"

// showing accepts the page a click leads to; nil when none is expected.
func showing(page string) func(menuSeen) bool {
	switch page {
	case "":
		return nil
	case anyMenu:
		return func(m menuSeen) bool { return m.page != "" }
	}
	return func(m menuSeen) bool { return m.page == page }
}

// giveUp closes the menu, if it is open in the middle, and returns err.
func (s *Server) giveUp(err error) error {
	s.closeMenu(nil)
	return err
}

// closeMenu closes the menu open in the middle of the screen, and reports
// whether none is left there. Button 5 closes it, or takes another page
// back to the main one; after each press the picture gets a few looks to
// show it. The first look takes what open accepts (see lookAtMenu).
func (s *Server) closeMenu(open func(menuSeen) bool) bool {
	seen := s.lookAtMenu(open)
	for presses := 0; ; presses++ {
		log.Printf("rotate: closing the menu: %s", seen)
		if seen.page == "" {
			return true
		}
		if presses == 3 || s.pressMenuButton() != nil {
			return false
		}
		was := seen.page
		gone := func(m menuSeen) bool { return m.page != was }
		for range 3 {
			if seen = s.lookAtMenu(gone); seen.page != was {
				break
			}
		}
	}
}

// turned follows a turn chosen in the menu: it waits for the picture, notes
// how the phone takes itself to be held, and closes the menu.
func (s *Server) turned(to string) error {
	land := to == capture.OrientationLandscape
	// The pointer is read the new way from now on, and an app that turns
	// takes a moment to: until the picture shows it, Roadie takes the phone
	// to be held the way the picture faces.
	if !land {
		s.turn.landscape.Store(false)
	}
	s.waitOrientation(to, rotateShows)
	s.turn.landscape.Store(land)
	// The menu stays on its page, and turns with the screen.
	if !s.closeMenu(showing(pageRotate)) {
		return errMenuOpen
	}
	return nil
}

// pressMenuButton presses button 5 with the pointer in the middle of the
// screen, where it opens the menu.
func (s *Server) pressMenuButton() error {
	if err := s.HID.MouseMove(hid.HIDCoordMax/2, hid.HIDCoordMax/2); err != nil {
		return err
	}
	return s.HID.MouseClick(menuButton)
}

// clickMenu clicks an item of the menu where it was seen.
func (s *Server) clickMenu(seen menuSeen, at menuSpot) error {
	x, y := menuAt(seen, at)
	if err := s.HID.MouseMove(x, y); err != nil {
		return err
	}
	return s.HID.MouseClick(1)
}

// menuAt is where an item of the menu is, in the pointer's coordinates. The
// picture is taken to be the screen, as cropped.
func menuAt(seen menuSeen, at menuSpot) (int, int) {
	f := seen.frame
	short := float64(min(f.Dx(), f.Dy()))
	x := float64(seen.mid.X-f.Min.X) + at.x*short
	y := float64(seen.mid.Y-f.Min.Y) + at.y*short
	return int(x / float64(f.Dx()) * hid.HIDCoordMax), int(y / float64(f.Dy()) * hid.HIDCoordMax)
}

// lookAtMenu finds the page of the menu open near the middle of the screen
// once it has caught up with the last click: once the screen has settled,
// or sooner, once what want accepts shows the same in two frames in a row.
// A page shows a third of a second before the menu stops animating it. The
// pointer waits in a corner meanwhile.
func (s *Server) lookAtMenu(want func(menuSeen) bool) menuSeen {
	if s.HID.MouseMove(menuPark, menuPark) != nil {
		return menuSeen{}
	}
	var last menuSeen
	early := false
	var enough func(*image.Gray) bool
	if want != nil {
		enough = func(g *image.Gray) bool {
			seen := findMenu(g)
			early = g != nil && seen == last && want(seen)
			last = seen
			return early
		}
	}
	frame := s.settled(enough)
	if early {
		return last
	}
	return findMenu(capture.DecodeGray(frame))
}

// settled waits for the screen to catch up with the last input, and returns
// it: see settledFrameOr.
func (s *Server) settled(enough func(*image.Gray) bool) []byte {
	last := time.Now()
	if h, ok := s.HID.(interface{ LastInput() time.Time }); ok {
		last = h.LastInput()
	}
	return capture.SettledFrameOr(context.Background(), s.Source, last, rotateClock, enough)
}

// waitOrientation waits up to d for the picture to face that way.
func (s *Server) waitOrientation(to string, d time.Duration) bool {
	for end := time.Now().Add(d); ; rotateSleep(50 * time.Millisecond) {
		if s.orientation() == to {
			return true
		}
		if time.Now().After(end) {
			return false
		}
	}
}
