package manager

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// This file provides the SSH collection runner for LLDP topology discovery.
//
// It is intentionally UI-agnostic: the Bubble Tea TUI can call into this layer
// (typically via a tea.Cmd wrapper) and then render the resulting graph.
//
// Collection strategy:
// - For each selected configured host, choose LLDP command specs based on Host.network_os
// - Execute via OpenSSH (BuildSSHCommand) with a timeout
// - Try specs in order until one parses successfully
// - Return per-host successes and failures (including "no neighbors" as success with 0 entries)
//
// NOTE ABOUT AUTH:
// - This runner uses BuildSSHCommand(..., extraArgs...) and exec.CommandContext.
// - If you later need to support macOS Keychain askpass flows for non-interactive collection,
//   you can add an alternate path that uses the internal connector (if it can return stdout
//   deterministically). For now, this uses plain OpenSSH invocation.

// LLDPCollectResult captures the outcome of one successful device collection.
type LLDPCollectResult struct {
	Host   ResolvedHost
	Spec   LLDPCommandSpec
	Parsed LLDPParseResult

	Stdout string
	Stderr string
	// Duration is how long the successful attempt took.
	Duration time.Duration
}

// LLDPCollectFailure captures the outcome of a device where no command spec produced
// a successful parse.
type LLDPCollectFailure struct {
	Host ResolvedHost

	// Attempts contains a record of each tried command spec and its outcome.
	Attempts []LLDPCollectAttempt

	// Err is a summarized failure message suitable for UI.
	Err string
}

// LLDPCollectAttempt is one command attempt against a host.
type LLDPCollectAttempt struct {
	Spec     LLDPCommandSpec
	Stdout   string
	Stderr   string
	ExitErr  string
	ParseErr string
	Duration time.Duration
}

// LLDPCollectOptions controls concurrency and timeouts.
type LLDPCollectOptions struct {
	// Concurrency limits how many hosts are collected at once.
	// If <=0, defaults to 6.
	Concurrency int

	// DefaultTimeout is used when a spec does not provide a timeout.
	// If <=0, defaults to 10s.
	DefaultTimeout time.Duration

	// SSHConnectTimeoutSeconds sets SSH -o ConnectTimeout (seconds).
	// If <=0, defaults to 5.
	SSHConnectTimeoutSeconds int

	// SSHBatchMode forces -o BatchMode=yes to avoid hanging on password prompts.
	// Default true.
	SSHBatchMode bool

	// AdditionalSSHOptions are appended as "-o", "Key=Value" pairs.
	// Example: []string{"StrictHostKeyChecking=accept-new"}.
	AdditionalSSHOptions []string
}

func DefaultLLDPCollectOptions() LLDPCollectOptions {
	return LLDPCollectOptions{
		Concurrency:              6,
		DefaultTimeout:           10 * time.Second,
		SSHConnectTimeoutSeconds: 5,
		SSHBatchMode:             true,
		AdditionalSSHOptions:     nil,
	}
}

// CollectLLDPForHosts runs LLDP collection for the given resolved hosts.
//
// It returns:
// - successes: hosts that produced a parsed result (including 0-neighbor results)
// - failures: hosts where all attempts failed (ssh error and/or parse error)
//
// The order of returned successes/failures is not guaranteed to match input order.
func CollectLLDPForHosts(ctx context.Context, cfg *Config, targets []ResolvedHost, opts LLDPCollectOptions) (successes []LLDPCollectResult, failures []LLDPCollectFailure, err error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("nil config")
	}
	if len(targets) == 0 {
		return nil, nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Apply defaults field-by-field (LLDPCollectOptions contains a slice, so it is not comparable).
	if opts.Concurrency <= 0 {
		opts.Concurrency = 6
	}
	if opts.DefaultTimeout <= 0 {
		opts.DefaultTimeout = 10 * time.Second
	}
	if opts.SSHConnectTimeoutSeconds <= 0 {
		opts.SSHConnectTimeoutSeconds = 5
	}
	// Default to non-interactive collection unless explicitly disabled by the caller.
	// (Zero value is false, so we only force-enable when no explicit choice is available.)
	// For now: default true.
	if !opts.SSHBatchMode {
		opts.SSHBatchMode = true
	}

	type job struct {
		r ResolvedHost
	}
	type outMsg struct {
		ok  *LLDPCollectResult
		bad *LLDPCollectFailure
	}

	jobs := make(chan job)
	outs := make(chan outMsg)

	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		for j := range jobs {
			ok, bad := collectOneHost(ctx, cfg, j.r, opts)
			if ok != nil {
				outs <- outMsg{ok: ok}
			} else if bad != nil {
				outs <- outMsg{bad: bad}
			}
		}
	}

	nw := opts.Concurrency
	if nw > len(targets) {
		nw = len(targets)
	}
	wg.Add(nw)
	for i := 0; i < nw; i++ {
		go worker()
	}

	go func() {
		defer close(jobs)
		for _, r := range targets {
			select {
			case <-ctx.Done():
				return
			case jobs <- job{r: r}:
			}
		}
	}()

	// Close outs after workers finish
	go func() {
		wg.Wait()
		close(outs)
	}()

	var oks []LLDPCollectResult
	var bads []LLDPCollectFailure
	for m := range outs {
		if m.ok != nil {
			oks = append(oks, *m.ok)
		} else if m.bad != nil {
			bads = append(bads, *m.bad)
		}
	}

	// If ctx canceled, return partial results and ctx error.
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.Canceled) && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		// unexpected ctx error
		return oks, bads, ctx.Err()
	}
	return oks, bads, ctx.Err()
}

