# tmux-ssh-manager

A tmux plugin that reads `Host` aliases from `~/.ssh/config` and presents them in an interactive picker for SSH session management.

## Features

- Connect to hosts in the current pane, new tmux windows, or vertical/horizontal splits
- Multi-select hosts for tiled layouts
- Mark favorites and track recently connected hosts
- Append new host entries to `~/.ssh/config`
- Automatic credential injection via macOS Keychain (`SSH_ASKPASS`)
- Transparent `ssh` and `scp` wrappers with credential passthrough
- Automatic session logging via `tmux pipe-pane`

The only host inventory is `~/.ssh/config` (including `Include` directives). No YAML or sidecar metadata.

## Install

### As a tmux plugin

Add to your `tmux.conf`:

```tmux
set -g @tmux_ssh_manager_key 's'
set -g @tmux_ssh_manager_launch_mode 'popup'
run-shell '~/path/to/tmux-ssh-manager/tmux-ssh-manager.tmux'
```

The binary auto-builds on first use and rebuilds when the git commit changes.

### tmux options

| Option | Default | Description |
|---|---|---|
| `@tmux_ssh_manager_key` | `s` | Key binding to open the picker |
| `@tmux_ssh_manager_bin` | `<repo>/bin/tmux-ssh-manager` | Path to binary (supports `~/` expansion) |
| `@tmux_ssh_manager_launch_mode` | `popup` | `popup` or `window` |
| `@tmux_ssh_manager_mode` | `search` | Picker start mode: `search` or `normal` |
| `@tmux_ssh_manager_implicit_select` | *(on)* | Set to `off` to require explicit selection |
| `@tmux_ssh_manager_enter_mode` | `p` | Enter key action: `p` (pane), `w` (window), `s` (split-h), `v` (split-v) |

### Shell aliases (optional)

To get automatic credential injection for all SSH and SCP usage:

```sh
alias ssh='tmux-ssh-manager ssh'
alias scp='tmux-ssh-manager scp'
```

These pass all arguments through to the real `ssh`/`scp` binary, injecting `SSH_ASKPASS` when a stored credential matches the destination host.

## Picker keybindings

The picker starts in **search mode** (input focused). Press `Esc` to switch to **normal mode** for vim-style navigation. Search terms are preserved when switching modes.

### Search mode

| Key | Action |
|---|---|
| *(typing)* | Filter hosts |
| `up` / `down` | Move cursor |
| `enter` | Connect to highlighted host (with implicit select) |
| `ctrl+a` | Select all filtered hosts |
| `esc` | Switch to normal mode (preserves filter) |

### Normal mode

| Key | Action |
|---|---|
| `/` | Focus search |
| `j`/`k`, `up`/`down` | Move cursor |
| `ctrl+d` / `ctrl+u` | Half-page scroll |
| `gg` / `G` | Jump to top / bottom |
| `space` | Toggle multi-select |
| `enter` | Connect (action depends on `--enter-mode`) |
| `v` | Vertical split |
| `s` | Horizontal split |
| `w` | New tmux window |
| `t` | Tiled layout (multi-select) |
| `p` | Connect in current pane |
| `f` | Toggle favorite |
| `F` | Filter to favorites |
| `R` | Filter to recents |
| `ctrl+a` | Select all filtered |
| `a` | Add host to `~/.ssh/config` |
| `c` | Store credential (macOS) |
| `d` | Delete credential (macOS) |
| `q` / `esc` | Quit |

## CLI

```sh
tmux-ssh-manager                    # open picker
tmux-ssh-manager list               # print host aliases
tmux-ssh-manager list --json        # print hosts as JSON
tmux-ssh-manager connect <alias>    # SSH to host
tmux-ssh-manager connect <alias> --split-count 4 --split-mode v --layout tiled
tmux-ssh-manager add --alias edge1 --hostname 10.0.0.10 --user matt
tmux-ssh-manager cred set --host edge1 [--user matt] [--kind password]
tmux-ssh-manager cred get --host edge1
tmux-ssh-manager cred delete --host edge1
tmux-ssh-manager ssh <args...>      # passthrough to ssh with credential injection
tmux-ssh-manager scp <args...>      # passthrough to scp with credential injection
tmux-ssh-manager print-ssh-config-path
tmux-ssh-manager --version
```

### Picker flags

| Flag | Default | Description |
|---|---|---|
| `--mode` / `-m` | `search` | Start mode: `search` or `normal` |
| `--implicit-select` | `true` | `enter` acts on highlighted host in search mode |
| `--enter-mode` | `p` | Enter key action: `p`, `w`, `s`, `v` |

### Connect flags

| Flag | Default | Description |
|---|---|---|
| `--dry-run` | `false` | Print the SSH command instead of executing |
| `--split-count` | `0` | Open N connections (>1 creates splits/windows) |
| `--split-mode` | `window` | With split-count: `window`, `v`, `h` |
| `--layout` | | tmux layout: `tiled`, `even-horizontal`, `even-vertical`, `main-horizontal`, `main-vertical` |

## Credentials (macOS)

Credentials are stored in macOS Keychain under service names `tmux-ssh-manager:<host>:<kind>`.

When a credential exists for a host, all SSH connections (picker, `connect`, `ssh` passthrough) automatically inject it via `SSH_ASKPASS`. No manual password entry needed.

Supported kinds: `password` (default), `passphrase`, `otp`/`totp`.

## Session logging

SSH connections log output via `tmux pipe-pane`:

- Path: `~/.config/tmux-ssh-manager/logs/<alias>/YYYY-MM-DD.log`
- Respects `$XDG_CONFIG_HOME`
- One log per host per day, appended across sessions
- Restrictive permissions (dirs 0700, files 0600)
- Failures never block connections

## Development

```sh
make            # fmt + test + build + smoke
make test       # go test ./...
make build      # build with version injection
make smoke      # build + fixture SSH config test
make clean      # rm -rf bin/
```

Live SSH validation:

```sh
scripts/harness.sh live                             # default: LIVE_HOST=k3d-staging
LIVE_HOST=k3d-production scripts/harness.sh live    # override target
```
