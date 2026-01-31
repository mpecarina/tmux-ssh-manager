// Package manager contains configuration types and helpers for tmux-ssh-manager.
package manager

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the full YAML configuration for tmux-ssh-manager.
//
// Example YAML:
//
// groups:
//   - name: dc1
//     default_user: netops
//     default_port: 22
//     jump_host: bastion.dc1.example.com
//
// hosts:
//   - name: rtr1.dc1.example.com
//     group: dc1
//     user: admin
//     tags: [router, ios-xe]
type Config struct {
	Groups     []Group     `yaml:"groups"`
	Hosts      []Host      `yaml:"hosts"`
	Dashboards []Dashboard `yaml:"dashboards,omitempty"`

	// Macros are reusable command lists (primarily for network engineers).
	// These can be invoked from the TUI command bar (e.g. :run <macro>).
	Macros []Macro `yaml:"macros,omitempty"`
}

// Group defines defaults that apply to all hosts referencing this group.
type Group struct {
	Name        string `yaml:"name"`
	DefaultUser string `yaml:"default_user,omitempty"`
	DefaultPort int    `yaml:"default_port,omitempty"`
	JumpHost    string `yaml:"jump_host,omitempty"`

	// ConnectDelayMS is an optional delay (in milliseconds) to wait after starting SSH
	// before sending on_connect commands. Useful for slow logins and for reliably starting
	// remote `watch` loops.
	ConnectDelayMS int `yaml:"connect_delay_ms,omitempty"`

	OnConnect   []string `yaml:"on_connect,omitempty"`
	PreConnect  []string `yaml:"pre_connect,omitempty"`
	PostConnect []string `yaml:"post_connect,omitempty"`

	// Registers are named command lists available to hosts in this group (host-level overrides/extends).
	Registers []Register `yaml:"registers,omitempty"`
}

// Macro is a named list of commands that can be sent after connecting.
// Intended for common network workflows (show commands, setup steps, etc).
type Macro struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description,omitempty"`
	Commands    []string `yaml:"commands"`
}

// Host defines a connectable endpoint and any overrides from its group.
type Host struct {
	// Name is the hostname or IP. Can include a DNS suffix.
	Name string `yaml:"name"`

	// Group references a Group.Name. Optional.
	Group string `yaml:"group,omitempty"`

	// Optional overrides for user/port/jump_host. If empty/zero, group or global defaults apply.
	User     string   `yaml:"user,omitempty"`
	Port     int      `yaml:"port,omitempty"`
	JumpHost string   `yaml:"jump_host,omitempty"`
	Tags     []string `yaml:"tags,omitempty"`

	// NetworkDevice marks this host as a network device (switch/router/firewall) as opposed to
	// a generic Linux host. This is used for topology discovery (LLDP command selection, parsing,
	// and rendering emphasis).
	NetworkDevice bool `yaml:"network_device,omitempty"`

	// NetworkOS is an enum-like string identifying the network OS family for this device.
	// This is used to select the correct LLDP neighbor commands and parsers.
	//
	// Supported values (initial):
	// - "cisco_iosxe"
	// - "sonic_dell"
	//
	// Empty is allowed when network_device=false.
	NetworkOS string `yaml:"network_os,omitempty"`

	// ConnectDelayMS optionally overrides the group-level connect_delay_ms for this host.
	// If unset/0, the group-level value (if any) should be used. Callers may apply a global
	// default (e.g. 500ms) if the effective value is still 0.
	ConnectDelayMS int `yaml:"connect_delay_ms,omitempty"`

	// LoginMode controls how authentication is handled for this host.
	// Supported values:
	// - "" / "manual"  (default): do not attempt to supply credentials programmatically
	// - "askpass": use a Keychain-backed SSH_ASKPASS flow (macOS-only, requires stored credential)
	LoginMode string `yaml:"login_mode,omitempty"`

	OnConnect   []string `yaml:"on_connect,omitempty"`
	PreConnect  []string `yaml:"pre_connect,omitempty"`
	PostConnect []string `yaml:"post_connect,omitempty"`

	// Registers are named command lists available when this host is the active pane/target.
	Registers []Register `yaml:"registers,omitempty"`
}

// ResolvedHost captures the effective settings after merging group defaults with host overrides.
type ResolvedHost struct {
	Host                 Host
	Group                *Group
	EffectiveUser        string
	EffectivePort        int
	EffectiveJumpHost    string
	EffectiveOnConnect   []string
	EffectivePreConnect  []string
	EffectivePostConnect []string

	// EffectiveConnectDelayMS is the resolved delay (in milliseconds) to wait after starting SSH
	// before sending on_connect commands.
	// Resolution: host.connect_delay_ms overrides group.connect_delay_ms (if set).
	EffectiveConnectDelayMS int
}

