# tmux-ssh-manager

Recorded demos live in `docs/demos/` (with regeneration instructions in `docs/README.md`).

## Registers (host-scoped, vim-like)

Registers are named command lists you can paste into the active host’s pane without automatically pressing Enter. Registers are **resolved per host** (so different hosts can have different registers).

### Usage (host-scoped)
- Open registers for the active host pane:
  - `Ctrl-r`
  - or command bar: `:r` / `:registers`
- Navigate: `j/k`
- Paste selection (does not press Enter): `Enter`
- Close: `Esc` / `q`

### YAML config (host-scoped)

Define registers on a host (or group) so they are **not global**:

```yaml
hosts:
  - name: rtr1
    registers:
      - name: health
        description: "Quick checks"
        commands:
          - terminal length 0
          - show version
          - show clock
```

Optionally attach register names to a dashboard for discoverability.

Notes:
- Dashboard `registers` are still **host-scoped**.
- A register name listed on a dashboard must exist on the target pane host (from either the host’s `registers` or its group’s `registers`).

```yaml
dashboards:
  - name: core-status
    registers: [health]
    panes:
      - host: rtr1
        commands:
          - show ip interface brief
```

## Interop: dashboards → tmux-session-manager `.tmux-session.yaml`

tmux-ssh-manager can export a resolved dashboard into a tmux-session-manager spec file and (optionally) apply it via `tmux-session-manager --spec`.

### What this is for
- Turn a dashboard (multi-pane SSH view) into a portable, reproducible session spec (`.tmux-session.yaml` / `.json`).
- Hand off “session materialization” to tmux-session-manager while keeping tmux-ssh-manager as the source of dashboard intent (hosts + commands).

### How it works
When enabled, tmux-ssh-manager:
1) Resolves the dashboard (hosts + effective command lists per pane).
2) Writes a spec file under your tmux-ssh-manager config directory (by default).
3) Optionally opens a new tmux window and runs:
   `tmux-session-manager --spec "<exported spec path>"`

### Enable (environment variables)
Set these (in the environment used to launch tmux-ssh-manager):

- Enable integration:
  - `TMUX_SSH_MANAGER_USE_SESSION_MANAGER=1`

- Output format (optional; default: yaml):
  - `TMUX_SSH_MANAGER_DASH_SPEC_FORMAT=yaml|json`

- Output directory (optional; default: `~/.config/tmux-ssh-manager/dashboards`):
  - `TMUX_SSH_MANAGER_DASH_SPEC_DIR=/path`

- Deterministic splits (optional; default: on):
  - `TMUX_SSH_MANAGER_DASH_DETERMINISTIC_SPLITS=0` to disable `pane_plan`

- Apply after export (optional; default: on):
  - `TMUX_SSH_MANAGER_DASH_APPLY=0` to export only (do not invoke tmux-session-manager)

- tmux-session-manager binary path (optional; default: `tmux-session-manager`):
  - `TMUX_SESSION_MANAGER_BIN=/path/to/tmux-session-manager`

### Output
The exported spec is written as:
- `~/.config/tmux-ssh-manager/dashboards/<dashboard>.tmux-session.yaml` (or `.json`)

The exported spec:
- uses a single window (default name: `dashboard`)
- uses `pane_plan` when deterministic splits are enabled
- encodes each pane as “connect + send commands” using safe actions (no shell/tmux passthrough is intended by default)

### Launching tmux-session-manager manually
You can apply an exported dashboard spec directly:

```sh
tmux-session-manager --spec ~/.config/tmux-ssh-manager/dashboards/<dashboard>.tmux-session.yaml
```

## Linux: credential storage (Secret Service + headless fallback)

On macOS, `tmux-ssh-manager` can store credentials in Keychain.

On Linux, `tmux-ssh-manager` can store credentials via the Secret Service API (GNOME Keyring / compatible) by shelling out to `secret-tool`.

### Install `secret-tool` (desktop Linux)

Debian/Ubuntu:

```sh
sudo apt-get update
sudo apt-get install -y libsecret-tools
```

Fedora:

```sh
sudo dnf install -y libsecret
```

Arch:

```sh
sudo pacman -S --needed libsecret
```

### Notes (Linux desktop)

- You need a running Secret Service provider (commonly GNOME Keyring). This is usually present in a logged-in desktop session.
- If `secret-tool` is installed but no Secret Service provider is running, you may see errors like:
  `The name org.freedesktop.secrets was not provided by any .service files`

### Headless Linux (no GUI session): recommended GPG fallback

On headless servers (SSH + tmux, no GUI session), Secret Service is often unavailable even if `secret-tool` is installed. In that environment, use the GPG-based credential fallback.

1) Install GPG:

Debian/Ubuntu:

```sh
sudo apt-get update
sudo apt-get install -y gpg
```

2) Configure GPG symmetric mode using a passphrase file (file-only)

Symmetric mode requires a passphrase. In headless tmux environments, relying on `gpg-agent` / `pinentry` prompts can fail (TTY issues). Instead, configure a passphrase file and use loopback mode.

Create a passphrase file (permissions must be restrictive):

```sh
sudo mkdir -p /root/.config/tmux-ssh-manager
sudo sh -lc 'umask 077; printf "%s\n" "YOUR_STRONG_PASSPHRASE" > /root/.config/tmux-ssh-manager/gpg_passphrase'
sudo chmod 600 /root/.config/tmux-ssh-manager/gpg_passphrase
```

Enable symmetric fallback + point at the passphrase file:

```sh
export TMUX_SSH_MANAGER_GPG_SYMMETRIC=1
export TMUX_SSH_MANAGER_GPG_PASSPHRASE_FILE="/root/.config/tmux-ssh-manager/gpg_passphrase"
```

With this configuration, `tmux-ssh-manager` will:
- use Secret Service when available (desktop)
- automatically fall back to the GPG credential store when Secret Service is unavailable (headless)

### Optional: configure via tmux options (recommended for keybinding launches)

If you launch `tmux-ssh-manager` from a tmux keybinding, exporting env vars in your shell may not propagate. Configure via tmux options instead:

```sh
set -g @tmux_ssh_manager_gpg_symmetric 1
set -g @tmux_ssh_manager_gpg_passphrase_file "/root/.config/tmux-ssh-manager/gpg_passphrase"
```

### Optional: force backend selection (Linux)

You can override automatic selection:

```sh
export TMUX_SSH_MANAGER_CRED_BACKEND=auto          # default
export TMUX_SSH_MANAGER_CRED_BACKEND=secretservice # require secret-tool/provider
export TMUX_SSH_MANAGER_CRED_BACKEND=gpg           # require gpg configuration
export TMUX_SSH_MANAGER_CRED_BACKEND=none          # disable credential store
```

## Shell aliases (ssh + scp wrappers)

If you already alias `ssh` to the wrapper, you can also alias `scp` so file transfers use the same host matching and (when enabled) Keychain-backed credential flow.

Example for `~/.zshrc`:

```sh
alias ssh='tmux-ssh-manager ssh'
alias scp='tmux-ssh-manager scp'
```

Notes:

- `tmux-ssh-manager scp ...` is intended to behave like system `scp` with the same arguments.
- When a host is configured for Keychain-backed auth (`login_mode=askpass` / `auth_mode=keychain`) and a credential is present, the wrapper will use an askpass flow for `scp` as well.
- For best results, use your usual SSH aliases/hosts from `~/.ssh/config` (or YAML host names), e.g.:

```sh
scp file.txt leaf01.lab.local:/tmp/
scp leaf01.lab.local:/var/log/system.log .
```

## Per-host daily logs + in-TUI log viewer

`tmux-ssh-manager` can maintain per-host, per-day log files under your config directory and lets you browse them inside the Bubble Tea TUI with vim-style navigation.

### Log
### Log file location and naming

By default, logs are stored under:

- `~/.config/tmux-ssh-manager/logs/<hostkey>/YYYY-MM-DD.log`

Where:
- `<hostkey>` is typically the SSH alias/hostname you select in the TUI (sanitized for filesystem safety).
- `YYYY-MM-DD` is the local calendar day (e.g. `2025-12-18.log`).
- A new file is created each day and appended to for that day.

If `XDG_CONFIG_HOME` is set, logs are stored under:

- `$XDG_CONFIG_HOME/tmux-ssh-manager/logs/<hostkey>/YYYY-MM-DD.log`

### Viewing logs in the TUI

From the main selector, select a host and press:

- `L` to open the logs viewer for that host.

In the logs viewer:

- `q` or `Esc` : close logs view
- `j/k`        : scroll down/up one line
- `d/u`        : half-page down/up
- `gg`         : jump to top
- `G`          : jump to bottom (best-effort)
- `J`          : next log file (older day)
- `K`          : previous log file (newer day)
- `r`          : reload the current view

### Viewing logs on disk

Logs are plain text files with a `.log` suffix and can be tailed/grepped normally, e.g.:

- `tail -f ~/.config/tmux-ssh-manager/logs/<hostkey>/$(date +%F).log`


## iTerm2 integration (Password Manager + triggers)

If you use iTerm2, you can lean on its native **Password Manager** (Keychain-backed) and **Triggers** to speed up password entry without having `tmux-ssh-manager` ever handling secrets.

### Recommended approach (iTerm2-native, safest)

1) **Store credentials in iTerm2**
- Open **Window → Password Manager**.
- Save passwords for your common SSH targets.

2) **Create an iTerm2 Trigger to open Password Manager on prompts**
- Go to **Settings → Profiles → Advanced → Triggers → Edit…**
- Add triggers that match common password prompts and set the action to:
  - **Open Password Manager**
  - (Optionally) select the relevant account by default.

Suggested prompt regex patterns (start here and adjust to your environment):

```/dev/null/iterm2-triggers.txt#L1-20
# Generic password prompts
(?i)password:\s*$
(?i)passphrase\s+for\s+key.*:\s*$
(?i)enter\s+passphrase.*:\s*$

# Network-device style prompts (examples)
(?i)username:\s*$
(?i)login:\s*$
(?i)passcode:\s*$
(?i)verification\s+code:\s*$
```

Notes:
- Use `(?i)` for case-insensitive matches.
- Prefer `$`-anchored patterns to reduce false positives.
- Consider enabling "Instant" for prompts that don’t end with a newline.

### If you use tmux

iTerm2 notes that shell integration/triggers may need an opt-in to work well with tmux:
- Set this in your shell before iTerm2 shell integration is sourced:
  - `export ITERM_ENABLE_SHELL_INTEGRATION_WITH_TMUX=1`

(See iTerm2 docs: "Limitations" under Shell Integration.)

### Security notes

- iTerm2’s Password Manager stores secrets in **macOS Keychain**.
- `tmux-ssh-manager` can integrate with iTerm2 triggers, but **does not** programmatically extract secrets from iTerm2’s Password Manager.

## Dashboards (named multi-pane views)

Dashboards let you define reusable, multi-pane tmux views with commands that run in each pane, so you can quickly inspect device health, interfaces, routes, CPU/memory, etc. They show up alongside hosts/groups in the Bubble Tea selector and can be materialized with one key.

### Optional: export dashboards to tmux-session-manager session specs (YAML/JSON)

Dashboards and recorded dashboards can be **exported** into the `tmux-session-manager` project-local session spec format (the `.tmux-session.yaml` / `.tmux-session.json` schema).

Why this is useful:
- A dashboard becomes a portable "session plan" that can be saved, versioned, and replayed.
- You get deterministic split geometry via `pane_plan` and a consistent, diff-friendly YAML/JSON artifact.
- This is not a hard dependency: ssh-manager continues to support its native dashboard materialization.

What gets exported:
- A `pane_plan` that reproduces pane splits deterministically (when possible).
- Pane "actions" that:
  - SSH into the target host
  - then run the effective command list (host/group `on_connect` + dashboard pane commands)
- The dashboard window layout (`layout` / `#{window_layout}`) when captured by `:dash save ...` can be preserved and applied as part of the exported spec.

Current status (important):
- Export to spec is supported and best-effort.
- Automatic "apply via tmux-session-manager" is not enabled by default and depends on tmux-session-manager supporting a direct `--spec <path>` (or equivalent) apply path for non-project-local specs.

