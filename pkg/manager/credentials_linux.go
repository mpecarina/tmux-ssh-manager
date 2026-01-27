//go:build linux
// +build linux

package manager

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Linux credential backend: Secret Service via `secret-tool`, with optional GPG file-store fallback.
// Config (GPG): TMUX_SSH_MANAGER_GPG_RECIPIENT or TMUX_SSH_MANAGER_GPG_SYMMETRIC=1 + TMUX_SSH_MANAGER_GPG_PASSPHRASE_FILE (chmod 600).
// Optional: TMUX_SSH_MANAGER_GPG_BINARY.
// Security: do not print secrets; do not log CredReveal output.

const secretServiceApp = "tmux-ssh-manager"

const (
	envGPGRecipient      = "TMUX_SSH_MANAGER_GPG_RECIPIENT"
	envGPGSymmetric      = "TMUX_SSH_MANAGER_GPG_SYMMETRIC"
	envGPGBinary         = "TMUX_SSH_MANAGER_GPG_BINARY"
	envGPGPassphraseFile = "TMUX_SSH_MANAGER_GPG_PASSPHRASE_FILE"
)

// CredSet stores/updates a secret.
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

	account := username
	if account == "" {
		account = hostKey
	}

	// 1) Secret Service (preferred when available)
	if err := secretToolStore(fmt.Sprintf("%s (%s)", hostKey, kind), secret, hostKey, account, kind); err == nil {
		return nil
	}

	// 2) Auto-fallback to GPG if configured (headless-friendly)
	if gpgConfigured() {
		return gpgCredSet(hostKey, account, kind, secret)
	}

	// If secret service is unavailable and no gpg fallback is configured, return a hint.
	// Otherwise bubble up the secret-tool error.
	if isSecretServiceUnavailableErr(err) {
		return fmt.Errorf("%v (headless: set %s or %s=1 to enable GPG fallback)", err, envGPGRecipient, envGPGSymmetric)
	}
	return err
}

// CredGet verifies that a credential exists and is accessible.
// It intentionally does NOT print or return the secret.
func CredGet(hostKey, username, kind string) error {
	hostKey = strings.TrimSpace(hostKey)
	username = strings.TrimSpace(username)
	kind = normalizeCredKind(kind)

	if hostKey == "" {
		return errors.New("CredGet: hostKey is required")
	}

	account := username
	if account == "" {
		account = hostKey
	}

	// 1) Secret Service (preferred when available)
	secret, err := secretToolLookup(hostKey, account, kind)
	if err == nil {
		if strings.TrimSpace(secret) == "" {
			return fmt.Errorf("CredGet: credential not found (app=%q host=%q user=%q kind=%q)", secretServiceApp, hostKey, account, kind)
		}
		return nil
	}

	// 2) Auto-fallback to GPG if configured
	if gpgConfigured() {
		_, gerr := gpgCredRevealInternal(hostKey, account, kind)
		if gerr == nil {
			return nil
		}
		// If Secret Service is unavailable, prefer the GPG error message (it’s actionable).
		// Otherwise, keep the original error as it may indicate a mismatch or missing credential.
		if isSecretServiceUnavailableErr(err) {
			return gerr
		}
		// If Secret Service returned "not found" but GPG also didn't find it, return a not-found style error.
		return gerr
	}

	if isSecretServiceUnavailableErr(err) {
		return fmt.Errorf("%v (headless: set %s or %s=1 to enable GPG fallback)", err, envGPGRecipient, envGPGSymmetric)
	}
	return err
}

// CredReveal retrieves secret material from the credential store.
//
// WARNING: This returns sensitive data. It MUST NOT be used by normal UI paths.
func CredReveal(hostKey, username, kind string) (string, error) {
	hostKey = strings.TrimSpace(hostKey)
	username = strings.TrimSpace(username)
	kind = normalizeCredKind(kind)

	if hostKey == "" {
		return "", errors.New("CredReveal: hostKey is required")
	}

	account := username
	if account == "" {
		account = hostKey
	}

	// 1) Secret Service
	secret, err := secretToolLookup(hostKey, account, kind)
	if err == nil {
		secret = strings.TrimRight(secret, "\r\n")
		if secret == "" {
			return "", fmt.Errorf("CredReveal: empty secret returned (app=%q host=%q user=%q kind=%q)", secretServiceApp, hostKey, account, kind)
		}
		return secret, nil
	}

	// 2) Auto-fallback to GPG if configured
	if gpgConfigured() {
		return gpgCredRevealInternal(hostKey, account, kind)
	}

	if isSecretServiceUnavailableErr(err) {
		return "", fmt.Errorf("%v (headless: set %s or %s=1 to enable GPG fallback)", err, envGPGRecipient, envGPGSymmetric)
	}
	return "", err
}

