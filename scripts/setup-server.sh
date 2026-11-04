#!/bin/sh
# Fresh installs only. Safe to invoke as: curl -fsSL URL | sh -s -- [options]

usage() {
    cat <<'USAGE'
Usage: setup-server.sh [--directory PATH] [--host DNS_OR_IPV4]
                       [--channel stable|staging] [--http-port PORT]
                       [--https-port PORT] [--management-port PORT]
Requires Docker Engine, Docker Compose, and curl. Creates a new ./p2pstream
folder by default. Existing installations must use their normal Compose updates.
USAGE
}

fail() {
    printf 'Setup failed: %s\n' "$*" >&2
    if [ "$created" = yes ]; then
        printf 'Files and any container data were retained in: %s\n' "$directory" >&2
        printf '%s\n' 'Inspect: docker logs p2pstream' 'Inspect: docker inspect p2pstream' >&2
        printf '%s\n' 'After resolving the error, run from that directory:' \
            '  docker compose pull p2pstream && docker compose up -d p2pstream' \
            'If .env was created, it contains MANAGEMENT_SETUP_TOKEN. Keep it private.' >&2
    fi
    exit 1
}

valid_ipv4() (
    case "$1" in ''|*[!0-9.]*|.*|*.|*..*) exit 1 ;; esac
    IFS=.
    set -- $1
    [ "$#" -eq 4 ] || exit 1
    for octet do
        case "$octet" in ''|0[0-9]*|????*) exit 1 ;; esac
        [ "$octet" -le 255 ] || exit 1
    done
    # Reject unspecified, loopback, multicast, and reserved destination addresses.
    [ "$1" -gt 0 ] && [ "$1" -ne 127 ] && [ "$1" -lt 224 ]
)

valid_host() (
    [ "${#1}" -le 253 ] || exit 1
    case "$1" in ''|*[!a-zA-Z0-9.-]*|.*|*.) exit 1 ;; esac
    case "$1" in *[!0-9.]*) ;; *) valid_ipv4 "$1"; exit $? ;; esac
    IFS=.
    set -- $1
    for label do
        case "$label" in ''|-*|*-) exit 1 ;; esac
        [ "${#label}" -le 63 ] || exit 1
    done
)

valid_port() {
    case "$1" in ''|*[!0-9]*|0*|??????*) return 1 ;; esac
    [ "$1" -le 65535 ]
}

find_host() {
    # Both time and captured response size are bounded, including older curl
    # versions that do not enforce --max-filesize on chunked responses.
    candidate=$({ curl -q --fail --silent --show-error --noproxy '*' --proto '=https' \
        --connect-timeout 3 --max-time 8 --max-filesize 64 \
        https://api.ipify.org 2>/dev/null && printf ':ok'; } | dd bs=1 count=68 2>/dev/null) || candidate=
    case "$candidate" in *:ok) candidate=${candidate%:ok} ;; *) candidate= ;; esac
    if valid_ipv4 "$candidate"; then
        host=$candidate
        printf 'Detected public IPv4 address: %s\n' "$host"
    else
        host=
        if command -v ip >/dev/null 2>&1; then
            route=$(ip -4 route get 1.1.1.1 2>/dev/null) || route=
            previous=
            for word in $route; do
                if [ "$previous" = src ] && valid_ipv4 "$word"; then
                    host=$word
                    break
                fi
                previous=$word
            done
        fi
        if [ -z "$host" ] && command -v hostname >/dev/null 2>&1; then
            addresses=$(hostname -I 2>/dev/null) || addresses=
            for candidate in $addresses; do
                if valid_ipv4 "$candidate"; then host=$candidate; break; fi
            done
        fi
        [ -n "$host" ] || fail 'Cannot detect an IPv4 address; supply --host DNS_OR_IPV4.'
        printf 'Public IP lookup unavailable; using local IPv4 address: %s\n' "$host"
    fi
    printf '%s\n' 'Address detection does not verify router forwarding or firewall reachability.'
}

check_fresh_inventory() {
    containers=$(docker container ls --all --format '{{.Names}}') || fail 'Cannot inspect Docker containers; check Docker access.'
    volumes=$(docker volume ls --format '{{.Name}}') || fail 'Cannot inspect Docker volumes; check Docker access.'
    newline='
'
    case "$newline$containers$newline" in *"${newline}p2pstream${newline}"*) fail 'Container p2pstream already exists; use the normal Compose update workflow.' ;; esac
    case "$newline$volumes$newline" in *"${newline}p2pstream-data${newline}"*) fail 'Volume p2pstream-data already exists; use the normal Compose update workflow.' ;; esac
}

