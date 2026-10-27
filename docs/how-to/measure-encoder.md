# Measure the Encoder

`encbench` streams the capture dongle into the host's hardware H.264 encoder
the way Roadie's WebRTC path does, and prints what it costs: CPU, frame
rate, dropped frames, bitrate and the delay from a frame's capture to its
encoded bytes. It is the "R0 encoder check" from the
[Roadie Remote spec](https://github.com/VibiumDev/station/blob/main/docs/remote-spec.md)
and the quickest way to see whether a new dongle, kernel or host can feed
the encoder at 1080p30.

It needs 64-bit Linux and V4L2 devices: a UVC capture node and a
memory-to-memory encoder. On a Raspberry Pi 4 those are `/dev/video0` and
`/dev/video11`. The code is pure Go (`v4l2/`), so it cross-compiles.

## Run it

The dongle streams to one process at a time, so stop Roadie first:

```bash
make encbench
sudo systemctl stop roadie
./encbench/encbench -mode direct -t 20s
sudo systemctl start roadie
```

`--crop x,y,w,h` is the phone's rectangle in the 1080p frame; the default
is the iPhone on the reference Pi. Roadie's saved crop is in
`~/.config/roadie/crop-<name>.json`. `--out file.h264` keeps the Annex-B
stream for a look with `ffprobe` or `ffplay`.

| Mode | What it does |
|---|---|
| `direct` | Copies the YUYV crop into the encoder. The Pi 4's encoder takes 4:2:2 natively, so nothing converts. |
| `go420` | Converts the crop to NV12 in Go (every other chroma row dropped), then encodes. |
| `isp` | Hands the frame to the codec driver's ISP role (`/dev/video12`) to crop and convert. It refuses a crop offset, so this mode stops with an error and stays as a record. |
| `full` | The whole 1080p frame, no crop. |
| `none` | Capture only: the floor every other mode builds on. |

`--vbr` picks variable bitrate; the default is CBR. `--profile high` and
`--bitrate` are there for comparisons.

## The whole path, from the outside

`rtcprobe` is a headless viewer: it connects to a running Roadie the way
`/view?transport=webrtc` does, receives the H.264 track and reports frame
rate, bitrate, keyframes and whether SPS arrived. It exits non-zero when
no video comes, so a script can check the remote video path.

```bash
make rtcprobe
./rtcprobe/rtcprobe -url http://roadie.local:8080 -token "$(roadie token)" -t 20s
```

## What the numbers mean

- **captured / dropped / stalled**: frames the dongle delivered, frames the
  driver skipped (gaps in its sequence numbers), and frames thrown away
  because the encoder still held every input buffer. Stalls mean the encoder
  cannot keep up.
- **stamp to dequeued**: from the capture driver's timestamp, which marks
  the start of a frame, to the frame being complete in memory. For a UVC
  dongle this is one frame period: about 32 ms at 30 fps.
- **stamp to encoded**: the same start to the H.264 bytes being ready. The
  difference between the two is the encoder's own latency.
- **process CPU**: the whole process, kernel time included, as a fraction of
  one core.

## Results on the reference Pi

Raspberry Pi 4, Linux 6.18, UGREEN 15389 on USB 3, YUYV 1920x1080 at
30 fps, 2 Mbps, 20 s runs on Oct 26, 2026. The crop is an iPhone in
portrait, 448x972.

| Mode | Encoded | CPU | Copy or convert | Stamp to encoded, p50 |
|---|---|---|---|---|
| `none` (capture only) | | 1% | | 32 ms to dequeued |
| `direct`, VBR | 30 fps | 6% | 0.7 ms | 37 ms |
| `direct`, CBR | 30 fps | 6% | 0.7 ms | 53 ms |
| `go420`, CBR | 30 fps | 13% | 3.2 ms | 55 ms |
| `direct` landscape 1728x798, VBR | 30 fps | 8% | 1.6 ms | 46 ms |
| `direct` landscape 1728x798, CBR | 17 fps, stalls | 6% | 1.1 ms | 360 ms |
| `full` 1920x1080, VBR | 30 fps | 11% | 2.2 ms | 52 ms |
| `full` 1920x1080, CBR | 11 fps, stalls | 5% | 1.1 ms | 540 ms |

No run dropped a frame at the driver. Every IDR carried SPS and PPS; the
SPS says Constrained Baseline (profile 66, constraint flags 0xc0), level
4.0.

For comparison, `ffmpeg` on the same frames: YUYV straight into
`h264_v4l2m2m` costs 0.13 cores; converting to 4:2:0 with swscale first
costs 1.1 cores; decoding MJPEG from the dongle instead costs 1.5 cores.

What this settles:

- The encoder takes YUYV, so Roadie converts nothing on a Pi 4; it copies
  the crop (0.7 ms a frame) and encodes.
- CBR mode in `bcm2835-codec` stalls the encoder above roughly 1.4
  megapixels whatever the bitrate, and adds about 15 ms of latency and
  undershoots its target at smaller sizes. Roadie uses VBR with the target
  bitrate set from congestion feedback.
- The ISP is not worth its complexity here: the direct path is within 5% of
  a core of capturing alone, and the codec's ISP role cannot crop at an
  offset. The separate `bcm2835-isp` driver (`/dev/video13` and
  `/dev/video14`) can, but there is nothing left to save.
- A frame is complete 32 ms after its capture timestamp and encoded 5 ms
  after that, so the Pi contributes about 40 ms to glass-to-glass latency
  before the network.
