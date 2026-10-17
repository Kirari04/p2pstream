#!/usr/bin/env python3
"""Fresh whole-workload controller-policy C/R/C; preserve every mature outcome."""
from pathlib import Path
from fractions import Fraction
import copy,hashlib,importlib.util,json,math,stat,subprocess,sys
HERE=Path(__file__).resolve().parent
ORDER=('cubic','reno','cubic');FLOOR=29_700_000
SCHEDULE=[('warmup','download')]*4+[('warmup','upload')]*4+[('retained-warmup','download'),('retained-warmup','upload'),('steady','download'),('steady','upload'),('steady','upload'),('steady','download'),('steady','download'),('steady','upload')]
def require(ok,message):
 if not ok:raise ValueError(message)
def load(path):
 m=path.lstat();require(stat.S_ISREG(m.st_mode) and not path.is_symlink() and 0<m.st_size<=16<<20,'input file bound/type differs');return json.loads(path.read_text())
def infos(value):
 if type(value) is dict:
  if 'congestion_control' in value and value.get('captured') is True:yield value
  for child in value.values():yield from infos(child)
 elif type(value) is list:
  for child in value:yield from infos(child)
def binding_tuple(v):return tuple(v[k] for k in ['lane','generation','local_address','remote_address'])
def validate_bindings(sidecar,terminal,envelopes,namespace_event,expected):
 require(expected in ORDER and sidecar['schema_version']==1 and sidecar['requested']==expected,'independent controller selection differs')
 require(sidecar['namespace']==namespace_event['agent']['namespace'] and sidecar['parent_namespace']==namespace_event['runner_namespace'] and sidecar['namespace']!=sidecar['parent_namespace'],'actual agent namespace binding differs')
 require(sidecar['default']=='cubic' and all(set(['cubic','reno'])<=set(sidecar[k]) for k in ['available','allowed']),'namespace controller preflight differs')
 bindings=sidecar['bindings'];records=terminal['records'];require(len(bindings)==len(records)==5,'fixed five controller owners required')
 owners={};sessions={}
 for lane,(b,r) in enumerate(zip(bindings,records)):
  want='cubic' if lane==0 else expected;raw=r['raw_write_activity']
  require(b['lane']==r['lane']==lane and b['requested']==b['readback']==want and b['before_tls'] is True and b['explicit_set'] is (lane>0),'exact pre-TLS lane controller differs')
  require(type(b['generation']) is int and b['generation']>0 and binding_tuple(b)==binding_tuple(raw),'controller/terminal owner tuple differs')
  owners[lane]=binding_tuple(b);sessions[lane]=r['session_identity']
  captured=list(infos(raw));require(captured and raw['tcp_state']['bind_tcp_info']['captured'] is True,'terminal owner lacks bind TCP capture')
  require(all(x['congestion_control']==want for x in captured),'terminal healthy TCP controller differs')
 require(len(set(owners.values()))==5 and len(set(sessions.values()))==5,'controller owner/session identities repeat')
 for envelope in envelopes:
  counters=envelope['response']['send_counters'];require([c['lane'] for c in counters]==list(range(5)),'sender envelope lost a lane')
  for counter in counters:
   lane=counter['lane'];raw=counter['raw_write_activity'];want='cubic' if lane==0 else expected
   require(binding_tuple(raw)==owners[lane] and counter['session_identity']==sessions[lane],'sender/terminal owner session changed')
   require(all(x['congestion_control']==want for x in infos(raw)),'healthy TCP sample controller differs')
   tcp=raw['tcp_state']
   if envelope['action']=='post' and tcp['sequence']:
    require(tcp['sequence']==str(envelope['sequence']) and tcp['pre_tcp_info']==tcp['threshold_history']['selected_pre_tcp_info'],'selected pre capture uses wrong interval')
RETAINED=(9,11,14,15)
UPLOADS=(10,12,13,16)
POSTWARMUP=tuple(range(9,17))
DOWNLOADS=(1,2,3,4,9,11,14,15)
def positive_number(value):
 return type(value) in (int,float) and math.isfinite(value) and value>0
