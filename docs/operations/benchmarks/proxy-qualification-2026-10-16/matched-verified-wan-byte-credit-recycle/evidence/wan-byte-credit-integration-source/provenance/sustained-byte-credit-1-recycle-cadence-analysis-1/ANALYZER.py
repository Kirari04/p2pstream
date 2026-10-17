#!/usr/bin/python3.14
from __future__ import annotations
from collections import Counter
import hashlib, json, pathlib, statistics

ROOT=pathlib.Path('/home/user/.codex/worktrees/astra-proxy-performance/p2pstream')
RUN=ROOT/'tmp/continuation-20261016/wan-byte-credit-integration-source/integration-source-6/integration/runs/sustained-byte-credit-1'
SRC=ROOT/'tmp/continuation-20261016/wan-byte-credit-integration-source/combined-build-source/runs/audited-build-attempt-5/source'
OLD=ROOT/'tmp/continuation-20261016/wan-byte-credit-integration-source/provenance/sustained-byte-credit-1-trace-analysis-1'
CLEAN=(9,11)

def load(p): return json.loads(p.read_text())
def sha(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(1024*1024),b''): h.update(b)
 return h.hexdigest()
def stat(xs):
 return {'count':len(xs),'sum_ns':sum(xs),'median_ns':int(statistics.median(xs)) if xs else 0,'min_ns':min(xs) if xs else 0,'max_ns':max(xs) if xs else 0}
def trace(rows,lane):
 x=[r['trace'] for r in rows if r.get('lane')==lane]
 if len(x)!=1: raise ValueError('lane cardinality')
 return x[0]

