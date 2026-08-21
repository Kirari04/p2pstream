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

assert_line_count() {
  local path="$1"
  local text="$2"
  local expected="$3"
  local actual
  actual="$(grep -Fxc -- "$text" "$path" || true)"
  [[ "$actual" == "$expected" ]] \
    || fail "expected ${path} to contain ${expected} exact lines matching ${text}, found ${actual}"
}

assert_empty() {
  [[ ! -s "$1" ]] || fail "expected path to be empty: $1"
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
  AGENT_STATE_DIR="${TEST_DIR}/var/lib/p2pstream-agent"
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
    'printf "curl %s\n" "$url" >>"${FAKE_COMMAND_LOG:?}"' \
    'case "$url" in' \
    '  */staging/checksums.txt) printf "0000000000000000000000000000000000000000000000000000000000000000  p2pstream_staging_linux_amd64.tar.gz\n" >"$output" ;;' \
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
    P2PSTREAM_AGENT_STATE_DIR="$AGENT_STATE_DIR" \
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
    P2PSTREAM_AGENT_STATE_DIR="$AGENT_STATE_DIR" \
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
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    TUNNEL_MAX_CONCURRENT_REQUESTS="32" \
    AGENT_ALLOW_TARGETS="myapp.internal:443,10.0.5.0/24:8080" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="token-one"

  assert_exists "$INSTALL_PATH"
  assert_exists "${CONFIG_DIR}/agent.env"
  assert_exists "${CONFIG_DIR}/management-ca.pem"
  assert_exists "${AGENT_STATE_DIR}/management-ca.pem"
  assert_exists "${SYSTEMD_DIR}/p2pstream-agent.service"
  assert_contains "${CONFIG_DIR}/agent.env" "MANAGEMENT_URL=\"https://mgmt.example.test:8081\""
  assert_contains "${CONFIG_DIR}/agent.env" "MANAGEMENT_CA_FILE=\"${CONFIG_DIR}/management-ca.pem\""
  assert_contains "${CONFIG_DIR}/agent.env" "MANAGEMENT_TRUST_FILE=\"${AGENT_STATE_DIR}/management-ca.pem\""
  assert_contains "${CONFIG_DIR}/agent.env" "TUNNEL_MAX_STREAM_WINDOW_BYTES=\"4194304\""
  assert_contains "${CONFIG_DIR}/agent.env" "TUNNEL_MAX_CONCURRENT_REQUESTS=\"32\""
  assert_contains "${CONFIG_DIR}/agent.env" "AGENT_ALLOW_TARGETS=\"myapp.internal:443,10.0.5.0/24:8080\""
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
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    TUNNEL_MAX_CONCURRENT_REQUESTS="32" \
    AGENT_ALLOW_TARGETS="myapp.internal:443,10.0.5.0/24:8080" \
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
  assert_contains "${CONFIG_DIR}/agent.env" "TUNNEL_MAX_STREAM_WINDOW_BYTES=\"4194304\""
  assert_contains "${CONFIG_DIR}/agent.env" "TUNNEL_MAX_CONCURRENT_REQUESTS=\"32\""
  assert_contains "${CONFIG_DIR}/agent.env" "AGENT_ALLOW_TARGETS=\"myapp.internal:443,10.0.5.0/24:8080\""
  assert_contains "${CONFIG_DIR}/management-ca.pem" "new CA"
  assert_not_contains "${CONFIG_DIR}/management-ca.pem" "old CA"
  assert_systemctl_enable_before_restart
  assert_not_contains "$SYSTEMCTL_LOG" "enable --now"
}

test_reinstall_preserves_effective_tunnel_limits() {
  setup_fixture
  {
    printf 'TUNNEL_MAX_STREAM_WINDOW_BYTES="1048576"\n'
    printf 'TUNNEL_MAX_CONCURRENT_REQUESTS=8\n'
    printf '   TUNNEL_MAX_STREAM_WINDOW_BYTES=0004194304\n'
    printf '\tTUNNEL_MAX_CONCURRENT_REQUESTS=\04700032\047'
  } >"${CONFIG_DIR}/agent.env"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"

  assert_line_count "${CONFIG_DIR}/agent.env" 'TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304"' 1
  assert_line_count "${CONFIG_DIR}/agent.env" 'TUNNEL_MAX_CONCURRENT_REQUESTS="32"' 1
  assert_not_contains "${CONFIG_DIR}/agent.env" '1048576'
  assert_not_contains "${CONFIG_DIR}/agent.env" 'TUNNEL_MAX_CONCURRENT_REQUESTS="8"'
}

