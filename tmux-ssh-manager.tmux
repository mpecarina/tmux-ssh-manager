#!/usr/bin/env bash
set -euo pipefail

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="${CURRENT_DIR}"

# Read key binding from tmux option; default: prefix + s
KEY_BIND="$(tmux show -gqv @tmux_ssh_manager_key || true)"
if [[ -z "${KEY_BIND}" ]]; then
  KEY_BIND="s"
fi

LAUNCHER="${REPO_ROOT}/scripts/tmux_ssh_manager.tmux"

# If launcher is missing, do nothing (TPM may source this multiple times).
if [[ ! -f "${LAUNCHER}" ]]; then
  tmux display-message -d 5000 "tmux-ssh-manager: launcher script not found at ${LAUNCHER}"
  tmux display-message -d 5000 "Ensure plugin files are installed correctly."
  exit 0
fi

tmux bind-key "${KEY_BIND}" run-shell "\"${LAUNCHER}\""
tmux display-message -d 2000 "tmux-ssh-manager: bound prefix + ${KEY_BIND}"
