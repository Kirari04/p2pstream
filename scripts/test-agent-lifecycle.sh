#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
INSTALL_SCRIPT="${ROOT_DIR}/scripts/install-agent.sh"
UNINSTALL_SCRIPT="${ROOT_DIR}/scripts/uninstall-agent.sh"
REAL_PATH="$PATH"
TEST_DIR=""

fail() {
  printf 'agent lifecycle test failed: %s\n' "$*" >&2
  if [[ -n "${TEST_DIR:-}" ]]; then
    printf 'fixture kept at: %s\n' "$TEST_DIR" >&2
  fi
  exit 1
}

assert_exists() {
  [[ -e "$1" ]] || fail "expected path to exist: $1"
}

assert_absent() {
  [[ ! -e "$1" && ! -L "$1" ]] || fail "expected path to be absent: $1"
}

assert_contains() {
  local path="$1"
  local text="$2"
  grep -Fq -- "$text" "$path" || fail "expected ${path} to contain: ${text}"
}

assert_not_contains() {
  local path="$1"
  local text="$2"
  if grep -Fq -- "$text" "$path"; then
    fail "expected ${path} not to contain: ${text}"
  fi
}

assert_systemctl_enable_before_restart() {
  local enable_line restart_line
  enable_line="$(grep -n '^enable p2pstream-agent$' "$SYSTEMCTL_LOG" | tail -n 1 | cut -d: -f1)"
  restart_line="$(grep -n '^restart p2pstream-agent$' "$SYSTEMCTL_LOG" | tail -n 1 | cut -d: -f1)"
  [[ -n "$enable_line" ]] || fail "systemctl enable was not called"
  [[ -n "$restart_line" ]] || fail "systemctl restart was not called"
  (( enable_line < restart_line )) || fail "systemctl enable should run before restart"
}

base64_value() {
  printf '%s' "$1" | base64 | tr -d '\n'
}

write_executable() {
  local path="$1"
  shift
  printf '%s\n' "$@" >"$path"
  chmod +x "$path"
}

