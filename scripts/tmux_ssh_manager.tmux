#!/usr/bin/env bash
# Launcher for tmux-ssh-manager (window by default, popup optional).
set -euo pipefail

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${CURRENT_DIR}/.." && pwd)"

BIN_PATH="$(tmux show -gqv @tmux_ssh_manager_bin || true)"
CONFIG_PATH="$(tmux show -gqv @tmux_ssh_manager_config || true)"
TUI_SOURCE="$(tmux show -gqv @tmux_ssh_manager_tui_source || true)"
SSH_CONFIG_OPT="$(tmux show -gqv @tmux_ssh_manager_ssh_config || true)"
LAUNCH_MODE="$(tmux show -gqv @tmux_ssh_manager_launch_mode || true)"
PICKER_MODE="$(tmux show -gqv @tmux_ssh_manager_picker || true)"

GPG_SYMMETRIC_OPT="$(tmux show -gqv @tmux_ssh_manager_gpg_symmetric || true)"
GPG_PASSPHRASE_FILE_OPT="$(tmux show -gqv @tmux_ssh_manager_gpg_passphrase_file || true)"
GPG_RECIPIENT_OPT="$(tmux show -gqv @tmux_ssh_manager_gpg_recipient || true)"
GPG_BINARY_OPT="$(tmux show -gqv @tmux_ssh_manager_gpg_binary || true)"
ENTER_MODE_OPT="$(tmux show -gqv @tmux_ssh_manager_enter_mode || true)"

# Force SSH mode by default for tmux keybinding launches (never require YAML).
# Users can still override with: set -g @tmux_ssh_manager_tui_source 'yaml'
if [[ -z "${TUI_SOURCE}" ]]; then
  TUI_SOURCE="ssh"
fi


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



DEFAULT_BIN_PATH="${REPO_ROOT}/bin/tmux-ssh-manager"
if [[ ! -x "${BIN_PATH}" ]]; then
  # Only auto-build when using the default (repo-local) binary location.
  # If the user set @tmux_ssh_manager_bin, don't build into arbitrary paths.
  if [[ "${BIN_PATH}" == "${DEFAULT_BIN_PATH}" ]]; then
    if ! command -v go >/dev/null 2>&1; then
      tmux display-message -d 7000 "tmux-ssh-manager: 'go' not found; cannot build ${DEFAULT_BIN_PATH}"
      tmux display-message -d 7000 "Install Go or set: set -g @tmux_ssh_manager_bin '/path/to/tmux-ssh-manager'"
      exit 1
    fi

    mkdir -p "${REPO_ROOT}/bin" 2>/dev/null || true
    tmux display-message -d 2000 "tmux-ssh-manager: building Go binary..."
    if ! (cd "${REPO_ROOT}" && go build -o "bin/tmux-ssh-manager" "./cmd/tmux-ssh-manager"); then
      tmux display-message -d 8000 "tmux-ssh-manager: build failed. Try: (cd ${REPO_ROOT} && go build -o bin/tmux-ssh-manager ./cmd/tmux-ssh-manager)"
      exit 1
    fi
  fi

  if [[ ! -x "${BIN_PATH}" ]]; then
    tmux display-message -d 5000 "tmux-ssh-manager: binary not found or not executable at: ${BIN_PATH}"
    tmux display-message -d 5000 "Build it with: (cd ${REPO_ROOT} && go build -o bin/tmux-ssh-manager ./cmd/tmux-ssh-manager)"
    tmux display-message -d 5000 "Or set: set -g @tmux_ssh_manager_bin '/path/to/tmux-ssh-manager'"
    exit 1
  fi
fi

CMD_STR="exec \"${BIN_PATH}\""

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

# Optional picker:
# - tui (default): built-in Bubble Tea UI
# - fzf: external fuzzy picker with multi-select (space toggles selection inside fzf; space allowed in query)
if [[ -z "${PICKER_MODE}" ]]; then
  PICKER_MODE="tui"
fi
if [[ "${PICKER_MODE}" == "fzf" ]]; then
  CMD_STR+=" --fzf"
fi

