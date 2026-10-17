#!/usr/bin/env bash
set -Eeuo pipefail
root=$1
wrapper=scripts/qualify-proxy-agent-transport-ceiling-stability.sh
base=(--runner scripts/qualify-proxy-agent-transport-ceiling.sh
 --server-binary tmp/transport-ceiling-bundle.7bc5148.final/bin/transport-ceiling-server.test
 --agent-binary tmp/transport-ceiling-bundle.7bc5148.final/bin/transport-ceiling-agent.test
 --source-ref 326e4a0016ff14d818eb0483d7789849db0a9fde
 --expected-payload-sha256 ad818ce955253fd9030cbefa352527321b308cfc574cb4bbee9cbcd0d4930790
 --build-manifest tmp/transport-ceiling-bundle.7bc5148.final/build-manifest.json
 --expected-runner-sha256 a38fa1ae4f4a9196c06f0e14127a598005d5cbed00e33224d507780aef5219b8
 --expected-server-sha256 2253b2bc3423836c2a65cd503791e7e29d2516427c8da532c7d6956f3cdb86b0
 --expected-agent-sha256 d5bdfffda469e2f2fafafe70ebe9715531b2bcc08d61228ef400013014ee587a
 --expected-build-manifest-sha256 f5ab6117a1fb90992ba0efb9c57b3b85029358277bf31ee7a48c1c84207b9044
 --preflight-only)
run_case() {
 local name=$1 expected=$2; shift 2
 set +e
 unshare --user --map-root-user --mount "$wrapper" "${base[@]}" --output-dir "$root/$name" "$@" >"$root/$name.log" 2>&1
 local status=$?
 set -e
 printf '%s\t%s\t%s\n' "$name" "$expected" "$status" | tee -a "$root/pin-negative-results.tsv"
 [[ $status -eq $expected ]]
}
# Later duplicate options win in the wrapper parser.
run_case wrong-source 2 --source-ref 026e4a0016ff14d818eb0483d7789849db0a9fde
run_case wrong-runner-sha 2 --expected-runner-sha256 b38fa1ae4f4a9196c06f0e14127a598005d5cbed00e33224d507780aef5219b8
run_case wrong-manifest-sha 2 --expected-build-manifest-sha256 e5ab6117a1fb90992ba0efb9c57b3b85029358277bf31ee7a48c1c84207b9044
run_case wrong-payload 2 --expected-payload-sha256 bd818ce955253fd9030cbefa352527321b308cfc574cb4bbee9cbcd0d4930790
run_case wrong-agent-sha 2 --expected-agent-sha256 e5bdfffda469e2f2fafafe70ebe9715531b2bcc08d61228ef400013014ee587a
