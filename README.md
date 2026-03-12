# tmux-ssh-manager

A tmux plugin that reads literal `Host` aliases from `~/.ssh/config`, presents them in a Bubble Tea picker, and dispatches tmux actions.

- connect in place
- open selections in new tmux windows
- split selections into vertical or horizontal panes
- mark favorites and track recents
- append new host entries to `~/.ssh/config`
- manage macOS Keychain credentials via `cred set|get|delete`

The only host inventory is OpenSSH config. No YAML, no sidecar metadata.

## Architecture

- `pkg/sshconfig`: parse OpenSSH config files and append new `Host` blocks
- `pkg/state`: persist favorites and recents
- `pkg/credentials`: macOS Keychain storage for explicit `cred` commands
- `pkg/tmuxui`: Bubble Tea picker and action dispatch
- `pkg/tmuxrun`: tmux command construction and execution helpers
- `pkg/app`: top-level orchestration for listing, direct connect, add-host, credential commands, and picker launch

## Keybindings

Inside the picker:

- `/`: focus search
- `j` / `k`: move
- `ctrl+d` / `ctrl+u`: half-page down/up
- `gg` / `G`: jump to top/bottom
- `space`: toggle selection
- `enter`: connect current host in place
- `v`: open selected hosts as vertical splits
- `s`: open selected hosts as horizontal splits
- `w`: open selected hosts in new tmux windows
- `t`: open selected hosts in tiled layout (multi-select)
- `p`: connect highlighted host in current pane
- `c`: store a credential for the current host
- `d`: delete a credential for the current host
- `f`: toggle favorite on current host
- `F`: filter favorites
- `R`: filter recents
- `ctrl+a`: select all (filtered)
- `a`: add a host to `~/.ssh/config`
- `q` or `esc`: quit

## CLI

```sh
go run ./cmd/tmux-ssh-manager list
go run ./cmd/tmux-ssh-manager list --json
go run ./cmd/tmux-ssh-manager
go run ./cmd/tmux-ssh-manager connect my-host
go run ./cmd/tmux-ssh-manager connect my-host --split-count 4 --split-mode v --layout tiled
go run ./cmd/tmux-ssh-manager add --alias my-host --hostname 10.0.0.10 --user matt
go run ./cmd/tmux-ssh-manager cred set --host my-host --user matt
go run ./cmd/tmux-ssh-manager cred delete --host my-host --user matt
go run ./cmd/tmux-ssh-manager print-ssh-config-path
go run ./cmd/tmux-ssh-manager --version
```

The picker starts in search mode by default — the search input is focused so you can immediately type a hostname. Arrow keys navigate the host list while search remains focused. With **implicit select** (on by default), pressing `enter`, `v`, `s`, or `w` in search mode acts on the highlighted host without needing to toggle selection with space first.

To start in normal mode (vim-style navigation without search focused):

```sh
go run ./cmd/tmux-ssh-manager --mode normal
```

To disable implicit select (enter in search mode only blurs the search input):

```sh
go run ./cmd/tmux-ssh-manager --implicit-select=false
```

The `--enter-mode` flag controls what `enter` does. Default is `p` (connect in the current pane). Other values: `w` (new tmux window), `s` (horizontal split), `v` (vertical split):

```sh
go run ./cmd/tmux-ssh-manager --enter-mode p    # pane (default)
go run ./cmd/tmux-ssh-manager --enter-mode w    # new window
go run ./cmd/tmux-ssh-manager --enter-mode s    # horizontal split
go run ./cmd/tmux-ssh-manager --enter-mode v    # vertical split
```

When multi-selecting with `p` mode, multiple hosts open as new windows (can't connect multiple hosts in one pane).

The `connect` subcommand supports `--split-count`, `--split-mode`, and `--layout` for opening multiple panes to the same host:

```sh
go run ./cmd/tmux-ssh-manager connect my-host --split-count 4 --split-mode v --layout tiled
```

Supported layouts: `tiled`, `even-horizontal`, `even-vertical`, `main-horizontal`, `main-vertical`.

`cred` is intentionally macOS-only. On macOS it stores generic passwords in Keychain under host-specific service names so the same username can be stored separately for different hosts.

## Session Logging

All SSH connections automatically log session output via `tmux pipe-pane`:

- Logs: `~/.config/tmux-ssh-manager/logs/<alias>/YYYY-MM-DD.log`
- Respects `$XDG_CONFIG_HOME` when set
- One log file per host per day, appended across sessions
- Logging failures never block SSH connections

## Validation

Use `make` or call the harness directly:

```sh
make            # fmt + test + build + smoke
make test       # go test ./...
make build      # build with version injection
make smoke      # build + smoke test
make clean      # rm -rf bin/
```

Or the harness:

```sh
scripts/harness.sh fmt
scripts/harness.sh test
scripts/harness.sh build
scripts/harness.sh smoke
scripts/harness.sh all
```

For live SSH validation against a real host:

```sh
scripts/harness.sh live                             # default: LIVE_HOST=k3d-staging
LIVE_HOST=k3d-production scripts/harness.sh live    # override target
```

The smoke test creates a temporary home directory, writes a fixture SSH config, and validates parsing plus CLI listing without touching your real `~/.ssh/config`.

## Tmux Plugin

Recommended tmux config:

```tmux
set -g @tmux_ssh_manager_key 's'
set -g @tmux_ssh_manager_bin '~/path/to/tmux-ssh-manager/bin/tmux-ssh-manager'
set -g @tmux_ssh_manager_launch_mode 'popup'
# set -g @tmux_ssh_manager_mode 'normal'              # override default search mode
# set -g @tmux_ssh_manager_implicit_select 'off'       # disable implicit select
# set -g @tmux_ssh_manager_enter_mode 'p'              # enter action: p/w/s/v
run-shell '~/path/to/tmux-ssh-manager/tmux-ssh-manager.tmux'
```