// CredDelete removes a credential from the credential store.
func CredDelete(hostKey, username, kind string) error {
	hostKey = strings.TrimSpace(hostKey)
	username = strings.TrimSpace(username)
	kind = normalizeCredKind(kind)

	if hostKey == "" {
		return errors.New("CredDelete: hostKey is required")
	}

	account := username
	if account == "" {
		account = hostKey
	}

	// 1) Secret Service
	if err := secretToolClear(hostKey, account, kind); err == nil {
		return nil
	} else {
		// 2) Auto-fallback to GPG if configured
		if gpgConfigured() {
			return gpgCredDelete(hostKey, account, kind)
		}
		if isSecretServiceUnavailableErr(err) {
			return fmt.Errorf("%v (headless: set %s or %s=1 to enable GPG fallback)", err, envGPGRecipient, envGPGSymmetric)
		}
		return err
	}
}

// ---- Secret Service (secret-tool) helpers ----

func ensureSecretTool() (string, error) {
	candidates := []string{"/usr/bin/secret-tool", "/bin/secret-tool", "secret-tool"}
	for _, c := range candidates {
		if strings.Contains(c, "/") {
			if st, err := os.Stat(c); err == nil && st != nil {
				return c, nil
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil && p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf("secret-tool not found: install libsecret tools (e.g. Debian/Ubuntu: apt-get install libsecret-tools)")
}

func secretToolStore(label, secret, hostKey, account, kind string) error {
	path, err := ensureSecretTool()
	if err != nil {
		return err
	}

	// Best-effort upsert: clear first (ignore errors).
	_ = secretToolClear(hostKey, account, kind)

	args := []string{
		"store",
		"--label=" + label,
		"app", secretServiceApp,
		"host", hostKey,
		"user", account,
		"kind", kind,
	}

	cmd := exec.Command(path, args...)
	cmd.Stdin = strings.NewReader(secret)

	var stderr bytes.Buffer
	cmd.Stdout = nil
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("CredSet: secret-tool store failed: %s", msg)
	}
	return nil
}

func secretToolLookup(hostKey, account, kind string) (string, error) {
	path, err := ensureSecretTool()
	if err != nil {
		return "", err
	}

	args := []string{
		"lookup",
		"app", secretServiceApp,
		"host", hostKey,
		"user", account,
		"kind", kind,
	}

	cmd := exec.Command(path, args...)
	cmd.Stdin = nil

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("CredGet: credential not found (app=%q host=%q user=%q kind=%q)", secretServiceApp, hostKey, account, kind)
		}
		if looksLikeSecretServiceUnavailable(msg) {
			return "", fmt.Errorf("CredGet: secret service unavailable: %s (ensure a keyring/secret service is running; install libsecret-tools)", msg)
		}
		return "", fmt.Errorf("CredGet: secret-tool lookup failed: %s", msg)
	}

	return stdout.String(), nil
}

func secretToolClear(hostKey, account, kind string) error {
	path, err := ensureSecretTool()
	if err != nil {
		return err
	}

	args := []string{
		"clear",
		"app", secretServiceApp,
		"host", hostKey,
		"user", account,
		"kind", kind,
	}

	cmd := exec.Command(path, args...)
	cmd.Stdin = nil

	var stderr bytes.Buffer
	cmd.Stdout = nil
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil
		}
		if looksLikeSecretServiceUnavailable(msg) {
			return fmt.Errorf("CredDelete: secret service unavailable: %s (ensure a keyring/secret service is running; install libsecret-tools)", msg)
		}
		if strings.Contains(strings.ToLower(msg), "not found") || strings.Contains(strings.ToLower(msg), "no such") {
			return nil
		}
		return fmt.Errorf("CredDelete: secret-tool clear failed: %s", msg)
	}
	return nil
}

