package manager

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"tmux-ssh-manager/pkg/sessionfmt"
)

// This file provides reusable, dependency-light integration between tmux-ssh-manager dashboards
// and tmux-session-manager's spec + engine.
//
// Goals:
// - Export resolved dashboards (multi-pane tmux views) into tmux-session-manager spec files (YAML/JSON).
// - Optionally apply those specs by invoking the tmux-session-manager binary in a tmux window.
// - Keep this integration optional and best-effort: failures should not break legacy dashboard behavior.
//
// Non-goals:
// - Make tmux-ssh-manager depend on tmux-session-manager as a Go module.
//   We integrate via spec files + a binary invocation.
//
// Notes:
// - tmux-session-manager now supports applying an arbitrary spec file via `--spec <path>`.
// - For safety, exported specs use safe actions (run + send_keys). No shell/tmux passthrough is emitted by default.

// DashboardSpecConfig controls export/apply behavior.
type DashboardSpecConfig struct {
	Enabled bool

	// OutputFormat: "yaml" or "json" (default "yaml")
	OutputFormat string

	// OutputDir: where specs are written (default: ~/.config/tmux-ssh-manager/dashboards)
	OutputDir string

	// DeterministicSplits: if true, export uses pane_plan
	DeterministicSplits bool

	// ApplyAfterExport: if true, will invoke tmux-session-manager to apply the spec
	ApplyAfterExport bool

	// SessionManagerBin: path or name of tmux-session-manager binary (default: "tmux-session-manager")
	SessionManagerBin string

	// TmuxWindowName: name for the tmux window used to run tmux-session-manager apply (default: "dash-apply")
	TmuxWindowName string

	// SessionPrefix: optional prefix used when exporting session names (default: "dash")
	SessionPrefix string

	// Timeout is not enforced in this integration yet; kept for future.
	Timeout time.Duration
}

// DashboardSpecDefaults returns a conservative default config.
func DashboardSpecDefaults() DashboardSpecConfig {
	return DashboardSpecConfig{
		Enabled:             false,
		OutputFormat:        "yaml",
		OutputDir:           "",
		DeterministicSplits: true,
		ApplyAfterExport:    true,
		SessionManagerBin:   "tmux-session-manager",
		TmuxWindowName:      "dash-apply",
		SessionPrefix:       "dash",
		Timeout:             0,
	}
}

// DashboardSpecConfigFromEnv builds config from environment variables used by tmux-ssh-manager.
//
// Supported env vars:
// - TMUX_SSH_MANAGER_USE_SESSION_MANAGER=1                => Enabled=true
// - TMUX_SSH_MANAGER_DASH_SPEC_FORMAT=yaml|json           => OutputFormat
// - TMUX_SSH_MANAGER_DASH_SPEC_DIR=/path                  => OutputDir
// - TMUX_SSH_MANAGER_DASH_DETERMINISTIC_SPLITS=0          => DeterministicSplits=false
// - TMUX_SSH_MANAGER_DASH_APPLY=0                         => ApplyAfterExport=false
// - TMUX_SESSION_MANAGER_BIN=/path/to/tmux-session-manager => SessionManagerBin
func DashboardSpecConfigFromEnv() DashboardSpecConfig {
	cfg := DashboardSpecDefaults()

	if strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_USE_SESSION_MANAGER")) != "" {
		cfg.Enabled = true
	}
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_SPEC_FORMAT"))); v != "" {
		if v == "json" {
			cfg.OutputFormat = "json"
		} else {
			cfg.OutputFormat = "yaml"
		}
	}
	cfg.OutputDir = strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_SPEC_DIR"))

	if strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_DETERMINISTIC_SPLITS")) == "0" {
		cfg.DeterministicSplits = false
	}
	if v := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_APPLY")); v != "" {
		// default apply is true; allow explicit disable with 0/off
		switch strings.ToLower(v) {
		case "0", "off", "false", "no":
			cfg.ApplyAfterExport = false
		default:
			cfg.ApplyAfterExport = true
		}
	}

	if v := strings.TrimSpace(os.Getenv("TMUX_SESSION_MANAGER_BIN")); v != "" {
		cfg.SessionManagerBin = v
	}
	return cfg
}

