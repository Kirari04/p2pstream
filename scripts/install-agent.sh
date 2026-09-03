#!/usr/bin/env bash
set -Eeuo pipefail

readonly DEFAULT_REPOSITORY="Kirari04/p2pstream"
readonly SERVICE_NAME="p2pstream-agent"
readonly CONFIG_DIR="${P2PSTREAM_CONFIG_DIR:-/etc/p2pstream}"
readonly ENV_FILE="${CONFIG_DIR}/agent.env"
readonly SYSTEMD_DIR="${P2PSTREAM_SYSTEMD_DIR:-/etc/systemd/system}"
readonly SERVICE_FILE="${SYSTEMD_DIR}/${SERVICE_NAME}.service"
readonly INSTALL_PATH="${P2PSTREAM_INSTALL_PATH:-/usr/local/bin/p2pstream}"
readonly AGENT_INSTALL_ROOT="${P2PSTREAM_AGENT_INSTALL_ROOT:-/opt/p2pstream-agent}"
readonly AGENT_SLOTS_DIR="${AGENT_INSTALL_ROOT}/slots"
readonly AGENT_CURRENT_PATH="${AGENT_INSTALL_ROOT}/current"
readonly UPDATER_RUNNER_DIR="${AGENT_INSTALL_ROOT}/updater"
readonly UPDATER_RUNNER_PATH="${UPDATER_RUNNER_DIR}/p2pstream"
readonly MANAGEMENT_CA_PEM_FILE="${CONFIG_DIR}/management-ca.pem"
readonly AGENT_STATE_DIR="${P2PSTREAM_AGENT_STATE_DIR:-/var/lib/p2pstream-agent}"
readonly MANAGEMENT_TRUST_FILE="${AGENT_STATE_DIR}/management-ca.pem"
readonly SERVICE_USER="p2pstream"
readonly SERVICE_GROUP="p2pstream"
readonly UPDATER_USER="p2pstream-updater"
readonly UPDATER_GROUP="p2pstream-updater"
readonly UPDATER_CONFIG_DIR="${P2PSTREAM_UPDATER_CONFIG_DIR:-/etc/p2pstream-updater}"
readonly UPDATER_STATE_DIR="${P2PSTREAM_UPDATER_STATE_DIR:-/var/lib/p2pstream-updater}"
readonly UPDATER_WORKER_DIR="${UPDATER_STATE_DIR}/worker"
readonly UPDATER_STAGING_DIR="${UPDATER_STATE_DIR}/staging"
readonly UPDATER_ROOT_DIR="${UPDATER_STATE_DIR}/root"
readonly UPDATER_FLOOR_FILE="${UPDATER_STATE_DIR}/floor.json"
readonly UPDATER_SERVICE_FILE="${SYSTEMD_DIR}/p2pstream-updater.service"
readonly UPDATER_TIMER_FILE="${SYSTEMD_DIR}/p2pstream-updater.timer"
readonly UPDATER_ACTIVATE_SERVICE_FILE="${SYSTEMD_DIR}/p2pstream-updater-activate.service"
readonly UPDATER_ACTIVATE_PATH_FILE="${SYSTEMD_DIR}/p2pstream-updater-activate.path"
readonly MIN_TUNNEL_MAX_STREAM_WINDOW_BYTES="262144"
readonly MAX_TUNNEL_MAX_STREAM_WINDOW_BYTES="67108864"
readonly MIN_TUNNEL_MAX_CONCURRENT_REQUESTS="1"
readonly MAX_TUNNEL_MAX_CONCURRENT_REQUESTS="2048"
INSTALL_TMP_DIR=""
EXISTING_AGENT_ENV_ASSIGNMENT=""

fail() {
  printf 'p2pstream agent install failed: %s\n' "$*" >&2
  exit 1
}

cleanup_tmp_dir() {
  if [[ -n "$INSTALL_TMP_DIR" ]]; then
    rm -rf "$INSTALL_TMP_DIR"
  fi
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

require_env() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    fail "missing required environment variable: ${name}"
  fi
}

require_readable_file() {
  local name="$1"
  local path="$2"
  if [[ ! -f "$path" || ! -r "$path" ]]; then
    fail "${name} must reference a readable file: ${path}"
  fi
}

require_safe_agent_state_dir() {
	[[ "$AGENT_STATE_DIR" =~ ^/[A-Za-z0-9._/-]+$ ]] \
		|| fail "P2PSTREAM_AGENT_STATE_DIR must be an absolute path containing only letters, numbers, dots, underscores, dashes, and slashes"
	[[ "/${AGENT_STATE_DIR#/}/" != *"/../"* ]] \
		|| fail "P2PSTREAM_AGENT_STATE_DIR must not contain a parent-directory segment"
	[[ "/${AGENT_STATE_DIR#/}/" != *"/./"* && "$AGENT_STATE_DIR" != *"//"* ]] \
		|| fail "P2PSTREAM_AGENT_STATE_DIR must not contain dot or repeated-slash segments"
  case "$AGENT_STATE_DIR" in
    ""|"/"|"/var"|"/var/lib"|"/etc"|"/usr"|"/usr/local")
      fail "refusing unsafe P2PSTREAM_AGENT_STATE_DIR: ${AGENT_STATE_DIR}"
      ;;
  esac
}

single_line() {
  printf '%s' "$1" | tr -d '\r\n'
}

systemd_env_value() {
  local value
  value="$(single_line "$1")"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '"%s"' "$value"
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)
      printf 'amd64'
      ;;
    aarch64|arm64)
      printf 'arm64'
      ;;
    *)
      fail "unsupported architecture: $(uname -m)"
      ;;
  esac
}

