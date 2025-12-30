package manager

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// tmuxwrap.go
//
// Socket-aware tmux command runner.
//
// Why this exists:
// - tmux popups / run-shell contexts can execute with different client env,
//   and invoking `tmux` without explicitly targeting the correct server socket
//   can lead to commands that appear to succeed but do not affect the pane you
//   think they do (e.g., pipe-pane not actually attaching).
//
// - The TMUX environment variable contains the server socket path plus metadata:
//     TMUX=/private/tmp/tmux-502/default,35218,0
//   The socket path is the portion before the first comma.
//   Using `tmux -S <socket>` forces commands to the correct server.
//
// This file provides:
// - TmuxCmd: builds an exec.Cmd for tmux using the current socket (when available)
// - TmuxOutput / TmuxRun: convenience wrappers with combined stdout/stderr
// - EnablePipePaneAppend: enables pipe-pane appending to a file for a given pane id
// - VerifyPipePane: checks #{pane_pipe} and (optionally) #{pane_pipe_path}
// - ResolveCurrentPaneID: resolves #{pane_id} explicitly
//
// NOTE: This file does not assume any particular UI; it is safe to use from both
// Bubble Tea and classic code paths.
//
// Security:
// - Do not pass secrets via tmux commands.
// - File paths are treated as trusted app-controlled paths; if you allow user-provided
//   paths, validate/clean them first.

var ErrNotInTmux = errors.New("not in tmux")

// TmuxSocketPathFromEnv parses $TMUX and returns the socket path portion.
// If TMUX is empty or malformed, returns "".
func TmuxSocketPathFromEnv() string {
	t := strings.TrimSpace(os.Getenv("TMUX"))
	if t == "" {
		return ""
	}
	// TMUX format: <socket_path>,<server_pid>,<client_id>
	if i := strings.IndexByte(t, ','); i >= 0 {
		return t[:i]
	}
	// Sometimes TMUX may contain just the socket path.
	return t
}

// HaveTmuxServer returns true if we can plausibly talk to tmux.
// It does not guarantee a specific client/pane context.
func HaveTmuxServer() bool {
	// If TMUX is set, we assume tmux is present.
	if TmuxSocketPathFromEnv() != "" {
		return true
	}
	// Otherwise, attempt a cheap `tmux -V`.
	cmd := exec.Command("tmux", "-V")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// TmuxCmd creates an exec.Cmd to run tmux with socket-awareness when possible.
func TmuxCmd(args ...string) (*exec.Cmd, error) {
	if len(args) == 0 {
		return nil, errors.New("tmux: empty args")
	}

	socket := TmuxSocketPathFromEnv()
	full := make([]string, 0, len(args)+2)

	if socket != "" {
		// Force correct tmux server socket.
		full = append(full, "-S", socket)
	} else {
		// Not necessarily fatal; tmux might still be reachable via default socket.
		// But for actions that must affect a specific pane, callers should handle ErrNotInTmux.
	}

	full = append(full, args...)
	cmd := exec.Command("tmux", full...)
	// Do not inherit stdin by default; most tmux commands don't need it.
	cmd.Stdin = nil
	return cmd, nil
}

// TmuxOutput runs a tmux command and returns stdout (trimmed) or an error containing stderr.
func TmuxOutput(args ...string) (string, error) {
	cmd, err := TmuxCmd(args...)
	if err != nil {
		return "", err
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = runErr.Error()
		}
		return "", fmt.Errorf("tmux %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// TmuxRun runs a tmux command and returns a rich error message on failure.
func TmuxRun(args ...string) error {
	_, err := TmuxOutput(args...)
	return err
}

// ResolveCurrentPaneID returns the current pane id (e.g. "%0") in this tmux client context.
func ResolveCurrentPaneID() (string, error) {
	if !HaveTmuxServer() {
		return "", ErrNotInTmux
	}
	out, err := TmuxOutput("display-message", "-p", "#{pane_id}")
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", errors.New("tmux returned empty pane_id")
	}
	return out, nil
}

// ShellEscapeSingleQuotes escapes a string for safe use in a single-argument sh command line,
// using the classic single-quote strategy.
// Example: /tmp/foo'bar -> '/tmp/foo'"'"'bar'
func ShellEscapeSingleQuotes(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexByte(s, '\'') < 0 {
		return "'" + s + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// EnablePipePaneAppend enables pipe-pane logging for a specific pane.
// - paneID must be a tmux pane id like "%0".
// - logPath must be an absolute path to the daily log file.
// - It uses `-o` to avoid stacking multiple pipes.
func EnablePipePaneAppend(paneID, logPath string) error {
	paneID = strings.TrimSpace(paneID)
	logPath = strings.TrimSpace(logPath)

	if paneID == "" {
		return errors.New("pipe-pane: empty pane id")
	}
	if logPath == "" {
		return errors.New("pipe-pane: empty log path")
	}
	if !HaveTmuxServer() {
		return ErrNotInTmux
	}

	// Use the simplest working form; tmux runs this via /bin/sh -c internally.
	// Avoid extra shell layers (sh -lc) to reduce quoting/parsing issues.
	//
	// We quote the log path to be safe even if path contains spaces.
	pipeCmd := "cat >> " + ShellEscapeSingleQuotes(logPath)

	// tmux pipe-pane -t %0 -o "<pipeCmd>"
	if err := TmuxRun("pipe-pane", "-t", paneID, "-o", pipeCmd); err != nil {
		return err
	}

	// Emit a marker into pane output stream to validate end-to-end capture.
	// This should appear in the log if piping is working.
	_ = TmuxRun("display-message", "-t", paneID, fmt.Sprintf("tmux-ssh-manager: pipe-pane enabled (%s)", time.Now().Format(time.RFC3339)))
	return nil
}

// VerifyPipePane checks whether pipe-pane is enabled for paneID.
// Returns (pipeOn, pipePath, error).
//
// Note: #{pane_pipe_path} is not always available/populated depending on tmux version/build.
// We return pipePath as best-effort; it may be empty even when pipeOn is true.
func VerifyPipePane(paneID string) (bool, string, error) {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return false, "", errors.New("verify: empty pane id")
	}
	if !HaveTmuxServer() {
		return false, "", ErrNotInTmux
	}

	pipeVal, err := TmuxOutput("display-message", "-p", "-t", paneID, "#{pane_pipe}")
	if err != nil {
		return false, "", err
	}
	pipeOn := strings.TrimSpace(pipeVal) == "1"

	// Best-effort: may be empty or unsupported.
	pipePath, _ := TmuxOutput("display-message", "-p", "-t", paneID, "#{pane_pipe_path}")
	return pipeOn, strings.TrimSpace(pipePath), nil
}

// DisablePipePane disables an existing pipe-pane for paneID (best-effort).
func DisablePipePane(paneID string) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return errors.New("disable: empty pane id")
	}
	if !HaveTmuxServer() {
		return ErrNotInTmux
	}
	// tmux pipe-pane -t %0  (no command) => disables
	return TmuxRun("pipe-pane", "-t", paneID)
}
