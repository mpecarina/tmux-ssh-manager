//go:build !windows
// +build !windows

package main

import (
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// flushTTYInput best-effort flushes any pending unread input bytes queued for the
// controlling terminal (e.g. terminal integration replies like OSC/DSR that can
// otherwise be consumed as "typed characters" by the next interactive program).
//
// It is intentionally conservative and never returns an error; callers should
// treat this as an opportunistic hygiene step.
//
// NOTE:
//   - This should typically be called right before starting an interactive PTY
//     session (ssh/fzf/etc), especially when not running under tmux.
//   - On macOS/Linux, we implement "tcflush(TCIFLUSH)" via ioctl(TCFLSH).
//   - Some terminal integrations can emit replies *right after* a flush; we also
//     perform a short non-blocking drain window to catch late arrivals.
//   - If /dev/tty isn't available (non-interactive), this becomes a no-op.
func flushTTYInput() {
	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		return
	}
	defer func() { _ = tty.Close() }()

	fd := int(tty.Fd())
	if fd < 0 {
		return
	}

	// Implement tcflush(fd, TCIFLUSH) via ioctl(TCFLSH).
	//
	// This avoids relying on x/sys/unix exposing Tcflush across all platforms.
	// Values:
	// - Linux:  TCFLSH = 0x540B (asm-generic/ioctls.h)
	// - Darwin: TCFLSH = 0x540B (sys/ttycom.h)
	//
	// Arg:
	// - TCIFLUSH = 0 (flush unread input)
	const TCFLSH = 0x540B
	_, _, _ = unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(TCFLSH), uintptr(unix.TCIFLUSH))

	// Follow-up: short non-blocking drain window to catch any bytes that arrive
	// immediately after the flush (common with OSC/CPR reply bursts).
	_ = unix.SetNonblock(fd, true)
	defer func() { _ = unix.SetNonblock(fd, false) }()

	deadline := time.Now().Add(200 * time.Millisecond)
	buf := make([]byte, 512)

	for time.Now().Before(deadline) {
		n, rerr := unix.Read(fd, buf)
		if n > 0 {
			// If we consumed bytes, extend slightly to catch the remainder of a burst.
			deadline = time.Now().Add(75 * time.Millisecond)
			continue
		}
		if rerr == nil {
			break
		}
		if rerr == unix.EAGAIN || rerr == unix.EWOULDBLOCK {
			break
		}
		break
	}

	// Keep runtime referenced; useful if you later add per-OS fallbacks.
	_ = runtime.GOOS
}
