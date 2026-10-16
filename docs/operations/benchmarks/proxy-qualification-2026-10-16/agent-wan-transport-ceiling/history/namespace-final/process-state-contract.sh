#!/usr/bin/env bash
set -Eeuo pipefail
process_start_state() {
  python3 - "$1" <<'PY'
import sys
from pathlib import Path
try:
    text=Path(f"/proc/{int(sys.argv[1])}/stat").read_text()
except FileNotFoundError:
    raise SystemExit(3)
right=text.rfind(")")
if right < 0:
    raise SystemExit("malformed /proc PID stat")
fields=text[right+2:].split()
if len(fields) < 20:
    raise SystemExit("short /proc PID stat")
print(int(fields[19]),fields[0])
PY
}

process_start_ticks() {
  local line start state status
  if line=$(process_start_state "$1"); then
    read -r start state <<<"$line" || return 1
  else
    status=$?
    return "$status"
  fi
  printf '%s\n' "$start"
}

live=$(process_start_ticks $$)
[[ $live =~ ^[0-9]+$ && $live -gt 0 ]]
missing=99999999
set +e
process_start_state "$missing" > missing-state.stdout 2> missing-state.stderr
state_status=$?
process_start_ticks "$missing" > missing-ticks.stdout 2> missing-ticks.stderr
ticks_status=$?
set -e
[[ $state_status -eq 3 && $ticks_status -eq 3 && ! -s missing-state.stderr && ! -s missing-ticks.stderr ]]
process_start_state() { return 7; }
set +e
process_start_ticks $$ > generic.stdout 2> generic.stderr
generic_status=$?
set -e
[[ $generic_status -eq 7 ]]
printf '{"live_start_ticks":%s,"missing_state_status":%s,"missing_ticks_status":%s,"generic_status":%s,"status":"pass"}\n' "$live" "$state_status" "$ticks_status" "$generic_status" > process-state-contract-result.json
