//go:build windows

package termio

import (
	"io"
	"os"
)

func SanitizeStdinBeforeExec(stdin *os.File, stderr io.Writer) {
	// No-op on Windows.
}
