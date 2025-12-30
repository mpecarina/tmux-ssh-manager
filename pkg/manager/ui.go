package manager

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// UIOptions controls the selector behavior.
type UIOptions struct {
	// InitialQuery pre-populates the search box.
	InitialQuery string
	// ExecReplace, if true, replaces the current process with ssh (syscall.Exec).
	// If false, it runs ssh as a child process and returns to the selector after it exits.
	ExecReplace bool
	// MaxResults limits how many matches are shown. If 0, defaults to 20.
	MaxResults int
}

// RunTUI launches an interactive selector with vim-like motions and tmux-aware actions.
//
// Enhancements for network engineers starting with tmux:
// - Vim motions: j/k to move, gg to top, G to bottom, u/d to half-page
// - Enter or c: connect in current pane
// - v: split vertically (side-by-side) and connect (tmux split-window -h)
// - s: split horizontally (stacked) and connect (tmux split-window -v)
// - w: new window and connect
// - y: yank the ssh command to tmux buffer
// - /: refine search; ?: help overlay; q: quit
func RunTUIClassic(cfg *Config, opts UIOptions) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	if len(cfg.Hosts) == 0 {
		fmt.Fprintln(os.Stderr, "No hosts defined in configuration.")
		return nil
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = 20
	}

	// Build candidates from config once.
	candidates := buildCandidates(cfg)

	defer cleanupManagedSSH()

	reader := bufio.NewReader(os.Stdin)

	// Local helpers
	shellQuoteCmd := func(argv []string) string {
		isShellSpecial := func(r rune) bool {
			switch r {
			case ' ', '\t', '\n', '"', '\'', '\\', '$', '`', '&', '|', ';', '<', '>', '(', ')', '{', '}', '*', '?', '!', '~', '#':
				return true
			default:
				return false
			}
		}
		quoted := make([]string, 0, len(argv))
		for _, a := range argv {
			if a == "" {
				quoted = append(quoted, "''")
				continue
			}
			if strings.IndexFunc(a, isShellSpecial) >= 0 {
				quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", "'\"'\"'")+"'")
			} else {
				quoted = append(quoted, a)
			}
		}
		return strings.Join(quoted, " ")
	}

	tmuxSplitH := func(cmdline string) error {
		// Side-by-side split (vim 'v' split)
		cmd := exec.Command("tmux", "split-window", "-h", "-c", "#{pane_current_path}", "bash", "-lc", cmdline)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	tmuxSplitV := func(cmdline string) error {
		// Stacked split (vim 's' split notion here)
		cmd := exec.Command("tmux", "split-window", "-v", "-c", "#{pane_current_path}", "bash", "-lc", cmdline)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	tmuxNewWindow := func(cmdline string) error {
		cmd := exec.Command("tmux", "new-window", "-c", "#{pane_current_path}", "bash", "-lc", cmdline)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	tmuxYank := func(text string) error {
		// Put in tmux buffer for later paste; short message for feedback
		if err := exec.Command("tmux", "set-buffer", "--", text).Run(); err != nil {
			return err
		}
		_ = exec.Command("tmux", "display-message", "-d", "1500", "Yanked SSH command to tmux buffer").Run()
		return nil
	}

	// UI state
	query := strings.TrimSpace(opts.InitialQuery)
	selected := 0
	scroll := 0
	showHelp := false

	for {
		clearScreen()
		printHeader()

		if showHelp {
			fmt.Println("Help (vim-like motions):")
			fmt.Println("------------------------")
			fmt.Println("  Navigation:")
			fmt.Println("    j/k  - down/up")
			fmt.Println("    gg   - top")
			fmt.Println("    G    - bottom")
			fmt.Println("    u/d  - half-page up/down")
			fmt.Println("    /    - search/filter")
			fmt.Println()
			fmt.Println("  Actions on selected:")
			fmt.Println("    Enter or c  - connect in current pane")
			fmt.Println("    v           - vertical split (side-by-side) and connect")
			fmt.Println("    s           - horizontal split (stacked) and connect")
			fmt.Println("    w           - new window and connect")
			fmt.Println("    y           - yank ssh command to tmux buffer")
			fmt.Println()
			fmt.Println("  Misc:")
			fmt.Println("    ?           - toggle this help")
			fmt.Println("    q           - quit")
			fmt.Println()
			fmt.Print("Press Enter to return...")
			_, _ = reader.ReadString('\n')
			showHelp = false
			continue
		}

		if query == "" {
			fmt.Println("Search: (type to filter hosts; or enter '/' to start searching)")
		} else {
			fmt.Printf("Search: %s\n", query)
		}
		fmt.Println()

		// Compute matches and clamp selection
		matches := rankMatches(candidates, query)
		if selected >= len(matches) {
			selected = len(matches) - 1
		}
		if selected < 0 {
			selected = 0
		}

		if len(matches) == 0 {
			fmt.Println("No matches.")
		} else {
			limit := opts.MaxResults
			if limit <= 0 {
				limit = 20
			}
			// Ensure selected is visible; adjust scroll
			if selected < scroll {
				scroll = selected
			}
			if selected >= scroll+limit {
				scroll = selected - limit + 1
			}
			if scroll < 0 {
				scroll = 0
			}
			end := scroll + limit
			if end > len(matches) {
				end = len(matches)
			}
			for i := scroll; i < end; i++ {
				prefix := "   "
				if i == selected {
					prefix = " > "
				}
				fmt.Printf("%s%2d) %s\n", prefix, i+1, matches[i].Display)
			}
			if end < len(matches) {
				fmt.Printf("... and %d more. Use j/k, u/d, gg, G to navigate.\n", len(matches)-end)
			}
		}
		fmt.Println()
		fmt.Println("Motions: j/k, gg, G, u/d   Search: /query   Help: ?   Quit: q")
		fmt.Println("Actions: Enter/c connect | v split-vert | s split-horiz | w new-window | y yank")
		fmt.Print("> ")

		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)

		// Global commands
		if line == "q" || strings.EqualFold(line, "quit") || strings.EqualFold(line, "exit") {
			return nil
		}
		if line == "?" {
			showHelp = true
			continue
		}
		if strings.HasPrefix(line, "/") {
			query = strings.TrimSpace(strings.TrimPrefix(line, "/"))
			// reset selection and scroll on new search
			selected, scroll = 0, 0
			continue
		}

		// Motions
		switch line {
		case "j":
			if selected+1 < len(matches) {
				selected++
			}
			continue
		case "k":
			if selected > 0 {
				selected--
			}
			continue
		case "gg":
			selected, scroll = 0, 0
			continue
		case "G":
			if len(matches) > 0 {
				selected = len(matches) - 1
				// let loop adjust scroll on next render
			}
			continue
		case "u":
			half := opts.MaxResults / 2
			if half <= 0 {
				half = 10
			}
			selected -= half
			if selected < 0 {
				selected = 0
			}
			continue
		case "d":
			half := opts.MaxResults / 2
			if half <= 0 {
				half = 10
			}
			selected += half
			if selected >= len(matches) {
				selected = len(matches) - 1
			}
			continue
		}

		// Empty line: connect selected (if any)
		if line == "" {
			if len(matches) == 0 {
				continue
			}
			if err := connect(matches[selected].Resolved, opts.ExecReplace); err != nil {
				fmt.Fprintf(os.Stderr, "ssh error: %v\n", err)
				waitForEnter(reader)
			}
			if opts.ExecReplace {
				return nil
			}
			continue
		}

		// Actions with selection (if any)
		if len(matches) > 0 {
			cur := matches[selected].Resolved
			argv := BuildSSHCommand(cur)
			cmdline := shellQuoteCmd(argv)

			switch line {
			case "c":
				if err := connect(cur, opts.ExecReplace); err != nil {
					fmt.Fprintf(os.Stderr, "ssh error: %v\n", err)
					waitForEnter(reader)
				}
				if opts.ExecReplace {
					return nil
				}
				continue
			case "v":
				if err := tmuxSplitH(cmdline); err != nil {
					fmt.Fprintf(os.Stderr, "tmux split-window -h error: %v\n", err)
					waitForEnter(reader)
				}
				continue
			case "s":
				if err := tmuxSplitV(cmdline); err != nil {
					fmt.Fprintf(os.Stderr, "tmux split-window -v error: %v\n", err)
					waitForEnter(reader)
				}
				continue
			case "w":
				if err := tmuxNewWindow(cmdline); err != nil {
					fmt.Fprintf(os.Stderr, "tmux new-window error: %v\n", err)
					waitForEnter(reader)
				}
				continue
			case "y":
				if err := tmuxYank(cmdline); err != nil {
					fmt.Fprintf(os.Stderr, "tmux set-buffer error: %v\n", err)
					waitForEnter(reader)
				}
				continue
			}
		}

		// Numerical selection: connect immediately if valid
		if n, err := strconv.Atoi(line); err == nil {
			if n > 0 && n <= len(matches) {
				cur := matches[n-1].Resolved
				if err := connect(cur, opts.ExecReplace); err != nil {
					fmt.Fprintf(os.Stderr, "ssh error: %v\n", err)
					waitForEnter(reader)
				}
				if opts.ExecReplace {
					return nil
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "Invalid selection: %s\n", line)
			waitForEnter(reader)
			continue
		}

		// Otherwise treat input as a new query (fuzzy-search friendly)
		query = line
		selected, scroll = 0, 0
	}
}

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

	type scored struct {
		c candidate
		s int
	}
	lowQ := strings.ToLower(q)
	scoreds := make([]scored, 0, len(cands))
	for _, c := range cands {
		if s, ok := fuzzyScore(lowQ, c.SearchText); ok {
			scoreds = append(scoreds, scored{c: c, s: s})
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