func collectOneHost(ctx context.Context, cfg *Config, r ResolvedHost, opts LLDPCollectOptions) (*LLDPCollectResult, *LLDPCollectFailure) {
	hostName := strings.TrimSpace(r.Host.Name)
	if hostName == "" {
		return nil, &LLDPCollectFailure{Host: r, Err: "empty host name"}
	}

	// Determine which OS to use for neighbor discovery command selection.
	//
	// Precedence:
	// 1) Per-host extras device_os (local override; does not require YAML inventory fields)
	// 2) YAML host.network_os (legacy)
	//
	// If still empty, treat as unsupported (no discovery command specs).
	nos := ""
	if hk := strings.TrimSpace(r.Host.Name); hk != "" {
		if ex, exErr := LoadHostExtras(hk); exErr == nil {
			// device_os is stored in per-host extras as a string, e.g. cisco_iosxe | sonic_dell
			nos = strings.ToLower(strings.TrimSpace(ex.DeviceOS))
		}
	}
	if nos == "" {
		nos = strings.ToLower(strings.TrimSpace(r.Host.NetworkOS))
	}
	specs := DefaultLLDPCommands(nos)
	if len(specs) == 0 {
		return nil, &LLDPCollectFailure{
			Host: r,
			Err:  fmt.Sprintf("no LLDP command specs for network_os=%q", nos),
		}
	}

	attempts := make([]LLDPCollectAttempt, 0, len(specs))
	for _, spec := range specs {
		// Use per-spec timeout when present; otherwise default.
		timeout := spec.Timeout
		if timeout <= 0 {
			timeout = opts.DefaultTimeout
		}

		a := LLDPCollectAttempt{Spec: spec}
		start := time.Now()

		stdout, stderr, exitErr := runSSHCommandAttempt(ctx, r, spec.Command, timeout, opts)
		a.Duration = time.Since(start)
		a.Stdout = stdout
		a.Stderr = stderr
		if exitErr != nil {
			a.ExitErr = exitErr.Error()
			attempts = append(attempts, a)
			continue
		}

		parsed, perr := ParseLLDPOutput(spec.ParserID, hostName, stdout)
		if perr != nil {
			a.ParseErr = perr.Error()
			attempts = append(attempts, a)
			continue
		}

		// Success. Note: parsed.Entries may be empty (0 neighbors); that's still a success.
		return &LLDPCollectResult{
			Host:     r,
			Spec:     spec,
			Parsed:   parsed,
			Stdout:   stdout,
			Stderr:   stderr,
			Duration: a.Duration,
		}, nil
	}

	// Summarize failure.
	summary := "lldp: all command attempts failed"
	// Prefer the last attempt error if present.
	if len(attempts) > 0 {
		last := attempts[len(attempts)-1]
		if last.ExitErr != "" {
			summary = "ssh failed: " + last.ExitErr
		} else if last.ParseErr != "" {
			summary = "parse failed: " + last.ParseErr
		}
	}

	return nil, &LLDPCollectFailure{
		Host:     r,
		Attempts: attempts,
		Err:      summary,
	}
}

func runSSHCommandAttempt(parent context.Context, r ResolvedHost, remoteCmd string, timeout time.Duration, opts LLDPCollectOptions) (stdout string, stderr string, exitErr error) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	remoteCmd = strings.TrimSpace(remoteCmd)
	if remoteCmd == "" {
		return "", "", fmt.Errorf("empty remote command")
	}

	// Build SSH argv:
	// ssh [resolved args...] -o BatchMode=yes -o ConnectTimeout=... -- <remoteCmd>
	extra := make([]string, 0, 16)

	if opts.SSHBatchMode {
		extra = append(extra, "-o", "BatchMode=yes")
	}
	if opts.SSHConnectTimeoutSeconds > 0 {
		extra = append(extra, "-o", fmt.Sprintf("ConnectTimeout=%d", opts.SSHConnectTimeoutSeconds))
	}
	for _, kv := range opts.AdditionalSSHOptions {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		extra = append(extra, "-o", kv)
	}

	// Always separate destination from command.
	extra = append(extra, "--", remoteCmd)

	argv := BuildSSHCommand(r, extra...)
	if len(argv) == 0 {
		return "", "", fmt.Errorf("empty ssh argv")
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	outB, outErr := cmd.Output()
	stdout = string(outB)

	// Capture stderr if available
	if outErr != nil {
		// If it's ExitError, it contains Stderr.
		if ee, ok := outErr.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		exitErr = outErr
		return stdout, stderr, exitErr
	}

	// Some devices write prompts/banners to stderr even on success; capture best-effort.
	// (Go's exec.Command.Output only returns stdout; stderr is only available on ExitError.)
	// If you want full capture for success paths, switch to CombinedOutput and split; but
	// CombinedOutput loses strict stdout/stderr separation. Keep current behavior for now.
	return stdout, stderr, nil
}