setup_fixture() {
  TEST_DIR="$(mktemp -d)"
  FAKE_BIN="${TEST_DIR}/bin"
  CONFIG_DIR="${TEST_DIR}/etc/p2pstream"
  INSTALL_PATH="${TEST_DIR}/usr/local/bin/p2pstream"
  SYSTEMD_DIR="${TEST_DIR}/systemd"
  SYSTEMCTL_LOG="${TEST_DIR}/systemctl.log"
  COMMAND_LOG="${TEST_DIR}/commands.log"
  mkdir -p "$FAKE_BIN" "$CONFIG_DIR" "$(dirname "$INSTALL_PATH")" "$SYSTEMD_DIR"
  : >"$SYSTEMCTL_LOG"
  : >"$COMMAND_LOG"

  write_executable "${FAKE_BIN}/uname" \
    '#!/usr/bin/env bash' \
    'case "${1:-}" in' \
    '  -s) printf "Linux\n" ;;' \
    '  -m) printf "x86_64\n" ;;' \
    '  *) printf "Linux\n" ;;' \
    'esac'

  write_executable "${FAKE_BIN}/id" \
    '#!/usr/bin/env bash' \
    'if [[ "${1:-}" == "-u" && $# -eq 1 ]]; then printf "0\n"; exit 0; fi' \
    'if [[ "${1:-}" == "-u" && "${2:-}" == "p2pstream" ]]; then' \
    '  [[ "${FAKE_USER_EXISTS:-}" == "1" ]] && { printf "123\n"; exit 0; }' \
    '  exit 1' \
    'fi' \
    'exit 1'

  write_executable "${FAKE_BIN}/getent" \
    '#!/usr/bin/env bash' \
    'if [[ "${1:-}" == "group" && "${2:-}" == "p2pstream" ]]; then' \
    '  [[ "${FAKE_GROUP_EXISTS:-}" == "1" ]] && exit 0' \
    '  exit 1' \
    'fi' \
    'exit 1'

  for command in groupadd useradd userdel groupdel; do
    write_executable "${FAKE_BIN}/${command}" \
      '#!/usr/bin/env bash' \
      'printf "%s" "$(basename "$0")" >>"${FAKE_COMMAND_LOG:?}"' \
      'printf " %q" "$@" >>"${FAKE_COMMAND_LOG:?}"' \
      'printf "\n" >>"${FAKE_COMMAND_LOG:?}"'
  done

  write_executable "${FAKE_BIN}/systemctl" \
    '#!/usr/bin/env bash' \
    'printf "%s" "$*" >>"${FAKE_SYSTEMCTL_LOG:?}"' \
    'printf "\n" >>"${FAKE_SYSTEMCTL_LOG:?}"' \
    'if [[ "${1:-}" == "--version" ]]; then printf "systemd 252\n"; exit 0; fi' \
    'if [[ "${FAKE_SYSTEMCTL_FAIL_RESTART:-}" == "1" && "${1:-}" == "restart" ]]; then exit 1; fi' \
    'exit 0'

  write_executable "${FAKE_BIN}/install" \
    '#!/usr/bin/env bash' \
    'set -Eeuo pipefail' \
    'mode=""' \
    'make_dir=false' \
    'args=()' \
    'while (($#)); do' \
    '  case "$1" in' \
    '    -d) make_dir=true; shift ;;' \
    '    -m) mode="$2"; shift 2 ;;' \
    '    -o|-g) shift 2 ;;' \
    '    *) args+=("$1"); shift ;;' \
    '  esac' \
    'done' \
    'if [[ "$make_dir" == "true" ]]; then' \
    '  for dir in "${args[@]}"; do mkdir -p "$dir"; [[ -z "$mode" ]] || chmod "$mode" "$dir"; done' \
    'else' \
    '  src="${args[0]}"' \
    '  dst="${args[1]}"' \
    '  mkdir -p "$(dirname "$dst")"' \
    '  cp "$src" "$dst"' \
    '  [[ -z "$mode" ]] || chmod "$mode" "$dst"' \
    'fi'

  write_executable "${FAKE_BIN}/curl" \
    '#!/usr/bin/env bash' \
    'set -Eeuo pipefail' \
    'output=""' \
    'url=""' \
    'while (($#)); do' \
    '  case "$1" in' \
    '    -o) output="$2"; shift 2 ;;' \
    '    -*) shift ;;' \
    '    *) url="$1"; shift ;;' \
    '  esac' \
    'done' \
    'if [[ "$url" == *"api.github.com"* ]]; then printf "{\"tag_name\":\"v1.2.3\"}\n"; exit 0; fi' \
    '[[ -n "$output" ]] || { printf "missing -o\n" >&2; exit 1; }' \
    'case "$url" in' \
    '  *checksums.txt) printf "0000000000000000000000000000000000000000000000000000000000000000  p2pstream_v1.2.3_linux_amd64.tar.gz\n" >"$output" ;;' \
    '  *) printf "archive" >"$output" ;;' \
    'esac'

  write_executable "${FAKE_BIN}/sha256sum" \
    '#!/usr/bin/env bash' \
    'cat >/dev/null' \
    'exit 0'

  write_executable "${FAKE_BIN}/tar" \
    '#!/usr/bin/env bash' \
    'set -Eeuo pipefail' \
    'dest=""' \
    'while (($#)); do' \
    '  case "$1" in' \
    '    -C) dest="$2"; shift 2 ;;' \
    '    *) shift ;;' \
    '  esac' \
    'done' \
    '[[ -n "$dest" ]] || { printf "missing -C\n" >&2; exit 1; }' \
    'mkdir -p "$dest"' \
    'printf "#!/usr/bin/env sh\n" >"${dest}/p2pstream"' \
    'printf "printf p2pstream\n" >>"${dest}/p2pstream"' \
    'chmod +x "${dest}/p2pstream"'
}

run_installer() {
  env -i \
    PATH="${FAKE_BIN}:${REAL_PATH}" \
    FAKE_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" \
    FAKE_COMMAND_LOG="$COMMAND_LOG" \
    P2PSTREAM_CONFIG_DIR="$CONFIG_DIR" \
    P2PSTREAM_INSTALL_PATH="$INSTALL_PATH" \
    P2PSTREAM_SYSTEMD_DIR="$SYSTEMD_DIR" \
    P2PSTREAM_REPOSITORY="ExampleUser/p2pstream" \
    P2PSTREAM_VERSION="v1.2.3" \
    "$@" \
    bash "$INSTALL_SCRIPT"
}