How it can be used (conceptual):
- `:dash save <name> [description...]` still saves to `state.json`, but can additionally write:
  - `~/.config/tmux-ssh-manager/dashboards/<name>.tmux-session.yaml` (or `.json`)
- A future enhancement can add:
  - `:dash export <name> [yaml|json]` (explicit export command)
  - `:dash apply <name>` (apply exported spec via tmux-session-manager when supported)

Safety model:
- Export defaults to "safe / declarative" actions where possible.
- If an exported spec contains `shell` or `actions.tmux`, those should remain opt-in behind explicit enablement (consistent with tmux-session-manager policy gates).

Notes:
- Dashboards are about panes and commands; tmux sessions are a natural container for that. The export feature bridges the two without forcing you to change how you author dashboards today.

## Credential storage (macOS Keychain) and login_mode=askpass (macOS-only)

This project supports a more "formal" password-manager workflow on macOS using **System Keychain** (via the built-in `security` tool), without putting secrets into tmux buffers or your clipboard.

## SSH key bootstrap (Linux): install `~/.ssh/id_rsa.pub` once, then prefer `IdentityFile`

If your targets are Linux hosts with `~/.ssh/authorized_keys`, you can use a simple workflow:

1) Connect via `login_mode=askpass` once (password comes from Keychain).
2) Install your local public key into the remote user’s `~/.ssh/authorized_keys` (idempotent).
3) On subsequent connects, use key-based auth (faster, no password prompt).

### Defaults and overrides (per-host extras)

Per-host extras live at:

- `~/.config/tmux-ssh-manager/hosts/<hostkey>.conf`

Supported keys:

- `key_install=enabled|disabled`
- `key_install_pubkey=...`
  - Default: `~/.ssh/id_rsa.pub` (Linux-oriented baseline)
  - Override if you want a different key per host
- `identity_file=...`
  - Optional override for the SSH private key used when connecting (adds `ssh -i <identity_file>`)
  - Example: `identity_file=~/.ssh/id_rsa`

Example:

```conf
auth_mode=keychain
key_install=enabled
key_install_pubkey=~/.ssh/id_rsa.pub
identity_file=~/.ssh/id_rsa
authorized_keys_mode=ensure
```

### How to use (TUI)

- Press `K` on a host to launch "Install my public key" (runs in a new tmux window).
- Once installed successfully, `ssh` should no longer prompt for a password on that host (assuming the remote sshd permits pubkey auth).

### Concepts

- **Keychain credential**: a password stored under the service name `tmux-ssh-manager`, keyed by the host alias / configured host name (e.g. `leaf01.lab.local`).
- **login_mode**:
  - `manual` (default): no programmatic password handling (you type it yourself / use external tooling).
  - `askpass` (macOS-only): launches `ssh` with an `SSH_ASKPASS` helper that retrieves the password from Keychain **in-memory** and supplies it to `ssh` when prompted.

Important notes:

- This is **password-auth only** (for now).
- This is designed to work inside tmux, where iTerm2 triggers typically can’t "see" password prompts reliably.
- The askpass flow is **best-effort**; ssh/askpass invocation can vary by environment and auth method.
- Secrets are not copied to clipboard or tmux buffers by this feature.

### Keychain-backed credential commands (macOS)

These commands manage credentials in macOS Keychain under the service name `tmux-ssh-manager`.

Commands:
- `tmux-ssh-manager cred set --host <alias>`
- `tmux-ssh-manager cred get --host <alias>`
- `tmux-ssh-manager cred delete --host <alias>`

Notes:
- `cred get` is intentionally non-revealing: it verifies presence/access but does **not** print the secret.
- Use the same `--host` key you type after `ssh` (SSH alias), e.g. `leaf01.lab.local`.

### Enable askpass login_mode per host (YAML)

Set `login_mode: askpass` on hosts where you want Keychain-backed password auth:

```yaml
hosts:
  - name: leaf01.lab.local
    user: admin
    login_mode: askpass
```

Then store the password:

```sh
tmux-ssh-manager cred set --host leaf01.lab.local
```

When you connect via the TUI using tmux actions (new window / split / dashboards / :run), `ssh` will use an internal askpass helper to fetch the password from Keychain when required.

### Security model for askpass

- Password is retrieved from Keychain only when needed.
- Password is not displayed in the UI.
- Password is not written to tmux buffers or the system clipboard.
- Password is not meant to be logged; avoid adding debug logging around credential operations.

If you’re using per-host daily logs, be aware that anything printed by remote shells after login can still be logged depending on your logging settings; the password itself should not be printed by this workflow.

`tmux-ssh-manager` includes credential helpers that store secrets in macOS Keychain (service name: `tmux-ssh-manager`). This is separate from iTerm2’s Password Manager, but uses the same underlying OS store.

Commands:
- `tmux-ssh-manager cred set --host <alias>`
- `tmux-ssh-manager cred get --host <alias>`
- `tmux-ssh-manager cred delete --host <alias>`

Notes:
- `cred get` is intentionally non-revealing: it verifies presence/access but does **not** print the secret.
- You choose your `--host` key. A good convention is to use the same value you type after `ssh` (SSH alias), e.g. `leaf01.lab.local`.

### Auto-inject warnings (read this first)

Password auto-inject (typing a password into an interactive prompt, or using askpass) is **security-sensitive**:

- It can leak secrets via screen capture, terminal logs, scrollback, or accidental paste.
- It can break in subtle ways with keyboard-interactive/MFA flows.
- It can cause lockouts if it retries incorrectly.
- Prefer SSH keys + agent where possible.

If you enable/implement auto-inject, treat it as opt-in, default-off, and ensure secrets are never logged.

- Where to define:
  - In your YAML config under the top-level `dashboards` key.

