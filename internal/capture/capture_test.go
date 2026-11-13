package capture

import (
	"bytes"
	"github.com/VibiumDev/roadie/internal/devices"
	"github.com/pion/mediadevices/pkg/driver"
	"github.com/pion/mediadevices/pkg/prop"
	"image"
	"image/color"
	"reflect"
	"sync"
	"testing"
	"time"
)

// makeImage creates a w×h RGBA image filled with fillColor, then draws a
// rectangle of contentColor within the given content bounds.
func makeImage(w, h int, fillColor, contentColor color.RGBA, content image.Rectangle) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, fillColor)
		}
	}
	for y := content.Min.Y; y < content.Max.Y; y++ {
		for x := content.Min.X; x < content.Max.X; x++ {
			img.Set(x, y, contentColor)
		}
	}
	return img
}

func TestDetectContentRectFullImage(t *testing.T) {
	// All bright pixels — no black bars at all.
	img := makeImage(1920, 1080, color.RGBA{200, 200, 200, 255}, color.RGBA{200, 200, 200, 255}, image.Rect(0, 0, 1920, 1080))
	got := detectContentRect(img, cropThreshold)
	want := image.Rect(0, 0, 1920, 1080)
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDetectContentRectPillarbox(t *testing.T) {
	// Black bars on left (0-520) and right (1400-1920), content in middle.
	black := color.RGBA{0, 0, 0, 255}
	bright := color.RGBA{200, 200, 200, 255}
	img := makeImage(1920, 1080, black, bright, image.Rect(520, 0, 1400, 1080))
	got := detectContentRect(img, cropThreshold)
	if got.Min.X != 520 || got.Max.X != 1400 || got.Min.Y != 0 || got.Max.Y != 1080 {
		t.Errorf("got %v, want (520,0)-(1400,1080)", got)
	}
}

func TestDetectContentRectLetterbox(t *testing.T) {
	// Black bars on top (0-140) and bottom (940-1080), content in middle.
	black := color.RGBA{0, 0, 0, 255}
	bright := color.RGBA{200, 200, 200, 255}
	img := makeImage(1920, 1080, black, bright, image.Rect(0, 140, 1920, 940))
	got := detectContentRect(img, cropThreshold)
	if got.Min.Y != 140 || got.Max.Y != 940 || got.Min.X != 0 || got.Max.X != 1920 {
		t.Errorf("got %v, want (0,140)-(1920,940)", got)
	}
}

func TestDetectContentRectAllBlack(t *testing.T) {
	// Entirely black image — should return full bounds (no crop).
	img := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	got := detectContentRect(img, cropThreshold)
	want := image.Rect(0, 0, 1920, 1080)
	if got != want {
		t.Errorf("got %v, want %v (full bounds for all-black)", got, want)
	}
}

func TestFrameBufferDualStorage(t *testing.T) {
	fb := &FrameBuffer{}
	cropped := TestJPEG(880, 1080)
	raw := TestJPEG(1920, 1080)
	rect := image.Rect(520, 0, 1400, 1080)

	fb.UpdateBoth(cropped, raw, rect)

	if got := fb.Latest(); !bytes.Equal(got, cropped) {
		t.Error("Latest() should return cropped frame")
	}
	if got := fb.LatestRaw(); !bytes.Equal(got, raw) {
		t.Error("LatestRaw() should return raw frame")
	}
	if got := fb.CropRect(); got != rect {
		t.Errorf("CropRect() = %v, want %v", got, rect)
	}
}

func TestFrameBufferUpdateCropped(t *testing.T) {
	fb := &FrameBuffer{}
	raw := TestJPEG(1920, 1080)
	cropped1 := TestJPEG(880, 1080)
	rect := image.Rect(520, 0, 1400, 1080)

	// Set initial state with both.
	fb.UpdateBoth(cropped1, raw, rect)

	// Update only cropped.
	cropped2 := TestJPEG(600, 1080)
	rect2 := image.Rect(660, 0, 1260, 1080)
	fb.UpdateCropped(cropped2, rect2)

	if got := fb.Latest(); !bytes.Equal(got, cropped2) {
		t.Error("Latest() should return updated cropped frame")
	}
	if got := fb.LatestRaw(); !bytes.Equal(got, raw) {
		t.Error("LatestRaw() should still return original raw frame")
	}
	if got := fb.CropRect(); got != rect2 {
		t.Errorf("CropRect() = %v, want %v", got, rect2)
	}
}

func TestFrameBufferLatestNil(t *testing.T) {
	fb := &FrameBuffer{}
	if got := fb.Latest(); got != nil {
		t.Errorf("expected nil, got %d bytes", len(got))
	}
}

func TestFrameBufferStoreAndRetrieve(t *testing.T) {
	fb := &FrameBuffer{}
	frame := TestJPEG(320, 240)
	fb.Update(frame)

	if got := fb.Latest(); !bytes.Equal(got, frame) {
		t.Error("retrieved frame does not match stored frame")
	}
}

func TestFrameBufferOverwrite(t *testing.T) {
	fb := &FrameBuffer{}
	fb.Update(TestJPEG(320, 240))

	frame2 := TestJPEG(640, 480)
	fb.Update(frame2)

	if got := fb.Latest(); !bytes.Equal(got, frame2) {
		t.Error("expected latest frame to be frame2")
	}
}

func TestFrameBufferConcurrent(t *testing.T) {
	fb := &FrameBuffer{}
	frame := TestJPEG(320, 240)

	var wg sync.WaitGroup
	wg.Add(5)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			fb.Update(frame)
		}
	}()
	for i := 0; i < 4; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				fb.Latest()
			}
		}()
	}
	wg.Wait()
}

