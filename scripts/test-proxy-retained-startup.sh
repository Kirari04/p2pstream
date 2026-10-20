#!/usr/bin/env bash
# Opt-in LOCAL experiment; root must separately own and authorize execution.
set -Eeuo pipefail
cd "$(dirname "$0")/.."
[[ $# -ge 2 && $1 == --output-dir ]] || { echo 'usage: test-proxy-retained-startup.sh --output-dir NEW_DIR [four singleton CPU overrides]' >&2; exit 2; }
out=$(realpath -m "$2")
shift 2
# Only the reviewed four optional CPU groups are forwarded; no workload flags.
forward=("$@")
while (($#)); do
  case "$1" in --server-cpus|--agent-cpus|--origin-cpus|--load-cpus) [[ $# -ge 2 ]]; shift 2 ;; *) exit 2 ;; esac
done
set -- "${forward[@]}"
[[ ! -e $out && -x ${P2PSTREAM_STARTUP_GO:?absolute reviewed Go executable required} ]] || exit 2
[[ $P2PSTREAM_STARTUP_GO == /* ]] || exit 2
export GIT_OPTIONAL_LOCKS=0
source_clean() {
  git diff --quiet HEAD -- && [[ -z $(git ls-files --others --exclude-standard) ]]
}
# Refuse dirty tracked or nonignored untracked inputs BEFORE creating output/builds.
source_clean || { echo "commit the reviewed source before qualification" >&2; exit 2; }
ref=$(git rev-parse HEAD)
mkdir -m 700 "$out"
# Preserve existing offline dependency caches before changing HOME.
export GOPATH=${GOPATH:-$HOME/go} GOCACHE=${GOCACHE:-$HOME/.cache/go-build}
export GOMODCACHE=${GOMODCACHE:-$GOPATH/pkg/mod}
export HOME="$out/private-home" TEST_TELEMETRY_DIR="$out/private-telemetry"
export XDG_CONFIG_HOME="$HOME/.config" TMPDIR="$out/private-tmp" GOTMPDIR="$out/go-tmp"
mkdir -p "$TEST_TELEMETRY_DIR" "$HOME/.config/go/telemetry" "$TMPDIR" "$GOTMPDIR"
printf 'off\n' >"$TEST_TELEMETRY_DIR/mode"
printf 'off\n' >"$HOME/.config/go/telemetry/mode"
export PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOMAXPROCS=4 LC_ALL=C GOENV=off GOWORK=off GOFLAGS=-mod=readonly
for name in ${!P2PSTREAM_QUALIFICATION_@}; do unset "$name"; done
go=$P2PSTREAM_STARTUP_GO
for package in server agent; do
  timeout -k 10 300 "$go" test -c -mod=readonly -p=2 -o "$out/$package.test" "./internal/$package" >"$out/$package.build.log" 2>&1
  timeout -k 2 30 "$go" version -m "$out/$package.test" >"$out/$package.metadata.txt" 2>&1
done
sha256sum "$go" /usr/bin/time scripts/qualify-proxy-agent-growth.sh "$out/server.test" "$out/agent.test" >"$out/inputs.sha256.txt"
[[ $(git rev-parse HEAD) == "$ref" ]] && source_clean
printf '%s\n' "$ref" >"$out/source-ref.txt"
# No historical binary, legacy arm, automatic retry, profiles or race timing.
if (
  # Linux Bash file-size units are KiB: this is a per-file limit, not a tree quota.
  ulimit -f 16384
  python3 -c 'import resource; assert resource.getrlimit(resource.RLIMIT_FSIZE)[0] == 16777216'
  timeout -k 20 660 scripts/qualify-proxy-agent-growth.sh --retained-startup-ci \
    --server-binary "$out/server.test" --aggregate-agent-binary "$out/agent.test" \
    --server-source-ref "$ref" --aggregate-agent-source-ref "$ref" \
    --output "$out/results.jsonl" "$@"
) >"$out/runtime.stdout.log" 2>"$out/runtime.stderr.log"; then status=0; else status=$?; fi
# Finite post-run text/log rejection; live tree/log caps and descendant ownership
# MUST come from the caller's separately reviewed owned supervisor receipt.
if ! python3 - "$out" <<'TEXT_CAP'
from pathlib import Path
import json,sys
root=Path(sys.argv[1]);total=0;logs=[];count=0
for p in root.rglob("*"):
    if p.is_symlink(): raise ValueError("unexpected output symlink")
    if not p.is_file(): continue
    count+=1
    if count>4096: raise ValueError("output file count exceeds bound")
    size=p.stat().st_size
    if p.suffix in (".log", ".json", ".jsonl", ".txt"): total+=size
    if p.suffix==".log" and size>16<<20: logs.append(str(p.relative_to(root)))
value={"named_text_bytes":total,"text_limit_bytes":256<<20,"log_limit_bytes":16<<20,"oversize_logs":logs,"accepted":total+4096<=256<<20 and not logs,"receipt_reserve_bytes":4096,"phase":"postrun_only_not_live_cap","external_owner_receipt_required":True}
(root/"text-cap.json").write_text(json.dumps(value,sort_keys=True)+"\n")
if not value["accepted"]: raise SystemExit(1)
TEXT_CAP
then status=1; fi
[[ $(git rev-parse HEAD) == "$ref" ]] && source_clean || status=1
sha256sum --check "$out/inputs.sha256.txt" >"$out/inputs-postcheck.txt" 2>&1 || status=1
# Runtime/private readiness/certificate files are NOT a publication selection.
# Exit0 alone is NOT qualification: caller must verify actual owned cleanup/caps.
exit "$status"
