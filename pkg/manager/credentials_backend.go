package manager

import (
	"os"
	"runtime"
	"strings"
)

// CredentialBackendKind describes which credential storage backend is active on the current OS.
// This is intended for UI/help/status messages (not for security decisions).
type CredentialBackendKind string

const (
	// CredBackendKeychain is macOS Keychain (via `security`).
	CredBackendKeychain CredentialBackendKind = "keychain"

	// CredBackendSecretService is Linux Secret Service / libsecret (via `secret-tool`).
	CredBackendSecretService CredentialBackendKind = "secret-service"

	// CredBackendGPGFileStore is a headless-friendly encrypted file store using `gpg`.
	// This is intended as a fallback when Secret Service is unavailable on Linux.
	CredBackendGPGFileStore CredentialBackendKind = "gpg"

	// CredBackendUnsupported means the project has no credential backend implementation
	// for this OS/target.
	CredBackendUnsupported CredentialBackendKind = "unsupported"
)

// linuxGPGConfigured reports whether the Linux GPG fallback is configured.
// This is intentionally environment-driven so headless systems can opt into a reliable backend.
//
// Supported configuration (any of the following enables it):
// - TMUX_SSH_MANAGER_GPG_RECIPIENT=<keyid/email>
// - TMUX_SSH_MANAGER_GPG_SYMMETRIC=1
func linuxGPGConfigured() bool {
	if strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_GPG_RECIPIENT")) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_GPG_SYMMETRIC"))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// CredentialBackend returns the effective backend kind for the current runtime OS.
//
// Note:
//   - This is used only for messaging. Actual support is enforced by OS-specific implementations
//     of CredSet/CredGet/CredReveal/CredDelete.
//   - On Linux, this attempts to reflect the configured fallback behavior: if GPG fallback is
//     configured, we label it as such (since Secret Service may not exist on headless systems).
func CredentialBackend() CredentialBackendKind {
	switch runtime.GOOS {
	case "darwin":
		return CredBackendKeychain
	case "linux":
		if linuxGPGConfigured() {
			return CredBackendGPGFileStore
		}
		return CredBackendSecretService
	default:
		return CredBackendUnsupported
	}
}

// CredentialBackendLabel returns a user-facing short label for the active backend.
//
// Examples:
// - "Keychain" (macOS)
// - "Secret Service (secret-tool)" (Linux desktop)
// - "GPG (encrypted file store)" (Linux headless fallback)
func CredentialBackendLabel() string {
	switch CredentialBackend() {
	case CredBackendKeychain:
		return "Keychain"
	case CredBackendSecretService:
		return "Secret Service (secret-tool)"
	case CredBackendGPGFileStore:
		return "GPG (encrypted file store)"
	default:
		return "Unsupported"
	}
}

// CredentialBackendLongHint returns a short hint suitable for status/error messages.
// It is intentionally brief; README holds install details.
func CredentialBackendLongHint() string {
	switch CredentialBackend() {
	case CredBackendKeychain:
		return "macOS Keychain via `security`"
	case CredBackendSecretService:
		return "Linux Secret Service via `secret-tool` (install libsecret tools + keyring provider)"
	case CredBackendGPGFileStore:
		return "Linux headless fallback via `gpg` (set TMUX_SSH_MANAGER_GPG_RECIPIENT or TMUX_SSH_MANAGER_GPG_SYMMETRIC=1)"
	default:
		return "no credential store backend for this OS"
	}
}
