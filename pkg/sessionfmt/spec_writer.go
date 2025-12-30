package sessionfmt

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// This package exports tmux-ssh-manager Dashboards / RecordedDashboards into the
// tmux-session-manager project-local spec format.
//
// Why:
// - Dashboards are "named multi-pane views" already.
// - tmux-session-manager specs provide a richer, standardized representation for
//   windows, deterministic splits (pane_plan), and future enhancements.
// - We can optionally materialize dashboards by delegating execution to the
//   tmux-session-manager binary (if installed), while keeping the legacy in-process
//   dashboard materializer as a fallback.
//
// Constraints / design choices:
// - This file only WRITE/serialize specs. It does not execute tmux commands.
// - It targets the *formal* schema used by tmux-session-manager (pkg/spec).
//   To avoid a hard Go module dependency between tmux-ssh-manager and tmux-session-manager,
//   we duplicate a minimal compatible struct shape here for serialization.
//
// Security model (aligned with tmux-session-manager):
// - Default output uses only safe actions: "run", "send_keys" (no "shell" or "tmux").
// - We avoid "sleep" actions because tmux-session-manager currently models them as shell,
//   which is gated behind AllowShell.
// - For repeat/refresh workflows, we support a SAFE "builtin watch mode" at the YAML level
//   by exporting a structured watch action, which tmux-session-manager can compile into
//   safe send-keys on the remote shell after SSH connects.

const (
	// DefaultSessionSpecVersion matches tmux-session-manager/pkg/spec.CurrentVersion.
	DefaultSessionSpecVersion = 1

	// DefaultSpecFilenameYAML is a conventional filename for export.
	DefaultSpecFilenameYAML = ".tmux-session.yaml"
	DefaultSpecFilenameJSON = ".tmux-session.json"
)

// OutputFormat controls which serialization is produced.
type OutputFormat string

const (
	FormatYAML OutputFormat = "yaml"
	FormatJSON OutputFormat = "json"
)

// WriterOptions controls spec export behavior.
type WriterOptions struct {
	// SessionName is the tmux session name to embed in the spec (spec.session.name).
	// If empty, it will be derived from ExportName.
	SessionName string

	// ExportName is used for metadata and default session naming when SessionName is empty.
	ExportName string

	// Description is optional; included as spec.description.
	Description string

	// Root is the spec session root (spec.session.root).
	// If empty, uses "${PROJECT_PATH}".
	Root string

	// Attach is stored in spec.session.attach (default true if nil).
	Attach *bool

	// SwitchClient is stored in spec.session.switch_client (default true if nil).
	SwitchClient *bool

	// BaseIndex / PaneBaseIndex are optional tmux options.
	BaseIndex     *int
	PaneBaseIndex *int

	// Deterministic splits:
	// - When true, emit windows[].pane_plan based on PanePlan.
	// - When false, emit windows[].panes (order only; split geometry must be inferred by executor).
	PreferPanePlan bool

	// PanePlan describes the deterministic split plan. If empty and PreferPanePlan is true,
	// we will emit a simple default plan (h splits at 50%) for N panes.
	PanePlan PanePlan

	// Layout is applied as windows[].layout when non-empty (tmux layout name or custom layout string).
	Layout string

	// WindowName is the tmux window name for the exported dashboard window.
	// If empty, defaults to "dashboard".
	WindowName string

	// PaneTitles controls whether pane plan panes get a "name" field set.
	// This does not necessarily set tmux pane titles; it is metadata for UIs.
	PaneTitles bool

	// ShellProgram is used for "run" actions that need shell evaluation.
	// Default "bash".
	ShellProgram string

	// ShellFlag is the flag used with ShellProgram to run a command.
	// Default "-lc".
	ShellFlag string

	// Now, if provided, is used for timestamps in meta (optional).
	Now func() time.Time
}

// PanePlan is a deterministic split plan.
// It is interpreted left-to-right by tmux-session-manager:
//   - first step must be pane
//   - split applies to active pane
//   - pane after split describes newly created pane
type PanePlan struct {
	Steps []PanePlanStep
}

// PanePlanStep is either a Pane or a Split.
type PanePlanStep struct {
	Pane  *PanePlanPane
	Split *PanePlanSplit
}

