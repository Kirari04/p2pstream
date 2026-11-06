#!/usr/bin/env bash
# Finite slow-reader and controlled-pressure cases in disposable cgroups. No
# host ports, sysctls, memory limits or networking are modified; OOM is
# contained in each container.
set -Eeuo pipefail
cd "$(dirname "$0")/.."
mkdir -p tmp/proxy-memory
go test -c -o tmp/proxy-memory/server.test ./internal/server
for test_name in TestPublicOutboundTCPQueueLowatBurst TestPublicAdmissionControlledMemoryPressure; do
  docker run --rm --network=none --memory=512m --memory-swap=512m \
    --cpus=2 --pids-limit=128 --read-only --cap-drop=ALL \
    --security-opt=no-new-privileges --tmpfs /tmp:rw,nosuid,nodev,size=16m \
    --mount "type=bind,source=$(pwd)/tmp/proxy-memory/server.test,target=/server.test,readonly" \
    --env P2PSTREAM_MEMORY_STRESS=isolated-512m \
    --entrypoint /server.test debian:trixie-slim \
    -test.run "^${test_name}$" -test.v -test.timeout=30s
done
