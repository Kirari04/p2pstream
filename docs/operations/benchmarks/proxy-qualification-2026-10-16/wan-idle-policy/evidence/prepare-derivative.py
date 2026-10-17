"""Source preparation only; no validator, namespace, build or workload execution."""
from pathlib import Path
import difflib,hashlib,json
ROOT=Path(__file__).resolve().parent
OLD=ROOT.parent/'wan-controller-screen-20261017'
INTEGRATION=OLD/'runner-source/integration'
def replace_one(text,old,new):
 if text.count(old)!=1:raise ValueError('source insertion count differs: '+old[:80])
 return text.replace(old,new,1)
def save(name,text,original):
 p=ROOT/name;p.write_text(text);p.chmod(0o755)
 (ROOT/(name+'.diff')).write_text(''.join(difflib.unified_diff(original.splitlines(True),text.splitlines(True),fromfile=str(INTEGRATION/name),tofile=str(p))))
old=(INTEGRATION/'generated/qualify-proxy-agent-growth-wan-below-tls.sh').read_text();s=old
s=replace_one(s,'set -Eeuo pipefail\n[[ ${P2PSTREAM_QUALIFICATION_DATA_CONTROLLER-}', 'set -Eeuo pipefail\n[[ ${P2PSTREAM_QUALIFICATION_DATA_CONTROLLER-} == cubic && ${P2PSTREAM_IDLE_ARM_INDEX-} =~ ^[012]$ ]] || { echo \"exact all-CUBIC idle-policy arm required\" >&2; exit 125; }\n[[ ${P2PSTREAM_QUALIFICATION_DATA_CONTROLLER-}')
s=replace_one(s,'  cd "$(dirname "$0")/.."','  cd "$(dirname "$0")"')
configure='''# Private idle policy is configured before origin/agent TCP creation.
idle_proof_dir=$(dirname "$output")
python3 -I -B "'''+str(ROOT/'idle-policy-proof.py')+'''" configure \\
  "$idle_proof_dir/idle-configured.json" "$P2PSTREAM_IDLE_ARM_INDEX" "$idle_proof_dir/idle-host-pre.json" \\
  "$agent_namespace_pid" "$agent_namespace_start_ticks" "$agent_namespace_identity" \\
  "$router_namespace_pid" "$router_namespace_start_ticks" "$router_namespace_identity"
'''
s=replace_one(s,'ip link set lo up\nagent_ns ip link set lo up',configure+'ip link set lo up\nagent_ns ip link set lo up')
endpoint='''  python3 -I -B "'''+str(ROOT/'idle-policy-proof.py')+'''" endpoints \\
    "$idle_proof_dir/idle-endpoints-PHASE.json" PHASE "$P2PSTREAM_IDLE_ARM_INDEX" "$idle_proof_dir/idle-configured.json" \\
    "$agent_pid" "$(jq -r .pid "$origin_ready")" "$server_pid" \\
    "$agent_ready" "$origin_ready" "$server_ready"
'''
s=replace_one(s,'  wait_mode "$server_admin" "$mode" "$expected_bulk"','  wait_mode "$server_admin" "$mode" "$expected_bulk"\n'+endpoint.replace('PHASE','endpoint-binding'))
s=replace_one(s,'\tlocal sender_terminal_status=0 sender_terminal_validation_status=0',endpoint.replace('PHASE','post-workload')+'\tlocal sender_terminal_status=0 sender_terminal_validation_status=0')
# These exact legacy transfer-function bytes are preserved, including taskset.
old_copy=old[old.index('run_transfer() {'):old.index('\nrun_tiny_response()',old.index('run_transfer() {'))] if '\nrun_tiny_response()' in old else None
if old_copy is None:
 begin=old.index('run_transfer() {');end=old.index('\nrun_maybe_sender_wait_transfer()',begin);old_copy=old[begin:end]
if old_copy not in s:raise ValueError('timed transfer function changed')
save('runner.sh',s,old)
old=(INTEGRATION/'run-wan-below-tls-arm-joined-ps.sh').read_text();s=old
s=replace_one(s,'expected_terminal_build_receipt_sha256=$(cat "$(dirname "$0")/../../REVIEWED-RECEIPT-SHA256")',f'expected_terminal_build_receipt_sha256=$(cat "{OLD}/REVIEWED-RECEIPT-SHA256")')
s=replace_one(s,'integration_dir=$(cd "$(dirname "$0")" && pwd)',f'integration_dir="{INTEGRATION}"')
s=replace_one(s,'runner="$integration_dir/generated/qualify-proxy-agent-growth-wan-below-tls.sh"',f'runner="{ROOT}/runner.sh"')
s=replace_one(s,'allowed_root=$(realpath -m "$integration_dir/runs")',f'allowed_root="{ROOT}/runs"')
s=replace_one(s,'308ab9ef7e9cb2b3c03014707203824ed19bf74486a8404bc1f2a1c336286416',hashlib.sha256((ROOT/'runner.sh').read_bytes()).hexdigest())
s=replace_one(s,'export TMPDIR="$output_dir/tmp"','export TMPDIR="$output_dir/tmp"\n'+f'python3 -I -B "{ROOT}/idle-policy-proof.py" host host-pre "$output_dir/idle-host-pre.json"')
s=replace_one(s,'write_hashes "$post_hashes"',f'python3 -I -B "{ROOT}/idle-policy-proof.py" host host-post "$output_dir/idle-host-post.json"\nwrite_hashes "$post_hashes"')
save('arm-wrapper.sh',s,old)
commands=json.loads((OLD/'COMMANDS-policy.json').read_text())
for i,c in enumerate(commands['arms']):
 c['argv']=[str(ROOT/'arm-wrapper.sh') if x==str(INTEGRATION/'run-wan-below-tls-arm-joined-ps.sh') else x for x in c['argv']]
 previous=c['output_dir'];c['output_dir']=str(ROOT/f'runs/idle-101-once/arm-{i}-idle-{(1,0,1)[i]}');c['argv']=[c['output_dir'] if x==previous else x for x in c['argv']]
 c.update(controller='cubic',idle_policy=(1,0,1)[i],env={'P2PSTREAM_QUALIFICATION_DATA_CONTROLLER':'cubic','P2PSTREAM_IDLE_ARM_INDEX':str(i)})
(ROOT/'COMMANDS.json').write_text(json.dumps(commands,indent=2,sort_keys=True)+'\n')