run_uninstaller() {
  env -i \
    PATH="${FAKE_BIN}:${REAL_PATH}" \
    FAKE_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" \
    FAKE_COMMAND_LOG="$COMMAND_LOG" \
    FAKE_USER_EXISTS="1" \
    FAKE_GROUP_EXISTS="1" \
    P2PSTREAM_CONFIG_DIR="$CONFIG_DIR" \
    P2PSTREAM_INSTALL_PATH="$INSTALL_PATH" \
    P2PSTREAM_SYSTEMD_DIR="$SYSTEMD_DIR" \
    P2PSTREAM_UNINSTALL_CONFIRM="full-purge" \
    "$@" \
    bash "$UNINSTALL_SCRIPT"
}

test_first_install() {
  setup_fixture
  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    MANAGEMENT_CA_PEM_BASE64="$(base64_value "CA-one")" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="token-one"

  assert_exists "$INSTALL_PATH"
  assert_exists "${CONFIG_DIR}/agent.env"
  assert_exists "${CONFIG_DIR}/management-ca.pem"
  assert_exists "${SYSTEMD_DIR}/p2pstream-agent.service"
  assert_contains "${CONFIG_DIR}/agent.env" "MANAGEMENT_URL=\"https://mgmt.example.test:8081\""
  assert_contains "${CONFIG_DIR}/agent.env" "MANAGEMENT_CA_FILE=\"${CONFIG_DIR}/management-ca.pem\""
  assert_contains "${CONFIG_DIR}/agent.env" "AGENT_ID=\"agent-one\""
  assert_contains "${CONFIG_DIR}/agent.env" "AGENT_TOKEN=\"token-one\""
  assert_contains "${CONFIG_DIR}/management-ca.pem" "CA-one"
  assert_contains "${SYSTEMD_DIR}/p2pstream-agent.service" "EnvironmentFile=${CONFIG_DIR}/agent.env"
  assert_systemctl_enable_before_restart
  assert_not_contains "$SYSTEMCTL_LOG" "enable --now"
}

test_reinstall_overwrites_token_and_ca() {
  setup_fixture
  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    MANAGEMENT_CA_PEM_BASE64="$(base64_value "old CA")" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="old-token"
  : >"$SYSTEMCTL_LOG"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    MANAGEMENT_CA_PEM_BASE64="$(base64_value "new CA")" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"

  assert_contains "${CONFIG_DIR}/agent.env" "AGENT_TOKEN=\"new-token\""
  assert_not_contains "${CONFIG_DIR}/agent.env" "old-token"
  assert_contains "${CONFIG_DIR}/management-ca.pem" "new CA"
  assert_not_contains "${CONFIG_DIR}/management-ca.pem" "old CA"
  assert_systemctl_enable_before_restart
  assert_not_contains "$SYSTEMCTL_LOG" "enable --now"
}

test_reinstall_without_ca_removes_stale_managed_ca() {
  setup_fixture
  printf 'stale CA\n' >"${CONFIG_DIR}/management-ca.pem"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="token-one"

  assert_absent "${CONFIG_DIR}/management-ca.pem"
  assert_not_contains "${CONFIG_DIR}/agent.env" "MANAGEMENT_CA_FILE"
}