def pooled_exact(rates):
 require(len(rates)==4 and all(positive_number(v) for v in rates),'four finite positive retained rates required')
 return Fraction(4)/sum((Fraction(1)/Fraction(v) for v in rates),Fraction(0))
def pooled(rates):
 result=float(pooled_exact(rates))
 require(positive_number(result),'pooled retained rate is not finite positive')
 return result
def arm_statistics(arm):
 speeds=arm['speeds'];retained=arm['retained_downloads']
 require(type(speeds) is dict and set(speeds)=={str(n) for n in range(1,17)} and all(positive_number(v) for v in speeds.values()),'full16 finite positive rate vector required')
 require(type(retained) is list and [v['sequence'] for v in retained]==list(RETAINED),'exact retained vector required')
 for row in retained:
  require(row['bytes_per_second']==speeds[str(row['sequence'])] and positive_number(row['wall_seconds']),'retained rate/wall differs')
  require(type(row['upstream_read_ns']) is int and row['upstream_read_ns']>0 and type(row['downstream_write_ns']) is int and row['downstream_write_ns']>=0 and type(row['handler_elapsed_ns']) is int and row['handler_elapsed_ns']>0,'retained copy durations invalid')
 return {'pooled_bytes_per_second':pooled([speeds[str(n)] for n in RETAINED]),'aggregate_upstream_read_ns':sum(v['upstream_read_ns'] for v in retained)}
def reuse_outcomes(rows,attestation):
 """Reconstruct exact mature reuse predicates from canonical raw evidence."""
 traces=[];expectations=[];conditions=[];first_keys=set()
 for n,row in enumerate(rows,1):
  envelope=row['upstream_transport_trace'];matches=envelope['matches']
  require(envelope['overflow'] is False and envelope['sequence']==str(n) and envelope['store_count']==n and len(matches)==1,'raw trace envelope correlation/cardinality differs')
  trace=matches[0];download=row['transfer_direction']=='download'
  require(trace['sequence']==str(n) and trace['method']==('GET' if download else 'POST') and trace['path']==('/agent/bulk' if download else '/agent/bulk-upload') and trace['callback_overflow'] is False,'raw trace request correlation differs')
  require(type(trace['got_conn']) is list and len(trace['got_conn'])==1 and type(trace['put_idle_conn']) is list and len(trace['put_idle_conn'])==1,'complete GotConn/PutIdleConn evidence required')
  got=trace['got_conn'][0];put=trace['put_idle_conn'][0];tls=row['wan_response_tls']
  require(type(tls) is list and len(tls)==1 and type(tls[0]['origin_connection_id']) is str and tls[0]['origin_connection_id'],'origin identity missing')
  require(all(type(got[k]) is str and got[k] for k in ['connection_type','local_address','remote_address']),'origin stream identity missing')
  require(type(got['reused']) is bool and type(got['was_idle']) is bool and type(got['idle_time_ns']) is int and got['idle_time_ns']>=0,'raw reuse flags/idle duration malformed')
  times=[trace['started_unix_ns'],got['observed_unix_ns'],put['observed_unix_ns'],trace['handler_return_unix_ns']]
  require(all(type(t) is int and t>0 for t in times) and times==sorted(times) and not put.get('error'),'raw trace timestamps or idle release invalid')
  traces.append({'sequence':n,'lane_position':((n-9)%4+1) if n>=9 else None,'origin_connection_id':tls[0]['origin_connection_id'],'connection_key':'|'.join(got[k] for k in ['connection_type','local_address','remote_address']),'raw_trace':copy.deepcopy(trace)})
 for n in range(9,13):
  t=traces[n-1];got=t['raw_trace']['got_conn'][0];key=t['connection_key']
  satisfied=key not in first_keys and got['reused'] is False and got['was_idle'] is False and got['idle_time_ns']==0
  require(satisfied,'initial post-close four-lane conditioning failed');first_keys.add(key)
  expectations.append({'sequence':n,'kind':'first-post-close-visit','lane_position':n-8,'connection_key':key,'satisfied':True})
 for n in range(13,17):
  current=traces[n-1];first=traces[n-5];got=current['raw_trace']['got_conn'][0];prior_put=first['raw_trace']['put_idle_conn'][0]['observed_unix_ns'];elapsed=got['observed_unix_ns']-prior_put
  require(elapsed>=0,'revisit time precedes prior PutIdleConn')
  require(current['connection_key']==first['connection_key'],'unsupported revisit tunnel/lane key change')
  require(got['reused'] is got['was_idle'],'serial revisit reuse/idle flags contradict')
  require((got['reused'] and current['origin_connection_id']==first['origin_connection_id']) or (not got['reused'] and got['idle_time_ns']==0 and current['origin_connection_id']!=first['origin_connection_id']),'revisit origin identity/reuse flags contradict')
  satisfied=current['connection_key']==first['connection_key'] and got['reused'] is True and got['was_idle'] is True and elapsed<30_000_000_000 and current['origin_connection_id']==first['origin_connection_id']
  expectations.append({'sequence':n,'kind':'same-key-revisit','lane_position':n-12,'connection_key':current['connection_key'],'expected_connection_key':first['connection_key'],'prior_put_idle_unix_ns':prior_put,'got_conn_unix_ns':got['observed_unix_ns'],'elapsed_idle_ns':elapsed,'httptrace_idle_time_ns':got['idle_time_ns'],'prior_origin_connection_id':first['origin_connection_id'],'observed_origin_connection_id':current['origin_connection_id'],'satisfied':satisfied})
  if not satisfied:conditions.append(f'sequence {n} fully evidenced reuse/idle/origin conditioning was not retained')
 require(attestation['reuse_expectations']==expectations,'attested reuse expectations disagree with raw evidence')
 require(attestation['conditioning_issues']==conditions,'unknown, uncorrelated or initial conditioning failure')
 return {'raw_origin_traces':traces,'reuse_expectations':expectations,'revisit_outcome_counts':{'total':4,'satisfied':sum(e['satisfied'] for e in expectations[4:]),'unsatisfied':sum(not e['satisfied'] for e in expectations[4:])},'origin_outcome_counts':{'retained':sum(t['raw_trace']['got_conn'][0]['reused'] for t in traces[12:]),'fresh_replacement':sum(not t['raw_trace']['got_conn'][0]['reused'] for t in traces[12:])},'revisit_outcomes':[dict(e,reused=traces[e['sequence']-1]['raw_trace']['got_conn'][0]['reused'],was_idle=traces[e['sequence']-1]['raw_trace']['got_conn'][0]['was_idle'],outcome='retained-origin' if traces[e['sequence']-1]['raw_trace']['got_conn'][0]['reused'] else 'fresh-origin-replacement') for e in expectations[4:]]}
