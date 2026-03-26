package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tmux-ssh-manager/pkg/credentials"
	"tmux-ssh-manager/pkg/sshconfig"
	"tmux-ssh-manager/pkg/state"
	"tmux-ssh-manager/pkg/termio"
	"tmux-ssh-manager/pkg/tmuxrun"
	"tmux-ssh-manager/pkg/tmuxui"
)

var credSet = credentials.Set
var credGet = credentials.Get
var credDelete = credentials.Delete

var Version = "dev"

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "--version", "-v", "version":
			_, err := fmt.Fprintln(stdout, Version)
			return err
		case "list":
			return runList(args[1:], stdout)
		case "connect":
			return runConnect(args[1:], stdin, stdout, stderr)
		case "add":
			return runAdd(args[1:], stdout)
		case "cred":
			return runCred(args[1:], stdout)
		case "__askpass":
			return runAskpass(args[1:], stdout)
		case "ssh":
			return runSSHPassthrough("ssh", args[1:])
		case "scp":
			return runSSHPassthrough("scp", args[1:])
		case "print-ssh-config-path":
			path, err := sshconfig.DefaultPrimaryPath()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(stdout, path)
			return err
		}
	}

	fs := flag.NewFlagSet("picker", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mode := fs.String("mode", "search", "picker mode: search or normal")
	fs.StringVar(mode, "m", "search", "picker mode (shorthand)")
	implicitSelect := fs.Bool("implicit-select", true, "enter/v/s/w act on highlighted host in search mode")
	enterMode := fs.String("enter-mode", "p", "enter key action: p (pane), w (window), s (split-h), v (split-v)")
	_ = fs.Parse(args)

	hosts, err := sshconfig.LoadDefault()
	if err != nil {
		return err
	}
	storePath, err := state.DefaultPath()
	if err != nil {
		return err
	}
	store, err := state.Load(storePath)
	if err != nil {
		return err
	}

	// Build host→user map for credential lookups.
	hostUsers := make(map[string]string, len(hosts))
	for _, h := range hosts {
		hostUsers[h.Alias] = h.User
	}

	askpassScript := createAskpassScript()
	if askpassScript != "" {
		defer os.Remove(askpassScript)
	}

	hasCred := func(alias string) bool {
		user := hostUsers[alias]
		return credentials.Get(alias, user, "password") == nil
	}

	sess := tmuxrun.Session{
		AskpassScript: askpassScript,
		HostUsers:     hostUsers,
		HasCredential: hasCred,
	}

	app := tmuxui.App{
		Hosts:          hosts,
		State:          store,
		StatePath:      storePath,
		StartInSearch:  *mode != "normal",
		ImplicitSelect: *implicitSelect,
		EnterMode:      normalizeEnterMode(*enterMode),
		AddHost:        sshconfig.AddHostToPrimary,
		ExecCredential: credentialCommand,
		InTmux:         tmuxrun.InTmux,
		Connect: func(alias string) *exec.Cmd {
			return sshCommandWithAskpass(alias, hostUsers[alias], askpassScript, hasCred)
		},
		NewWindow:    sess.NewWindow,
		SplitVert:    sess.SplitVertical,
		SplitHoriz:   sess.SplitHorizontal,
		Tiled:        sess.Tiled,
		SetupLogging: sess.SetupPaneLogging,
	}
	return app.Run()
}

func normalizeEnterMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "p", "pane":
		return "p"
	case "w", "window":
		return "w"
	case "s", "split", "split-h":
		return "s"
	case "v", "split-v":
		return "v"
	default:
		return "p"
	}
}

type listEntry struct {
	Alias         string   `json:"alias"`
	HostName      string   `json:"hostname,omitempty"`
	User          string   `json:"user,omitempty"`
	Port          int      `json:"port,omitempty"`
	ProxyJump     string   `json:"proxyjump,omitempty"`
	IdentityFiles []string `json:"identity_files,omitempty"`
}

func runList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "output hosts as JSON array")
	if err := fs.Parse(args); err != nil {
		return err
	}
	hosts, err := sshconfig.LoadDefault()
	if err != nil {
		return err
	}
	if *jsonOut {
		entries := make([]listEntry, len(hosts))
		for i, h := range hosts {
			entries[i] = listEntry{
				Alias:         h.Alias,
				HostName:      h.HostName,
				User:          h.User,
				Port:          h.Port,
				ProxyJump:     h.ProxyJump,
				IdentityFiles: h.IdentityFiles,
			}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}
	for _, host := range hosts {
		if _, err := fmt.Fprintln(stdout, host.Alias); err != nil {
			return err
		}
	}
	return nil
}