type PanePlanPane struct {
	Name    string
	Root    string
	Focus   bool
	Actions []Action
	Command string // shorthand; will be normalized to a shell action by validator in tmux-session-manager
}

type PanePlanSplit struct {
	Direction string // "h" or "v"
	Size      string // "50%" or "20"
}

// ----- Spec schema (minimal compatible subset for serialization) -----

type Spec struct {
	Version     int               `json:"version" yaml:"version"`
	Name        string            `json:"name,omitempty" yaml:"name,omitempty"`
	Description string            `json:"description,omitempty" yaml:"description,omitempty"`
	Session     Session           `json:"session,omitempty" yaml:"session,omitempty"`
	Env         map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	Windows     []Window          `json:"windows,omitempty" yaml:"windows,omitempty"`
	Actions     []Action          `json:"actions,omitempty" yaml:"actions,omitempty"`
	Meta        map[string]string `json:"meta,omitempty" yaml:"meta,omitempty"`
}

type Session struct {
	Name          string `json:"name,omitempty" yaml:"name,omitempty"`
	Prefix        string `json:"prefix,omitempty" yaml:"prefix,omitempty"`
	Root          string `json:"root,omitempty" yaml:"root,omitempty"`
	Attach        *bool  `json:"attach,omitempty" yaml:"attach,omitempty"`
	SwitchClient  *bool  `json:"switch_client,omitempty" yaml:"switch_client,omitempty"`
	BaseIndex     *int   `json:"base_index,omitempty" yaml:"base_index,omitempty"`
	PaneBaseIndex *int   `json:"pane_base_index,omitempty" yaml:"pane_base_index,omitempty"`

	// FocusWindow requests selecting a specific window after creation.
	// Supported: "active", numeric string (window index), or window name.
	// This matches tmux-session-manager/pkg/spec semantics.
	FocusWindow string `json:"focus_window,omitempty" yaml:"focus_window,omitempty"`
}

type Window struct {
	Name   string `json:"name" yaml:"name"`
	Root   string `json:"root,omitempty" yaml:"root,omitempty"`
	Layout string `json:"layout,omitempty" yaml:"layout,omitempty"`

	// Focus indicates this window should be selected after creation.
	// NOTE: For deterministic final focus across multiple windows, prefer Session.FocusWindow.
	Focus bool `json:"focus,omitempty" yaml:"focus,omitempty"`

	// FocusPane requests focusing a specific pane after panes are created.
	// Supported: "active" or numeric string (pane index relative to pane-base-index).
	// This matches tmux-session-manager/pkg/spec semantics.
	FocusPane string `json:"focus_pane,omitempty" yaml:"focus_pane,omitempty"`

	Panes    []Pane      `json:"panes,omitempty" yaml:"panes,omitempty"`
	PanePlan []PanePlanY `json:"pane_plan,omitempty" yaml:"pane_plan,omitempty"`
	Actions  []Action    `json:"actions,omitempty" yaml:"actions,omitempty"`
}

type Pane struct {
	Name    string   `json:"name,omitempty" yaml:"name,omitempty"`
	Root    string   `json:"root,omitempty" yaml:"root,omitempty"`
	Focus   bool     `json:"focus,omitempty" yaml:"focus,omitempty"`
	Actions []Action `json:"actions,omitempty" yaml:"actions,omitempty"`
	Command string   `json:"command,omitempty" yaml:"command,omitempty"`
}

// PanePlanY matches the formal tagged union encoding in tmux-session-manager spec:
//   - pane: {...} OR split: {...}
type PanePlanY struct {
	Pane  *PanePlanPaneY  `json:"pane,omitempty" yaml:"pane,omitempty"`
	Split *PanePlanSplitY `json:"split,omitempty" yaml:"split,omitempty"`
}

type PanePlanPaneY struct {
	Name    string   `json:"name,omitempty" yaml:"name,omitempty"`
	Root    string   `json:"root,omitempty" yaml:"root,omitempty"`
	Focus   bool     `json:"focus,omitempty" yaml:"focus,omitempty"`
	Actions []Action `json:"actions,omitempty" yaml:"actions,omitempty"`
	Command string   `json:"command,omitempty" yaml:"command,omitempty"`
}