def main():
 final=load(RUN/'final-status.json')
 if final.get('accepted') is not False or final.get('size_mode')!='sustained': raise ValueError('run disposition')
 old=load(OLD/'ANALYSIS.json')
 if old.get('input_trace_integrity_accepted') is not True: raise ValueError('prior analysis')
 inputs=[RUN/'final-status.json',OLD/'ANALYSIS.json',OLD/'SHA256SUMS.txt']
 sources=[
  SRC/'third_party/yamux/stream.go',
  SRC/'third_party/yamux/stream_window_update_send_qualification.go',
  SRC/'third_party/yamux/qualification_byte_credit_trace.go',
  SRC/'internal/tunnel/growing_conn.go',
  SRC/'internal/tunnel/qualification_growth_trace.go',
  SRC/'internal/tunnel/yamux.go',
  SRC/'internal/tunnel/protocol.go',
  SRC/'third_party/yamux/aggregate_window.go',
  SRC/'internal/server/proxy_copy_attribution.go',
  SRC/'internal/server/reverse_proxy_buffer_pool.go',
  SRC/'internal/server/public_routing.go',
  ROOT/'tmp/continuation-20261016/wan-byte-credit-integration-source/combined-build-source/runs/audited-build-attempt-5/audited-byte-credit-build-receipt.json',
  pathlib.Path('/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/src/net/http/h2_bundle.go'),
 ]
 inputs += sources
 intervals=[]
 for arm in ('h1','h2'):
  attp=RUN/arm/'attestation.json'; rowp=RUN/arm/'results.jsonl'
  inputs += [attp,rowp]
  att=load(attp)
  if att.get('integrity_issues')!=[] or att.get('classification')!='inconclusive-upstream-reuse': raise ValueError('arm integrity')
  sums={x['sequence']:x for x in att['byte_credit_recycle_intervals']}
  rows=[json.loads(x) for x in rowp.read_text().splitlines()]
  for seq in CLEAN:
   s=sums[seq]
   if s.get('accepted') is not True or s.get('issues')!=[]: raise ValueError('selected interval')
   lane=s['selected_lane']; sid=s['selected_stream_id']; controls=RUN/arm/'results.sender-wait-controls'
   cp=controls/f'sequence-{seq}-post-capacity.json'; sp=controls/f'sequence-{seq}-post-sender.json'
   inputs += [cp,sp]
   cap=load(cp)['response']['server_receive_growth']; snd=load(sp)['response']
   if cap.get('dropped_events')!=0: raise ValueError('growth drops')
   rt=trace(cap['byte_credit_traces'],lane); pt=trace(snd['byte_credit_traces'],lane)
   for t in (rt,pt):
    if (t.get('complete') is not True or t.get('reset_performed') is not True or t.get('ambiguous') is not False
      or t.get('overflow') is not False or t.get('orphan_overflow') is not False
      or t.get('dropped_receiver_records')!=0 or t.get('dropped_peer_records')!=0 or t.get('dropped_wait_records')!=0): raise ValueError('trace health')
   rr=[x for x in rt['receiver_records'] if x.get('stream_id')==sid]
   pp=[x for x in pt['peer_records'] if x.get('stream_id')==sid]
   rj=[(x['relative_ordinal'],x['delta'],x['cumulative']) for x in rr]
   pj=[(x['relative_ordinal'],x['delta'],x['cumulative']) for x in pp]
   if rj!=pj or [x[0] for x in rj]!=list(range(1,len(rr)+1)): raise ValueError('ordered join')
   selected_growth=[e for e in cap['events'] if e.get('lane')==lane and e.get('stream_id')==sid]
   if any(e.get('outcome') not in ('grown','no_proposal') for e in selected_growth): raise ValueError('growth failure outcome')
   grown=[e for e in selected_growth if e.get('outcome')=='grown']
   forced=[x for x in rr if x['kind']=='forced_growth']
   if len(grown)!=len(forced) or not grown: raise ValueError('growth/forced cardinality')
   if grown[0]['current'] != 512*1024 or any(e['current']%4096 or e['proposed']%4096 for e in grown): raise ValueError('growth target alignment')
   maximum=grown[0]['current']; gi=0; derived=[]
   for rec in rr:
    if rec['kind']=='forced_growth':
     ev=grown[gi];gi+=1
     if ev['current']!=maximum or ev['proposed']<=ev['current']: raise ValueError('growth chain')
     maximum=ev['proposed']
    buffered=maximum-rec['receive_window_after']
    if buffered<0 or buffered>maximum: raise ValueError('derived buffer')
    if rec['kind']=='normal_read' and rec['flags']==0 and rec['delta']<maximum//4: raise ValueError('quarter threshold')
    derived.append({'relative_ordinal':rec['relative_ordinal'],'kind':rec['kind'],'flags':rec['flags'],'maximum':maximum,'buffered_at_eligibility':buffered,'delta':rec['delta'],'eligibility':rec['eligibility_observed_unix_ns'],'completion':rec['completion_result_observed_unix_ns']})
   gaps=[]
   for a,b,pa,pb in zip(derived,derived[1:],pp,pp[1:]):
    re=b['eligibility']-a['eligibility']; rc=b['completion']-a['completion']; gap=b['eligibility']-a['completion']; pg=pb['handler_entry_unix_ns']-pa['handler_entry_unix_ns']
    if re<0 or rc<0 or gap<0 or pg<0: raise ValueError('wall-label order')
    gaps.append({'next_kind':b['kind'],'next_flags':b['flags'],'receiver_eligibility_to_next_eligibility_wall_ns':re,'receiver_completion_to_next_completion_wall_ns':rc,'receiver_completion_to_next_eligibility_wall_ns':gap,'peer_handler_to_next_handler_wall_ns':pg,'peer_minus_receiver_eligibility_spacing_ns':pg-re,'peer_minus_receiver_completion_spacing_ns':pg-rc})
   normal=[g for g in gaps if g['next_kind']=='normal_read' and g['next_flags']==0]; force=[g for g in gaps if g['next_kind']=='forced_growth']
   normal_records=[x for x in derived if x['kind']=='normal_read']
   recycled_normal_records=[x for x in normal_records if x['flags']==0]
   row=rows[seq-1]; matches=row['response_copy_attribution']['matches']
   if len(matches)!=1 or matches[0].get('sequence')!=str(seq): raise ValueError('copy attribution')
   copy=matches[0]['copy']
   if copy.get('complete') is not True or copy.get('read_errors')!=0 or copy.get('write_errors')!=0 or copy.get('short_writes')!=0: raise ValueError('copy health')
   intervals.append({
    'arm':arm,'sequence':seq,'wall_seconds':row['wall_seconds'],'rate_Bps':row['useful_transfer_bytes_per_second'],
    'receiver_records':len(rr),'forced_records':len(forced),'normal_records':len(normal_records),
    'adjacent_zero_flag_normal_receiver_eligibility_wall_spacing':stat([g['receiver_eligibility_to_next_eligibility_wall_ns'] for g in normal]),
    'adjacent_zero_flag_normal_receiver_completion_wall_spacing':stat([g['receiver_completion_to_next_completion_wall_ns'] for g in normal]),
    'completion_to_next_zero_flag_normal_eligibility_wall_gap':stat([g['receiver_completion_to_next_eligibility_wall_ns'] for g in normal]),
    'adjacent_zero_flag_normal_peer_handler_wall_spacing':stat([g['peer_handler_to_next_handler_wall_ns'] for g in normal]),
    'adjacent_zero_flag_normal_peer_minus_receiver_eligibility_wall_spacing':stat([g['peer_minus_receiver_eligibility_spacing_ns'] for g in normal]),
    'adjacent_zero_flag_normal_peer_minus_receiver_completion_wall_spacing':stat([g['peer_minus_receiver_completion_spacing_ns'] for g in normal]),
    'adjacent_forced_receiver_wall_spacing':stat([g['receiver_completion_to_next_eligibility_wall_ns'] for g in force]),
    'adjacent_forced_peer_wall_spacing':stat([g['peer_handler_to_next_handler_wall_ns'] for g in force]),
    'zero_flag_normal_buffer_at_eligibility':{**stat([x['buffered_at_eligibility'] for x in recycled_normal_records]),'zero_count':sum(x['buffered_at_eligibility']==0 for x in recycled_normal_records)},
    'normal_quarter_threshold_verified_count':sum(x['flags']==0 and x['delta']>=x['maximum']//4 for x in normal_records),
    'normal_nonzero_flag_count':sum(x['flags']!=0 for x in normal_records),
    'copy':{k:{f:copy[k][f] for f in ('calls','bytes','total_ns','max_ns','span_ns')} for k in ('upstream_read','downstream_write','inter_read_gap')},
    'copy_handler_elapsed_ns':matches[0]['handler_elapsed_ns'],
   })
 by={(x['arm'],x['sequence']):x for x in intervals}; comparisons=[]
 for seq in CLEAN:
  a=by['h1',seq];b=by['h2',seq]
  comparisons.append({'sequence':seq,
   'h2_minus_h1_wall_ns':round((b['wall_seconds']-a['wall_seconds'])*1e9),
   'h2_minus_h1_zero_flag_normal_receiver_eligibility_wall_spacing_sum_ns':b['adjacent_zero_flag_normal_receiver_eligibility_wall_spacing']['sum_ns']-a['adjacent_zero_flag_normal_receiver_eligibility_wall_spacing']['sum_ns'],
   'h2_minus_h1_zero_flag_normal_receiver_completion_wall_spacing_sum_ns':b['adjacent_zero_flag_normal_receiver_completion_wall_spacing']['sum_ns']-a['adjacent_zero_flag_normal_receiver_completion_wall_spacing']['sum_ns'],
   'h2_minus_h1_zero_flag_normal_peer_handler_wall_spacing_sum_ns':b['adjacent_zero_flag_normal_peer_handler_wall_spacing']['sum_ns']-a['adjacent_zero_flag_normal_peer_handler_wall_spacing']['sum_ns'],
   'h2_minus_h1_copy_span_ns':b['copy']['upstream_read']['span_ns']-a['copy']['upstream_read']['span_ns'],
   'h2_minus_h1_upstream_read_total_ns':b['copy']['upstream_read']['total_ns']-a['copy']['upstream_read']['total_ns'],
   'h2_minus_h1_downstream_write_total_ns':b['copy']['downstream_write']['total_ns']-a['copy']['downstream_write']['total_ns'],
   'h2_minus_h1_inter_read_gap_total_ns':b['copy']['inter_read_gap']['total_ns']-a['copy']['inter_read_gap']['total_ns'],
  })
 return {
  'schema_version':1,'diagnostic':'wan-byte-credit-recycle-cadence-static-analysis','offline_only':True,'endpoint_or_traffic':False,
  'cohort_accepted':False,'conditioning_clean_sequences':[9,11],'intervals':intervals,'comparisons':comparisons,
  'clock_scope':{'adjacent_spacings':'Each spacing subtracts two serialized Unix labels from one process only. No receiver/peer timestamp subtraction. Serialization removes time.Time monotonic data, so results are coarse cadence evidence, not validation-quality durations or absolute transit latency.'},
  'source_conclusions':[
   'ReverseProxy uses a fixed 32KiB buffer and performs the response-body Read and downstream ResponseWriter Write sequentially.',
   'Each successful Yamux Stream.Read consumes buffered bytes and immediately checks receive credit; a normal positive WINDOW_UPDATE is eligible only when delta is at least maxReceiveWindow/4.',
   'The retained copy attribution measures every upstream Read, downstream Write, and the gap from one Read return to the next Read call with monotonic time.Time.Sub aggregates, but retains no per-read record joined to a WINDOW_UPDATE ordinal.',
   'A Go HTTP/2 handler write waits for the scheduled DATA write result; therefore its observed write time includes local HTTP/2 scheduling, flow-control, and socket-write backpressure.',
  ],
  'interpretation':{
   'supported':[
    'For clean sequences 9 and 11, exact zero-flag normal-update eligibility-to-eligibility and completion-to-completion cadence changes closely match peer handler-entry cadence changes. Conditional on the serialized wall labels, the retained evidence does not show material cadence expansion after receiver publication.',
    'For these exact rows, the successful growth/forced-record chain plus aligned target values reconstructs maxReceiveWindow at each eligibility point. Most zero-flag normal updates end with an empty receive buffer, and all satisfy the exact quarter-window threshold.',
    'H2 downstream Write/inter-read-gap time is higher, but inter-read gap overlaps the sequential Write and must not be added to it. Sequence 11 excess is predominantly inside upstream Read, not downstream Write.',
   ],
   'not_supported':[
    'No absolute receiver-to-peer transit latency, because endpoint timestamps are never subtracted.',
    'No monotonic adjacent-record duration: serialized Unix labels support ordering/coarse cadence only.',
    'No per-update separation of time waiting for inbound DATA from time outside Stream.Read while ReverseProxy writes to the public client.',
    'No absence of transient buffering; buffered_at_eligibility describes only the exact eligibility snapshot.',
    'No runtime policy, growth threshold, rounding, or 30-second reuse-timeout change.',
   ],
   'runtime_change_supported':False,
   'minimal_missing_measurement':'If a further discriminator is required before any runtime change, extend only the qualification-tagged selected-stream observer with fixed per-stream accumulators (no per-read ring): monotonic time inside Stream.Read before bytes become consumable, monotonic time outside Stream.Read from the prior return to the next entry, successful-read bytes/count, and maximum outside gap, snapshotted and reset only at each accepted positive WINDOW_UPDATE. Join the snapshot to the existing receiver ordinal/delta/cumulative record. This falsifies public-consumer/backpressure (outside-Read excess) versus inbound-data/feedback (inside-Read excess) while preserving transport transit as unclassified and leaving production policy unchanged.'
  },
  'input_sha256':{str(p.relative_to(ROOT) if p.is_relative_to(ROOT) else p):sha(p) for p in sorted(set(inputs),key=str)}
 }

if __name__=='__main__': print(json.dumps(main(),indent=2,sort_keys=True))