if ! tmux display-message -d 1 "tmux-ssh-manager: starting" >/dev/null 2>&1; then
  echo "tmux-ssh-manager: executing: ${CMD_STR}"
  eval "${CMD_STR}"
  exit_code=$?
  if [[ $exit_code -ne 0 ]]; then
    echo "tmux-ssh-manager: command failed with exit code ${exit_code}"
  fi
  exit $exit_code
fi

# Determine tmux version for popup support (require >= 3.3)
version_raw="$(tmux -V 2>/dev/null | awk '{print $2}')"
# Extract numeric major.minor (handles suffixes like 3.3a)
ver_major="${version_raw%%.*}"
ver_minor_patch="${version_raw#*.}"
ver_minor="${ver_minor_patch%%[^0-9]*}"

supports_popup=false
if [[ -n "${ver_major}" && -n "${ver_minor}" ]]; then
  if (( ver_major > 3 )) || (( ver_major == 3 && ver_minor >= 3 )); then
    supports_popup=true
  fi
fi

POPUP_WRAPPER="${REPO_ROOT}/scripts/popup_wrapper.sh"

# Capture the pane that triggered the launch so "pane" mode can send-keys back to it.
# We write this to a temp file instead of embedding in ENV_PREFIX because pane IDs
# contain '%' (e.g., %0) which tmux interprets as format specifiers in new-window/display-popup commands.
CALLER_PANE="$(tmux display-message -p '#{pane_id}' 2>/dev/null || true)"
CALLER_PANE_FILE="${TMPDIR:-/tmp}/tmux-ssh-manager-caller-pane.$$"
if [[ -n "${CALLER_PANE}" ]]; then
  printf '%s' "${CALLER_PANE}" > "${CALLER_PANE_FILE}"
fi


