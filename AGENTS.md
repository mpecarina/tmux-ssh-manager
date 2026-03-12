# AGENTS.md — tmux-ssh-manager

## Overview

A tmux plugin that reads literal `Host` aliases from `~/.ssh/config`, presents them in a Bubble Tea picker, and dispatches tmux actions:

- open SSH sessions directly, in tmux windows, or in tmux splits
- persist favorites and recents
- append new host entries to `~/.ssh/config`
- manage macOS Keychain credentials through `cred set|get|delete`
- automatically log session output to `~/.config/tmux-ssh-manager/logs`

## Source Of Truth

The only host inventory is OpenSSH config.

- Primary file: `~/.ssh/config`
- Includes: resolved recursively via `Include` directives
- Wildcard host patterns (`*`, `?`, `[]`): excluded from the picker
- Add-host behavior: append-only writes to the primary config file

No YAML, no sidecar metadata, no parallel host stores.

## Architecture

```
cmd/tmux-ssh-manager/main.go
  └── app.Run()
       ├── sshconfig.Load()          — parse ~/.ssh/config + Includes
       ├── state.Load() / Save()     — favorites, recents (JSON)
       ├── credentials.Set/Get/Del() — macOS Keychain (darwin only)
       ├── tmuxrun.Session.*()       — new-window, split-v, split-h
       └── tmuxui.App.Run()          — Bubble Tea picker
            ├── sshconfig (host display + add-host modal)
            ├── state (favorites/recents via injected callbacks)
            └── tmuxrun (via injected callbacks)
```

Imports flow downward. Packages do not import each other laterally.

## Repo Map

| File | Purpose |
|---|---|
| `cmd/tmux-ssh-manager/main.go` | Process entrypoint — delegates to `app.Run()` |
| `pkg/app/app.go` | CLI routing (`list`, `connect`, `add`, `cred`, `--version`) and dependency wiring |
| `pkg/sshconfig/sshconfig.go` | OpenSSH config parser, Include resolution, append-only host writer |
| `pkg/tmuxui/ui.go` | Bubble Tea picker, add-host modal, credential modal, vim-style navigation |
| `pkg/tmuxrun/tmuxrun.go` | tmux command construction (`new-window`, `split-window`), socket-aware |
| `pkg/state/state.go` | Favorites and recents persistence (atomic JSON writes, XDG-aware) |
| `pkg/credentials/credentials.go` | Credential normalization helpers |
| `pkg/credentials/credentials_darwin.go` | macOS Keychain implementation via `security` CLI |
| `pkg/credentials/credentials_other.go` | Non-darwin stub (returns `ErrUnsupported`) |
| `scripts/harness.sh` | Canonical validation loop (fmt, test, build, smoke) |
| `tmux-ssh-manager.tmux` | tmux plugin entrypoint (key binding registration) |
| `scripts/tmux_ssh_manager.tmux` | tmux launcher script (popup or new-window) |

## Runtime Model

Two modes:

1. **CLI mode** — `list [--json]`, `connect [--split-count N] [--split-mode window|v|h] [--layout tiled]`, `add`, `cred`, `print-ssh-config-path`, `--version`
2. **Picker mode** — default (no subcommand), Bubble Tea alt-screen UI. Starts in search mode (input focused) by default. Use `--mode normal` (`-m normal`) to start with search blurred for vim-style navigation. With `--implicit-select` (on by default), `enter`/`v`/`s`/`w` act on the highlighted host directly from search mode. Disable with `--implicit-select=false` or `set -g @tmux_ssh_manager_implicit_select 'off'`. The `--enter-mode` flag (default `p`) controls what `enter` does: `p` (pane/connect in place), `w` (new window), `s` (horizontal split), `v` (vertical split). Configure via `set -g @tmux_ssh_manager_enter_mode 'p'`.

### Picker Keybindings

