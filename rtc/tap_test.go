package rtc

import (
	"testing"
	"time"

	"github.com/VibiumDev/roadie/encoder"
)

func TestTapRunsTheEncoderWithNoViewer(t *testing.T) {
	src := &fakeSource{added: make(chan struct{}), gone: make(chan struct{})}
	enc := &fakeEncoder{samples: make(chan encoder.Sample, 4), closed: make(chan struct{})}
	b, err := New(src, Options{
		Logf:        t.Logf,
		OpenEncoder: func(encoder.Config) (encoder.Encoder, string, error) { return enc, "fake", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	type got struct {
		key    bool
		sps    bool
		length int
	}
	units := make(chan got, 256)
	stop := b.Tap(func(data []byte, key bool, _ time.Duration) {
		sps, _ := encoder.ParameterSets(data)
		select {
		case units <- got{key, sps != nil, len(data)}:
		default:
		}
	})
	select {
	case <-src.added:
	case <-time.After(2 * time.Second):
		t.Fatal("the tap did not start the capture feed")
	}
	deadline := time.After(3 * time.Second)
	for n := 0; n < 5; n++ {
		select {
		case u := <-units:
			if n == 0 && (!u.key || !u.sps) {
				t.Errorf("the first unit: keyframe %v, parameter sets %v", u.key, u.sps)
			}
			if u.length == 0 {
				t.Error("an empty unit")
			}
		case <-deadline:
			t.Fatalf("%d units in 3 s", n)
		}
	}
	if st := b.Status(); st.Sessions != 0 || st.Encoder != "fake" {
		t.Errorf("status %+v; want the encoder running with no session", st)
	}

	stop()
	stop()
	for _, c := range []chan struct{}{src.gone, enc.closed} {
		select {
		case <-c:
		case <-time.After(2 * time.Second):
			t.Fatal("stopping the last tap left the encoder running")
		}
	}
}

func TestAViewerSaysItRecords(t *testing.T) {
	b, err := New(&fakeSource{added: make(chan struct{}), gone: make(chan struct{})}, Options{
		Logf:        t.Logf,
		OpenEncoder: func(encoder.Config) (encoder.Encoder, string, error) { return nil, "", encoder.ErrUnavailable },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	told := 0
	b.OnViewerRecording(func() { told++ })

	gone := &Session{b: b}
	gone.onControl([]byte(`{"type":"recording"}`))
	if told != 0 || b.ViewerRecording() {
		t.Fatal("a viewer that is not connected was heard")
	}

	s := &Session{b: b}
	b.mu.Lock()
	b.sessions[s] = struct{}{}
	b.mu.Unlock()
	if b.ViewerRecording() {
		t.Fatal("a viewer that has said nothing counts as recording")
	}
	s.onControl([]byte(`{"type":"recording"}`))
	if told != 1 || !b.ViewerRecording() {
		t.Fatalf("told %d times, recording %v", told, b.ViewerRecording())
	}
	b.mu.Lock()
	delete(b.sessions, s)
	b.mu.Unlock()
	if b.ViewerRecording() {
		t.Fatal("a viewer that left still counts as recording")
	}
}