- Schema:
  - `name`: unique name for the dashboard
  - `description`: optional text shown in the selector
  - `new_window`: boolean; if true, the dashboard opens in a new tmux window
  - `layout`: tmux layout (e.g. `main-vertical`, `even-horizontal`, or custom)
  - `connect_delay_ms`: optional integer delay (milliseconds) to wait after starting SSH in each pane before sending `on_connect` + pane `commands`. Useful for slow logins and for reliably starting remote `watch` loops. (Default: 500ms if unset.)
  - `panes`: list of pane definitions
    - Pane fields:
      - `title`: optional pane title (for display in previews, and future pane titles)
      - `host`: explicit host name or SSH alias (from `hosts` or your SSH config)
      - `filter`: criteria to select a host if `host` is not set
        - `group`: only hosts in this group
        - `tags`: list of tags; all must match
        - `name_contains`: substring match on host name
        - `name_regex`: RE2-compatible regex match on host name
      - `connect_delay_ms`: optional integer delay (milliseconds) to override the dashboard-level `connect_delay_ms` for just this pane.
      - `commands`: list of strings to send to the pane after connect (after host/group `on_connect`)
      - `env`: optional env variables to export in the pane before commands (future hook)

### connect_delay_ms (uniform behavior across dashboards, splits/windows, macros, and send)

`connect_delay_ms` is a small "settle time" (in milliseconds) inserted after SSH starts and before tmux-ssh-manager begins sending remote commands via `tmux send-keys`. This improves reliability when you’re starting long-running UI commands (like `watch`) right after login.

Where it applies:

- **Dashboards:** before sending `on_connect` + pane `commands` in each dashboard pane (dashboard-level + per-pane override).
- **Splits/windows (v/s/w/W):** before sending `on_connect` for that host.
- **Macros (`:run <macro>`):** before sending the macro commands into the newly-opened pane/window.
- **Send workflows:** if you use `:send` / `:sendall` after connecting, you typically don’t need additional delays (the session is already established), but dashboards/macros/on_connect benefit most from `connect_delay_ms`.

Defaults:

- If not set, the runtime default is **500ms**.

Configuration levels:

- `group.connect_delay_ms` (default for hosts in that group)
- `host.connect_delay_ms` (overrides group)
- `dashboard.connect_delay_ms` (default for panes in that dashboard)
- `dashboard.panes[].connect_delay_ms` (overrides dashboard for that pane)

- Example:
  ```
  dashboards:
    - name: core-status
      description: Quick core status across DC1
      new_window: true
      layout: main-vertical
      connect_delay_ms: 500
      panes:
        - title: "RTR1 IF Brief"
          host: rtr1.dc1.example.com
          commands:
            - terminal length 0
            - show ip interface brief
        - title: "RTR2 Routes"
          filter:
            group: dc1
            name_contains: "rtr2"
          connect_delay_ms: 1000
          commands:
            - terminal length 0
            - show ip route summary
  ```

- How to use:
  - In the Bubble Tea selector, press `B` to open the Dashboards browser.
  - Navigate with `j/k`, press `Enter` to materialize a dashboard.
  - If `new_window` is true, a new tmux window is created; otherwise panes are added to the current window.
  - The manager will:
    - Create the required panes
    - Apply the layout if provided
    - Start SSH in each pane using the target host (via YAML or SSH config)
    - Wait `connect_delay_ms` (default 500ms if unset) so the remote shell can settle
    - Send `on_connect` commands (group+host), then pane-specific `commands`

## Recording and replay (save ad-hoc dashboards)

There are two complementary workflows:

1) **Send + record (recommended)**
   - Use `:send` / `:sendall` to dispatch commands into the connected panes.
   - These commands are recorded reliably because the manager is the one sending them (no attempt to intercept raw keystrokes inside SSH).

2) **Snapshot a tmux window into a dashboard**
   - After you’ve opened panes, resized them, and sent the commands you want (typically ending in a long-running `watch ...`), run `:dash save <name> [description...]`.
   - This captures:
     - the set of panes in the current tmux window that were created by tmux-ssh-manager (and have a known host mapping)
     - the commands recorded for those panes (from `:send`, `:sendall`, `on_connect`, dashboards, and macros)
     - the current tmux `#{window_layout}` so resized panes can be replayed

### Storage
- Saved in persistent state at `~/.config/tmux-ssh-manager/state.json` under `recorded_dashboards`.
## Recording and replay (save ad-hoc dashboards)

This project supports a "record what the manager sent" workflow so you can reliably replay multi-pane tmux windows without trying to intercept arbitrary keystrokes inside SSH.

What a recording captures (best-effort):
- The set of tmux panes in the current tmux window that were created by tmux-ssh-manager (and have a known host mapping).
- The commands that tmux-ssh-manager sent into those panes (from `:send`, `:sendall`, `:watch`, `:watchall`, dashboards, and `on_connect`).
- The current tmux window layout string (`#{window_layout}`) so resized panes can be replayed.

### Watch helpers (repeat a command without typing `watch ...` yourself)

These are sugar over `:send` / `:sendall` that wrap your command in:

- `watch -n <interval_s> -t -- <cmd>`

Commands:
- `:watch [interval_s] <cmd...>`
  - Sends to the current tmux pane (or last created pane), and records it.
  - `interval_s` is optional; defaults to 2.
- `:watchall [interval_s] <cmd...>`
  - Sends to all panes tracked by tmux-ssh-manager (created via splits/windows/dashboards), and records it.
  - `interval_s` is optional; defaults to 2.

Examples (run inside the tmux-ssh-manager command line):
- `:watch 2 show version`
- `:watchall 5 show clock`

### Step-by-step walkthrough: 2 hosts → split → watch → save → rehydrate

1) **Select two hosts**
- In the TUI list, toggle-select host A and host B (multi-select).

2) **Open the split and connect**
- Press `v` (vertical split / side-by-side) or `s` (horizontal split / stacked) to connect.
- You should now have a single tmux window with two SSH panes.

3) **Start repeating commands**
- Focus pane A, then run:
  - `:watch 2 <your command>`
- Then focus pane B and run:
  - `:watch 2 <your command>`
- Or, if both panes are tracked by tmux-ssh-manager, you can do:
  - `:watchall 2 <your command>`

4) **Save the window as a recorded dashboard**
- Named save:
  - `:dash save <name> [description...]`
  - Example: `:dash save noc-two-hosts "two panes w/ watch"`
- Quick save (auto-generated name):
  - `Ctrl+s`