test_validation_failures() {
  setup_fixture
  if run_installer MANAGEMENT_URL="https://mgmt.example.test:8081" AGENT_ID="agent-one" >/dev/null 2>"${TEST_DIR}/missing-token.err"; then
    fail "missing AGENT_TOKEN should fail"
  fi
  assert_contains "${TEST_DIR}/missing-token.err" "missing required environment variable: AGENT_TOKEN"

  if run_installer MANAGEMENT_URL="https://mgmt.example.test:8081" AGENT_ID="agent-one" AGENT_TOKEN="token-one" AGENT_TLS_CERT_FILE="/tmp/agent.crt.pem" >/dev/null 2>"${TEST_DIR}/partial-mtls.err"; then
    fail "partial mTLS configuration should fail"
  fi
  assert_contains "${TEST_DIR}/partial-mtls.err" "AGENT_TLS_CERT_FILE and AGENT_TLS_KEY_FILE must be set together"

  if run_installer MANAGEMENT_URL="http://mgmt.example.test:8081" AGENT_ID="agent-one" AGENT_TOKEN="token-one" >/dev/null 2>"${TEST_DIR}/http.err"; then
    fail "HTTP management URL without opt-in should fail"
  fi
  assert_contains "${TEST_DIR}/http.err" "refusing insecure MANAGEMENT_URL"
}

test_uninstall_full_purge() {
  setup_fixture
  mkdir -p "${SYSTEMD_DIR}/p2pstream-agent.service.d" "$CONFIG_DIR" "$(dirname "$INSTALL_PATH")"
  printf 'unit\n' >"${SYSTEMD_DIR}/p2pstream-agent.service"
  printf 'dropin\n' >"${SYSTEMD_DIR}/p2pstream-agent.service.d/override.conf"
  printf 'env\n' >"${CONFIG_DIR}/agent.env"
  printf 'binary\n' >"$INSTALL_PATH"

  run_uninstaller

  assert_absent "${SYSTEMD_DIR}/p2pstream-agent.service"
  assert_absent "${SYSTEMD_DIR}/p2pstream-agent.service.d"
  assert_absent "$CONFIG_DIR"
  assert_absent "$INSTALL_PATH"
  assert_contains "$SYSTEMCTL_LOG" "disable --now p2pstream-agent"
  assert_contains "$SYSTEMCTL_LOG" "daemon-reload"
  assert_contains "$SYSTEMCTL_LOG" "reset-failed p2pstream-agent"
  assert_contains "$COMMAND_LOG" "userdel p2pstream"
  assert_contains "$COMMAND_LOG" "groupdel p2pstream"
}

test_uninstall_dry_run_and_unsafe_paths() {
  setup_fixture
  mkdir -p "${SYSTEMD_DIR}/p2pstream-agent.service.d" "$CONFIG_DIR" "$(dirname "$INSTALL_PATH")"
  printf 'unit\n' >"${SYSTEMD_DIR}/p2pstream-agent.service"
  printf 'dropin\n' >"${SYSTEMD_DIR}/p2pstream-agent.service.d/override.conf"
  printf 'env\n' >"${CONFIG_DIR}/agent.env"
  printf 'binary\n' >"$INSTALL_PATH"

  run_uninstaller P2PSTREAM_UNINSTALL_DRY_RUN="true" >"${TEST_DIR}/dry-run.out"
  assert_exists "${SYSTEMD_DIR}/p2pstream-agent.service"
  assert_exists "${SYSTEMD_DIR}/p2pstream-agent.service.d"
  assert_exists "$CONFIG_DIR"
  assert_exists "$INSTALL_PATH"
  assert_contains "${TEST_DIR}/dry-run.out" "p2pstream-agent.service.d"

  if run_uninstaller P2PSTREAM_CONFIG_DIR="/" >/dev/null 2>"${TEST_DIR}/unsafe.err"; then
    fail "unsafe config dir should fail"
  fi
  assert_contains "${TEST_DIR}/unsafe.err" "refusing to remove unsafe P2PSTREAM_CONFIG_DIR"
}

run_test() {
  local name="$1"
  shift
  TEST_DIR=""
  printf 'Running %s...\n' "$name"
  "$@"
  rm -rf "$TEST_DIR"
  TEST_DIR=""
}

run_test "first install" test_first_install
run_test "reinstall overwrites token and CA" test_reinstall_overwrites_token_and_ca
run_test "reinstall without CA removes stale managed CA" test_reinstall_without_ca_removes_stale_managed_ca
run_test "validation failures" test_validation_failures
run_test "uninstall full purge" test_uninstall_full_purge
run_test "uninstall dry-run and unsafe paths" test_uninstall_dry_run_and_unsafe_paths

printf 'agent lifecycle tests passed.\n'
