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

write_agent_env() {
  local tmp_file="$1"
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