### Export to tmux-session-manager session specs (YAML/JSON) and rehydrate

Recorded dashboards can optionally be exported into the `tmux-session-manager` project-local session spec format (the `.tmux-session.yaml` / `.tmux-session.json` schema).

Enable export on `:dash save ...` by setting:
- `TMUX_SSH_MANAGER_EXPORT_DASHBOARD_SPEC=1`

Optional knobs:
- `TMUX_SSH_MANAGER_DASH_SPEC_FORMAT=yaml|json` (default: yaml)
- `TMUX_SSH_MANAGER_DASH_SPEC_DIR=/path` (default: `~/.config/tmux-ssh-manager/dashboards`)
- `TMUX_SSH_MANAGER_DASH_DETERMINISTIC_SPLITS=0` (set to 0 to disable `pane_plan` output)

Rehydrate options:
- **From tmux-ssh-manager:** open Dashboards browser (`B`) and materialize the recorded dashboard.
- **From tmux-session-manager:** apply the exported spec (see tmux-session-manager docs for `--spec` and/or project-root discovery workflows).

### Managing recordings
- TUI:
  - `:dash save <name> [description...]` — snapshot current tmux window into a recorded dashboard
  - `:record delete <name>` — delete a recorded dashboard
- Or by editing `state.json` directly (the manager keeps them deduplicated by name).

### NOC-style workflow (step-by-step)
1. Open your target hosts into splits/windows (e.g. multi-select + `v/s/w/W`, or materialize an existing dashboard with `B`).
2. Resize panes using tmux (e.g. `prefix` + arrow keys, or `resize-pane ...`) until the layout looks right.
3. Start "live" commands via the manager so they are recorded:
   - `:send watch -n 2 -t -- <your command>`
   - `:sendall watch -n 5 -t -- <your command>`
4. Save the whole window as a dashboard:
   - `:dash save noc-wallboard "NOC watch layout"`
5. Exit the selector. Later, reopen the dashboard from `B` (Dashboards browser) and it will reconnect and restore the layout.

## Pre-connect and post-connect hooks

In addition to `on_connect` (commands sent to the remote device), you can define local shell hooks to run before and after connection. These are executed on your local machine (not on the remote), useful for logging, archiving outputs, setting up environment, etc.

- Where to define:
  - Group-level:
    - `pre_connect`: list of local shell commands to run before connecting
    - `post_connect`: list of local shell commands to run after connecting
  - Host-level:
    - `pre_connect`: overrides/extends group-level
    - `post_connect`: overrides/extends group-level

- Execution order:
  - Pre-connect: group `pre_connect` then host `pre_connect`
  - Connect to host (pane/window/new window)
  - On-connect (remote) commands: group `on_connect` then host `on_connect`
  - Pane-specific dashboard `commands` (remote)
  - Post-connect: group `post_connect` then host `post_connect`

- Example:
  ```
  groups:
    - name: dc1
      pre_connect:
        - echo "Starting dc1 session" >> ~/tmux-ssh-manager.log
      post_connect:
        - echo "dc1 session connected" >> ~/tmux-ssh-manager.log
      on_connect:
        - terminal length 0

  hosts:
    - name: rtr1.dc1.example.com
      group: dc1
      pre_connect:
        - export SESSION_TAG=core1
      on_connect:
        - show ip interface brief
      post_connect:
        - echo "RTR1 connected with tag=$SESSION_TAG" >> ~/tmux-ssh-manager.log
  ```

- Notes:
  - Local hooks run via `bash -lc` to honor your shell environment.
  - `on_connect` and dashboard pane `commands` are sent to the remote session in the tmux pane/window created by the manager.
  - If you need pacing, sleeps, or prompt detection, you can use `connect_delay_ms` (see below).


## On-connect commands (macros) for network engineers

This project is designed to replace "SSH session managers" for network engineers by combining:

- Fast host selection (search, favorites/recents, groups)
- Repeatable automation (macros / on-connect commands)
- tmux ergonomics (splits, windows, dashboards)
- Per-host daily logs + in-TUI log viewer

There are two related concepts:

1) **on_connect** (already supported):
   - Defined at the **group** and **host** level (`on_connect:`).
   - Runs automatically after connecting when you use tmux actions (splits/windows/dashboards).

2) **macros** (named command lists):
   - Define reusable workflows (e.g., "prep terminal", "show health", "show interfaces").
   - Invoked from the TUI command bar with `:run <macro>` (see "Command bar" below).

### Macros (YAML)

Add a `macros:` section to your YAML config:

```yaml
macros:
  - name: prep-ios
    description: "Disable paging + set common terminal prefs"
    commands:
      - terminal length 0
      - terminal width 0

  - name: health
    description: "Quick health snapshot"
    commands:
      - show version
      - show processes cpu | ex 0.00
      - show memory statistics

  - name: ifbrief
    description: "Interfaces overview"
    commands:
      - terminal length 0
      - show ip interface brief
```

Notes:
- Macro names must be unique.
- Each macro must define at least one command.

### Command bar (SecureCRT-like)

The Bubble Tea TUI supports a vim-style command bar:

- Press `:` to open the command bar.
- Type commands and press Enter.
- Press `Tab` for simple completion suggestions.
- Use `:menu` to see a concise command summary.

Common commands:

- `:menu` — show a quick summary of available commands
- `:help` / `:h` — show help
- `:search <query>` (or `:/ <query>`) — set search query (forward)
- `:? <query>` — set search query and set reverse direction for `n/N`
- `:fav` / `:favorites` — toggle favorites filter
- `:recents` — toggle recents filter
- `:all` — clear filters
- `:dash` — open dashboards browser
- `:dash <name>` — open dashboards browser and preselect a matching dashboard by name
- `:connect` — connect (acts on selection if multi-select is active)
- `:split v` / `:split h` — split + connect (acts on selection if multi-select is active)
- `:window` — new window + connect (acts on selection if multi-select is active)
- `:windows` — show a hint for tmux-native window switching
- `:logs` — open logs viewer for the current host
- `:log toggle` / `:log on` / `:log off` — toggle per-host logging policy
- `:run <macro>` — invoke a macro (see notes below)

