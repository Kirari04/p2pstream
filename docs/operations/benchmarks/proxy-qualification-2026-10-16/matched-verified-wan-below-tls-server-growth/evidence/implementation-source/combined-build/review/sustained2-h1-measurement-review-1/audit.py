#!/usr/bin/python3
from __future__ import annotations
import collections, hashlib, json
from pathlib import Path
ROOT=Path('/home/user/.codex/worktrees/astra-proxy-performance/p2pstream')
RUN=ROOT/'tmp/continuation-20261016/wan-below-tls-raw-write-source/implementation-source/final-integration-source/integration/runs/sustained-below-tls-2/h1'
OUT=ROOT/'tmp/continuation-20261016/wan-below-tls-raw-write-source/implementation-source/combined-build/review/sustained2-h1-measurement-review-1'
SEQ=(9,11,14,15)
def read(p): return json.loads(p.read_text())
def sha(p): return hashlib.sha256(p.read_bytes()).hexdigest()
def ns(v): return v['total_nanoseconds']
a=read(RUN/'attestation.json'); s=read(RUN/'results.summary.json')
assert a['accepted'] is False and a['protocol']=='h1' and a['row_count']==16 and a['size_mode']=='sustained'
assert a['integrity_issues']==[] and a['conditioning_issues']==[]
assert a['natural_endpoint_and_namespace_statuses'] is True and a['outer_runner_group_joined'] is True
assert a['sender_terminal_closure_valid'] is True and a['raw_write_final_closure_valid'] is True
assert a['validation_issues']==['retained-warmup download phase failed independent 29700000 B/s floor']
assert s['acceptance']['correctness'] is True and s['acceptance']['download_floor_every_phase'] is False
assert s['download_by_phase']['retained-warmup']['minimum_bytes_per_second']==29673138.349552546
assert s['download_by_phase']['steady']['minimum_bytes_per_second']==30266590.300495118
rows={x['diagnostic_sequence']:x for x in map(json.loads,(RUN/'results.jsonl').read_text().splitlines())}
records=[json.loads(x) for x in (RUN/'results.sender-wait-records.jsonl').read_text().splitlines()]
interval={x['sequence']:x for x in records if isinstance(x.get('sender_validation'),dict)}
growth={x['sequence']:x for x in a['server_receive_growth_intervals']}
sender_join={x['sequence']:x for x in a['sender_duration_intervals']}
assert sorted(interval)==list(SEQ) and sorted(growth)==list(SEQ) and sorted(sender_join)==list(SEQ)
reuse={x['sequence']:x for x in s['reuse_expectations']}
selected=[]
for seq in SEQ:
 e=interval[seq]; v=e['sender_validation']; assert e['valid'] is True and v['accepted'] is True
 sel=v['selected']; lane=sel['lane']; stream=sel['stream_id']
 matches=[x for x in e['post_sender']['response']['send_counters'] if x['lane']==lane]; assert len(matches)==1
 t=matches[0]['timing']; raw=sel['raw_write_activity']; g=growth[seq]; row=rows[seq]
 assert sender_join[seq]['compound_tuple_join'] is True
 assert g['accepted'] is True and g['post_dropped_events']==0 and g['selected_event_count']==len(g['selected_events'])>0
 assert g['selected_identity']['lane']==lane and g['selected_identity']['stream_id']==stream
 assert raw['accepted'] is True and raw['issues']==[]
 assert raw['bools']['complete'] is True and raw['bools']['reset_performed'] is True
 assert raw['bools']['ambiguous'] is raw['bools']['overflow'] is raw['bools']['multiple_streams'] is False
 ri=raw['ints']; assert ri['active_envelopes']==ri['active_raw_writes']==ri['boundary_active_raw_writes']==0
 assert ri['boundary_outside_completions']==ri['envelope_overlaps']==ri['owner_generation_changes']==0
 assert ri['maximum_active_raw_writes']==1 and ri['envelope_zero_raw_calls']==0
 assert ri['envelope_count']==sel['frames'] and ri['envelope_payload_bytes']==sel['payload_bytes'] and ri['envelope_wire_bytes']==sel['wire_bytes']
 physical=ns(sel['durations']['physical_write_total']); contained=ns(raw['durations']['contained_raw_duration']); residual=ns(raw['durations']['residual_duration'])
 assert physical==ns(raw['durations']['envelope_duration'])==contained+residual
 assert raw['classes']['straddling']['scalars']['calls']==0 and raw['classes']['contained']['scalars']['calls']>0
 assert t['physical_error_frames']==t['physical_short_frames']==t['data_abandoned_frames']==0
 causes={k:ns(x) for k,x in t['credit_wait_by_cause'].items()}; credit=ns(t['credit_wait_total']); assert sum(causes.values())==credit
 copy=row['response_copy_attribution']; assert copy['overflow'] is False and len(copy['matches'])==1
 copy=copy['matches'][0]['copy']; assert copy['complete'] is True and copy['upstream_read']['bytes']==copy['downstream_write']['bytes']==268435456
 sessions=[x for x in e['post_server_capacity']['server_receive_growth']['credit_snapshot']['sessions'] if x['lane']==lane]
 assert len(sessions)==1
 streams=[x for x in sessions[0]['snapshot']['streams'] if x['id']==stream]; assert len(streams)==1
 outcomes=dict(collections.Counter(x['outcome'] for x in g['selected_events']))
 selected.append({'sequence':seq,'stage':e['stage'],'lane':lane,'stream_id':stream,'bytes_per_second':row['useful_transfer_bytes_per_second'],'wall_seconds':row['wall_seconds'],'reuse_kind':reuse[seq]['kind'],'reuse_satisfied':reuse[seq]['satisfied'],'idle_ns':reuse[seq].get('elapsed_idle_ns'),'same_origin_connection':reuse[seq].get('observed_origin_connection_id')==reuse[seq].get('prior_origin_connection_id') if reuse[seq]['kind']=='same-key-revisit' else None,'credit_wait_ns':credit,'credit_wait_by_cause_ns':causes,'completion_select_ns':ns(t['data_completion_select_total']),'physical_write_ns':physical,'contained_raw_write_ns':contained,'tls_residual_ns':residual,'frames':sel['frames'],'contained_raw_calls':raw['classes']['contained']['scalars']['calls'],'outside_raw_calls':raw['classes']['outside']['scalars']['calls'],'straddling_raw_calls':0,'copy_upstream_read_ns':copy['upstream_read']['total_ns'],'copy_downstream_write_ns':copy['downstream_write']['total_ns'],'growth_event_count':g['selected_event_count'],'growth_outcomes':outcomes,'growth_dropped_events':0,'post_receive_limit':streams[0]['byte_receive_limit'],'compound_tuple_join':True})
