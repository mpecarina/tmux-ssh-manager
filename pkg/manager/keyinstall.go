package manager

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// KeyInstallMode controls how the remote ~/.ssh/authorized_keys is updated.
type KeyInstallMode string

const (
	// KeyInstallEnsure appends the key if missing (idempotent, recommended default).
	KeyInstallEnsure KeyInstallMode = "ensure"
	// KeyInstallReplace replaces authorized_keys with exactly this key (dangerous, explicit only).
	KeyInstallReplace KeyInstallMode = "replace"
)

// LocalPublicKey represents a local SSH public key file and its contents.
type LocalPublicKey struct {
	// Path is the filesystem path to the .pub file.
	Path string
	// Contents is the raw public key line as stored in the file.
	// (Single line, trimmed of trailing whitespace/newlines.)
	Contents string
	// FingerprintHint is optional, best-effort metadata for display (not computed here).
	FingerprintHint string
}

// DetectLocalPublicKeys finds common public key files under ~/.ssh and reads their contents.
//
// It returns keys in a stable priority order:
//  1. id_ed25519.pub
//  2. id_ecdsa.pub
//  3. id_rsa.pub
//  4. id_dsa.pub
//  5. any other *.pub (sorted)
//
// This function does not attempt to validate the key format beyond basic sanity checks.
func DetectLocalPublicKeys() ([]LocalPublicKey, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	sshDir := filepath.Join(home, ".ssh")

	// Preferred common keys.
	preferred := []string{
		filepath.Join(sshDir, "id_ed25519.pub"),
		filepath.Join(sshDir, "id_ecdsa.pub"),
		filepath.Join(sshDir, "id_rsa.pub"),
		filepath.Join(sshDir, "id_dsa.pub"),
	}

	seen := map[string]struct{}{}
	out := make([]LocalPublicKey, 0, 8)

	// Load preferred first (if they exist).
	for _, p := range preferred {
		if _, ok := seen[p]; ok {
			continue
		}
		k, ok, readErr := readPubKeyFile(p)
		if readErr != nil {
			// If file exists but unreadable, surface error.
			if !errors.Is(readErr, os.ErrNotExist) {
				return nil, readErr
			}
			continue
		}
		if ok {
			seen[p] = struct{}{}
			out = append(out, k)
		}
	}

	// Load any other *.pub.
	matches, _ := filepath.Glob(filepath.Join(sshDir, "*.pub"))
	sort.Strings(matches)
	for _, p := range matches {
		if _, ok := seen[p]; ok {
			continue
		}
		k, ok, readErr := readPubKeyFile(p)
		if readErr != nil {
			if !errors.Is(readErr, os.ErrNotExist) {
				return nil, readErr
			}
			continue
		}
		if ok {
			seen[p] = struct{}{}
			out = append(out, k)
		}
	}

	return out, nil
}

// ReadLocalPublicKey reads a specific public key file and returns its parsed form.
func ReadLocalPublicKey(path string) (LocalPublicKey, error) {
	path = expandPath(path)
	k, ok, err := readPubKeyFile(path)
	if err != nil {
		return LocalPublicKey{}, err
	}
	if !ok {
		return LocalPublicKey{}, fmt.Errorf("no usable public key found in %s", path)
	}
	return k, nil
}

func readPubKeyFile(path string) (LocalPublicKey, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LocalPublicKey{}, false, err
	}
	// SSH public key files are typically single-line.
	s := strings.TrimSpace(string(data))
	if s == "" {
		return LocalPublicKey{}, false, nil
	}
	// Ignore multi-line garbage; take first non-empty line.
	lines := strings.Split(s, "\n")
	line := ""
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			line = ln
			break
		}
	}
	if line == "" {
		return LocalPublicKey{}, false, nil
	}
	// Basic sanity: should start with "ssh-" or "ecdsa-" or "sk-" etc.
	if !looksLikeSSHPublicKey(line) {
		// Still allow it if user explicitly points at the file; but for auto-detect, skip.
		return LocalPublicKey{}, false, nil
	}
	return LocalPublicKey{
		Path:     path,
		Contents: line,
	}, true, nil
}