Search ergonomics (vim-like):
- `/` focuses search and sets "forward" direction for `n`
- `?` focuses search and sets "backward" direction for `n`
- `n` / `N` move next/previous within the filtered results

tmux window switching:
- Once you’re inside SSH, keystrokes go to the remote host; rely on tmux:
  - `prefix + w` to choose a window
  - `prefix + n` / `prefix + p` to move next/previous window

### Macro invocation notes

- `:run <macro>` is intended to create the relevant tmux targets (window/split) and send the macro commands.
- If you want guaranteed "send commands after connect" automation today, prefer `on_connect` and dashboards.
- Macros are best used for repeatable workflows that you want to invoke on demand across one or more hosts.

You can define commands to run automatically after the SSH session connects. These are sent to the remote device via tmux send-keys into the pane/window created by the session manager.

- Where to define:
  - Group-level `on_connect`: applies to all hosts in the group
  - Host-level `on_connect`: appended after group commands

- How they run:
  - When you launch a connection in a tmux split or new window (v, s, w, W), the manager sends each command followed by Enter into that pane.
  - Host-level commands run after group-level commands.
  - Classic TUI direct-in-pane connects do not support on_connect (use splits/windows/dashboards for automation).

- Example YAML (inline):
```/dev/null/hosts.yaml#L1-24
groups:
  - name: dc1
    default_user: netops
    default_port: 22
    jump_host: bastion.dc1.example.com
    on_connect:
      - terminal length 0
      - show version | include Serial

hosts:
  - name: rtr1.dc1.example.com
    group: dc1
    user: admin
    tags: [router, ios-xe]
    on_connect:
      - show ip interface brief
      - show arp | include Vlan
```

- Notes:
  - Commands are raw strings sent as if you typed them. Use device syntax appropriate for IOS-XE, NX-OS, EOS, PAN-OS, etc.
  - If you need per-device delays or pacing, we can add configurable sleeps in a future iteration.

## Configuration reference (YAML) — on_connect

In addition to the previously documented keys, both `groups` and `hosts` support:

- `on_connect`: list of strings
  - Group-level commands run first.
  - Host-level commands are appended after group commands.
  - Commands are sent to the remote shell in the pane created by the manager.

## Persistent state (favorites/recents)

Favorites and recents are saved between runs so you can pick up where you left off.

- Location:
  - `~/.config/tmux-ssh-manager/state.json`
  - Or `$XDG_CONFIG_HOME/tmux-ssh-manager/state.json` if `XDG_CONFIG_HOME` is set
- What’s stored:
  - `favorites`: list of host names you’ve starred (via `f`)
  - `recents`: MRU list of hosts you’ve connected to (updated on Enter/c, v, s, w/W, and batch actions)
  - `version` and `updated` metadata
- Behavior:
  - Recents are kept most-recent-first and are capped (default 100)
  - File and directory are created as needed; you can safely delete the file to reset state
- Example:
```/dev/null/state.json#L1-12
{
  "version": 1,
  "favorites": ["rtr1.dc1.example.com", "leaf01.lab.local"],
  "recents": ["leaf01.lab.local", "rtr1.dc1.example.com", "fw1.dc1.example.com"],
  "updated": "2025-01-01T12:34:56Z"
}
```

## Theming (optional colors)

You can enable simple ANSI color theming with a JSON file or an environment variable.

- Config file:
  - `~/.config/tmux-ssh-manager/theme.json`
  - Or `$XDG_CONFIG_HOME/tmux-ssh-manager/theme.json`
- Environment override:
  - `TMUX_SSH_MANAGER_THEME=none|dark|light|catppuccin|catppuccin-mocha|mocha`
  - If unset, a sensible dark theme is chosen when color is supported
- JSON keys (all optional):
  - `enabled`: true/false
  - `name`: base palette name (`dark`, `light`, `catppuccin-mocha`, or `none`)
  - `colors`: map of logical roles to styles:
    - header, accent, selected, favorite, checkbox, group, dim, separator, help, error, success, warn
  - Style values can be:
    - Basic names: `bold`, `faint`, `red`, `blue`, `gray`, etc.
    - 256-color: `color214`, or raw `38;5;214`
    - Truecolor: `rgb(255,200,0)` (accepted; support varies by terminal)
- Example theme:
```/dev/null/theme.json#L1-24
{
  "enabled": true,
  "name": "catppuccin-mocha",
  "colors": {
    "header": "bold mauve",
    "accent": "teal",
    "selected": "bold peach",
    "favorite": "yellow",
    "checkbox": "blue",
    "group": "lavender",
    "dim": "faint",
    "separator": "gray",
    "help": "teal",
    "error": "red",
    "success": "green",
    "warn": "yellow"
  }
}
```

- Quick switch via env:
```/dev/null/shell.sh#L1-2
export TMUX_SSH_MANAGER_THEME=catppuccin-mocha
```

## TUI: Bubble Tea (vim/tmux ergonomics, preview, favorites/recents, groups, multi-select)

The selector runs on Bubble Tea and is tuned for tmux + vim muscle memory. It now includes:
- Vim motions:
  - j / k: move down/up
  - gg: go to top
  - G: go to bottom
  - u / d: half-page up/down
  - H / L: jump to previous/next group (YAML group name or inferred domain)
  - Numeric quick-select: type a number like 15 then Enter to connect to that row
- Incremental search:
  - / focuses the search input; filtering happens as you type
  - Esc blurs the search input to return to motion/action mode
- tmux-native actions (on the selected host):
  - Enter or c: connect in the current pane
  - v: vertical split (tmux split-window -h) and connect
  - s: horizontal split (tmux split-window -v) and connect
  - t: tiled multi-pane (open selected hosts into panes, then tmux select-layout tiled)
  - w: new tmux window and connect
  - W: open a new tmux window for ALL selected hosts (batch)
  - y: yank the ssh command of the selected host to tmux buffer
  - Y: yank ssh commands for ALL selected hosts (batch), one per line
  - ctrl+a: select all (respects current filter/search)
- Favorites and recents:
  - Space: toggle multi-select for the current row (for batch actions)
  - f: toggle favorite on the current host
  - F: filter to favorites only
  - R: filter to recents only (recently connected hosts)
  - A: clear filters (return to all)
