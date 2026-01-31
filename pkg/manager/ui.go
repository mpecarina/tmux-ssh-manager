package manager

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"
)

// UIOptions controls the selector behavior (used by the Bubble Tea TUI).
type UIOptions struct {
	InitialQuery string
	ExecReplace  bool
	MaxResults   int
}

// Classic/line-oriented TUI has been removed. Bubble Tea TUI is implemented in `tui_bubble.go`.

// candidate represents a host ready for fuzzy searching and display.
type candidate struct {
	Host       Host
	Resolved   ResolvedHost
	SearchText string
	Display    string
}

// buildCandidates constructs the searchable and displayable data for all hosts.
func buildCandidates(cfg *Config) []candidate {
	cands := make([]candidate, 0, len(cfg.Hosts))
	groups := cfg.GroupByName()

	for _, h := range cfg.Hosts {
		r := cfg.ResolveEffective(h)
		var groupName string
		if g, ok := groups[h.Group]; ok {
			groupName = g.Name
		}

		// Search text: name + group + user + tags
		searchFields := []string{h.Name, groupName, r.EffectiveUser}
		if len(h.Tags) > 0 {
			searchFields = append(searchFields, strings.Join(h.Tags, " "))
		}
		searchText := strings.ToLower(strings.Join(searchFields, " "))

		// Display line
		display := formatHostLine(r)

		cands = append(cands, candidate{
			Host:       h,
			Resolved:   r,
			SearchText: searchText,
			Display:    display,
		})
	}
	return cands
}

// rankMatches filters and sorts candidates by fuzzy score against query.
//
// Query semantics (simple, fzf-like tokenization):
// - Split query on whitespace into tokens.
// - All tokens must match (AND).
// - Score is the sum of token scores (higher is better).
func rankMatches(cands []candidate, query string) []candidate {
	q := strings.TrimSpace(query)
	if q == "" {
		// No query: return a stable, alphabetical list by host name.
		out := make([]candidate, len(cands))
		copy(out, cands)
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Host.Name < out[j].Host.Name
		})
		return out
	}

	// Tokenize on whitespace, ignore empty tokens.
	// Note: This intentionally keeps the syntax simple (no quoting, no OR).
	tokens := strings.Fields(q)
	if len(tokens) == 0 {
		out := make([]candidate, len(cands))
		copy(out, cands)
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Host.Name < out[j].Host.Name
		})
		return out
	}

	type scored struct {
		c candidate
		s int
	}

	// Lowercase tokens for case-insensitive matching (SearchText is already lowercase).
	for i := range tokens {
		tokens[i] = strings.ToLower(tokens[i])
	}

	scoreds := make([]scored, 0, len(cands))
	for _, c := range cands {
		total := 0
		okAll := true
		for _, t := range tokens {
			if s, ok := fuzzyScore(t, c.SearchText); ok {
				total += s
			} else {
				okAll = false
				break
			}
		}
		if okAll {
			scoreds = append(scoreds, scored{c: c, s: total})
		}
	}

	// Sort by score (desc), then by name (asc) for stability.
	sort.SliceStable(scoreds, func(i, j int) bool {
		if scoreds[i].s != scoreds[j].s {
			return scoreds[i].s > scoreds[j].s
		}
		return scoreds[i].c.Host.Name < scoreds[j].c.Host.Name
	})

	out := make([]candidate, len(scoreds))
	for i := range scoreds {
		out[i] = scoreds[i].c
	}
	return out
}