report={'schema_version':1,'review_completed':True,'arm':'h1','run_accepted':False,'fixed_outcome':{'correctness':True,'conditioning':True,'natural_closure':True,'download_floor_every_phase':False,'floor_bytes_per_second':29700000,'retained_minimum_bytes_per_second':29673138.349552546,'steady_minimum_bytes_per_second':30266590.300495118,'failure':'retained sequence 9 is below the fixed floor'},'selected_intervals':selected,'interpretation':['All four selected H1 intervals have exact sender/raw/server-growth ownership joins, conservation, zero growth drops, and natural final closure.','Contained temporal raw-write occupancy accounts for nearly all measured physical DATA Write duration; TLS residual is about 108-113 ms per selected transfer. This does not identify kernel TCP wait or delivery.','Sequences 9, 11, and 14 show byte/stream credit waits and receive-limit growth. Same-direction retained sequence 15 has zero credit wait and no growth publication, confirming a warm receive-credit history on that stream.','The floor-failing sequence 9 and lowest steady sequence 14 each combine about 1.1-1.18 s credit wait with about 7.23-7.25 s contained raw occupancy. Faster sequence 11 has less of both. This localizes two contributors but does not establish either as the causal protocol discrepancy.','Server upstream Read dominates downstream Write in each row. Server copy and agent sender clocks/boundaries differ, so their durations are descriptive and are not added or subtracted.'],'input_sha256':{str(p.relative_to(ROOT)):sha(p) for p in [RUN/'attestation.json',RUN/'results.summary.json',RUN/'results.jsonl',RUN/'results.sender-wait-records.jsonl',RUN/'results.lifecycle.jsonl',RUN/'raw-post-join-supervisor-validation.json']}}
(OUT/'REVIEW.json').write_text(json.dumps(report,indent=2,sort_keys=True)+'\n')
