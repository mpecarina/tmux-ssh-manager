#!/usr/bin/env bash
# Robust tmux popup wrapper for tmux-ssh-manager.
#
# Goals:
# - Preserve a real TTY for interactive TUIs (Bubble Tea) in tmux popups.
# - Avoid "blank" popups by showing errors on failure.
# - Capture minimal metadata to a log file for post-mortem.
# - Keep popup open on failure so errors are visible.
# - Be silent on success (so the TUI fully owns the screen).
# - When host logging is enabled, append the *popup session output* to the per-host daily log
#   using macOS `script` (does not break TTY like piping to tee would).
#
# Popup action loop controller (tmux >= 3.3):
# - When running inside a tmux popup, nested popups can still be visually awkward in some setups.
# - Bubble Tea also runs in raw/alt-screen mode; trying to prompt inline can wedge input.
# - To make interactive flows reliable (cred set/delete), this wrapper runs a loop:
#     1) Run the main TUI command
#     2) If the TUI requested an action (via an action file), run it in THIS SAME popup TTY
#     3) Relaunch the TUI (same popup) until no action is requested
#
# Usage:
#   popup_wrapper.sh --cmd "<command string>"
# or:
#   popup_wrapper.sh "<command string>"
#
# Environment:
#   TMUX_SSH_MANAGER_LOG               Override metadata log path (default: ~/.config/tmux-ssh-manager/popup.log)
#   TMUX_SSH_MANAGER_TITLE             Optional title shown in header (only when not silent)
#   TMUX_SSH_MANAGER_SILENT_ON_SUCCESS If 1 (default), do not print wrapper header on success
#   TMUX_SSH_MANAGER_POPUP_ACTION_FILE Path to an action file (default: ~/.config/tmux-ssh-manager/popup_action.env)
#
# Popup host logging integration:
#   TMUX_SSH_MANAGER_HOST_LOG_PATH     If set, popup session output is appended to this file (per-host daily log).
#
# Action file format (key=value; comments with # allowed):
#   action=cred-set|cred-delete
#   host=<hostkey>
#   user=<optional username>
#   kind=password
#
# Security:
# - This wrapper may log the command string. Do not include secrets in the command line.
#
# NOTE:
# This file must be executable. Ensure:
#   chmod +x ~/.tmux/plugins/tmux-ssh-manager/scripts/popup_wrapper.sh

set -euo pipefail

# Enable verbose tracing when diagnosing "stuck" popups.
# Can be disabled by setting TMUX_SSH_MANAGER_WRAPPER_TRACE=0.
if [[ "${TMUX_SSH_MANAGER_WRAPPER_TRACE-1}" != "0" ]]; then
  set -x
fi

# ---------- args ----------
cmd_str=""
if [[ "${1-}" == "--cmd" ]]; then
  shift
  cmd_str="${1-}"
else
  cmd_str="${1-}"
fi

if [[ -z "${cmd_str}" ]]; then
  echo "tmux-ssh-manager popup wrapper: missing command string"
  echo "Usage: popup_wrapper.sh --cmd \"<command>\""
  exit 2
fi

# ---------- config ----------
silent_on_success="${TMUX_SSH_MANAGER_SILENT_ON_SUCCESS-1}"

title="${TMUX_SSH_MANAGER_TITLE-tmux-ssh-manager}"
log_path="${TMUX_SSH_MANAGER_LOG-}"
host_log_path="${TMUX_SSH_MANAGER_HOST_LOG_PATH-}"
action_file="${TMUX_SSH_MANAGER_POPUP_ACTION_FILE-}"

# Resolve default metadata log path under ~/.config
if [[ -z "${log_path}" ]]; then
  home="${HOME-}"
  if [[ -z "${home}" ]]; then
    home="/tmp"
  fi
  log_dir="${home}/.config/tmux-ssh-manager"
  log_path="${log_dir}/popup.log"
else
  log_dir="$(dirname "${log_path}")"
fi

# Default action file under the same config directory
if [[ -z "${action_file}" ]]; then
  action_file="${log_dir}/popup_action.env"
fi

mkdir -p "${log_dir}" 2>/dev/null || true
: >> "${log_path}" 2>/dev/null || true

# If host log path is provided, ensure its parent exists and the file is touch-able.
if [[ -n "${host_log_path}" ]]; then
  host_log_dir="$(dirname "${host_log_path}")"
  mkdir -p "${host_log_dir}" 2>/dev/null || true
  : >> "${host_log_path}" 2>/dev/null || true
fi

# ---------- helpers ----------
ts() { date +"%Y-%m-%dT%H:%M:%S%z"; }

hr() {
  printf '%*s\n' "${1:-80}" '' | tr ' ' '-'
}

print_header() {
  local cols="${COLUMNS:-80}"
  hr "${cols}"
  echo "${title} (popup)"
  echo "Time: $(ts)"
  echo "TMUX: ${TMUX-}"
  echo "TERM: ${TERM-}"
  echo "PWD : $(pwd)"
  echo "SHELL: ${SHELL-}"
  echo "PATH: ${PATH-}"
  echo "Log : ${log_path}"
  if [[ -n "${host_log_path}" ]]; then
    echo "HostLog: ${host_log_path}"
  fi
  echo "Cmd : ${cmd_str}"
  echo "ActionFile: ${action_file}"
  hr "${cols}"
}

keep_open_prompt() {
  echo
  echo "Press Enter to close this popup."
  if [[ -r /dev/tty ]]; then
    # shellcheck disable=SC2162
    read _ < /dev/tty || true
  else
    # shellcheck disable=SC2162
    read _ || true
  fi
}

