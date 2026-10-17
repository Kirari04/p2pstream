#!/usr/bin/env python3
"""Independent fixed C/R/C expectations; never infer the expected CC from env."""
from pathlib import Path
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
def decide_arm(index,rows,attestation):
 require(type(index) is int and 0<=index<3,'invalid arm index')
 require(len(rows)==16 and [(r['measurement_stage'],r['transfer_direction']) for r in rows]==SCHEDULE and [r['diagnostic_sequence'] for r in rows]==list(range(1,17)),'exact16 transfer schedule differs')
 require(all(r['configured_payload_bytes']==268435456 and r['public_protocol']=='h2' and r['configured_rtt_ms']==80 for r in rows),'payload/protocol/RTT differs')
 require(attestation['integrity_issues']==[] and attestation['conditioning_issues']==[],'arm integrity/conditioning failed')
 speeds={r['diagnostic_sequence']:r['useful_transfer_bytes_per_second'] for r in rows}
 require(all(type(v) in [int,float] and math.isfinite(v) and v>0 for v in speeds.values()),'nonfinite throughput')
 if index in [0,2]:
  require(speeds[11]<FLOOR and speeds[14]<FLOOR and speeds[15]>=FLOOR,'CUBIC bracket did not reproduce11/14 with healthy15')
  require(attestation['performance_issues']==['steady download phase failed independent 29700000 B/s floor'],'CUBIC has another existing floor failure')
 else:
  require(attestation['performance_issues']==[] and attestation['accepted'] is True,'Reno failed an existing floor')
 return {'schema_version':1,'arm_index':index,'controller':ORDER[index],'accepted':True,'speeds':speeds,'read_ns':{str(n):next(r for r in rows if r['diagnostic_sequence']==n)['response_copy_attribution']['matches'][0]['copy']['upstream_read']['total_ns'] for n in [11,14]}}
def decide_screen(arms):
 require(len(arms)==3 and [a['arm_index'] for a in arms]==[0,1,2] and [a['controller'] for a in arms]==list(ORDER) and all(a['accepted'] is True for a in arms),'complete fixed C/R/C required')
 for seq in [11,14]:
  rates=[a['speeds'].get(str(seq),a['speeds'].get(seq)) for a in arms]
  require(rates[1]>=1.08*max(rates[0],rates[2]),'Reno improvement below8% of faster corresponding CUBIC')
  reads=[a['read_ns'][str(seq)] for a in arms];require(0<reads[1]<min(reads[0],reads[2]),'Reno upstream Read not below both CUBIC controls')
 return {'schema_version':1,'accepted':True,'interpretation':'broad controller dependence only; no HyStart attribution, production recommendation, parity or qualification clearance'}
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
if __name__=='__main__':main()
