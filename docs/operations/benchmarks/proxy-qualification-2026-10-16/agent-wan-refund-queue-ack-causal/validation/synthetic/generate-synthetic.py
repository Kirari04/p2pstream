import copy, json
from pathlib import Path
src=Path('docs/operations/benchmarks/proxy-qualification-2026-10-16/agent-wan-refund-emission-causal/results.jsonl')
out=Path(__import__('sys').argv[1])
rows=[json.loads(x) for x in src.read_text().splitlines() if x.strip()]
sha={
 'driver_binary_sha256':'dbbe77ffa01be06ea9e60827f29485d0d4f654d5e3d3c37af426a492b572c6a9',
 'batched_server_binary_sha256':'dbbe77ffa01be06ea9e60827f29485d0d4f654d5e3d3c37af426a492b572c6a9',
 'separate_server_binary_sha256':'c7b2eb8889d41251711c0d46dc30d05e6e3f471ce2a992e8a6347102d52a3f3c',
 'batched_agent_binary_sha256':'fc35f33359e69277a8cc25808faea3270789c92d86f872037dac63c20afe2ae1',
 'separate_agent_binary_sha256':'353066727ad1823eb44e65d86a62087514cd2d224fcbf69b484ba9f66313f48d',
 'common_scaffold_sha256':'dd1b8a0ef22a7e29fc112bdd201bb758031eaafbaff8e912f90b0e8e50fef53c',
 'separate_items_overlay_sha256':'b50b28cbafdb4b7609a547f781154446c484efe381b623dfac4524d5e01a4431',
}
ref='5eeccaaa797254d7bde4db9854a337a02c3bc28e'
for row in rows:
    batched=row['label']=='refund-gathered-control'
    row['label']='refund-batched-item-control' if batched else 'refund-separate-items-ack'
    p={**sha,'harness_sha256':'a'*64,'base_source_ref':ref,'batched_source_ref':ref,'separate_source_ref':ref}
    p['endpoint_server_binary_sha256']=p['batched_server_binary_sha256' if batched else 'separate_server_binary_sha256']
    p['endpoint_agent_binary_sha256']=p['batched_agent_binary_sha256' if batched else 'separate_agent_binary_sha256']
    row['provenance']=p
    if row['transfer_direction']=='download': row['useful_transfer_bytes_per_second']=25_000_000 if batched else 32_000_000
    else: row['useful_transfer_bytes_per_second']=30_000_000

def write(name, values):
    (out/name).write_text(''.join(json.dumps(x,separators=(',',':'))+'\n' for x in values))
write('synthetic-pass.jsonl',rows)
wrong=copy.deepcopy(rows); wrong[:32]=wrong[16:32]+wrong[:16]; write('synthetic-wrong-order.jsonl',wrong)
conservation=copy.deepcopy(rows); conservation[0]['errors']=1; write('synthetic-conservation.jsonl',conservation)
provenance=copy.deepcopy(rows); del provenance[0]['provenance']['common_scaffold_sha256']; write('synthetic-provenance.jsonl',provenance)
timing=copy.deepcopy(rows)
for row in timing:
    if row['label']=='refund-separate-items-ack' and row['transfer_direction']=='download': row['useful_transfer_bytes_per_second']=26_000_000
write('synthetic-timing-rejection.jsonl',timing)
