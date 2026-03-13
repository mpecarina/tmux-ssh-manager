//go:build darwin

package credentials

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

var runSecurityCommand = defaultRunSecurityCommand
var promptSecret = defaultPromptSecret

func Set(host, user, kind string) error {
	host, err := normalizeHost(host)
	if err != nil {
		return err
	}
	kind = normalizeKind(kind)
	user = normalizeUser(host, user)

	secret, err := promptSecret(fmt.Sprintf("Enter %s for %s", kind, itemLabel(host, user, kind)))
	if err != nil {
		return err
	}
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("empty secret refused")
	}

	_, err = runSecurityCommand(
		"add-generic-password",
		"-U",
		"-s", serviceName(host, kind),
		"-a", user,
		"-l", itemLabel(host, user, kind),
		"-w", secret,
	)
	if err != nil {
		return fmt.Errorf("keychain write failed: %w", err)
	}
	return nil
}

func Get(host, user, kind string) error {
	host, err := normalizeHost(host)
	if err != nil {
		return err
	}
	user = normalizeUser(host, user)

	_, err = runSecurityCommand(
		"find-generic-password",
		"-s", serviceName(host, kind),
		"-a", user,
	)
	if err != nil {
		return fmt.Errorf("credential not found for %s", itemLabel(host, user, kind))
	}
	return nil
}

func Delete(host, user, kind string) error {
	host, err := normalizeHost(host)
	if err != nil {
		return err
	}
	user = normalizeUser(host, user)

	_, err = runSecurityCommand(
		"delete-generic-password",
		"-s", serviceName(host, kind),
		"-a", user,
	)
	if err != nil {
		return fmt.Errorf("keychain delete failed: %w", err)
	}
	return nil
}

func defaultRunSecurityCommand(args ...string) (string, error) {
	path := "/usr/bin/security"
	if _, err := os.Stat(path); err != nil {
		path = "security"
	}

	cmd := exec.Command(path, args...)
	cmd.Stdin = nil

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("%s", message)
	}
	return stdout.String(), nil
}

func defaultPromptSecret(prompt string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("open /dev/tty: %w", err)
	}
	defer tty.Close()

	if _, err := fmt.Fprintf(tty, "%s: ", prompt); err != nil {
		return "", fmt.Errorf("write prompt: %w", err)
	}

	if err := runStty(tty, "-echo"); err != nil {
		return "", err
	}
	defer func() {
		_ = runStty(tty, "echo")
		_, _ = fmt.Fprintln(tty)
	}()

	secret, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read secret: %w", err)
	}
	return strings.TrimRight(secret, "\r\n"), nil
}

func Reveal(host, user, kind string) (string, error) {
	host, err := normalizeHost(host)
	if err != nil {
		return "", err
	}
	user = normalizeUser(host, user)

	out, err := runSecurityCommand(
		"find-generic-password",
		"-w",
		"-s", serviceName(host, kind),
		"-a", user,
	)
	if err != nil {
		return "", fmt.Errorf("credential not found for %s", itemLabel(host, user, kind))
	}
	secret := strings.TrimRight(out, "\r\n")
	if secret == "" {
		return "", fmt.Errorf("empty credential for %s", itemLabel(host, user, kind))
	}
	return secret, nil
}

func runStty(tty *os.File, mode string) error {
	cmd := exec.Command("stty", mode)
	cmd.Stdin = tty
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("configure tty: %w", err)
	}
	return nil
}
