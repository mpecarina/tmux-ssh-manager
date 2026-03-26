//go:build !windows

package termio

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/sys/unix"
)

func debugStdinDrainEnabled() bool {
	v := strings.TrimSpace(os.Getenv("TSSM_DEBUG_STDIN"))
	if v == "" {
		return false
	}
	v = strings.ToLower(v)
	return v != "0" && v != "false" && v != "no"
}

func debugStdinDrainf(stderr io.Writer, format string, args ...any) {
	if !debugStdinDrainEnabled() {
		return
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	_, _ = fmt.Fprintf(stderr, "tmux-ssh-manager: "+format+"\n", args...)
}

// SanitizeStdinBeforeExec drains stray terminal reply bytes from stdin.
//
// Some terminals write replies to OSC/DSR queries (e.g. OSC 11 background color,
// DSR cursor position) onto the application's stdin. If those bytes outlive a
// TUI lifecycle, they can be interpreted as literal input by the next exec'd
// program (ssh) or by the shell after the program exits.
//
// Strategy: if stdin is a TTY, switch it to non-blocking mode, drain until
// stdin is quiet for quietFor (bounded by maxTotal), then restore original flags.
func SanitizeStdinBeforeExec(stdin *os.File, stderr io.Writer) {
	if stdin == nil {
		debugStdinDrainf(stderr, "sanitize stdin: skipped (stdin is nil)")
		return
	}
	fd := int(stdin.Fd())
	if fd <= 0 {
		debugStdinDrainf(stderr, "sanitize stdin: skipped (invalid fd=%d)", fd)
		return
	}
	if !isatty.IsTerminal(uintptr(fd)) {
		debugStdinDrainf(stderr, "sanitize stdin: skipped (stdin is not a TTY)")
		return
	}

	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		debugStdinDrainf(stderr, "sanitize stdin: skipped (fcntl F_GETFL failed: %v)", err)
		return
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		debugStdinDrainf(stderr, "sanitize stdin: skipped (set nonblock failed: %v)", err)
		return
	}
	defer func() {
		_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags)
	}()

	const (
		maxTotal  = 500 * time.Millisecond
		quietFor  = 50 * time.Millisecond
		sleepStep = 10 * time.Millisecond
		bufSz     = 4096
		debugMax  = 256
	)

	debug := debugStdinDrainEnabled()
	start := time.Now()
	lastRead := start
	var drained int
	var sample []byte
	buf := make([]byte, bufSz)

	for {
		n, rerr := unix.Read(fd, buf)
		if n > 0 {
			drained += n
			lastRead = time.Now()
			if debug && len(sample) < debugMax {
				need := debugMax - len(sample)
				if n < need {
					need = n
				}
				sample = append(sample, buf[:need]...)
			}
			continue
		}
		if rerr == nil {
			// EOF
			break
		}
		if rerr != unix.EAGAIN && rerr != unix.EWOULDBLOCK {
			debugStdinDrainf(stderr, "sanitize stdin: stopped (read error: %v)", rerr)
			break
		}
		if time.Since(lastRead) >= quietFor {
			break
		}
		if time.Since(start) >= maxTotal {
			debugStdinDrainf(stderr, "sanitize stdin: stopped (maxTotal=%s reached; drained=%d)", maxTotal.String(), drained)
			break
		}
		time.Sleep(sleepStep)
	}

	if debug && drained == 0 {
		debugStdinDrainf(stderr, "sanitize stdin: no bytes drained")
	}

	if debug && drained > 0 {
		hexStr := strings.TrimSpace(hex.EncodeToString(sample))
		if hexStr == "" {
			debugStdinDrainf(stderr, "sanitize stdin: drained %d bytes (no sample available)", drained)
			return
		}
		// Group into pairs for readability.
		pairs := make([]byte, 0, len(hexStr)+len(hexStr)/2)
		for i := 0; i < len(hexStr); i += 2 {
			if i > 0 {
				pairs = append(pairs, ' ')
			}
			pairs = append(pairs, hexStr[i], hexStr[i+1])
		}
		trunc := ""
		if drained > len(sample) {
			trunc = fmt.Sprintf(" (showing first %d bytes)", len(sample))
		}
		_, _ = fmt.Fprintf(stderr, "tmux-ssh-manager: drained %d stdin bytes%s: %s\n", drained, trunc, string(pairs))
	}
}