compose() (
    # Shell environment wins over --env-file interpolation. Clear every variable
    # used by the embedded manifest and Compose settings that could redirect it.
    unset P2PSTREAM_IMAGE P2PSTREAM_HTTP_PORT P2PSTREAM_HTTPS_PORT \
        P2PSTREAM_MANAGEMENT_PORT P2PSTREAM_MANAGEMENT_PUBLISH_ADDRESS \
        MANAGEMENT_BIND_ADDRESS MANAGEMENT_UI_DISABLED MANAGEMENT_PUBLIC_URL \
        MANAGEMENT_SETUP_TOKEN MANAGEMENT_TLS_EXTRA_HOSTS \
        MANAGEMENT_TRUSTED_PROXY_CIDRS MANAGEMENT_CLIENT_IP_HEADER MANAGEMENT_CLIENT_IP_MODE \
        PUBLIC_MAX_HEADER_BYTES PUBLIC_MAX_REQUEST_BODY_BYTES \
        PUBLIC_REQUEST_BODY_IDLE_TIMEOUT_MILLIS PUBLIC_MAX_CONCURRENT_REQUESTS \
        PUBLIC_MAX_CONCURRENT_REQUESTS_PER_TARGET PUBLIC_MAX_CONNECTIONS_PER_TARGET \
        SERVER_TUNNEL_MAX_CONCURRENT_STREAMS COMPOSE_FILE COMPOSE_PROJECT_NAME \
        COMPOSE_ENV_FILES COMPOSE_PROFILES COMPOSE_DISABLE_ENV_FILE
    docker compose --project-name "$project" --project-directory "$directory" \
        --env-file "$directory/.env" -f "$directory/compose.yaml" "$@"
)

write_compose() {
    cat <<'P2PSTREAM_COMPOSE'
services:
  p2pstream:
    image: "${P2PSTREAM_IMAGE:-ghcr.io/kirari04/p2pstream:latest}"
    container_name: p2pstream
    restart: unless-stopped
    read_only: true
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    cap_add:
      - NET_BIND_SERVICE
    tmpfs:
      - /tmp:rw,nosuid,nodev,mode=1777
    environment:
      CONFIG_DIR: /data
      MANAGEMENT_PORT: "8081"
      # Default accepts Docker forwarding; preserve explicit operator overrides.
      MANAGEMENT_BIND_ADDRESS: "${MANAGEMENT_BIND_ADDRESS:-0.0.0.0}"
      MANAGEMENT_UI_DISABLED: "${MANAGEMENT_UI_DISABLED:-false}"
      MANAGEMENT_PUBLIC_URL: "${MANAGEMENT_PUBLIC_URL:-https://localhost:8081}"
      MANAGEMENT_SETUP_TOKEN: "${MANAGEMENT_SETUP_TOKEN:-}"
      MANAGEMENT_TLS_EXTRA_HOSTS: "${MANAGEMENT_TLS_EXTRA_HOSTS:-}"
      MANAGEMENT_TRUSTED_PROXY_CIDRS: "${MANAGEMENT_TRUSTED_PROXY_CIDRS:-}"
      MANAGEMENT_CLIENT_IP_HEADER: "${MANAGEMENT_CLIENT_IP_HEADER:-X-Forwarded-For}"
      MANAGEMENT_CLIENT_IP_MODE: "${MANAGEMENT_CLIENT_IP_MODE:-trusted_chain}"
      PUBLIC_MAX_HEADER_BYTES: "${PUBLIC_MAX_HEADER_BYTES:-65536}"
      PUBLIC_MAX_REQUEST_BODY_BYTES: "${PUBLIC_MAX_REQUEST_BODY_BYTES:-1073741824}"
      PUBLIC_REQUEST_BODY_IDLE_TIMEOUT_MILLIS: "${PUBLIC_REQUEST_BODY_IDLE_TIMEOUT_MILLIS:-30000}"
      PUBLIC_MAX_CONCURRENT_REQUESTS: "${PUBLIC_MAX_CONCURRENT_REQUESTS:-2048}"
      PUBLIC_MAX_CONCURRENT_REQUESTS_PER_TARGET: "${PUBLIC_MAX_CONCURRENT_REQUESTS_PER_TARGET:-0}"
      PUBLIC_MAX_CONNECTIONS_PER_TARGET: "${PUBLIC_MAX_CONNECTIONS_PER_TARGET:-256}"
      SERVER_TUNNEL_MAX_CONCURRENT_STREAMS: "${SERVER_TUNNEL_MAX_CONCURRENT_STREAMS:-0}"
    ports:
      - "${P2PSTREAM_HTTP_PORT:-80}:80"
      - "${P2PSTREAM_HTTPS_PORT:-443}:443"
      - "${P2PSTREAM_MANAGEMENT_PUBLISH_ADDRESS:-}:${P2PSTREAM_MANAGEMENT_PORT:-8081}:8081"
    volumes:
      - p2pstream-data:/data

volumes:
  p2pstream-data:
    name: p2pstream-data
P2PSTREAM_COMPOSE
}

