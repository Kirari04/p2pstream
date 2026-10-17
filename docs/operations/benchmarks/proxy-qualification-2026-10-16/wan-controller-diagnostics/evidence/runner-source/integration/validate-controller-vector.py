#!/usr/bin/env python3
"""Fresh whole-retained-vector C/R/C; preserve every mature control outcome."""
from pathlib import Path
from fractions import Fraction
import hashlib,importlib.util,json,math,stat,subprocess,sys
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
def decide_arm(index,rows,attestation):
 require(type(index) is int and 0<=index<3,'invalid arm index')
 require(type(rows) is list and len(rows)==16 and [(r['measurement_stage'],r['transfer_direction']) for r in rows]==SCHEDULE and [r['diagnostic_sequence'] for r in rows]==list(range(1,17)),'exact16 transfer schedule differs')
 require(all(r['configured_payload_bytes']==268435456 and r['public_protocol']=='h2' and r['configured_rtt_ms']==80 for r in rows),'payload/protocol/RTT differs')
 require(attestation['integrity_issues']==[] and attestation['conditioning_issues']==[],'arm integrity/conditioning failed')
 issues=attestation['performance_issues'];require(type(issues) is list and all(type(v) is str for v in issues) and type(attestation['accepted']) is bool and attestation['accepted']==(not issues),'mature performance outcome malformed')
 speeds={str(r['diagnostic_sequence']):r['useful_transfer_bytes_per_second'] for r in rows}
 retained=[]
 for n in RETAINED:
  row=rows[n-1];matches=row['response_copy_attribution']['matches'];require(len(matches)==1,'single retained copy record required')
  record=matches[0];copy=record['copy']
  retained.append({'sequence':n,'stage':row['measurement_stage'],'bytes_per_second':speeds[str(n)],'wall_seconds':row['wall_seconds'],'handler_elapsed_ns':record['handler_elapsed_ns'],'upstream_read_ns':copy['upstream_read']['total_ns'],'downstream_write_ns':copy['downstream_write']['total_ns']})
 value={'schema_version':1,'diagnostic':'whole-retained-vector','arm_index':index,'controller':ORDER[index],'eligible_for_diagnostic':True,'mature_performance_accepted':attestation['accepted'],'mature_performance_issues':issues.copy(),'speeds':speeds,'retained_downloads':retained,'eligibility_issues':[]}
 value.update(arm_statistics(value))
 if index==1 and (not attestation['accepted'] or any(speeds[str(n)]<FLOOR for n in DOWNLOADS)):
  value['eligible_for_diagnostic']=False;value['eligibility_issues']=['Reno failed at least one original mature download floor']
 return value
def decide_screen(arms):
 require(type(arms) is list and len(arms)==3 and [a['arm_index'] for a in arms]==[0,1,2] and [a['controller'] for a in arms]==list(ORDER),'complete fixed C/R/C required')
 for arm in arms:
  require(arm['diagnostic']=='whole-retained-vector' and arm['eligible_for_diagnostic'] is True and arm['eligibility_issues']==[],'all arms must be independently eligible')
  issues=arm['mature_performance_issues'];require(type(issues) is list and all(type(v) is str for v in issues) and type(arm['mature_performance_accepted']) is bool and arm['mature_performance_accepted']==(not issues),'mature control outcome malformed')
 statistics=[arm_statistics(arm) for arm in arms]
 require(arms[1]['mature_performance_accepted'] is True and all(arms[1]['speeds'][str(n)]>=FLOOR for n in DOWNLOADS),'Reno must pass every original download floor')
 rates=[v['pooled_bytes_per_second'] for v in statistics];reads=[v['aggregate_upstream_read_ns'] for v in statistics]
 exact_rates=[pooled_exact([arm['speeds'][str(n)] for n in RETAINED]) for arm in arms]
 ratios={str(n):float(Fraction(arms[1]['speeds'][str(n)])/max(Fraction(arms[0]['speeds'][str(n)]),Fraction(arms[2]['speeds'][str(n)]))) for n in RETAINED}
 criteria={'pooled_rate_at_least_108pct_faster_control':100*exact_rates[1]>=108*max(exact_rates[0],exact_rates[2]),'aggregate_read_strictly_below_both_controls':0<reads[1]<min(reads[0],reads[2]),'each_retained_rate_at_least_95pct_faster_control':all(100*Fraction(arms[1]['speeds'][str(n)])>=95*max(Fraction(arms[0]['speeds'][str(n)]),Fraction(arms[2]['speeds'][str(n)])) for n in RETAINED)}
 return {'schema_version':1,'diagnostic':'whole-retained-vector','accepted':all(criteria.values()),'criteria':criteria,'failed_criteria':[k for k,v in criteria.items() if not v],'arms':arms,'pooled_bytes_per_second':rates,'candidate_pooled_ratio_to_faster_control':float(exact_rates[1]/max(exact_rates[0],exact_rates[2])),'aggregate_upstream_read_ns':reads,'candidate_rate_ratios_to_faster_corresponding_control':ratios,'interpretation':'fresh diagnostic of broad controller dependence only; all mature control failures preserved; no HyStart attribution, production recommendation, original96-transfer, HAProxy, parity, merge or release clearance'}
def validate_arm(index,root,status):
 expected=ORDER[index];att=load(root/'attestation.json')
 # Re-run every unchanged authoritative validator, then require exact equality
 # with saved attestation. Recognized control performance failures remain failed.
 regen=root/'independent-controller-attestation.json';require(not regen.exists(),'fresh independent output required')
 argv=[sys.executable,'-I','-B',str(HERE/'validate-wan-below-tls-arm.py')]+[str(root/p) for p in ['results.jsonl','results.lifecycle.jsonl','results.summary.json','outer-namespaces.json','supervisor-lifecycle.json','pre-hashes.json','post-hashes.json']]+[str(regen),'h2','sustained']
 result=subprocess.run(argv,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=45,env={'PATH':'/usr/bin:/bin','PYTHONDONTWRITEBYTECODE':'1'})
 require(result.returncode in [0,1] and load(regen)==att,'independent existing attestation differs')
 spec=importlib.util.spec_from_file_location('controller_integrity',HERE/'validate-controller-arm-integrity.py');module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
 closure=module.evaluate(load(root/'supervisor-lifecycle.json'),load(root/'outer-namespaces.json'),att);require(closure['accepted'] and closure['conditioning_passed'],'independent natural lifecycle/legacy106/prospective gate failed')
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
