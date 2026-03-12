#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${ROOT_DIR}/bin"
BIN_PATH="${BIN_DIR}/tmux-ssh-manager"

cmd="${1:-all}"

run_fmt() {
  (cd "${ROOT_DIR}" && gofmt -w ./cmd ./pkg)
}

run_test() {
  (cd "${ROOT_DIR}" && go test ./...)
}

run_build() {
  mkdir -p "${BIN_DIR}"
  (cd "${ROOT_DIR}" && go build -ldflags "-X tmux-ssh-manager/pkg/app.Version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o "${BIN_PATH}" ./cmd/tmux-ssh-manager)
}

run_smoke() {
  local tmp_home
  tmp_home="$(mktemp -d)"
  trap 'rm -rf "${tmp_home}"' RETURN

  mkdir -p "${tmp_home}/.ssh/conf.d"
  cat > "${tmp_home}/.ssh/config" <<'EOF'
Include conf.d/*.conf

Host app1
  HostName 10.10.0.10
  User demo
EOF

  cat > "${tmp_home}/.ssh/conf.d/lab.conf" <<'EOF'
Host db1
  HostName 10.10.0.20
  User postgres
  Port 2200
EOF

  local output
  output="$(HOME="${tmp_home}" "${BIN_PATH}" list)"
  grep -qx 'app1' <<<"${output}"
  grep -qx 'db1' <<<"${output}"

  local json_output
  json_output="$(HOME="${tmp_home}" "${BIN_PATH}" list --json)"
  echo "${json_output}" | grep -q '"alias": "app1"'
  echo "${json_output}" | grep -q '"alias": "db1"'
}

LIVE_HOST="${LIVE_HOST:-k3d-staging}"

run_live() {
  # Validate the binary can resolve and list the live host.
  "${BIN_PATH}" list | grep -qx "${LIVE_HOST}" || {
    echo "FAIL: live host ${LIVE_HOST} not found in list output" >&2; return 1
  }

  # Validate --json includes the live host with expected fields.
  local json
  json="$("${BIN_PATH}" list --json)"
  echo "${json}" | grep -q "\"alias\": \"${LIVE_HOST}\"" || {
    echo "FAIL: live host ${LIVE_HOST} not found in --json output" >&2; return 1
  }

  # Validate connect --dry-run produces the expected ssh command.
  local dry
  dry="$("${BIN_PATH}" connect --dry-run "${LIVE_HOST}")"
  if [[ "${dry}" != "ssh ${LIVE_HOST}" ]]; then
    echo "FAIL: connect --dry-run expected 'ssh ${LIVE_HOST}', got '${dry}'" >&2; return 1
  fi

  # Validate SSH round-trip: run a command on the remote host, check output.
  local marker="TSM_LIVE_$(date +%s)"
  local remote_out
  remote_out="$(ssh -T -o ConnectTimeout=10 "${LIVE_HOST}" "echo ${marker}" 2>/dev/null)" || {
    echo "FAIL: ssh to ${LIVE_HOST} failed" >&2; return 1
  }
  echo "${remote_out}" | head -1 | grep -qx "${marker}" || {
    echo "FAIL: expected '${marker}' in ssh output, got: ${remote_out}" >&2; return 1
  }

  # Validate multi-command output preservation (no mangling).
  local multi_out
  multi_out="$(ssh -T -o ConnectTimeout=10 "${LIVE_HOST}" 'echo LINE_A; echo LINE_B; echo LINE_C' 2>/dev/null)" || {
    echo "FAIL: multi-command ssh to ${LIVE_HOST} failed" >&2; return 1
  }
  local line_count
  line_count="$(echo "${multi_out}" | wc -l | tr -d ' ')"
  if [[ "${line_count}" -ne 3 ]]; then
    echo "FAIL: expected 3 lines of output, got ${line_count}: ${multi_out}" >&2; return 1
  fi
}

case "${cmd}" in
  fmt)
    run_fmt
    ;;
  test)
    run_test
    ;;
  build)
    run_build
    ;;
  smoke)
    run_build
    run_smoke
    ;;
  live)
    run_build
    run_live
    ;;
  all)
    run_fmt
    run_test
    run_build
    run_smoke
    ;;
  *)
    echo "usage: scripts/harness.sh {fmt|test|build|smoke|live|all}" >&2
    exit 2
    ;;
esac