// Register is a named sequence of commands for quick paste into remote terminals.
type Register struct {
	// Name is the identifier (e.g. "a", "ops", "warmup"). Must be unique.
	Name string `yaml:"name"`

	// Description is optional UI text.
	Description string `yaml:"description,omitempty"`

	// Commands are sent in order; each requires an extra Enter by the operator to execute on the remote host.
	Commands []string `yaml:"commands"`
}

// ErrConfigNotFound is returned when no configuration file can be located.
var ErrConfigNotFound = errors.New("config not found")

// LoadConfig discovers and loads the YAML configuration.
// If explicitPath is empty, it searches common locations in order:
// 1. $TMUX_SSH_MANAGER_CONFIG
// 2. $XDG_CONFIG_HOME/tmux-ssh-manager/hosts.yaml
// 3. ~/.config/tmux-ssh-manager/hosts.yaml
//
// Returns the parsed Config and the path that was used.
func LoadConfig(explicitPath string) (*Config, string, error) {
	candidates := ConfigPathCandidates(explicitPath)
	var lastErr error
	for _, p := range candidates {
		p = expandPath(p)
		if p == "" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			lastErr = err
			continue
		}
		var cfg Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, p, fmt.Errorf("parse yaml %s: %w", p, err)
		}
		if err := cfg.Validate(); err != nil {
			return nil, p, fmt.Errorf("invalid config %s: %w", p, err)
		}
		return &cfg, p, nil
	}
	if lastErr == nil {
		lastErr = ErrConfigNotFound
	}
	return nil, "", lastErr
}

// ConfigPathCandidates returns possible configuration file paths, in priority order.
// If explicitPath is provided, it is returned first (expanded).
func ConfigPathCandidates(explicitPath string) []string {
	var out []string
	if explicitPath != "" {
		out = append(out, explicitPath)
	}
	if env := os.Getenv("TMUX_SSH_MANAGER_CONFIG"); env != "" {
		out = append(out, env)
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg != "" {
		out = append(out, filepath.Join(xdg, "tmux-ssh-manager", "hosts.yaml"))
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		out = append(out, filepath.Join(home, ".config", "tmux-ssh-manager", "hosts.yaml"))
	}
	return out
}

// Validate performs basic sanity checks on the configuration.
//
// - Group names must be unique and non-empty.
// - Hosts must have non-empty names.
// - Hosts referencing a group must reference an existing group.
// - login_mode must be one of: "" | manual | askpass
// - If hosts[].network_device is true, hosts[].network_os must be a supported value
func (c *Config) Validate() error {
	// Index groups
	seenGroups := map[string]struct{}{}
	for i, g := range c.Groups {
		if strings.TrimSpace(g.Name) == "" {
			return fmt.Errorf("groups[%d]: name is required", i)
		}
		if _, dup := seenGroups[g.Name]; dup {
			return fmt.Errorf("groups[%d]: duplicate group name %q", i, g.Name)
		}
		if g.ConnectDelayMS < 0 {
			return fmt.Errorf("groups[%d](%s).connect_delay_ms: must be >= 0", i, g.Name)
		}
		seenGroups[g.Name] = struct{}{}
	}

	// Validate hosts
	for i, h := range c.Hosts {
		if strings.TrimSpace(h.Name) == "" {
			return fmt.Errorf("hosts[%d]: name is required", i)
		}
		if strings.TrimSpace(h.Group) != "" {
			if _, ok := seenGroups[h.Group]; !ok {
				return fmt.Errorf("hosts[%d]: group %q not found", i, h.Group)
			}
		}

		if h.ConnectDelayMS < 0 {
			return fmt.Errorf("hosts[%d](%s).connect_delay_ms: must be >= 0", i, h.Name)
		}

		// login_mode validation (per-host)
		switch strings.ToLower(strings.TrimSpace(h.LoginMode)) {
		case "", "manual", "askpass":
			// ok
		default:
			return fmt.Errorf("hosts[%d](%s): invalid login_mode %q (expected: manual|askpass)", i, h.Name, h.LoginMode)
		}

		// network_os validation (per-host)
		nd := h.NetworkDevice
		nos := strings.ToLower(strings.TrimSpace(h.NetworkOS))
		if nd {
			switch nos {
			case "cisco_iosxe", "sonic_dell":
				// ok
			default:
				return fmt.Errorf("hosts[%d](%s): invalid network_os %q (expected: cisco_iosxe|sonic_dell)", i, h.Name, h.NetworkOS)
			}
		}
	}

	// Validate macros
	seenMacros := map[string]struct{}{}
	for i, m := range c.Macros {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			return fmt.Errorf("macros[%d]: name is required", i)
		}
		if _, dup := seenMacros[name]; dup {
			return fmt.Errorf("macros[%d]: duplicate macro name %q", i, name)
		}
		seenMacros[name] = struct{}{}

		if len(m.Commands) == 0 {
			return fmt.Errorf("macros[%d](%s): at least one command is required", i, name)
		}
		for j, cmd := range m.Commands {
			if strings.TrimSpace(cmd) == "" {
				return fmt.Errorf("macros[%d](%s).commands[%d]: empty command string", i, name, j)
			}
		}
	}

	return nil
}

