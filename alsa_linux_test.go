//go:build linux

package main

import (
	"encoding/binary"
	"os"
	"testing"
	"time"
	"unsafe"
)

// The ioctl numbers encode the struct sizes, so a layout mistake shows up
// as a different number. These are the values alsa-lib uses on 64-bit.
func TestALSAStructSizes(t *testing.T) {
	if unsafe.Sizeof(uint(0)) != 8 {
		t.Skip("checked on 64-bit only")
	}
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"sizeof(snd_pcm_hw_params)", unsafe.Sizeof(sndHwParams{}), 608},
		{"sizeof(snd_pcm_sw_params)", unsafe.Sizeof(sndSwParams{}), 136},
		{"sizeof(snd_xferi)", unsafe.Sizeof(sndXferi{}), 24},
		{"SNDRV_PCM_IOCTL_HW_PARAMS", ioctlHwParams, 0xC2604111},
		{"SNDRV_PCM_IOCTL_SW_PARAMS", ioctlSwParams, 0xC0884113},
		{"SNDRV_PCM_IOCTL_PREPARE", ioctlPrepare, 0x4140},
		{"SNDRV_PCM_IOCTL_START", ioctlStart, 0x4142},
		{"SNDRV_PCM_IOCTL_WRITEI_FRAMES", ioctlWriteI, 0x40184150},
		{"SNDRV_PCM_IOCTL_READI_FRAMES", ioctlReadI, 0x80184151},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestALSALoopback drives the real kernel through snd-aloop, where what is
// played on device 0 is captured on device 1. It checks the tone arrives,
// and that once nothing more is written the stream plays silence -- not an
// XRUN, and not the last buffer over again. That is what the gadget
// microphone relies on between bursts of speech.
//
// Needs the module: sudo modprobe snd-aloop (skipped without it), and the
// loopback card to itself -- each side opens the first free subdevice, so a
// second user of snd-aloop at the same moment can pair them wrongly.
func TestALSALoopback(t *testing.T) {
	card, err := alsaCardIndex("/proc/asound", "Loopback")
	if err != nil {
		t.Skip("no snd-aloop card (sudo modprobe snd-aloop to run this)")
	}
	want := pcmParams{rate: 48000, channels: 1, periodFrames: 480, periods: 4}
	play, err := openPCMDevice(card, 0, true, want)
	if err != nil {
		t.Fatal(err)
	}
	defer play.Close()
	rec, err := openPCMDevice(card, 1, false, want)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	if got := play.Params(); got.rate != 48000 || got.channels != 1 || got.periodFrames < 480 {
		t.Fatalf("granted %+v", got)
	}

	var captured []int16
	period := make([]byte, 480*2)
	readSome := func() {
		for {
			ready, err := rec.Wait(0)
			if err != nil {
				t.Fatal(err)
			}
			if !ready {
				return
			}
			n, err := rec.ReadFrames(period)
			if err == errPCMAgain {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < n; i++ {
				captured = append(captured, int16(binary.LittleEndian.Uint16(period[2*i:])))
			}
		}
	}

	// 300 ms of a 1 kHz tone, written as the device takes it.
	tone := pcmBytes(sine(1000, 48000, 14400, 12000))
	for off := 0; off < len(tone); {
		ready, err := play.Wait(200 * time.Millisecond)
		if err != nil || !ready {
			t.Fatalf("playback never ready: %v", err)
		}
		n, err := play.WriteFrames(tone[off:min(off+len(period), len(tone))])
		if err != nil && err != errPCMAgain {
			t.Fatal(err)
		}
		off += n * 2
		readSome()
	}
	// Then write nothing for half a second, still reading.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		readSome()
	}
	if _, err := play.WriteFrames(make([]byte, 960)); err != nil && err != errPCMAgain {
		t.Errorf("playback after the underrun: %v (the stream should still be running)", err)
	}

	if len(captured) < 24000 {
		t.Fatalf("captured only %d samples", len(captured))
	}
	start := -1
	for i, v := range captured {
		if v > 3000 || v < -3000 {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("the tone never arrived")
	}
	body := captured[start+480 : start+9600]
	if p := goertzel(body, 1000, 48000) / goertzel(body, 3000, 48000); p < 1e4 {
		t.Errorf("captured audio is not the 1 kHz tone (ratio %.0f)", p)
	}
	tail := captured[len(captured)-4800:]
	if r := rms(tail); r != 0 {
		t.Errorf("after the underrun the stream played rms %.0f, want silence", r)
	}
}

func TestALSAOpenMissingCard(t *testing.T) {
	if _, err := openPCM(99, true, pcmParams{rate: 48000, channels: 1, periodFrames: 480, periods: 4}); err == nil {
		t.Error("opened card 99")
	} else if !os.IsNotExist(err) {
		t.Logf("error: %v", err)
	}
}
