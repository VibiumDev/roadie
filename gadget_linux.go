//go:build linux

package main

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// hidgFile is an open /dev/hidgN.
type hidgFile struct {
	fd   int
	path string
	ino  uint64
}

// openHIDGadget opens a gadget HID node for non-blocking report writes. The
// inode is kept so a node that udev has since replaced -- the gadget was
// rebuilt -- can be told apart from the one we hold.
func openHIDGadget(path string) (reportDevice, error) {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	return &hidgFile{fd: fd, path: path, ino: st.Ino}, nil
}

// WriteReport queues one report. f_hid holds a single report per device:
// EAGAIN means the phone has not yet collected the previous one, so wait for
// the node to become writable; ESHUTDOWN means no host has configured us.
func (h *hidgFile) WriteReport(report []byte, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	polled := false
	for {
		n, err := unix.Write(h.fd, report)
		switch err {
		case nil:
			if n != len(report) {
				return fmt.Errorf("short write: %d of %d bytes", n, len(report))
			}
			return nil
		case unix.EINTR:
			continue
		case unix.ESHUTDOWN:
			return errUSBNotConfigured
		case unix.EAGAIN:
			left := time.Until(deadline)
			if left <= 0 {
				return errHostNotReading
			}
			if polled {
				// Writable, yet still refused: the controller will not
				// queue while the bus is suspended, and f_hid then reports
				// writable at once. Back off rather than spin.
				time.Sleep(min(5*time.Millisecond, left))
			}
			ms := max(int(time.Until(deadline)/time.Millisecond), 1)
			fds := []unix.PollFd{{Fd: int32(h.fd), Events: unix.POLLOUT}}
			if _, perr := unix.Poll(fds, ms); perr != nil && perr != unix.EINTR {
				return perr
			}
			polled = true
		default:
			return err
		}
	}
}

func (h *hidgFile) Stale() bool {
	var st unix.Stat_t
	if err := unix.Stat(h.path, &st); err != nil {
		return true
	}
	return st.Ino != h.ino
}

func (h *hidgFile) Close() error { return unix.Close(h.fd) }
