package manager

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HostExtras provides per-host supplemental configuration loaded from the filesystem.
// The intent is to support settings that are awkward to represent in OpenSSH config
// (and/or that users prefer to keep local), such as password automation mode and
// authorized_keys management preferences.
//
// File layout (default):
//
//	~/.config/tmux-ssh-manager/hosts/<hostkey>.conf
//
// Where <hostkey> is typically the ssh alias you type after `ssh` (e.g. "leaf01"),
// or a hostname (e.g. "leaf01.lab.local").
//
// This file uses a simple "key=value" format with '#' comments.
// Keys are case-insensitive. Unknown keys are ignored.
//
// Example:
//
//	auth_mode=manual
//	# or: auth_mode=keychain
//	key_install=enabled
//	key_install_pubkey=~/.ssh/id_rsa.pub
//	identity_file=~/.ssh/id_rsa
//	neighbor_discovery=auto
//	# neighbor_discovery=lldp   # force LLDP (skip CDP fallback)
//	# neighbor_discovery=cdp    # force CDP (skip LLDP)
//	authorized_keys_mode=ensure
//	# authorized_keys_mode=replace  # requires explicit user confirmation in UI
type HostExtras struct {
	// HostKey is the identifier used to locate the extras file.
	// Usually this is the ssh alias (Host.Name when using --tui-source ssh).
	HostKey string

	// DeviceOS is an enum-like identifier for network device OS families used by topology discovery.
	// Examples (current supported values): cisco_iosxe | sonic_dell
	//
	// Empty means "unset / treat as non-network-device for topology purposes".
	DeviceOS string

	// AuthMode controls how password auth is handled.
	// - "manual"   (default): user types password interactively.
	// - "keychain"          : opt-in automation (e.g. SSH_ASKPASS backed by Keychain).
	AuthMode string

	// CredKey optionally overrides the Keychain credential lookup key.
	// If empty, caller should derive one (e.g. "user@hostkey" or "hostkey").
	CredKey string

	// Logging controls whether per-host session output is logged by default.
	// - true  (default): enable logging (pipe-pane for splits/windows/dashboards; script for popups)
	// - false          : do not log for this host unless explicitly enabled
	Logging bool

	// KeyInstall controls whether key bootstrap actions are enabled for this host.
	// This governs UI actions like "Install my public key".
	KeyInstall bool

	// KeyInstallPubKeyPath optionally pins which local public key to use for install.
	//
	// Default behavior:
	// - If unset, default to "~/.ssh/id_rsa.pub" (Linux-oriented baseline).
	// - Users can still override this per-host via key_install_pubkey=...
	KeyInstallPubKeyPath string

	// IdentityFile optionally pins which local private key to use for SSH connections
	// (e.g. "~/.ssh/id_rsa"). This is an override knob for users who want a per-host
	// identity without relying on ~/.ssh/config IdentityFile rules.
	//
	// NOTE: This is only a config field; the SSH argv builder must honor it.
	IdentityFile string

	// NeighborDiscovery controls how topology discovery chooses protocols/commands for this host.
	// Supported values:
	// - "auto" (default): use OS-specific default order (e.g. IOS/XE: LLDP detail -> LLDP -> CDP)
	// - "lldp": force LLDP only (skip CDP fallback)
	// - "cdp" : force CDP only (skip LLDP)
	//
	// This does not affect SSH connection behavior; it only impacts background discovery commands.
	NeighborDiscovery string

	// MgmtIP is an optional management-plane address (typically reachable via SSH / mgmt VRF).
	// This should NOT be used as the SSH destination directly; it exists to help topology
	// discovery and device identification (e.g. when LLDP reports a management address).
	//
	// Expected values: IPv4 or IPv6 literal (stored as a string). Empty means unset.
	MgmtIP string

	// RouterID is an optional "identity" address for topology matching (e.g. a loopback).
	// This should NOT be used as the SSH destination; it exists so LLDP parsing can
	// recognize devices even when SSH is only enabled on a separate management interface/VRF.
	//
	// Expected values: IPv4 or IPv6 literal (stored as a string). Empty means unset.
	RouterID string

	// AuthorizedKeysMode controls how we update remote ~/.ssh/authorized_keys:
	// - "ensure"  (default): add key if missing (idempotent), do not remove anything.
	// - "replace"          : replace authorized_keys content with the selected key.
	//                        MUST only be performed when explicitly confirmed by the user.
	AuthorizedKeysMode string

	// PreferTmuxNewWindow controls whether key management actions should run
	// in a new tmux window first (recommended for interactive password prompts).
	// If false, actions may try a split pane as a fallback.
	PreferTmuxNewWindow bool
}

