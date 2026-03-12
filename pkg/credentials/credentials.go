package credentials

import (
	"errors"
	"fmt"
	"strings"
)

const servicePrefix = "tmux-ssh-manager"

var ErrUnsupported = errors.New("credentials are only supported on macOS")

func normalizeHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("host is required")
	}
	return host, nil
}

func normalizeUser(host, user string) string {
	user = strings.TrimSpace(user)
	if user != "" {
		return user
	}
	return host
}

func normalizeKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "password":
		return "password"
	case "passphrase":
		return "passphrase"
	case "otp", "totp":
		return "otp"
	default:
		return strings.ToLower(strings.TrimSpace(kind))
	}
}

func serviceName(host, kind string) string {
	return fmt.Sprintf("%s:%s:%s", servicePrefix, host, normalizeKind(kind))
}

func itemLabel(host, user, kind string) string {
	if user != "" && user != host {
		return fmt.Sprintf("%s for %s@%s", normalizeKind(kind), user, host)
	}
	return fmt.Sprintf("%s for %s", normalizeKind(kind), host)
}