run_action_file_if_present() {
  # Return codes:
  # - 0 => no action file present (no action requested)
  # - 1 => action was handled (caller should relaunch TUI)
  # - 2 => action file present but invalid (caller should stop)
  if [[ -z "${action_file}" || ! -f "${action_file}" ]]; then
    return 0
  fi

  local action=""
  local host=""
  local user=""
  local kind="password"

  while IFS= read -r line || [[ -n "${line}" ]]; do
    line="${line%%#*}"
    line="$(echo "${line}" | xargs 2>/dev/null || true)"
    [[ -z "${line}" ]] && continue

    key="${line%%=*}"
    val="${line#*=}"
    key="$(echo "${key}" | xargs 2>/dev/null || true)"
    val="$(echo "${val}" | xargs 2>/dev/null || true)"

    case "${key}" in
      action) action="${val}" ;;
      host) host="${val}" ;;
      user) user="${val}" ;;
      kind) kind="${val}" ;;
      *) ;;
    esac
  done < "${action_file}"

  rm -f "${action_file}" 2>/dev/null || true

  if [[ -z "${action}" || -z "${host}" ]]; then
    echo
    echo "tmux-ssh-manager: invalid popup action file (need action= and host=): ${action_file}"
    keep_open_prompt
    return 2
  fi

  local bin="${TMUX_SSH_MANAGER_BIN-tmux-ssh-manager}"
  local user_flag=""
  if [[ -n "${user}" ]]; then
    user_flag="--user ${user}"
  fi

  echo
  case "${action}" in
    cred-set)
      echo "tmux-ssh-manager: Set credential for ${host}"
      set +e
      ${bin} cred set --host "${host}" ${user_flag} --kind "${kind}"
      rc=$?
      set -e
      echo
      if [[ $rc -eq 0 ]]; then
        echo "Saved credential."
      else
        echo "FAILED (exit=${rc})"
      fi
      echo
      echo "Press Enter to return..."
      if [[ -r /dev/tty ]]; then
        # shellcheck disable=SC2162
        read _ < /dev/tty || true
      else
        # shellcheck disable=SC2162
        read _ || true
      fi
      return 1
      ;;
    cred-delete)
      echo "tmux-ssh-manager: Delete credential for ${host}"
      set +e
      ${bin} cred delete --host "${host}" ${user_flag} --kind "${kind}"
      rc=$?
      set -e
      echo
      if [[ $rc -eq 0 ]]; then
        echo "Deleted credential."
      else
        echo "FAILED (exit=${rc})"
      fi
      echo
      echo "Press Enter to return..."
      if [[ -r /dev/tty ]]; then
        # shellcheck disable=SC2162
        read _ < /dev/tty || true
      else
        # shellcheck disable=SC2162
        read _ || true
      fi
      return 1
      ;;
    *)
      echo "tmux-ssh-manager: unknown popup action=${action} (expected: cred-set|cred-delete)"
      keep_open_prompt
      return 2
      ;;
  esac
}



# ---------- main ----------
# Loop controller:
# - run the TUI command
# - if it requests an action (action file), run it and relaunch
# - otherwise exit normally (allows tmux display-popup -E to close the popup)
tty_in="/dev/tty"
tty_out="/dev/tty"

while true; do
  # In silent mode, clear the screen before launching the TUI so the wrapper doesn't
  # appear to be "stuck". The TUI will draw its own screen.
  if [[ "${silent_on_success}" != "0" ]]; then
    printf "\033[2J\033[H"
  else
    print_header
  fi

  # Log minimal metadata. Avoid piping/tee for interactive TTY correctness.
  {
    echo
    echo "===== $(ts) ====="
    echo "cmd=${cmd_str}"
    echo "pwd=$(pwd)"
    echo "term=${TERM-}"
    echo "tmux=${TMUX-}"
    echo "path=${PATH-}"
    echo "action_file=${action_file}"
    if [[ -n "${host_log_path}" ]]; then
      echo "host_log_path=${host_log_path}"
    fi
  } >> "${log_path}" 2>/dev/null || true

  set +e
  if [[ -r "${tty_in}" && -w "${tty_out}" ]]; then
    if [[ -n "${host_log_path}" && -x /usr/bin/script ]]; then
      /usr/bin/script -q -a "${host_log_path}" /bin/bash -lc "${cmd_str}" <"${tty_in}" >"${tty_out}" 2>&1
      exit_code=$?
    else
      bash -lc "${cmd_str}" <"${tty_in}" >"${tty_out}" 2>&1
      exit_code=$?
    fi
  else
    if [[ -n "${host_log_path}" && -x /usr/bin/script ]]; then
      /usr/bin/script -q -a "${host_log_path}" /bin/bash -lc "${cmd_str}"
      exit_code=$?
    else
      bash -lc "${cmd_str}"
      exit_code=$?
    fi
  fi
  set -e

  # If the TUI requested an action, handle it and relaunch.
  if run_action_file_if_present; then
    # return code 0 => no action file, fall through
    :
  else
    rc=$?
    if [[ $rc -eq 1 ]]; then
      # action handled; relaunch TUI
      continue
    fi
    # rc == 2 => invalid action file; exit with error
    exit_code=1
  fi

  # No action requested. Normal exit behavior.
  if [[ "${exit_code}" -ne 0 ]]; then
    echo
    echo "Command exited with code ${exit_code}."
    echo "See log: ${log_path}"
    if [[ -n "${host_log_path}" ]]; then
      echo "Host log: ${host_log_path}"
    fi
    keep_open_prompt
    exit "${exit_code}"
  fi

  exit 0
done
