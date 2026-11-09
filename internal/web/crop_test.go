package web

import (
	"bytes"
	"encoding/json"
	"github.com/VibiumDev/roadie/internal/capture"
	"github.com/VibiumDev/roadie/internal/hid"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var (
	tallDark  = image.Rect(750, 76, 1168, 1007) // the same, in a dark app
	nearBlack = image.Rect(900, 500, 980, 580)  // a dark screen's lone bright patch
)

func TestOrientationOf(t *testing.T) {
	for r, want := range map[image.Rectangle]string{
		tall: capture.OrientationPortrait, tallDark: capture.OrientationPortrait, wide: capture.OrientationLandscape,
		image.Rect(0, 0, 100, 110): "", {}: "",
	} {
		if got := capture.OrientationOf(r); got != want {
			t.Errorf("orientationOf(%v) = %q, want %q", r, got, want)
		}
	}
}

// The orientation follows the picture, but one odd detection doesn't turn
// it, and one that can't tell keeps it.
func TestOrientationNeedsTwoDetections(t *testing.T) {
	fb := &capture.FrameBuffer{}
	step := func(r image.Rectangle, want string) {
		t.Helper()
		fb.SetContentRect(r)
		if got := fb.Orientation(); got != want {
			t.Fatalf("after %v: orientation %q, want %q", r, got, want)
		}
	}
	step(wide, "") // not even the first is taken alone
	step(tall, "")
	step(tall, capture.OrientationPortrait) // two in a row say
	step(wide, capture.OrientationPortrait) // one wide one isn't enough
	step(tallDark, capture.OrientationPortrait)
	step(nearBlack, capture.OrientationPortrait) // can't tell: keeps it
	step(wide, capture.OrientationPortrait)
	step(wide, capture.OrientationLandscape) // two in a row turn it
}

// The rough picture taken of every frame says which way the target faces,
// and a change of shape has the edges detected in the frames after it.
func TestPictureTurnSeenAtOnce(t *testing.T) {
	frame := func(r image.Rectangle) image.Image {
		img := image.NewRGBA(image.Rectangle{Max: hd})
		draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{16, 16, 16, 255}), image.Point{}, draw.Src)
		draw.Draw(img, r, image.NewUniform(color.RGBA{200, 200, 200, 255}), image.Point{}, draw.Src)
		return img
	}
	for r, want := range map[image.Rectangle]string{
		tall: capture.OrientationPortrait, tallDark: capture.OrientationPortrait, wide: capture.OrientationLandscape, nearBlack: "",
	} {
		if got := capture.ShapeOf(capture.BrightBox(frame(r)), hd); got != want {
			t.Errorf("shape of a frame showing %v = %q, want %q", r, got, want)
		}
	}

	var w capture.ShapeWatch
	var got []bool
	for _, s := range []string{capture.OrientationPortrait, "", capture.OrientationPortrait, // the first shape isn't a change
		capture.OrientationLandscape, capture.OrientationLandscape, "", capture. // turned: three frames detected
												OrientationLandscape, capture.OrientationLandscape, capture.OrientationPortrait,
	} {
		got = append(got, w.See(s, 3))
	}
	if want := []bool{false, false, false, true, true, true, false, false, true}; !reflect.DeepEqual(got, want) {
		t.Errorf("detected %v, want %v", got, want)
	}
}

