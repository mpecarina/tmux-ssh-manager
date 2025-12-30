#!/usr/bin/env bash
# tmux_ssh_manager.tmux
#
# Launcher for the tmux-ssh-manager Go binary.
# - Reads the @tmux_ssh_manager_bin option (path to the compiled binary)
# - Optionally reads @tmux_ssh_manager_config (path to YAML inventory)
# - Runs the manager in a tmux window (default, more native) or in a tmux popup (optional)
# - Defaults to SSH inventory mode (never requires YAML) unless explicitly overridden
#
# Launch mode (tmux option):
# - @tmux_ssh_manager_launch_mode = window | popup
#   - window (default): opens a new tmux window running the TUI (recommended)
#   - popup: opens a tmux popup running the TUI (tmux >= 3.2)
#
# Launcher-side debug logging:
# - Writes a debug log to ~/.config/tmux-ssh-manager/launcher.log so you can see why launch failed.
#
# Popup behavior (only when launch_mode=popup):
# - Set TMUX_SSH_MANAGER_IN_POPUP=1 for downstream use.
# - Force a stable TERM inside the popup to improve Bubble Tea rendering reliability.
# - Pass binary path into the popup environment as TMUX_SSH_MANAGER_BIN for askpass.
# - Use a robust wrapper that captures output to a log and keeps the popup open on failures.

set -euo pipefail
# Enable xtrace for debugging; disable by commenting if too verbose
set -x

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${CURRENT_DIR}/.." && pwd)"

# Launcher-side log (separate from popup wrapper log).
LOG_DIR="${HOME}/.config/tmux-ssh-manager"
LOG_FILE="${LOG_DIR}/launcher.log"
mkdir -p "${LOG_DIR}" 2>/dev/null || true
# Ensure file exists so it's tail-able even if we exit early.
: >> "${LOG_FILE}" 2>/dev/null || true

ts() { date +"%Y-%m-%dT%H:%M:%S%z"; }
log() { printf "[%s] %s\n" "$(ts)" "$*" >> "${LOG_FILE}" 2>/dev/null || true; }

log "launcher: start; cwd=$(pwd); current_dir=${CURRENT_DIR}; repo_root=${REPO_ROOT}"
log "launcher: tmux=$(tmux -V 2>/dev/null || echo 'unknown'); TMUX_env=${TMUX-}; TERM=${TERM-}; SHELL=${SHELL-}; PATH=${PATH-}"

# Read tmux options (global, quiet, value-only)
BIN_PATH="$(tmux show -gqv @tmux_ssh_manager_bin || true)"
CONFIG_PATH="$(tmux show -gqv @tmux_ssh_manager_config || true)"
TUI_SOURCE="$(tmux show -gqv @tmux_ssh_manager_tui_source || true)"
SSH_CONFIG_OPT="$(tmux show -gqv @tmux_ssh_manager_ssh_config || true)"
LAUNCH_MODE="$(tmux show -gqv @tmux_ssh_manager_launch_mode || true)"

log "launcher: opt @tmux_ssh_manager_bin=${BIN_PATH}"
log "launcher: opt @tmux_ssh_manager_config=${CONFIG_PATH}"
log "launcher: opt @tmux_ssh_manager_tui_source=${TUI_SOURCE}"
log "launcher: opt @tmux_ssh_manager_ssh_config=${SSH_CONFIG_OPT}"
log "launcher: opt @tmux_ssh_manager_launch_mode=${LAUNCH_MODE}"

# Force SSH mode by default for tmux keybinding launches (never require YAML).
# Users can still override with: set -g @tmux_ssh_manager_tui_source 'yaml'
if [[ -z "${TUI_SOURCE}" ]]; then
  TUI_SOURCE="ssh"
fi
log "launcher: effective TUI_SOURCE(after default)=${TUI_SOURCE}"

# Defaults
if [[ -z "${BIN_PATH}" ]]; then
  BIN_PATH="${REPO_ROOT}/bin/tmux-ssh-manager"
