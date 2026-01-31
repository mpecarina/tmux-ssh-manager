package manager

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Dashboard defines a named, multi-pane view with optional tmux layout and commands.
// Dashboards are persisted in YAML as part of Config.
//
// Example YAML:
//
// dashboards:
//   - name: core-status
//     description: "Quick core status"
//     new_window: true
//     layout: main-vertical
//     panes:
//   - title: "RTR1 IF Brief"
//     host: rtr1.dc1.example.com
//     commands:
//   - terminal length 0
//   - show ip interface brief
//   - title: "RTR2 Routes"
//     filter:
//     group: dc1
//     name_contains: "rtr2"
//     commands:
//   - terminal length 0
//   - show ip route summary
type Dashboard struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	NewWindow   bool   `yaml:"new_window,omitempty"` // open the dashboard in a new tmux window
	Layout      string `yaml:"layout,omitempty"`     // tmux layout name or format (e.g. "even-horizontal", "main-vertical", or custom format string)
	// ConnectDelayMS is an optional delay (in milliseconds) after starting SSH in a pane
	// before sending on_connect/pane commands. This helps with slow logins and interactive shells.
	// Default behavior: if unset/0, callers should use a sensible default (e.g. 500ms).
	ConnectDelayMS int        `yaml:"connect_delay_ms,omitempty"`
	Panes          []DashPane `yaml:"panes"`

	// Registers references named command sequences that are scoped to this dashboard’s hosts.
	// These are exposed by the UI as quick paste actions for panes belonging to those hosts.
	Registers []string `yaml:"registers,omitempty"`
}

// DashPane describes one pane of a dashboard.
// You can target a host explicitly via `Host`, or select one via `Filter`.
// If both are provided, `Host` wins.
type DashPane struct {
	Title    string     `yaml:"title,omitempty"`
	Host     string     `yaml:"host,omitempty"`   // explicit host name (or SSH alias) from Config/SSH
	Filter   PaneFilter `yaml:"filter,omitempty"` // selection criteria if Host is not set
	Commands []string   `yaml:"commands,omitempty"`
	// ConnectDelayMS optionally overrides Dashboard.ConnectDelayMS for this specific pane.
	// If unset/0, the dashboard/global default should be used.
	ConnectDelayMS int         `yaml:"connect_delay_ms,omitempty"`
	Env            []EnvVarKV  `yaml:"env,omitempty"` // optional environment variables to export before running commands
	Tags           []string    `yaml:"tags,omitempty"`
	Meta           interface{} `yaml:"meta,omitempty"` // optional arbitrary metadata for future extensibility
}

// PaneFilter selects a host by criteria.
// All provided criteria must match (logical AND).
type PaneFilter struct {
	Group        string   `yaml:"group,omitempty"`         // match group name
	Tags         []string `yaml:"tags,omitempty"`          // required tags (all must be present)
	NameContains string   `yaml:"name_contains,omitempty"` // substring match on host name
	NameRegex    string   `yaml:"name_regex,omitempty"`    // regex match on host name (RE2-compatible)
}

// EnvVarKV holds a name/value pair to be exported in the pane before commands.
type EnvVarKV struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

// ResolvedDashboard is a fully-resolved dashboard, ready for execution.
type ResolvedDashboard struct {
	Dashboard Dashboard
	Panes     []ResolvedPane
}

// ResolvedPane includes the selected host and effective commands for a pane.
type ResolvedPane struct {
	Pane              DashPane
	Target            ResolvedHost
	EffectiveCommands []string // EffectiveOnConnect (group+host) + pane.Commands

	// EffectiveConnectDelayMS is the resolved delay (in milliseconds) to wait after starting SSH
	// in this pane before sending on_connect/pane commands.
	// Resolution: pane.connect_delay_ms overrides dashboard.connect_delay_ms (if set).
	EffectiveConnectDelayMS int
}