func looksLikeSecretServiceUnavailable(msg string) bool {
	m := strings.ToLower(strings.TrimSpace(msg))
	return strings.Contains(m, "org.freedesktop.secrets") ||
		strings.Contains(m, "no such interface") ||
		strings.Contains(m, "serviceunknown") ||
		strings.Contains(m, "could not connect") ||
		strings.Contains(m, "failed to connect") ||
		strings.Contains(m, "dbus") ||
		strings.Contains(m, "not provided")
}

func isSecretServiceUnavailableErr(err error) bool {
	if err == nil {
		return false
	}
	return looksLikeSecretServiceUnavailable(err.Error())
}

// ---- GPG encrypted file store fallback (headless-friendly) ----

// gpgConfigured returns true if the user has explicitly configured the GPG backend.
func gpgConfigured() bool {
	if strings.TrimSpace(os.Getenv(envGPGRecipient)) != "" {
		return true
	}

	// Symmetric mode must be explicitly enabled AND have a passphrase file configured (file-only policy).
	if v := strings.TrimSpace(os.Getenv(envGPGSymmetric)); v != "" && v != "0" && strings.ToLower(v) != "false" && strings.ToLower(v) != "no" {
		pf := strings.TrimSpace(os.Getenv(envGPGPassphraseFile))
		if pf == "" {
			return false
		}
		// Only consider it configured if the file exists (permissions checked by operator; we only validate existence/readability).
		if st, err := os.Stat(pf); err == nil && st != nil && !st.IsDir() {
			return true
		}
		return false
	}

	return false
}

func gpgBinary() (string, error) {
	if p := strings.TrimSpace(os.Getenv(envGPGBinary)); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("gpg not found at %s", p)
	}
	if p, err := exec.LookPath("gpg"); err == nil && p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("gpg2"); err == nil && p != "" {
		return p, nil
	}
	return "", fmt.Errorf("gpg not found: install gpg (e.g. Debian/Ubuntu: apt-get install gpg)")
}

func credsBaseDir() (string, error) {
	xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if xdg != "" {
		return filepath.Join(xdg, "tmux-ssh-manager", "creds"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "tmux-ssh-manager", "creds"), nil
}

func gpgCredPath(hostKey, account, kind string) (string, error) {
	base, err := credsBaseDir()
	if err != nil {
		return "", err
	}
	h := sanitizeHostKeyToFilename(hostKey)
	u := sanitizeHostKeyToFilename(account)
	k := sanitizeHostKeyToFilename(kind)
	return filepath.Join(base, h, u, k+".gpg"), nil
}

func ensureDir0700(p string) error {
	return os.MkdirAll(p, 0o700)
}

func gpgCredSet(hostKey, account, kind, secret string) error {
	path, err := gpgBinary()
	if err != nil {
		return err
	}
	outPath, err := gpgCredPath(hostKey, account, kind)
	if err != nil {
		return err
	}
	if err := ensureDir0700(filepath.Dir(outPath)); err != nil {
		return fmt.Errorf("CredSet: mkdir: %w", err)
	}

	recipient := strings.TrimSpace(os.Getenv(envGPGRecipient))
	symmetric := strings.TrimSpace(os.Getenv(envGPGSymmetric))
	passFile := strings.TrimSpace(os.Getenv(envGPGPassphraseFile))

	args := []string{"--batch", "--yes", "--armor", "--output", outPath}

	if recipient != "" {
		args = append(args, "--encrypt", "--recipient", recipient)
		cmd := exec.Command(path, args...)
		cmd.Stdin = strings.NewReader(secret)

		var stderr bytes.Buffer
		cmd.Stdout = nil
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return fmt.Errorf("CredSet: gpg encrypt failed: %s", msg)
		}
		_ = os.Chmod(outPath, 0o600)
		return nil
	}

	// Symmetric (file-only, loopback):
	// - Use pinentry-mode loopback to avoid gpg-agent/pinentry TTY issues in tmux popups/headless.
	// - Read passphrase from the configured file.
	if symmetric != "" && symmetric != "0" && strings.ToLower(symmetric) != "false" && strings.ToLower(symmetric) != "no" {
		if passFile == "" {
			return fmt.Errorf("CredSet: symmetric gpg requires %s (file-only)", envGPGPassphraseFile)
		}
		if st, err := os.Stat(passFile); err != nil || st == nil || st.IsDir() {
			return fmt.Errorf("CredSet: symmetric gpg passphrase file not found: %s", passFile)
		}

		args = append(args,
			"--pinentry-mode", "loopback",
			"--passphrase-file", passFile,
			"--symmetric",
		)

		cmd := exec.Command(path, args...)
		cmd.Stdin = strings.NewReader(secret)

		var stderr bytes.Buffer
		cmd.Stdout = nil
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return fmt.Errorf("CredSet: gpg symmetric encrypt failed: %s", msg)
		}
		_ = os.Chmod(outPath, 0o600)
		return nil
	}

	return fmt.Errorf("CredSet: gpg fallback not configured: set %s or (%s=1 and %s=/path)", envGPGRecipient, envGPGSymmetric, envGPGPassphraseFile)
}

