package capture

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/VibiumDev/roadie/internal/platform"
	"image"
	"io/fs"
	"os"
	"path/filepath"
)

// Saved crops live in the user's config directory, one file per Roadie
// instance (by --name), so they outlive restarts and two instances on one
// host keep their own.
func DefaultCropFile(name string) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "roadie", "crop-"+platform.MDNSHostname(name)+".json")
}

// cropJSON is one saved crop as the API and the file show it.
type CropJSON struct {
	X           int `json:"x"`
	Y           int `json:"y"`
	Width       int `json:"width"`
	Height      int `json:"height"`
	FrameWidth  int `json:"frame_width"`
	FrameHeight int `json:"frame_height"`
}

func (c CropJSON) SavedCrop() SavedCrop {
	return SavedCrop{
		Rect:  image.Rect(c.X, c.Y, c.X+c.Width, c.Y+c.Height),
		Frame: image.Pt(c.FrameWidth, c.FrameHeight),
	}
}

func CropJSONOf(c SavedCrop) *CropJSON {
	return &CropJSON{
		X: c.Rect.Min.X, Y: c.Rect.Min.Y, Width: c.Rect.Dx(), Height: c.Rect.Dy(),
		FrameWidth: c.Frame.X, FrameHeight: c.Frame.Y,
	}
}

// SavedCropsJSON is a crop for each way the target can be turned.
type SavedCropsJSON struct {
	Portrait  *CropJSON `json:"portrait"`
	Landscape *CropJSON `json:"landscape"`
}

// cropFileJSON is the file: the crop chosen, and the saved crops.
type cropFileJSON struct {
	Use string `json:"use"`
	SavedCropsJSON
}

func ValidCropUse(use string) bool {
	return use == CropAuto || use == CropOff || use == CropSaved
}

func SavedCropsJSONOf(m map[string]SavedCrop) SavedCropsJSON {
	var out SavedCropsJSON
	if c, ok := m[OrientationPortrait]; ok {
		out.Portrait = CropJSONOf(c)
	}
	if c, ok := m[OrientationLandscape]; ok {
		out.Landscape = CropJSONOf(c)
	}
	return out
}

// loadSavedCrops reads the crop chosen and the saved crops; no file means
// auto-crop and none saved.
func LoadSavedCrops(path string) (string, map[string]SavedCrop, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return CropAuto, nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var f cropFileJSON
	if err := json.Unmarshal(data, &f); err != nil {
		return "", nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Use != "" && !ValidCropUse(f.Use) {
		return "", nil, fmt.Errorf("%s: use %q isn't auto, off or saved", path, f.Use)
	}
	out := map[string]SavedCrop{}
	for o, c := range map[string]*CropJSON{OrientationPortrait: f.Portrait, OrientationLandscape: f.Landscape} {
		if c == nil {
			continue
		}
		sc := c.SavedCrop()
		if sc.Rect.Empty() || !sc.Rect.In(image.Rectangle{Max: sc.Frame}) {
			return "", nil, fmt.Errorf("%s: %s crop %v isn't inside a %v frame", path, o, sc.Rect, sc.Frame)
		}
		out[o] = sc
	}
	if f.Use == "" { // written before there was a choice: saved if any
		f.Use = CropAuto
		if len(out) > 0 {
			f.Use = CropSaved
		}
	}
	return f.Use, out, nil
}

// storeSavedCrops writes the crop chosen and the saved crops, replacing the
// file whole so a crash can't leave half of one. Auto-crop with none saved
// is the default, so then the file goes.
func StoreSavedCrops(path, use string, m map[string]SavedCrop) error {
	if use == CropAuto && len(m) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cropFileJSON{Use: use, SavedCropsJSON: SavedCropsJSONOf(m)}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