// ValidateDashboards ensures all dashboards in the config are sane.
//   - Each dashboard must have a unique non-empty name.
//   - Must have at least one pane.
//   - Each pane must specify either `host` or a filter which resolves to at least one host.
//     (Validation here checks presence; resolution will catch empties.)
//
// Returns an error on the first problem found.
func (c *Config) ValidateDashboards() error {
	seen := map[string]struct{}{}
	for i, d := range c.Dashboards {
		name := strings.TrimSpace(d.Name)
		if name == "" {
			return fmt.Errorf("dashboards[%d]: name is required", i)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("dashboards[%d]: duplicate dashboard name %q", i, name)
		}
		seen[name] = struct{}{}

		if len(d.Panes) == 0 {
			return fmt.Errorf("dashboards[%d](%s): at least one pane is required", i, name)
		}

		// validate connect_delay_ms (dashboard-level)
		if d.ConnectDelayMS < 0 {
			return fmt.Errorf("dashboards[%d](%s).connect_delay_ms: must be >= 0", i, name)
		}

		// validate register references (dashboard-level)
		// Register names are validated at runtime when the active host is known (host-scoped registers).

		for j, p := range d.Panes {
			if strings.TrimSpace(p.Host) == "" && !p.Filter.hasAnyCriterion() {
				return fmt.Errorf("dashboards[%d](%s).panes[%d]: specify either host or filter", i, name, j)
			}

			// validate connect_delay_ms (pane override)
			if p.ConnectDelayMS < 0 {
				return fmt.Errorf("dashboards[%d](%s).panes[%d].connect_delay_ms: must be >= 0", i, name, j)
			}

			for k, cmd := range p.Commands {
				if strings.TrimSpace(cmd) == "" {
					return fmt.Errorf("dashboards[%d](%s).panes[%d].commands[%d]: empty command string", i, name, j, k)
				}
			}
			for k, ev := range p.Env {
				if strings.TrimSpace(ev.Name) == "" {
					return fmt.Errorf("dashboards[%d](%s).panes[%d].env[%d]: env name is required", i, name, j, k)
				}
			}
		}
	}
	return nil
}

// ResolveDashboard resolves a dashboard into specific hosts and effective commands.
// Resolution rules:
//   - If pane.Host is set, resolve that host by name. Error if not found.
//   - Otherwise, use pane.Filter. If multiple hosts match, the first match is used.
//     (Future work: allow multi-target panes and batch actions.)
//   - EffectiveCommands = Target.EffectiveOnConnect + pane.Commands.
//
// Returns a ResolvedDashboard or an error.
func (c *Config) ResolveDashboard(d Dashboard) (ResolvedDashboard, error) {
	if err := c.ValidateDashboards(); err != nil {
		return ResolvedDashboard{}, err
	}
	rd := ResolvedDashboard{
		Dashboard: d,
		Panes:     make([]ResolvedPane, 0, len(d.Panes)),
	}
	for idx, p := range d.Panes {
		var h *Host
		if strings.TrimSpace(p.Host) != "" {
			h = c.HostByName(strings.TrimSpace(p.Host))
			if h == nil {
				return ResolvedDashboard{}, fmt.Errorf("dashboard %q: pane %d host %q not found", d.Name, idx, p.Host)
			}
		} else {
			matches, err := c.FilterHosts(p.Filter)
			if err != nil {
				return ResolvedDashboard{}, fmt.Errorf("dashboard %q: pane %d filter error: %v", d.Name, idx, err)
			}
			if len(matches) == 0 {
				return ResolvedDashboard{}, fmt.Errorf("dashboard %q: pane %d filter matched no hosts", d.Name, idx)
			}
			// Choose first match deterministically (Host order in config).
			h = &matches[0]
		}
		rh := c.ResolveEffective(*h)
		effectiveCmds := make([]string, 0, len(rh.EffectiveOnConnect)+len(p.Commands))
		// PreConnect is handled by the launcher before SSH; here we include OnConnect and pane-level commands.
		effectiveCmds = append(effectiveCmds, rh.EffectiveOnConnect...)
		effectiveCmds = append(effectiveCmds, p.Commands...)

		// Resolve connect delay: pane override wins, otherwise dashboard default.
		delayMS := d.ConnectDelayMS
		if p.ConnectDelayMS > 0 {
			delayMS = p.ConnectDelayMS
		}

		rd.Panes = append(rd.Panes, ResolvedPane{
			Pane:                    p,
			Target:                  rh,
			EffectiveCommands:       effectiveCmds,
			EffectiveConnectDelayMS: delayMS,
		})
	}
	return rd, nil
}