// The saved crop for the way the device is turned is used instead of what
// is detected, scaled to the frames being captured.
func TestSavedCropOverridesDetection(t *testing.T) {
	fb := &capture.FrameBuffer{}
	fb.SetFrameSize(hd)
	fb.SetContentRect(tall)
	fb.SetContentRect(tall)
	fb.SetContentRect(tallDark) // a dark app shrinks what is detected
	if fb.ContentRect() != tallDark || fb.CropMode() != "auto" {
		t.Fatalf("no saved crop: content %v mode %s", fb.ContentRect(), fb.CropMode())
	}

	fb.SetCropUse(capture.CropSaved)
	fb.SetSavedCrop(capture.OrientationLandscape, capture.SavedCrop{Rect: wide, Frame: hd})
	if fb.ContentRect() != tallDark || fb.CropMode() != capture.CropAuto {
		t.Errorf("a landscape crop was used upright, or nothing saved for upright wasn't auto")
	}
	fb.SetSavedCrop(capture.OrientationPortrait, capture.SavedCrop{Rect: tall, Frame: hd})
	if got, _ := fb.SavedCropFor(image.Rectangle{Max: hd}); got != tall {
		t.Errorf("crop = %v, want the saved %v", got, tall)
	}
	if fb.ContentRect() != tall || fb.DetectedRect() != tallDark || fb.CropMode() != "saved" {
		t.Errorf("saved crop: content %v detected %v mode %s", fb.ContentRect(), fb.DetectedRect(), fb.CropMode())
	}

	// The capture resolution changed: the dongle scales the whole picture,
	// so the saved crop and the last detection scale with it.
	for _, c := range []struct {
		frame         image.Point
		saved, detect image.Rectangle
	}{
		{image.Pt(1280, 720), image.Rect(489, 36, 791, 684), image.Rect(500, 51, 779, 671)},
		{image.Pt(720, 480), image.Rect(275, 24, 445, 456), image.Rect(281, 34, 438, 448)}, // stretched
	} {
		fb.SetFrameSize(c.frame)
		got, ok := fb.SavedCropFor(image.Rectangle{Max: c.frame})
		if !ok || got != c.saved || fb.ContentRect() != c.saved || fb.CropMode() != capture.CropSaved {
			t.Errorf("at %v: saved crop %v %v, content %v, mode %s; want %v", c.frame, got, ok, fb.ContentRect(), fb.CropMode(), c.saved)
		}
		if fb.DetectedRect() != c.detect {
			t.Errorf("at %v: detected %v, want %v", c.frame, fb.DetectedRect(), c.detect)
		}
	}
	fb.SetFrameSize(hd)
	if fb.DetectedRect() != tallDark {
		t.Errorf("back at 1080p, detected %v, want %v: scaling drifted", fb.DetectedRect(), tallDark)
	}

	// Auto or off chosen: the saved crops are kept, not used.
	for _, use := range []string{capture.CropAuto, capture.CropOff} {
		fb.SetCropUse(use)
		if _, ok := fb.SavedCropFor(image.Rectangle{Max: hd}); ok || fb.CropMode() != use || fb.ContentRect() != tallDark {
			t.Errorf("%s chosen: mode %s, content %v", use, fb.CropMode(), fb.ContentRect())
		}
	}
	fb.SetCropUse(capture.CropSaved)

	fb.SetSavedCrop(capture.OrientationPortrait, capture.SavedCrop{})
	if _, ok := fb.SavedCropFor(image.Rectangle{Max: hd}); ok || len(fb.SavedCrops()) != 1 {
		t.Errorf("clearing the portrait crop: %v", fb.SavedCrops())
	}
}

func TestSavedCropsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roadie", "crop-x.json")
	if use, m, err := capture.LoadSavedCrops(path); use != capture.CropAuto || m != nil || err != nil {
		t.Fatalf("no file: %q %v, %v", use, m, err)
	}
	want := map[string]capture.SavedCrop{capture.OrientationPortrait: {Rect: tall, Frame: hd}, capture.OrientationLandscape: {Rect: wide, Frame: hd}}
	if err := capture.StoreSavedCrops(path, capture.CropOff, want); err != nil {
		t.Fatal(err)
	}
	use, got, err := capture.LoadSavedCrops(path)
	if err != nil || use != capture.CropOff || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip: %q %v, %v", use, got, err)
	}
	if err := capture.StoreSavedCrops(path, capture.CropOff, nil); err != nil {
		t.Fatal(err)
	}
	if use, _, _ := capture.LoadSavedCrops(path); use != capture.CropOff {
		t.Errorf("off with no crops saved wasn't kept: %q", use)
	}
	if err := capture.StoreSavedCrops(path, capture.CropAuto, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("auto with no crops saved left the file")
	}

	// A file from before there was a choice: saved, as it was then.
	os.WriteFile(path, []byte(`{"portrait":{"x":734,"y":54,"width":452,"height":972,"frame_width":1920,"frame_height":1080}}`), 0o644)
	if use, m, err := capture.LoadSavedCrops(path); use != capture.CropSaved || len(m) != 1 || err != nil {
		t.Errorf("older file: %q %v, %v", use, m, err)
	}

	for name, body := range map[string]string{
		"not json":     "{",
		"out of frame": `{"portrait":{"x":1800,"y":0,"width":452,"height":972,"frame_width":1920,"frame_height":1080}}`,
		"bad use":      `{"use":"sometimes"}`,
	} {
		os.WriteFile(path, []byte(body), 0o644)
		if _, _, err := capture.LoadSavedCrops(path); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func cropCall(t *testing.T, s *Server, method, url, body string) (int, cropState, string) {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPICrop(w, httptest.NewRequest(method, url, strings.NewReader(body)))
	var st cropState
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, st, w.Body.String()
}

func TestCropAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crop.json")
	fb := &capture.FrameBuffer{}
	s := &Server{Buf: fb, CropFile: path}

	if code, _, _ := cropCall(t, s, "POST", "/api/crop", ""); code != http.StatusConflict {
		t.Errorf("save before any frame: %d, want 409", code)
	}
	fb.SetFrameSize(hd)
	if code, _, msg := cropCall(t, s, "POST", "/api/crop", ""); code != http.StatusConflict || !strings.Contains(msg, "bright") {
		t.Errorf("save with nothing detected: %d %q", code, msg)
	}

	// Save what is detected: the device is upright.
	fb.SetContentRect(tall)
	fb.SetContentRect(tall)
	code, st, _ := cropCall(t, s, "POST", "/api/crop", "")
	if code != http.StatusOK || st.Use != capture.CropSaved || st.Mode != capture.CropSaved || st.Orientation != capture.OrientationPortrait ||
		st.Saved.Portrait == nil || st.Saved.Portrait.SavedCrop().Rect != tall || st.Saved.Landscape != nil {
		t.Fatalf("save detected: %d %+v", code, st)
	}
	if use, m, _ := capture.LoadSavedCrops(path); use != capture.CropSaved || len(m) != 1 {
		t.Errorf("not written to the file: %q %v", use, m)
	}

	// Choose: auto and off keep the saved crop but don't use it.
	for _, use := range []string{capture.CropAuto, capture.CropOff, capture.CropSaved} {
		code, st, _ := cropCall(t, s, "PUT", "/api/crop", `{"use":"`+use+`"}`)
		if code != http.StatusOK || st.Use != use || st.Mode != use || st.Saved.Portrait == nil {
			t.Errorf("choose %s: %d %+v", use, code, st)
		}
		if got, _, _ := capture.LoadSavedCrops(path); got != use {
			t.Errorf("choose %s: the file says %q", use, got)
		}
	}
	for _, body := range []string{`{"use":"sometimes"}`, `{`} {
		if code, _, _ := cropCall(t, s, "PUT", "/api/crop", body); code != http.StatusBadRequest {
			t.Errorf("PUT %s: %d, want 400", body, code)
		}
	}

	// Save a rectangle given; its shape says landscape.
	code, st, _ = cropCall(t, s, "POST", "/api/crop", `{"x":0,"y":97,"width":1920,"height":886}`)
	if code != http.StatusOK || st.Saved.Landscape == nil || st.Saved.Landscape.SavedCrop().Rect != wide {
		t.Fatalf("save given: %d %+v", code, st)
	}

	for _, body := range []string{
		`{"x":0,"y":0,"width":100}`,                 // half a rectangle
		`{"x":1800,"y":0,"width":452,"height":972}`, // off the frame
		`{"x":0,"y":0,"width":100,"height":100}`,    // square: which way?
		`{"orientation":"sideways"}`,                // no such way
	} {
		if code, _, _ := cropCall(t, s, "POST", "/api/crop", body); code != http.StatusBadRequest {
			t.Errorf("POST %s: %d, want 400", body, code)
		}
	}

	// Saved chosen, upright, but only a landscape crop left: auto for now.
	code, st, _ = cropCall(t, s, "DELETE", "/api/crop?orientation=portrait", "")
	if code != http.StatusOK || st.Saved.Portrait != nil || st.Saved.Landscape == nil || st.Use != capture.CropSaved || st.Mode != capture.CropAuto {
		t.Errorf("clear portrait: %d %+v", code, st)
	}
	// None left: back to auto.
	code, st, _ = cropCall(t, s, "DELETE", "/api/crop", "")
	if code != http.StatusOK || st.Saved.Landscape != nil || st.Use != capture.CropAuto {
		t.Errorf("clear all: %d %+v", code, st)
	}
	if code, _, _ := cropCall(t, s, "PUT", "/api/crop", `{"use":"saved"}`); code != http.StatusConflict {
		t.Errorf("choose saved with none saved: %d, want 409", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("clearing every crop left the file")
	}
	if code, _, _ := cropCall(t, s, "DELETE", "/api/crop?orientation=up", ""); code != http.StatusBadRequest {
		t.Errorf("clear a made-up orientation: %d, want 400", code)
	}
	if code, _, _ := cropCall(t, s, "PATCH", "/api/crop", ""); code != http.StatusMethodNotAllowed {
		t.Errorf("PATCH: %d, want 405", code)
	}
}

// A presumed orientation applies before anything is detected, and
// detection still turns it.
func TestPresumedOrientation(t *testing.T) {
	fb := &capture.FrameBuffer{}
	fb.SetFrameSize(hd)
	fb.SetSavedCrop(capture.OrientationPortrait, capture.SavedCrop{Rect: tall, Frame: hd})
	fb.SetCropUse(capture.CropSaved)
	fb.PresumeOrientation(capture.OrientationPortrait)
	if fb.CropMode() != "saved" || fb.ContentRect() != tall {
		t.Fatalf("before any detection: mode %s, content %v", fb.CropMode(), fb.ContentRect())
	}
	fb.SetContentRect(wide)
	fb.SetContentRect(wide)
	if fb.Orientation() != capture.OrientationLandscape || fb.CropMode() != "auto" {
		t.Errorf("turned sideways: %s, mode %s", fb.Orientation(), fb.CropMode())
	}
	fb.PresumeOrientation(capture.OrientationPortrait)
	if fb.Orientation() != capture.OrientationLandscape {
		t.Errorf("a presumption overrode what was detected")
	}
}

// /api/settings' autocrop is the two-way form of the choice, kept with it.
func TestSettingsAutocropChoosesTheCrop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crop.json")
	s := &Server{Buf: &capture.FrameBuffer{}, CropFile: path}
	put := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		s.handleAPISettings(w, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d", body, w.Code)
		}
	}
	put(`{"autocrop":false}`)
	if use, _, _ := capture.LoadSavedCrops(path); s.Buf.CropUse() != capture.CropOff || use != capture.CropOff {
		t.Errorf("autocrop false: chosen %q, file %q", s.Buf.CropUse(), use)
	}
	put(`{"autocrop":true}`)
	if s.Buf.CropUse() != capture.CropAuto {
		t.Errorf("autocrop true: chosen %q", s.Buf.CropUse())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("back to the default left a file")
	}
}

