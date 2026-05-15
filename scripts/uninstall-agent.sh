#!/usr/bin/env bash
set -Eeuo pipefail

readonly SERVICE_NAME="p2pstream-agent"
readonly CONFIG_DIR="${P2PSTREAM_CONFIG_DIR:-/etc/p2pstream}"
readonly INSTALL_PATH="${P2PSTREAM_INSTALL_PATH:-/usr/local/bin/p2pstream}"
readonly SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
readonly SERVICE_USER="p2pstream"
readonly SERVICE_GROUP="p2pstream"
readonly CONFIRM_VALUE="full-purge"

fail() {
  printf 'p2pstream agent uninstall failed: %s\n' "$*" >&2
  exit 1
}

warn() {
  printf 'warning: %s\n' "$*" >&2
}

info() {
  printf '%s\n' "$*"
}

is_dry_run() {
  [[ "${P2PSTREAM_UNINSTALL_DRY_RUN:-}" == "true" ]]
}

run_cmd() {
  if is_dry_run; then
    printf 'DRY RUN:'
    printf ' %q' "$@"
    printf '\n'
    return 0
  fi
  "$@"
}

run_cmd_tolerate() {
  if ! run_cmd "$@"; then
    warn "command failed and was skipped: $*"
  fi
}

require_confirmation() {
  if [[ "${P2PSTREAM_UNINSTALL_CONFIRM:-}" != "$CONFIRM_VALUE" ]]; then
    fail "set P2PSTREAM_UNINSTALL_CONFIRM=${CONFIRM_VALUE} to remove the agent service, config directory, binary, and service user"
  fi
}

require_linux() {
  [[ "$(uname -s)" == "Linux" ]] || fail "this uninstaller supports Linux only"
}

require_root_for_changes() {
  if ! is_dry_run && [[ "$(id -u)" != "0" ]]; then
    fail "run this uninstaller with sudo"
  fi
}

require_safe_config_dir() {
  case "$CONFIG_DIR" in
    ""|"/"|"/etc"|"/usr"|"/usr/local"|"/usr/local/bin")
      fail "refusing to remove unsafe P2PSTREAM_CONFIG_DIR: ${CONFIG_DIR}"
      ;;
  esac
}

require_safe_install_path() {
  case "$INSTALL_PATH" in
    ""|"/"|"/etc"|"/etc/p2pstream"|"/usr"|"/usr/local"|"/usr/local/bin")
      fail "refusing to remove unsafe P2PSTREAM_INSTALL_PATH: ${INSTALL_PATH}"
      ;;
  esac
}

remove_file() {
  local path="$1"
  if [[ -e "$path" || -L "$path" ]]; then
    run_cmd rm -f "$path"
  else
    info "Already absent: ${path}"
  fi
}

remove_dir() {
  local path="$1"
  if [[ -d "$path" || -L "$path" ]]; then
    run_cmd rm -rf "$path"
  else
    info "Already absent: ${path}"
  fi
}

systemd_unit_known() {
  [[ -f "$SERVICE_FILE" ]] && return 0
  systemctl list-unit-files --no-legend "${SERVICE_NAME}.service" 2>/dev/null | grep -q "^${SERVICE_NAME}[.]service[[:space:]]"
}

stop_service() {
  if ! command -v systemctl >/dev/null 2>&1; then
    warn "systemctl not found; skipping systemd stop and disable"
    return
  fi

  if systemd_unit_known; then
    run_cmd_tolerate systemctl disable --now "$SERVICE_NAME"
  else
    info "Systemd unit is not registered: ${SERVICE_NAME}"
  fi
}

reload_systemd() {
  if ! command -v systemctl >/dev/null 2>&1; then
    return
  fi

  run_cmd_tolerate systemctl daemon-reload
  run_cmd_tolerate systemctl reset-failed "$SERVICE_NAME"
}

delete_service_user() {
  if id -u "$SERVICE_USER" >/dev/null 2>&1; then
    run_cmd_tolerate userdel "$SERVICE_USER"
  else
    info "User is already absent: ${SERVICE_USER}"
  fi

  if getent group "$SERVICE_GROUP" >/dev/null 2>&1; then
    run_cmd_tolerate groupdel "$SERVICE_GROUP"
  else
    info "Group is already absent: ${SERVICE_GROUP}"
  fi
}

main() {
  require_confirmation
  require_linux
  require_root_for_changes
  require_safe_config_dir
  require_safe_install_path

  info "Uninstalling p2pstream agent with full purge."
  info "Service: ${SERVICE_NAME}"
  info "Service file: ${SERVICE_FILE}"
  info "Config directory: ${CONFIG_DIR}"
  info "Binary: ${INSTALL_PATH}"
  if is_dry_run; then
    info "Dry run is enabled; no changes will be made."
  fi

  stop_service
  remove_file "$SERVICE_FILE"
  reload_systemd
  remove_dir "$CONFIG_DIR"
  remove_file "$INSTALL_PATH"
  delete_service_user

  info "p2pstream agent uninstall finished."
  info "If the management UI still has this agent registered, delete or disable the agent record there as a separate step."
}

main "$@"