- Preview pane:
  - Two-column layout shows the list on the left and a details pane on the right
  - Details include Name, Group (YAML or inferred), User, Port, Jump host, Tags, and the exact SSH command that will be executed
  - Shows whether the item is a Favorite, how many are Selected, and a count of tracked Recents
- Help and quit:
  - ?: toggle a help overlay with all key bindings
  - q or Esc: quit

Notes:
- Works with either YAML inventory or native SSH aliases depending on --tui-source (yaml or ssh).
- tmux integration uses tmux commands for splits and new windows, so the flow stays inside tmux.
- Batch actions operate on your multi-selection (Space to toggle). If nothing is selected, they operate on the current item.
- Styling: the TUI is terminal-friendly and plays nicely with Catppuccin and other tmux/iTerm2 themes. If you’d like colorized lines and a fully themed UI (e.g., with Lip Gloss), we can layer that on next—current focus is speed and ergonomics.
- See the "Classic TUI fallback" section below if you prefer the minimal interface or need a fallback in constrained environments.

## TUI: Bubble Tea (vim/tmux ergonomics)

The interactive selector is built on Bubble Tea and designed to feel natural for engineers building familiarity with vim and tmux.

- Vim motions:
  - j / k: move down/up
  - gg: go to top
  - G: go to bottom
  - u / d: half-page up/down
  - Numeric quick-select: type a number (e.g., 15) then Enter
- Search:
  - /: focus search input; type to filter incrementally
  - Esc: blur search input
- Actions on the selected host:
  - Enter or c: connect in the current pane
  - v: split vertically (side-by-side) and connect (tmux split-window -h)
  - s: split horizontally (stacked) and connect (tmux split-window -v)
  - t: tiled multi-pane (open selected hosts into panes, then tmux select-layout tiled)
  - w: open a new tmux window and connect (tmux new-window)
  - y: yank the ssh command to the tmux buffer (works well with tmux-yank)
  - ctrl+a: select all (respects current filter/search)
- Help and quit:
  - ?: toggle a help overlay
  - q or Esc: quit

Notes:
- Works with either YAML inventory or native SSH aliases depending on `--tui-source` (`yaml` or `ssh`).
- tmux integration uses tmux commands for splits and new windows, so the flow stays inside tmux.

### Classic TUI fallback

If you prefer a minimal, line-oriented interface or need a fallback in constrained environments, you can switch to the classic TUI:

- Command-line: add `--classic-tui`
- Environment variable: set `TMUX_SSH_MANAGER_TUI=classic`

Example:
```
/dev/null/shell.sh#L1-3
tmux-ssh-manager --tui-source yaml --classic-tui
# or
TMUX_SSH_MANAGER_TUI=classic tmux-ssh-manager --tui-source ssh
```

## SSH config support (native OpenSSH)

You can leverage your existing `~/.ssh/config` aliases directly with this plugin and CLI. This means all the power of OpenSSH is available out of the box: `User`, `Port`, `ForwardAgent`, `IdentityFile`, `ProxyJump`, and more.

- Direct connect by alias:
  - Run from a shell or bind a tmux key to:
    ```
    /dev/null/shell.sh#L1-1
    tmux-ssh-manager --host 192.168.0.1 --exec-replace
    ```
  - This defers to the system `ssh` client, so your `~/.ssh/config` is applied automatically without duplicating settings.

- Within tmux:
  - The default selector uses the YAML inventory file for grouping and metadata.
  - If you prefer to stick to SSH aliases only, you can create a custom binding in your `~/.tmux.conf` that calls the binary with a specific alias:
    ```
    /dev/null/tmux.conf#L1-3
    bind-key d run-shell "~/.tmux/plugins/tmux-ssh-manager/bin/tmux-ssh-manager --host 192.168.0.1 --exec-replace"
    ```
    This is a static binding for a single host alias. For a dynamic list and fuzzy filtering over SSH aliases, see the roadmap below.

- Why this is useful:
  - Keep using your SSH config; no need to duplicate per-host settings in YAML.
  - All OpenSSH features remain available and respected by the CLI invocation.

- Tips:
  - Mix and match: use YAML for grouping, tags, and organizational metadata, while relying on `~/.ssh/config` for low-level connection details.
  - If a host alias exists in SSH config, simply use `--host <alias>` with the CLI; SSH handles the rest.

- Roadmap:
  - The selector will support browsing and fuzzy-filtering native SSH config aliases out of the box.
  - Optional merging: YAML groups + SSH config details for a richer UI without duplication.


A tmux plugin and Go-backed CLI that turns tmux into a comfortable SSH session manager for network engineers.

Think: folders of saved sessions (hosts), jump hosts, per-host defaults, and fast fuzzy selection driven from within tmux.

This repository will ship:
- A Go `main` that powers the session manager UI and SSH orchestration
- A tiny `.tmux` launcher script to bind keys and run the binary
- A simple, human-friendly config format for defining sessions, groups, and jump hosts

---

## Features (MVP)

- Session catalog with groups (akin to SecureCRT session folders)
- Per-host options: username, port, jump host, tags
- Quick fuzzy selection of hosts and groups
- tmux-friendly UX: runs in-pane, binds to a tmux key, and returns you to work
- SSH command construction with optional ProxyJump
- Config-first: edit one YAML file and you’re done

Planned
- Bookmarking, recents, favorites
- Per-group defaults and overrides
- Per-host "command on connect" (e.g., `terminal length 0`)
- Optional inventory sourcing from NetBox and flat CSV/JSON files

---

## Prerequisites

- macOS with iTerm2 (recommended)
- tmux 3.2+ (earlier may work, but 3.2+ is best)
- tpm (Tmux Plugin Manager)
- Go 1.22 (for building the binary)
- OpenSSH client
- zsh + oh-my-zsh (optional, recommended)
- Catppuccin themes for iTerm2/tmux (optional)

---

## Quick Start

1) Install TPM (if you don’t already have it)
- Follow TPM instructions and ensure your `.tmux.conf` includes the TPM init line:
```
run '~/.tmux/plugins/tpm/tpm'
```

