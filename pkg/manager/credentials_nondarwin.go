//go:build !darwin && !linux
// +build !darwin,!linux

package manager

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
)

// Non-darwin, non-linux stubs for credential management helpers.
//
// macOS (darwin): implemented via Keychain.
// Linux: implemented via Secret Service (`secret-tool`).
// Other OSes: not implemented.

// CredSet stores/updates a secret in the platform credential store.
// Non-darwin, non-linux: not implemented.
func CredSet(hostKey, username, kind string) error {
	_ = strings.TrimSpace(hostKey)
	_ = strings.TrimSpace(username)
	_ = strings.TrimSpace(kind)
	return notSupportedErr()
}

// CredGet verifies that a credential exists and is accessible.
// Non-darwin, non-linux: not implemented.
func CredGet(hostKey, username, kind string) error {
	_ = strings.TrimSpace(hostKey)
	_ = strings.TrimSpace(username)
	_ = strings.TrimSpace(kind)
	return notSupportedErr()
}

// CredReveal retrieves secret material from the platform credential store.
// Non-darwin, non-linux: not implemented.
func CredReveal(hostKey, username, kind string) (string, error) {
	_ = strings.TrimSpace(hostKey)
	_ = strings.TrimSpace(username)
	_ = strings.TrimSpace(kind)
	return "", notSupportedErr()
}

// CredDelete removes a credential from the platform credential store.
// Non-darwin, non-linux: not implemented.
func CredDelete(hostKey, username, kind string) error {
	_ = strings.TrimSpace(hostKey)
	_ = strings.TrimSpace(username)
	_ = strings.TrimSpace(kind)
	return notSupportedErr()
}

func notSupportedErr() error {
	// Use a stable error so callers can detect the condition if desired.
	return fmt.Errorf("%w: credential store is only supported on macOS (darwin) and Linux (secret-tool); current=%s", errors.New("not supported"), runtime.GOOS)
}
