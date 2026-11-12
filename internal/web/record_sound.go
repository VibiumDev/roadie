package web

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"time"

	"github.com/VibiumDev/roadie/aac"
	"github.com/VibiumDev/roadie/audio/pcm"
	"github.com/VibiumDev/roadie/internal/audio"
)

// A hold's sound, as 16 kHz mono: what the device played while the hold
// was recorded, and what was sent to its microphone, whether a viewer
// spoke or a client played a clip. Each is kept twice in the recording: as
// a WAV under audio/, which the Record Player plays, and mixed into the
// sound track of the video, so the video file plays with its sound
// anywhere.

// SoundSource is the device's sound. audio.AudioBroadcaster is one.
type SoundSource interface {
	Subscribe() (<-chan []byte, func())
	Params() audio.AudioParams
	IsActive() bool
}

const (
	soundRate = aac.Rate
	// soundJump is how far behind the clock the samples may fall before
	// the difference is taken for a pause and filled with silence. A
	// source that has nothing to play sends nothing.
	soundJump = 150 * time.Millisecond
	// soundMax is the most sound one recording keeps: 25 minutes.
	soundMax = 48 << 20 / 2
	// soundFloor is the loudest sample of a recording in which nothing
	// was heard, about -72 dBFS.
	soundFloor = 8
)

// holdSound is the sound of one hold being recorded.
type holdSound struct {
	path    string // raw samples, 16-bit little-endian
	started time.Time
	now     func() time.Time
	logf    func(format string, args ...any)
	quit    chan struct{}
	done    chan struct{}
	pushed  chan saidPiece // for sound given with push

	// The writer's own until done closes.
	f       *os.File
	w       *bufio.Writer
	samples int64
	peak    int
	err     error
}

func startHoldSound(src SoundSource, path string, started time.Time, now func() time.Time, logf func(string, ...any)) *holdSound {
	s := &holdSound{path: path, started: started, now: now, logf: logf, quit: make(chan struct{}), done: make(chan struct{})}
	go s.run(src)
	return s
}

// saidQueue is how many pieces of sound may wait for the disk. Past it
// they are dropped, never the microphone held up.
const saidQueue = 256

// startSaidSound keeps the sound it is given with push: what goes to the
// device's microphone.
func startSaidSound(path string, started time.Time, now func() time.Time, logf func(string, ...any)) *holdSound {
	s := &holdSound{path: path, started: started, now: now, logf: logf, quit: make(chan struct{}), done: make(chan struct{}),
		pushed: make(chan saidPiece, saidQueue)}
	go s.runPushed()
	return s
}

type saidPiece struct {
	samples []int16
	at      time.Time
}

// push takes 16 kHz mono samples, 16-bit little-endian, as they are sent.
func (s *holdSound) push(raw []byte) {
	samples := make([]int16, len(raw)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	select {
	case s.pushed <- saidPiece{samples, s.now()}:
	default:
	}
}

func (s *holdSound) runPushed() {
	defer close(s.done)
	if !s.open() {
		return
	}
	defer s.close()
	for {
		select {
		case <-s.quit:
			for {
				select {
				case p := <-s.pushed:
					s.add(p.samples, p.at)
				default:
					return
				}
			}
		case p := <-s.pushed:
			s.add(p.samples, p.at)
		}
	}
}

// open makes the spool file.
func (s *holdSound) open() bool {
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		s.err = err
		s.logf("record: sound: %v", err)
		return false
	}
	s.f, s.w = f, bufio.NewWriterSize(f, 64<<10)
	return true
}

// close pads the sound to now and puts it on disk.
func (s *holdSound) close() {
	s.pad(s.at(s.now()) - s.samples)
	if err := s.w.Flush(); err != nil && s.err == nil {
		s.err = err
	}
}

func (s *holdSound) run(src SoundSource) {
	defer close(s.done)
	if !s.open() {
		return
	}
	defer s.close()
	var scratch []int16
	for {
		params := src.Params()
		if !src.IsActive() || params.SampleRate <= 0 || params.Channels <= 0 {
			select {
			case <-s.quit:
				return
			case <-time.After(500 * time.Millisecond):
				continue
			}
		}
		ch, unsub := src.Subscribe()
		down := pcm.NewDownsampler(params.SampleRate, soundRate, params.Channels)
		for open := true; open; {
			select {
			case <-s.quit:
				unsub()
				return
			case chunk, ok := <-ch:
				if !ok {
					// The source changed its format and dropped its listeners.
					open = false
					break
				}
				scratch = down.Process(scratch[:0], pcm.Float32LE(chunk))
				s.add(scratch, s.now())
			}
		}
		unsub()
	}
}

