# AGENTS.md — tmux-ssh-manager

## Overview

A tmux plugin that reads `Host` aliases from `~/.ssh/config`, presents them in a Bubble Tea picker, and dispatches tmux actions. Hosts come exclusively from OpenSSH config (primary file + `Include` directives). No YAML, no sidecar metadata.

## Architecture

```
cmd/tmux-ssh-manager/main.go
  └── app.Run()
       ├── sshconfig.Load()          — parse ~/.ssh/config + Includes
       ├── state.Load() / Save()     — favorites, recents (JSON)
       ├── credentials.*()           — macOS Keychain (Set/Get/Delete/Reveal)
       ├── tmuxrun.Session.*()       — new-window, split-v, split-h, tiled, logging
       └── tmuxui.App.Run()          — Bubble Tea picker
            ├── sshconfig (host display + add-host modal)
            ├── state (favorites/recents via injected callbacks)
            └── tmuxrun (via injected callbacks)
```

Imports flow downward. Packages do not import each other laterally.

## Repo Map

| File | Purpose |
|---|---|
| `cmd/tmux-ssh-manager/main.go` | Entrypoint — delegates to `app.Run()` |
| `pkg/app/app.go` | CLI routing, dependency wiring, askpass script creation, SSH/SCP passthrough |
| `pkg/sshconfig/sshconfig.go` | OpenSSH config parser, `Include` resolution, append-only host writer |
| `pkg/tmuxui/ui.go` | Bubble Tea picker, add-host modal, credential modal, vim-style navigation |
| `pkg/tmuxrun/tmuxrun.go` | tmux command construction, askpass-aware SSH commands, session logging |
| `pkg/state/state.go` | Favorites and recents persistence (atomic JSON, XDG-aware) |
| `pkg/credentials/credentials.go` | Credential normalization, service naming |
| `pkg/credentials/credentials_darwin.go` | macOS Keychain via `security` CLI (Set/Get/Delete/Reveal) |
| `pkg/credentials/credentials_other.go` | Non-darwin stub (returns `ErrUnsupported`) |
| `scripts/harness.sh` | Validation loop: fmt, test, build, smoke, live |
| `tmux-ssh-manager.tmux` | tmux plugin entrypoint (key binding registration) |
| `scripts/tmux_ssh_manager.tmux` | tmux launcher (popup or new-window), auto-build |

## CLI Subcommands

| Subcommand | Purpose |
|---|---|
| *(none)* | Interactive Bubble Tea picker |
| `list [--json]` | Print host aliases (or JSON array) |
| `connect <alias> [flags]` | SSH to host; supports `--dry-run`, `--split-count`, `--split-mode`, `--layout` |
| `add --alias ... --hostname ...` | Append host to `~/.ssh/config` |
| `cred set\|get\|delete --host <alias>` | Manage macOS Keychain credentials |
| `ssh <args...>` | Passthrough to real `ssh` with credential injection |
| `scp <args...>` | Passthrough to real `scp` with credential injection |
| `__askpass --host <alias>` | Internal: prints credential to stdout for `SSH_ASKPASS` |
| `print-ssh-config-path` | Print resolved config path |
| `--version` | Print version |

## Picker Flags

| Flag | Default | Description |
|---|---|---|
| `--mode` / `-m` | `search` | `search` (input focused) or `normal` (vim nav) |
| `--implicit-select` | `true` | `enter` acts on highlighted host from search mode |
| `--enter-mode` | `p` | Enter action: `p` (pane), `w` (window), `s` (split-h), `v` (split-v) |

## Credential Flow (SSH_ASKPASS)

1. On picker launch, `app.Run()` creates a temp askpass script (`/tmp/tssm-askpass-<pid>.sh`)
2. The script calls `tmux-ssh-manager __askpass --host "$TSSM_HOST" --user "$TSSM_USER"`
3. When connecting to a host with a stored credential, SSH env is set:
   - `TSSM_HOST=<alias>`, `TSSM_USER=<user>`, `SSH_ASKPASS=<script>`, `SSH_ASKPASS_REQUIRE=force`, `DISPLAY=1`
4. OpenSSH invokes the script → `__askpass` → `credentials.Reveal()` → password to stdout
5. Applies to: picker connections, `connect` subcommand, `ssh`/`scp` passthrough, tmux window/split/tiled
6. If no credential exists, plain `ssh <alias>` runs (normal password prompt)

Service naming: `tmux-ssh-manager:<host>:<kind>` — no collisions across hosts.

## Picker Keybindings

### Search mode (input focused)

| Key | Action |
|---|---|
| `esc` | Switch to normal mode (preserves filter) |
| `up` / `down` | Move cursor |
| `enter` | Connect to highlighted host (if implicit select on) |
| `ctrl+a` | Select all filtered |

### Normal mode

| Key | Action |
|---|---|
| `/` | Focus search |
| `j`/`k`, arrows | Move cursor |
| `ctrl+d`/`ctrl+u` | Half-page scroll |
| `gg`/`G` | Top / bottom |
| `space` | Toggle multi-select |
| `enter` | Default action (per `--enter-mode`) |
| `p` | Connect in current pane |
| `v` | Vertical split |
| `s` | Horizontal split |
| `w` | New tmux window |
| `t` | Tiled layout |
| `f` | Toggle favorite |
| `F` | Filter favorites |
| `R` | Filter recents |
| `ctrl+a` | Select all filtered |
| `a` | Add host modal |
| `c` | Store credential (macOS) |
| `d` | Delete credential (macOS) |
| `q`/`esc` | Quit |

## Session Logging

All SSH connections log output via `tmux pipe-pane`:

- Path: `~/.config/tmux-ssh-manager/logs/<alias>/YYYY-MM-DD.log`
- Respects `$XDG_CONFIG_HOME`; permissions 0700/0600
- Non-fatal — never blocks connections

## Package Quick Reference

| Need to change... | Work in... |
|---|---|
| Host parsing, `Include` resolution | `pkg/sshconfig` |
| Picker keybindings or UX | `pkg/tmuxui` |
| tmux command construction, askpass shell commands | `pkg/tmuxrun` |
| CLI flags, subcommands, askpass wiring | `pkg/app` |
| Keychain credential behavior | `pkg/credentials` |
| Favorites/recents persistence | `pkg/state` |
| Build/test/validation | `scripts/harness.sh`, `Makefile` |

## Design Constraints

- No YAML config files or sidecar metadata
- `~/.ssh/config` is the single source of truth for hosts
- Prefer append-only SSH config edits over in-place rewrites
- Packages stay small with narrow interfaces
- `pkg/tmuxui` is a picker, not a multi-purpose application
- tmux pane commands use `$SHELL -lc` to respect the user's login shell

## Change Workflow

1. Identify the owning package
2. Keep the change inside that package unless crossing an interface boundary
3. Add or update tests for parser, state, CLI, or command-builder changes
4. Run the harness before finishing:

```sh
make all
# or: scripts/harness.sh all
```

## What To Read First

1. This file
2. `pkg/app/app.go` — CLI routing and dependency wiring
3. The package that owns the behavior you need to change
