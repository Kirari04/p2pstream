#!/usr/bin/env bash
set -Eeuo pipefail
root=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream
compact="$root/tmp/continuation-20261016/wan-read-wait-stack-intern-source"
integration="$compact/integration-source"
capacity="$root/tmp/continuation-20261016/wan-read-wait-capacity-source"
checkpoint="$compact/review/compact-integration-source-checkpoint-2"
(cd "$checkpoint" && sha256sum -c SHA256SUMS.txt)
for script in "$integration/run-wan-read-wait-arm.sh" "$integration/run-wan-read-wait-diagnostic.sh"; do
  bash -n "$script"
done
python3 - "$integration/validate-wan-read-wait-arm.py" <<'PY'
import ast, pathlib, sys
ast.parse(pathlib.Path(sys.argv[1]).read_text())
PY
[[ $(sha256sum "$capacity/generated/qualify-proxy-agent-growth-wan-read-wait.sh" | awk '{print $1}') == e9e453206cd479e1d29b78c05cf4d0eaf0cbaefe3c6ff13f0ba9594235c3bea0 ]]
[[ $(sha256sum "$compact/run_interned_trace_parser.py" | awk '{print $1}') == 719eac5dac37a0dc686283025e7005aba3cbf5408436b6257e6fde8811a50df8 ]]
[[ $(sha256sum "$compact/intern_parsed_trace.py" | awk '{print $1}') == 4d1bbcdd2140d148741775842e1ba9d2d74f2541b15933a6232b18cf3473e793 ]]
[[ $(sha256sum "$compact/stack_intern_format.py" | awk '{print $1}') == f370243b753a94a122157e9b0b13eb535065fe88746269bd70de5475d82e4dbf ]]
[[ $(sha256sum "$compact/analyze_interned_trace.py" | awk '{print $1}') == ebb8e1a48b10f966df026a475d6217c83f117ac70e7a9195d66aadc7e1bdf895 ]]
[[ $(sha256sum "$integration/COMPACT-CHECKPOINT.json" | awk '{print $1}') == 7a851aca80494e1b12d4e9dd4c94406f02445cf067e3b0c497dc679ab0fd2cbc ]]
python3 - "$integration/run-wan-read-wait-arm.sh" "$integration/validate-wan-read-wait-arm.py" <<'PY'
import ast, pathlib, sys
arm = pathlib.Path(sys.argv[1]).read_text()
validator = pathlib.Path(sys.argv[2]).read_text()
wait = arm.index('wait "$runner_pid"')
offline = arm.index('trace_reduction_dir="$output_dir/read-wait-reductions"')
assert wait < offline
assert 'if ((smoke)); then trace_sequences=(9 11 13); else trace_sequences=(9 11 14 15); fi' in arm
assert '.parsed.txt' not in arm and 'parsed_path' not in validator
assert '--warmup-mib 256' in arm and '--steady-mib 256' in arm
assert '--transfer-repetitions 3' in arm and '--required-pool-mib 8' in arm and '--rtt-ms 80' in arm
module = ast.parse(validator)
required = None
for node in ast.walk(module):
    if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id == 'required_hashes' for target in node.targets):
        required = ast.literal_eval(node.value)
assert required is not None
for name in ('trace_parser_adapter', 'trace_canonicalizer', 'trace_format', 'trace_focused_summary',
             'trace_lifecycle_summary', 'trace_compact_checkpoint'):
    assert name in required

identity_function = next(node for node in module.body
                         if isinstance(node, ast.FunctionDef) and node.name == 'valid_compact_child_identities')
identity_module = ast.Module(body=[identity_function], type_ignores=[])
namespace = {}
exec(compile(ast.fix_missing_locations(identity_module), str(sys.argv[2]), 'exec'), namespace)
valid = namespace['valid_compact_child_identities']
assert valid({'pid': 101, 'pgid': 101}, {'pid': 202, 'pgid': 202}) is True
assert valid({'pid': None, 'pgid': None}, {'pid': 202, 'pgid': 202}) is False
assert valid({'pid': 101, 'pgid': 101}, {'pid': 101, 'pgid': 101}) is False
assert valid({'pid': 101, 'pgid': 999}, {'pid': 202, 'pgid': 202}) is False
assert valid({'pid': True, 'pgid': True}, {'pid': 202, 'pgid': 202}) is False
PY
printf 'static compact integration checks passed\n'