func TestFrameBufferStatus(t *testing.T) {
	fb := &FrameBuffer{}
	if got := fb.Status(); got != "" {
		t.Errorf("default status: expected empty, got %q", got)
	}
	for _, s := range []CaptureStatus{StatusConnected, StatusDisconnected, StatusConnecting} {
		fb.SetStatus(s)
		if got := fb.Status(); got != s {
			t.Errorf("SetStatus(%q): got %q", s, got)
		}
	}
}

func TestFrameBufferClear(t *testing.T) {
	fb := &FrameBuffer{}
	fb.Update(TestJPEG(320, 240))
	fb.SetStatus(StatusConnected)

	fb.Clear()

	if fb.Latest() != nil {
		t.Error("expected nil frame after Clear")
	}
	if fb.Status() != StatusDisconnected {
		t.Errorf("expected %q after Clear, got %q", StatusDisconnected, fb.Status())
	}
}

func TestIsMajorCropChangeFirstDetection(t *testing.T) {
	full := image.Rect(0, 0, 1920, 1080)
	newRect := image.Rect(520, 0, 1400, 1080)
	if !isMajorCropChange(image.Rectangle{}, newRect, full) {
		t.Error("expected true for first detection (zero activeCrop)")
	}
}

func TestIsMajorCropChangeSmallShift(t *testing.T) {
	full := image.Rect(0, 0, 1920, 1080)
	active := image.Rect(520, 0, 1400, 1080)
	// Shift by a few pixels — area stays the same.
	shifted := image.Rect(522, 0, 1402, 1080)
	if isMajorCropChange(active, shifted, full) {
		t.Error("expected false for small shift with same area")
	}
}

func TestIsMajorCropChangeLargeArea(t *testing.T) {
	full := image.Rect(0, 0, 1920, 1080)
	active := image.Rect(520, 0, 1400, 1080)
	// Jump to full frame — large area change.
	if !isMajorCropChange(active, full, full) {
		t.Error("expected true for large area change")
	}
}

func TestIsMajorCropChangeAspectFlip(t *testing.T) {
	full := image.Rect(0, 0, 1920, 1080)
	landscape := image.Rect(0, 140, 1920, 940) // 1920x800 landscape
	portrait := image.Rect(660, 0, 1260, 1080) // 600x1080 portrait
	if !isMajorCropChange(landscape, portrait, full) {
		t.Error("expected true for landscape→portrait flip")
	}
	if !isMajorCropChange(portrait, landscape, full) {
		t.Error("expected true for portrait→landscape flip")
	}
}