test_reinstall_explicitly_clears_allow_targets() {
  setup_fixture
  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_ALLOW_TARGETS="myapp.internal:443" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="old-token"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_CLEAR_ALLOW_TARGETS="true" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"

  assert_not_contains "${CONFIG_DIR}/agent.env" "AGENT_ALLOW_TARGETS"
  assert_not_contains "${CONFIG_DIR}/agent.env" "AGENT_CLEAR_ALLOW_TARGETS"
  assert_contains "${CONFIG_DIR}/agent.env" "AGENT_TOKEN=\"new-token\""
}

test_installer_requires_explicit_unrestricted_agent_policy() {
  setup_fixture
  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_ALLOW_ANY_TARGET="true" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="token-one"
  assert_contains "${CONFIG_DIR}/agent.env" 'AGENT_ALLOW_ANY_TARGET="true"'
  assert_not_contains "${CONFIG_DIR}/agent.env" "AGENT_ALLOW_TARGETS"

  if run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_ALLOW_ANY_TARGET="true" \
    AGENT_ALLOW_TARGETS="app.internal:443" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="token-two" >/dev/null 2>"${TEST_DIR}/allow-any.err"; then
    fail "allow-any and allow-targets combination should fail"
  fi
  assert_contains "${TEST_DIR}/allow-any.err" "cannot be combined"
}

test_reinstall_explicit_false_revokes_unrestricted_policy() {
  setup_fixture
  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_ALLOW_ANY_TARGET="true" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="old-token"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_ALLOW_ANY_TARGET="false" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"

  assert_not_contains "${CONFIG_DIR}/agent.env" "AGENT_ALLOW_ANY_TARGET"
  assert_not_contains "${CONFIG_DIR}/agent.env" "AGENT_ALLOW_TARGETS"
  assert_contains "${CONFIG_DIR}/agent.env" "AGENT_TOKEN=\"new-token\""
}

test_reinstall_preserves_effective_last_allow_targets() {
  setup_fixture
  printf '%s\n' \
    'MANAGEMENT_URL="https://mgmt.example.test:8081"' \
    'AGENT_ALLOW_TARGETS=""' \
    '   AGENT_ALLOW_TARGETS="effective.internal:443"' \
    'AGENT_ID="agent-one"' \
    'AGENT_TOKEN="old-token"' >"${CONFIG_DIR}/agent.env"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"

  assert_contains "${CONFIG_DIR}/agent.env" 'AGENT_ALLOW_TARGETS="effective.internal:443"'
  assert_not_contains "${CONFIG_DIR}/agent.env" 'AGENT_ALLOW_TARGETS=""'
  [[ "$(grep -c '^AGENT_ALLOW_TARGETS=' "${CONFIG_DIR}/agent.env")" == "1" ]] \
    || fail "expected exactly one normalized AGENT_ALLOW_TARGETS assignment"
}

test_reinstall_preserves_effective_last_combined_policies() {
  setup_fixture
  printf '%s\n' \
    'TUNNEL_MAX_STREAM_WINDOW_BYTES="1048576"' \
    'AGENT_ALLOW_TARGETS="old.internal:443"' \
    'TUNNEL_MAX_CONCURRENT_REQUESTS=8' \
    '   TUNNEL_MAX_STREAM_WINDOW_BYTES=0004194304' \
    'AGENT_ALLOW_TARGETS="effective.internal:8443"' \
    "TUNNEL_MAX_CONCURRENT_REQUESTS='00032'" >"${CONFIG_DIR}/agent.env"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"

  assert_line_count "${CONFIG_DIR}/agent.env" 'TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304"' 1
  assert_line_count "${CONFIG_DIR}/agent.env" 'TUNNEL_MAX_CONCURRENT_REQUESTS="32"' 1
  assert_line_count "${CONFIG_DIR}/agent.env" 'AGENT_ALLOW_TARGETS="effective.internal:8443"' 1
  assert_not_contains "${CONFIG_DIR}/agent.env" 'old.internal:443'
  assert_not_contains "${CONFIG_DIR}/agent.env" '1048576'
}

