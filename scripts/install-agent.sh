#!/usr/bin/env bash
set -Eeuo pipefail

readonly DEFAULT_REPOSITORY="Kirari04/p2pstream"
readonly SERVICE_NAME="p2pstream-agent"
readonly CONFIG_DIR="${P2PSTREAM_CONFIG_DIR:-/etc/p2pstream}"
readonly ENV_FILE="${CONFIG_DIR}/agent.env"
readonly SYSTEMD_DIR="${P2PSTREAM_SYSTEMD_DIR:-/etc/systemd/system}"
readonly SERVICE_FILE="${SYSTEMD_DIR}/${SERVICE_NAME}.service"
readonly INSTALL_PATH="${P2PSTREAM_INSTALL_PATH:-/usr/local/bin/p2pstream}"
readonly MANAGEMENT_CA_PEM_FILE="${CONFIG_DIR}/management-ca.pem"
readonly SERVICE_USER="p2pstream"
readonly SERVICE_GROUP="p2pstream"
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

latest_release_tag() {
  local repository="$1"
  local tag
  tag="$(curl -fsSL "https://api.github.com/repos/${repository}/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  [[ -n "$tag" ]] || fail "could not resolve latest release for ${repository}"
  printf '%s' "$tag"
}

trim_leading_agent_env_whitespace() {
  local value="$1"
  while [[ "$value" == " "* || "$value" == $'\t'* || "$value" == $'\r'* ]]; do
    value="${value:1}"
  done
  printf '%s' "$value"
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
  local value
  local suffix

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
    if [[ "$trimmed" == "${name}="* ]]; then
      value="${trimmed#"${name}="}"
      if ! agent_env_value_is_supported_single_line "$value"; then
        fail "cannot safely preserve ${name} from ${ENV_FILE}: multiline or ambiguous assignment; provide ${name} or set AGENT_CLEAR_ALLOW_TARGETS=true"
      fi
      EXISTING_AGENT_ENV_ASSIGNMENT="${name}=${value}"
      continue
    fi
    if [[ "$trimmed" == "$name"* ]]; then
      suffix="${trimmed:${#name}:1}"
      if [[ -z "$suffix" || ! "$suffix" =~ [A-Za-z0-9_] ]]; then
        fail "cannot safely preserve ${name} from ${ENV_FILE}: ambiguous assignment; provide ${name} or set AGENT_CLEAR_ALLOW_TARGETS=true"
      fi
    fi
    if [[ "$trimmed" == export[[:space:]]* ]]; then
      trimmed="${trimmed#export}"
      trimmed="$(trim_leading_agent_env_whitespace "$trimmed")"
      if [[ "$trimmed" == "${name}="* ]]; then
        fail "cannot safely preserve ${name} from ${ENV_FILE}: unsupported export assignment; provide ${name} or set AGENT_CLEAR_ALLOW_TARGETS=true"
      fi
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
}

prepare_allow_target_preservation() {
  EXISTING_AGENT_ENV_ASSIGNMENT=""
  if [[ -z "${AGENT_ALLOW_TARGETS:-}" && "${AGENT_CLEAR_ALLOW_TARGETS:-false}" != "true" ]]; then
    load_existing_agent_env_assignment AGENT_ALLOW_TARGETS
  fi
}