// fuzzyScore performs a simple subsequence fuzzy match.
// Returns (score, true) if query is a subsequence of text; otherwise (0, false).
// The score rewards consecutive matches, word boundaries, and early positions.
func fuzzyScore(query, text string) (int, bool) {
	if query == "" {
		return 0, true
	}
	// Ensure both are lowercase (caller already lowercases).
	rt := []rune(text)
	rq := []rune(query)

	ti := 0
	lastPos := -1
	consecutive := 0
	score := 0
	firstPos := -1

	for _, qch := range rq {
		found := false
		for i := ti; i < len(rt); i++ {
			if rt[i] == qch {
				// Base score for a match
				score += 10
				if firstPos == -1 {
					firstPos = i
				}
				// Consecutive bonus
				if lastPos >= 0 && i == lastPos+1 {
					consecutive++
					score += 5 * consecutive // escalating bonus
				} else {
					consecutive = 0
				}
				// Word boundary bonus
				if i == 0 || !isAlphaNum(rt[i-1]) {
					score += 10
				}
				lastPos = i
				ti = i + 1
				found = true
				break
			}
		}
		if !found {
			return 0, false
		}
	}
	// Early start bonus
	if firstPos >= 0 {
		if bonus := 20 - firstPos; bonus > 0 {
			score += bonus
		}
	}
	return score, true
}

func isAlphaNum(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}

// formatHostLine renders a readable one-liner for the selector list.
func formatHostLine(r ResolvedHost) string {
	h := r.Host
	parts := []string{h.Name}
	if r.Group != nil && r.Group.Name != "" {
		parts = append(parts, fmt.Sprintf("[%s]", r.Group.Name))
	}
	if r.EffectiveUser != "" {
		parts = append(parts, "as "+r.EffectiveUser)
	}
	if r.EffectivePort != 22 && r.EffectivePort > 0 {
		parts = append(parts, fmt.Sprintf(":%d", r.EffectivePort))
	}
	if r.EffectiveJumpHost != "" {
		parts = append(parts, "via "+r.EffectiveJumpHost)
	}
	if len(h.Tags) > 0 {
		parts = append(parts, "tags:"+strings.Join(h.Tags, ","))
	}
	return strings.Join(parts, " ")
}

// connect launches SSH for the resolved host.
// If replace is true, replaces the current process with ssh (does not return on success).
// Otherwise, runs ssh as a child process and returns once it exits.
var managedSSH []*exec.Cmd

func connect(r ResolvedHost, replace bool) error {
	argv := BuildSSHCommand(r)
	if len(argv) == 0 {
		return errors.New("no ssh argv constructed")
	}

	if replace {
		// Popup/in-place connect:
		// We no longer exec-replace into ssh because we want the popup wrapper to own the ssh
		// process so it can log the full popup session output using `script -a <host daily log>`.
		//
		// To support that, we export the per-host daily log path so the wrapper can append
		// output into ~/.config/tmux-ssh-manager/logs/<hostkey>/YYYY-MM-DD.log.
		//
		// We also restore terminal state so the session has a visible cursor and predictable
		// line editing.
		restoreTerminalForExec()

		// Compute per-host daily log path and export for popup wrapper.
		if info, err := EnsureDailyHostLog(r.Host.Name, time.Now(), DefaultLogOptions()); err == nil {
			_ = os.Setenv("TMUX_SSH_MANAGER_HOST_LOG_PATH", info.Path)
		}

		// Optional: small handoff banner. Keep it minimal to avoid clutter.
		fmt.Fprintf(os.Stdout, "\r\n--- Connecting to %s ---\r\n", r.Host.Name)

		// Run ssh as a child so the wrapper can log it with `script`.
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		return nil
	}

	// Run as child
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	managedSSH = append(managedSSH, cmd)
	return nil
}

func cleanupManagedSSH() {
	for _, cmd := range managedSSH {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Process.Kill()
		}
	}
	managedSSH = nil
}

