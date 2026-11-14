# How many frames a second Roadie sends

Roadie captures the device's screen at one rate, 30 frames a second unless
`--fps` or a setting says otherwise. Over WebRTC it usually sends every one
of those frames. This page explains the three cases where it does something
else, and the measurements behind them.

The numbers were measured on a Raspberry Pi 4 with its hardware H.264
encoder and a phone-sized picture, 446 by 972 pixels: a page of Settings
text, scrolled at a steady speed. Quality is PSNR of the brightness channel
against the picture that went in, after the encoder had settled. Higher is
sharper. Above about 40 dB the text looks clean, and under about 30 dB it is
hard to read.

## The frame rate setting

The frame rate in Settings and in the viewer's menu is **Auto** unless
someone picks a number.

- **Auto** captures at Roadie's own rate and lets Roadie hold frames back in
  the cases below.
- **A number** (10, 15, 20 or 30) asks the capture device for that many
  frames a second and sends them all.

The setting belongs to the Roadie, not to a browser. It lasts until Roadie
restarts, and then `--fps` applies again.

## The encoder is paid by the frame

The encoder is opened for 30 frames a second, and it spends its bitrate by
the frame. Given 15 frames a second it sends half the bits, and no frame
comes out sharper.

So Roadie scales the bitrate it asks for by the rate frames actually arrive
at. At 15 frames a second it asks for twice the link's bitrate, and at 10 for
three times. The stream then adds up to what the link carries.

This matters most for a capture set to a low rate. With the capture at 10
frames a second, a brisk scroll and a link capped at 900 kbps, before and
after the changes on this page:

| | Frames a second | Sent |
|---|---|---|
| Before | 8.1, falling to 5 | 0.33 Mbps |
| Now | 10.1 | 0.90 Mbps |

## A thin link: 20 frames a second

With the correction above, fewer frames means more bits for each. Whether
that makes a better picture depends on how fast the screen moves.

**Slow scroll, 240 pixels a second**

| Link | 30 fps | 20 fps | 15 fps | 10 fps |
|---|---|---|---|---|
| 300 kbps | 37.1 | 41.5 | 42.5 | 47.6 |
| 450 kbps | 47.1 | 48.1 | 48.1 | 48.2 |
| 600 kbps | 48.3 | 48.4 | 48.4 | 47.9 |

**Brisk scroll, 700 pixels a second**

| Link | 30 fps | 20 fps | 15 fps | 10 fps |
|---|---|---|---|---|
| 300 kbps | 30.9 | 35.1 | 36.4 | 23.2 |
| 450 kbps | 39.0 | 42.3 | 43.8 | 28.1 |
| 600 kbps | 44.6 | 47.7 | 48.2 | 30.8 |

**Fast flick, 1500 pixels a second**

| Link | 30 fps | 20 fps | 15 fps | 10 fps |
|---|---|---|---|---|
| 300 kbps | 17.5 | 23.0 | 22.9 | 23.1 |
| 450 kbps | 30.8 | 23.1 | 23.0 | |
| 600 kbps | 35.9 | 23.1 | 24.9 | 27.9 |
| 900 kbps | 43.0 | 26.2 | 28.1 | 31.6 |
| 1500 kbps | 48.3 | 30.6 | 33.2 | 37.6 |

What the tables say:

- **From about 450 kbps up, 30 frames a second is the best choice or as good
  as any.** A fast flick is far worse at a lower rate. The picture moves
  further between frames than the encoder can follow, so it has to draw each
  frame from scratch.
- **At 300 kbps, 20 frames a second is better for every kind of motion**, by
  4 to 6 dB.
- **Between the two it is mixed.** At 450 kbps a brisk scroll is 3 dB better
  at 20 frames a second and a fast flick is 8 dB worse.
- **10 frames a second is never the answer to a thin link.** A brisk scroll
  falls apart there.

So in Auto, Roadie sends 20 frames a second only where that was as good or
better for every motion measured: while the link carries less than 0.81 bits
a second for each pixel of the picture. For a phone that is about 350 kbps.
It goes back to every frame once the link has carried 15% more than that for
three seconds.

Two things keep a good link from being mistaken for a thin one. The estimate
has to stay that low for six seconds, and the stream has to be using most of
it. A still screen sends very little, and the estimate then drops to what is
sent at the first hiccup and climbs back within a few seconds.

The line is set by the picture's size. A full 1080p picture has almost five
times the pixels, so the same rule puts its line near 1.7 Mbps. That was not
measured, only scaled.

## The link at its lowest: 15 frames a second, half the bits

The link's estimate cannot go under 300 kbps. When it has sat there for four
seconds with the stream using most of it, the link may carry less than that.
Roadie then sends 15 frames a second and asks the encoder for half the
bitrate, so about 150 kbps goes out. This applies with a number chosen as
well as in Auto.

## A host under strain: 20, then 15, then 10

A Raspberry Pi 4 starts slowing its CPU at 80 °C. In Auto, Roadie sends 20
frames a second once the host is at 80 °C or more, or once the encoder is
skipping frames because it is behind.

- **Heat** is slow to answer, so the next steps wait: 15 frames a second if
  it is still that hot a minute later, and 10 a minute after that.
- **An encoder that is behind** shows at once whether fewer frames help, so
  those steps come five seconds apart.
- **Each step back up** takes thirty seconds under 77 °C with no frames
  skipped.

The frames that are sent get the bits of the ones that are not, so the
picture stays sharp while the host recovers. A host with no temperature to
read is judged by skipped frames alone.

Whether a hot Pi 4 keeps up with 30 frames a second without this was not
measured. The rule is a precaution, and it costs frames while it applies.

## Loss that was not there

Under about 16 frames a second, frames are more than 64 ms apart. The
browser's reports of what arrived then use a different encoding, with
padding at the end. The congestion control library Roadie uses
(`pion/interceptor`, up to v0.1.42) counted that padding as lost packets.

The effect was a link estimate that collapsed for no reason whenever frames
were sparse. Three runs of 18 seconds each, at 15 frames a second on the Pi's
own loopback, where nothing is lost:

| | Loss reported | Link estimate at the end |
|---|---|---|
| v0.1.42 | 16% to 18% | 0.3 Mbps, the lowest there is |
| v0.1.51 | 0% | 4 Mbps |

Roadie now uses v0.1.51, which reads only as many entries as the report says
it holds.

## What is reported

`/health` and the viewer's stats carry `fps_limit` and `limited_by` under
`webrtc` while frames are held back. `limited_by` is `link`, `host` or
`floor`. The log has a line each time the rate changes.