func runConnect(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dryRun := fs.Bool("dry-run", false, "print the ssh command instead of executing it")
	splitCount := fs.Int("split-count", 0, "open N connections (>1 creates panes/windows)")
	splitMode := fs.String("split-mode", "window", "with --split-count: window|v|h")
	layout := fs.String("layout", "", "tmux layout: tiled|even-horizontal|even-vertical|main-horizontal|main-vertical")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: tmux-ssh-manager connect [--dry-run] [--split-count N] [--split-mode window|v|h] [--layout tiled] <alias>")
	}
	alias := strings.TrimSpace(fs.Arg(0))
	if *dryRun {
		_, err := fmt.Fprintln(stdout, "ssh "+alias)
		return err
	}
	if *splitCount > 1 {
		return runConnectSplit(alias, *splitCount, *splitMode, *layout)
	}
	return execSSH(alias, stdin, stdout, stderr)
}

func runConnectSplit(alias string, count int, mode, layout string) error {
	if !tmuxrun.InTmux() {
		return fmt.Errorf("split-count requires running inside tmux")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "window"
	}
	s := tmuxrun.Session{}
	switch mode {
	case "window":
		for i := 0; i < count; i++ {
			if err := s.NewWindow(alias); err != nil {
				return err
			}
		}
		return nil
	case "v", "h":
		aliases := make([]string, count)
		for i := range aliases {
			aliases[i] = alias
		}
		if layout == "" {
			layout = "tiled"
		}
		return s.Tiled(aliases, layout)
	default:
		return fmt.Errorf("split-mode must be one of: window, v, h")
	}
}

func runAdd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var input sshconfig.AddHostInput
	fs.StringVar(&input.Alias, "alias", "", "Host alias")
	fs.StringVar(&input.HostName, "hostname", "", "HostName value")
	fs.StringVar(&input.User, "user", "", "User value")
	fs.IntVar(&input.Port, "port", 0, "Port value")
	fs.StringVar(&input.ProxyJump, "proxyjump", "", "ProxyJump value")
	fs.StringVar(&input.IdentityFile, "identity-file", "", "IdentityFile value")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := sshconfig.AddHostToPrimary(input); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stdout, "added host %s\n", strings.TrimSpace(input.Alias))
	return err
}

func runCred(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tmux-ssh-manager cred <set|get|delete> --host <alias> [--user <user>] [--kind password]")
	}

	action := strings.TrimSpace(args[0])
	fs := flag.NewFlagSet("cred", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var host string
	var user string
	var kind string
	fs.StringVar(&host, "host", "", "Host alias or destination key")
	fs.StringVar(&user, "user", "", "Optional username for the credential")
	fs.StringVar(&kind, "kind", "password", "Credential kind")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("missing required --host")
	}

	subject := host
	if strings.TrimSpace(user) != "" {
		subject = strings.TrimSpace(user) + "@" + host
	}

	switch action {
	case "set":
		if err := credSet(host, user, kind); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "stored %s for %s\n", strings.TrimSpace(kind), subject)
		return err
	case "get":
		if err := credGet(host, user, kind); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "%s exists for %s\n", strings.TrimSpace(kind), subject)
		return err
	case "delete":
		if err := credDelete(host, user, kind); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "deleted %s for %s\n", strings.TrimSpace(kind), subject)
		return err
	default:
		return fmt.Errorf("unknown cred action %q (expected set|get|delete)", action)
	}
}

func credentialCommand(action, host, user, kind string) (*exec.Cmd, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	return credentialCommandForPath(path, action, host, user, kind), nil
}