func TestFrameBufferQuality(t *testing.T) {
	fb := &FrameBuffer{}
	// Default (never set) should return 80.
	if got := fb.Quality(); got != 80 {
		t.Errorf("default quality: got %d, want 80", got)
	}
	fb.SetQuality(50)
	if got := fb.Quality(); got != 50 {
		t.Errorf("after SetQuality(50): got %d, want 50", got)
	}
}

func TestFrameBufferQualityClamping(t *testing.T) {
	fb := &FrameBuffer{}
	fb.SetQuality(10)
	if got := fb.Quality(); got != 30 {
		t.Errorf("below min: got %d, want 30", got)
	}
	fb.SetQuality(100)
	if got := fb.Quality(); got != 95 {
		t.Errorf("above max: got %d, want 95", got)
	}
}

func TestIsBlackFrame(t *testing.T) {
	black := image.NewRGBA(image.Rect(0, 0, 100, 100))
	// All pixels default to zero (black).
	if !isBlackFrame(black) {
		t.Error("expected all-black image to be detected as black")
	}

	bright := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			bright.Set(x, y, color.RGBA{R: 200, G: 200, B: 200, A: 255})
		}
	}
	if isBlackFrame(bright) {
		t.Error("expected bright image not to be detected as black")
	}
}

func TestDeviceInfoVideoNode(t *testing.T) {
	// Labels as the mediadevices camera driver actually builds them: the
	// leading segment is the path it enumerated through, which for USB
	// capture dongles is a /dev/v4l/by-id name, not the node.
	tests := []struct {
		label string
		want  string
	}{
		{"usb-Example_HDMI_Capture_B-video-index0;video2", "video2"},
		{"usb-Example_HDMI_Capture_A-video-index0;video0", "video0"},
		{"platform-fe00b840.mailbox-video-index0;video18", "video18"},
		{"video10;video10", "video10"},
		{"", ""},
		{"AVFoundation-style-label", ""},
		{"usb-something-with-no-node;notanode", ""},
	}
	for _, tt := range tests {
		if got := (devices.DeviceInfo{Label: tt.label}).VideoNode(); got != tt.want {
			t.Errorf("deviceInfo{Label: %q}.VideoNode() = %q, want %q", tt.label, got, tt.want)
		}
	}
}

func TestCameraName(t *testing.T) {
	tests := []struct {
		label    string
		info     driver.Info
		wantName string
		wantOK   bool
	}{
		{
			label:    "named device",
			info:     driver.Info{Name: "USB Video", Label: "USB Video;/dev/video0"},
			wantName: "USB Video",
			wantOK:   true,
		},
		{
			label:    "no name falls back to the label",
			info:     driver.Info{Label: "bcm2835-isp;platform:bcm2835-isp"},
			wantName: "bcm2835-isp;platform:bcm2835-isp",
			wantOK:   true,
		},
		{
			// A Pi node that reports neither: the label is "name;path", so
			// it comes through as a bare separator. Offering it as a
			// --device choice is no help to anyone, and the capture monitor
			// can only fail to open it.
			label:  "nothing to go on",
			info:   driver.Info{Label: ";"},
			wantOK: false,
		},
		{
			label:  "nothing at all",
			info:   driver.Info{},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			name, ok := cameraName(tt.info)
			if ok != tt.wantOK {
				t.Fatalf("cameraName(%+v) ok = %v, want %v", tt.info, ok, tt.wantOK)
			}
			if ok && name != tt.wantName {
				t.Errorf("cameraName(%+v) = %q, want %q", tt.info, name, tt.wantName)
			}
		})
	}
}

