package manager

import (
	"os"
	"runtime"
	"strings"
)

// CredentialBackendKind identifies the active credential backend (for UI/status only).
type CredentialBackendKind string

const (
	CredBackendKeychain      CredentialBackendKind = "keychain"
	CredBackendSecretService CredentialBackendKind = "secret-service"
	CredBackendGPGFileStore  CredentialBackendKind = "gpg"
	CredBackendUnsupported   CredentialBackendKind = "unsupported"
)

// linuxGPGConfigured reports whether the GPG backend is enabled via env.
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

// CredentialBackend returns the backend kind for the current OS/runtime.
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

// CredentialBackendLabel returns a short label for display.
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