// at is the sample that belongs to a moment.
func (s *holdSound) at(t time.Time) int64 {
	return int64(t.Sub(s.started)) * soundRate / int64(time.Second)
}

// add appends samples whose last one was heard at arrived.
func (s *holdSound) add(samples []int16, arrived time.Time) {
	if first := s.at(arrived) - int64(len(samples)); first-s.samples > s.at(s.started.Add(soundJump)) {
		s.pad(first - s.samples)
	}
	if room := soundMax - s.samples; int64(len(samples)) > room {
		samples = samples[:max(room, 0)]
	}
	for _, v := range samples {
		if v < 0 {
			v = -v
		}
		s.peak = max(s.peak, int(v))
	}
	if _, err := s.w.Write(pcm.Int16LE(nil, samples)); err != nil && s.err == nil {
		s.err = err
	}
	s.samples += int64(len(samples))
}

// pad appends silence.
func (s *holdSound) pad(n int64) {
	n = min(n, soundMax-s.samples)
	if n <= 0 {
		return
	}
	zeros := make([]byte, 32<<10)
	for left := n * 2; left > 0; {
		k := min(left, int64(len(zeros)))
		if _, err := s.w.Write(zeros[:k]); err != nil && s.err == nil {
			s.err = err
		}
		left -= k
	}
	s.samples += n
}

// finish ends the capture and says whether anything was heard. The samples
// stay in the file until remove.
func (s *holdSound) finish() (heard bool) {
	close(s.quit)
	<-s.done
	if s.err != nil || s.f == nil {
		return false
	}
	return s.samples > 0 && s.peak > soundFloor
}

// drop ends the capture and keeps none of it.
func (s *holdSound) drop() {
	close(s.quit)
	<-s.done
	s.remove()
}

// between gives the samples from one moment to another.
func (s *holdSound) between(from, to time.Time) []int16 {
	first := min(max(s.at(from), 0), s.samples)
	last := min(max(s.at(to), first), s.samples)
	raw := make([]byte, (last-first)*2)
	if _, err := s.f.ReadAt(raw, first*2); err != nil && err != io.EOF {
		return nil
	}
	out := make([]int16, len(raw)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	return out
}

// mixSound gives the sound of several recordings between two moments as
// one. Nil recordings count for nothing; with none it is nil.
func mixSound(sounds ...*holdSound) func(from, to time.Time) []int16 {
	var live []*holdSound
	for _, s := range sounds {
		if s != nil {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return nil
	}
	return func(from, to time.Time) []int16 {
		out := live[0].between(from, to)
		for _, s := range live[1:] {
			more := s.between(from, to)
			if len(more) > len(out) {
				out, more = more, out
			}
			for i, v := range more {
				out[i] = int16(min(max(int32(out[i])+int32(v), -32768), 32767))
			}
		}
		return out
	}
}

// writeWAV writes everything recorded as a WAV file.
func (s *holdSound) writeWAV(w io.Writer) error {
	data := uint32(s.samples * 2)
	head := make([]byte, 0, 44)
	head = append(head, "RIFF"...)
	head = binary.LittleEndian.AppendUint32(head, 36+data)
	head = append(head, "WAVEfmt "...)
	head = binary.LittleEndian.AppendUint32(head, 16)
	head = binary.LittleEndian.AppendUint16(head, 1)
	head = binary.LittleEndian.AppendUint16(head, 1)
	head = binary.LittleEndian.AppendUint32(head, soundRate)
	head = binary.LittleEndian.AppendUint32(head, soundRate*2)
	head = binary.LittleEndian.AppendUint16(head, 2)
	head = binary.LittleEndian.AppendUint16(head, 16)
	head = append(head, "data"...)
	head = binary.LittleEndian.AppendUint32(head, data)
	if _, err := w.Write(head); err != nil {
		return err
	}
	_, err := io.Copy(w, io.NewSectionReader(s.f, 0, s.samples*2))
	return err
}

// duration is how long the recorded sound runs.
func (s *holdSound) duration() time.Duration {
	return time.Duration(s.samples) * time.Second / soundRate
}

// remove drops the spooled samples.
func (s *holdSound) remove() {
	if s.f != nil {
		s.f.Close()
	}
	os.Remove(s.path)
}