test_explicit_tunnel_setting_bypasses_replaced_value() {
  setup_fixture
  {
    printf 'TUNNEL_MAX_STREAM_WINDOW_BYTES=not-a-number\n'
    printf 'TUNNEL_MAX_CONCURRENT_REQUESTS="00032"\n'
  } >"${CONFIG_DIR}/agent.env"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"

  assert_line_count "${CONFIG_DIR}/agent.env" 'TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304"' 1
  assert_line_count "${CONFIG_DIR}/agent.env" 'TUNNEL_MAX_CONCURRENT_REQUESTS="32"' 1
  assert_not_contains "${CONFIG_DIR}/agent.env" 'not-a-number'
}

test_ambiguous_tunnel_preservation_fails_before_mutation() {
  setup_fixture
  printf 'UNRELATED=continued\\\nTUNNEL_MAX_CONCURRENT_REQUESTS=32\n' >"${CONFIG_DIR}/agent.env"

  if run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token" \
    >/dev/null 2>"${TEST_DIR}/ambiguous.err"; then
    fail "ambiguous existing agent environment should fail"
  fi
  assert_contains "${TEST_DIR}/ambiguous.err" "unsupported multiline syntax in existing agent environment at line 1"
  assert_absent "$INSTALL_PATH"
  assert_empty "$COMMAND_LOG"

  printf 'TUNNEL_MAX_CONCURRENT_REQUESTS =32\n' >"${CONFIG_DIR}/agent.env"
  if run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token" \
    >/dev/null 2>"${TEST_DIR}/spaced-assignment.err"; then
    fail "unsupported whitespace in a tunnel setting assignment should fail"
  fi
  assert_contains "${TEST_DIR}/spaced-assignment.err" "unsupported syntax in existing agent environment at line 1"
  assert_contains "${CONFIG_DIR}/agent.env" "TUNNEL_MAX_CONCURRENT_REQUESTS =32"
  assert_absent "$INSTALL_PATH"
  assert_empty "$COMMAND_LOG"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    TUNNEL_MAX_CONCURRENT_REQUESTS="16" \
    AGENT_CLEAR_ALLOW_TARGETS="true" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"
  assert_line_count "${CONFIG_DIR}/agent.env" 'TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304"' 1
  assert_line_count "${CONFIG_DIR}/agent.env" 'TUNNEL_MAX_CONCURRENT_REQUESTS="16"' 1
}

test_unreadable_tunnel_settings_require_explicit_replacement() {
  setup_fixture
  printf 'TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304"\n' >"${CONFIG_DIR}/agent.env"
  chmod 0200 "${CONFIG_DIR}/agent.env"

  if run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token" \
    >/dev/null 2>"${TEST_DIR}/unreadable.err"; then
    fail "unreadable existing agent environment should fail"
  fi
  assert_contains "${TEST_DIR}/unreadable.err" "cannot safely read existing agent environment"
  assert_absent "$INSTALL_PATH"
  assert_empty "$COMMAND_LOG"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    TUNNEL_MAX_CONCURRENT_REQUESTS="16" \
    AGENT_CLEAR_ALLOW_TARGETS="true" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"
  assert_line_count "${CONFIG_DIR}/agent.env" 'TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304"' 1
  assert_line_count "${CONFIG_DIR}/agent.env" 'TUNNEL_MAX_CONCURRENT_REQUESTS="16"' 1
}

test_reinstall_preserves_unterminated_allow_targets_line() {
  setup_fixture
  printf '%s\n' \
    'MANAGEMENT_URL="https://mgmt.example.test:8081"' \
    'AGENT_ID="agent-one"' \
    'AGENT_TOKEN="old-token"' >"${CONFIG_DIR}/agent.env"
  printf '%s' 'AGENT_ALLOW_TARGETS="tail.internal:443"' >>"${CONFIG_DIR}/agent.env"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"

  assert_contains "${CONFIG_DIR}/agent.env" 'AGENT_ALLOW_TARGETS="tail.internal:443"'
}

