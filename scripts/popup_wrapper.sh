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
# Usage:
#   popup_wrapper.sh --cmd "<command string>"
# or:
#   popup_wrapper.sh "<command string>"
#
# Environment:
#   TMUX_SSH_MANAGER_LOG               Override metadata log path (default: ~/.config/tmux-ssh-manager/popup.log)
#   TMUX_SSH_MANAGER_TITLE             Optional title shown in header (only when not silent)
#   TMUX_SSH_MANAGER_SILENT_ON_SUCCESS If 1 (default), do not print wrapper header on success
#
# Popup host logging integration:
#   TMUX_SSH_MANAGER_HOST_LOG_PATH     If set, popup session output is appended to this file (per-host daily log).
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

# ---------- main ----------
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
  if [[ -n "${host_log_path}" ]]; then
    echo "host_log_path=${host_log_path}"
  fi
} >> "${log_path}" 2>/dev/null || true

# IMPORTANT:
# Do NOT use a pipe to `tee` for Bubble Tea or other full-screen TUIs.
# A pipe makes stdout/stderr non-TTY, which can prevent rendering or cause hangs.
#
# Wire stdio to /dev/tty to ensure Bubble Tea sees a real terminal device.
tty_in="/dev/tty"
tty_out="/dev/tty"

set +e
if [[ -r "${tty_in}" && -w "${tty_out}" ]]; then
  if [[ -n "${host_log_path}" && -x /usr/bin/script ]]; then
    # Capture the popup session output (ssh output) to the per-host daily log.
    # -q: quiet
    # -a: append
    # We run a bash -lc command inside script so ssh remains interactive.
    #
    # NOTE: script logs what is printed by the program; it does not log keystrokes.
    /usr/bin/script -q -a "${host_log_path}" /bin/bash -lc "${cmd_str}" <"${tty_in}" >"${tty_out}" 2>&1
    exit_code=$?
  else
    bash -lc "${cmd_str}" <"${tty_in}" >"${tty_out}" 2>&1
    exit_code=$?
  fi
else
  # Fallback: run with inherited stdio (may be less reliable if not a TTY).
  if [[ -n "${host_log_path}" && -x /usr/bin/script ]]; then
    /usr/bin/script -q -a "${host_log_path}" /bin/bash -lc "${cmd_str}"
    exit_code=$?
  else
    bash -lc "${cmd_str}"
    exit_code=$?
  fi
fi
set -e

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