// GroupByName builds a name->Group index. Last definition wins on duplicate names
// (should not happen due to Validate, but function is defensive).
func (c *Config) GroupByName() map[string]Group {
	m := make(map[string]Group, len(c.Groups))
	for _, g := range c.Groups {
		m[g.Name] = g
	}
	return m
}

// HostByName returns a pointer to the first host matching the provided name,
// or nil if not found.
func (c *Config) HostByName(name string) *Host {
	name = strings.TrimSpace(name)
	for i := range c.Hosts {
		if c.Hosts[i].Name == name {
			return &c.Hosts[i]
		}
	}
	return nil
}

// ResolveEffective merges host with its group's defaults to produce a ResolvedHost.
// Rules:
// - user: host.user > group.default_user > $USER (if available) > ""
// - port: host.port > group.default_port > 22
// jump_host: host.jump_host > group.jump_host > ""
func (c *Config) ResolveEffective(h Host) ResolvedHost {
	var grp *Group
	if h.Group != "" {
		gi := c.GroupByName()
		if g, ok := gi[h.Group]; ok {
			grp = &g
		}
	}

	// User
	userVal := strings.TrimSpace(h.User)
	if userVal == "" && grp != nil {
		userVal = strings.TrimSpace(grp.DefaultUser)
	}

	// Connect delay (ms): host override wins, else group value, else 0 (caller may default).
	delayMS := 0
	if grp != nil && grp.ConnectDelayMS > 0 {
		delayMS = grp.ConnectDelayMS
	}
	if h.ConnectDelayMS > 0 {
		delayMS = h.ConnectDelayMS
	}
	if userVal == "" {
		userVal = currentUsername()
	}

	// Port
	port := h.Port
	if port <= 0 && grp != nil && grp.DefaultPort > 0 {
		port = grp.DefaultPort
	}
	if port <= 0 {
		port = 22
	}

	// Jump host
	jump := strings.TrimSpace(h.JumpHost)
	if jump == "" && grp != nil {
		jump = strings.TrimSpace(grp.JumpHost)
	}

	// OnConnect: group defaults first, then host-specific
	var oc []string
	if grp != nil && len(grp.OnConnect) > 0 {
		oc = append(oc, grp.OnConnect...)
	}
	if len(h.OnConnect) > 0 {
		oc = append(oc, h.OnConnect...)
	}

	// PreConnect: group first, then host
	var pre []string
	if grp != nil && len(grp.PreConnect) > 0 {
		pre = append(pre, grp.PreConnect...)
	}
	if len(h.PreConnect) > 0 {
		pre = append(pre, h.PreConnect...)
	}

	// PostConnect: group first, then host
	var post []string
	if grp != nil && len(grp.PostConnect) > 0 {
		post = append(post, grp.PostConnect...)
	}
	if len(h.PostConnect) > 0 {
		post = append(post, h.PostConnect...)
	}

	return ResolvedHost{
		Host:                    h,
		Group:                   grp,
		EffectiveUser:           userVal,
		EffectivePort:           port,
		EffectiveJumpHost:       jump,
		EffectiveOnConnect:      oc,
		EffectivePreConnect:     pre,
		EffectivePostConnect:    post,
		EffectiveConnectDelayMS: delayMS,
	}
}

