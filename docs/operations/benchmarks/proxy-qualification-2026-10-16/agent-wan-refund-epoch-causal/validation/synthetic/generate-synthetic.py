import copy, json
from pathlib import Path
src=Path('docs/operations/benchmarks/proxy-qualification-2026-10-16/agent-wan-refund-emission-causal/results.jsonl')
out=Path(__import__('sys').argv[1])
rows=[json.loads(x) for x in src.read_text().splitlines() if x.strip()]
sha={
 'driver_binary_sha256':'453924e217f211e575440613925c27651e7c14fa3a3316851469ebd70c4b79aa',
 'control_server_binary_sha256':'453924e217f211e575440613925c27651e7c14fa3a3316851469ebd70c4b79aa',
 'deferred_server_binary_sha256':'ad7f3b2605f9375fa2f5015a73dd10e6f32946124fff21e7494f7185d5019699',
 'control_agent_binary_sha256':'bae850a2c81085148098d90f4d27dd88617a5023f3dd67665bda742806ae6dcf',
 'deferred_agent_binary_sha256':'3a7b45294921e633f9e13a825c8ec572c43e45076aa6e243b5f3baeeb6ddcbde',
 'common_scaffold_sha256':'3fa10c967b69d4d371c64dd6bb2ca175ffbd9f98d80193d3d41b2722809bfe59',
 'deferred_global_overlay_sha256':'d07cac9d6f5dac0311416282c0c0e19ece2d3369e1d372abc43b406b3e33b81d',
}
ref='5eeccaaa797254d7bde4db9854a337a02c3bc28e'
for row in rows:
    control=row['label']=='refund-gathered-control'
    row['label']='refund-current-batch-control' if control else 'refund-streams-before-global-epoch'
    p={**sha,'harness_sha256':'a'*64,'base_source_ref':ref,'control_source_ref':ref,'deferred_source_ref':ref}
    p['endpoint_server_binary_sha256']=p['control_server_binary_sha256' if control else 'deferred_server_binary_sha256']
    p['endpoint_agent_binary_sha256']=p['control_agent_binary_sha256' if control else 'deferred_agent_binary_sha256']
    row['provenance']=p
    if row['transfer_direction']=='download': row['useful_transfer_bytes_per_second']=25_000_000 if control else 32_000_000
    else: row['useful_transfer_bytes_per_second']=30_000_000

def write(name, values):
    (out/name).write_text(''.join(json.dumps(x,separators=(',',':'))+'\n' for x in values))
write('synthetic-pass.jsonl',rows)
wrong=copy.deepcopy(rows); wrong[:32]=wrong[16:32]+wrong[:16]; write('synthetic-wrong-order.jsonl',wrong)
conservation=copy.deepcopy(rows); conservation[0]['errors']=1; write('synthetic-conservation.jsonl',conservation)
provenance=copy.deepcopy(rows); del provenance[0]['provenance']['common_scaffold_sha256']; write('synthetic-provenance.jsonl',provenance)
wrong_status=copy.deepcopy(rows); wrong_status[0]['status_codes']={'204':1}; write('synthetic-wrong-status.jsonl',wrong_status)
wrong_protocol=copy.deepcopy(rows); wrong_protocol[0]['observed_http_major']=1; write('synthetic-wrong-protocol.jsonl',wrong_protocol)
timing=copy.deepcopy(rows)
for row in timing:
    if row['label']=='refund-streams-before-global-epoch' and row['transfer_direction']=='download': row['useful_transfer_bytes_per_second']=26_000_000
write('synthetic-timing-rejection.jsonl',timing)
