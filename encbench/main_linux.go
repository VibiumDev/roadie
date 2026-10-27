//go:build linux && (arm64 || amd64)

// encbench measures the capture-to-H.264 pipeline on a Roadie host: it
// captures YUYV from the dongle, crops to the phone, feeds the hardware
// encoder one of several ways, and reports CPU, frame rate, drops, bitrate
// and capture-to-encoded latency. It is the R0 encoder check from
// station/docs/remote-spec.md, and its pipeline is the one Roadie's WebRTC
// encoder builds on.
//
// Modes:
//
//	direct      copy the YUYV crop into the encoder (it takes 4:2:2 natively)
//	go420       convert the crop to NV12 in Go, then encode
//	isp         let the ISP (/dev/video12) crop and convert to NV12, copying
//	            frames in and out
//	isp-dmabuf  the same with no copies: capture buffers go to the ISP and
//	            ISP buffers to the encoder as dmabufs
//	full        copy the whole YUYV frame into the encoder, no crop
//	none        capture only, to see what the dongle alone costs
//
// Stop roadie.service first; the dongle streams to one process at a time.
package main

import (
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/VibiumDev/roadie/v4l2"
	"golang.org/x/sys/unix"
)

func main() {
	video := flag.String("video", "/dev/video0", "capture node")
	encPath := flag.String("enc", "/dev/video11", "H.264 encoder node")
	ispPath := flag.String("isp", "/dev/video12", "ISP node")
	width := flag.Int("width", 1920, "capture width")
	height := flag.Int("height", 1080, "capture height")
	fps := flag.Int("fps", 30, "capture frame rate")
	cropArg := flag.String("crop", "736,54,448,972", "x,y,w,h of the phone in the frame; empty for the whole frame")
	mode := flag.String("mode", "direct", "direct, go420, isp, isp-dmabuf or full")
	dur := flag.Duration("t", 20*time.Second, "how long to run")
	bitrate := flag.Int("bitrate", 2_000_000, "encoder bitrate, bits per second")
	bufs := flag.Int("bufs", 4, "buffers per queue")
	out := flag.String("out", "", "write the Annex-B H.264 stream here")
	profile := flag.String("profile", "cb", "H.264 profile: cb (Constrained Baseline), baseline, main or high")
	vbr := flag.Bool("vbr", false, "variable bitrate instead of CBR")
	flag.Parse()

	profiles := map[string]int32{"cb": v4l2.H264ProfileConstrainedBaseline, "baseline": v4l2.H264ProfileBaseline, "main": v4l2.H264ProfileMain, "high": v4l2.H264ProfileHigh}
	prof, ok := profiles[*profile]
	if !ok {
		log.Fatalf("--profile: unknown %q", *profile)
	}

	rect := image.Rect(0, 0, *width, *height)
	if *cropArg != "" && *mode != "full" {
		var x, y, w, h int
		if _, err := fmt.Sscanf(*cropArg, "%d,%d,%d,%d", &x, &y, &w, &h); err != nil {
			log.Fatalf("--crop: %v", err)
		}
		rect = image.Rect(x, y, x+w, y+h)
	}
	if err := run(*video, *encPath, *ispPath, *width, *height, *fps, rect, *mode, *dur, *bitrate, *bufs, *out, prof, *vbr); err != nil {
		log.Fatal(err)
	}
}

type stats struct {
	captured, gaps, stalled, encoded, keyframes, idrWithoutSPS int
	bytes                                                      int
	latencies                                                  []time.Duration
	capLat                                                     []time.Duration
	convert                                                    time.Duration
	first, last                                                time.Duration
}