func TestCropsAgree(t *testing.T) {
	full := image.Rect(0, 0, 1920, 1080) // tolerance is 2% of width = 38px
	base := image.Rect(720, 54, 1200, 1027)

	tests := []struct {
		label string
		other image.Rectangle
		want  bool
	}{
		{"identical", base, true},
		{"a few pixels of edge noise", image.Rect(724, 56, 1197, 1025), true},
		{"nothing pending yet", image.Rectangle{}, false},
		{
			// What a target's dark screen produces: the detected content
			// shrinks to whatever is still lit.
			label: "collapsed to a fragment",
			other: image.Rect(240, 24, 400, 457),
			want:  false,
		},
		{"shifted well beyond tolerance", image.Rect(800, 54, 1280, 1027), false},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			if got := cropsAgree(base, tt.other, full); got != tt.want {
				t.Errorf("cropsAgree(%v, %v) = %v, want %v",
					base, tt.other, got, tt.want)
			}
		})
	}
}

var (
	ispDev    = devices.DeviceInfo{Name: "bcm2835-isp;platform:bcm2835-isp"}
	codecDev  = devices.DeviceInfo{Name: "bcm2835-codec-decode;platform:bcm2835-codec"}
	dongleDev = devices.DeviceInfo{Name: "UGREEN 15389: UGREEN 15389;usb-0000:01:00.0-1"}
	webcamDev = devices.DeviceInfo{Name: "FaceTime HD Camera"}
	otherDev  = devices.DeviceInfo{Name: "Some Camera"}
)

