//go:build darwin
// +build darwin

package manager

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// This file implements a macOS Keychain-backed credential store by shelling out to
// the built-in `security` tool. This keeps us macOS-native and avoids introducing
// heavy cgo dependencies.
//
// Security model notes:
// - We never print secrets to stdout.
// - `CredGet` verifies presence/access but does not reveal secret material.
// - `CredSet` prompts on the controlling TTY and stores the secret in Keychain.
// - Secrets are stored as a "generic password" item.
// - "kind" and "username" are stored as item attributes where practical.
//
// Askpass integration notes:
// - `CredReveal` is intentionally NOT used by the normal UI paths.
// - `CredReveal` returns secret material for controlled use-cases like SSH_ASKPASS.
// - Callers MUST ensure the secret is never logged, stored, or displayed.

const keychainServiceName = "tmux-ssh-manager"

// CredSet stores/updates a secret in the macOS Keychain for a host key.
//
// hostKey: an SSH alias or destination key you decide to use (e.g. "leaf01.lab.local").
// username: optional; stored as the Keychain "account" attribute when present.
// kind: logical credential kind (password|passphrase|otp). Stored in "label"/"comment" best-effort.
func CredSet(hostKey, username, kind string) error {
	hostKey = strings.TrimSpace(hostKey)
	username = strings.TrimSpace(username)
	kind = normalizeCredKind(kind)
	if hostKey == "" {
		return errors.New("CredSet: hostKey is required")
	}

	secret, err := promptSecret(fmt.Sprintf("Enter %s for %s", kind, hostKey))
	if err != nil {
		return err
	}
	if secret == "" {
		return errors.New("CredSet: empty secret refused")
	}

	// Upsert behavior:
	// - `security add-generic-password -U` updates if present
	// - Store secret with -w
	// - Use service + account; account falls back to hostKey if username not provided
	account := username
	if account == "" {
		account = hostKey
	}

	args := []string{
		"add-generic-password",
		"-U",
		"-s", keychainServiceName,
		"-a", account,
		"-l", fmt.Sprintf("%s (%s)", hostKey, kind),
		"-w", secret,
	}

	// Store hostKey and kind as comments where supported (best-effort).
	// Note: `security` supports -j (comment) and -D (kind/type) but behavior varies.
	// We keep it simple and portable across macOS versions.
	if err := runSecurity(args...); err != nil {
		return fmt.Errorf("CredSet: keychain write failed: %w", err)
	}

	return nil
}

// CredGet verifies that a credential exists and is accessible.
// It intentionally does NOT print or return the secret.
func CredGet(hostKey, username, kind string) error {
	hostKey = strings.TrimSpace(hostKey)
	username = strings.TrimSpace(username)
	_ = normalizeCredKind(kind) // currently informational

	if hostKey == "" {
		return errors.New("CredGet: hostKey is required")
	}

	account := username
	if account == "" {
		// If caller didn't provide a username, try the hostKey as account first.
		// This matches CredSet's default account fallback.
		account = hostKey
	}

	// `security find-generic-password` returns 0 if found.
	// We avoid `-w` to ensure we never print the secret.
	args := []string{
		"find-generic-password",
		"-s", keychainServiceName,
		"-a", account,
	}

	if err := runSecurity(args...); err == nil {
		return nil
	}

	// If username was provided, no fallback makes sense. If it wasn't provided,
	// also try account=""? There's no safe general "search by label" without
	// potentially matching multiple entries. Keep deterministic.
	return fmt.Errorf("CredGet: credential not found (service=%q account=%q)", keychainServiceName, account)
}