2) Clone this repository as a tmux plugin (local development)
```
mkdir -p ~/.tmux/plugins
git clone <this repo> ~/.tmux/plugins/tmux-ssh-manager
```

3) Build the Go binary
```
cd ~/.tmux/plugins/tmux-ssh-manager
go build -o bin/tmux-ssh-manager ./cmd/tmux-ssh-manager
```

4) Configure tmux
Add the following to your `~/.tmux.conf`:
```
# Load the plugin (local path)
set -g @plugin '~/.tmux/plugins/tmux-ssh-manager'

# Where the built binary lives
set -g @tmux_ssh_manager_bin '~/.tmux/plugins/tmux-ssh-manager/bin/tmux-ssh-manager'

# Bind a key to open the session manager (prefix + s)
bind-key s run-shell "~/.tmux/plugins/tmux-ssh-manager/scripts/tmux_ssh_manager.tmux"

# Optional: choose how the UI launches
# - window (default): opens a new tmux window running the UI (recommended)
# - popup: opens in a tmux popup (tmux >= 3.2)
set -g @tmux_ssh_manager_launch_mode 'window'

# Optional: choose how host selection works (picker)
# - tui (default): built-in interactive UI
# - fzf: use fzf for multi-select (Space toggles selection in fzf; spaces in the query are allowed)
#   Requires `fzf` to be installed and on PATH for the tmux server environment.
set -g @tmux_ssh_manager_picker 'tui'

# Optional: configure what the Enter key does in the host list
# - w (default): open a new tmux window and connect
# - p: connect in the existing pane (inline)
# - s: split horizontally (stacked) and connect
# - v: split vertically (side-by-side) and connect
# The dedicated keys (p, s, v, w) always work regardless of this setting.
set -g @tmux_ssh_manager_enter_mode 'w'

# TPM init (keep at bottom)
run '~/.tmux/plugins/tpm/tpm'
```

If you later publish the repo on GitHub, you can switch to:
```
set -g @plugin 'github_username/tmux-ssh-manager'
```

5) Create your config
Put your inventory file here:
```
~/.config/tmux-ssh-manager/hosts.yaml
```

Example:
```
groups:
  - name: dc1
    default_user: netops
    default_port: 22
    jump_host: bastion.dc1.example.com
  - name: lab
    default_user: labuser

hosts:
  - name: rtr1.dc1.example.com
    group: dc1
    user: admin
    tags: [router, ios-xe]

  - name: fw1.dc1.example.com
    group: dc1
    port: 2222
    tags: [firewall, pan]

  - name: leaf01.lab.local
    group: lab
    tags: [lab, eos]
```

6) Reload tmux and use it
- In tmux: `prefix` + `I` to install/update plugins via TPM
- Then hit `prefix` + `s` to open the session manager
- Pick a host; the plugin will open SSH in the current pane (or a new pane/window depending on future settings)

---

- Per-session login → per-host `user`/`port` with group defaults
- Jump host / SSH gateway → `jump_host` in group or per-host (uses `ProxyJump`)
- Session comments/tags → `tags` array on hosts
- Session commands → planned "on_connect" and per-host macros

---

## iTerm2 tips

- You don’t have to use iTerm2’s tmux integration (`tmux -CC`); standard tmux is fine.
- Enable "status bar" and set Catppuccin for a cohesive theme across tmux/iTerm2.
- iTerm2 profiles can set font, colors, and key mappings; tmux will happily coexist.

---

## Recommended tmux/tpm plugins

- `tmux-plugins/tpm` (plugin manager)
- `tmux-plugins/tmux-sensible` (good defaults)
- `tmux-plugins/tmux-yank` (system clipboard)
- `catppuccin/tmux` (theme)

Example:
```
set -g @plugin 'tmux-plugins/tpm'
set -g @plugin 'tmux-plugins/tmux-sensible'
set -g @plugin 'tmux-plugins/tmux-yank'
set -g @plugin 'catppuccin/tmux'
set -g @plugin '~/.tmux/plugins/tmux-ssh-manager'
run '~/.tmux/plugins/tpm/tpm'
```

---

## Configuration reference (YAML)

Top-level keys:
- `groups`: list of groups
  - `name`: string
  - `default_user`: string (optional)
  - `default_port`: int (optional)
  - `jump_host`: string (optional)
- `hosts`: list of hosts
  - `name`: string (hostname or IP)
  - `group`: string (must match a group name)
  - `user`: string (optional; overrides group default)
  - `port`: int (optional; overrides group default)
  - `jump_host`: string (optional; overrides group default)
  - `tags`: list of strings (optional)

Resolution rules:
- Host-level settings override group defaults.
- If no user/port is specified anywhere, falls back to ssh defaults (`$USER`, port `22`).
- If `jump_host` exists at host or group level, SSH is invoked with `-J`.

---

## Key bindings

- `prefix + s`: open the session manager UI (selector)
- `prefix + S`: reserved for future "quick connect" actions
- Customize in `.tmux.conf` as you like.

---

## Building from source

- Requires Go 1.22
- Build:
```
go build -o bin/tmux-ssh-manager ./cmd/tmux-ssh-manager
```
- Run directly (dev mode):
```
./bin/tmux-ssh-manager --config ~/.config/tmux-ssh-manager/hosts.yaml
```

---

## Troubleshooting

- Plugin doesn’t show up:
  - Ensure TPM is installed and `run '~/.tmux/plugins/tpm/tpm'` is at the bottom of `.tmux.conf`.
  - Run `prefix + I` to re-install.
- Key binding not working:
  - Check the path to the `.tmux` script and the compiled binary.
  - Verify `@tmux_ssh_manager_bin` matches where you built the binary.
  - If you previously used popups and want to switch back, set:
    - `set -g @tmux_ssh_manager_launch_mode 'popup'`
- SSH errors:
  - Try the constructed command manually (`ssh -J jump host`) to validate credentials and reachability.
  - Confirm group/host overrides are correct and YAML is valid.

---

## Contributing

- Keep the UX fast and keyboard-centric.
- Favor simple text configs; no heavyweight frameworks.
- PRs: tests for config parsing and SSH command construction are appreciated.

---

## License

MIT

---