// Normalize fills defaults and normalizes fields.
func (x HostExtras) Normalize() HostExtras {
	out := x
	out.AuthMode = strings.ToLower(strings.TrimSpace(out.AuthMode))
	if out.AuthMode == "" {
		out.AuthMode = "manual"
	}
	switch out.AuthMode {
	case "manual", "keychain":
	default:
		// Be conservative: unknown values fall back to manual
		out.AuthMode = "manual"
	}

	// Normalize device_os to a stable lowercase token.
	out.DeviceOS = strings.ToLower(strings.TrimSpace(out.DeviceOS))
	out.CredKey = strings.TrimSpace(out.CredKey)

	// Default logging ON unless explicitly disabled.
	// (This matches the expectation that hosts are logged unless turned off.)
	if !out.Logging {
		// We can't distinguish "unset" vs explicit false in a bool, so we treat the
		// zero value as default true at load time (see LoadHostExtras defaults).
		// Normalize keeps the current value.
	}

	out.KeyInstallPubKeyPath = strings.TrimSpace(out.KeyInstallPubKeyPath)
	if out.KeyInstallPubKeyPath == "" {
		out.KeyInstallPubKeyPath = "~/.ssh/id_rsa.pub"
	}

	out.IdentityFile = strings.TrimSpace(out.IdentityFile)

	out.NeighborDiscovery = strings.ToLower(strings.TrimSpace(out.NeighborDiscovery))
	if out.NeighborDiscovery == "" {
		out.NeighborDiscovery = "auto"
	}
	switch out.NeighborDiscovery {
	case "auto", "lldp", "cdp":
	default:
		out.NeighborDiscovery = "auto"
	}

	out.MgmtIP = strings.TrimSpace(out.MgmtIP)
	out.RouterID = strings.TrimSpace(out.RouterID)

	out.AuthorizedKeysMode = strings.ToLower(strings.TrimSpace(out.AuthorizedKeysMode))
	if out.AuthorizedKeysMode == "" {
		out.AuthorizedKeysMode = "ensure"
	}
	switch out.AuthorizedKeysMode {
	case "ensure", "replace":
	default:
		out.AuthorizedKeysMode = "ensure"
	}

	// Default to new tmux window for key management tasks (interactive and safer).
	if !out.PreferTmuxNewWindow {
		out.PreferTmuxNewWindow = true
	}

	return out
}

// ExtrasDir returns the directory where per-host extras files are stored.
// It uses XDG_CONFIG_HOME if set; otherwise ~/.config.
func ExtrasDir() (string, error) {
	xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if xdg != "" {
		return filepath.Join(xdg, "tmux-ssh-manager", "hosts"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "tmux-ssh-manager", "hosts"), nil
}

// ExtrasPathForHost returns the expected extras file path for the given host key.
// The host key is sanitized into a filename.
func ExtrasPathForHost(hostKey string) (string, error) {
	hostKey = strings.TrimSpace(hostKey)
	if hostKey == "" {
		return "", errors.New("host key is empty")
	}
	dir, err := ExtrasDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sanitizeHostKeyToFilename(hostKey)+".conf"), nil
}

// LoadHostExtras loads per-host extras for the given host key.
// If the file does not exist, it returns a default HostExtras with HostKey set.
func LoadHostExtras(hostKey string) (HostExtras, error) {
	hostKey = strings.TrimSpace(hostKey)
	if hostKey == "" {
		return HostExtras{}, errors.New("LoadHostExtras: hostKey is required")
	}

	p, err := ExtrasPathForHost(hostKey)
	if err != nil {
		return HostExtras{}, err
	}

	x := HostExtras{
		HostKey:              hostKey,
		DeviceOS:             "",
		AuthMode:             "manual",
		Logging:              true,
		KeyInstall:           false,
		AuthorizedKeysMode:   "ensure",
		PreferTmuxNewWindow:  true,
		KeyInstallPubKeyPath: "",
		IdentityFile:         "",
		NeighborDiscovery:    "auto",
		MgmtIP:               "",
		RouterID:             "",
	}

	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return x.Normalize(), nil
		}
		return HostExtras{}, fmt.Errorf("LoadHostExtras: open %s: %w", p, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Allow reasonably long lines (e.g. long paths). Keep bounded.
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 512*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		// Allow trailing comments: key=val # comment
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}

		k, v, ok := splitKV(line)
		if !ok {
			// Ignore malformed lines to keep the file tolerant.
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)

		switch k {
		case "device_os":
			x.DeviceOS = v
		case "auth_mode":
			x.AuthMode = v
		case "cred_key":
			x.CredKey = v
		case "logging":
			x.Logging = parseBool(v)
		case "key_install":
			x.KeyInstall = parseBool(v)
		case "key_install_pubkey":
			x.KeyInstallPubKeyPath = v
		case "identity_file":
			x.IdentityFile = v
		case "neighbor_discovery":
			x.NeighborDiscovery = v
		case "mgmt_ip":
			x.MgmtIP = v
		case "router_id":
			x.RouterID = v
		case "authorized_keys_mode":
			x.AuthorizedKeysMode = v
		case "prefer_tmux_new_window":
			x.PreferTmuxNewWindow = parseBool(v)
		default:
			// ignore unknown keys
		}
	}

	if err := sc.Err(); err != nil {
		return HostExtras{}, fmt.Errorf("LoadHostExtras: scan %s: %w", p, err)
	}

	return x.Normalize(), nil
}