func run(video, encPath, ispPath string, width, height, fps int, rect image.Rectangle, mode string, dur time.Duration, bitrate, nbufs int, outPath string, profile int32, vbr bool) error {
	cap, err := v4l2.OpenCapture(video)
	if err != nil {
		return err
	}
	defer cap.Close()
	cf, err := cap.SetFormat(uint32(width), uint32(height), v4l2.PixFmtYUYV)
	if err != nil {
		return err
	}
	if cf.PixelFormat != v4l2.PixFmtYUYV || int(cf.Width) != width || int(cf.Height) != height {
		return fmt.Errorf("capture gave %s %dx%d, wanted YUYV %dx%d", v4l2.FourCC(cf.PixelFormat), cf.Width, cf.Height, width, height)
	}
	gotFPS, err := cap.SetFrameRate(uint32(fps))
	if err != nil {
		return err
	}
	fmt.Printf("capture: %s %dx%d stride %d size %d at %.0f fps\n", v4l2.FourCC(cf.PixelFormat), cf.Width, cf.Height, cf.BytesPerLine, cf.SizeImage, gotFPS)
	if mode == "none" {
		return captureOnly(cap, nbufs, dur)
	}

	enc, err := v4l2.OpenM2M(encPath)
	if err != nil {
		return err
	}
	defer enc.Close()

	var isp *v4l2.M2M
	dmabuf := mode == "isp-dmabuf"
	encW, encH := uint32(rect.Dx()), uint32(rect.Dy())
	var encIn v4l2.PixFormatMplane
	switch mode {
	case "direct", "full":
		encIn, err = enc.SetInputFormat(encW, encH, v4l2.PixFmtYUYV)
	case "go420":
		encIn, err = enc.SetInputFormat(encW, encH, v4l2.PixFmtNV12)
	case "isp", "isp-dmabuf":
		isp, err = v4l2.OpenM2M(ispPath)
		if err != nil {
			return err
		}
		defer isp.Close()
		ispIn, err := isp.SetInputFormat(uint32(width), uint32(height), v4l2.PixFmtYUYV)
		if err != nil {
			return err
		}
		got, err := isp.SetInputCrop(v4l2.Rect{Left: int32(rect.Min.X), Top: int32(rect.Min.Y), Width: encW, Height: encH})
		if err != nil {
			return fmt.Errorf("ISP crop: %w", err)
		}
		ispOut, err := isp.SetOutputFormat(encW, encH, v4l2.PixFmtNV12, 0)
		if err != nil {
			return err
		}
		fmt.Printf("isp in: %s %dx%d stride %d size %d; crop %+v; out: %s %dx%d stride %d size %d\n",
			v4l2.FourCC(ispIn.PixelFormat), ispIn.Width, ispIn.Height, ispIn.PlaneFmt[0].BytesPerLine, ispIn.PlaneFmt[0].SizeImage, got,
			v4l2.FourCC(ispOut.PixelFormat), ispOut.Width, ispOut.Height, ispOut.PlaneFmt[0].BytesPerLine, ispOut.PlaneFmt[0].SizeImage)
		if got.Left != int32(rect.Min.X) || got.Top != int32(rect.Min.Y) || got.Width != encW || got.Height != encH {
			return fmt.Errorf("ISP changed the crop to %+v", got)
		}
		encIn, err = enc.SetInputFormat(encW, encH, v4l2.PixFmtNV12)
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	if err != nil {
		return err
	}
	encOut, err := enc.SetOutputFormat(encW, encH, v4l2.PixFmtH264, 1<<20)
	if err != nil {
		return err
	}
	fmt.Printf("encoder in: %s %dx%d stride %d size %d; out: %s size %d\n",
		v4l2.FourCC(encIn.PixelFormat), encIn.Width, encIn.Height, encIn.PlaneFmt[0].BytesPerLine, encIn.PlaneFmt[0].SizeImage,
		v4l2.FourCC(encOut.PixelFormat), encOut.PlaneFmt[0].SizeImage)
	if encIn.Width != encW || encIn.Height != encH {
		return fmt.Errorf("encoder changed the size to %dx%d", encIn.Width, encIn.Height)
	}

	// The settings the spec asks for: Constrained Baseline, level 4.0 set
	// explicitly, SPS/PPS on every IDR, a 2 s GOP, CBR.
	controls := []struct {
		name string
		id   uint32
		v    int32
	}{
		{"profile", v4l2.CIDH264Profile, profile},
		{"level", v4l2.CIDH264Level, v4l2.H264Level40},
		{"bitrate_mode", v4l2.CIDBitrateMode, rateMode(vbr)},
		{"bitrate", v4l2.CIDBitrate, int32(bitrate)},
		{"gop_size", v4l2.CIDGOPSize, int32(2 * fps)},
		{"h264_i_period", v4l2.CIDH264IPeriod, int32(2 * fps)},
		{"repeat_sequence_header", v4l2.CIDRepeatSeqHeader, 1},
	}
	var set []string
	for _, c := range controls {
		if err := enc.SetControl(c.id, c.v); err != nil {
			return err
		}
		got, err := enc.GetControl(c.id)
		if err != nil {
			return err
		}
		set = append(set, fmt.Sprintf("%s=%d", c.name, got))
		if got != c.v {
			return fmt.Errorf("control %s: asked %d, got %d", c.name, c.v, got)
		}
	}
	fmt.Printf("encoder controls: %s\n", strings.Join(set, " "))

	if err := cap.Start(nbufs); err != nil {
		return err
	}
	var capFDs []int
	if dmabuf {
		if capFDs, err = cap.Export(); err != nil {
			return err
		}
	}
	if isp != nil {
		if err := isp.Start(nbufs, nbufs, dmabuf); err != nil {
			return fmt.Errorf("ISP start: %w", err)
		}
	}
	var ispOutFDs []int
	if dmabuf {
		if ispOutFDs, err = isp.ExportOutput(); err != nil {
			return err
		}
	}
	if err := enc.Start(nbufs, nbufs, dmabuf); err != nil {
		return fmt.Errorf("encoder start: %w", err)
	}

	var outFile *os.File
	if outPath != "" {
		if outFile, err = os.Create(outPath); err != nil {
			return err
		}
		defer outFile.Close()
	}

	// NV12 in these buffers: Y plane rows of BytesPerLine, then UV at the
	// aligned height. SizeImage = stride * alignedHeight * 3/2.
	uvOffset := func(f v4l2.PixFormatMplane) int {
		stride := int(f.PlaneFmt[0].BytesPerLine)
		return int(f.PlaneFmt[0].SizeImage) * 2 / 3 / stride * stride
	}
	encStride := int(encIn.PlaneFmt[0].BytesPerLine)
	encUV := uvOffset(encIn)
	var ispStride, ispUV int
	if isp != nil {
		ispStride = int(isp.OutputFormat().PlaneFmt[0].BytesPerLine)
		ispUV = uvOffset(isp.OutputFormat())
	}
	srcStride := int(cf.BytesPerLine)

	var st stats
	var ru0 unix.Rusage
	unix.Getrusage(unix.RUSAGE_SELF, &ru0)
	start := v4l2.Now()
	deadline := start + dur
	lastSeq := int64(-1)
	// Frames lent to the ISP as dmabufs, by capture buffer fd, until the ISP
	// has read them; ISP results lent to the encoder, by fd.
	lentFrames := map[int]v4l2.Frame{}
	lentResults := map[int]v4l2.Result{}

	fds := []unix.PollFd{{Fd: int32(cap.Device().FD()), Events: unix.POLLIN}, {Fd: int32(enc.Device().FD()), Events: unix.POLLIN | unix.POLLOUT}}
	if isp != nil {
		fds = append(fds, unix.PollFd{Fd: int32(isp.Device().FD()), Events: unix.POLLIN | unix.POLLOUT})
	}
	feedEncoder := func(src []byte, stride, uv int, ts time.Duration) error {
		idx, buf, ok := enc.InputBuffer()
		if !ok {
			st.stalled++
			return nil
		}
		t0 := v4l2.Now()
		switch mode {
		case "direct":
			v4l2.YUYVCrop(buf, encStride, src, stride, rect)
		case "full":
			copy(buf, src)
		case "go420":
			v4l2.YUYVToNV12(buf, encStride, encUV, src, stride, rect)
		case "isp":
			v4l2.NV12Copy(buf, encStride, encUV, src, stride, uv, int(encW), int(encH))
		}
		st.convert += v4l2.Now() - t0
		return enc.QueueInput(idx, int(encIn.PlaneFmt[0].SizeImage), ts)
	}
	for v4l2.Now() < deadline {
		for i := range fds {
			fds[i].Revents = 0
		}
		n, err := unix.Poll(fds, 100)
		if err != nil && err != unix.EINTR {
			return fmt.Errorf("poll: %w", err)
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			frame, err := cap.Dequeue()
			if err != nil && err != v4l2.ErrAgain {
				return err
			}
			if err == nil {
				st.captured++
				if lastSeq >= 0 && int64(frame.Sequence) > lastSeq+1 {
					st.gaps += int(int64(frame.Sequence) - lastSeq - 1)
				}
				lastSeq = int64(frame.Sequence)
				if st.first == 0 {
					st.first = v4l2.Now()
				}
				st.capLat = append(st.capLat, v4l2.Now()-frame.Timestamp)
				switch {
				case isp == nil:
					if err := feedEncoder(frame.Data, srcStride, 0, frame.Timestamp); err != nil {
						return err
					}
					if err := cap.Release(frame); err != nil {
						return err
					}
				case dmabuf:
					idx, _, ok := isp.InputBuffer()
					if !ok {
						st.stalled++
						cap.Release(frame)
						break
					}
					fd := capFDs[frame.Index]
					if err := isp.QueueInputFD(idx, fd, len(frame.Data), frame.Timestamp); err != nil {
						return fmt.Errorf("ISP dmabuf input: %w", err)
					}
					lentFrames[fd] = frame
				default:
					idx, buf, ok := isp.InputBuffer()
					if !ok {
						st.stalled++
						cap.Release(frame)
						break
					}
					t0 := v4l2.Now()
					copy(buf, frame.Data)
					st.convert += v4l2.Now() - t0
					if err := isp.QueueInput(idx, len(frame.Data), frame.Timestamp); err != nil {
						return err
					}
					if err := cap.Release(frame); err != nil {
						return err
					}
				}
			}
		}
		if isp != nil && fds[2].Revents&unix.POLLOUT != 0 {
			returned, err := isp.ReapInput()
			if err != nil {
				return err
			}
			for _, fd := range returned {
				if f, ok := lentFrames[fd]; ok {
					delete(lentFrames, fd)
					if err := cap.Release(f); err != nil {
						return err
					}
				}
			}
		}
		if isp != nil && fds[2].Revents&unix.POLLIN != 0 {
			res, err := isp.Dequeue()
			if err != nil && err != v4l2.ErrAgain {
				return err
			}
			if err == nil {
				if dmabuf {
					idx, _, ok := enc.InputBuffer()
					if !ok {
						st.stalled++
						isp.Release(res)
					} else {
						fd := ispOutFDs[res.Index]
						if err := enc.QueueInputFD(idx, fd, int(isp.OutputFormat().PlaneFmt[0].SizeImage), res.Timestamp); err != nil {
							return fmt.Errorf("encoder dmabuf input: %w", err)
						}
						lentResults[fd] = res
					}
				} else {
					if err := feedEncoder(res.Data, ispStride, ispUV, res.Timestamp); err != nil {
						return err
					}
					if err := isp.Release(res); err != nil {
						return err
					}
				}
			}
		}
		if fds[1].Revents&unix.POLLOUT != 0 {
			returned, err := enc.ReapInput()
			if err != nil {
				return err
			}
			for _, fd := range returned {
				if r, ok := lentResults[fd]; ok {
					delete(lentResults, fd)
					if err := isp.Release(r); err != nil {
						return err
					}
				}
			}
		}
		if fds[1].Revents&unix.POLLIN != 0 {
			res, err := enc.Dequeue()
			if err != nil && err != v4l2.ErrAgain {
				return err
			}
			if err == nil {
				st.encoded++
				st.bytes += len(res.Data)
				st.last = v4l2.Now()
				st.latencies = append(st.latencies, v4l2.Now()-res.Timestamp)
				if res.Flags&v4l2.BufFlagKeyframe != 0 {
					st.keyframes++
					sps, pps, _ := nalTypes(res.Data)
					if !sps || !pps {
						st.idrWithoutSPS++
					}
				}
				if outFile != nil {
					outFile.Write(res.Data)
				}
				if err := enc.Release(res); err != nil {
					return err
				}
			}
		}
	}
	var ru1 unix.Rusage
	unix.Getrusage(unix.RUSAGE_SELF, &ru1)
	wall := v4l2.Now() - start
	cpu := tv(ru1.Utime) - tv(ru0.Utime) + tv(ru1.Stime) - tv(ru0.Stime)

	pct := percentiles(st.latencies)
	capPct := percentiles(st.capLat)
	span := st.last - st.first
	fmt.Printf("\nmode=%s crop=%dx%d ran %.1fs\n", mode, rect.Dx(), rect.Dy(), wall.Seconds())
	fmt.Printf("captured %d frames (%.1f fps), %d dropped by the driver, %d stalled (no free buffer)\n",
		st.captured, float64(st.captured)/wall.Seconds(), st.gaps, st.stalled)
	fmt.Printf("encoded %d frames (%.1f fps), %d keyframes, %d IDR without SPS+PPS, %.2f Mbps\n",
		st.encoded, float64(st.encoded)/span.Seconds(), st.keyframes, st.idrWithoutSPS, float64(st.bytes)*8/span.Seconds()/1e6)
	fmt.Printf("latency frame stamp->dequeued: p50 %v p95 %v; stamp->encoded: p50 %v p95 %v max %v\n",
		capPct(0.5), capPct(0.95), pct(0.5), pct(0.95), pct(1))
	perFrame := time.Duration(0)
	if st.captured > 0 {
		perFrame = (st.convert / time.Duration(st.captured)).Round(10 * time.Microsecond)
	}
	fmt.Printf("process CPU: %.2f cores (%.0f%% of one), copy/convert %v per frame\n",
		cpu.Seconds()/wall.Seconds(), 100*cpu.Seconds()/wall.Seconds(), perFrame)
	return nil
}

func rateMode(vbr bool) int32 {
	if vbr {
		return v4l2.BitrateModeVBR
	}
	return v4l2.BitrateModeCBR
}

// percentiles sorts the samples and returns a lookup by fraction.
func percentiles(d []time.Duration) func(p float64) time.Duration {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return func(p float64) time.Duration {
		if len(d) == 0 {
			return 0
		}
		return d[int(float64(len(d)-1)*p)].Round(100 * time.Microsecond)
	}
}

// captureOnly streams the dongle and touches nothing else: the floor every
// other mode builds on.
func captureOnly(cap *v4l2.Capture, nbufs int, dur time.Duration) error {
	if err := cap.Start(nbufs); err != nil {
		return err
	}
	var ru0, ru1 unix.Rusage
	unix.Getrusage(unix.RUSAGE_SELF, &ru0)
	start := v4l2.Now()
	var n, gaps int
	lastSeq := int64(-1)
	var lat []time.Duration
	for v4l2.Now() < start+dur {
		f, err := cap.Read(100 * time.Millisecond)
		if err == v4l2.ErrAgain {
			continue
		}
		if err != nil {
			return err
		}
		n++
		if lastSeq >= 0 && int64(f.Sequence) > lastSeq+1 {
			gaps += int(int64(f.Sequence) - lastSeq - 1)
		}
		lastSeq = int64(f.Sequence)
		lat = append(lat, v4l2.Now()-f.Timestamp)
		if err := cap.Release(f); err != nil {
			return err
		}
	}
	unix.Getrusage(unix.RUSAGE_SELF, &ru1)
	wall := v4l2.Now() - start
	cpu := tv(ru1.Utime) - tv(ru0.Utime) + tv(ru1.Stime) - tv(ru0.Stime)
	p := percentiles(lat)
	fmt.Printf("\nmode=none ran %.1fs\ncaptured %d frames (%.1f fps), %d dropped by the driver\n", wall.Seconds(), n, float64(n)/wall.Seconds(), gaps)
	fmt.Printf("latency frame stamp->dequeued: p50 %v p95 %v\n", p(0.5), p(0.95))
	fmt.Printf("process CPU: %.2f cores (%.0f%% of one)\n", cpu.Seconds()/wall.Seconds(), 100*cpu.Seconds()/wall.Seconds())
	return nil
}

func tv(t unix.Timeval) time.Duration {
	return time.Duration(t.Sec)*time.Second + time.Duration(t.Usec)*time.Microsecond
}

// nalTypes scans an Annex-B buffer and reports whether it holds an SPS, a
// PPS and an IDR slice.
func nalTypes(b []byte) (sps, pps, idr bool) {
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			switch b[i+3] & 0x1f {
			case 7:
				sps = true
			case 8:
				pps = true
			case 5:
				idr = true
			}
			i += 3
		}
	}
	return
}