main() {
    set -eu
    set -f
    created=no
    directory=./p2pstream
    host=
    channel=stable
    http_port=80
    https_port=443
    management_port=8081
    while [ "$#" -gt 0 ]; do
        option=$1
        case "$option" in
            --help|-h) usage; exit 0 ;;
            --directory|--host|--channel|--http-port|--https-port|--management-port)
                [ "$#" -ge 2 ] || fail "Missing value for $option."
                case "$option" in
                    --directory) directory=$2 ;;
                    --host) host=$2; valid_host "$host" || fail 'Invalid --host; use a DNS name or usable IPv4 address.' ;;
                    --channel) channel=$2 ;;
                    --http-port) http_port=$2 ;;
                    --https-port) https_port=$2 ;;
                    --management-port) management_port=$2 ;;
                esac
                shift 2 ;;
            *) fail "Unknown option: $option" ;;
        esac
    done
    case "$channel" in stable) tag=latest ;; staging) tag=staging ;; *) fail 'Channel must be stable or staging.' ;; esac
    for port in "$http_port" "$https_port" "$management_port"; do
        valid_port "$port" || fail 'Ports must be integers from 1 to 65535 (without leading zeros).'
    done
    [ "$http_port" != "$https_port" ] && [ "$http_port" != "$management_port" ] && \
        [ "$https_port" != "$management_port" ] || fail 'The three published ports must be distinct.'
    [ -n "$directory" ] || fail 'Directory must not be empty.'
    case "$directory" in /*) ;; *) directory=$PWD/$directory ;; esac
    # A trailing slash can hide a dangling symlink from -L.
    while [ "${directory%/}" != "$directory" ]; do directory=${directory%/}; done
    [ -n "$directory" ] || fail 'The destination directory already exists.'
    [ ! -e "$directory" ] && [ ! -L "$directory" ] || fail 'The destination already exists; choose a new directory.'
    for program in docker curl dd od tr; do
        command -v "$program" >/dev/null 2>&1 || fail "Required command not found: $program."
    done
    docker compose version >/dev/null 2>&1 || fail 'Docker Compose is required.'
    endpoint=$(docker context inspect --format '{{.Endpoints.docker.Host}}') || fail 'Cannot inspect the Docker context.'
    case "$endpoint" in unix://*) ;; *) fail 'Setup requires a local Docker daemon using a Unix socket.' ;; esac
    check_fresh_inventory
    [ -n "$host" ] || find_host
    token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n') || fail 'Cannot generate a setup token.'
    [ "${#token}" -eq 64 ] || fail 'Cannot generate a setup token.'
    case "$token" in *[!0-9a-f]*) fail 'Cannot generate a setup token.' ;; esac
    project_id=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n') || fail 'Cannot generate a Compose project name.'
    [ "${#project_id}" -eq 12 ] || fail 'Cannot generate a Compose project name.'
    case "$project_id" in *[!0-9a-f]*) fail 'Cannot generate a Compose project name.' ;; esac
    umask 077
    mkdir -m 700 "$directory" || fail 'Cannot create the new destination directory.'
    created=yes
    write_compose >"$directory/compose.yaml" || fail 'Cannot write compose.yaml.'
    project=p2pstream-$project_id
    management_url=https://$host:$management_port
    {
        printf 'COMPOSE_PROJECT_NAME=%s\n' "$project"
        printf 'P2PSTREAM_IMAGE=ghcr.io/kirari04/p2pstream:%s\n' "$tag"
        printf 'P2PSTREAM_HTTP_PORT=%s\n' "$http_port"
        printf 'P2PSTREAM_HTTPS_PORT=%s\n' "$https_port"
        printf 'P2PSTREAM_MANAGEMENT_PORT=%s\n' "$management_port"
        printf 'MANAGEMENT_PUBLIC_URL=%s\n' "$management_url"
        printf 'MANAGEMENT_SETUP_TOKEN=%s\n' "$token"
    } >"$directory/.env" || fail 'Cannot write .env.'
    compose config --quiet || fail 'Compose configuration validation failed.'
    compose pull || fail 'Image download failed.'
    # Pulling can take time; refuse resources created by a competing install.
    check_fresh_inventory
    compose up -d || fail 'Container startup failed.'
    attempt=0
    while [ "$attempt" -lt 30 ]; do
        # The fresh server uses its own CA. Skip verification only for loopback,
        # and bypass proxy configuration so this remains a local readiness check.
        if curl -q --fail --silent --insecure --noproxy '*' --proto '=https' \
            --connect-timeout 1 --max-time 2 --output /dev/null \
            "https://127.0.0.1:$management_port/"; then
            printf '\nManagement HTTPS responded locally. Open: %s\n' "$management_url"
            printf 'Deployment directory: %s\n' "$directory"
            printf 'Setup token: %s\n' "$token"
            printf '%s\n' 'Complete first-run setup in your browser. The initial certificate uses a private CA.'
            return 0
        fi
        attempt=$((attempt + 1))
        [ "$attempt" -ge 30 ] || sleep 1
    done
    fail 'Management HTTPS did not become ready locally; inspect the retained installation before retrying.'
}

# Keep this last: a piped script is fully parsed before any setup action starts.
main "$@" </dev/null