// restoreTerminalForExec best-effort restores a sane terminal state before exec-replacing
// the current process (e.g., when a Bubble Tea TUI hands off to ssh in a tmux popup).
//
// We do two things:
// 1) Emit ANSI sequences to show the cursor and reset attributes.
// 2) Run `stty sane` on the controlling TTY when available.
//
// All failures are ignored on purpose: this is a best-effort cleanup.
func restoreTerminalForExec() {
	// Show cursor + reset attributes.
	// - CSI ? 25 h  => show cursor
	// - CSI 0 m     => reset attributes
	_, _ = fmt.Fprint(os.Stdout, "\033[?25h\033[0m")

	// Try to reset tty modes. On macOS, /bin/stty exists.
	sttyPath := "/bin/stty"
	if _, err := os.Stat(sttyPath); err != nil {
		return
	}

	cmd := exec.Command(sttyPath, "sane")
	// Prefer controlling terminal so we don't break piped executions.
	if tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0); err == nil {
		defer tty.Close()
		cmd.Stdin = tty
	} else {
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run()
}

// enableTmuxPipePaneLogging enables per-host daily logging for the *current* tmux pane
// using `tmux pipe-pane`, which preserves an interactive TTY for ssh.
//
// It appends to:
//
//	~/.config/tmux-ssh-manager/logs/<hostkey>/YYYY-MM-DD.log
//
// This is best-effort; failures are recorded in the host log and returned, but callers
// may still ignore the error to avoid blocking connection.
//
// IMPORTANT:
// - Use socket-aware tmux invocations so the pipe is attached to the correct server/pane.
// - Explicitly target the current pane id (#{pane_id}) rather than relying on implicit targeting.
// - Verify tmux reports pipe=1 after enabling, and emit a marker line to the pane output stream.
func enableTmuxPipePaneLogging(hostKey string) error {
	hostKey = strings.TrimSpace(hostKey)
	if hostKey == "" {
		return errors.New("empty host key")
	}

	// Only attempt inside tmux.
	if os.Getenv("TMUX") == "" {
		return nil
	}

	// Resolve current pane id using socket-aware tmux calls.
	paneID, err := ResolveCurrentPaneID()
	if err != nil {
		_, _ = AppendHostLogLine(hostKey, time.Now(), DefaultLogOptions(),
			fmt.Sprintf("log: failed to resolve current pane id: %v", err))
		return err
	}

	// Compute per-host daily log path (local time, daily rotation).
	info, err := EnsureDailyHostLog(hostKey, time.Now(), DefaultLogOptions())
	if err != nil {
		return err
	}

	// Enable pipe-pane using socket-aware tmux.
	if err := EnablePipePaneAppend(paneID, info.Path); err != nil {
		_, _ = AppendHostLogLine(hostKey, time.Now(), DefaultLogOptions(),
			fmt.Sprintf("log: failed to enable tmux pipe-pane for pane %s: %v", paneID, err))
		return err
	}

	// Verify (best-effort). Some tmux builds do not populate pane_pipe_path; pane_pipe is the key signal.
	if pipeOn, _, vErr := VerifyPipePane(paneID); vErr == nil {
		if !pipeOn {
			_, _ = AppendHostLogLine(hostKey, time.Now(), DefaultLogOptions(),
				fmt.Sprintf("log: pipe-pane verify failed for pane %s (pane_pipe=0)", paneID))
		}
	}

	_, _ = AppendHostLogLine(hostKey, time.Now(), DefaultLogOptions(),
		fmt.Sprintf("log: enabled tmux pipe-pane for pane %s", paneID))
	return nil
}

// Note: shellEscapeForSh is implemented in `tui_bubble.go` and shared across the package.
// Keep only one implementation to avoid duplicate symbol errors.

func printHeader() {
	fmt.Println("tmux-ssh-manager: Session Selector")
	fmt.Println("----------------------------------")
}

// clearScreen clears the terminal using ANSI escape codes.
func clearScreen() {
	// ESC[2J clears screen; ESC[H moves cursor to top-left
	fmt.Print("\033[2J\033[H")
}

// waitForEnter prompts the user to press Enter, useful after an error.
func waitForEnter(r *bufio.Reader) {
	fmt.Print("Press Enter to continue...")
	r.ReadString('\n')
}
