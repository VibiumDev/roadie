package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func newAudioTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s, mux := newTestServer()
	hid := &HIDController{}
	s.HID = hid
	s.Player = NewAudioPlayer(hid)
	s.SoundsDir = t.TempDir()
	return s, mux
}

// postJSON is a test helper that POSTs a JSON body with the content type the
// audio-in handlers require.
func postJSON(t *testing.T, mux http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(w, req)
	return w
}

func TestAudioPlayRejectsBadNames(t *testing.T) {
	_, mux := newAudioTestServer(t)
	for _, name := range []string{"", "..", "../server.go", "a/b.wav", `a\b.wav`, "/etc/passwd"} {
		w := postJSON(t, mux, "/audio-in/play", `{"name":`+strconv.Quote(name)+`}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("name %q: got %d, want %d", name, w.Code, http.StatusBadRequest)
		}
	}
}

func TestAudioPlayUnknownName(t *testing.T) {
	_, mux := newAudioTestServer(t)
	w := postJSON(t, mux, "/audio-in/play", `{"name":"nope.wav"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("got %d, want %d", w.Code, http.StatusNotFound)
	}
}

// Cross-origin pages can fire "simple" POSTs blind; requiring the JSON
// content type forces a CORS preflight, so these must be rejected (#12).
func TestAudioPostsRequireJSONContentType(t *testing.T) {
	_, mux := newAudioTestServer(t)
	for _, tt := range []struct{ path, body, ct string }{
		{"/audio-in/say", `{"text":"hi"}`, "text/plain"},
		{"/audio-in/play", `{"name":"a.wav"}`, ""},
		{"/audio-in/stop", "", ""},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", tt.path, strings.NewReader(tt.body))
		if tt.ct != "" {
			req.Header.Set("Content-Type", tt.ct)
		}
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s with ct %q: got %d, want %d", tt.path, tt.ct, w.Code, http.StatusUnsupportedMediaType)
		}
	}
}

// The audio-in WebSocket must refuse cross-origin browser connections (#12).
func TestAudioInRejectsCrossOrigin(t *testing.T) {
	s, mux := newAudioTestServer(t)
	_ = s
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, err := http.NewRequest("GET", srv.URL+"/audio-in", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin upgrade: got %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestAudioSoundsMissingDirIsEmpty(t *testing.T) {
	s, mux := newAudioTestServer(t)
	s.SoundsDir = s.SoundsDir + "/does-not-exist"
	code, _, body := get(t, mux, "/audio-in/sounds")
	if code != http.StatusOK {
		t.Fatalf("got %d, want %d", code, http.StatusOK)
	}
	if strings.TrimSpace(body) != `{"sounds":[]}` {
		t.Errorf("got body %q, want empty sounds list", body)
	}
}

func TestTTSArgvKeepsTextWhole(t *testing.T) {
	argv, stdin, err := ttsArgv("espeak-ng --stdout -v {{voice}} {{text}}", "-v hack; rm -rf /", "")
	if err != nil {
		t.Fatal(err)
	}
	if stdin {
		t.Error("template with {{text}} should not use stdin")
	}
	want := []string{"espeak-ng", "--stdout", "-v", "en", "-v hack; rm -rf /"}
	if len(argv) != len(want) {
		t.Fatalf("got %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, argv[i], want[i])
		}
	}
}

func TestSplitSentences(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"hello world", []string{"hello world"}},
		{"one. two! three?", []string{"one.", "two!", "three?"}},
		{"spans\nlines. and more", []string{"spans", "lines.", "and more"}},
		{"trailing period.", []string{"trailing period."}},
		{"ellipsis... then text", []string{"ellipsis...", "then text"}},
		// Line breaks are boundaries too: lyrics and poems have no sentence
		// punctuation, and each typed line should synthesize on its own.
		{"ho ro the rattlin bog\nthe bog down in the valley o\n\nand in that bog there was a tree",
			[]string{"ho ro the rattlin bog", "the bog down in the valley o", "and in that bog there was a tree"}},
		// Sentences stay whole regardless of length — no mid-phrase cuts.
		{"a b c d e f g h i j k l m n o p q r s t u v w x y z aa bb cc dd",
			[]string{"a b c d e f g h i j k l m n o p q r s t u v w x y z aa bb cc dd"}},
		{"short start. this second sentence is quite long and has well over seven words in it",
			[]string{"short start.", "this second sentence is quite long and has well over seven words in it"}},
	}
	for _, c := range cases {
		got := splitSentences(c.in)
		if len(got) != len(c.want) {
			t.Errorf("%q: got %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q: got %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestTTSArgvStdinMode(t *testing.T) {
	argv, stdin, err := ttsArgv("piper --model m.onnx --output_file -", "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if !stdin {
		t.Error("template without {{text}} should use stdin")
	}
	if len(argv) != 5 || argv[0] != "piper" {
		t.Errorf("unexpected argv %v", argv)
	}
}

func TestAudioStatusIdle(t *testing.T) {
	_, mux := newAudioTestServer(t)
	code, _, body := get(t, mux, "/audio-in/status")
	if code != http.StatusOK {
		t.Fatalf("got %d, want %d", code, http.StatusOK)
	}
	if !strings.Contains(body, `"playing":false`) {
		t.Errorf("got body %q, want playing:false", body)
	}
}