def mature_partition(attestation):
 fields=['integrity_issues','conditioning_issues','performance_issues']
 require(all(type(attestation[k]) is list and all(type(v) is str for v in attestation[k]) for k in fields),'mature issue partitions malformed')
 require(attestation['validation_issues']==sum((attestation[k] for k in fields),[]) and attestation['continuation_safe'] is (not attestation['integrity_issues']),'mature partition/continuation malformed')
 require(type(attestation['accepted']) is bool and attestation['accepted']==(not any(attestation[k] for k in fields)),'mature accepted outcome malformed')
def decide_arm(index,rows,attestation):
 require(type(index) is int and 0<=index<3,'invalid arm index')
 require(type(rows) is list and len(rows)==16 and [(r['measurement_stage'],r['transfer_direction']) for r in rows]==SCHEDULE and [r['diagnostic_sequence'] for r in rows]==list(range(1,17)),'exact16 transfer schedule differs')
 require(all(r['configured_payload_bytes']==268435456 and r['public_protocol']=='h2' and r['configured_rtt_ms']==80 for r in rows),'payload/protocol/RTT differs')
 mature_partition(attestation)
 require(attestation['integrity_issues']==[],'arm integrity failed')
 require(all(positive_number(r['wall_seconds']) and positive_number(r['useful_transfer_bytes_per_second']) and r['useful_transfer_bytes_per_second']==r['configured_payload_bytes']/r['wall_seconds'] for r in rows),'recorded rate/payload/wall differs from original jq division')
 outcomes=reuse_outcomes(rows,attestation)
 issues=attestation['performance_issues']
 speeds={str(r['diagnostic_sequence']):r['useful_transfer_bytes_per_second'] for r in rows}
 retained=[]
 for n in RETAINED:
  row=rows[n-1];matches=row['response_copy_attribution']['matches'];require(len(matches)==1,'single retained copy record required')
  record=matches[0];attribution=record['copy']
  retained.append({'sequence':n,'stage':row['measurement_stage'],'bytes_per_second':speeds[str(n)],'wall_seconds':row['wall_seconds'],'handler_elapsed_ns':record['handler_elapsed_ns'],'upstream_read_ns':attribution['upstream_read']['total_ns'],'downstream_write_ns':attribution['downstream_write']['total_ns']})
 value={'schema_version':1,'diagnostic':'whole-workload-controller-policy','arm_index':index,'controller':ORDER[index],'eligible_for_diagnostic':True,'mature_accepted':attestation['accepted'],'mature_conditioning_issues':attestation['conditioning_issues'].copy(),'mature_performance_issues':issues.copy(),'mature_attestation':copy.deepcopy(attestation),'speeds':speeds,'retained_downloads':retained,'eligibility_issues':[]}
 value.update(arm_statistics(value))
 value.update(outcomes)
 value.update({'measurement_valid':True,'initial_conditions_valid':True,'postwarmup_transfers':[{'sequence':n,'direction':rows[n-1]['transfer_direction'],'payload_bytes':rows[n-1]['configured_payload_bytes'],'wall_seconds':rows[n-1]['wall_seconds'],'bytes_per_second':speeds[str(n)]} for n in POSTWARMUP],'aggregate_active_transfer_rates':{name:float(Fraction(len(sequences))/sum((Fraction(1)/Fraction(speeds[str(n)]) for n in sequences),Fraction(0))) for name,sequences in [('download',RETAINED),('upload',UPLOADS),('all_postwarmup',POSTWARMUP)]},'total_measured_wall_seconds':{name:sum(rows[n-1]['wall_seconds'] for n in sequences) for name,sequences in [('download',RETAINED),('upload',UPLOADS),('all_postwarmup',POSTWARMUP)]},'original_download_floor_outcomes':{str(n):speeds[str(n)]>=FLOOR for n in DOWNLOADS}})
 return value