func credentialCommandForPath(path, action, host, user, kind string) *exec.Cmd {
	args := []string{"cred", action, "--host", strings.TrimSpace(host), "--kind", strings.TrimSpace(kind)}
	if strings.TrimSpace(user) != "" {
		args = append(args, "--user", strings.TrimSpace(user))
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func sshCommand(alias string) *exec.Cmd {
	cmd := exec.Command("ssh", alias)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func sshCommandWithAskpass(alias, user, askpassScript string, hasCred func(string) bool) *exec.Cmd {
	if askpassScript != "" && hasCred != nil && hasCred(alias) {
		cmd := exec.Command("ssh",
			"-o", "PubkeyAuthentication=no",
			"-o", "PreferredAuthentications=keyboard-interactive,password",
			alias,
		)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(os.Environ(),
			"TSSM_HOST="+alias,
			"TSSM_USER="+user,
			"SSH_ASKPASS="+askpassScript,
			"SSH_ASKPASS_REQUIRE=force",
			"DISPLAY=1",
		)
		return cmd
	}
	cmd := exec.Command("ssh", alias)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func createAskpassScript() string {
	binPath, err := os.Executable()
	if err != nil {
		return ""
	}
	tmpDir := os.TempDir()
	scriptPath := filepath.Join(tmpDir, fmt.Sprintf("tssm-askpass-%d.sh", os.Getpid()))
	content := "#!/usr/bin/env bash\nexec " + shellQuote(binPath) + " __askpass --host \"$TSSM_HOST\" --user \"$TSSM_USER\" --kind password\n"
	if err := os.WriteFile(scriptPath, []byte(content), 0o700); err != nil {
		return ""
	}
	return scriptPath
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func connectInPlace(alias string) error {
	return execSSH(alias, os.Stdin, os.Stdout, os.Stderr)
}

func execSSH(alias string, stdin io.Reader, stdout, stderr io.Writer) error {
	termio.SanitizeStdinBeforeExec(os.Stdin, os.Stderr)
	cmd := exec.Command("ssh", alias)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func runAskpass(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("__askpass", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var host, user, kind string
	fs.StringVar(&host, "host", "", "Host alias")
	fs.StringVar(&user, "user", "", "Username")
	fs.StringVar(&kind, "kind", "password", "Credential kind")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("usage: tmux-ssh-manager __askpass --host <alias> [--user <user>] [--kind password]")
	}
	secret, err := credentials.Reveal(host, user, kind)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(stdout, secret)
	return err
}

// runSSHPassthrough execs the real ssh or scp binary with all original args,
// injecting SSH_ASKPASS when a stored credential matches the destination host.
func runSSHPassthrough(binary string, args []string) error {
	binPath, err := exec.LookPath(binary)
	if err != nil {
		return fmt.Errorf("%s not found in PATH", binary)
	}

	cmd := exec.Command(binPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Resolve the destination host/user and inject askpass if a stored credential matches.
	if dest := extractSSHCredentialTarget(binary, args); dest.host != "" {
		hosts, loadErr := sshconfig.LoadDefault()
		if loadErr == nil {
			hostUsers := make(map[string]string, len(hosts))
			for _, h := range hosts {
				hostUsers[h.Alias] = h.User
			}
			user, ok := resolveCredentialUser(dest.host, dest.user, hostUsers[dest.host])
			if ok {
				script := createAskpassScript()
				if script != "" {
					defer os.Remove(script)
					// Disable pubkey auth so SSH doesn't burn auth attempts
					// by sending the login password as key passphrases.
					askpassArgs := []string{
						"-o", "PubkeyAuthentication=no",
						"-o", "PreferredAuthentications=keyboard-interactive,password",
					}
					askpassArgs = append(askpassArgs, args...)
					cmd = exec.Command(binPath, askpassArgs...)
					cmd.Stdin = os.Stdin
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					cmd.Env = append(os.Environ(),
						"TSSM_HOST="+dest.host,
						"TSSM_USER="+user,
						"SSH_ASKPASS="+script,
						"SSH_ASKPASS_REQUIRE=force",
						"DISPLAY=1",
					)
				}
			}
		}
	}

	return cmd.Run()
}

type sshCredentialTarget struct {
	host string
	user string
}

// extractSSHCredentialTarget extracts the destination host and user from ssh/scp arguments.
// For ssh: the first non-flag argument is the destination. The user may come from
// user@host, -l user, or -o User=<user>.
// For scp: the first remote argument (user@host:path or host:path) is used.
func extractSSHCredentialTarget(binary string, args []string) sshCredentialTarget {
	// ssh flags that consume the next argument.
	sshArgFlags := map[string]bool{
		"-b": true, "-c": true, "-D": true, "-E": true, "-e": true,
		"-F": true, "-I": true, "-i": true, "-J": true, "-L": true,
		"-l": true, "-m": true, "-O": true, "-o": true, "-p": true,
		"-Q": true, "-R": true, "-S": true, "-W": true, "-w": true,
	}
	var cliUser string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "-oUser=") {
			cliUser = strings.TrimPrefix(arg, "-oUser=")
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if arg == "-l" && i+1 < len(args) {
				cliUser = args[i+1]
			}
			if arg == "-o" && i+1 < len(args) {
				if key, value, ok := splitSSHOption(args[i+1]); ok && strings.EqualFold(key, "User") {
					cliUser = value
				}
			}
			if sshArgFlags[arg] && i+1 < len(args) {
				i++ // skip the flag's value
			}
			continue
		}
		// For scp, look for user@host:path or host:path patterns.
		if binary == "scp" {
			if idx := strings.Index(arg, ":"); idx > 0 {
				host, user := hostAndUserFromDestination(arg[:idx])
				if cliUser != "" && user == "" {
					user = cliUser
				}
				return sshCredentialTarget{host: host, user: user}
			}
			continue
		}
		// For ssh, the first non-flag arg is the destination.
		host, user := hostAndUserFromDestination(arg)
		if cliUser != "" && user == "" {
			user = cliUser
		}
		return sshCredentialTarget{host: host, user: user}
	}
	return sshCredentialTarget{}
}

func hostAndUserFromDestination(dest string) (host, user string) {
	if at := strings.LastIndex(dest, "@"); at >= 0 {
		return dest[at+1:], dest[:at]
	}
	return dest, ""
}

func splitSSHOption(option string) (key, value string, ok bool) {
	parts := strings.SplitN(option, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

func resolveCredentialUser(host string, candidates ...string) (string, bool) {
	seen := make(map[string]struct{}, len(candidates)+1)
	for _, candidate := range append(candidates, "") {
		candidate = strings.TrimSpace(candidate)
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if credGet(host, candidate, "password") == nil {
			return candidate, true
		}
	}
	return "", false
}