test_reinstall_fails_closed_on_ambiguous_allow_targets() {
  setup_fixture
  printf '%s\n' \
    'MANAGEMENT_URL="https://mgmt.example.test:8081"' \
    'AGENT_ALLOW_TARGETS="first.internal:443,' \
    'second.internal:443"' \
    'AGENT_ID="agent-one"' \
    'AGENT_TOKEN="old-token"' >"${CONFIG_DIR}/agent.env"

  if run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    TUNNEL_MAX_CONCURRENT_REQUESTS="16" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token" \
    >"${TEST_DIR}/ambiguous-policy.out" 2>"${TEST_DIR}/ambiguous-policy.err"; then
    fail "ambiguous existing AGENT_ALLOW_TARGETS should fail closed"
  fi
  assert_contains "${TEST_DIR}/ambiguous-policy.err" "cannot safely preserve AGENT_ALLOW_TARGETS"
  assert_contains "${CONFIG_DIR}/agent.env" 'AGENT_ALLOW_TARGETS="first.internal:443,'
  assert_absent "$INSTALL_PATH"
  assert_not_contains "$COMMAND_LOG" "curl "
  assert_not_contains "$SYSTEMCTL_LOG" "restart p2pstream-agent"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    TUNNEL_MAX_CONCURRENT_REQUESTS="16" \
    AGENT_ALLOW_TARGETS="replacement.internal:443" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"
  assert_contains "${CONFIG_DIR}/agent.env" 'AGENT_ALLOW_TARGETS="replacement.internal:443"'
  assert_not_contains "${CONFIG_DIR}/agent.env" "second.internal:443"
}

test_reinstall_fails_closed_on_allow_targets_read_error() {
  setup_fixture
  printf '%s\n' \
    'MANAGEMENT_URL="https://mgmt.example.test:8081"' \
    'AGENT_ALLOW_TARGETS="preserved.internal:443"' \
    'AGENT_ID="agent-one"' \
    'AGENT_TOKEN="old-token"' >"${CONFIG_DIR}/agent.env"
  write_executable "${FAKE_BIN}/sed" \
    '#!/usr/bin/env bash' \
    'exit 1'

  if run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    TUNNEL_MAX_CONCURRENT_REQUESTS="16" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token" \
    >"${TEST_DIR}/policy-read.out" 2>"${TEST_DIR}/policy-read.err"; then
    fail "unreadable existing AGENT_ALLOW_TARGETS should fail closed"
  fi
  assert_contains "${TEST_DIR}/policy-read.err" "cannot safely read existing"
  assert_contains "${CONFIG_DIR}/agent.env" 'AGENT_ALLOW_TARGETS="preserved.internal:443"'
  assert_absent "$INSTALL_PATH"
  assert_not_contains "$COMMAND_LOG" "curl "
  assert_not_contains "$SYSTEMCTL_LOG" "restart p2pstream-agent"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_CLEAR_ALLOW_TARGETS="true" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"
  assert_not_contains "${CONFIG_DIR}/agent.env" "AGENT_ALLOW_TARGETS"
}

