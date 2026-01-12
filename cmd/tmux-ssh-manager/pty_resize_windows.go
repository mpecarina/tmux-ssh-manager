//go:build windows
// +build windows

package main

import "os"

// startPTYResizeWatcher is a no-op on Windows.
//
// Rationale:
//   - The askpass PTY connector uses SIGWINCH on Unix-like systems to propagate terminal
//     size changes into the PTY.
//   - Windows does not define SIGWINCH, and referencing it anywhere in a Windows build
//     will fail compilation.
//   - For Windows builds, we skip live resize propagation; initial PTY sizing (if any)
//     is handled elsewhere on a best-effort basis.
func startPTYResizeWatcher(_ *os.File) {
	// no-op
}