func TestChooseDevice(t *testing.T) {
	for _, tc := range []struct {
		name    string
		devices []devices.DeviceInfo
		filter  string
		want    devices.DeviceInfo // zero: an error
	}{
		{"the dongle, not the Pi's ISP listed first", []devices.DeviceInfo{ispDev, codecDev, dongleDev}, "", dongleDev},
		// A USB reset takes the dongle off the bus for a moment: then there
		// is nothing to capture, not the ISP.
		{"only on-chip blocks: none", []devices.DeviceInfo{ispDev, codecDev}, "", devices.DeviceInfo{}},
		{"a filter never picks an on-chip block", []devices.DeviceInfo{ispDev, dongleDev}, "bcm2835", devices.DeviceInfo{}},
		{"a filter picks its match", []devices.DeviceInfo{ispDev, otherDev, dongleDev}, "ugreen", dongleDev},
		{"built-in cameras are skipped", []devices.DeviceInfo{webcamDev, dongleDev}, "", dongleDev},
		{"falls back to any external device", []devices.DeviceInfo{ispDev, webcamDev, otherDev}, "", otherDev},
		{"nothing", nil, "", devices.DeviceInfo{}},
	} {
		got, err := chooseDevice(tc.devices, tc.filter)
		if tc.want == (devices.DeviceInfo{}) {
			if err == nil {
				t.Errorf("%s: chose %q, want none", tc.name, got.Name)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: chose %q (%v), want %q", tc.name, got.Name, err, tc.want.Name)
		}
	}
}

// fakeNode is a video node as the driver list gives it.
type fakeNode struct{ info driver.Info }

func (f fakeNode) Open() error              { return nil }
func (f fakeNode) Close() error             { return nil }
func (f fakeNode) Properties() []prop.Media { return nil }
func (f fakeNode) ID() string               { return f.info.Label }
func (f fakeNode) Info() driver.Info        { return f.info }
func (f fakeNode) Status() driver.State     { return driver.StateClosed }

// Whichever of a dongle's nodes was chosen, every one of them is tried,
// the chosen one first, in the same order however the list comes.
func TestCaptureNodesTriesTheWholeDevice(t *testing.T) {
	name := "UGREEN 15389: UGREEN 15389;usb-0000:01:00.0-1"
	node := func(label, name string) driver.Driver {
		return fakeNode{driver.Info{Label: label, Name: name, DeviceType: driver.Camera}}
	}
	video := node("usb-MACROSILICON_UGREEN_15389_20230204-video-index0;video0", name)
	meta := node("usb-MACROSILICON_UGREEN_15389_20230204-video-index1;video1", name)
	other := node("usb-Other-video-index0;video2", "Other: Other;usb-0000:01:00.0-2")
	mic := fakeNode{driver.Info{Label: "hw:5,0", Name: name, DeviceType: driver.Microphone}}
	labels := func(ds []driver.Driver) (out []string) {
		for _, d := range ds {
			out = append(out, d.Info().Label)
		}
		return out
	}

	for _, list := range [][]driver.Driver{{meta, other, video, mic}, {video, mic, meta, other}} {
		chose := devices.DeviceInfo{Name: name, Label: meta.Info().Label} // the metadata node
		got := labels(captureNodes(list, chose))
		want := labels([]driver.Driver{meta, video})
		if !reflect.DeepEqual(got, want) {
			t.Errorf("chose the metadata node: try %v, want %v", got, want)
		}
		// The V4L2 path tries the same nodes in the same order.
		if nodes := nativeNodes(list, chose); !reflect.DeepEqual(nodes, []string{"video1", "video0"}) {
			t.Errorf("chose the metadata node: V4L2 tries %v", nodes)
		}
		chose.Label = video.Info().Label
		got = labels(captureNodes(list, chose))
		want = labels([]driver.Driver{video, meta})
		if !reflect.DeepEqual(got, want) {
			t.Errorf("chose the video node: try %v, want %v", got, want)
		}
		if nodes := nativeNodes(list, chose); !reflect.DeepEqual(nodes, []string{"video0", "video1"}) {
			t.Errorf("chose the video node: V4L2 tries %v", nodes)
		}
	}
	if nodes := nativeNodes(nil, devices.DeviceInfo{Name: "gone", Label: "by-id-name;video7"}); !reflect.DeepEqual(nodes, []string{"video7"}) {
		t.Errorf("a device the driver list lacks: V4L2 tries %v", nodes)
	}
	if nodes := nativeNodes(nil, devices.DeviceInfo{Name: "camera", Label: "0x1234"}); nodes != nil {
		t.Errorf("a label without a node: V4L2 tries %v", nodes)
	}
}

// A dongle whose captures keep failing at once is reset after three in a
// row, then less and less often while it stays broken; a capture that runs
// clears it all.
func TestStuckWatch(t *testing.T) {
	var w stuckWatch
	t0 := time.Unix(1_000_000, 0)
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	quick := 30 * time.Millisecond

	if w.ended(quick, at(0)) || w.ended(quick, at(3*time.Second)) {
		t.Fatal("reset before three failures")
	}
	if !w.ended(quick, at(6*time.Second)) {
		t.Fatal("no reset after three quick failures")
	}
	// Still failing: the next reset waits a minute, then two, then four.
	for _, c := range []struct {
		after time.Duration
		want  bool
	}{
		{20 * time.Second, false},
		{30 * time.Second, false},
		{40 * time.Second, false},
		{66 * time.Second, true}, // a minute after the first
		{2 * time.Minute, false},
		{3 * time.Minute, false},
		{3*time.Minute + 2*time.Second, false}, // three failures, inside the gap
		{3*time.Minute + 6*time.Second, true},  // two minutes after the second
	} {
		if got := w.ended(quick, at(c.after)); got != c.want {
			t.Errorf("failure at %s: reset %v, want %v", c.after, got, c.want)
		}
	}
	if w.gap != 4*time.Minute {
		t.Errorf("gap after three resets: %s, want 4m", w.gap)
	}
	for range 10 {
		w.gap = min(2*w.gap, maxResetGap)
	}
	if w.gap != maxResetGap {
		t.Errorf("gap grows past %s", maxResetGap)
	}

	// A capture that ran clears the count and the gap.
	if w.ended(time.Hour, at(time.Hour)) || w.fails != 0 || w.gap != 0 || !w.lastReset.IsZero() {
		t.Fatalf("a working capture left %+v", w)
	}
	w.ended(quick, at(time.Hour+time.Second))
	w.ended(quick, at(time.Hour+2*time.Second))
	if !w.ended(quick, at(time.Hour+3*time.Second)) {
		t.Error("after a working capture, three failures should reset at once")
	}
	// A capture that fails to start counts as a quick failure.
	var s stuckWatch
	s.ended(0, at(0))
	s.ended(0, at(time.Second))
	if !s.ended(0, at(2*time.Second)) {
		t.Error("failed starts should count")
	}
}