// BuildSSHCommand constructs the argv slice for exec'ing OpenSSH.
// The command is: ssh [options] destination [extraArgs...]
//
// - destination is "user@host" if user is not empty, else just "host"
// - adds "-p <port>" if port != 22
// - adds "-J <jumpHost>" if EffectiveJumpHost is set
// - adds "-i <identityFile>" if per-host identity_file override is set in host extras
// - Optional SSH multiplexing:
//   - If TMUX_SSH_MANAGER_SSH_MUX is set (non-empty and not "0"/"false"/"no"),
//     adds ControlMaster/ControlPath/ControlPersist options to reuse SSH transport/auth.
//   - ControlPath can be overridden with TMUX_SSH_MANAGER_SSH_MUX_PATH (a literal path template is not supported here;
//     provide a concrete path if you want full control). By default we use:
//     ~/.config/tmux-ssh-manager/mux/<user>@<host>_<port>.sock
//
// - extraArgs are appended verbatim to allow caller to pass flags like "-o", etc.
func BuildSSHCommand(r ResolvedHost, extraArgs ...string) []string {
	destHost := r.Host.Name
	dest := destHost
	if r.EffectiveUser != "" {
		dest = r.EffectiveUser + "@" + dest
	}

	args := []string{}
	// Port
	if r.EffectivePort > 0 && r.EffectivePort != 22 {
		args = append(args, "-p", strconv.Itoa(r.EffectivePort))
	}
	// ProxyJump
	if r.EffectiveJumpHost != "" {
		args = append(args, "-J", r.EffectiveJumpHost)
	}

	// Optional SSH multiplexing (transport/auth reuse).
	if muxEnabled() {
		cp := muxControlPath(r)
		if cp != "" {
			// Be conservative: only add if we have a deterministic path.
			args = append(args,
				"-o", "ControlMaster=auto",
				"-o", "ControlPersist=10m",
				"-o", "ControlPath="+cp,
			)
		}
	}

	// IdentityFile override (best-effort; only if per-host extras specify identity_file).
	// Note: this is intentionally non-fatal; if extras cannot be loaded, we fall back to ssh defaults.
	if hostKey := strings.TrimSpace(r.Host.Name); hostKey != "" {
		if ex, err := LoadHostExtras(hostKey); err == nil {
			if id := strings.TrimSpace(ex.IdentityFile); id != "" {
				id = expandPath(id)
				if id != "" {
					args = append(args, "-i", id)
				}
			}
		}
	}

	// construct
	cmd := []string{"ssh"}
	cmd = append(cmd, args...)
	cmd = append(cmd, dest)
	if len(extraArgs) > 0 {
		cmd = append(cmd, extraArgs...)
	}
	return cmd
}

// muxEnabled reports whether SSH multiplexing is enabled via env var.
func muxEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_SSH_MUX")))
	if v == "" {
		return false
	}
	switch v {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// muxControlPath returns the ControlPath socket path for SSH multiplexing.
// If TMUX_SSH_MANAGER_SSH_MUX_PATH is set, it is used as-is (after ~ expansion).
// Otherwise, it defaults to: ~/.config/tmux-ssh-manager/mux/<user>@<host>_<port>.sock
func muxControlPath(r ResolvedHost) string {
	if p := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_SSH_MUX_PATH")); p != "" {
		p = expandPath(p)
		return strings.TrimSpace(p)
	}

	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}

	userPart := strings.TrimSpace(r.EffectiveUser)
	if userPart == "" {
		userPart = currentUsername()
	}
	hostPart := strings.TrimSpace(r.Host.Name)
	if hostPart == "" {
		return ""
	}
	port := r.EffectivePort
	if port <= 0 {
		port = 22
	}

	// Keep path short and filesystem-safe.
	sockName := sanitizeHostKeyToFilename(userPart+"@"+hostPart) + "_" + strconv.Itoa(port) + ".sock"
	baseDir := filepath.Join(home, ".config", "tmux-ssh-manager", "mux")
	_ = os.MkdirAll(baseDir, 0o700)

	return filepath.Join(baseDir, sockName)
}

// BuildSSHCommandForHost is a convenience that finds a host by name from cfg,
// resolves it with group defaults, and builds ssh argv.
// Returns an error if the host cannot be found.
func BuildSSHCommandForHost(cfg *Config, hostName string, extraArgs ...string) ([]string, error) {
	h := cfg.HostByName(hostName)
	if h == nil {
		return nil, fmt.Errorf("host %q not found", hostName)
	}
	r := cfg.ResolveEffective(*h)
	return BuildSSHCommand(r, extraArgs...), nil
}

// currentUsername returns the current OS user name, or the USER env if lookup fails.
// Returns empty string if neither are available.
func currentUsername() string {
	if u, err := user.Current(); err == nil && u != nil && u.Username != "" {
		// Normalize username: user.Current().Username on macOS can be short name already,
		// but on some systems it may return a full path-like. Prefer basename to be safe.
		return filepath.Base(u.Username)
	}
	return os.Getenv("USER")
}

// expandPath expands leading "~" and environment variables in a path.
// If the input is empty, returns "".
func expandPath(p string) string {
	if p == "" {
		return ""
	}
	// Expand env vars like $HOME
	p = os.ExpandEnv(p)
	// Expand leading "~" or "~user"
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		if home != "" {
			if p == "~" {
				p = home
			} else if strings.HasPrefix(p, "~/") {
				p = filepath.Join(home, p[2:])
			}
			// Note: "~user" not handled to avoid userdb lookups; rare for local client config paths.
		}
	}
	return p
}
