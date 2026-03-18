package tmuxrun

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Session struct {
	AskpassScript string
	HostUsers     map[string]string
	HasCredential func(alias string) bool
}

func InTmux() bool {
	return strings.TrimSpace(os.Getenv("TMUX")) != ""
}

func socketPath() string {
	value := strings.TrimSpace(os.Getenv("TMUX"))
	if value == "" {
		return ""
	}
	if index := strings.IndexByte(value, ','); index >= 0 {
		return value[:index]
	}
	return value
}

func (Session) Command(args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+2)
	if socket := socketPath(); socket != "" {
		full = append(full, "-S", socket)
	}
	full = append(full, args...)
	return exec.Command("tmux", full...)
}

func (s Session) Run(args ...string) error {
	cmd := s.Command(args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("tmux %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

func (s Session) output(args ...string) (string, error) {
	cmd := s.Command(args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("tmux %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func SSHCommand(alias string) string {
	return fmt.Sprintf("exec ssh %s", shellQuote(alias))
}

func (s Session) sshCommand(alias string) string {
	if s.AskpassScript != "" && s.HasCredential != nil && s.HasCredential(alias) {
		user := ""
		if s.HostUsers != nil {
			user = s.HostUsers[alias]
		}
		return fmt.Sprintf(
			"export TSSM_HOST=%s TSSM_USER=%s SSH_ASKPASS=%s SSH_ASKPASS_REQUIRE=force DISPLAY=1; exec ssh -o PubkeyAuthentication=no -o PreferredAuthentications=keyboard-interactive,password %s",
			shellQuote(alias), shellQuote(user), shellQuote(s.AskpassScript), shellQuote(alias),
		)
	}
	return SSHCommand(alias)
}

func loginShell() string {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell
	}
	return "sh"
}

func (s Session) NewWindow(alias string) error {
	paneID, err := s.output("new-window", "-P", "-F", "#{pane_id}", "-n", alias, loginShell(), "-lc", s.sshCommand(alias))
	if err != nil {
		return err
	}
	s.setupLogging(paneID, alias)
	return nil
}

func (s Session) SplitVertical(alias string) error {
	paneID, err := s.output("split-window", "-P", "-F", "#{pane_id}", "-v", "-c", "#{pane_current_path}", loginShell(), "-lc", s.sshCommand(alias))
	if err != nil {
		return err
	}
	s.setupLogging(paneID, alias)
	return nil
}

func (s Session) SplitHorizontal(alias string) error {
	paneID, err := s.output("split-window", "-P", "-F", "#{pane_id}", "-h", "-c", "#{pane_current_path}", loginShell(), "-lc", s.sshCommand(alias))
	if err != nil {
		return err
	}
	s.setupLogging(paneID, alias)
	return nil
}

// Tiled opens multiple hosts in a single tmux window with a tiled layout.
// The first alias gets a new window; remaining aliases are added as vertical
// splits. After each split, select-layout is called with the given layout
// (default "tiled") to continuously rebalance panes.
func (s Session) Tiled(aliases []string, layout string) error {
	if len(aliases) == 0 {
		return nil
	}
	if layout == "" {
		layout = "tiled"
	}

	// First host → new window.
	windowID, err := s.output("new-window", "-P", "-F", "#{window_id}", "-n", "tiled", loginShell(), "-lc", s.sshCommand(aliases[0]))
	if err != nil {
		return err
	}
	// Also get the pane ID of the first window for logging.
	if paneID, perr := s.output("display-message", "-p", "-t", windowID, "#{pane_id}"); perr == nil {
		s.setupLogging(paneID, aliases[0])
	}

	// Remaining hosts → splits within that window.
	for _, alias := range aliases[1:] {
		paneID, serr := s.output("split-window", "-P", "-F", "#{pane_id}", "-v", "-t", windowID, loginShell(), "-lc", s.sshCommand(alias))
		if serr != nil {
			return serr
		}
		s.setupLogging(paneID, alias)
		// Rebalance after each split.
		_ = s.Run("select-layout", "-t", windowID, layout)
	}

	// Final layout pass.
	_ = s.Run("select-layout", "-t", windowID, layout)
	return nil
}

// SelectLayout applies a tmux layout to the current window.
func (s Session) SelectLayout(layout string) error {
	return s.Run("select-layout", layout)
}

// SetupPaneLogging enables pipe-pane logging on the current tmux pane.
func (s Session) SetupPaneLogging(alias string) {
	if !InTmux() {
		return
	}
	paneID, err := s.output("display-message", "-p", "#{pane_id}")
	if err != nil {
		return
	}
	s.setupLogging(paneID, alias)
}

func (s Session) setupLogging(paneID, alias string) {
	logPath, err := ensureLogFile(alias)
	if err != nil {
		return
	}
	_ = s.Run("pipe-pane", "-t", paneID, "-o", "cat >> "+shellQuote(logPath))
}

// LogDir returns the log directory path for a host alias.
func LogDir(alias string) (string, error) {
	base, err := logsBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, sanitizeAlias(alias)), nil
}

func logsBaseDir() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "tmux-ssh-manager", "logs"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "tmux-ssh-manager", "logs"), nil
}

func ensureLogFile(alias string) (string, error) {
	dir, err := LogDir(alias)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	filename := time.Now().Format("2006-01-02") + ".log"
	path := filepath.Join(dir, filename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	f.Close()
	return path, nil
}

func sanitizeAlias(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
