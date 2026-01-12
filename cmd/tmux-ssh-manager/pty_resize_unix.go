//go:build !windows
// +build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// startPTYResizeWatcher keeps the PTY size in sync with the current terminal size.
//
// IMPORTANT:
// - Implemented only on non-Windows because Windows does not define SIGWINCH.
// - This is best-effort; if stdout is not a TTY, or size queries fail, it does nothing.
func startPTYResizeWatcher(ptmx *os.File) {
	if ptmx == nil {
		return
	}

	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)

	go func() {
		defer signal.Stop(winchCh)
		for range winchCh {
			if term.IsTerminal(int(os.Stdout.Fd())) {
				if cols, rows, err := term.GetSize(int(os.Stdout.Fd())); err == nil && rows > 0 && cols > 0 {
					_ = pty.Setsize(ptmx, &pty.Winsize{
						Rows: uint16(rows),
						Cols: uint16(cols),
					})
				}
			}
		}
	}()
}
