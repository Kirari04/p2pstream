#!/usr/bin/env bash
# SOURCE ONLY — NOT EXECUTED. Reviewed single-arm H2 complement invocation.
set -Eeuo pipefail
unset P2PSTREAM_QUALIFICATION_PROFILE_DIR P2PSTREAM_QUALIFICATION_PROFILE_MODE
arm='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/integration-source/run-wan-read-wait-arm.sh'
server='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-capacity-source/build/bin/current-server-wan-read-wait-capacity-audited.test'
agent='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/current-wan-prep/bin/current-agent.test'
cert='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/integration-source/runs/sustained-matched-protocol-1/tls/public.test.crt.pem'
key='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/integration-source/runs/sustained-matched-protocol-1/tls/public.test.key.pem'
output='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/integration-source/runs/sustained-h2-complement-1'
stdout='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/integration-source/runs/sustained-h2-complement-1.arm.stdout.log'
stderr='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/integration-source/runs/sustained-h2-complement-1.arm.stderr.log'
status='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/integration-source/runs/sustained-h2-complement-1.arm.status'
for path in "$output" "$stdout" "$stderr" "$status"; do
  [[ ! -e $path ]] || { echo "fresh H2 complement path already exists: $path" >&2; exit 2; }
done
[[ $(sha256sum "$arm" | awk '{print $1}') == 60573b4432d082b017627d185f8e8ff38a8a9265c04b16a8a958413a9ba6acd9 ]]
[[ $(sha256sum '/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/analyze_interned_trace.py' | awk '{print $1}') == ebb8e1a48b10f966df026a475d6217c83f117ac70e7a9195d66aadc7e1bdf895 ]]
[[ $(sha256sum "$server" | awk '{print $1}') == 57d77ddc5ee8ec6ab49cab0b7d0dd29641a70fa8598f0c7ba3eac73f46632ff5 ]]
[[ $(sha256sum "$agent" | awk '{print $1}') == 9e635d6e1e8fbd8651260eb081fb5fc7847438bc51c653a5f7785cbecde4a137 ]]
[[ $(sha256sum "$cert" | awk '{print $1}') == 8fe7f72a3e119d80cc5024f8fe1c9dadb7b4ba8e40fcee3511fed379a2e216f2 ]]
[[ $(sha256sum "$key" | awk '{print $1}') == 231d8d29b1dc9108c194d9d5df2065ffb76de38cea278c7a4d7333e8898ac8da ]]
[[ $(openssl x509 -in "$cert" -outform DER | sha256sum | awk '{print $1}') == db0a24013e5ff45e4ac69cbe5b14db1ea580e6543b6a592f3bab5a839bc3709f ]]
set +e
/usr/bin/bash "$arm"   --server-binary "$server"   --aggregate-agent-binary "$agent"   --server-source-ref 8ac4c8df8237e37777ba2fac0da6f949da17bdea   --aggregate-agent-source-ref 8ac4c8df8237e37777ba2fac0da6f949da17bdea   --protocol h2   --cert "$cert"   --key "$key"   --expected-leaf-sha256 db0a24013e5ff45e4ac69cbe5b14db1ea580e6543b6a592f3bab5a839bc3709f   --output-dir "$output" >"$stdout" 2>"$stderr"
arm_status=$?
set -e
printf '%s\n' "$arm_status" >"$status"
exit "$arm_status"