| Key | Action |
|---|---|
| `/` | Focus search |
| `j`/`k`, arrows | Move cursor |
| `ctrl+d`/`ctrl+u` | Half-page scroll |
| `gg`/`G` | Jump to top/bottom |
| `space` | Toggle multi-select |
| `enter` | Connect in place |
| `v` | Split vertical |
| `s` | Split horizontal |
| `w` | New tmux window |
| `t` | Tiled layout (multi-select) |
| `p` | Connect in current pane |
| `f` | Toggle favorite |
| `F` | Filter favorites |
| `R` | Filter recents |
| `ctrl+a` | Select all (filtered) |
| `a` | Add host to `~/.ssh/config` |
| `c` | Store credential (macOS) |
| `d` | Delete credential (macOS) |
| `q`/`esc` | Quit |

Credential actions suspend Bubble Tea and re-invoke the binary with `cred set|delete` so the Keychain prompt gets a real TTY.

## Credential Model

- Supported platform: macOS only (build tag `darwin`)
- Backend: `/usr/bin/security` (Keychain)
- Service naming: `tmux-ssh-manager:<host>:<kind>` — no collisions across hosts
- Default user: falls back to host alias if `--user` is omitted
- Kind normalization: `password` (default), `passphrase`, `otp`/`totp`

## Session Logging

All SSH connections automatically log session output via `tmux pipe-pane`.

- Log directory: `~/.config/tmux-ssh-manager/logs/<alias>/YYYY-MM-DD.log`
- Respects `$XDG_CONFIG_HOME` when set
- Alias is sanitized to filesystem-safe characters
- One log file per host per calendar day (local time), appended to across sessions
- tmux window/split connections: pipe-pane enabled on the new pane automatically
- Pane connects (in-place SSH): pipe-pane enabled on the current pane before exec
- Logging failures are non-fatal — they never block SSH connections
- Directories and files created with restrictive permissions (0700/0600)

## Change Workflow

1. Identify the owning package.
2. Keep the change inside that package unless there is a real interface boundary to cross.
3. Add or update tests when the behavior is parser-, state-, CLI-, or command-builder-related.
4. Run the full harness before finishing.

```sh
make all
# or: ./scripts/harness.sh all
```

This runs: `gofmt` → `go test ./...` → `go build` (with version injection) → smoke test with fixture SSH config.

For live SSH validation against a real host:

```sh
./scripts/harness.sh live                       # uses LIVE_HOST=k3d-staging
LIVE_HOST=k3d-production ./scripts/harness.sh live  # override target
```

The live test validates: host listing, `connect --dry-run`, SSH round-trip I/O, and multi-line output preservation.

## Package Quick Reference

| Need to change... | Work in... |
|---|---|
| Host parsing, Include resolution | `pkg/sshconfig` |
| Picker keybindings or UX | `pkg/tmuxui` |
| tmux command construction | `pkg/tmuxrun` |
| CLI flags or subcommands | `pkg/app` |
| Keychain credential behavior | `pkg/credentials` |
| Favorites/recents persistence | `pkg/state` |
| Build/test/validation | `scripts/harness.sh` |

## Design Constraints

- No YAML config files.
- No sidecar per-host metadata.
- No dashboard or topology abstractions.
- No secondary source of truth for hosts.
- Prefer append-only SSH config edits over in-place rewrites.
- Keep packages small with narrow interfaces.
- Do not grow `pkg/tmuxui` into a multi-purpose application.
- tmux pane commands use `$SHELL -lc` to respect the user's login shell.

## Useful Commands

```sh
go run ./cmd/tmux-ssh-manager
go run ./cmd/tmux-ssh-manager --mode normal
go run ./cmd/tmux-ssh-manager list
go run ./cmd/tmux-ssh-manager list --json
go run ./cmd/tmux-ssh-manager connect edge1
go run ./cmd/tmux-ssh-manager add --alias edge1 --hostname 10.0.0.10 --user matt
go run ./cmd/tmux-ssh-manager cred set --host edge1 --user matt
go run ./cmd/tmux-ssh-manager cred delete --host edge1 --user matt
./scripts/harness.sh all
```

## What To Read First

If you are new to the repo, read in this order:

1. `README.md`
2. `AGENTS.md`
3. `pkg/app/app.go`
4. the package that owns the behavior you need to change