normalize_decimal() {
  local value="$1"
  while [[ ${#value} -gt 1 && "$value" == 0* ]]; do
    value="${value:1}"
  done
  printf '%s' "$value"
}

require_local_pinned_installer() {
  local source_path="${BASH_SOURCE[0]:-}"
  [[ "$source_path" == /* && -f "$source_path" && ! -L "$source_path" ]] \
    || fail "run a locally supplied, pinned installer file by absolute path; piping remote installer code into root is forbidden"
}

validate_local_agent_binary() {
  local binary_path="${P2PSTREAM_AGENT_BINARY_FILE:-}"
  require_env P2PSTREAM_AGENT_BINARY_FILE
  [[ "$binary_path" == /* && "$binary_path" != *"//"* && "/${binary_path#/}/" != *"/../"* && "/${binary_path#/}/" != *"/./"* ]] \
    || fail "P2PSTREAM_AGENT_BINARY_FILE must be a clean absolute path"
  [[ -f "$binary_path" && -r "$binary_path" && ! -L "$binary_path" ]] \
    || fail "P2PSTREAM_AGENT_BINARY_FILE must be a readable, non-symlink regular file"
  local binary_size
  binary_size="$(stat -c '%s' "$binary_path")" \
    || fail "could not inspect P2PSTREAM_AGENT_BINARY_FILE"
  [[ "$binary_size" =~ ^[0-9]+$ ]] && (( binary_size > 0 && binary_size <= 536870912 )) \
    || fail "P2PSTREAM_AGENT_BINARY_FILE must contain 1 to 536870912 bytes"
}

trim_leading_agent_env_whitespace() {
  local value="$1"
  while [[ "$value" == " "* || "$value" == $'\t'* || "$value" == $'\r'* ]]; do
    value="${value:1}"
  done
  printf '%s' "$value"
}

decimal_is_at_least() {
  local value="$1"
  local minimum="$2"
  if [[ ${#value} -ne ${#minimum} ]]; then
    [[ ${#value} -gt ${#minimum} ]]
    return
  fi
  [[ "$value" == "$minimum" || "$value" > "$minimum" ]]
}

decimal_is_at_most() {
  local value="$1"
  local maximum="$2"
  if [[ ${#value} -ne ${#maximum} ]]; then
    [[ ${#value} -lt ${#maximum} ]]
    return
  fi
  [[ "$value" == "$maximum" || "$value" < "$maximum" ]]
}

parse_numeric_env_value() {
  local raw="$1"
  local value
  if [[ "$raw" =~ ^[[:space:]]*([0-9]+)[[:space:]]*$ ]]; then
    value="${BASH_REMATCH[1]}"
  elif [[ "$raw" =~ ^[[:space:]]*\"([0-9]+)\"[[:space:]]*$ ]]; then
    value="${BASH_REMATCH[1]}"
  elif [[ "$raw" =~ ^[[:space:]]*\'([0-9]+)\'[[:space:]]*$ ]]; then
    value="${BASH_REMATCH[1]}"
  else
    return 1
  fi
  normalize_decimal "$value"
}

validate_tunnel_setting() {
  local name="$1"
  local raw="$2"
  local minimum="$3"
  local maximum="$4"
  local value
  value="$(parse_numeric_env_value "$raw")" \
    || fail "${name} must be a single quoted or unquoted decimal integer"
  decimal_is_at_least "$value" "$minimum" \
    || fail "${name} must be at least ${minimum}"
  decimal_is_at_most "$value" "$maximum" \
    || fail "${name} must be at most ${maximum}"
  printf '%s' "$value"
}

env_assignment_is_single_line() {
  local line="$1"
  local state="unquoted"
  local index character
  for ((index = 0; index < ${#line}; index++)); do
    character="${line:index:1}"
    case "$state:$character" in
      unquoted:\\|double:\\)
        index=$((index + 1))
        ((index < ${#line})) || return 1
        ;;
      unquoted:\')
        state="single"
        ;;
      single:\')
        state="unquoted"
        ;;
      unquoted:\")
        state="double"
        ;;
      double:\")
        state="unquoted"
        ;;
    esac
  done
  [[ "$state" == "unquoted" ]]
}

load_existing_tunnel_settings() {
  local need_window="$1"
  local need_concurrency="$2"
  local line name raw
  local line_number=0
  local window_seen="false"
  local concurrency_seen="false"
  local window_raw=""
  local concurrency_raw=""
  local -a lines=()

  [[ -e "$ENV_FILE" || -L "$ENV_FILE" ]] || return 0
  if [[ ! -f "$ENV_FILE" || ! -r "$ENV_FILE" ]]; then
    fail "cannot safely read existing agent environment: ${ENV_FILE}"
  fi
  mapfile -t lines <"$ENV_FILE" \
    || fail "could not read existing agent environment: ${ENV_FILE}"

  for line in "${lines[@]}"; do
    line_number=$((line_number + 1))
    if [[ "$line" =~ ^[[:space:]]*$ || "$line" =~ ^[[:space:]]*# ]]; then
      continue
    fi
    env_assignment_is_single_line "$line" \
      || fail "unsupported multiline syntax in existing agent environment at line ${line_number}"
    if [[ ! "$line" =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; then
      fail "unsupported syntax in existing agent environment at line ${line_number}"
    fi
    name="${BASH_REMATCH[1]}"
    raw="${BASH_REMATCH[2]}"
    case "$name" in
      TUNNEL_MAX_STREAM_WINDOW_BYTES)
        if [[ "$need_window" == "true" ]]; then
          window_seen="true"
          window_raw="$raw"
        fi
        ;;
      TUNNEL_MAX_CONCURRENT_REQUESTS)
        if [[ "$need_concurrency" == "true" ]]; then
          concurrency_seen="true"
          concurrency_raw="$raw"
        fi
        ;;
    esac
  done

  if [[ "$window_seen" == "true" ]]; then
    TUNNEL_MAX_STREAM_WINDOW_BYTES="$(validate_tunnel_setting \
      TUNNEL_MAX_STREAM_WINDOW_BYTES "$window_raw" \
      "$MIN_TUNNEL_MAX_STREAM_WINDOW_BYTES" "$MAX_TUNNEL_MAX_STREAM_WINDOW_BYTES")"
  fi
  if [[ "$concurrency_seen" == "true" ]]; then
    TUNNEL_MAX_CONCURRENT_REQUESTS="$(validate_tunnel_setting \
      TUNNEL_MAX_CONCURRENT_REQUESTS "$concurrency_raw" \
      "$MIN_TUNNEL_MAX_CONCURRENT_REQUESTS" "$MAX_TUNNEL_MAX_CONCURRENT_REQUESTS")"
  fi
}

preflight_tunnel_settings() {
  local need_window="false"
  local need_concurrency="false"

  if [[ -n "${TUNNEL_MAX_STREAM_WINDOW_BYTES:-}" ]]; then
    TUNNEL_MAX_STREAM_WINDOW_BYTES="$(validate_tunnel_setting \
      TUNNEL_MAX_STREAM_WINDOW_BYTES "$TUNNEL_MAX_STREAM_WINDOW_BYTES" \
      "$MIN_TUNNEL_MAX_STREAM_WINDOW_BYTES" "$MAX_TUNNEL_MAX_STREAM_WINDOW_BYTES")"
  else
    need_window="true"
  fi
  if [[ -n "${TUNNEL_MAX_CONCURRENT_REQUESTS:-}" ]]; then
    TUNNEL_MAX_CONCURRENT_REQUESTS="$(validate_tunnel_setting \
      TUNNEL_MAX_CONCURRENT_REQUESTS "$TUNNEL_MAX_CONCURRENT_REQUESTS" \
      "$MIN_TUNNEL_MAX_CONCURRENT_REQUESTS" "$MAX_TUNNEL_MAX_CONCURRENT_REQUESTS")"
  else
    need_concurrency="true"
  fi

  if [[ "$need_window" == "true" || "$need_concurrency" == "true" ]]; then
    load_existing_tunnel_settings "$need_window" "$need_concurrency"
  fi

}

write_optional_agent_env() {
  local name="$1"
  local value="$2"
  if [[ -n "$value" ]]; then
    printf '%s=%s\n' "$name" "$(systemd_env_value "$value")"
  fi
}

agent_env_value_is_supported_single_line() {
  local value="$1"
  local length="${#value}"
  local index=0
  local char

  while (( index < length )); do
    char="${value:index:1}"
    if [[ "$char" != " " && "$char" != $'\t' && "$char" != $'\r' ]]; then
      break
    fi
    index=$((index + 1))
  done
  if (( index == length )); then
    return 0
  fi

  char="${value:index:1}"
  if [[ "$char" == "'" ]]; then
    index=$((index + 1))
    while (( index < length )) && [[ "${value:index:1}" != "'" ]]; do
      index=$((index + 1))
    done
    if (( index == length )); then
      return 1
    fi
    index=$((index + 1))
    while (( index < length )); do
      char="${value:index:1}"
      if [[ "$char" != " " && "$char" != $'\t' && "$char" != $'\r' ]]; then
        return 1
      fi
      index=$((index + 1))
    done
    return 0
  fi

  if [[ "$char" == '"' ]]; then
    index=$((index + 1))
    while (( index < length )); do
      char="${value:index:1}"
      if [[ "$char" == $'\\' ]]; then
        index=$((index + 1))
        if (( index == length )); then
          return 1
        fi
        index=$((index + 1))
        continue
      fi
      if [[ "$char" == '"' ]]; then
        index=$((index + 1))
        while (( index < length )); do
          char="${value:index:1}"
          if [[ "$char" != " " && "$char" != $'\t' && "$char" != $'\r' ]]; then
            return 1
          fi
          index=$((index + 1))
        done
        return 0
      fi
      index=$((index + 1))
    done
    return 1
  fi

  while (( index < length )); do
    char="${value:index:1}"
    if [[ "$char" == "'" || "$char" == '"' ]]; then
      return 1
    fi
    index=$((index + 1))
  done

  local trailing_backslashes=0
  index=$((length - 1))
  while (( index >= 0 )) && [[ "${value:index:1}" == $'\\' ]]; do
    trailing_backslashes=$((trailing_backslashes + 1))
    index=$((index - 1))
  done
  if (( trailing_backslashes % 2 != 0 )); then
    return 1
  fi
  return 0
}

load_existing_agent_env_assignment() {
  local name="$1"
  local contents
  local line
  local trimmed
  local assignment_name
  local value

  EXISTING_AGENT_ENV_ASSIGNMENT=""
  if [[ ! -e "$ENV_FILE" && ! -L "$ENV_FILE" ]]; then
    return 0
  fi
  if [[ ! -f "$ENV_FILE" || ! -r "$ENV_FILE" ]]; then
    fail "cannot safely read existing ${ENV_FILE} to preserve ${name}; provide ${name} or set AGENT_CLEAR_ALLOW_TARGETS=true"
  fi
  if ! contents="$(sed -n 'p' "$ENV_FILE")"; then
    fail "cannot safely read existing ${ENV_FILE} to preserve ${name}; provide ${name} or set AGENT_CLEAR_ALLOW_TARGETS=true"
  fi

  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    trimmed="$(trim_leading_agent_env_whitespace "$line")"
    if [[ -z "$trimmed" || "$trimmed" == \#* || "$trimmed" == \;* ]]; then
      continue
    fi
    if [[ "$trimmed" != *=* ]]; then
      fail "cannot safely preserve ${name} from ${ENV_FILE}: unsupported or multiline environment syntax; provide ${name} or set AGENT_CLEAR_ALLOW_TARGETS=true"
    fi
    assignment_name="${trimmed%%=*}"
    value="${trimmed#*=}"
    if [[ ! "$assignment_name" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      fail "cannot safely preserve ${name} from ${ENV_FILE}: unsupported or multiline environment syntax; provide ${name} or set AGENT_CLEAR_ALLOW_TARGETS=true"
    fi
    if ! agent_env_value_is_supported_single_line "$value"; then
      fail "cannot safely preserve ${name} from ${ENV_FILE}: unsupported or multiline environment syntax; provide ${name} or set AGENT_CLEAR_ALLOW_TARGETS=true"
    fi
    if [[ "$assignment_name" == "$name" ]]; then
      EXISTING_AGENT_ENV_ASSIGNMENT="${name}=${value}"
    fi
  done <<<"$contents"
}

validate_allow_target_inputs() {
  case "${AGENT_CLEAR_ALLOW_TARGETS:-false}" in
    true|false) ;;
    *) fail "AGENT_CLEAR_ALLOW_TARGETS must be true or false" ;;
  esac
  if [[ "${AGENT_CLEAR_ALLOW_TARGETS:-false}" == "true" && -n "${AGENT_ALLOW_TARGETS:-}" ]]; then
    fail "AGENT_CLEAR_ALLOW_TARGETS=true cannot be combined with AGENT_ALLOW_TARGETS"
  fi
  case "${AGENT_ALLOW_ANY_TARGET:-false}" in
    true|false) ;;
    *) fail "AGENT_ALLOW_ANY_TARGET must be true or false" ;;
  esac
  if [[ "${AGENT_CLEAR_ALLOW_TARGETS:-false}" == "true" && "${AGENT_ALLOW_ANY_TARGET:-false}" == "true" ]]; then
    fail "AGENT_CLEAR_ALLOW_TARGETS=true cannot be combined with AGENT_ALLOW_ANY_TARGET=true"
  fi
  if [[ -n "${AGENT_ALLOW_TARGETS:-}" && "${AGENT_ALLOW_ANY_TARGET:-false}" == "true" ]]; then
    fail "AGENT_ALLOW_ANY_TARGET=true cannot be combined with AGENT_ALLOW_TARGETS"
  fi
}

prepare_allow_target_preservation() {
  EXISTING_AGENT_ALLOW_TARGETS_ASSIGNMENT=""
  EXISTING_AGENT_ALLOW_ANY_TARGET_ASSIGNMENT=""
  # An explicitly supplied false is a policy change: it must revoke a
  # previously persisted unrestricted setting rather than be treated as unset.
  if [[ -z "${AGENT_ALLOW_TARGETS:-}" && ! -v AGENT_ALLOW_ANY_TARGET && "${AGENT_CLEAR_ALLOW_TARGETS:-false}" != "true" ]]; then
    load_existing_agent_env_assignment AGENT_ALLOW_TARGETS
    EXISTING_AGENT_ALLOW_TARGETS_ASSIGNMENT="$EXISTING_AGENT_ENV_ASSIGNMENT"
    load_existing_agent_env_assignment AGENT_ALLOW_ANY_TARGET
    EXISTING_AGENT_ALLOW_ANY_TARGET_ASSIGNMENT="$EXISTING_AGENT_ENV_ASSIGNMENT"
    if [[ -n "$EXISTING_AGENT_ALLOW_TARGETS_ASSIGNMENT" && -n "$EXISTING_AGENT_ALLOW_ANY_TARGET_ASSIGNMENT" ]]; then
      fail "cannot preserve conflicting AGENT_ALLOW_TARGETS and AGENT_ALLOW_ANY_TARGET assignments from ${ENV_FILE}; provide an explicit replacement policy"
    fi
  fi
}

write_agent_env() {
  local tmp_file="$1"
  local preserved_allow_targets="$EXISTING_AGENT_ALLOW_TARGETS_ASSIGNMENT"
  local preserved_allow_any_target="$EXISTING_AGENT_ALLOW_ANY_TARGET_ASSIGNMENT"
  {
    printf 'MANAGEMENT_URL=%s\n' "$(systemd_env_value "$MANAGEMENT_URL")"
    if [[ -n "${MANAGEMENT_CA_FILE:-}" ]]; then
      printf 'MANAGEMENT_CA_FILE=%s\n' "$(systemd_env_value "$MANAGEMENT_CA_FILE")"
    fi
    printf 'MANAGEMENT_TRUST_FILE=%s\n' "$(systemd_env_value "$MANAGEMENT_TRUST_FILE")"
    if [[ -n "${AGENT_TLS_CERT_FILE:-}" ]]; then
      printf 'AGENT_TLS_CERT_FILE=%s\n' "$(systemd_env_value "$AGENT_TLS_CERT_FILE")"
    fi
    if [[ -n "${AGENT_TLS_KEY_FILE:-}" ]]; then
      printf 'AGENT_TLS_KEY_FILE=%s\n' "$(systemd_env_value "$AGENT_TLS_KEY_FILE")"
    fi
    if [[ "${AGENT_ALLOW_INSECURE_MANAGEMENT:-}" == "true" ]]; then
      printf 'AGENT_ALLOW_INSECURE_MANAGEMENT="true"\n'
    fi
    write_optional_agent_env TUNNEL_MAX_STREAM_WINDOW_BYTES "${TUNNEL_MAX_STREAM_WINDOW_BYTES:-}"
    write_optional_agent_env TUNNEL_MAX_CONCURRENT_REQUESTS "${TUNNEL_MAX_CONCURRENT_REQUESTS:-}"
    if [[ -n "${AGENT_ALLOW_TARGETS:-}" ]]; then
      printf 'AGENT_ALLOW_TARGETS=%s\n' "$(systemd_env_value "$AGENT_ALLOW_TARGETS")"
    elif [[ -n "$preserved_allow_targets" ]]; then
      printf '%s\n' "$preserved_allow_targets"
    fi
    if [[ "${AGENT_ALLOW_ANY_TARGET:-false}" == "true" ]]; then
      printf 'AGENT_ALLOW_ANY_TARGET="true"\n'
    elif [[ -n "$preserved_allow_any_target" ]]; then
      printf '%s\n' "$preserved_allow_any_target"
    fi
    printf 'AGENT_ID=%s\n' "$(systemd_env_value "$AGENT_ID")"
    printf 'AGENT_TOKEN=%s\n' "$(systemd_env_value "$AGENT_TOKEN")"
  } >"$tmp_file"
}

write_service_file() {
  local tmp_file="$1"
  cat >"$tmp_file" <<EOF
[Unit]
Description=p2pstream agent
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=${ENV_FILE}
ExecStart=${INSTALL_PATH} agent
Restart=always
RestartSec=5s
User=${SERVICE_USER}
Group=${SERVICE_GROUP}
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${AGENT_STATE_DIR}

[Install]
WantedBy=multi-user.target
EOF
}

ensure_service_user() {
  if ! getent group "$SERVICE_GROUP" >/dev/null 2>&1; then
    groupadd --system "$SERVICE_GROUP"
  fi
  if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
    useradd --system --gid "$SERVICE_GROUP" --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
  fi
}

managed_updates_requested() {
  [[ "${P2PSTREAM_ENABLE_MANAGED_UPDATES:-false}" == "true" ]]
}

is_exact_semver() {
	local version="$1" prerelease identifier
	local -a identifiers
	(( ${#version} <= 96 )) || return 1
	[[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]] || return 1
	if [[ "$version" == *-* ]]; then
		prerelease="${version#*-}"
		IFS=. read -r -a identifiers <<<"$prerelease"
		for identifier in "${identifiers[@]}"; do
			if [[ "$identifier" =~ ^[0-9]+$ && ${#identifier} -gt 1 && "$identifier" == 0* ]]; then
				return 1
			fi
		done
	fi
	return 0
}

validate_managed_update_inputs() {
  case "${P2PSTREAM_ENABLE_MANAGED_UPDATES:-false}" in
    true|false) ;;
    *) fail "P2PSTREAM_ENABLE_MANAGED_UPDATES must be true or false" ;;
  esac
  if ! managed_updates_requested; then
    return
  fi
  require_env P2PSTREAM_UPDATER_ENROLLMENT_TOKEN
  require_env P2PSTREAM_AGENT_UPDATE_ROOT_BASE64
  require_env P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64
  require_env P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID
  require_env P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH
	require_env P2PSTREAM_AGENT_UPDATE_CHANNEL
	[[ "$P2PSTREAM_AGENT_UPDATE_CHANNEL" == "stable" || "$P2PSTREAM_AGENT_UPDATE_CHANNEL" == "staging" ]] \
		|| fail "P2PSTREAM_AGENT_UPDATE_CHANNEL must be stable or staging"
  [[ "$P2PSTREAM_UPDATER_ENROLLMENT_TOKEN" != *$'\n'* && "$P2PSTREAM_UPDATER_ENROLLMENT_TOKEN" != *$'\r'* ]] \
    || fail "P2PSTREAM_UPDATER_ENROLLMENT_TOKEN must be a single line"
  (( ${#P2PSTREAM_UPDATER_ENROLLMENT_TOKEN} <= 4096 )) \
    || fail "P2PSTREAM_UPDATER_ENROLLMENT_TOKEN is too long"
  printf '%s' "$P2PSTREAM_AGENT_UPDATE_ROOT_BASE64" | base64 -d >/dev/null 2>&1 \
    || fail "P2PSTREAM_AGENT_UPDATE_ROOT_BASE64 must be valid base64"
  local decoded_authority_key_length canonical_authority_key authority_key_id
  decoded_authority_key_length="$(printf '%s' "$P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64" | base64 -d 2>/dev/null | wc -c)" \
    || fail "P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64 must be canonical base64"
  [[ "$decoded_authority_key_length" == "32" ]] \
    || fail "P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64 must encode exactly 32 bytes"
  canonical_authority_key="$(printf '%s' "$P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64" | base64 -d | base64 | tr -d '\n')"
  [[ "$canonical_authority_key" == "$P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64" ]] \
    || fail "P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64 must be canonical base64"
  [[ "$P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID" =~ ^[0-9a-f]{64}$ ]] \
    || fail "P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID must be a lowercase SHA-256 digest"
  authority_key_id="$(printf '%s' "$P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64" | base64 -d | sha256sum)"
  authority_key_id="${authority_key_id%% *}"
  [[ "$authority_key_id" == "$P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID" ]] \
    || fail "P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID does not match the authority public key"
  P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH="$(validate_tunnel_setting \
    P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH "$P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH" 1 9223372036854775807)"
  if [[ "${P2PSTREAM_TEST_ALLOW_CUSTOM_UPDATER_PATHS:-false}" != "true" ]]; then
    [[ "$INSTALL_PATH" == "/usr/local/bin/p2pstream" && "$AGENT_INSTALL_ROOT" == "/opt/p2pstream-agent" && \
       "$UPDATER_CONFIG_DIR" == "/etc/p2pstream-updater" && "$UPDATER_STATE_DIR" == "/var/lib/p2pstream-updater" && \
       "$SYSTEMD_DIR" == "/etc/systemd/system" ]] \
      || fail "managed updates require the fixed production install, state, config, and systemd paths"
  fi
}

ensure_updater_user() {
  if ! getent group "$UPDATER_GROUP" >/dev/null 2>&1; then
    groupadd --system "$UPDATER_GROUP"
  fi
  if ! id -u "$UPDATER_USER" >/dev/null 2>&1; then
    useradd --system --gid "$UPDATER_GROUP" --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin "$UPDATER_USER"
  fi
}

write_updater_units() {
  local tmp_dir="$1"
  cat >"${tmp_dir}/p2pstream-updater.service" <<EOF
[Unit]
Description=p2pstream unprivileged update staging worker
After=network-online.target
Wants=network-online.target
ConditionPathExists=${UPDATER_CONFIG_DIR}/enrolled.json
ConditionPathExists=${UPDATER_CONFIG_DIR}/root.json
ConditionPathExists=${UPDATER_CONFIG_DIR}/management-authority.json

[Service]
Type=oneshot
User=${UPDATER_USER}
Group=${UPDATER_GROUP}
ExecStart=${UPDATER_RUNNER_PATH} updater stage
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectClock=true
ProtectControlGroups=true
ProtectKernelLogs=true
ProtectKernelModules=true
ProtectKernelTunables=true
ProtectHostname=true
RestrictSUIDSGID=true
RestrictRealtime=true
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
SystemCallArchitectures=native
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
ReadWritePaths=${UPDATER_WORKER_DIR} ${UPDATER_STAGING_DIR}
EOF

  cat >"${tmp_dir}/p2pstream-updater.timer" <<EOF
[Unit]
Description=Periodically check for an assigned p2pstream agent update

[Timer]
OnBootSec=30s
OnUnitActiveSec=30s
RandomizedDelaySec=30s
AccuracySec=5s
Persistent=true
Unit=p2pstream-updater.service

[Install]
WantedBy=timers.target
EOF

  cat >"${tmp_dir}/p2pstream-updater-activate.service" <<EOF
[Unit]
Description=p2pstream offline update activator
StartLimitIntervalSec=10min
StartLimitBurst=5
ConditionPathExists=${UPDATER_CONFIG_DIR}/enrolled.json
ConditionPathExists=${UPDATER_CONFIG_DIR}/root.json
ConditionPathExists=${UPDATER_CONFIG_DIR}/management-authority.json

[Service]
Type=oneshot
Restart=on-failure
RestartSec=30s
User=root
Group=root
ExecStart=${UPDATER_RUNNER_PATH} updater activate
ExecStartPost=/usr/bin/systemctl start --no-block p2pstream-updater.service
ExecStopPost=/usr/bin/systemctl start --no-block p2pstream-updater.service
UMask=0077
NoNewPrivileges=true
PrivateNetwork=true
IPAddressDeny=any
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectClock=true
ProtectControlGroups=true
ProtectKernelLogs=true
ProtectKernelModules=true
ProtectKernelTunables=true
ProtectHostname=true
RestrictSUIDSGID=true
RestrictRealtime=true
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
SystemCallArchitectures=native
RestrictAddressFamilies=AF_UNIX
ReadWritePaths=${UPDATER_STATE_DIR} ${AGENT_INSTALL_ROOT}
EOF

  cat >"${tmp_dir}/p2pstream-updater-activate.path" <<EOF
[Unit]
Description=Activate a verified staged p2pstream agent update
ConditionPathExists=${UPDATER_CONFIG_DIR}/enrolled.json
ConditionPathExists=${UPDATER_CONFIG_DIR}/management-authority.json

[Path]
PathExists=${UPDATER_STAGING_DIR}/ready.json
PathExists=${UPDATER_STAGING_DIR}/rollback.json
PathExists=${UPDATER_ROOT_DIR}/activation-command.json
PathExists=${UPDATER_ROOT_DIR}/rollback-command.json
PathExists=${UPDATER_ROOT_DIR}/activation.json
PathExists=${UPDATER_ROOT_DIR}/rollback-journal.json
Unit=p2pstream-updater-activate.service

[Install]
WantedBy=multi-user.target
EOF
}

install_updater_foundation() {
  local tmp_dir="$1"
  local repository="$2"
  local tag="$3"
	local reenroll="$4"
  ensure_updater_user
  install -d -o root -g "$UPDATER_GROUP" -m 0750 "$UPDATER_CONFIG_DIR"
  install -d -o root -g "$UPDATER_GROUP" -m 0750 "$UPDATER_STATE_DIR"
  install -d -o "$UPDATER_USER" -g "$UPDATER_GROUP" -m 0700 "$UPDATER_WORKER_DIR" "$UPDATER_STAGING_DIR"
  install -d -o root -g root -m 0700 "$UPDATER_ROOT_DIR"

  # Keep the update control plane on a separately pinned rescue binary. The
  # mutable agent slot may contain a candidate that starts but breaks its
  # updater command; that candidate must not be able to disable rollback or
  # reporting. Only an explicit, locally pinned bootstrap replaces this copy.
  install -d -o root -g root -m 0755 "$UPDATER_RUNNER_DIR"
  local next_runner="${UPDATER_RUNNER_DIR}/.p2pstream-next"
  rm -f "$next_runner"
	install -o root -g root -m 0755 "$P2PSTREAM_AGENT_BINARY_FILE" "$next_runner"
  sync -d "$next_runner"

  P2PSTREAM_REPOSITORY="$repository" \
  P2PSTREAM_UPDATER_ENROLLMENT_TOKEN="$P2PSTREAM_UPDATER_ENROLLMENT_TOKEN" \
  P2PSTREAM_AGENT_UPDATE_ROOT_BASE64="$P2PSTREAM_AGENT_UPDATE_ROOT_BASE64" \
  P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64="$P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64" \
  P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID="$P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID" \
	P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH="$P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH" \
	P2PSTREAM_AGENT_UPDATE_CHANNEL="$P2PSTREAM_AGENT_UPDATE_CHANNEL" \
  P2PSTREAM_CURRENT_VERSION="$tag" \
	P2PSTREAM_EXISTING_TUNNEL_VERSION="${P2PSTREAM_EXISTING_TUNNEL_VERSION:-}" \
	P2PSTREAM_EXISTING_TUNNEL_COMMIT="${P2PSTREAM_EXISTING_TUNNEL_COMMIT:-}" \
	P2PSTREAM_UPDATER_REENROLL="$reenroll" \
  MANAGEMENT_URL="$MANAGEMENT_URL" \
  AGENT_ID="$AGENT_ID" \
		"$next_runner" updater bootstrap-host >"${tmp_dir}/updater-identities.json" \
    || fail "failed to create isolated updater identities"
	systemctl stop p2pstream-updater.timer p2pstream-updater-activate.path p2pstream-updater.service p2pstream-updater-activate.service >/dev/null 2>&1 || true
	systemctl disable p2pstream-updater.timer p2pstream-updater-activate.path >/dev/null 2>&1 || true
	mv -Tf "$next_runner" "$UPDATER_RUNNER_PATH"
	sync -d "$UPDATER_RUNNER_DIR"

  write_updater_units "$tmp_dir"
  install -m 0644 "${tmp_dir}/p2pstream-updater.service" "$UPDATER_SERVICE_FILE"
  install -m 0644 "${tmp_dir}/p2pstream-updater.timer" "$UPDATER_TIMER_FILE"
  install -m 0644 "${tmp_dir}/p2pstream-updater-activate.service" "$UPDATER_ACTIVATE_SERVICE_FILE"
  install -m 0644 "${tmp_dir}/p2pstream-updater-activate.path" "$UPDATER_ACTIVATE_PATH_FILE"

  # Enrollment is intentionally a separate fail-closed transition. Neither
  # unit is enabled until the signed Check adapter persists enrolled.json,
  # trusted root metadata, and monotonic counters atomically.
  systemctl daemon-reload
	runuser -u "$UPDATER_USER" -- "$UPDATER_RUNNER_PATH" updater enroll \
    || fail "unprivileged updater enrollment or first signed check failed"
  "$UPDATER_RUNNER_PATH" updater finalize-enrollment \
    || fail "failed to finalize updater enrollment and enable hardened units"
}

install_version_slot() {
  local binary="$1"
  local version="$2"
  local slot_dir="${AGENT_SLOTS_DIR}/${version}"
  local next_current="${AGENT_INSTALL_ROOT}/.current-next"
  local next_command="$(dirname "$INSTALL_PATH")/.p2pstream-next"

  (is_exact_semver "$version" || [[ "$version" =~ ^bootstrap-[0-9a-f]{16}$ ]]) \
    || fail "invalid fixed version slot"
  install -d -o root -g root -m 0755 "$AGENT_INSTALL_ROOT" "$AGENT_SLOTS_DIR" "$slot_dir"
  install -o root -g root -m 0755 "$binary" "${slot_dir}/p2pstream"
  sync -d "${slot_dir}/p2pstream"
  rm -f "$next_current"
  ln -s "slots/${version}/p2pstream" "$next_current"
  mv -Tf "$next_current" "$AGENT_CURRENT_PATH"
  sync -d "$AGENT_INSTALL_ROOT"

  install -d -m 0755 "$(dirname "$INSTALL_PATH")"
  rm -f "$next_command"
  ln -s "$AGENT_CURRENT_PATH" "$next_command"
  mv -Tf "$next_command" "$INSTALL_PATH"
  sync -d "$(dirname "$INSTALL_PATH")"
}

validate_management_url_inputs() {
  local url_lower
  url_lower="$(single_line "$MANAGEMENT_URL")"
  url_lower="${url_lower,,}"
  case "$url_lower" in
    https://*)
      ;;
    http://*)
      if [[ "${AGENT_ALLOW_INSECURE_MANAGEMENT:-}" != "true" ]]; then
        fail "refusing insecure MANAGEMENT_URL; use https or set AGENT_ALLOW_INSECURE_MANAGEMENT=true"
      fi
      if [[ -n "${MANAGEMENT_CA_FILE:-}" || -n "${MANAGEMENT_CA_PEM_BASE64:-}" || -n "${AGENT_TLS_CERT_FILE:-}" || -n "${AGENT_TLS_KEY_FILE:-}" ]]; then
        fail "agent TLS files require an https MANAGEMENT_URL"
      fi
      ;;
    *)
      fail "MANAGEMENT_URL must start with https:// or http://"
      ;;
  esac
}

validate_tls_inputs() {
  if [[ -n "${AGENT_TLS_CERT_FILE:-}" && -z "${AGENT_TLS_KEY_FILE:-}" ]]; then
    fail "AGENT_TLS_CERT_FILE and AGENT_TLS_KEY_FILE must be set together"
  fi
  if [[ -z "${AGENT_TLS_CERT_FILE:-}" && -n "${AGENT_TLS_KEY_FILE:-}" ]]; then
    fail "AGENT_TLS_CERT_FILE and AGENT_TLS_KEY_FILE must be set together"
  fi
  if [[ -n "${MANAGEMENT_CA_FILE:-}" && -z "${MANAGEMENT_CA_PEM_BASE64:-}" ]]; then
    require_readable_file MANAGEMENT_CA_FILE "$MANAGEMENT_CA_FILE"
  fi
  if [[ -n "${AGENT_TLS_CERT_FILE:-}" ]]; then
    require_readable_file AGENT_TLS_CERT_FILE "$AGENT_TLS_CERT_FILE"
    require_readable_file AGENT_TLS_KEY_FILE "$AGENT_TLS_KEY_FILE"
  fi
}

decode_management_ca_pem() {
  if [[ -z "${MANAGEMENT_CA_PEM_BASE64:-}" ]]; then
    return
  fi
  require_command base64
  printf '%s' "$MANAGEMENT_CA_PEM_BASE64" | base64 -d >"$1" 2>/dev/null \
    || fail "MANAGEMENT_CA_PEM_BASE64 is not valid base64"
}

sync_management_ca() {
  local tmp_dir="$1"
  if [[ -n "${MANAGEMENT_CA_PEM_BASE64:-}" ]]; then
    decode_management_ca_pem "${tmp_dir}/management-ca.pem"
    install -m 0644 "${tmp_dir}/management-ca.pem" "$MANAGEMENT_CA_PEM_FILE"
    MANAGEMENT_CA_FILE="$MANAGEMENT_CA_PEM_FILE"
    return
  fi
  if [[ -n "${MANAGEMENT_CA_FILE:-}" ]]; then
    return
  fi
  if [[ -e "$MANAGEMENT_CA_PEM_FILE" || -L "$MANAGEMENT_CA_PEM_FILE" ]]; then
    rm -f "$MANAGEMENT_CA_PEM_FILE"
  fi
}

initialize_management_trust() {
  install -d -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0700 "$AGENT_STATE_DIR"
  if [[ -n "${MANAGEMENT_CA_FILE:-}" ]]; then
    install -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0644 "$MANAGEMENT_CA_FILE" "$MANAGEMENT_TRUST_FILE"
  elif [[ ! -e "$MANAGEMENT_TRUST_FILE" ]]; then
    install -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0644 /dev/null "$MANAGEMENT_TRUST_FILE"
  fi
}

repair_management_trust() {
  require_env MANAGEMENT_CA_PEM_BASE64
  [[ -f "$ENV_FILE" ]] || fail "existing agent environment not found at ${ENV_FILE}; rerun the full install command"
  ensure_service_user
  local tmp_dir
  tmp_dir="$(mktemp -d)"
  INSTALL_TMP_DIR="$tmp_dir"
  trap cleanup_tmp_dir EXIT
  decode_management_ca_pem "${tmp_dir}/management-ca.pem"
  grep -q -- '-----BEGIN CERTIFICATE-----' "${tmp_dir}/management-ca.pem" \
    || fail "MANAGEMENT_CA_PEM_BASE64 contains no PEM certificate"
  install -d -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0700 "$AGENT_STATE_DIR"
  install -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0644 "${tmp_dir}/management-ca.pem" "$MANAGEMENT_TRUST_FILE"
  rm -f "${MANAGEMENT_TRUST_FILE}.state.json"
  systemctl restart "$SERVICE_NAME" \
    || fail "failed to restart ${SERVICE_NAME}; rerun the full install command"
  printf 'p2pstream agent management trust repaired and service restarted.\n'
}

restart_service() {
  systemctl daemon-reload
  systemctl enable "$SERVICE_NAME"
  if ! systemctl restart "$SERVICE_NAME"; then
    printf 'p2pstream agent install failed: systemctl restart %s failed\n' "$SERVICE_NAME" >&2
    printf 'Check status with: sudo systemctl status %s\n' "$SERVICE_NAME" >&2
    printf 'View logs with: sudo journalctl -u %s -n 100 --no-pager\n' "$SERVICE_NAME" >&2
    exit 1
  fi
}

main() {
  require_local_pinned_installer
  [[ "$(uname -s)" == "Linux" ]] || fail "this installer supports Linux only"
  [[ "$(id -u)" == "0" ]] || fail "run this installer with sudo"

  require_command chmod
  require_command chown
  require_command base64
  require_command install
  require_command ln
  require_command getent
  require_command groupadd
  require_command grep
  require_command useradd
  require_command mktemp
  require_command mv
  require_command rm
  require_command runuser
  require_command sha256sum
  require_command sed
  require_command stat
  require_command systemctl
  require_command sync
  require_command tr
  require_command uname
  require_command wc

  systemctl --version >/dev/null 2>&1 || fail "systemd is required"
  require_safe_agent_state_dir
  validate_managed_update_inputs
  if [[ "${P2PSTREAM_REPAIR_TRUST:-false}" == "true" ]]; then
    repair_management_trust
    return
  fi
  require_env P2PSTREAM_VERSION
  validate_local_agent_binary
  local preserve_existing_env="false"
  if managed_updates_requested && [[ -f "$ENV_FILE" ]] && [[ -z "${AGENT_TOKEN:-}" ]]; then
    require_env MANAGEMENT_URL
    require_env AGENT_ID
    [[ ! -L "$ENV_FILE" ]] || fail "refusing symlinked existing agent environment: ${ENV_FILE}"
    preserve_existing_env="true"
    validate_management_url_inputs
		[[ -f "$SERVICE_FILE" && ! -L "$SERVICE_FILE" ]] \
			|| fail "existing agent service not found at ${SERVICE_FILE}"
		grep -Fqx -- "ExecStart=${INSTALL_PATH} agent" "$SERVICE_FILE" \
			|| fail "existing agent service does not execute the binary being preserved: ${INSTALL_PATH} agent"
  else
    require_env MANAGEMENT_URL
    require_env AGENT_ID
    require_env AGENT_TOKEN
    validate_management_url_inputs
    validate_tls_inputs
    preflight_tunnel_settings
    validate_allow_target_inputs
    prepare_allow_target_preservation
  fi
  ensure_service_user

  local repository="${P2PSTREAM_REPOSITORY:-$DEFAULT_REPOSITORY}"
  local version="$P2PSTREAM_VERSION"
  local arch tag tmp_dir

  repository="$(single_line "$repository")"
  [[ "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] \
    || fail "P2PSTREAM_REPOSITORY must use GitHub owner/repo with letters, numbers, dots, underscores, or hyphens"

  arch="$(detect_arch)"
  version="$(single_line "$version")"
	if is_exact_semver "$version"; then
		tag="$version"
	else
		fail "P2PSTREAM_VERSION must pin an exact SemVer release or prerelease"
	fi
	if managed_updates_requested; then
		if [[ "$tag" == *-* && "$P2PSTREAM_AGENT_UPDATE_CHANNEL" != "staging" ]]; then
			fail "SemVer prereleases require P2PSTREAM_AGENT_UPDATE_CHANNEL=staging"
		fi
		if [[ "$tag" != *-* && "$P2PSTREAM_AGENT_UPDATE_CHANNEL" != "stable" ]]; then
			fail "final SemVer releases require P2PSTREAM_AGENT_UPDATE_CHANNEL=stable"
		fi
	fi

  tmp_dir="$(mktemp -d)"
  INSTALL_TMP_DIR="$tmp_dir"
  trap cleanup_tmp_dir EXIT

  printf 'Installing locally supplied p2pstream %s for linux/%s...\n' "$tag" "$arch"
	local bootstrap_digest slot_version="$tag" updater_reenroll="false"
	if managed_updates_requested && [[ -e "${UPDATER_CONFIG_DIR}/enrolled.json" || -L "${UPDATER_CONFIG_DIR}/enrolled.json" ]]; then
		[[ -f "${UPDATER_CONFIG_DIR}/enrolled.json" && ! -L "${UPDATER_CONFIG_DIR}/enrolled.json" ]] \
			|| fail "refusing unsafe existing managed updater enrollment marker"
		updater_reenroll="true"
	fi
	local tunnel_bootstrap_binary="$P2PSTREAM_AGENT_BINARY_FILE"
	if managed_updates_requested && [[ "$preserve_existing_env" == "true" && "$updater_reenroll" != "true" ]]; then
		require_env P2PSTREAM_EXISTING_TUNNEL_VERSION
		require_env P2PSTREAM_EXISTING_TUNNEL_COMMIT
		is_exact_semver "$P2PSTREAM_EXISTING_TUNNEL_VERSION" \
			|| fail "P2PSTREAM_EXISTING_TUNNEL_VERSION must be the exact observed agent SemVer"
		[[ "$P2PSTREAM_EXISTING_TUNNEL_COMMIT" =~ ^[0-9a-f]{40}$ ]] \
			|| fail "P2PSTREAM_EXISTING_TUNNEL_COMMIT must be the exact observed agent commit"
		[[ -x "$INSTALL_PATH" ]] || fail "existing agent command is not executable: ${INSTALL_PATH}"
		tunnel_bootstrap_binary="${tmp_dir}/existing-agent"
		install -o root -g root -m 0755 "$INSTALL_PATH" "$tunnel_bootstrap_binary"
		[[ -f "$tunnel_bootstrap_binary" && ! -L "$tunnel_bootstrap_binary" && -s "$tunnel_bootstrap_binary" ]] \
			|| fail "could not snapshot the existing tunnel binary safely"
	fi
  if managed_updates_requested; then
		bootstrap_digest="$(sha256sum "$tunnel_bootstrap_binary")"
    bootstrap_digest="${bootstrap_digest%% *}"
    slot_version="bootstrap-${bootstrap_digest:0:16}"
  fi
	if [[ "$updater_reenroll" != "true" ]]; then
		install_version_slot "$tunnel_bootstrap_binary" "$slot_version"
	fi

  install -d -m 0755 "$CONFIG_DIR"
  if [[ "$preserve_existing_env" == "true" ]]; then
    chown root:"$SERVICE_GROUP" "$ENV_FILE"
    chmod 0640 "$ENV_FILE"
  else
    sync_management_ca "$tmp_dir"
    initialize_management_trust
    write_agent_env "${tmp_dir}/agent.env"
    install -o root -g "$SERVICE_GROUP" -m 0640 "${tmp_dir}/agent.env" "$ENV_FILE"
  fi

  install -d -m 0755 "$SYSTEMD_DIR"
  if [[ "$preserve_existing_env" == "true" ]]; then
    : # Existing service path and ExecStart were verified before any mutation.
  else
    write_service_file "${tmp_dir}/${SERVICE_NAME}.service"
    install -m 0644 "${tmp_dir}/${SERVICE_NAME}.service" "$SERVICE_FILE"
  fi

  if managed_updates_requested; then
		install_updater_foundation "$tmp_dir" "$repository" "$tag" "$updater_reenroll"
  fi

	if [[ "$preserve_existing_env" == "true" ]] && managed_updates_requested; then
		printf 'Existing tunnel service left running on its unchanged binary.\n'
	else
		restart_service
	fi

  printf 'p2pstream agent installed and restarted.\n'
  printf 'Check status with: sudo systemctl status %s\n' "$SERVICE_NAME"
  printf 'View logs with: sudo journalctl -u %s -f\n' "$SERVICE_NAME"
  if managed_updates_requested; then
    printf 'Managed updater enrolled; hardened polling and activation units enabled.\n'
  fi
}

main "$@"