// FilterHosts returns all hosts matching the provided filter criteria.
// Matching rules (AND):
// - If Group is set, host.Group must equal it.
// - For Tags, all requested tags must be present in host.Tags (case-insensitive).
// - NameContains checks substring in Host.Name (case-insensitive).
// - NameRegex uses RE2 syntax to match Host.Name (case-insensitive).
func (c *Config) FilterHosts(f PaneFilter) ([]Host, error) {
	out := make([]Host, 0, len(c.Hosts))
	var re *regexp.Regexp
	var err error

	nameContains := strings.TrimSpace(f.NameContains)
	nameRegex := strings.TrimSpace(f.NameRegex)
	group := strings.TrimSpace(f.Group)

	if nameRegex != "" {
		re, err = regexp.Compile("(?i)" + nameRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid name_regex: %w", err)
		}
	}
	reqTags := normalizeTags(f.Tags)

	for _, h := range c.Hosts {
		if group != "" && !strings.EqualFold(strings.TrimSpace(h.Group), group) {
			continue
		}
		if len(reqTags) > 0 && !hostHasAllTags(h.Tags, reqTags) {
			continue
		}
		if nameContains != "" && !strings.Contains(strings.ToLower(h.Name), strings.ToLower(nameContains)) {
			continue
		}
		if re != nil && !re.MatchString(h.Name) {
			continue
		}
		out = append(out, h)
	}
	return out, nil
}

func (f PaneFilter) hasAnyCriterion() bool {
	return strings.TrimSpace(f.Group) != "" ||
		len(f.Tags) > 0 ||
		strings.TrimSpace(f.NameContains) != "" ||
		strings.TrimSpace(f.NameRegex) != ""
}

func hostHasAllTags(have []string, need []string) bool {
	if len(need) == 0 {
		return true
	}
	// Build case-insensitive set of existing tags.
	set := map[string]struct{}{}
	for _, t := range have {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			set[t] = struct{}{}
		}
	}
	for _, n := range need {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		if _, ok := set[n]; !ok {
			return false
		}
	}
	return true
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, 0, len(tags))
	seen := map[string]struct{}{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// ValidateLayoutName checks if the layout string is one of the common tmux presets,
// or a non-empty custom layout format. It does not validate custom formats.
// Returns nil if acceptable.
func ValidateLayoutName(layout string) error {
	layout = strings.TrimSpace(layout)
	if layout == "" {
		return nil // empty means "use tmux default"
	}
	switch strings.ToLower(layout) {
	case "even-horizontal", "even-vertical", "main-horizontal", "main-vertical", "tiled":
		return nil
	default:
		// Accept custom layout strings like "c30,128x32,0,0[64x32,0,0,0,64x32,64,0,1]"
		// Ensure non-empty and not obviously invalid (e.g., spaces).
		if strings.Contains(layout, "\n") {
			return errors.New("layout string must be single-line")
		}
		return nil
	}
}

// FindDashboard returns a pointer to the dashboard by name, or nil if not found.
func (c *Config) FindDashboard(name string) *Dashboard {
	name = strings.TrimSpace(name)
	for i := range c.Dashboards {
		if c.Dashboards[i].Name == name {
			return &c.Dashboards[i]
		}
	}
	return nil
}

// DashboardsIndex returns a name->Dashboard mapping for convenience.
func (c *Config) DashboardsIndex() map[string]Dashboard {
	m := make(map[string]Dashboard, len(c.Dashboards))
	for _, d := range c.Dashboards {
		if strings.TrimSpace(d.Name) != "" {
			m[d.Name] = d
		}
	}
	return m
}