type PanePlanSplitY struct {
	Direction string `json:"direction" yaml:"direction"`
	Size      string `json:"size,omitempty" yaml:"size,omitempty"`
}

type Action struct {
	Type     string          `json:"type" yaml:"type"`
	Target   Target          `json:"target,omitempty" yaml:"target,omitempty"`
	Tmux     *TmuxAction     `json:"tmux,omitempty" yaml:"tmux,omitempty"`
	Run      *RunAction      `json:"run,omitempty" yaml:"run,omitempty"`
	SendKeys *SendKeysAction `json:"send_keys,omitempty" yaml:"send_keys,omitempty"`
	Shell    *ShellAction    `json:"shell,omitempty" yaml:"shell,omitempty"`
	Sleep    *SleepAction    `json:"sleep,omitempty" yaml:"sleep,omitempty"`

	// WaitForPrompt is a SAFE best-effort readiness gate exported by tmux-ssh-manager.
	//
	// Intended semantics (to be implemented by the tmux-session-manager executor):
	// - Poll pane output (e.g. capture-pane) until it looks ready for input (prompt/quiet heuristic)
	// - Then wait SettleMS before proceeding
	WaitForPrompt *WaitForPromptAction `json:"wait_for_prompt,omitempty" yaml:"wait_for_prompt,omitempty"`

	// SshManagerConnect is a SAFE structured SSH connect action exported by tmux-ssh-manager.
	//
	// This is intended to be compiled/executed by tmux-session-manager (not by tmux-ssh-manager),
	// so the spec can rehydrate using password auth via Keychain/askpass without blocking on
	// an interactive password prompt in the pane.
	SshManagerConnect *SshManagerConnectAction `json:"ssh_manager_connect,omitempty" yaml:"ssh_manager_connect,omitempty"`

	// Watch is a SAFE builtin convenience action exported by tmux-ssh-manager.
	//
	// Intended semantics (to be implemented by the tmux-session-manager executor/compiler):
	// - Wrap `command` into: watch -n <interval_s> -t -- <command>
	// - Send it to the target pane (send-keys + Enter)
	//
	// This keeps "repeat" workflows declarative without relying on shell passthrough.
	Watch *WatchAction `json:"watch,omitempty" yaml:"watch,omitempty"`

	Ignore  bool   `json:"ignore_error,omitempty" yaml:"ignore_error,omitempty"`
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`
}

type Target struct {
	Session string `json:"session,omitempty" yaml:"session,omitempty"`
	Window  string `json:"window,omitempty" yaml:"window,omitempty"`
	Pane    string `json:"pane,omitempty" yaml:"pane,omitempty"`
}

type TmuxAction struct {
	Name string   `json:"name" yaml:"name"`
	Args []string `json:"args,omitempty" yaml:"args,omitempty"`
}

type RunAction struct {
	Program string   `json:"program" yaml:"program"`
	Args    []string `json:"args,omitempty" yaml:"args,omitempty"`
	Enter   *bool    `json:"enter,omitempty" yaml:"enter,omitempty"`
}

type SendKeysAction struct {
	Keys  []string `json:"keys" yaml:"keys"`
	Enter bool     `json:"enter,omitempty" yaml:"enter,omitempty"`
}

type ShellAction struct {
	Cmd   string `json:"cmd" yaml:"cmd"`
	Shell string `json:"shell,omitempty" yaml:"shell,omitempty"`
}

type SleepAction struct {
	Milliseconds int `json:"ms" yaml:"ms"`
}

// WatchAction describes a safe, declarative "repeat this command" request.
//
// NOTE: This is exported in the YAML/JSON spec as `type: watch`, but it requires
// tmux-session-manager to understand and compile it.
type WatchAction struct {
	// IntervalSeconds is optional; if <=0, consumers should treat it as 2 seconds.
	IntervalSeconds int `json:"interval_s,omitempty" yaml:"interval_s,omitempty"`

	// Command is the raw command to run repeatedly on the remote shell after SSH connects.
	// Example: "show clock"
	Command string `json:"command" yaml:"command"`
}

// SshManagerConnectAction describes a safe, structured SSH connect request.
//
// NOTE: This is exported in the YAML/JSON spec as `type: ssh_manager_connect`, and requires
// tmux-session-manager support to actually perform askpass/Keychain-backed auth.
// If unsupported, it will be rejected by spec validation as an unknown action type.
type SshManagerConnectAction struct {
	Host string `json:"host" yaml:"host"`

	// Optional; if empty, ssh default user resolution applies.
	User string `json:"user,omitempty" yaml:"user,omitempty"`

	// Optional; if <=0, ssh default applies.
	Port int `json:"port,omitempty" yaml:"port,omitempty"`

	// LoginMode: askpass|manual|key (default askpass)
	LoginMode string `json:"login_mode,omitempty" yaml:"login_mode,omitempty"`

	// ConnectTimeoutMS is a best-effort bound for the connect attempt. If <=0, executor default.
	ConnectTimeoutMS int `json:"connect_timeout_ms,omitempty" yaml:"connect_timeout_ms,omitempty"`
}

// WaitForPromptAction is a SAFE, best-effort readiness gate intended for interactive targets (e.g. SSH).
//
// It approximates an "expect" sequence without requiring shell passthrough by allowing an executor to
// poll pane output (e.g. via tmux capture-pane) until:
//  1. output matches a prompt-like regex (or executor default), AND
//  2. output has remained unchanged for at least MinQuietMS (to allow banners/MOTD to settle), AND
//  3. an optional SettleMS elapses before proceeding.
//
// This is meant to run immediately before `type: watch` in exported specs.
//
// NOTE: This is exported in the YAML/JSON spec as `type: wait_for_prompt`, but it requires
// tmux-session-manager to understand and execute it. If unsupported, it will be rejected by
// spec validation as an unknown action type.
type WaitForPromptAction struct {
	// TimeoutMS bounds total wait time. If <=0, treat as 15000.
	TimeoutMS int `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`

	// MinQuietMS requires pane output to be unchanged for this long before considering it ready.
	// If <=0, treat as 500.
	MinQuietMS int `json:"min_quiet_ms,omitempty" yaml:"min_quiet_ms,omitempty"`

	// SettleMS is an extra delay after readiness is detected, before allowing subsequent actions to proceed.
	// If <=0, treat as 250.
	SettleMS int `json:"settle_ms,omitempty" yaml:"settle_ms,omitempty"`

	// PromptRegex is an optional regex used to detect a prompt-like last line.
	// If empty, the executor should use a conservative default.
	PromptRegex string `json:"prompt_regex,omitempty" yaml:"prompt_regex,omitempty"`

	// MaxLines controls how many lines of pane output to consider (e.g. last N lines).
	// If <=0, the executor should choose a safe default (e.g. 200).
	MaxLines int `json:"max_lines,omitempty" yaml:"max_lines,omitempty"`
}

// ----- Public: high-level export API -----

// DashboardPane is a minimal interface/shape needed from tmux-ssh-manager.
// It matches manager.DashPane/RecordedPane semantics: Host + Commands.
type DashboardPane struct {
	Title    string
	Host     string
	Commands []string
	Env      map[string]string // optional
}

// DashboardExport is the minimal data required to write a spec.
type DashboardExport struct {
	Name        string
	Description string
	Layout      string
	Panes       []DashboardPane
}

// BuildSpecFromDashboard converts a dashboard-like definition into a tmux-session-manager Spec.
//
// It builds a single-window spec, where each pane runs:
//  1. ssh to the host
//  2. then sends the commands (as part of the ssh session) by chaining them into the shell
//
// For safety/compatibility, it uses "run" actions and runs shell via bash -lc.
// This avoids needing "shell" actions in the spec (which are opt-in/unsafe).
func BuildSpecFromDashboard(d DashboardExport, opt WriterOptions) (*Spec, error) {
	if strings.TrimSpace(d.Name) == "" && strings.TrimSpace(opt.ExportName) == "" {
		return nil, errors.New("dashboard export must have a name")
	}
	if len(d.Panes) == 0 {
		return nil, errors.New("dashboard export must have at least one pane")
	}

	now := time.Now
	if opt.Now != nil {
		now = opt.Now
	}

	shellProg := strings.TrimSpace(opt.ShellProgram)
	if shellProg == "" {
		shellProg = "bash"
	}
	shellFlag := strings.TrimSpace(opt.ShellFlag)
	if shellFlag == "" {
		shellFlag = "-lc"
	}

	name := strings.TrimSpace(d.Name)
	if name == "" {
		name = strings.TrimSpace(opt.ExportName)
	}

	sessionName := strings.TrimSpace(opt.SessionName)
	if sessionName == "" {
		sessionName = sanitizeName(name)
		if sessionName == "" {
			sessionName = "dashboard"
		}
	}

	root := strings.TrimSpace(opt.Root)
	if root == "" {
		root = "${PROJECT_PATH}"
	}

	windowName := strings.TrimSpace(opt.WindowName)
	if windowName == "" {
		windowName = "dashboard"
	}

	// Build panes as pane_plan if preferred.
	//
	// Focus semantics to match tmux-session-manager:
	// - Prefer Window.FocusPane for deterministic final pane selection.
	// - PanePlanPane.Focus remains supported as metadata, but is considered legacy by
	//   tmux-session-manager (often interpreted as "focus window").
	win := Window{
		Name:   windowName,
		Root:   root,
		Layout: strings.TrimSpace(nonEmpty(opt.Layout, d.Layout)),
		Panes:  nil,
	}

	// If a pane was marked focused in the plan, translate it to windows[].focus_pane = "<index>"
	// (relative to pane-base-index). This is best-effort based on plan order.
	if opt.PreferPanePlan && len(opt.PanePlan.Steps) > 0 {
		focusedStepIdx := -1
		for i, st := range opt.PanePlan.Steps {
			if st.Pane != nil && st.Pane.Focus {
				focusedStepIdx = i
				break
			}
		}
		if focusedStepIdx >= 0 {
			// Map "pane steps" to a pane ordinal (0-based), then apply pane-base-index.
			paneOrdinal := 0
			for i := 0; i <= focusedStepIdx; i++ {
				if opt.PanePlan.Steps[i].Pane != nil {
					paneOrdinal++
				}
			}
			// paneOrdinal is count; convert to 0-based index within panes.
			paneIdx0 := paneOrdinal - 1

			base := 0
			if opt.PaneBaseIndex != nil {
				base = *opt.PaneBaseIndex
			}
			win.FocusPane = strconv.Itoa(base + paneIdx0)
		}
	}

	if opt.PreferPanePlan {
		win.PanePlan = buildPanePlanY(d, opt, shellProg, shellFlag)
	} else {
		win.Panes = buildPanesList(d, opt, shellProg, shellFlag)
	}

	meta := map[string]string{
		"exported_by": "tmux-ssh-manager",
		"exported_at": now().UTC().Format(time.RFC3339),
		"source":      "dashboard",
	}
	// stable pane host list for metadata (useful for preview/diffing)
	hosts := make([]string, 0, len(d.Panes))
	for _, p := range d.Panes {
		h := strings.TrimSpace(p.Host)
		if h != "" {
			hosts = append(hosts, h)
		}
	}
	sort.Strings(hosts)
	if len(hosts) > 0 {
		meta["hosts"] = strings.Join(hosts, ",")
	}

	// Builtin watch mode metadata:
	// If the dashboard was saved using tmux-ssh-manager's :watch/:watchall commands, the recorded
	// commands in panes will include `watch -n ...` lines today. We intentionally avoid encoding
	// that as opaque shell strings in exported specs. Instead, tmux-session-manager should
	// eventually compile `type: watch` actions.
	//
	// Until tmux-session-manager adds support, these remain declarative intent only.
	meta["watch_mode"] = "builtin"
	meta["watch_mode_note"] = "requires tmux-session-manager support for type=watch; otherwise unknown action type"

	// Focus precedence to match tmux-session-manager intent:
	// 1) If the session has an explicit final focus window, honor it.
	// 2) Otherwise, if any window has Focus=true, use that window name as session.focus_window
	//    so final focus is stable even if windows are created after it.
	// 3) Otherwise, leave focus_window empty (executor default).
	focusWindow := ""
	for i := range []Window{win} {
		if ([]Window{win})[i].Focus {
			focusWindow = ([]Window{win})[i].Name
			break
		}
	}
	if focusWindow != "" {
		// Normalize "active" and trim whitespace (tmux-session-manager lowercases during validation)
		focusWindow = strings.TrimSpace(focusWindow)
	}

	specDoc := &Spec{
		Version:     DefaultSessionSpecVersion,
		Name:        name,
		Description: strings.TrimSpace(nonEmpty(opt.Description, d.Description)),
		Session: Session{
			Name:          sessionName,
			Root:          root,
			Attach:        opt.Attach,
			SwitchClient:  opt.SwitchClient,
			BaseIndex:     opt.BaseIndex,
			PaneBaseIndex: opt.PaneBaseIndex,
			FocusWindow:   focusWindow,
		},
		Env:     nil,
		Windows: []Window{win},
		Meta:    meta,
	}

	return specDoc, nil
}

// WriteSpecFile writes the spec to path in the requested format.
// Parent directories are created (0700).
func WriteSpecFile(path string, format OutputFormat, s *Spec) error {
	if s == nil {
		return errors.New("nil spec")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("empty output path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	var b []byte
	var err error

	switch format {
	case FormatJSON:
		b, err = json.MarshalIndent(s, "", "  ")
		if err != nil {
			return fmt.Errorf("encode json: %w", err)
		}
		b = append(b, '\n')
	default:
		// YAML
		b, err = yaml.Marshal(s)
		if err != nil {
			return fmt.Errorf("encode yaml: %w", err)
		}
		// Ensure trailing newline for nicer diffs.
		if len(b) == 0 || b[len(b)-1] != '\n' {
			b = append(b, '\n')
		}
	}

	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// WriteDashboardSpec writes a dashboard export into a spec file at outputPath.
func WriteDashboardSpec(outputPath string, format OutputFormat, d DashboardExport, opt WriterOptions) error {
	s, err := BuildSpecFromDashboard(d, opt)
	if err != nil {
		return err
	}
	return WriteSpecFile(outputPath, format, s)
}

// SuggestedDashboardSpecPath returns a suggested output path for an exported dashboard spec.
// It writes under a directory (typically ~/.config/tmux-ssh-manager/dashboards) using a
// stable, sanitized filename.
//
// Example:
//
//	SuggestedDashboardSpecPath("~/.config/tmux-ssh-manager/dashboards", "core-status", "yaml")
//	  -> ~/.config/tmux-ssh-manager/dashboards/core-status.tmux-session.yaml
func SuggestedDashboardSpecPath(outputDir string, dashboardName string, format OutputFormat) (string, error) {
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return "", errors.New("outputDir is empty")
	}

	base := sanitizeName(dashboardName)
	if base == "" {
		base = "dashboard"
	}

	ext := ".tmux-session.yaml"
	if format == FormatJSON {
		ext = ".tmux-session.json"
	}

	return filepath.Join(outputDir, base+ext), nil
}

// ----- Helpers: spec construction -----

func buildPanesList(d DashboardExport, opt WriterOptions, shellProg, shellFlag string) []Pane {
	out := make([]Pane, 0, len(d.Panes))
	for i, p := range d.Panes {
		paneName := ""
		if opt.PaneTitles {
			if strings.TrimSpace(p.Title) != "" {
				paneName = strings.TrimSpace(p.Title)
			} else if strings.TrimSpace(p.Host) != "" {
				paneName = strings.TrimSpace(p.Host)
			} else {
				paneName = fmt.Sprintf("pane-%d", i+1)
			}
		}
		out = append(out, Pane{
			Name:    paneName,
			Focus:   i == 0,
			Actions: paneActionsForHost(p, shellProg, shellFlag),
		})
	}
	return out
}

func buildPanePlanY(d DashboardExport, opt WriterOptions, shellProg, shellFlag string) []PanePlanY {
	// If an explicit plan was provided, use it.
	if len(opt.PanePlan.Steps) > 0 {
		return convertPanePlanToY(d, opt, shellProg, shellFlag)
	}

	// Default: first pane, then split horizontally at 50% repeatedly.
	steps := make([]PanePlanY, 0, maxInt(1, len(d.Panes))*2)
	for i, p := range d.Panes {
		if i == 0 {
			steps = append(steps, PanePlanY{
				Pane: &PanePlanPaneY{
					Name:    paneDisplayName(p, opt, i),
					Focus:   true,
					Actions: paneActionsForHost(p, shellProg, shellFlag),
				},
			})
			continue
		}
		steps = append(steps, PanePlanY{
			Split: &PanePlanSplitY{Direction: "h", Size: "50%"},
		})
		steps = append(steps, PanePlanY{
			Pane: &PanePlanPaneY{
				Name:    paneDisplayName(p, opt, i),
				Focus:   false,
				Actions: paneActionsForHost(p, shellProg, shellFlag),
			},
		})
	}
	return steps
}

func convertPanePlanToY(d DashboardExport, opt WriterOptions, shellProg, shellFlag string) []PanePlanY {
	// This maps user steps to the tagged union encoding.
	// Pane steps map to pane definitions; split steps map to split definitions.
	out := make([]PanePlanY, 0, len(opt.PanePlan.Steps))
	paneIdx := 0

	for _, st := range opt.PanePlan.Steps {
		if st.Split != nil {
			out = append(out, PanePlanY{
				Split: &PanePlanSplitY{
					Direction: strings.TrimSpace(st.Split.Direction),
					Size:      strings.TrimSpace(st.Split.Size),
				},
			})
			continue
		}
		if st.Pane != nil {
			// If the pane refers to a dashboard pane index, we map sequentially.
			var src DashboardPane
			if paneIdx < len(d.Panes) {
				src = d.Panes[paneIdx]
			}
			paneIdx++

			actions := st.Pane.Actions
			if len(actions) == 0 && strings.TrimSpace(st.Pane.Command) != "" {
				// Shorthand -> shell action; this is intentionally unsafe unless enabled by policy in executor.
				actions = []Action{
					{Type: "shell", Shell: &ShellAction{Cmd: st.Pane.Command}},
				}
			}
			// If still empty, build actions from dashboard pane (ssh + commands)
			if len(actions) == 0 {
				actions = paneActionsForHost(src, shellProg, shellFlag)
			}

			name := strings.TrimSpace(st.Pane.Name)
			if name == "" {
				name = paneDisplayName(src, opt, paneIdx-1)
			}

			out = append(out, PanePlanY{
				Pane: &PanePlanPaneY{
					Name:    name,
					Root:    st.Pane.Root,
					Focus:   st.Pane.Focus,
					Actions: actions,
					Command: st.Pane.Command,
				},
			})
		}
	}

	return out
}

func paneDisplayName(p DashboardPane, opt WriterOptions, idx int) string {
	if !opt.PaneTitles {
		return ""
	}
	if strings.TrimSpace(p.Title) != "" {
		return strings.TrimSpace(p.Title)
	}
	if strings.TrimSpace(p.Host) != "" {
		return strings.TrimSpace(p.Host)
	}
	return fmt.Sprintf("pane-%d", idx+1)
}

func paneActionsForHost(p DashboardPane, shellProg, shellFlag string) []Action {
	host := strings.TrimSpace(p.Host)

	// Export model:
	// - Always start an interactive SSH session in the pane (run: bash -lc "ssh <host>").
	// - Then describe follow-up commands as *structured* actions that can be replayed safely.
	//
	// Builtin watch mode (YAML-level):
	// - If a recorded command looks like: `watch -n <N> -t -- <cmd...>`
	//   we export it as:
	//     - type: watch
	//       watch: { interval_s: <N>, command: "<cmd...>" }
	//
	// This keeps the exported spec declarative and avoids embedding opaque shell strings.
	//
	// NOTE: tmux-session-manager must implement `type: watch` compilation for this to work.
	actions := make([]Action, 0, 1+len(p.Commands)+len(p.Env))

	sshLine := "ssh " + host
	if host == "" {
		sshLine = "bash" // fallback
	}

	actions = append(actions, Action{
		Type: "run",
		Run: &RunAction{
			Program: shellProg,
			Args:    []string{shellFlag, sshLine},
			Enter:   boolPtr(true),
		},
	})

	// Export env vars in the pane before commands (best-effort).
	// We do this after starting ssh only if commands are local. For remote, users can include `export ...`
	// commands in their Commands list. Here, we export in the shell before ssh by sending it prior.
	// To keep behavior consistent and simple, we inject env via send_keys before the first run
	// by prepending them as send_keys actions. But since run is first, we instead:
	// - if env present, we modify sshLine to `export ...; exec ssh ...`
	// This is local env, not remote env.
	if len(p.Env) > 0 && host != "" {
		exports := make([]string, 0, len(p.Env))
		for k, v := range p.Env {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			exports = append(exports, fmt.Sprintf("export %s=%s", k, shellQuote(v)))
		}
		sort.Strings(exports)
		combined := strings.Join(exports, "; ") + "; exec ssh " + host
		actions[0].Run.Args = []string{shellFlag, combined}
	}

	for _, c := range p.Commands {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}

		// Convert recorded `watch -n <N> -t -- <cmd...>` into structured actions:
		//
		//  1) ssh_manager_connect (safe, structured): lets tmux-session-manager reuse the
		//     tmux-ssh-manager Keychain service via askpass to avoid blocking on password prompts.
		//  2) wait_for_prompt (safe): best-effort "expect-like" gate for banners/MOTD.
		//  3) watch (safe): repeat helper.
		//
		// IMPORTANT:
		// We still keep the initial `run: ssh <host>` for compatibility with older session-manager versions,
		// but when `ssh_manager_connect` is present, tmux-session-manager should prefer it (or the spec author
		// can remove the raw ssh run line once migration is complete).
		if wa, ok := parseRecordedWatchCommand(c); ok {
			actions = append(actions,
				Action{
					Type: "ssh_manager_connect",
					SshManagerConnect: &SshManagerConnectAction{
						Host:             host,
						User:             "",
						Port:             0,
						LoginMode:        "askpass",
						ConnectTimeoutMS: 0,
					},
				},
				Action{
					Type: "wait_for_prompt",
					WaitForPrompt: &WaitForPromptAction{
						TimeoutMS:   15000,
						MinQuietMS:  500,
						SettleMS:    250,
						PromptRegex: "",
						MaxLines:    200,
					},
				},
				Action{
					Type:  "watch",
					Watch: &wa,
				},
			)
			continue
		}

		actions = append(actions, Action{
			Type: "send_keys",
			SendKeys: &SendKeysAction{
				Keys:  []string{c},
				Enter: true,
			},
		})
	}

	return actions
}

// ----- Utility -----

func boolPtr(v bool) *bool { return &v }

// parseRecordedWatchCommand attempts to parse the tmux-ssh-manager recorded watch wrapper:
//
//	watch -n <interval> -t -- <cmd...>
//
// Returns (WatchAction, true) on success; otherwise (zero, false).
func parseRecordedWatchCommand(s string) (WatchAction, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return WatchAction{}, false
	}

	// Require prefix.
	if !strings.HasPrefix(s, "watch ") {
		return WatchAction{}, false
	}

	// Tokenize with simple whitespace rules. This matches how the TUI constructs the wrapper.
	parts := strings.Fields(s)
	if len(parts) < 6 {
		// Minimum: watch -n N -t -- CMD
		return WatchAction{}, false
	}
	if parts[0] != "watch" || parts[1] != "-n" {
		return WatchAction{}, false
	}

	interval, err := strconv.Atoi(parts[2])
	if err != nil || interval <= 0 {
		return WatchAction{}, false
	}

	// Expect: -t -- then the rest is the command.
	if parts[3] != "-t" || parts[4] != "--" {
		return WatchAction{}, false
	}

	// Reconstruct command from the original string so we preserve spaces/quoting as typed.
	// Find the first occurrence of "--" token boundary and take everything after it.
	idx := strings.Index(s, "--")
	if idx < 0 {
		return WatchAction{}, false
	}
	cmd := strings.TrimSpace(s[idx+2:])
	if cmd == "" {
		return WatchAction{}, false
	}

	return WatchAction{
		IntervalSeconds: interval,
		Command:         cmd,
	}, true
}

func nonEmpty(a, b string) string {
	a = strings.TrimSpace(a)
	if a != "" {
		return a
	}
	return strings.TrimSpace(b)
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastUnderscore = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		case r == '-' || r == '_':
			b.WriteRune('_')
			lastUnderscore = true
		default:
			if !lastUnderscore {
				b.WriteRune('_')
				lastUnderscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "session"
	}
	return out
}

func shellQuote(s string) string {
	// Conservative single-quote escape for sh-like shells.
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n\"'\\$`&|;<>()*?!~#{}") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\"'\"'`) + "'"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