write_agent_env() {
  local tmp_file="$1"
  local preserved_allow_targets="$EXISTING_AGENT_ENV_ASSIGNMENT"
  {
    printf 'MANAGEMENT_URL=%s\n' "$(systemd_env_value "$MANAGEMENT_URL")"
    if [[ -n "${MANAGEMENT_CA_FILE:-}" ]]; then
      printf 'MANAGEMENT_CA_FILE=%s\n' "$(systemd_env_value "$MANAGEMENT_CA_FILE")"
    fi
    if [[ -n "${AGENT_TLS_CERT_FILE:-}" ]]; then
      printf 'AGENT_TLS_CERT_FILE=%s\n' "$(systemd_env_value "$AGENT_TLS_CERT_FILE")"
    fi
    if [[ -n "${AGENT_TLS_KEY_FILE:-}" ]]; then
      printf 'AGENT_TLS_KEY_FILE=%s\n' "$(systemd_env_value "$AGENT_TLS_KEY_FILE")"
    fi
    if [[ "${AGENT_ALLOW_INSECURE_MANAGEMENT:-}" == "true" ]]; then
      printf 'AGENT_ALLOW_INSECURE_MANAGEMENT="true"\n'
    fi
    if [[ -n "${AGENT_ALLOW_TARGETS:-}" ]]; then
      printf 'AGENT_ALLOW_TARGETS=%s\n' "$(systemd_env_value "$AGENT_ALLOW_TARGETS")"
    elif [[ -n "$preserved_allow_targets" ]]; then
      printf '%s\n' "$preserved_allow_targets"
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
  [[ "$(uname -s)" == "Linux" ]] || fail "this installer supports Linux only"
  [[ "$(id -u)" == "0" ]] || fail "run this installer with sudo"

  require_command curl
  require_command install
  require_command getent
  require_command groupadd
  require_command useradd
  require_command mktemp
  require_command rm
  require_command sed
  require_command sha256sum
  require_command systemctl
  require_command tar
  require_command uname

  systemctl --version >/dev/null 2>&1 || fail "systemd is required"
  require_env MANAGEMENT_URL
  require_env AGENT_ID
  require_env AGENT_TOKEN
  validate_management_url_inputs
  validate_tls_inputs
  validate_allow_target_inputs
  prepare_allow_target_preservation
  ensure_service_user

  local repository="${P2PSTREAM_REPOSITORY:-$DEFAULT_REPOSITORY}"
  local version="${P2PSTREAM_VERSION:-latest}"
  local arch tag asset base_url tmp_dir checksum_line

  repository="$(single_line "$repository")"
  [[ "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] \
    || fail "P2PSTREAM_REPOSITORY must use GitHub owner/repo with letters, numbers, dots, underscores, or hyphens"

  arch="$(detect_arch)"
  version="$(single_line "$version")"
  if [[ "$version" == "latest" ]]; then
    tag="$(latest_release_tag "$repository")"
  elif [[ "$version" == "staging" ]]; then
    tag="staging"
  elif [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    tag="$version"
  else
    fail "P2PSTREAM_VERSION must be latest, staging, or vX.Y.Z"
  fi

  asset="p2pstream_${tag}_linux_${arch}.tar.gz"
  base_url="https://github.com/${repository}/releases/download/${tag}"
  tmp_dir="$(mktemp -d)"
  INSTALL_TMP_DIR="$tmp_dir"
  trap cleanup_tmp_dir EXIT

  printf 'Downloading p2pstream %s for linux/%s...\n' "$tag" "$arch"
  curl -fL "${base_url}/${asset}" -o "${tmp_dir}/${asset}"
  curl -fL "${base_url}/checksums.txt" -o "${tmp_dir}/checksums.txt"

  checksum_line="$(grep -E "[[:space:]]${asset}$" "${tmp_dir}/checksums.txt" || true)"
  [[ -n "$checksum_line" ]] || fail "checksums.txt does not contain ${asset}"
  printf '%s\n' "$checksum_line" | (cd "$tmp_dir" && sha256sum -c -)

  tar -xzf "${tmp_dir}/${asset}" -C "$tmp_dir"
  [[ -f "${tmp_dir}/p2pstream" ]] || fail "release archive did not contain p2pstream binary"

  install -d -m 0755 "$(dirname "$INSTALL_PATH")"
  install -m 0755 "${tmp_dir}/p2pstream" "$INSTALL_PATH"

  install -d -m 0755 "$CONFIG_DIR"
  sync_management_ca "$tmp_dir"
  write_agent_env "${tmp_dir}/agent.env"
  install -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0600 "${tmp_dir}/agent.env" "$ENV_FILE"

  write_service_file "${tmp_dir}/${SERVICE_NAME}.service"
  install -d -m 0755 "$SYSTEMD_DIR"
  install -m 0644 "${tmp_dir}/${SERVICE_NAME}.service" "$SERVICE_FILE"

  restart_service

  printf 'p2pstream agent installed and restarted.\n'
  printf 'Check status with: sudo systemctl status %s\n' "$SERVICE_NAME"
  printf 'View logs with: sudo journalctl -u %s -f\n' "$SERVICE_NAME"
}

main "$@"
