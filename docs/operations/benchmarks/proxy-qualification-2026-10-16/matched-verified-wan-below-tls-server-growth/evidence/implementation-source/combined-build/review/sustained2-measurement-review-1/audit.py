#!/usr/bin/python3
from __future__ import annotations
import collections, hashlib, json
from pathlib import Path
ROOT=Path('/home/user/.codex/worktrees/astra-proxy-performance/p2pstream')
RUN=ROOT/'tmp/continuation-20261016/wan-below-tls-raw-write-source/implementation-source/final-integration-source/integration/runs/sustained-below-tls-2'
OUT=ROOT/'tmp/continuation-20261016/wan-below-tls-raw-write-source/implementation-source/combined-build/review/sustained2-measurement-review-1'
SEQ=(9,11,14,15)
def read(p): return json.loads(p.read_text())
def sha(p): return hashlib.sha256(p.read_bytes()).hexdigest()
def ns(v): return v['total_nanoseconds']

def extract(arm):
 r=RUN/arm; a=read(r/'attestation.json'); summary=read(r/'results.summary.json')
 assert a['accepted'] is False and a['protocol']==arm and a['row_count']==16 and a['size_mode']=='sustained'
 assert a['integrity_issues']==[] and a['conditioning_issues']==[]
 assert a['natural_endpoint_and_namespace_statuses'] is True and a['outer_runner_group_joined'] is True
 assert a['sender_terminal_closure_valid'] is True and a['raw_write_final_closure_valid'] is True
 assert summary['acceptance']['correctness'] is True and summary['acceptance']['response_copy_attribution_complete'] is True
 assert summary['acceptance']['transport_trace_complete'] is True and summary['acceptance']['verified_tls_and_protocol_every_response'] is True
 assert summary['acceptance']['expected_upstream_reuse'] is True
 rows={x['diagnostic_sequence']:x for x in map(json.loads,(r/'results.jsonl').read_text().splitlines())}
 records=[json.loads(x) for x in (r/'results.sender-wait-records.jsonl').read_text().splitlines()]
 intervals={x['sequence']:x for x in records if isinstance(x.get('sender_validation'),dict)}
 growth={x['sequence']:x for x in a['server_receive_growth_intervals']}
 joins={x['sequence']:x for x in a['sender_duration_intervals']}
 reuse={x['sequence']:x for x in summary['reuse_expectations']}
 assert sorted(intervals)==sorted(growth)==sorted(joins)==list(SEQ)
 selected=[]
 for seq in SEQ:
  e=intervals[seq]; validation=e['sender_validation']; assert e['valid'] is True and validation['accepted'] is True
  sel=validation['selected']; lane=sel['lane']; stream=sel['stream_id']; row=rows[seq]
  counters=[x for x in e['post_sender']['response']['send_counters'] if x['lane']==lane]; assert len(counters)==1
  timing=counters[0]['timing']; raw=sel['raw_write_activity']; g=growth[seq]
  assert joins[seq]['compound_tuple_join'] is True
  assert g['accepted'] is True and g['post_dropped_events']==0 and g['selected_event_count']==len(g['selected_events'])>0
  assert g['selected_identity']['lane']==lane and g['selected_identity']['stream_id']==stream
  assert g['selected_identity']['local_address']==counters[0]['remote_address'] and g['selected_identity']['remote_address']==counters[0]['local_address']
  assert raw['accepted'] is True and raw['issues']==[]
  assert raw['bools']['complete'] is True and raw['bools']['reset_performed'] is True
  assert raw['bools']['ambiguous'] is raw['bools']['overflow'] is raw['bools']['multiple_streams'] is False
  ints=raw['ints']; assert ints['active_envelopes']==ints['active_raw_writes']==ints['boundary_active_raw_writes']==0
  assert ints['boundary_outside_completions']==ints['envelope_overlaps']==ints['owner_generation_changes']==0
  assert ints['maximum_active_raw_writes']==1 and ints['envelope_zero_raw_calls']==0
  assert ints['envelope_count']==sel['frames'] and ints['envelope_payload_bytes']==sel['payload_bytes'] and ints['envelope_wire_bytes']==sel['wire_bytes']
  physical=ns(sel['durations']['physical_write_total']); contained=ns(raw['durations']['contained_raw_duration']); residual=ns(raw['durations']['residual_duration'])
  assert physical==ns(raw['durations']['envelope_duration'])==contained+residual
  assert raw['classes']['contained']['scalars']['calls']>0 and raw['classes']['outside']['scalars']['calls']>0
  assert raw['classes']['straddling']['scalars']['calls']==0
  assert timing['physical_error_frames']==timing['physical_short_frames']==timing['data_abandoned_frames']==0
  causes={k:ns(v) for k,v in timing['credit_wait_by_cause'].items()}; credit=ns(timing['credit_wait_total']); assert sum(causes.values())==credit
  copywrap=row['response_copy_attribution']; assert copywrap['overflow'] is False and len(copywrap['matches'])==1
  copy=copywrap['matches'][0]['copy']; assert copy['complete'] is True and copy['upstream_read']['bytes']==copy['downstream_write']['bytes']==268435456
  assert copy['read_errors']==copy['write_errors']==copy['short_writes']==0
  sessions=[x for x in e['post_server_capacity']['server_receive_growth']['credit_snapshot']['sessions'] if x['lane']==lane and x['session_identity']==g['selected_identity']['session_identity']]
  assert len(sessions)==1
  streams=[x for x in sessions[0]['snapshot']['streams'] if x['id']==stream]; assert len(streams)==1
  re=reuse[seq]; same_origin=None
  if re['kind']=='same-key-revisit': same_origin=re['observed_origin_connection_id']==re['prior_origin_connection_id']; assert same_origin is True and re['satisfied'] is True
  selected.append({'sequence':seq,'stage':e['stage'],'lane':lane,'stream_id':stream,'bytes_per_second':row['useful_transfer_bytes_per_second'],'wall_seconds':row['wall_seconds'],'reuse_kind':re['kind'],'reuse_satisfied':re['satisfied'],'idle_ns':re.get('elapsed_idle_ns'),'same_origin_connection':same_origin,'credit_wait_ns':credit,'credit_wait_by_cause_ns':causes,'completion_select_ns':ns(timing['data_completion_select_total']),'physical_write_ns':physical,'contained_raw_write_ns':contained,'tls_residual_ns':residual,'frames':sel['frames'],'contained_raw_calls':raw['classes']['contained']['scalars']['calls'],'outside_raw_calls':raw['classes']['outside']['scalars']['calls'],'straddling_raw_calls':0,'copy_upstream_read_ns':copy['upstream_read']['total_ns'],'copy_downstream_write_ns':copy['downstream_write']['total_ns'],'growth_event_count':g['selected_event_count'],'growth_outcomes':dict(collections.Counter(x['outcome'] for x in g['selected_events'])),'growth_dropped_events':0,'post_receive_limit':streams[0]['byte_receive_limit'],'compound_tuple_join':True})
 return {'attestation_accepted':False,'validation_issues':a['validation_issues'],'fixed_download_phases':summary['download_by_phase'],'selected_intervals':selected,'natural_closure':True}