fi
if [[ -z "${CONFIG_PATH}" ]]; then
  CONFIG_PATH="${HOME}/.config/tmux-ssh-manager/hosts.yaml"
fi

# Expand a leading "~/" in user-configured paths (tmux options often include it).
# Important: only expand if the string actually starts with "~/" (not ".../~/" somewhere in the middle).
if [[ "${BIN_PATH}" == "~/"* ]]; then
  BIN_PATH="${HOME}/${BIN_PATH:2}"
fi
if [[ "${CONFIG_PATH}" == "~/"* ]]; then
  CONFIG_PATH="${HOME}/${CONFIG_PATH:2}"
fi

# Default launch mode to "window" for a more native tmux experience.
if [[ -z "${LAUNCH_MODE}" ]]; then
  LAUNCH_MODE="window"
fi
# Normalize
if [[ "${LAUNCH_MODE}" != "popup" ]]; then
  LAUNCH_MODE="window"
fi

log "launcher: BIN_PATH(resolved)=${BIN_PATH}"
log "launcher: CONFIG_PATH(resolved)=${CONFIG_PATH}"
log "launcher: LAUNCH_MODE(effective)=${LAUNCH_MODE}"

# Sanity checks and friendly guidance
if [[ ! -x "${BIN_PATH}" ]]; then
  log "launcher: ERROR binary not executable: ${BIN_PATH}"
  tmux display-message -d 5000 "tmux-ssh-manager: binary not found or not executable at: ${BIN_PATH}"
  tmux display-message -d 5000 "Build it with: (cd ${REPO_ROOT} && go build -o bin/tmux-ssh-manager ./cmd/tmux-ssh-manager)"
  tmux display-message -d 5000 "Or set a custom path: set -g @tmux_ssh_manager_bin '/path/to/tmux-ssh-manager'"
  exit 1
fi

# Prepare command string
CMD_STR="exec \"${BIN_PATH}\""
log "launcher: base CMD_STR=${CMD_STR}"

# Decide TUI source:
# - If @tmux_ssh_manager_tui_source is set, honor it ('yaml' or 'ssh')
# - Default (when unset): SSH aliases (never require YAML)
effective_source=""
if [[ -n "${TUI_SOURCE}" ]]; then
  if [[ "${TUI_SOURCE}" == "ssh" ]]; then
    effective_source="ssh"
  else
    effective_source="yaml"
  fi
else
  effective_source="ssh"
fi

# Build command flags based on the effective source and available paths
if [[ "${effective_source}" == "yaml" ]]; then
  if [[ -f "${CONFIG_PATH}" ]]; then
    CMD_STR+=" --tui-source yaml --config \"${CONFIG_PATH}\""
  else
    tmux display-message -d 2500 "tmux-ssh-manager: YAML config missing, falling back to SSH aliases"
    CMD_STR+=" --tui-source ssh"
    if [[ -n "${SSH_CONFIG_OPT}" ]]; then
      CMD_STR+=" --ssh-config \"${SSH_CONFIG_OPT}\""
    fi
  fi
else
  CMD_STR+=" --tui-source ssh"
  if [[ -n "${SSH_CONFIG_OPT}" ]]; then
    CMD_STR+=" --ssh-config \"${SSH_CONFIG_OPT}\""
  fi
fi

# Detect whether we can talk to the tmux server.
# Note: `tmux run-shell` may execute without TMUX env set, even though we're in a tmux session.
# Use a capability check instead of relying on $TMUX.
if ! tmux display-message -d 1 "tmux-ssh-manager: starting" >/dev/null 2>&1; then
  log "launcher: tmux server NOT reachable from this context; running command directly"
  # Not in a tmux server context (or tmux is unavailable); just run the binary in this shell
  echo "tmux-ssh-manager: executing: ${CMD_STR}"
  eval "${CMD_STR}"
  exit_code=$?
  if [[ $exit_code -ne 0 ]]; then
    echo "tmux-ssh-manager: command failed with exit code ${exit_code}"
  fi
  exit $exit_code
fi
log "launcher: tmux server reachable; proceeding with popup path"