// ExportDashboardToSpecFile exports a resolved dashboard to a tmux-session-manager spec file.
// Returns the output path written to.
func ExportDashboardToSpecFile(
	cfgDir string,
	d Dashboard,
	rd ResolvedDashboard,
	specCfg DashboardSpecConfig,
) (string, error) {
	if !specCfg.Enabled {
		return "", errors.New("spec export disabled")
	}
	if strings.TrimSpace(d.Name) == "" {
		return "", errors.New("dashboard name is required")
	}
	if len(rd.Panes) == 0 {
		return "", fmt.Errorf("dashboard %q: no panes to export", d.Name)
	}

	outDir := strings.TrimSpace(specCfg.OutputDir)
	if outDir == "" {
		if strings.TrimSpace(cfgDir) == "" {
			var err error
			cfgDir, err = DefaultConfigDir()
			if err != nil {
				return "", err
			}
		}
		outDir = filepath.Join(cfgDir, "dashboards")
	}

	format := strings.ToLower(strings.TrimSpace(specCfg.OutputFormat))
	if format != "json" {
		format = "yaml"
	}

	// Build export payload from resolved dashboard data.
	exp := sessionfmt.DashboardExport{
		Name:        strings.TrimSpace(d.Name),
		Description: strings.TrimSpace(d.Description),
		Layout:      strings.TrimSpace(d.Layout),
		Panes:       make([]sessionfmt.DashboardPane, 0, len(rd.Panes)),
	}
	for _, rp := range rd.Panes {
		hostKey := strings.TrimSpace(rp.Target.Host.Name)
		cmds := make([]string, 0, len(rp.EffectiveCommands))
		for _, c := range rp.EffectiveCommands {
			c = strings.TrimSpace(c)
			if c != "" {
				cmds = append(cmds, c)
			}
		}
		exp.Panes = append(exp.Panes, sessionfmt.DashboardPane{
			Title:    strings.TrimSpace(rp.Pane.Title),
			Host:     hostKey,
			Commands: cmds,
			Env:      nil,
		})
	}

	_ = os.MkdirAll(outDir, 0o700)
	outPath, err := sessionfmt.SuggestedDashboardSpecPath(outDir, exp.Name, sessionfmt.OutputFormat(format))
	if err != nil {
		outPath = filepath.Join(outDir, "dashboard.tmux-session."+format)
	}

	wopt := sessionfmt.WriterOptions{
		SessionName:    "",
		ExportName:     exp.Name,
		Description:    exp.Description,
		Root:           outDir, // treat export dir as context root
		PreferPanePlan: specCfg.DeterministicSplits,
		Layout:         exp.Layout,
		WindowName:     "dashboard",
		PaneTitles:     true,
		ShellProgram:   "bash",
		ShellFlag:      "-lc",
		Now:            time.Now,
		Attach:         boolPtr(true),
		SwitchClient:   boolPtr(true),
	}
	if err := sessionfmt.WriteDashboardSpec(outPath, sessionfmt.OutputFormat(format), exp, wopt); err != nil {
		return "", err
	}
	return outPath, nil
}

// ApplySpecViaSessionManager invokes tmux-session-manager to apply a spec file.
// This is executed in a new tmux window so any output/errors are visible.
func ApplySpecViaSessionManager(specPath string, specCfg DashboardSpecConfig) error {
	if strings.TrimSpace(specPath) == "" {
		return errors.New("specPath is empty")
	}
	if !specCfg.Enabled {
		return errors.New("spec apply disabled")
	}
	if !specCfg.ApplyAfterExport {
		return errors.New("spec apply disabled by config (ApplyAfterExport=false)")
	}

	bin := strings.TrimSpace(specCfg.SessionManagerBin)
	if bin == "" {
		bin = "tmux-session-manager"
	}
	winName := strings.TrimSpace(specCfg.TmuxWindowName)
	if winName == "" {
		winName = "dash-apply"
	}

	// Use `--spec` integration mode. We intentionally do not pass allow-shell or allow-tmux-passthrough.
	// Exported dashboards are safe by default.
	cmdLine := fmt.Sprintf("%s --spec %q", shellEscapeForSh(bin), specPath)

	// Run in a new tmux window.
	// -c "#{pane_current_path}" keeps consistent user context.
	return exec.Command(
		"tmux",
		"new-window",
		"-n", winName,
		"-c", "#{pane_current_path}",
		"--",
		"bash", "-lc", cmdLine,
	).Run()
}

// ExportAndApplyDashboardSpec is a convenience helper for the common flow:
//
//	Resolve dashboard -> export spec -> optionally apply spec
//
// Returns the spec path written to.
func ExportAndApplyDashboardSpec(
	cfgDir string,
	cfg *Config,
	d Dashboard,
	specCfg DashboardSpecConfig,
) (string, error) {
	if cfg == nil {
		return "", errors.New("nil config")
	}
	if !specCfg.Enabled {
		return "", errors.New("spec integration disabled")
	}

	rd, err := cfg.ResolveDashboard(d)
	if err != nil {
		return "", err
	}

	specPath, err := ExportDashboardToSpecFile(cfgDir, d, rd, specCfg)
	if err != nil {
		return "", err
	}

	if specCfg.ApplyAfterExport {
		if err := ApplySpecViaSessionManager(specPath, specCfg); err != nil {
			// Keep this best-effort: return path + error so caller can fall back.
			return specPath, err
		}
	}

	return specPath, nil
}
