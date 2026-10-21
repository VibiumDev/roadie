package main

import (
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var (
	hd        = image.Pt(1920, 1080)
	tall      = image.Rect(734, 54, 1186, 1026) // an iPhone held upright
	tallDark  = image.Rect(750, 76, 1168, 1007) // the same, in a dark app
	wide      = image.Rect(0, 97, 1920, 983)    // the iPhone turned sideways
	nearBlack = image.Rect(900, 500, 980, 580)  // a dark screen's lone bright patch
)

func TestOrientationOf(t *testing.T) {
	for r, want := range map[image.Rectangle]string{
		tall: OrientationPortrait, tallDark: OrientationPortrait, wide: OrientationLandscape,
		image.Rect(0, 0, 100, 110): "", {}: "",
	} {
		if got := orientationOf(r); got != want {
			t.Errorf("orientationOf(%v) = %q, want %q", r, got, want)
		}
	}
}

// The orientation follows the picture, but one odd detection doesn't turn
// it, and one that can't tell keeps it.
func TestOrientationNeedsTwoDetections(t *testing.T) {
	fb := &FrameBuffer{}
	step := func(r image.Rectangle, want string) {
		t.Helper()
		fb.SetContentRect(r)
		if got := fb.Orientation(); got != want {
			t.Fatalf("after %v: orientation %q, want %q", r, got, want)
		}
	}
	step(wide, "") // not even the first is taken alone
	step(tall, "")
	step(tall, OrientationPortrait) // two in a row say
	step(wide, OrientationPortrait) // one wide one isn't enough
	step(tallDark, OrientationPortrait)
	step(nearBlack, OrientationPortrait) // can't tell: keeps it
	step(wide, OrientationPortrait)
	step(wide, OrientationLandscape) // two in a row turn it
}

// The saved crop for the way the device is turned is used instead of what
// is detected, at the frame size it was saved at only.
func TestSavedCropOverridesDetection(t *testing.T) {
	fb := &FrameBuffer{}
	fb.setFrameSize(hd)
	fb.SetContentRect(tall)
	fb.SetContentRect(tall)
	fb.SetContentRect(tallDark) // a dark app shrinks what is detected
	if fb.ContentRect() != tallDark || fb.CropMode() != "auto" {
		t.Fatalf("no saved crop: content %v mode %s", fb.ContentRect(), fb.CropMode())
	}

	fb.SetCropUse(CropSaved)
	fb.SetSavedCrop(OrientationLandscape, SavedCrop{Rect: wide, Frame: hd})
	if fb.ContentRect() != tallDark || fb.CropMode() != CropAuto {
		t.Errorf("a landscape crop was used upright, or nothing saved for upright wasn't auto")
	}
	fb.SetSavedCrop(OrientationPortrait, SavedCrop{Rect: tall, Frame: hd})
	if got, _ := fb.savedCropFor(image.Rectangle{Max: hd}); got != tall {
		t.Errorf("crop = %v, want the saved %v", got, tall)
	}
	if fb.ContentRect() != tall || fb.DetectedRect() != tallDark || fb.CropMode() != "saved" {
		t.Errorf("saved crop: content %v detected %v mode %s", fb.ContentRect(), fb.DetectedRect(), fb.CropMode())
	}

	fb.setFrameSize(image.Pt(1280, 720)) // resolution changed: it no longer means anything
	if _, ok := fb.savedCropFor(image.Rect(0, 0, 1280, 720)); ok || fb.CropMode() != "auto" {
		t.Errorf("a crop saved at 1080p was used at 720p")
	}
	fb.setFrameSize(hd)

	// Auto or off chosen: the saved crops are kept, not used.
	for _, use := range []string{CropAuto, CropOff} {
		fb.SetCropUse(use)
		if _, ok := fb.savedCropFor(image.Rectangle{Max: hd}); ok || fb.CropMode() != use || fb.ContentRect() != tallDark {
			t.Errorf("%s chosen: mode %s, content %v", use, fb.CropMode(), fb.ContentRect())
		}
	}
	fb.SetCropUse(CropSaved)

	fb.SetSavedCrop(OrientationPortrait, SavedCrop{})
	if _, ok := fb.savedCropFor(image.Rectangle{Max: hd}); ok || len(fb.SavedCrops()) != 1 {
		t.Errorf("clearing the portrait crop: %v", fb.SavedCrops())
	}
}

func TestSavedCropsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roadie", "crop-x.json")
	if use, m, err := loadSavedCrops(path); use != CropAuto || m != nil || err != nil {
		t.Fatalf("no file: %q %v, %v", use, m, err)
	}
	want := map[string]SavedCrop{
		OrientationPortrait:  {Rect: tall, Frame: hd},
		OrientationLandscape: {Rect: wide, Frame: hd},
	}
	if err := storeSavedCrops(path, CropOff, want); err != nil {
		t.Fatal(err)
	}
	use, got, err := loadSavedCrops(path)
	if err != nil || use != CropOff || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip: %q %v, %v", use, got, err)
	}
	if err := storeSavedCrops(path, CropOff, nil); err != nil {
		t.Fatal(err)
	}
	if use, _, _ := loadSavedCrops(path); use != CropOff {
		t.Errorf("off with no crops saved wasn't kept: %q", use)
	}
	if err := storeSavedCrops(path, CropAuto, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("auto with no crops saved left the file")
	}

	// A file from before there was a choice: saved, as it was then.
	os.WriteFile(path, []byte(`{"portrait":{"x":734,"y":54,"width":452,"height":972,"frame_width":1920,"frame_height":1080}}`), 0o644)
	if use, m, err := loadSavedCrops(path); use != CropSaved || len(m) != 1 || err != nil {
		t.Errorf("older file: %q %v, %v", use, m, err)
	}

	for name, body := range map[string]string{
		"not json":     "{",
		"out of frame": `{"portrait":{"x":1800,"y":0,"width":452,"height":972,"frame_width":1920,"frame_height":1080}}`,
		"bad use":      `{"use":"sometimes"}`,
	} {
		os.WriteFile(path, []byte(body), 0o644)
		if _, _, err := loadSavedCrops(path); err == nil {
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
	fb := &FrameBuffer{}
	s := &Server{Buf: fb, CropFile: path}

	if code, _, _ := cropCall(t, s, "POST", "/api/crop", ""); code != http.StatusConflict {
		t.Errorf("save before any frame: %d, want 409", code)
	}
	fb.setFrameSize(hd)
	if code, _, msg := cropCall(t, s, "POST", "/api/crop", ""); code != http.StatusConflict || !strings.Contains(msg, "bright") {
		t.Errorf("save with nothing detected: %d %q", code, msg)
	}

	// Save what is detected: the device is upright.
	fb.SetContentRect(tall)
	fb.SetContentRect(tall)
	code, st, _ := cropCall(t, s, "POST", "/api/crop", "")
	if code != http.StatusOK || st.Use != CropSaved || st.Mode != CropSaved || st.Orientation != OrientationPortrait ||
		st.Saved.Portrait == nil || st.Saved.Portrait.savedCrop().Rect != tall || st.Saved.Landscape != nil {
		t.Fatalf("save detected: %d %+v", code, st)
	}
	if use, m, _ := loadSavedCrops(path); use != CropSaved || len(m) != 1 {
		t.Errorf("not written to the file: %q %v", use, m)
	}

	// Choose: auto and off keep the saved crop but don't use it.
	for _, use := range []string{CropAuto, CropOff, CropSaved} {
		code, st, _ := cropCall(t, s, "PUT", "/api/crop", `{"use":"`+use+`"}`)
		if code != http.StatusOK || st.Use != use || st.Mode != use || st.Saved.Portrait == nil {
			t.Errorf("choose %s: %d %+v", use, code, st)
		}
		if got, _, _ := loadSavedCrops(path); got != use {
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
	if code != http.StatusOK || st.Saved.Landscape == nil || st.Saved.Landscape.savedCrop().Rect != wide {
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
	if code != http.StatusOK || st.Saved.Portrait != nil || st.Saved.Landscape == nil || st.Use != CropSaved || st.Mode != CropAuto {
		t.Errorf("clear portrait: %d %+v", code, st)
	}
	// None left: back to auto.
	code, st, _ = cropCall(t, s, "DELETE", "/api/crop", "")
	if code != http.StatusOK || st.Saved.Landscape != nil || st.Use != CropAuto {
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
	fb := &FrameBuffer{}
	fb.setFrameSize(hd)
	fb.SetSavedCrop(OrientationPortrait, SavedCrop{Rect: tall, Frame: hd})
	fb.SetCropUse(CropSaved)
	fb.presumeOrientation(OrientationPortrait)
	if fb.CropMode() != "saved" || fb.ContentRect() != tall {
		t.Fatalf("before any detection: mode %s, content %v", fb.CropMode(), fb.ContentRect())
	}
	fb.SetContentRect(wide)
	fb.SetContentRect(wide)
	if fb.Orientation() != OrientationLandscape || fb.CropMode() != "auto" {
		t.Errorf("turned sideways: %s, mode %s", fb.Orientation(), fb.CropMode())
	}
	fb.presumeOrientation(OrientationPortrait)
	if fb.Orientation() != OrientationLandscape {
		t.Errorf("a presumption overrode what was detected")
	}
}

// /api/settings' autocrop is the two-way form of the choice, kept with it.
func TestSettingsAutocropChoosesTheCrop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crop.json")
	s := &Server{Buf: &FrameBuffer{}, CropFile: path}
	put := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		s.handleAPISettings(w, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d", body, w.Code)
		}
	}
	put(`{"autocrop":false}`)
	if use, _, _ := loadSavedCrops(path); s.Buf.CropUse() != CropOff || use != CropOff {
		t.Errorf("autocrop false: chosen %q, file %q", s.Buf.CropUse(), use)
	}
	put(`{"autocrop":true}`)
	if s.Buf.CropUse() != CropAuto {
		t.Errorf("autocrop true: chosen %q", s.Buf.CropUse())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("back to the default left a file")
	}
}

// /api/settings shows and sets the glide switch, where the input can glide.
func TestSettingsGlide(t *testing.T) {
	h := newSettledHID(&pathHID{})
	s := &Server{Buf: &FrameBuffer{}, HID: h}
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