def decide_screen(arms):
 require(type(arms) is list and len(arms)==3 and [a['arm_index'] for a in arms]==[0,1,2] and [a['controller'] for a in arms]==list(ORDER),'complete fixed C/R/C required')
 for arm in arms:
  require(arm['diagnostic']=='whole-workload-controller-policy' and arm['eligible_for_diagnostic'] is True and arm['eligibility_issues']==[],'all arms must be independently eligible')
  mature_partition(arm['mature_attestation'])
  require(arm['mature_accepted'] is arm['mature_attestation']['accepted'] and arm['mature_conditioning_issues']==arm['mature_attestation']['conditioning_issues'] and arm['mature_performance_issues']==arm['mature_attestation']['performance_issues'],'reported mature outcome differs')
 statistics=[arm_statistics(arm) for arm in arms]
 rates=[v['pooled_bytes_per_second'] for v in statistics];reads=[v['aggregate_upstream_read_ns'] for v in statistics]
 exact_rates=[pooled_exact([arm['speeds'][str(n)] for n in RETAINED]) for arm in arms]
 ratios={str(n):float(Fraction(arms[1]['speeds'][str(n)])/max(Fraction(arms[0]['speeds'][str(n)]),Fraction(arms[2]['speeds'][str(n)]))) for n in RETAINED}
 criteria={'candidate_all_original_download_floors':all(arms[1]['speeds'][str(n)]>=FLOOR for n in DOWNLOADS),'candidate_mature_performance_passed':not arms[1]['mature_performance_issues'],'pooled_rate_at_least_108pct_faster_control':100*exact_rates[1]>=108*max(exact_rates[0],exact_rates[2]),'aggregate_read_strictly_below_both_controls':0<reads[1]<min(reads[0],reads[2]),'each_retained_rate_at_least_95pct_faster_control':all(100*Fraction(arms[1]['speeds'][str(n)])>=95*max(Fraction(arms[0]['speeds'][str(n)]),Fraction(arms[2]['speeds'][str(n)])) for n in RETAINED)}
 return {'schema_version':1,'diagnostic':'whole-workload-controller-policy','accepted':all(criteria.values()),'diagnostic_positive':all(criteria.values()),'original_qualification_cleared':False,'criteria':criteria,'control_pooled_ratio_0_to_2':float(exact_rates[0]/exact_rates[2]),'candidate_pooled_ratios_to_each_control':[float(exact_rates[1]/exact_rates[i]) for i in (0,2)],'rate_endpoint':'aggregate active-transfer rate from recorded rates; excludes between-request gaps','failed_criteria':[k for k,v in criteria.items() if not v],'arms':arms,'pooled_bytes_per_second':rates,'candidate_pooled_ratio_to_faster_control':float(exact_rates[1]/max(exact_rates[0],exact_rates[2])),'aggregate_upstream_read_ns':reads,'candidate_rate_ratios_to_faster_corresponding_control':ratios,'interpretation':'fresh fixed-order whole-workload controller-policy effect including observed postwarmup origin reconnections within the same five owned tunnel TCP sessions; every mature conditioning/performance failure preserved; diagnostic_positive is distinct from mature_accepted; no same-reused-stream claim, randomized causal proof, HyStart attribution, production Reno recommendation, original96-transfer, HAProxy, parity, merge or release clearance'}
