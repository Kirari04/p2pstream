#!/usr/bin/env bash
set -Eeuo pipefail

root=$(realpath "$(dirname "$0")/..")
runner="$root/generated/qualify-proxy-agent-growth-wan-copy-attribution.sh"
copy_validator="$root/validate-copy-record.py"
fixture="$root/contracts/arm-validator-attempt1/valid-smoke/results.jsonl"
output=${1:?new output directory required}
[[ ! -e $output ]] || { echo "output already exists: $output" >&2; exit 2; }
mkdir -p "$output"

awk '
  /^python3 - "\$output" "\$summary"/ { capture=1; next }
  capture && /^PY$/ { exit }
  capture { print }
' "$runner" > "$output/analyzer.py"
cp "$fixture" "$output/valid-results.jsonl"
fingerprint=2222222222222222222222222222222222222222222222222222222222
python3 "$output/analyzer.py" "$output/valid-results.jsonl" "$output/valid-summary.json" 2 1 0 0 h1 "$fingerprint" "$copy_validator"
jq -e '
  .classification == "observational-complete" and
  .acceptance.response_copy_attribution_complete == true and
  .copy_attribution_issues_by_sequence == {} and
  (.copy_attribution_interpretation | contains("Inter-read gaps include downstream Write time")) and
  (.copy_attribution_interpretation | contains("POST observations cover only the 32-byte response copy"))
' "$output/valid-summary.json" >/dev/null

python3 - "$output/valid-results.jsonl" "$output/missing-copy-results.jsonl" <<'PY'
import json, pathlib, sys
rows = [json.loads(line) for line in pathlib.Path(sys.argv[1]).read_text().splitlines()]
rows[0]["response_copy_attribution"]["matches"] = []
pathlib.Path(sys.argv[2]).write_text("".join(json.dumps(row) + "\n" for row in rows))
PY
set +e
python3 "$output/analyzer.py" "$output/missing-copy-results.jsonl" "$output/missing-copy-summary.json" 2 1 0 0 h1 "$fingerprint" "$copy_validator" \
  > "$output/missing-copy.stdout.log" 2> "$output/missing-copy.stderr.log"
status=$?
set -e
printf '%s\n' "$status" > "$output/missing-copy.status"
[[ $status != 0 ]]
jq -e '
  .classification == "invalid" and
  .acceptance.response_copy_attribution_complete == false and
  (.copy_attribution_issues_by_sequence["1"] | any(contains("correlation/cardinality")))
' "$output/missing-copy-summary.json" >/dev/null
printf '%s\n' "runner analyzer contracts passed"