# Determine tmux version for popup support (>= 3.2 is safe)
version_raw="$(tmux -V 2>/dev/null | awk '{print $2}')"
# Extract numeric major.minor (handles suffixes like 3.3a)
ver_major="${version_raw%%.*}"
ver_minor_patch="${version_raw#*.}"
ver_minor="${ver_minor_patch%%[^0-9]*}"

supports_popup=false
if [[ -n "${ver_major}" && -n "${ver_minor}" ]]; then
  if (( ver_major > 3 )) || (( ver_major == 3 && ver_minor >= 2 )); then
    supports_popup=true
  fi
fi

POPUP_WRAPPER="${REPO_ROOT}/scripts/popup_wrapper.sh"
log "launcher: supports_popup=${supports_popup}; popup_wrapper=${POPUP_WRAPPER}"

# Launch in a window by default (recommended), or in a popup if explicitly requested.
if [[ "${LAUNCH_MODE}" == "window" ]]; then
  log "launcher: launching in new tmux window; CMD_STR=${CMD_STR}"

  # Start in a new window and run the binary directly.
  # We also export TMUX_SSH_MANAGER_BIN so askpass can invoke the exact binary path.
  tmux display-message -d 1500 "tmux-ssh-manager: launching window"
  if ! tmux new-window -n "ssh-manager" -c "#{pane_current_path}" -- bash -lc "TERM=xterm-256color TMUX_SSH_MANAGER_BIN=$(printf %q "${BIN_PATH}") ${CMD_STR}"; then
    log "launcher: ERROR tmux new-window failed"
    tmux display-message -d 10000 "tmux-ssh-manager: failed to open window. See launcher.log under ~/.config/tmux-ssh-manager/"
    exit 1
  fi
  log "launcher: window launched successfully"
  exit 0
fi

# launch_mode=popup (optional)
if [[ "${supports_popup}" == true ]]; then
  if [[ ! -x "${POPUP_WRAPPER}" ]]; then
    log "launcher: ERROR popup wrapper not executable: ${POPUP_WRAPPER}"
    tmux display-message -d 8000 "tmux-ssh-manager: popup wrapper not executable: ${POPUP_WRAPPER} (fix with: chmod +x ${POPUP_WRAPPER})"
    exit 1
  fi

  # Host key for per-host logs is the selected SSH alias/hostname in the UI.
  # For popup, we don't know which host yet; the wrapper will log once it can infer host key.
  # We still pass the base logs directory for consistency.
  HOST_LOGS_BASE="${HOME}/.config/tmux-ssh-manager/logs"

  log "launcher: launching popup; CMD_STR=${CMD_STR}"
  # Close popup automatically when the command exits successfully (-E).
  # On failure, the wrapper will keep the popup open and prompt.
  tmux display-message -d 1500 "tmux-ssh-manager: launching popup"
  if ! tmux display-popup -E -w 90% -h 80% -- bash -lc "TERM=xterm-256color TMUX_SSH_MANAGER_IN_POPUP=1 TMUX_SSH_MANAGER_TITLE='tmux-ssh-manager' TMUX_SSH_MANAGER_HOST_LOGS_BASE=$(printf %q "${HOST_LOGS_BASE}") TMUX_SSH_MANAGER_BIN=$(printf %q "${BIN_PATH}") \"${POPUP_WRAPPER}\" --cmd $(printf %q "${CMD_STR}")"; then
    log "launcher: ERROR tmux display-popup failed; tmux_version=$(tmux -V 2>/dev/null | awk '{print $2}')"
    tmux display-message -d 10000 "tmux-ssh-manager: popup failed. See launcher.log and popup.log under ~/.config/tmux-ssh-manager/"
    exit 1
  fi
  log "launcher: popup launched successfully"
else
  log "launcher: ERROR popup unsupported; tmux_version=$(tmux -V 2>/dev/null | awk '{print $2}')"
  tmux display-message -d 10000 "tmux-ssh-manager: tmux popup unsupported (need tmux >= 3.2). Detected tmux=$(tmux -V 2>/dev/null | awk '{print $2}')"
  exit 1
fi