test_reinstall_rejects_unrelated_multiline_context() {
  setup_fixture
  printf '%s\n' \
    'MANAGEMENT_URL="https://mgmt.example.test:8081"' \
    'AGENT_ALLOW_TARGETS="restrictive.internal:443"' \
    'OTHER="continued\' \
    'AGENT_ALLOW_TARGETS=""' \
    '"' \
    'AGENT_ID="agent-one"' \
    'AGENT_TOKEN="old-token"' >"${CONFIG_DIR}/agent.env"

  if run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    TUNNEL_MAX_CONCURRENT_REQUESTS="16" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token" \
    >"${TEST_DIR}/continued-policy.out" 2>"${TEST_DIR}/continued-policy.err"; then
    fail "unrelated multiline context should fail before changing AGENT_ALLOW_TARGETS"
  fi
  assert_contains "${TEST_DIR}/continued-policy.err" "unsupported or multiline environment syntax"
  assert_contains "${CONFIG_DIR}/agent.env" 'AGENT_ALLOW_TARGETS="restrictive.internal:443"'
  assert_absent "$INSTALL_PATH"
  assert_not_contains "$COMMAND_LOG" "curl "
  assert_not_contains "$SYSTEMCTL_LOG" "restart p2pstream-agent"

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    TUNNEL_MAX_CONCURRENT_REQUESTS="16" \
    AGENT_ALLOW_TARGETS="replacement.internal:443" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"
  assert_contains "${CONFIG_DIR}/agent.env" 'AGENT_ALLOW_TARGETS="replacement.internal:443"'

  printf '%s\n' \
    'MANAGEMENT_URL="https://mgmt.example.test:8081"' \
    'AGENT_ALLOW_TARGETS="restrictive.internal:443"' \
    'OTHER=prefix"unsupported' \
    'AGENT_ID="agent-one"' \
    'AGENT_TOKEN="old-token"' >"${CONFIG_DIR}/agent.env"
  if run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    TUNNEL_MAX_CONCURRENT_REQUESTS="16" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token" \
    >"${TEST_DIR}/unquoted-policy.out" 2>"${TEST_DIR}/unquoted-policy.err"; then
    fail "quote characters in an unquoted environment value should fail closed"
  fi
  assert_contains "${TEST_DIR}/unquoted-policy.err" "unsupported or multiline environment syntax"
  assert_contains "${CONFIG_DIR}/agent.env" 'AGENT_ALLOW_TARGETS="restrictive.internal:443"'

  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    TUNNEL_MAX_STREAM_WINDOW_BYTES="4194304" \
    TUNNEL_MAX_CONCURRENT_REQUESTS="16" \
    AGENT_CLEAR_ALLOW_TARGETS="true" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="new-token"
  assert_not_contains "${CONFIG_DIR}/agent.env" "AGENT_ALLOW_TARGETS"
  assert_not_contains "${CONFIG_DIR}/agent.env" "OTHER="
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

test_staging_version_downloads_staging_asset() {
  setup_fixture
  run_installer \
    P2PSTREAM_VERSION="staging" \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="token-one"

  assert_contains "$COMMAND_LOG" "https://github.com/ExampleUser/p2pstream/releases/download/staging/p2pstream_staging_linux_amd64.tar.gz"
  assert_contains "$COMMAND_LOG" "https://github.com/ExampleUser/p2pstream/releases/download/staging/checksums.txt"
  assert_exists "$INSTALL_PATH"
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

  if run_installer P2PSTREAM_VERSION="nightly" MANAGEMENT_URL="https://mgmt.example.test:8081" AGENT_ID="agent-one" AGENT_TOKEN="token-one" >/dev/null 2>"${TEST_DIR}/version.err"; then
    fail "unsupported P2PSTREAM_VERSION should fail"
  fi
  assert_contains "${TEST_DIR}/version.err" "P2PSTREAM_VERSION must be latest, staging, or vX.Y.Z"

  if run_installer P2PSTREAM_AGENT_STATE_DIR="/" MANAGEMENT_URL="https://mgmt.example.test:8081" AGENT_ID="agent-one" AGENT_TOKEN="token-one" >/dev/null 2>"${TEST_DIR}/state-dir.err"; then
    fail "unsafe P2PSTREAM_AGENT_STATE_DIR should fail"
  fi
  assert_contains "${TEST_DIR}/state-dir.err" "P2PSTREAM_AGENT_STATE_DIR"

  if run_installer MANAGEMENT_URL="https://mgmt.example.test:8081" TUNNEL_MAX_STREAM_WINDOW_BYTES="262143" AGENT_ID="agent-one" AGENT_TOKEN="token-one" >/dev/null 2>"${TEST_DIR}/window.err"; then
    fail "undersized TUNNEL_MAX_STREAM_WINDOW_BYTES should fail"
  fi
  assert_contains "${TEST_DIR}/window.err" "TUNNEL_MAX_STREAM_WINDOW_BYTES must be at least 262144"

  if run_installer MANAGEMENT_URL="https://mgmt.example.test:8081" TUNNEL_MAX_STREAM_WINDOW_BYTES="67108864" TUNNEL_MAX_CONCURRENT_REQUESTS="9" AGENT_ID="agent-one" AGENT_TOKEN="token-one" >/dev/null 2>"${TEST_DIR}/aggregate.err"; then
    fail "oversized aggregate tunnel window should fail"
  fi
  assert_contains "${TEST_DIR}/aggregate.err" "TUNNEL_MAX_STREAM_WINDOW_BYTES times TUNNEL_MAX_CONCURRENT_REQUESTS must be at most 536870912"

  if run_installer MANAGEMENT_URL="https://mgmt.example.test:8081" AGENT_ALLOW_TARGETS="myapp.internal:443" AGENT_CLEAR_ALLOW_TARGETS="true" AGENT_ID="agent-one" AGENT_TOKEN="token-one" >/dev/null 2>"${TEST_DIR}/allow-targets.err"; then
    fail "conflicting allow-target inputs should fail"
  fi
  assert_contains "${TEST_DIR}/allow-targets.err" "AGENT_CLEAR_ALLOW_TARGETS=true cannot be combined with AGENT_ALLOW_TARGETS"
}

test_management_trust_repair() {
  setup_fixture
  run_installer \
    MANAGEMENT_URL="https://mgmt.example.test:8081" \
    MANAGEMENT_CA_PEM_BASE64="$(base64_value $'-----BEGIN CERTIFICATE-----\nold\n-----END CERTIFICATE-----\n')" \
    AGENT_ID="agent-one" \
    AGENT_TOKEN="token-one"
  printf '{"generation":1}\n' >"${AGENT_STATE_DIR}/management-ca.pem.state.json"
  : >"$SYSTEMCTL_LOG"

  run_installer \
    P2PSTREAM_REPAIR_TRUST="true" \
    MANAGEMENT_CA_PEM_BASE64="$(base64_value $'-----BEGIN CERTIFICATE-----\nnew\n-----END CERTIFICATE-----\n')"

  assert_contains "${AGENT_STATE_DIR}/management-ca.pem" "new"
  assert_not_contains "${AGENT_STATE_DIR}/management-ca.pem" "old"
  assert_absent "${AGENT_STATE_DIR}/management-ca.pem.state.json"
  assert_contains "$SYSTEMCTL_LOG" "restart p2pstream-agent"
}

test_uninstall_full_purge() {
  setup_fixture
  mkdir -p "${SYSTEMD_DIR}/p2pstream-agent.service.d" "$CONFIG_DIR" "$AGENT_STATE_DIR" "$(dirname "$INSTALL_PATH")"
  printf 'unit\n' >"${SYSTEMD_DIR}/p2pstream-agent.service"
  printf 'dropin\n' >"${SYSTEMD_DIR}/p2pstream-agent.service.d/override.conf"
  printf 'env\n' >"${CONFIG_DIR}/agent.env"
  printf 'trust\n' >"${AGENT_STATE_DIR}/management-ca.pem"
  printf 'binary\n' >"$INSTALL_PATH"

  run_uninstaller

  assert_absent "${SYSTEMD_DIR}/p2pstream-agent.service"
  assert_absent "${SYSTEMD_DIR}/p2pstream-agent.service.d"
  assert_absent "$CONFIG_DIR"
  assert_absent "$AGENT_STATE_DIR"
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
run_test "reinstall preserves effective tunnel limits" test_reinstall_preserves_effective_tunnel_limits
run_test "explicit tunnel setting bypasses replaced value" test_explicit_tunnel_setting_bypasses_replaced_value
run_test "ambiguous tunnel preservation fails before mutation" test_ambiguous_tunnel_preservation_fails_before_mutation
run_test "unreadable tunnel settings require explicit replacement" test_unreadable_tunnel_settings_require_explicit_replacement
run_test "reinstall explicitly clears allow targets" test_reinstall_explicitly_clears_allow_targets
run_test "installer requires explicit unrestricted agent policy" test_installer_requires_explicit_unrestricted_agent_policy
run_test "explicit false revokes unrestricted policy" test_reinstall_explicit_false_revokes_unrestricted_policy
run_test "reinstall preserves effective last allow targets" test_reinstall_preserves_effective_last_allow_targets
run_test "reinstall preserves effective last combined policies" test_reinstall_preserves_effective_last_combined_policies
run_test "reinstall preserves unterminated allow targets line" test_reinstall_preserves_unterminated_allow_targets_line
run_test "reinstall fails closed on ambiguous allow targets" test_reinstall_fails_closed_on_ambiguous_allow_targets
run_test "reinstall fails closed on allow targets read error" test_reinstall_fails_closed_on_allow_targets_read_error
run_test "reinstall rejects unrelated multiline context" test_reinstall_rejects_unrelated_multiline_context
run_test "reinstall without CA removes stale managed CA" test_reinstall_without_ca_removes_stale_managed_ca
run_test "staging version downloads staging asset" test_staging_version_downloads_staging_asset
run_test "validation failures" test_validation_failures
run_test "management trust repair" test_management_trust_repair
run_test "uninstall full purge" test_uninstall_full_purge
run_test "uninstall dry-run and unsafe paths" test_uninstall_dry_run_and_unsafe_paths

printf 'agent lifecycle tests passed.\n'