if [[ "${LAUNCH_MODE}" == "window" ]]; then
  tmux display-message -d 1500 "tmux-ssh-manager: launching window"

  ENV_PREFIX="TERM=xterm-256color TMUX_SSH_MANAGER_BIN=$(printf %q "${BIN_PATH}")"

  # Prefer explicit tmux options (reliable for keybinding launches), fall back to process env.
  if [[ -n "${GPG_SYMMETRIC_OPT}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_SYMMETRIC=$(printf %q "${GPG_SYMMETRIC_OPT}")"
  elif [[ -n "${TMUX_SSH_MANAGER_GPG_SYMMETRIC-}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_SYMMETRIC=$(printf %q "${TMUX_SSH_MANAGER_GPG_SYMMETRIC}")"
  fi

  if [[ -n "${GPG_PASSPHRASE_FILE_OPT}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_PASSPHRASE_FILE=$(printf %q "${GPG_PASSPHRASE_FILE_OPT}")"
  elif [[ -n "${TMUX_SSH_MANAGER_GPG_PASSPHRASE_FILE-}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_PASSPHRASE_FILE=$(printf %q "${TMUX_SSH_MANAGER_GPG_PASSPHRASE_FILE}")"
  fi

  if [[ -n "${GPG_RECIPIENT_OPT}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_RECIPIENT=$(printf %q "${GPG_RECIPIENT_OPT}")"
  elif [[ -n "${TMUX_SSH_MANAGER_GPG_RECIPIENT-}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_RECIPIENT=$(printf %q "${TMUX_SSH_MANAGER_GPG_RECIPIENT}")"
  fi

  if [[ -n "${GPG_BINARY_OPT}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_BINARY=$(printf %q "${GPG_BINARY_OPT}")"
  elif [[ -n "${TMUX_SSH_MANAGER_GPG_BINARY-}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_BINARY=$(printf %q "${TMUX_SSH_MANAGER_GPG_BINARY}")"
  fi

  if [[ -n "${ENTER_MODE_OPT}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_ENTER_MODE=$(printf %q "${ENTER_MODE_OPT}")"
  elif [[ -n "${TMUX_SSH_MANAGER_ENTER_MODE-}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_ENTER_MODE=$(printf %q "${TMUX_SSH_MANAGER_ENTER_MODE}")"
  fi

  if [[ -n "${CALLER_PANE_FILE}" ]] && [[ -f "${CALLER_PANE_FILE}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_CALLER_PANE_FILE=$(printf %q "${CALLER_PANE_FILE}")"
  fi

  if ! tmux new-window -n "ssh-manager" -c "#{pane_current_path}" -- bash -lc "${ENV_PREFIX} ${CMD_STR}"; then
    tmux display-message -d 10000 "tmux-ssh-manager: failed to open window."
    exit 1
  fi
  exit 0
fi

if [[ "${supports_popup}" == true ]]; then
  if [[ ! -x "${POPUP_WRAPPER}" ]]; then
    tmux display-message -d 8000 "tmux-ssh-manager: popup wrapper not executable: ${POPUP_WRAPPER} (chmod +x it)"
    exit 1
  fi

  HOST_LOGS_BASE="${HOME}/.config/tmux-ssh-manager/logs"

  tmux display-message -d 1500 "tmux-ssh-manager: launching popup"
  ENV_PREFIX="TERM=xterm-256color TMUX_SSH_MANAGER_IN_POPUP=1 TMUX_SSH_MANAGER_TITLE='tmux-ssh-manager' TMUX_SSH_MANAGER_HOST_LOGS_BASE=$(printf %q "${HOST_LOGS_BASE}") TMUX_SSH_MANAGER_BIN=$(printf %q "${BIN_PATH}")"

  # Prefer explicit tmux options (reliable for keybinding launches), fall back to process env.
  if [[ -n "${GPG_SYMMETRIC_OPT}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_SYMMETRIC=$(printf %q "${GPG_SYMMETRIC_OPT}")"
  elif [[ -n "${TMUX_SSH_MANAGER_GPG_SYMMETRIC-}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_SYMMETRIC=$(printf %q "${TMUX_SSH_MANAGER_GPG_SYMMETRIC}")"
  fi

  if [[ -n "${GPG_PASSPHRASE_FILE_OPT}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_PASSPHRASE_FILE=$(printf %q "${GPG_PASSPHRASE_FILE_OPT}")"
  elif [[ -n "${TMUX_SSH_MANAGER_GPG_PASSPHRASE_FILE-}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_PASSPHRASE_FILE=$(printf %q "${TMUX_SSH_MANAGER_GPG_PASSPHRASE_FILE}")"
  fi

  if [[ -n "${GPG_RECIPIENT_OPT}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_RECIPIENT=$(printf %q "${GPG_RECIPIENT_OPT}")"
  elif [[ -n "${TMUX_SSH_MANAGER_GPG_RECIPIENT-}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_RECIPIENT=$(printf %q "${TMUX_SSH_MANAGER_GPG_RECIPIENT}")"
  fi

  if [[ -n "${GPG_BINARY_OPT}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_BINARY=$(printf %q "${GPG_BINARY_OPT}")"
  elif [[ -n "${TMUX_SSH_MANAGER_GPG_BINARY-}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_GPG_BINARY=$(printf %q "${TMUX_SSH_MANAGER_GPG_BINARY}")"
  fi

  if [[ -n "${ENTER_MODE_OPT}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_ENTER_MODE=$(printf %q "${ENTER_MODE_OPT}")"
  elif [[ -n "${TMUX_SSH_MANAGER_ENTER_MODE-}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_ENTER_MODE=$(printf %q "${TMUX_SSH_MANAGER_ENTER_MODE}")"
  fi

  if [[ -n "${CALLER_PANE_FILE}" ]] && [[ -f "${CALLER_PANE_FILE}" ]]; then
    ENV_PREFIX+=" TMUX_SSH_MANAGER_CALLER_PANE_FILE=$(printf %q "${CALLER_PANE_FILE}")"
  fi

  if ! tmux display-popup -E -w 90% -h 80% -- bash -lc "${ENV_PREFIX} \"${POPUP_WRAPPER}\" --cmd $(printf %q "${CMD_STR}")"; then
    tmux display-message -d 10000 "tmux-ssh-manager: popup failed."
    exit 1
  fi
else
  tmux display-message -d 10000 "tmux-ssh-manager: popup mode requires tmux >= 3.3 (detected: $(tmux -V 2>/dev/null | awk '{print $2}'))"
  tmux display-message -d 10000 "tmux-ssh-manager: set -g @tmux_ssh_manager_launch_mode 'window'"
  exit 1
fi
