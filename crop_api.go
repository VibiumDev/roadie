package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
)

// cropState is what /api/crop answers with.
type cropState struct {
	// Use is the crop chosen: "auto", "off" or "saved".
	Use string `json:"use"`
	// Mode is what decides the crop now: the crop chosen, except "auto"
	// when "saved" is chosen but there is none for the way the target is
	// turned.
	Mode string `json:"mode"`
	// Orientation is which way the target's picture is turned, as detected.
	Orientation string         `json:"orientation"`
	Saved       savedCropsJSON `json:"saved"`
	// Detected is the picture's edges as last detected: what POST saves.
	Detected *cropJSON `json:"detected"`
}

func (s *Server) cropState() cropState {
	st := cropState{
		Use:         s.Buf.CropUse(),
		Mode:        s.Buf.CropMode(),
		Orientation: s.Buf.Orientation(),
		Saved:       savedCropsJSONOf(s.Buf.SavedCrops()),
	}
	if r := s.Buf.DetectedRect(); !r.Empty() {
		st.Detected = cropJSONOf(SavedCrop{Rect: r, Frame: s.Buf.FrameSize()})
	}
	return st
}

// handleAPICrop chooses the crop and keeps the saved ones. GET answers with
// the state; PUT {"use": ...} chooses auto, off or saved; POST saves a crop
// for the way the target is turned now, from the detected edges or a
// rectangle given, and chooses it; DELETE clears the saved crops, or the
// one named by ?orientation=.
func (s *Server) handleAPICrop(w http.ResponseWriter, r *http.Request) {
	if s.Buf == nil {
		http.Error(w, "no video source", http.StatusServiceUnavailable)
		return
	}
	var status int
	var err error
	switch r.Method {
	case http.MethodGet:
	case http.MethodPut:
		status, err = s.chooseCrop(r)
	case http.MethodPost:
		status, err = s.saveCrop(r)
	case http.MethodDelete:
		status, err = s.clearCrop(r.URL.Query().Get("orientation"))
	default:
		w.Header().Set("Allow", "GET, PUT, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.cropState())
}

func (s *Server) chooseCrop(r *http.Request) (int, error) {
	var body struct{ Use string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return http.StatusBadRequest, fmt.Errorf("bad request: %v", err)
	}
	if !validCropUse(body.Use) {
		return http.StatusBadRequest, errors.New(`use must be "auto", "off" or "saved"`)
	}
	s.cropMu.Lock()
	defer s.cropMu.Unlock()
	if body.Use == CropSaved && len(s.Buf.SavedCrops()) == 0 {
		return http.StatusConflict, errors.New("no crop saved yet: save one first")
	}
	s.Buf.SetCropUse(body.Use)
	log.Printf("crop: using %s", body.Use)
	return s.storeCrops()
}

func (s *Server) saveCrop(r *http.Request) (int, error) {
	var body struct {
		X, Y, Width, Height *int
		Orientation         string
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return http.StatusBadRequest, fmt.Errorf("bad request: %v", err)
	}
	frame := s.Buf.FrameSize()
	if frame == (image.Point{}) {
		return http.StatusConflict, errors.New("no frames captured yet")
	}
	var rect image.Rectangle
	switch {
	case body.X != nil && body.Y != nil && body.Width != nil && body.Height != nil:
		rect = image.Rect(*body.X, *body.Y, *body.X+*body.Width, *body.Y+*body.Height)
	case body.X != nil || body.Y != nil || body.Width != nil || body.Height != nil:
		return http.StatusBadRequest, errors.New("give x, y, width and height, or none of them to save what is detected")
	default:
		rect = s.Buf.DetectedRect()
		if rect.Empty() {
			return http.StatusConflict, errors.New("nothing detected: the screen is black at its edges. Show something bright to its edges, such as a white page, and save again")
		}
	}
	if rect.Empty() || !rect.In(image.Rectangle{Max: frame}) {
		return http.StatusBadRequest, fmt.Errorf("crop %v isn't inside the %dx%d frame", rect, frame.X, frame.Y)
	}
	o := body.Orientation
	if o == "" {
		o = orientationOf(rect)
	}
	if o != OrientationPortrait && o != OrientationLandscape {
		return http.StatusBadRequest, errors.New(`orientation must be "portrait" or "landscape"`)
	}

	s.cropMu.Lock()
	defer s.cropMu.Unlock()
	s.Buf.SetSavedCrop(o, SavedCrop{Rect: rect, Frame: frame})
	s.Buf.SetCropUse(CropSaved)
	log.Printf("crop: saved the %s crop, %dx%d at (%d,%d) of a %dx%d frame, and using it",
		o, rect.Dx(), rect.Dy(), rect.Min.X, rect.Min.Y, frame.X, frame.Y)
	return s.storeCrops()
}

func (s *Server) clearCrop(o string) (int, error) {
	orientations := []string{OrientationPortrait, OrientationLandscape}
	switch o {
	case "":
	case OrientationPortrait, OrientationLandscape:
		orientations = []string{o}
	default:
		return http.StatusBadRequest, errors.New(`orientation must be "portrait" or "landscape"`)
	}
	s.cropMu.Lock()
	defer s.cropMu.Unlock()
	for _, o := range orientations {
		s.Buf.SetSavedCrop(o, SavedCrop{})
	}
	msg := ""
	if len(s.Buf.SavedCrops()) == 0 && s.Buf.CropUse() == CropSaved {
		s.Buf.SetCropUse(CropAuto)
		msg = ", back to auto"
	}
	log.Printf("crop: cleared the saved %v crop%s", orientations, msg)
	return s.storeCrops()
}

// storeCrops writes the crop chosen and the saved crops to CropFile;
// without one they last until roadie stops.
func (s *Server) storeCrops() (int, error) {
	if s.CropFile == "" {
		return 0, nil
	}
	if err := storeSavedCrops(s.CropFile, s.Buf.CropUse(), s.Buf.SavedCrops()); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("in use, but not saved for next time: %v", err)
	}
	return 0, nil
}
