#!/usr/bin/env bash
set -Eeuo pipefail
root=$1
wrapper=scripts/qualify-proxy-agent-transport-ceiling-stability.sh
base=(--runner scripts/qualify-proxy-agent-transport-ceiling.sh
 --server-binary tmp/transport-ceiling-bundle.ccb9c9c.eof/bin/transport-ceiling-server.test
 --agent-binary tmp/transport-ceiling-bundle.ccb9c9c.eof/bin/transport-ceiling-agent.test
 --source-ref 40e1046c2f68ee3d545dfd582cd424c2f3604b2a
 --expected-payload-sha256 ad818ce955253fd9030cbefa352527321b308cfc574cb4bbee9cbcd0d4930790
 --build-manifest tmp/transport-ceiling-bundle.ccb9c9c.eof/build-manifest.json
 --expected-runner-sha256 a38fa1ae4f4a9196c06f0e14127a598005d5cbed00e33224d507780aef5219b8
 --expected-server-sha256 98d67ff45e6f4f0da317b7929c9cf7dce704f3946bcf710c9f4a5ae66f254c67
 --expected-agent-sha256 62a0ba54f0d62310e23607bf89a2738b951882529913c71be15a89d42849c9ac
 --expected-build-manifest-sha256 8b9e6d5ab40f73fdab67a66f56406114c2bfc0f4c1f6c244101f8dbd854d5eb6
 --preflight-only --fixture-smoke)
run_case(){ local name=$1; shift; set +e; unshare --user --map-root-user --mount "$wrapper" "${base[@]}" --output-dir "$root/$name" "$@" >"$root/$name.log" 2>&1; status=$?; set -e; printf '%s\t2\t%s\n' "$name" "$status" | tee -a "$root/results.tsv"; [[ $status -eq 2 ]]; }
: > "$root/results.tsv"
run_case old-source-ref --source-ref 326e4a0016ff14d818eb0483d7789849db0a9fde
run_case old-server-sha --expected-server-sha256 2253b2bc3423836c2a65cd503791e7e29d2516427c8da532c7d6956f3cdb86b0
run_case old-agent-sha --expected-agent-sha256 d5bdfffda469e2f2fafafe70ebe9715531b2bcc08d61228ef400013014ee587a
run_case old-manifest-sha --expected-build-manifest-sha256 f5ab6117a1fb90992ba0efb9c57b3b85029358277bf31ee7a48c1c84207b9044