arms={arm:extract(arm) for arm in ('h1','h2')}
final=read(RUN/'final-status.json')
assert final['accepted'] is False and final['performance_result'] is True and final['diagnostic_only'] is True
assert final['same_binaries_both_arms'] is final['same_verified_certificate_both_arms'] is final['pre_post_hashes_equal'] is True
assert final['h1']['status']==final['h2']['status']==1 and final['h1']['safe_to_continue'] is True
comparison=[]
for seq in SEQ:
 h1=next(x for x in arms['h1']['selected_intervals'] if x['sequence']==seq); h2=next(x for x in arms['h2']['selected_intervals'] if x['sequence']==seq)
 comparison.append({'sequence':seq,'h2_minus_h1_bytes_per_second':h2['bytes_per_second']-h1['bytes_per_second'],'h2_minus_h1_credit_wait_ns':h2['credit_wait_ns']-h1['credit_wait_ns'],'h2_minus_h1_physical_write_ns':h2['physical_write_ns']-h1['physical_write_ns'],'h2_minus_h1_contained_raw_write_ns':h2['contained_raw_write_ns']-h1['contained_raw_write_ns'],'h2_minus_h1_tls_residual_ns':h2['tls_residual_ns']-h1['tls_residual_ns'],'h2_minus_h1_copy_upstream_read_ns':h2['copy_upstream_read_ns']-h1['copy_upstream_read_ns'],'h2_minus_h1_copy_downstream_write_ns':h2['copy_downstream_write_ns']-h1['copy_downstream_write_ns']})
inputs=[RUN/'final-status.json']
for arm in ('h1','h2'):
 inputs += [RUN/arm/'attestation.json',RUN/arm/'results.summary.json',RUN/arm/'results.jsonl',RUN/arm/'results.sender-wait-records.jsonl',RUN/arm/'results.lifecycle.jsonl',RUN/arm/'raw-post-join-supervisor-validation.json']
report={'schema_version':1,'review_completed':True,'diagnostic_only':True,'cohort_accepted':False,'fixed_outcome':{'h1':'retained phase failed 29,700,000 B/s floor; steady phase passed','h2':'retained and steady phases failed 29,700,000 B/s floor','integrity_and_conditioning_both_arms':True,'natural_endpoint_scope_sender_raw_closure_both_arms':True},'arms':arms,'h2_minus_h1_selected':comparison,'architecture_interpretation':['The strongest consistent discriminator is sender credit on direction-cold downloads: H2 sequences 9, 11, and 14 spend 0.643-0.878 s more in credit wait than H1. Both protocols have zero credit wait on same-direction warm sequence 15, whose throughput is effectively equal.','Server receive-growth evidence has exact correct-direction lane/stream/tuple joins and zero drops. Sequences 9, 11, and 14 publish growth and wait for credit; sequence 15 publishes no growth and waits for no credit. This establishes a direction-history association, not a growth-policy defect.','Contained temporal raw-write occupancy accounts for nearly all agent physical DATA Write duration in every row. H2 raw occupancy is only about 69-80 ms above H1 on sequences 9, 14, and 15, but about 1.031 s above H1 on sequence 11. Raw/TLS occupancy alone therefore does not match the full repeated H2 slowdown pattern and remains unable to distinguish kernel/poller/TCP delivery.','Public H2 downstream Write totals are about 0.263-0.355 s above H1 for all selected rows, including fast sequence 15. Public Write overhead is observable but is not sufficient by itself to explain the three slow H2 rows.','The observed pattern is compatible with an interaction between public H2 consumption cadence and tunnel receive-credit replenishment on direction-cold streams. It does not determine whether credit wait is cause, consequence, or feedback, and does not justify a runtime policy fix from this cohort alone.','Agent sender/raw durations and server copy durations use different instrument boundaries and processes. Pairwise deltas are descriptive; durations across those observers are never summed or treated as exact wall-time partitions.'],'input_sha256':{str(p.relative_to(ROOT)):sha(p) for p in inputs}}
(OUT/'REVIEW.json').write_text(json.dumps(report,indent=2,sort_keys=True)+'\n')
