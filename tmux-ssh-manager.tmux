#!/usr/bin/env bash
# tmux-ssh-manager.tmux
#
# TPM plugin run file:
# - Binds a default key to launch the tmux-ssh-manager UI from within tmux.
# - Reads optional tmux options to customize the key and binary/config paths.
#
# Options (set in your ~/.tmux.conf if desired):
#   set -g @tmux_ssh_manager_key 's'                   # default: 's' (prefix + s)
#   set -g @tmux_ssh_manager_bin '~/.tmux/plugins/tmux-ssh-manager/bin/tmux-ssh-manager'
#   set -g @tmux_ssh_manager_config '~/.config/tmux-ssh-manager/hosts.yaml'
#
# This file is executed by TPM when the plugin is sourced.
# It binds the chosen key to run the launcher script:
#   scripts/tmux_ssh_manager.tmux
#
# The launcher opens the UI in a tmux window by default (more native), with an
# optional popup launch mode when configured.

set -euo pipefail

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="${CURRENT_DIR}"

# Read key binding from tmux option; default to 's' (prefix + s)
KEY_BIND="$(tmux show -gqv @tmux_ssh_manager_key || true)"
if [[ -z "${KEY_BIND}" ]]; then
  KEY_BIND="s"
fi

LAUNCHER="${REPO_ROOT}/scripts/tmux_ssh_manager.tmux"

# Friendly guidance if launcher is missing
if [[ ! -f "${LAUNCHER}" ]]; then
  tmux display-message -d 5000 "tmux-ssh-manager: launcher script not found at ${LAUNCHER}"
  tmux display-message -d 5000 "Ensure plugin files are installed correctly."
  exit 0
fi

# Bind the key in the default (prefix) table:
# Users will press: prefix + ${KEY_BIND}
tmux bind-key "${KEY_BIND}" run-shell "\"${LAUNCHER}\""

# Optional: brief status message (non-intrusive)
tmux display-message -d 2000 "tmux-ssh-manager: bound prefix + ${KEY_BIND} to session manager"
