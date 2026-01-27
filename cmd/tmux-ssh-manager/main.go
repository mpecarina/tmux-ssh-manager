package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"tmux-ssh-manager/pkg/manager"
)

var (
	flagConfig       string
	flagHost         string
	flagExecReplace  bool
	flagList         bool
	flagInitialQuery string
	flagMaxResults   int
	flagPrintConfig  bool
	flagDryRun       bool

	flagTUISource string // "yaml" (default) or "ssh"
	flagSSHConfig string // comma-separated paths; defaults to ~/.ssh/config when empty

	flagSplitCount  int
	flagSplitLayout string
	flagSplitMode   string
)

func init() {
	flag.StringVar(&flagConfig, "config", "", "Path to YAML config (defaults to XDG paths if empty)")
	flag.StringVar(&flagHost, "host", "", "Connect directly to a host (config name or literal destination like user@host)")
	flag.BoolVar(&flagExecReplace, "exec-replace", false, "Replace this process with ssh")
	flag.BoolVar(&flagList, "list", false, "List hosts and exit")
	flag.StringVar(&flagInitialQuery, "query", "", "Initial query for the TUI")
	flag.IntVar(&flagMaxResults, "max", 20, "Max results in the TUI")
	flag.BoolVar(&flagPrintConfig, "print-config-path", false, "Print resolved config path(s) and exit")
	flag.BoolVar(&flagDryRun, "dry-run", false, "Print the ssh command and exit")

	flag.IntVar(&flagSplitCount, "split-count", 0, "With --host inside tmux: open N connections (1=single; >1 creates panes/windows)")
	flag.StringVar(&flagSplitMode, "split-mode", "window", "With --split-count>0: window|v|h")
	flag.StringVar(&flagSplitLayout, "split-layout", "", "With --split-count>1 and split-mode v/h: tmux layout name or raw layout string")

	flag.StringVar(&flagTUISource, "tui-source", "yaml", "TUI source: yaml|ssh")
	flag.StringVar(&flagSSHConfig, "ssh-config", "", "SSH config path(s), comma-separated (default: ~/.ssh/config)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "tmux-ssh-manager\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  tmux-ssh-manager [options]\n")
		fmt.Fprintf(os.Stderr, "  tmux-ssh-manager --host <name-or-destination> [-- <extra ssh args...>]\n")
		fmt.Fprintf(os.Stderr, "  tmux-ssh-manager cred <set|get|delete> --host <alias>\n")
		fmt.Fprintf(os.Stderr, "  tmux-ssh-manager ssh <ssh-style args...>\n")
		fmt.Fprintf(os.Stderr, "  tmux-ssh-manager scp <scp-style args...>\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  tmux-ssh-manager
  tmux-ssh-manager --tui-source ssh
  tmux-ssh-manager --tui-source ssh --ssh-config ~/.ssh/config,~/.ssh/config.d/*.conf
  tmux-ssh-manager --host user@host -- -p 2222
  tmux-ssh-manager cred set --host leaf01.lab.local
`)
	}
}

func main() {
	flag.Parse()
	sshArgs := flag.Args()

	if flag.NArg() >= 1 {
		switch flag.Arg(0) {
		case "__askpass":
			if err := runAskpassSubcommand(flag.Args()[1:]); err != nil {
				fmt.Fprintf(os.Stderr, "tmux-ssh-manager: %v\n", err)
				os.Exit(exitCodeFromErr(err))
			}
			return

		case "__connect":
			if err := runConnectSubcommand(flag.Args()[1:]); err != nil {
				fmt.Fprintf(os.Stderr, "tmux-ssh-manager: %v\n", err)
				os.Exit(exitCodeFromErr(err))
			}
			return

		case "__hostextras-switch-to-identity":
			if err := runHostExtrasSwitchToIdentitySubcommand(flag.Args()[1:]); err != nil {
				fmt.Fprintf(os.Stderr, "tmux-ssh-manager: %v\n", err)
				os.Exit(exitCodeFromErr(err))
			}
			return

		case "cred":
			if err := runCredSubcommand(flag.Args()[1:]); err != nil {
				fmt.Fprintf(os.Stderr, "tmux-ssh-manager: %v\n", err)
				os.Exit(exitCodeFromErr(err))
			}
			return

		case "ssh":
			var cfg *manager.Config
			if strings.EqualFold(flagTUISource, "ssh") {
				var sshPaths []string
				if strings.TrimSpace(flagSSHConfig) != "" {
					for _, p := range strings.Split(flagSSHConfig, ",") {
						p = strings.TrimSpace(p)
						if p != "" {
							sshPaths = append(sshPaths, p)
						}
					}
				}
				if conf, err := manager.LoadConfigFromSSH(sshPaths...); err == nil {
					cfg = conf
				}
			} else {
				if conf, _, err := manager.LoadConfig(flagConfig); err == nil {
					cfg = conf
				}
			}

			if err := runSSHWrapperSubcommand(cfg, flag.Args()[1:], flagExecReplace); err != nil {
				fmt.Fprintf(os.Stderr, "tmux-ssh-manager: %v\n", err)
				os.Exit(exitCodeFromErr(err))
			}
			return

		case "scp":
			var cfg *manager.Config
			if strings.EqualFold(flagTUISource, "ssh") {
				var sshPaths []string
				if strings.TrimSpace(flagSSHConfig) != "" {
					for _, p := range strings.Split(flagSSHConfig, ",") {
						p = strings.TrimSpace(p)
						if p != "" {
							sshPaths = append(sshPaths, p)
						}
					}
				}
				if conf, err := manager.LoadConfigFromSSH(sshPaths...); err == nil {
					cfg = conf
				}
			} else {
				if conf, _, err := manager.LoadConfig(flagConfig); err == nil {
					cfg = conf
				}
			}

			if err := runSCPWrapperSubcommand(cfg, flag.Args()[1:], flagExecReplace); err != nil {
				fmt.Fprintf(os.Stderr, "tmux-ssh-manager: %v\n", err)
				os.Exit(exitCodeFromErr(err))
			}
			return
		}
	}

	// Select TUI source and load configuration accordingly.
	var cfg *manager.Config
	var cfgPath string
	var cfgErr error

	// Capture SSH config paths (if any) so we can reference them in messages.
	var sshPaths []string
	if strings.TrimSpace(flagSSHConfig) != "" {
		for _, p := range strings.Split(flagSSHConfig, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				sshPaths = append(sshPaths, p)
			}
		}
	}

	if strings.EqualFold(flagTUISource, "ssh") {
		// Build a Config from SSH config aliases
		conf, err := manager.LoadConfigFromSSH(sshPaths...)
		if err != nil {
			cfgErr = err
		} else {
			cfg = conf
			if len(sshPaths) > 0 {
				cfgPath = strings.Join(sshPaths, ", ")
			} else {
				cfgPath = "~/.ssh/config"
			}
		}
	} else {
		// Default: YAML config
		cfg, cfgPath, cfgErr = manager.LoadConfig(flagConfig)
	}

	_ = sshArgs

	if cfgErr != nil {
		// If direct-connect mode is requested, allow running without config.
		if flagHost == "" && !flagList && !flagPrintConfig {
			// If YAML config is missing, implicitly fall back to SSH-alias sourced TUI.
			// This makes `tmux-ssh-manager` runnable from a normal shell without requiring hosts.yaml.
			if !strings.EqualFold(flagTUISource, "ssh") {
				flagTUISource = "ssh"
			}
			conf, err := manager.LoadConfigFromSSH(sshPaths...)
			if err != nil {
				fmt.Fprintf(os.Stderr, "tmux-ssh-manager: failed to load SSH config for TUI: %v\n", err)
				if len(sshPaths) > 0 {
					fmt.Fprintf(os.Stderr, "Checked SSH config paths: %s\n", strings.Join(sshPaths, ", "))
				} else {
					fmt.Fprintf(os.Stderr, "Checked default SSH config path: ~/.ssh/config (and included files)\n")
				}
				os.Exit(1)
			}
			cfg = conf
			if len(sshPaths) > 0 {
				cfgPath = strings.Join(sshPaths, ", ")
			} else {
				cfgPath = "~/.ssh/config"
			}
			cfgErr = nil
		} else {
			// In direct or list/print mode we can proceed with limited behavior.
			cfg = nil
			cfgPath = ""
		}
	}

	if flagPrintConfig {
		if cfgPath == "" {
			fmt.Println("(no config found)")
		} else {
			fmt.Println(cfgPath)
		}
		return
	}

	if flagList {
		if cfg == nil || len(cfg.Hosts) == 0 {
			if strings.EqualFold(flagTUISource, "ssh") {
				fmt.Println("(no SSH aliases found)")
			} else {
				fmt.Println("(no hosts found in configuration)")
			}
			return
		}
		for _, h := range cfg.Hosts {
			r := cfg.ResolveEffective(h)
			line := hostLine(r)
			fmt.Println(line)
		}
		return
	}

	if flagHost != "" {
		// Direct-connect with optional tmux fanout (single host -> N panes/windows)
		if strings.TrimSpace(os.Getenv("TMUX")) != "" && flagSplitCount > 0 {
			if err := runDirectConnectWithSplits(cfg, flagHost, flagSplitCount, flagSplitMode, flagSplitLayout, flagExecReplace, flagDryRun, sshArgs); err != nil {
				fmt.Fprintf(os.Stderr, "tmux-ssh-manager: %v\n", err)
				os.Exit(exitCodeFromErr(err))
			}
			return
		}

		if err := runDirectConnect(cfg, flagHost, flagExecReplace, flagDryRun, sshArgs); err != nil {
			fmt.Fprintf(os.Stderr, "tmux-ssh-manager: %v\n", err)
			os.Exit(exitCodeFromErr(err))
		}
		return
	}

	// TUI mode
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "tmux-ssh-manager: no configuration available for TUI mode")
		os.Exit(1)
	}
	opts := manager.UIOptions{
		InitialQuery: flagInitialQuery,
		ExecReplace:  flagExecReplace,
		MaxResults:   flagMaxResults,
	}
	if err := manager.RunTUI(cfg, opts); err != nil {
		fmt.Fprintf(os.Stderr, "tmux-ssh-manager: %v\n", err)
		os.Exit(exitCodeFromErr(err))
	}
}

func runDirectConnect(cfg *manager.Config, hostArg string, execReplace, dryRun bool, extraArgs []string) error {
	hostArg = strings.TrimSpace(hostArg)

	// For direct-connect, resolve effective ssh config (user/hostname/port) when caller doesn't specify user@host.
	// This makes behavior consistent with `ssh` itself by using `ssh -G`.
	destUser := ""
	destHost := hostArg
	if at := strings.IndexByte(hostArg, '@'); at >= 0 {
		destUser = strings.TrimSpace(hostArg[:at])
		destHost = strings.TrimSpace(hostArg[at+1:])
	}
	if destHost == "" {
		return errors.New("empty host")
	}

	hostKey := destHost // what the user typed (ssh alias / host token)
	eff := sshEffectiveConfig(destHost)
	if destUser == "" && eff.User != "" {
		destUser = eff.User
	}
	// For literal ssh, the destination host token should remain what the user typed.
	// But when we have a resolved config host name, prefer it for credential lookup and config match.
	credHostKey := hostKey
	if eff.HostName != "" {
		credHostKey = eff.HostName
	}

	// Try to find host in config first (if available).
	// Note: config hosts are keyed by Host.Name; we try both the typed token and the resolved HostName.
	if cfg != nil {
		var h *manager.Host
		if h = cfg.HostByName(hostKey); h == nil && eff.HostName != "" {
			h = cfg.HostByName(eff.HostName)
		}
		if h != nil {
			r := cfg.ResolveEffective(*h)

			// Ensure effective user matches ssh config if present.
			if strings.TrimSpace(destUser) != "" {
				r.EffectiveUser = strings.TrimSpace(destUser)
			}
			// Ensure port matches ssh config if present.
			if eff.Port > 0 {
				r.EffectivePort = eff.Port
			}
			// Ensure host key matches ssh config HostName if present (so ssh options are applied consistently).
			if eff.HostName != "" {
				r.Host.Name = eff.HostName
			}

			argv := manager.BuildSSHCommand(r, extraArgs...)
			if dryRun {
				fmt.Println(shellQuoteCmd(argv))
				return nil
			}
			return execOrRun(argv, execReplace)
		}
	}

	// Fallback: treat as a literal destination (host or alias). Put options before the destination.
	// We still honor ssh -G user/port by emitting them explicitly.
	argv := []string{"ssh"}
	argv = append(argv, extraArgs...)
	if eff.Port > 0 {
		argv = append(argv, "-p", strconv.Itoa(eff.Port))
	}
	dest := hostKey
	if strings.TrimSpace(destUser) != "" {
		dest = strings.TrimSpace(destUser) + "@" + dest
	}
	_ = credHostKey // keep for future use (e.g. credential lookup in direct-connect askpass mode)
	argv = append(argv, dest)

	if dryRun {
		fmt.Println(shellQuoteCmd(argv))
		return nil
	}
	return execOrRun(argv, execReplace)
}

// runDirectConnectWithSplits opens multiple connections to the SAME host (single host selection),
// using panes (splits) or windows inside the current tmux session.
//
// Semantics:
// - count <= 0: error
// - count == 1: behaves like runDirectConnect() (no extra panes/windows)
// - mode:
//   - "window" (default): open each connection in a new tmux window
//   - "v": create stacked splits (tmux split-window -v) for additional connections
//   - "h": create side-by-side splits (tmux split-window -h) for additional connections
//
// - layout: optional; if provided and count > 1 and mode is "v" or "h", runs `tmux select-layout <layout>`
func runDirectConnectWithSplits(cfg *manager.Config, hostArg string, count int, mode, layout string, execReplace, dryRun bool, extraArgs []string) error {
	if count <= 0 {
		return errors.New("split-count must be > 0")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "window"
	}
	switch mode {
	case "window", "v", "h":
	default:
		return errors.New("split-mode must be one of: window, v, h")
	}

	// Split/window fanout is tmux-only.
	if strings.TrimSpace(os.Getenv("TMUX")) == "" {
		return errors.New("split-count requires tmux")
	}

	// For count==1, just do the normal direct connect (still supports dry-run/exec-replace behavior).
	if count == 1 {
		return runDirectConnect(cfg, hostArg, execReplace, dryRun, extraArgs)
	}

	// We deliberately do NOT support exec-replace for split/window fanout; it would replace the manager process.
	if execReplace {
		return errors.New("cannot use --exec-replace with --split-count (multiple tmux panes/windows)")
	}

	// Resolve the final argv we want each pane/window to execute.
	// We use the *current binary* and invoke the public ssh wrapper so behavior is consistent
	// (askpass/__connect decision, alias recursion safety, etc).
	binExec := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
	if binExec == "" {
		binExec = "tmux-ssh-manager"
	}
	hostArg = strings.TrimSpace(hostArg)
	if hostArg == "" {
		return errors.New("empty host")
	}

	// Build: <bin> ssh --tmux --host <hostArg> -- <extra ssh args...>
	// Note: extraArgs already came from CLI after flag.Parse(); it does NOT include a literal "--".
	cmdParts := []string{shellEscapeForSh(binExec), "ssh", "--tmux", "--host", shellEscapeForSh(hostArg)}
	if dryRun {
		// If the user asked for dry-run in fanout mode, print what each pane would run (one per line).
		// We still show the wrapper form since that's what we actually execute under tmux.
		line := strings.Join(cmdParts, " ")
		if len(extraArgs) > 0 {
			esc := make([]string, 0, len(extraArgs))
			for _, a := range extraArgs {
				esc = append(esc, shellEscapeForSh(a))
			}
			line = line + " -- " + strings.Join(esc, " ")
		}
		for i := 0; i < count; i++ {
			fmt.Println(line)
		}
		return nil
	}

	// Materialize: first connection in a new window/split depending on mode
	// - For split modes, we keep everything in the current window and create count-1 new panes.
	// - For window mode, we create count new windows.
	runLine := strings.Join(cmdParts, " ")
	if len(extraArgs) > 0 {
		esc := make([]string, 0, len(extraArgs))
		for _, a := range extraArgs {
			esc = append(esc, shellEscapeForSh(a))
		}
		runLine = runLine + " -- " + strings.Join(esc, " ")
	}
	runLine = "exec " + runLine

	switch mode {
	case "window":
		// Open count windows.
		for i := 0; i < count; i++ {
			// Best-effort name: host + index
			winName := hostArg
			if count > 1 {
				winName = fmt.Sprintf("%s[%d]", hostArg, i+1)
			}
			_ = exec.Command(
				"tmux",
				"new-window",
				"-n", winName,
				"-c", "#{pane_current_path}",
				"--",
				"bash", "-lc", runLine,
			).Run()
		}
		return nil

	case "v", "h":
		// Start first connection in-place by opening it in a new window (safer than mutating current pane),
		// then split within that window.
		//
		// This mirrors the "dashboard opens in a new window" safety approach used elsewhere in the project.
		winName := hostArg
		out, err := exec.Command("tmux", "new-window", "-P", "-F", "#{window_id}", "-n", winName, "-c", "#{pane_current_path}", "--", "bash", "-lc", runLine).Output()
		if err != nil {
			return fmt.Errorf("tmux new-window failed: %v", err)
		}
		windowID := strings.TrimSpace(string(out))

		splitArg := "-v"
		if mode == "h" {
			splitArg = "-h"
		}
		for i := 1; i < count; i++ {
			if _, err := exec.Command("tmux", "split-window", splitArg, "-t", windowID, "-c", "#{pane_current_path}", "--", "bash", "-lc", runLine).Output(); err != nil {
				return fmt.Errorf("tmux split-window failed: %v", err)
			}
		}
		if strings.TrimSpace(layout) != "" {
			_ = exec.Command("tmux", "select-layout", "-t", windowID, strings.TrimSpace(layout)).Run()
		}
		return nil
	}

	return nil
}

// runSSHWrapperSubcommand implements a public "ssh-like" entrypoint that can be aliased as `ssh`.
// It attempts to use tmux-ssh-manager's config + credential automation when appropriate,
// and otherwise falls back to system ssh with identical arguments.
//
// Supported shapes (examples):
//
//	tmux-ssh-manager ssh host
//	tmux-ssh-manager ssh user@host -p 922
//	tmux-ssh-manager ssh -p 922 host
//
// Notes:
// - We do a minimal parse: detect destination (user@host or host) and a "-p <port>" if present.
// - We do not attempt to fully re-implement ssh argument parsing; unknown flags are passed through.
// - If invoked outside tmux, we open a new tmux session/window and run the connection inside it.
func runSSHWrapperSubcommand(cfg *manager.Config, args []string, execReplace bool) error {
	if len(args) == 0 {
		return errors.New("usage: tmux-ssh-manager ssh <ssh-style args...>")
	}

	// Wrapper-only flags:
	// - --debug   : print decision info to stderr
	// - --no-tmux : never create/use tmux when outside tmux; run in-place
	// - --tmux    : force tmux mode even outside tmux (open session/window)
	//
	// Additionally, this wrapper accepts tmux-ssh-manager style flags when invoked as:
	//   tmux-ssh-manager ssh --tmux --host <host> [--user <user>]
	//
	// Those flags are translated into real ssh-style args so they are never forwarded to system ssh.
	debug := false
	forceNoTmux := false
	forceTmux := false

	// Manager-style args (translated):
	managerHost := ""
	managerUser := ""

	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--debug":
			debug = true
			continue
		case "--no-tmux":
			forceNoTmux = true
			continue
		case "--tmux":
			forceTmux = true
			continue
		case "--host":
			if i+1 < len(args) {
				managerHost = strings.TrimSpace(args[i+1])
				i++
				continue
			}
			// If malformed, keep it and let the normal parser error/fallthrough.
		case "--user":
			if i+1 < len(args) {
				managerUser = strings.TrimSpace(args[i+1])
				i++
				continue
			}
			// If malformed, keep it and let the normal parser error/fallthrough.
		}
		filtered = append(filtered, a)
	}
	args = filtered

	if forceNoTmux && forceTmux {
		return errors.New("ssh: cannot combine --no-tmux and --tmux")
	}

	// If manager-style flags were provided, translate them into ssh-style args.
	//
	// This is critical because macOS/OpenSSH will treat unknown "--foo" tokens as illegal options.
	// Example failure when "--host/--user" leak to system ssh:
	//   ssh: illegal option -- -
	if managerHost != "" {
		translated := make([]string, 0, len(args)+2)
		if managerUser != "" {
			translated = append(translated, "-l", managerUser)
		}
		translated = append(translated, managerHost)
		translated = append(translated, args...)
		args = translated
	}

	// Identify destination and capture a -p <port> if present.
	dest := ""
	port := ""
	user := ""

	// Also accept `-l user` (ssh style) as explicit user.
	for i := 0; i < len(args); i++ {
		if args[i] == "-l" && i+1 < len(args) {
			user = strings.TrimSpace(args[i+1])
			break
		}
	}

	// Collect passthrough args exactly as provided (minus wrapper-only flags).
	pass := append([]string(nil), args...)

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-p" && i+1 < len(args) {
			port = strings.TrimSpace(args[i+1])
			i++
			continue
		}
		if a == "-l" && i+1 < len(args) {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		// First non-flag token is destination in most ssh usages.
		dest = strings.TrimSpace(a)
		break
	}

	if dest == "" {
		// Example: only flags provided; just exec system ssh.
		if debug {
			fmt.Fprintln(os.Stderr, "tssm ssh --debug: no destination token found; passthrough to system ssh")
		}
		return execOrRun(append([]string{"ssh"}, pass...), execReplace)
	}

	// Split user@host if present.
	hostToken := dest
	if at := strings.IndexByte(dest, '@'); at >= 0 {
		// user@host wins over -l user
		user = strings.TrimSpace(dest[:at])
		hostToken = strings.TrimSpace(dest[at+1:])
	}
	hostToken = strings.TrimSpace(hostToken)

	// Resolve effective config from ssh -G (User, HostName, Port).
	// This makes the wrapper match what ssh would do.
	eff := sshEffectiveConfig(hostToken)

	// Resolve effective user from ssh config if not provided explicitly.
	if user == "" && eff.User != "" {
		user = eff.User
	}
	// Resolve effective port from ssh config if not provided explicitly.
	if port == "" && eff.Port > 0 {
		port = strconv.Itoa(eff.Port)
	}

	// Prefer effective host name for config matching and credential lookup, but keep the typed token for display/pass-through.
	// If HostName is empty, fall back to what the user typed.
	effectiveHostName := hostToken
	if eff.HostName != "" {
		effectiveHostName = eff.HostName
	}

	// If config is available and has this host, use resolved effective settings (user/port/jump/etc).
	// Otherwise treat dest as literal.
	var resolved *manager.ResolvedHost
	if cfg != nil && strings.TrimSpace(hostToken) != "" {
		var h *manager.Host
		if h = cfg.HostByName(hostToken); h == nil && strings.TrimSpace(effectiveHostName) != "" {
			h = cfg.HostByName(effectiveHostName)
		}
		if h != nil {
			r := cfg.ResolveEffective(*h)
			// Override port/user from args if caller provided them explicitly or via ssh config.
			if user != "" {
				r.EffectiveUser = user
			}
			if port != "" {
				if p, err := strconv.Atoi(port); err == nil {
					r.EffectivePort = p
				}
			}
			// Ensure we use the effective hostname if ssh config maps it.
			if strings.TrimSpace(effectiveHostName) != "" {
				r.Host.Name = strings.TrimSpace(effectiveHostName)
			}
			resolved = &r
		}
	}

	// Decide whether to use credential automation.
	//
	// Sources of truth (highest precedence first):
	// 1) HostExtras auth_mode=keychain/manual (what the TUI "Toggle login mode" persists)
	// 2) YAML login_mode=askpass/manual (only when the host exists in config)
	//
	// Additionally, if we intend to use __connect, ensure a credential is actually available
	// (fail closed to normal ssh if not).
	useConnect := false
	credLookupHostKey := strings.TrimSpace(effectiveHostName) // credential host key: use effective HostName if present

	// IMPORTANT:
	// Prefer the resolved effective user (from config/ssh config) for credential lookups.
	// Falling back to the CLI-parsed user (or empty) can cause false "missing credential" results
	// if the credential was stored under a different account key.
	credLookupUser := ""
	if resolved != nil {
		credLookupUser = strings.TrimSpace(resolved.EffectiveUser)
	}
	if credLookupUser == "" {
		credLookupUser = strings.TrimSpace(user)
	}

	reason := "default passthrough"

	// HostExtras override should work even if host is not in config.
	if strings.TrimSpace(credLookupHostKey) != "" {
		if ex, exErr := manager.LoadHostExtras(strings.TrimSpace(credLookupHostKey)); exErr == nil {
			am := strings.ToLower(strings.TrimSpace(ex.AuthMode))
			switch am {
			case "keychain":
				useConnect = true
				reason = "HostExtras auth_mode=keychain"
			case "manual":
				useConnect = false
				reason = "HostExtras auth_mode=manual"
			default:
				// fall through
			}
		}
	}

	// If HostExtras didn't decide, fall back to YAML login_mode if present.
	if !useConnect && resolved != nil {
		lm := strings.ToLower(strings.TrimSpace(resolved.Host.LoginMode))
		if lm == "askpass" {
			useConnect = true
			reason = "YAML login_mode=askpass"
		}
	}

	// If we're planning to use automation, verify the credential exists/non-revealing.
	if useConnect {
		if err := manager.CredGet(credLookupHostKey, credLookupUser, "password"); err != nil {
			useConnect = false
			reason = "credential missing/unavailable; fallback to system ssh"
		}
	}

	inTmux := strings.TrimSpace(os.Getenv("TMUX")) != ""

	// Default behavior:
	// - Inside tmux: stay in-place (never create a new tmux session)
	// - Outside tmux: run in-place (no tmux) unless --tmux is requested
	//
	// This makes it safe to alias `ssh='tmux-ssh-manager ssh'` without forcing tmux.
	useTmux := inTmux
	if !inTmux {
		if forceTmux {
			useTmux = true
		} else {
			useTmux = false
		}
	}
	if forceNoTmux {
		useTmux = false
	}

	if debug {
		fmt.Fprintf(
			os.Stderr,
			"tssm ssh --debug: hostToken=%q hostName=%q port=%q user=%q inConfig=%v inTmux=%v useTmux=%v decision=%v (%s)\n",
			hostToken,
			credLookupHostKey,
			port,
			credLookupUser,
			resolved != nil,
			inTmux,
			useTmux,
			useConnect,
			reason,
		)
	}

	// Outside tmux (or when forced): run in the current terminal.
	if !useTmux {
		if useConnect {
			ccArgs := []string{"--host", credLookupHostKey}
			if credLookupUser != "" {
				ccArgs = append(ccArgs, "--user", credLookupUser)
			}
			return runConnectSubcommand(ccArgs)
		}
		return execOrRun(append([]string{"ssh"}, pass...), execReplace)
	}

	// tmux mode:
	// - inside tmux we can run in-place
	// - outside tmux we create a session/window to host the interactive session
	if inTmux {
		if useConnect {
			ccArgs := []string{"--host", credLookupHostKey}
			if credLookupUser != "" {
				ccArgs = append(ccArgs, "--user", credLookupUser)
			}
			return runConnectSubcommand(ccArgs)
		}
		return execOrRun(append([]string{"ssh"}, pass...), execReplace)
	}

	// Outside tmux and tmux mode requested: create session/window.
	sess := "tssm-ssh"
	bin := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
	if bin == "" {
		bin = "tmux-ssh-manager"
	}

	var line string
	if useConnect {
		userFlag := ""
		if credLookupUser != "" {
			userFlag = " --user " + shellEscapeForSh(credLookupUser)
		}
		line = shellEscapeForSh(bin) + " __connect --host " + shellEscapeForSh(credLookupHostKey) + userFlag
	} else {
		line = "ssh " + shellQuoteCmd(pass)
	}

	if err := exec.Command("tmux", "new-session", "-d", "-s", sess).Run(); err != nil {
		return fmt.Errorf("ssh: tmux not available (failed to start session): %w (try --no-tmux)", err)
	}
	if err := exec.Command("tmux", "new-window", "-t", sess, "bash", "-lc", line).Run(); err != nil {
		return fmt.Errorf("ssh: tmux new-window failed: %w (try --no-tmux)", err)
	}
	return nil
}

func runAskpassSubcommand(args []string) error {
	fs := flag.NewFlagSet("__askpass", flag.ContinueOnError)
	var host string
	var username string
	var kind string
	fs.StringVar(&host, "host", "", "Host alias or destination key for the credential")
	fs.StringVar(&username, "user", "", "Optional username associated with the credential")
	fs.StringVar(&kind, "kind", "password", "Credential kind (password only supported for askpass use)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	host = strings.TrimSpace(host)
	username = strings.TrimSpace(username)
	kind = strings.TrimSpace(kind)

	if host == "" {
		return errors.New("usage: tmux-ssh-manager __askpass --host <alias> [--user <user>] [--kind password]")
	}
	if kind != "" && !strings.EqualFold(kind, "password") {
		return fmt.Errorf("__askpass: unsupported kind %q (password only)", kind)
	}

	secret, err := manager.CredReveal(host, username, kind)
	if err != nil {
		return err
	}

	// IMPORTANT: This prints the secret ONLY because SSH_ASKPASS requires stdout.
	// The caller must ensure this is only used as an askpass helper.
	fmt.Fprint(os.Stdout, secret)
	return nil
}

type connectConfig struct {
	Host string
	User string
}

func runConnectSubcommand(args []string) error {
	fs := flag.NewFlagSet("__connect", flag.ContinueOnError)
	var host string
	var user string
	fs.StringVar(&host, "host", "", "Host alias / configured host name")
	fs.StringVar(&user, "user", "", "Optional username (used for Keychain account and ssh dest when applicable)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	host = strings.TrimSpace(host)
	user = strings.TrimSpace(user)
	if host == "" {
		return errors.New("usage: tmux-ssh-manager __connect --host <alias> [--user <user>]")
	}

	// Retrieve password from Keychain (macOS) in-memory.
	pw, err := manager.CredReveal(host, user, "password")
	if err != nil {
		return fmt.Errorf("__connect: credential missing/unavailable for %s: %w", host, err)
	}

	// Build ssh argv. Keep it conservative: prefer keyboard-interactive/password.
	dest := host
	if user != "" {
		dest = user + "@" + dest
	}
	argv := []string{
		"ssh",
		"-o", "PreferredAuthentications=keyboard-interactive,password",
		"-o", "PubkeyAuthentication=no",
		"-o", "NumberOfPasswordPrompts=1",
		dest,
	}

	// Start ssh under a PTY so we can detect prompts and inject the password.
	//
	// IMPORTANT:
	// If we don't explicitly propagate the current terminal window size into the PTY,
	// some environments (notably when invoked via wrappers/aliases/tmux) can end up with
	// a 0x0 PTY size on the remote side (rows 0; columns 0), which breaks full-screen apps.
	cmd := exec.Command(argv[0], argv[1:]...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("__connect: pty start: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	// Seed PTY size from our current stdout terminal (best-effort).
	// (stdout is what the user is actually looking at; stdin might not be a tty in some setups)
	if term.IsTerminal(int(os.Stdout.Fd())) {
		if cols, rows, sizeErr := term.GetSize(int(os.Stdout.Fd())); sizeErr == nil && rows > 0 && cols > 0 {
			_ = pty.Setsize(ptmx, &pty.Winsize{
				Rows: uint16(rows),
				Cols: uint16(cols),
			})
		}
	}

	// Keep PTY size updated on window resize, best-effort.
	//
	// IMPORTANT:
	// - Do not reference syscall.SIGWINCH in the main build, because some build targets (notably Windows)
	//   do not define it and will fail to compile even if the code path is guarded.
	// - We delegate to a helper that is implemented per-OS via build tags.
	startPTYResizeWatcher(ptmx)

	// Disable local terminal echo so anything the user types (including password) isn't echoed locally.
	// This does NOT prevent the remote from echoing; it prevents local tty echo.
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		oldState, sErr := term.MakeRaw(fd)
		if sErr == nil {
			defer func() { _ = term.Restore(fd, oldState) }()
		}
		// Ensure cursor is visible.
		_, _ = fmt.Fprint(os.Stdout, "\033[?25h\033[0m")
	}

	// Expect-like prompt detection:
	// - Match common password prompts even when they are not newline-terminated.
	// - Respond once with password + CR.
	promptRe := regexp.MustCompile(`(?i)(password|passcode|pass phrase|passphrase)\s*:?\s*$`)
	seenPrompt := false

	// Copy user input -> ssh PTY (interactive session)
	go func() {
		_, _ = io.Copy(ptmx, os.Stdin)
	}()

	// Read ssh output, mirror to user, and detect prompts.
	buf := make([]byte, 4096)
	var tail strings.Builder
	deadline := time.Now().Add(30 * time.Second) // prompt detection window
	const maxTail = 2048

	for {
		n, rerr := ptmx.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			_, _ = os.Stdout.Write(chunk)

			// Maintain a rolling tail buffer for prompt detection (handles prompts without newline).
			for _, b := range chunk {
				if b == 0 {
					continue
				}
				// Normalize newlines but keep content for tail.
				if b == '\r' {
					// treat CR as a boundary
					tail.WriteByte('\n')
				} else {
					tail.WriteByte(b)
				}
				// Cap tail size
				if tail.Len() > maxTail {
					s := tail.String()
					tail.Reset()
					tail.WriteString(s[len(s)-maxTail:])
				}
			}

			if !seenPrompt && time.Now().Before(deadline) {
				// Only examine the last line-ish segment of tail
				s := tail.String()
				if idx := strings.LastIndexByte(s, '\n'); idx >= 0 && idx+1 < len(s) {
					s = s[idx+1:]
				}
				s = strings.TrimSpace(s)
				if promptRe.MatchString(s) {
					seenPrompt = true

					// Send password + CR (not LF) to match typical SSH password entry behavior.
					_, _ = ptmx.Write([]byte(pw))
					_, _ = ptmx.Write([]byte("\r"))
				}
			}
		}

		if rerr != nil {
			break
		}
	}

	// Wait for ssh to exit and return its status.
	if err := cmd.Wait(); err != nil {
		return err
	}
	return nil
}

func runCredSubcommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tmux-ssh-manager cred <set|get|delete> --host <alias>")
	}
	action := args[0]
	fs := flag.NewFlagSet("cred", flag.ContinueOnError)
	var host string
	var username string
	var kind string
	fs.StringVar(&host, "host", "", "Host alias or destination key for the credential")
	fs.StringVar(&username, "user", "", "Optional username associated with the credential (stored as metadata where supported)")
	fs.StringVar(&kind, "kind", "password", "Credential kind: password|passphrase|otp (metadata only for now)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return errors.New("missing required --host")
	}

	switch action {
	case "set":
		// Read secret from stdin safely (no echo) is not implemented here because this file avoids terminal control.
		// Instead, defer to pkg/manager helper which can handle secure prompts per platform.
		if err := manager.CredSet(host, username, kind); err != nil {
			return err
		}

		// Optional UX parity with the TUI:
		// If a credential was just stored and the host has per-host extras, auto-enable askpass by setting
		// auth_mode=keychain (which maps to login_mode=askpass in the rest of the codebase).
		//
		// This keeps behavior consistent across macOS Keychain and Linux backends (Secret Service / GPG fallback).
		if strings.TrimSpace(host) != "" {
			if ex, exErr := manager.LoadHostExtras(strings.TrimSpace(host)); exErr == nil {
				ex.HostKey = strings.TrimSpace(host)
				ex.AuthMode = "keychain"
				_ = manager.SaveHostExtras(ex)
			}
		}
		return nil
	case "get":
		// Intentionally does not print the secret. It only verifies existence/access.
		return manager.CredGet(host, username, kind)
	case "delete":
		return manager.CredDelete(host, username, kind)
	default:
		return fmt.Errorf("unknown cred action %q (expected set|get|delete)", action)
	}
}

func runHostExtrasSwitchToIdentitySubcommand(args []string) error {
	fs := flag.NewFlagSet("__hostextras-switch-to-identity", flag.ContinueOnError)
	var hostKey string
	var identityFile string
	fs.StringVar(&hostKey, "host", "", "Host key / alias (used for extras file lookup)")
	fs.StringVar(&identityFile, "identity-file", "", "Identity file to persist when switching to identity auth (default: auto-detect ~/.ssh/id_ed25519 then ~/.ssh/id_rsa)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	hostKey = strings.TrimSpace(hostKey)
	identityFile = strings.TrimSpace(identityFile)
	if hostKey == "" {
		return errors.New("usage: tmux-ssh-manager __hostextras-switch-to-identity --host <hostkey> [--identity-file <path>]")
	}
	if identityFile == "" {
		// Prefer modern keys. If the file doesn't exist, fall back to RSA.
		// Note: This is only the persisted default used when switching a host to key-based auth.
		// The underlying ssh client may still choose other keys via agent/config.
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			if _, err := os.Stat(filepath.Join(home, ".ssh", "id_ed25519")); err == nil {
				identityFile = "~/.ssh/id_ed25519"
			} else {
				identityFile = "~/.ssh/id_rsa"
			}
		} else {
			identityFile = "~/.ssh/id_rsa"
		}
	}

	ex, err := manager.LoadHostExtras(hostKey)
	if err != nil {
		return fmt.Errorf("__hostextras-switch-to-identity: load host extras: %w", err)
	}
	ex.HostKey = hostKey

	// Ensure identity_file is set (do not override an explicit user choice).
	if strings.TrimSpace(ex.IdentityFile) == "" {
		ex.IdentityFile = identityFile
	}

	// Disable keychain forcing so future connects prefer keys/agent/IdentityFile.
	ex.AuthMode = "manual"

	// Persist (normalizes and writes under ~/.config/tmux-ssh-manager/hosts/<hostkey>.conf).
	if err := manager.SaveHostExtras(ex); err != nil {
		return fmt.Errorf("__hostextras-switch-to-identity: save host extras: %w", err)
	}
	return nil
}

func execOrRun(argv []string, replace bool) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}

	if replace {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			return fmt.Errorf("command not found: %s", argv[0])
		}
		return syscall.Exec(path, argv, os.Environ())
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// execOrRunWithEnv is like execOrRun but applies env overrides (append/override semantics).
func execOrRunWithEnv(argv []string, replace bool, env map[string]string) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	if len(env) == 0 {
		return execOrRun(argv, replace)
	}

	merged := os.Environ()
	// Override/append.
	for k, v := range env {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		prefix := k + "="
		found := false
		for i := range merged {
			if strings.HasPrefix(merged[i], prefix) {
				merged[i] = prefix + v
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, prefix+v)
		}
	}

	if replace {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			return fmt.Errorf("command not found: %s", argv[0])
		}
		return syscall.Exec(path, argv, merged)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = merged
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// execOrRunWithEnvAndStdin is like execOrRunWithEnv but uses the provided stdin.
// Note: for replace=true, we fall back to non-replace execution because we can't safely
// emulate "stdin redirection" with syscall.Exec in a portable way without manipulating FDs.
func execOrRunWithEnvAndStdin(argv []string, replace bool, env map[string]string, stdin io.Reader) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	if stdin == nil {
		return execOrRunWithEnv(argv, replace, env)
	}

	merged := os.Environ()
	for k, v := range env {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		prefix := k + "="
		found := false
		for i := range merged {
			if strings.HasPrefix(merged[i], prefix) {
				merged[i] = prefix + v
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, prefix+v)
		}
	}

	if replace {
		// Fall back to child process so we can control stdin deterministically.
		replace = false
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = merged
	cmd.Stdin = stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// parseSCPRemoteHostToken extracts the host token for credential lookup from a scp arg.
// Returns (hostToken, userToken, ok).
//
// Examples:
// - "user@leaf01:/tmp/a" -> ("leaf01", "user", true)
// - "leaf01:/tmp/a"      -> ("leaf01", "", true)
// - "[fe80::1%lo0]:/tmp" -> ("fe80::1%lo0", "", true)   (best-effort; strips brackets)
// - "-P" / "-r" / "file" -> ("", "", false)
func parseSCPRemoteHostToken(arg string) (hostToken string, userToken string, ok bool) {
	a := strings.TrimSpace(arg)
	if a == "" {
		return "", "", false
	}
	// scp remote path generally contains a ':' separator, but avoid Windows drive letters.
	// Also allow bracketed IPv6 like [addr]:path.
	col := strings.IndexByte(a, ':')
	if col <= 0 {
		return "", "", false
	}
	left := strings.TrimSpace(a[:col])
	if left == "" {
		return "", "", false
	}
	// Strip [..] for IPv6.
	if strings.HasPrefix(left, "[") && strings.Contains(left, "]") {
		left = strings.TrimPrefix(left, "[")
		left = strings.TrimSuffix(left, "]")
	}
	// user@host
	if at := strings.IndexByte(left, '@'); at >= 0 {
		u := strings.TrimSpace(left[:at])
		h := strings.TrimSpace(left[at+1:])
		if h == "" {
			return "", "", false
		}
		return h, u, true
	}
	return left, "", true
}

// scpEffectiveUser resolves user via ssh -G for a host token (best-effort).
func scpEffectiveUser(host string) string {
	return sshEffectiveConfig(host).User
}

// runSCPWrapperSubcommand implements a public "scp-like" entrypoint that can be aliased as `scp`.
// It attempts to use tmux-ssh-manager's Keychain-backed SSH_ASKPASS flow when enabled for the
// target host, and otherwise falls back to system scp with identical arguments.
//
// Notes:
//   - We do a minimal parse: find the first scp arg that looks like a remote (contains ':').
//   - We intentionally do NOT fully re-implement scp parsing.
//   - When automation is enabled and credential exists, we set SSH_ASKPASS to:
//     tmux-ssh-manager __askpass --host <host> [--user <user>] --kind password
//     and force askpass usage by setting:
//     SSH_ASKPASS_REQUIRE=force, DISPLAY=1, setsid
func runSCPWrapperSubcommand(cfg *manager.Config, args []string, execReplace bool) error {
	if len(args) == 0 {
		return errors.New("usage: tmux-ssh-manager scp <scp-style args...>")
	}

	// Wrapper-only flags:
	// - --debug : print decision info to stderr
	debug := false
	filtered := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "--debug":
			debug = true
			continue
		}
		filtered = append(filtered, a)
	}
	args = filtered

	// Keep passthrough args exactly as provided (minus wrapper-only flags).
	pass := append([]string(nil), args...)

	// Identify a remote host token from any arg (either src or dst).
	hostToken := ""
	userToken := ""
	for _, a := range args {
		// skip flags
		if strings.HasPrefix(a, "-") {
			continue
		}
		if h, u, ok := parseSCPRemoteHostToken(a); ok {
			hostToken = h
			userToken = u
			break
		}
	}

	if hostToken == "" {
		// No remote host; just passthrough.
		if debug {
			fmt.Fprintln(os.Stderr, "tssm scp --debug: no remote token found; passthrough to system scp")
		}
		return execOrRun(append([]string{"scp"}, pass...), execReplace)
	}

	// Resolve effective ssh config (HostName/User). For scp, credential host key should align
	// with the effective HostName when possible.
	eff := sshEffectiveConfig(hostToken)

	credLookupHostKey := strings.TrimSpace(hostToken)
	if strings.TrimSpace(eff.HostName) != "" {
		credLookupHostKey = strings.TrimSpace(eff.HostName)
	}

	credLookupUser := strings.TrimSpace(userToken)
	if credLookupUser == "" {
		// prefer ssh -G derived user
		if u := strings.TrimSpace(eff.User); u != "" {
			credLookupUser = u
		}
	}

	// If config is available and has this host, we can consult YAML login_mode too.
	var resolved *manager.ResolvedHost
	if cfg != nil && strings.TrimSpace(hostToken) != "" {
		var h *manager.Host
		if h = cfg.HostByName(hostToken); h == nil && strings.TrimSpace(credLookupHostKey) != "" {
			h = cfg.HostByName(credLookupHostKey)
		}
		if h != nil {
			r := cfg.ResolveEffective(*h)
			// Ensure we use the effective hostname if ssh config maps it.
			if strings.TrimSpace(credLookupHostKey) != "" {
				r.Host.Name = strings.TrimSpace(credLookupHostKey)
			}
			// If user wasn't explicit, let resolved effective user win.
			if credLookupUser == "" && strings.TrimSpace(r.EffectiveUser) != "" {
				credLookupUser = strings.TrimSpace(r.EffectiveUser)
			}
			resolved = &r
		}
	}

	// Decide whether to use askpass env injection for scp.
	useAskpass := false
	reason := "default passthrough"

	// HostExtras override should work even if host is not in config.
	if strings.TrimSpace(credLookupHostKey) != "" {
		if ex, exErr := manager.LoadHostExtras(strings.TrimSpace(credLookupHostKey)); exErr == nil {
			am := strings.ToLower(strings.TrimSpace(ex.AuthMode))
			switch am {
			case "keychain":
				useAskpass = true
				reason = "HostExtras auth_mode=keychain"
			case "manual":
				useAskpass = false
				reason = "HostExtras auth_mode=manual"
			default:
				// fall through
			}
		}
	}

	// If HostExtras didn't decide, fall back to YAML login_mode if present.
	if !useAskpass && resolved != nil {
		lm := strings.ToLower(strings.TrimSpace(resolved.Host.LoginMode))
		if lm == "askpass" {
			useAskpass = true
			reason = "YAML login_mode=askpass"
		}
	}

	// Verify credential exists (non-revealing) before enabling askpass.
	if useAskpass {
		if err := manager.CredGet(credLookupHostKey, credLookupUser, "password"); err != nil {
			useAskpass = false
			reason = "credential missing/unavailable; fallback to system scp"
		}
	}

	if debug {
		fmt.Fprintf(
			os.Stderr,
			"tssm scp --debug: hostToken=%q hostName=%q user=%q inConfig=%v decision=%v (%s)\n",
			hostToken,
			credLookupHostKey,
			credLookupUser,
			resolved != nil,
			useAskpass,
			reason,
		)
	}

	if !useAskpass {
		return execOrRun(append([]string{"scp"}, pass...), execReplace)
	}

	// Build SSH_ASKPASS invocation.
	//
	// IMPORTANT (macOS/OpenSSH):
	// The value of SSH_ASKPASS is executed by ssh/scp. In some environments it is executed
	// without a reliable PATH, even if the parent process had one. That can cause:
	//   ssh_askpass: exec('tmux-ssh-manager' __askpass ...): No such file or directory
	//
	// To make this reliable, we create a tiny temp wrapper script and point SSH_ASKPASS at it.
	// The script execs the actual tmux-ssh-manager binary via an absolute path.
	bin := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
	if bin == "" {
		// Prefer an absolute path to this running binary when possible.
		if exe, e := os.Executable(); e == nil && strings.TrimSpace(exe) != "" {
			bin = strings.TrimSpace(exe)
		} else {
			// Fallback: rely on PATH (may still fail if tmux-ssh-manager is not installed globally).
			bin = "tmux-ssh-manager"
		}
	}

	// Construct the askpass argv we want the wrapper to exec.
	askpassArgs := []string{
		"__askpass",
		"--host", credLookupHostKey,
	}
	if strings.TrimSpace(credLookupUser) != "" {
		askpassArgs = append(askpassArgs, "--user", credLookupUser)
	}
	askpassArgs = append(askpassArgs, "--kind", "password")

	// Create a temp wrapper script so ssh/scp can exec SSH_ASKPASS reliably.
	// The script does not contain secrets; it only calls back into tmux-ssh-manager.
	tmpDir := strings.TrimSpace(os.Getenv("TMPDIR"))
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	wrapperPath := filepath.Join(tmpDir, fmt.Sprintf("tssm-askpass-%d.sh", os.Getpid()))
	wrapper := "#!/usr/bin/env bash\n" +
		"exec " + shellEscapeForSh(bin) + " " + shellQuoteCmd(askpassArgs) + "\n"
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		return fmt.Errorf("scp: failed to write askpass wrapper: %w", err)
	}

	// NOTE:
	// We intentionally do NOT remove the wrapper automatically. When scp/ssh fails to exec askpass,
	// the most useful artifact is the on-disk wrapper script.
	// Users can delete it manually; it contains no secrets (only the path to tmux-ssh-manager and args).

	// Force askpass usage for non-interactive scp. DISPLAY is required by OpenSSH to permit askpass.
	env := map[string]string{
		"SSH_ASKPASS":         wrapperPath,
		"SSH_ASKPASS_REQUIRE": "force",
		"DISPLAY":             "1",
	}

	// Run scp in a way that forces SSH_ASKPASS to be used.
	//
	// Rationale:
	// - macOS does not ship `setsid` by default.
	// - For OpenSSH, when SSH_ASKPASS_REQUIRE=force is set, ssh/scp will use askpass when it cannot
	//   read from a TTY/stdin for the password prompt.
	// - We keep stdout/stderr attached so progress/errors still display.
	//
	// Implementation: run `scp ...` with stdin redirected from /dev/null.
	argv := append([]string{"scp"}, pass...)
	devNull, err := os.Open("/dev/null")
	if err != nil {
		// Best-effort fallback: still try scp with env, using current stdin.
		return execOrRunWithEnv(argv, execReplace, env)
	}
	defer devNull.Close()

	if err := execOrRunWithEnvAndStdin(argv, execReplace, env, devNull); err != nil {
		// Improve diagnostics for the common failure case where OpenSSH can't exec SSH_ASKPASS.
		// The wrapper contains no secrets, so it's safe to print (bounded).
		msg := ""
		if b, rerr := os.ReadFile(wrapperPath); rerr == nil {
			s := string(b)
			if len(s) > 4096 {
				s = s[:4096] + "\n... (truncated)\n"
			}
			msg = "\n--- SSH_ASKPASS wrapper (" + wrapperPath + ") ---\n" + s + "\n--- end wrapper ---\n"
		} else {
			msg = "\n(note) failed to read SSH_ASKPASS wrapper at " + wrapperPath + ": " + rerr.Error() + "\n"
		}
		return fmt.Errorf("scp failed (askpass wrapper at %s). Underlying error: %w%s", wrapperPath, err, msg)
	}
	return nil
}

func hostLine(r manager.ResolvedHost) string {
	parts := []string{r.Host.Name}
	if r.Group != nil && r.Group.Name != "" {
		parts = append(parts, fmt.Sprintf("[%s]", r.Group.Name))
	}
	if r.EffectiveUser != "" {
		parts = append(parts, "as "+r.EffectiveUser)
	}
	if r.EffectivePort > 0 && r.EffectivePort != 22 {
		parts = append(parts, fmt.Sprintf(":%d", r.EffectivePort))
	}
	if r.EffectiveJumpHost != "" {
		parts = append(parts, "via "+r.EffectiveJumpHost)
	}
	if len(r.Host.Tags) > 0 {
		parts = append(parts, "tags:"+strings.Join(r.Host.Tags, ","))
	}
	return strings.Join(parts, " ")
}

func shellQuoteCmd(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		if a == "" {
			quoted = append(quoted, "''")
			continue
		}
		// Quote arguments with characters that are special in sh.
		if regexp.MustCompile(`[^\w@%+=:,./-]`).MatchString(a) {
			quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\"'\"'`)+"'")
		} else {
			quoted = append(quoted, a)
		}
	}
	return strings.Join(quoted, " ")
}

// shellEscapeForSh escapes a single string for safe inclusion in a `sh`/`bash` command line.
// Uses single-quote escaping: 'foo' -> 'foo', and internal ' becomes '"'"'.
// This helper is intentionally simple (good enough for host/user strings and small args).
func shellEscapeForSh(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\"'\"'`) + "'"
}

// sshEffectiveConfig captures the most relevant effective ssh config values for a host token.
type sshEffective struct {
	User     string
	HostName string
	Port     int
}

// sshEffectiveConfig attempts to resolve effective SSH configuration for a host/alias using
// the user's ssh configuration by invoking `ssh -G <host>` and parsing the output.
//
// It is intentionally minimal (we only parse user/hostname/port), but it respects includes and
// Match blocks because OpenSSH performs the evaluation.
//
// If a value cannot be determined, it is left empty/zero.
func sshEffectiveConfig(host string) sshEffective {
	host = strings.TrimSpace(host)
	if host == "" {
		return sshEffective{}
	}

	cmd := exec.Command("ssh", "-G", host)
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return sshEffective{}
	}

	var eff sshEffective
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		if ln == "" {
			continue
		}
		parts := strings.Fields(ln)
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "user":
			if eff.User == "" {
				eff.User = strings.TrimSpace(parts[1])
			}
		case "hostname":
			if eff.HostName == "" {
				eff.HostName = strings.TrimSpace(parts[1])
			}
		case "port":
			if eff.Port == 0 {
				if p, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && p > 0 {
					eff.Port = p
				}
			}
		}
	}
	return eff
}

// sshEffectiveUser attempts to resolve the effective SSH username for a host/alias using
// `ssh -G <host>` output. It exists for backward compatibility with older code paths.
func sshEffectiveUser(host string) string {
	return sshEffectiveConfig(host).User
}

func isShellSpecial(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '"', '\'', '\\', '$', '`', '&', '|', ';', '<', '>', '(', ')', '{', '}', '*', '?', '!', '~', '#':
		return true
	default:
		return false
	}
}

func exitCodeFromErr(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if status, ok := ee.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return 1
}