func gpgCredRevealInternal(hostKey, account, kind string) (string, error) {
	path, err := gpgBinary()
	if err != nil {
		return "", err
	}
	inPath, err := gpgCredPath(hostKey, account, kind)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(inPath); err != nil {
		return "", fmt.Errorf("CredGet: credential not found (gpg path=%s)", inPath)
	}

	recipient := strings.TrimSpace(os.Getenv(envGPGRecipient))
	symmetric := strings.TrimSpace(os.Getenv(envGPGSymmetric))
	passFile := strings.TrimSpace(os.Getenv(envGPGPassphraseFile))

	args := []string{"--batch", "--quiet"}

	// For symmetric mode, use loopback + passphrase file (file-only policy).
	// For recipient mode, no passphrase flags are needed.
	if recipient == "" && symmetric != "" && symmetric != "0" && strings.ToLower(symmetric) != "false" && strings.ToLower(symmetric) != "no" {
		if passFile == "" {
			return "", fmt.Errorf("CredReveal: symmetric gpg requires %s (file-only)", envGPGPassphraseFile)
		}
		if st, err := os.Stat(passFile); err != nil || st == nil || st.IsDir() {
			return "", fmt.Errorf("CredReveal: symmetric gpg passphrase file not found: %s", passFile)
		}
		args = append(args, "--pinentry-mode", "loopback", "--passphrase-file", passFile)
	}

	args = append(args, "--decrypt", inPath)

	cmd := exec.Command(path, args...)
	cmd.Stdin = nil

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("CredReveal: gpg decrypt failed: %s", msg)
	}

	secret := strings.TrimRight(stdout.String(), "\r\n")
	if secret == "" {
		return "", fmt.Errorf("CredReveal: empty secret returned (gpg path=%s)", inPath)
	}
	return secret, nil
}

func gpgCredDelete(hostKey, account, kind string) error {
	inPath, err := gpgCredPath(hostKey, account, kind)
	if err != nil {
		return err
	}
	if err := os.Remove(inPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("CredDelete: remove: %w", err)
	}
	return nil
}

// ---- shared helpers (mirrors darwin file behavior) ----

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
		return k
	}
}

func promptSecret(prompt string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("promptSecret: open /dev/tty: %w", err)
	}
	defer tty.Close()

	if prompt != "" {
		fmt.Fprint(tty, prompt)
		if !strings.HasSuffix(prompt, ": ") {
			fmt.Fprint(tty, ": ")
		}
	}

	if err := sttyNoEcho(true); err != nil {
		return "", err
	}
	defer func() { _ = sttyNoEcho(false) }()

	reader := bufio.NewReader(tty)
	line, readErr := reader.ReadString('\n')

	_ = sttyNoEcho(false)
	fmt.Fprint(tty, "\n")

	if readErr != nil && len(line) == 0 {
		return "", fmt.Errorf("promptSecret: read: %w", readErr)
	}

	secret := strings.TrimRight(line, "\r\n")
	return secret, nil
}

func sttyNoEcho(disable bool) error {
	arg := "echo"
	if disable {
		arg = "-echo"
	}

	sttyPath, err := exec.LookPath("stty")
	if err != nil || sttyPath == "" {
		for _, p := range []string{"/bin/stty", "/usr/bin/stty"} {
			if _, e := os.Stat(p); e == nil {
				sttyPath = p
				break
			}
		}
	}
	if sttyPath == "" {
		return fmt.Errorf("promptSecret: stty not found (required to disable echo)")
	}

	cmd := exec.Command(sttyPath, arg)

	if tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0); err == nil {
		defer tty.Close()
		cmd.Stdin = tty
	} else {
		cmd.Stdin = os.Stdin
	}

	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