// CredReveal retrieves secret material from the macOS Keychain.
//
// WARNING: This returns sensitive data. It MUST NOT be used by normal UI paths.
// Intended use is controlled integrations like SSH_ASKPASS where ssh consumes
// the secret via an internal helper, and nothing is written to tmux buffers,
// the clipboard, or logs.
//
// Behavior:
// - Looks up the item deterministically by service + account.
// - Never prints the secret itself; it is returned to the caller.
// - Caller must zero/overwrite buffers as best-effort if desired (Go strings are immutable).
func CredReveal(hostKey, username, kind string) (string, error) {
	hostKey = strings.TrimSpace(hostKey)
	username = strings.TrimSpace(username)
	_ = normalizeCredKind(kind) // currently informational

	if hostKey == "" {
		return "", errors.New("CredReveal: hostKey is required")
	}

	account := username
	if account == "" {
		// Match CredSet default
		account = hostKey
	}

	path := "/usr/bin/security"
	if _, err := os.Stat(path); err != nil {
		path = "security"
	}

	// `security find-generic-password -w` prints ONLY the password to stdout.
	// We capture it into memory and do not log it.
	cmd := exec.Command(path,
		"find-generic-password",
		"-w",
		"-s", keychainServiceName,
		"-a", account,
	)
	cmd.Stdin = nil

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("CredReveal: %s", msg)
	}

	secret := stdout.String()
	// `security` usually ends with a newline; trim it.
	secret = strings.TrimRight(secret, "\r\n")
	if secret == "" {
		return "", fmt.Errorf("CredReveal: empty secret returned (service=%q account=%q)", keychainServiceName, account)
	}
	return secret, nil
}

// CredDelete removes a credential from the Keychain.
func CredDelete(hostKey, username, kind string) error {
	hostKey = strings.TrimSpace(hostKey)
	username = strings.TrimSpace(username)
	_ = normalizeCredKind(kind) // currently informational

	if hostKey == "" {
		return errors.New("CredDelete: hostKey is required")
	}

	account := username
	if account == "" {
		account = hostKey
	}

	args := []string{
		"delete-generic-password",
		"-s", keychainServiceName,
		"-a", account,
	}

	if err := runSecurity(args...); err != nil {
		return fmt.Errorf("CredDelete: keychain delete failed: %w", err)
	}
	return nil
}

// ---- helpers ----

func normalizeCredKind(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	switch k {
	case "", "password":
		return "password"
	case "passphrase":
		return "passphrase"
	case "otp", "totp":
		return "otp"
	default:
		// Keep unknown kinds as-is so you can extend later without changing storage.
		return k
	}
}

func runSecurity(args ...string) error {
	// Prefer absolute path if available. On macOS, `security` lives at /usr/bin/security.
	path := "/usr/bin/security"
	if _, err := os.Stat(path); err != nil {
		// Fall back to PATH resolution.
		path = "security"
	}

	cmd := exec.Command(path, args...)
	// Never inherit stdin for security commands by default; they shouldn't prompt
	// (we use -w with add, and avoid -w with find). For operations that may prompt
	// due to Keychain access controls, macOS will handle UI prompt without stdin.
	cmd.Stdin = nil

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Do not include stdout because it can include item metadata that may be noisy.
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// promptSecret reads a secret from the controlling terminal without echo.
// Implementation uses `stty -echo` on /dev/tty to avoid bringing in extra deps.
func promptSecret(prompt string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("promptSecret: open /dev/tty: %w", err)
	}
	defer tty.Close()

	// Print prompt
	if prompt != "" {
		fmt.Fprint(tty, prompt)
		if !strings.HasSuffix(prompt, ": ") {
			fmt.Fprint(tty, ": ")
		}
	}

	// Disable echo
	if err := sttyNoEcho(true); err != nil {
		return "", err
	}
	defer func() { _ = sttyNoEcho(false) }()

	reader := bufio.NewReader(tty)
	line, readErr := reader.ReadString('\n')
	// Re-enable echo before returning, and print newline for UX.
	_ = sttyNoEcho(false)
	fmt.Fprint(tty, "\n")

	if readErr != nil && len(line) == 0 {
		return "", fmt.Errorf("promptSecret: read: %w", readErr)
	}

	secret := strings.TrimRight(line, "\r\n")
	return secret, nil
}

func sttyNoEcho(disable bool) error {
	// Use the controlling TTY to change echo. This is a common pattern on macOS.
	// Note: This assumes /bin/stty exists (it does on macOS).
	arg := "echo"
	if disable {
		arg = "-echo"
	}
	cmd := exec.Command("/bin/stty", arg)
	cmd.Stdin = os.Stdin
	// Prefer /dev/tty when available so we don't mess up piping scenarios.
	if tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0); err == nil {
		defer tty.Close()
		cmd.Stdin = tty
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
