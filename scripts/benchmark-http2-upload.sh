#!/usr/bin/env bash
# Finite HTTP/2 upload-capacity diagnostic in disposable namespaces. The
# router carries 40 ms netem in each direction and shapes each leg to
# 500 Mbit/s, leaving enough link capacity above the 100 Mbit/s BDP point
# under test. No host routes, qdiscs, or sysctls are modified.
set -Eeuo pipefail
script_path=$(readlink -f "${BASH_SOURCE[0]}")

if [[ ${1:-} != --inside ]]; then
  cd "$(dirname "$script_path")/.."
  mkdir -p tmp/throughput-profiling/http2
  go test -c -o tmp/throughput-profiling/http2/http2_capacity.test ./internal/server
  export P2PSTREAM_HTTP2_CAPACITY_PARENT_NETNS
  P2PSTREAM_HTTP2_CAPACITY_PARENT_NETNS=$(readlink /proc/self/ns/net)
  if unshare --user --map-root-user --net true 2>/dev/null; then
    exec unshare --user --map-root-user --net "$script_path" --inside "$(pwd)/tmp/throughput-profiling/http2/http2_capacity.test"
  fi
  exec sudo -n --preserve-env=P2PSTREAM_HTTP2_CAPACITY_PARENT_NETNS unshare --net "$script_path" --inside "$(pwd)/tmp/throughput-profiling/http2/http2_capacity.test"
fi

[[ -n ${P2PSTREAM_HTTP2_CAPACITY_PARENT_NETNS:-} && $(readlink /proc/self/ns/net) != "$P2PSTREAM_HTTP2_CAPACITY_PARENT_NETNS" ]] || {
  echo "Refusing to configure networking outside a disposable namespace" >&2
  exit 1
}

unshare --net sleep 600 &
client_pid=$!
unshare --net sleep 600 &
router_pid=$!
trap 'kill "$client_pid" "$router_pid" 2>/dev/null || true; wait || true' EXIT
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
ip link add capacity-server type veth peer name capacity-rs
ip link set capacity-rs netns "$router_pid"
ip link add capacity-client type veth peer name capacity-rc
ip link set capacity-client netns "$client_pid"
ip link set capacity-rc netns "$router_pid"
ip addr add 10.211.0.1/24 dev capacity-server
ip link set capacity-server up
router ip addr add 10.211.0.2/24 dev capacity-rs
router ip addr add 10.212.0.1/24 dev capacity-rc
router ip link set capacity-rs up
router ip link set capacity-rc up
client ip addr add 10.212.0.2/24 dev capacity-client
client ip link set capacity-client up
ip route add 10.212.0.0/24 via 10.211.0.2
client ip route add 10.211.0.0/24 via 10.212.0.1
router sysctl -qw net.ipv4.ip_forward=1
for iface in capacity-rs capacity-rc; do
  router tc qdisc add dev "$iface" root netem delay 40ms rate 500mbit limit 40000
done
echo "isolated router capacity:" >&2
router tc qdisc show dev capacity-rs >&2
router tc qdisc show dev capacity-rc >&2
export P2PSTREAM_HTTP2_CAPACITY_CLIENT_PID="$client_pid"
set +e
"$2" -test.run '^(TestHTTP2UploadCapacity|TestPublicHTTP2LargeReceiveWindowIsAdvertisedOnWire)$' -test.v -test.timeout 5m
status=$?
set -e
echo "post-run router statistics:" >&2
router tc -s qdisc show dev capacity-rs >&2 || true
router tc -s qdisc show dev capacity-rc >&2 || true
exit "$status"