func looksLikeSSHPublicKey(line string) bool {
	// Accept common prefixes. This is intentionally permissive.
	prefixes := []string{
		"ssh-ed25519 ",
		"ssh-rsa ",
		"ecdsa-sha2-",
		"sk-ssh-ed25519 ",
		"sk-ecdsa-sha2-",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	// Also accept if it contains at least 2 fields (type + base64).
	fields := strings.Fields(line)
	return len(fields) >= 2
}

// BuildAuthorizedKeysInstallRemoteScript builds the remote `sh -lc` script that updates ~/.ssh/authorized_keys.
//
// This is intentionally exposed so higher-level workflows can run the same installer through different
// connection transports (plain ssh, ssh wrapper, SSH_ASKPASS, etc.) without trying to parse ssh argv.
//
// It also contains a best-effort remediation path for hosts where ~/.ssh or authorized_keys are not writable
// by the connecting user (e.g. incorrect ownership/mode). If permission errors occur, it will attempt:
//
//	sudo -n ... (passwordless sudo)
//
// and if that fails, it will prompt for sudo and retry.
//
// mode:
// - ensure: add key if missing (idempotent)
// - replace: replace authorized_keys with exactly this key (dangerous; explicit only)
func BuildAuthorizedKeysInstallRemoteScript(pubKey LocalPublicKey, mode KeyInstallMode) (string, error) {
	key := strings.TrimSpace(pubKey.Contents)
	if key == "" {
		return "", errors.New("empty public key contents")
	}

	m := strings.ToLower(strings.TrimSpace(string(mode)))
	if m == "" {
		m = string(KeyInstallEnsure)
	}
	if m != string(KeyInstallEnsure) && m != string(KeyInstallReplace) {
		return "", fmt.Errorf("unknown key install mode %q", mode)
	}

	// Base64 encode the key to avoid quoting issues.
	keyB64 := base64.StdEncoding.EncodeToString([]byte(key))

	// Remote script:
	// - ensures ~/.ssh exists with correct perms
	// - ensures authorized_keys exists with correct perms
	// - decodes key from base64
	// - ensure: appends if missing
	// - replace: overwrites authorized_keys (with backup)
	//
	// Remediation:
	// If we hit permission errors touching/writing $HOME/.ssh or authorized_keys, attempt to fix ownership/perms
	// using sudo (first non-interactive, then interactive). This supports environments where the user must sudo
	// to chown/chmod their own home ssh path.
	//
	// We try base64 -d (GNU) first, then base64 -D (BSD/macOS).
	remote := fmt.Sprintf(`
set -e
umask 077

AUTH="$HOME/.ssh/authorized_keys"
SSH_DIR="$HOME/.ssh"

decode_base64() {
  if command -v base64 >/dev/null 2>&1; then
    printf '%%s' "$1" | base64 -d 2>/dev/null && return 0
    printf '%%s' "$1" | base64 -D 2>/dev/null && return 0
  fi
  return 1
}

KEY_B64='%s'
KEY="$(decode_base64 "$KEY_B64" || true)"
if [ -z "$KEY" ]; then
  echo "tmux-ssh-manager: failed to decode public key" >&2
  exit 1
fi

whoami_safe() {
  (id -un 2>/dev/null || whoami 2>/dev/null || echo "") | tr -d '\r\n'
}

ensure_writable() {
  # Create directory + file and set sane perms as the connecting user.
  mkdir -p "$SSH_DIR" 2>/dev/null || return 1
  chmod 700 "$SSH_DIR" 2>/dev/null || true
  touch "$AUTH" 2>/dev/null || return 1
  chmod 600 "$AUTH" 2>/dev/null || true
  return 0
}

sudo_fix_paths() {
  U="$(whoami_safe)"
  if [ -z "$U" ]; then
    return 1
  fi

  # Ensure directory/file exist, then set ownership + perms.
  # Use $USER as group target too; that's what most Linux setups expect.
  sudo $1 mkdir -p "$SSH_DIR" &&
  sudo $1 touch "$AUTH" &&
  sudo $1 chown -R "$U:$U" "$SSH_DIR" &&
  sudo $1 chmod 700 "$SSH_DIR" &&
  sudo $1 chmod 600 "$AUTH"
}

# First attempt without sudo.
if ! ensure_writable; then
  echo "tmux-ssh-manager: ~/.ssh not writable; attempting sudo remediation..." >&2

  # Try passwordless sudo first.
  if sudo -n true >/dev/null 2>&1; then
    if sudo_fix_paths "-n" >/dev/null 2>&1; then
      : # ok
    else
      echo "tmux-ssh-manager: sudo -n remediation failed" >&2
      exit 1
    fi
  else
    # Interactive sudo: will prompt if needed (works with SSH_ASKPASS in non-interactive flows).
    if sudo_fix_paths "" ; then
      : # ok
    else
      echo "tmux-ssh-manager: sudo remediation failed (check sudo rights and path permissions)" >&2
      exit 1
    fi
  fi

  # Retry after remediation.
  ensure_writable
fi

case "%s" in
  ensure)
    # Add if missing (exact line match)
    grep -qxF "$KEY" "$AUTH" 2>/dev/null || printf '%%s\n' "$KEY" >> "$AUTH"
    ;;
  replace)
    # Backup then replace (DANGEROUS)
    TS="$(date +%%s 2>/dev/null || echo 0)"
    cp "$AUTH" "$AUTH.bak.$TS" 2>/dev/null || true
    printf '%%s\n' "$KEY" > "$AUTH"
    ;;
esac

chmod 600 "$AUTH" 2>/dev/null || true
echo "tmux-ssh-manager: authorized_keys updated (%s)"
`, keyB64, m, m)

	return remote, nil
}

// BuildAuthorizedKeysInstallCommand builds the ssh argv to install a local public key on a remote host.
//
// The returned command is intended to be executed locally (e.g., in a tmux new window) and will:
// - connect with ssh (respecting user/port/jumphost via BuildSSHCommand)
// - run a remote sh script to update ~/.ssh/authorized_keys
//
// mode:
// - ensure: add key if missing
// - replace: replace authorized_keys with exactly this key (explicit user action only)
//
// Notes:
// - This function does NOT perform the SSH itself.
// - Password prompting is handled by ssh (manual) or by separate askpass integration.
// - The remote script attempts to work on common Linux/macOS shells.
func BuildAuthorizedKeysInstallCommand(r ResolvedHost, pubKey LocalPublicKey, mode KeyInstallMode) ([]string, error) {
	remote, err := BuildAuthorizedKeysInstallRemoteScript(pubKey, mode)
	if err != nil {
		return nil, err
	}

	// BuildSSHCommand places destination before extra args; for remote command execution,
	// extra args must come after destination (ssh DEST CMD...). Our current BuildSSHCommand
	// returns: ssh [opts] dest [extraArgs...]. We can provide the remote command as "extraArgs",
	// which will be appended after destination (correct for ssh remote command).
	argv := BuildSSHCommand(r, "sh", "-lc", remote)
	return argv, nil
}

// BuildAuthorizedKeysInstallCommandForPath is a convenience wrapper that reads a local pubkey file path.
func BuildAuthorizedKeysInstallCommandForPath(r ResolvedHost, pubKeyPath string, mode KeyInstallMode) ([]string, error) {
	k, err := ReadLocalPublicKey(pubKeyPath)
	if err != nil {
		return nil, err
	}
	return BuildAuthorizedKeysInstallCommand(r, k, mode)
}
