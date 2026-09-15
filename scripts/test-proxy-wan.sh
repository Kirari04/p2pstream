#!/usr/bin/env bash
# Finite TCP regression in disposable namespaces. No host addresses, routes,
# qdiscs or sysctls are modified. Delay on a separate router avoids sender TSQ
# artifacts from netem on the transmitting application's own interface.
set -Eeuo pipefail

if [[ ${1:-} != --inside ]]; then
  cd "$(dirname "$0")/.."
  mkdir -p tmp/proxy-wan
  go test -c -o tmp/proxy-wan/server.test ./internal/server
  export P2PSTREAM_WAN_PARENT_NETNS
  P2PSTREAM_WAN_PARENT_NETNS=$(readlink /proc/self/ns/net)
  if unshare --user --map-root-user --net true 2>/dev/null; then
    exec unshare --user --map-root-user --net "$0" --inside "$(pwd)/tmp/proxy-wan/server.test"
  fi
  # Ubuntu CI can restrict unprivileged user namespaces. Use its passwordless
  # sudo only for the disposable network, after compiling as the invoking user.
  exec sudo -n --preserve-env=P2PSTREAM_WAN_PARENT_NETNS unshare --net "$0" --inside "$(pwd)/tmp/proxy-wan/server.test"
fi

[[ -n ${P2PSTREAM_WAN_PARENT_NETNS:-} && $(readlink /proc/self/ns/net) != "$P2PSTREAM_WAN_PARENT_NETNS" ]] || {
  echo "Refusing to configure networking outside a disposable namespace" >&2
  exit 1
}

unshare --net sleep 600 &
client_pid=$!
unshare --net sleep 600 &
router_pid=$!
trap 'kill "$client_pid" "$router_pid" 2>/dev/null || true; wait || true' EXIT
# Wait for each child to enter its namespace before moving the veth devices.
for pid in "$client_pid" "$router_pid"; do
  for ((i=0; i<100; i++)); do
    [[ $(readlink /proc/"$pid"/ns/net) != "$(readlink /proc/self/ns/net)" ]] && break
    sleep 0.01
  done
  [[ $(readlink /proc/"$pid"/ns/net) != "$(readlink /proc/self/ns/net)" ]]
done
client() { nsenter -t "$client_pid" -n "$@"; }
router() { nsenter -t "$router_pid" -n "$@"; }
ip link set lo up
client ip link set lo up
router ip link set lo up
ip link add wan-server type veth peer name wan-rs
ip link set wan-rs netns "$router_pid"
ip link add wan-client type veth peer name wan-rc
ip link set wan-client netns "$client_pid"
ip link set wan-rc netns "$router_pid"
ip addr add 10.201.0.1/24 dev wan-server
ip link set wan-server up
router ip addr add 10.201.0.2/24 dev wan-rs
router ip addr add 10.202.0.1/24 dev wan-rc
router ip link set wan-rs up
router ip link set wan-rc up
client ip addr add 10.202.0.2/24 dev wan-client
client ip link set wan-client up
ip route add 10.202.0.0/24 via 10.201.0.2
client ip route add 10.201.0.0/24 via 10.202.0.1
router sysctl -qw net.ipv4.ip_forward=1
for iface in wan-rs wan-rc; do
  router tc qdisc add dev "$iface" root netem delay 40ms rate 100mbit limit 20000
done
export P2PSTREAM_WAN_CLIENT_PID="$client_pid"
"$2" -test.run '^TestPublicProxyWANThroughput$' -test.v -test.timeout 5m