def validate_arm(index,root,status):
 expected=ORDER[index];att=load(root/'attestation.json')
 # Re-run every unchanged authoritative validator, then require exact equality
 # with saved attestation. Recognized control performance failures remain failed.
 regen=root/'independent-controller-attestation.json';require(not regen.exists(),'fresh independent output required')
 argv=[sys.executable,'-I','-B',str(HERE/'validate-wan-below-tls-arm.py')]+[str(root/p) for p in ['results.jsonl','results.lifecycle.jsonl','results.summary.json','outer-namespaces.json','supervisor-lifecycle.json','pre-hashes.json','post-hashes.json']]+[str(regen),'h2','sustained']
 result=subprocess.run(argv,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=45,env={'PATH':'/usr/bin:/bin','PYTHONDONTWRITEBYTECODE':'1'})
 require(result.returncode in [0,1] and load(regen)==att,'independent existing attestation differs')
 spec=importlib.util.spec_from_file_location('controller_integrity',HERE/'validate-controller-arm-integrity.py');module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
 closure=module.evaluate(load(root/'supervisor-lifecycle.json'),load(root/'outer-namespaces.json'),att);require(closure['accepted'],'independent natural lifecycle/legacy106/prospective gate failed')
 require(type(status) is int and status==(0 if att['accepted'] else 1),'outer arm exit differs from existing outcome')
 require(int((root/'validation.status').read_text())==result.returncode,'saved existing validation status differs')
 require(load(root/'pre-hashes.json')==load(root/'post-hashes.json'),'source/runner/binary pre/post binding changed')
 control=root/'results.sender-wait-controls';events=[json.loads(l) for l in (root/'results.lifecycle.jsonl').read_text().splitlines()];ns=next(e for e in events if e['event']=='namespace-helpers-start')
 envelopes=[load(p) for p in sorted(control.glob('*-sender.json'))]
 validate_bindings(load(root/'controller-selection.json'),load(control/'final-raw-write-terminal.json'),envelopes,ns,expected)
 return decide_arm(index,[json.loads(l) for l in (root/'results.jsonl').read_text().splitlines()],att)
def main():
 if sys.argv[1]=='arm':value=validate_arm(int(sys.argv[2]),Path(sys.argv[3]),int(sys.argv[4]));out=Path(sys.argv[5])
 elif sys.argv[1]=='screen':value=decide_screen([load(Path(x)) for x in sys.argv[2:5]]);out=Path(sys.argv[5])
 else:raise ValueError('unknown validation mode')
 require(not out.exists(),'fresh decision required');out.write_text(json.dumps(value,indent=2,sort_keys=True)+'\n')
 raise SystemExit(0 if value.get('eligible_for_diagnostic',value.get('accepted',False)) else 1)
if __name__=='__main__':main()