// SaveHostExtras writes the provided HostExtras to its per-host file.
// It creates the parent directory if needed.
//
// This is useful for toggling auth_mode or key_install settings from the TUI.
func SaveHostExtras(x HostExtras) error {
	x = x.Normalize()
	if strings.TrimSpace(x.HostKey) == "" {
		return errors.New("SaveHostExtras: HostKey is required")
	}

	p, err := ExtrasPathForHost(x.HostKey)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("SaveHostExtras: mkdir %s: %w", dir, err)
	}

	// Write with restrictive permissions.
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("SaveHostExtras: open tmp: %w", err)
	}
	w := bufio.NewWriter(f)

	// Stable ordering
	fmt.Fprintln(w, "# tmux-ssh-manager per-host extras")
	fmt.Fprintln(w, "# Format: key=value. Unknown keys are ignored.")
	if strings.TrimSpace(x.DeviceOS) != "" {
		fmt.Fprintf(w, "device_os=%s\n", x.DeviceOS)
	}
	fmt.Fprintf(w, "auth_mode=%s\n", x.AuthMode)
	if x.CredKey != "" {
		fmt.Fprintf(w, "cred_key=%s\n", x.CredKey)
	}
	fmt.Fprintf(w, "logging=%s\n", formatBool(x.Logging))
	fmt.Fprintf(w, "key_install=%s\n", formatBool(x.KeyInstall))
	if x.KeyInstallPubKeyPath != "" {
		fmt.Fprintf(w, "key_install_pubkey=%s\n", x.KeyInstallPubKeyPath)
	}
	if x.IdentityFile != "" {
		fmt.Fprintf(w, "identity_file=%s\n", x.IdentityFile)
	}
	// Neighbor discovery preference for topology view (LLDP/CDP selection order)
	fmt.Fprintf(w, "neighbor_discovery=%s\n", x.NeighborDiscovery)
	if x.MgmtIP != "" {
		fmt.Fprintf(w, "mgmt_ip=%s\n", x.MgmtIP)
	}
	if x.RouterID != "" {
		fmt.Fprintf(w, "router_id=%s\n", x.RouterID)
	}
	fmt.Fprintf(w, "authorized_keys_mode=%s\n", x.AuthorizedKeysMode)
	fmt.Fprintf(w, "prefer_tmux_new_window=%s\n", formatBool(x.PreferTmuxNewWindow))

	if err := w.Flush(); err != nil {
		_ = f.Close()
		return fmt.Errorf("SaveHostExtras: flush: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("SaveHostExtras: close: %w", err)
	}

	// Atomic replace
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("SaveHostExtras: rename: %w", err)
	}
	return nil
}

// DeleteHostExtras removes the extras file for the given host key.
// It does not remove the containing directory.
func DeleteHostExtras(hostKey string) error {
	p, err := ExtrasPathForHost(hostKey)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

// sanitizeHostKeyToFilename converts a host key into a filesystem-safe filename stem.
// This is intentionally conservative.
func sanitizeHostKeyToFilename(hostKey string) string {
	hostKey = strings.TrimSpace(hostKey)
	// Replace path separators and other problematic characters.
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		" ", "_",
		"\t", "_",
	)
	hostKey = replacer.Replace(hostKey)

	// Collapse repeated underscores.
	for strings.Contains(hostKey, "__") {
		hostKey = strings.ReplaceAll(hostKey, "__", "_")
	}
	hostKey = strings.Trim(hostKey, "._-")
	if hostKey == "" {
		return "host"
	}
	return hostKey
}

func splitKV(line string) (k, v string, ok bool) {
	// Accept key=value or key: value (but prefer '=')
	if i := strings.IndexByte(line, '='); i >= 0 {
		return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
	}
	if i := strings.IndexByte(line, ':'); i >= 0 {
		return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
	}
	return "", "", false
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "y", "on", "enabled", "enable":
		return true
	default:
		return false
	}
}

func formatBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