// /api/settings shows and sets the glide switch, where the input can glide.
func TestSettingsGlide(t *testing.T) {
	h := hid.NewSettledHID(&pathHID{})
	s := &Server{Buf: &capture.FrameBuffer{}, HID: h}
	call := func(method, body string) (int, map[string]interface{}) {
		t.Helper()
		w := httptest.NewRecorder()
		s.handleAPISettings(w, httptest.NewRequest(method, "/api/settings", strings.NewReader(body)))
		var got map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &got)
		return w.Code, got
	}
	if _, got := call("GET", ""); got["glide"] != false {
		t.Errorf("GET: glide = %v, want false", got["glide"])
	}
	if code, got := call("PUT", `{"glide":true}`); code != http.StatusOK || got["glide"] != true || !h.Glide() {
		t.Errorf("PUT glide true: %d %v, switch %v", code, got["glide"], h.Glide())
	}

	s.HID = &pathHID{} // an input that can't glide
	if _, got := call("GET", ""); got["glide"] != nil {
		t.Errorf("no glide switch, but GET says %v", got["glide"])
	}
	if code, _ := call("PUT", `{"glide":true}`); code != http.StatusConflict {
		t.Errorf("PUT glide to an input that can't: %d", code)
	}
}

// Saving what is detected trims the edge lines that stand out: one
// half-lit, where the target's edge falls partway across a pixel, and one
// the scaler brightened. A crop given as numbers is saved as given.
func TestSaveTrimsEdgeLinesThatStandOut(t *testing.T) {
	dark := color.RGBA{16, 16, 16, 255}
	white := color.RGBA{235, 235, 235, 255}
	picture := image.Rect(735, 54, 1184, 1026)
	img := image.NewRGBA(image.Rectangle{Max: hd})
	for y := 0; y < hd.Y; y++ {
		for x := 0; x < hd.X; x++ {
			c := dark
			if image.Pt(x, y).In(picture) {
				c = white
				switch x {
				case picture.Min.X:
					c = color.RGBA{124, 124, 124, 255} // half across a pixel
				case picture.Min.X + 1, picture.Max.X - 1:
					c = color.RGBA{255, 255, 255, 255} // sharpened
				}
			}
			img.Set(x, y, c)
		}
	}
	var frame bytes.Buffer
	if err := jpeg.Encode(&frame, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	fb := &capture.FrameBuffer{}
	s := &Server{Buf: fb, CropFile: filepath.Join(t.TempDir(), "crop.json")}
	fb.SetFrameSize(hd)
	fb.UpdateBoth(frame.Bytes(), frame.Bytes(), image.Rectangle{Max: hd})
	fb.SetContentRect(picture)

	_, st, _ := cropCall(t, s, "POST", "/api/crop", "")
	if got, want := st.Saved.Portrait.SavedCrop().Rect, image.Rect(737, 54, 1183, 1026); got != want {
		t.Errorf("saved %v, want %v: the half-lit and sharpened columns trimmed, the clean edges kept", got, want)
	}
	_, st, _ = cropCall(t, s, "POST", "/api/crop", `{"x":735,"y":54,"width":449,"height":972}`)
	if got := st.Saved.Portrait.SavedCrop().Rect; got != picture {
		t.Errorf("saved %v from numbers, want them as given, %v", got, picture)
	}
}

// Frame sizes shared with the capture package's tests, which keep their own.
var (
	hd   = image.Pt(1920, 1080)
	tall = image.Rect(734, 54, 1186, 1026) // an iPhone held upright
	wide = image.Rect(0, 97, 1920, 983)    // the iPhone turned sideways
)
