#!/usr/bin/env python3
"""Read-only controller build receipt guard; never execute an endpoint."""
from pathlib import Path
import argparse,hashlib,json,os,re,stat
ROOT=Path(__file__).resolve().parents[2]
ORIGINAL_FREEZE='12d0eda626e79501dcacd49360d87dea911ee310406cdf732d0d734e8ea81a22'
REUSED=('preflight','legacy-tcp-state-validator','legacy-failed-capture-validator','prospective-terminal-contracts')
FRESH=('python-contracts','normal','race','agent-build','server-build','agent-metadata','server-metadata')
def sha(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def require(ok,message):
 if not ok:raise ValueError(message)
def read(path,limit=4<<20):
 p=Path(path);m=p.lstat();require(stat.S_ISREG(m.st_mode) and not p.is_symlink() and 0<m.st_size<=limit,'unbounded or nonregular input');return json.loads(p.read_text())
def verify_command(command,row,current_freeze):
 name=command['name'];require(name in REUSED+FRESH,'unknown continuation command')
 reused=name in REUSED;origin='reused-original-freeze' if reused else 'fresh-continuation-1'
 directory=ROOT/'build' if reused else ROOT/'build/continuation-1'
 require(command.get('proof_origin')==row.get('proof_origin')==origin and command.get('log_directory')==row.get('log_directory')==str(directory),'proof origin/log directory differs')
 require(row.get('execution_source_freeze_sha256')==(ORIGINAL_FREEZE if reused else current_freeze),'proof execution freeze differs')
 paths={kind:directory/(name+'.'+suffix) for kind,suffix in [('command_proof','command-proof.json'),('outer','outer.json'),('started','started.json'),('stdout','stdout.log'),('stderr','stderr.log')]}
 if reused:
  pinned=command.get('reused_proof');require(type(pinned) is dict and pinned.get('execution_source_freeze_sha256')==ORIGINAL_FREEZE and set(pinned.get('files',{}))==set(paths),'reused proof binding missing')
  for kind,path in paths.items():require(pinned['files'][kind]=={'path':str(path),'sha256':sha(path)},'reused proof/audit/log changed')
 else:require('reused_proof' not in command,'fresh command claims reused proof')
 proof=read(paths['command_proof']);require(row.get('command_proof_sha256')==sha(paths['command_proof']),'wrapped command proof changed')
 require(all(row.get(k)==v for k,v in proof.items()),'receipt rewrote original command proof')
 require(all(proof.get(k)==command[k] for k in ['name','argv','cwd']),'proof command differs')
 require(type(proof.get('exit_status')) is int and proof['exit_status']==0 and proof.get('forced') is False and proof.get('remaining_group_members')==[] and proof.get('remaining_owned',[])==[] and proof.get('remaining_after_cleanup',[])==[] and proof.get('adopted_descendants',[])==[],'proof did not join naturally')
 outer=read(paths['outer']);started=read(paths['started'])
 require(proof.get('outer_audit_sha256')==sha(paths['outer']) and outer.get('argv')==started.get('argv')==command['argv'],'outer/start command binding differs')
 require(type(outer.get('status')) is int and outer['status']==0 and outer.get('forced') is False and outer.get('remaining_owned')==[] and outer.get('remaining_after_cleanup',[])==[] and outer.get('adopted_descendants',[])==[] and outer.get('accepted_cleanup') is True,'outer proof did not join naturally')
 for stream in ['stdout','stderr']:
  path=paths[stream];m=path.lstat();require(stat.S_ISREG(m.st_mode) and not path.is_symlink(),'proof log type differs')
  require(proof.get(stream+'_sha256')==sha(path) and proof.get(stream+'_bytes')==outer.get(stream+'_bytes')==m.st_size and m.st_size<=command['max_'+stream+'_bytes'],'proof log hash/bytes differ')
 require(proof.get('output_bytes')==sum(paths[s].stat().st_size for s in ['stdout','stderr'])<=command['max_output_bytes'],'proof aggregate output differs')
 return {'name':name,'execution_source_freeze_sha256':ORIGINAL_FREEZE,'command_proof_sha256':sha(paths['command_proof'])} if reused else None
def main():
 p=argparse.ArgumentParser()
 for name in ['receipt','expected-receipt-sha256','server','agent','server-ref','agent-ref']:p.add_argument('--'+name,required=True)
 a=p.parse_args();require(re.fullmatch('[0-9a-f]{64}',a.expected_receipt_sha256) is not None,'reviewed controller receipt missing');require(sha(a.receipt)==a.expected_receipt_sha256,'reviewed controller receipt changed')
 v=read(a.receipt);plan=read(ROOT/'BUILD-TEST-PLAN.json')
 require(sha(ROOT/'failed-local-freeze-1/SHA256SUMS')==ORIGINAL_FREEZE,'original execution freeze changed')
 require(v.get('schema_version')==2 and v.get('diagnostic')=='controller-screen-build' and v.get('accepted') is True,'controller continuation build not accepted')
 require(v.get('endpoint_execution') is False and v.get('wan_traffic') is False and v.get('source_before_after_unchanged') is True,'build scope/source changed')
 require(v.get('plan_sha256')==sha(ROOT/'BUILD-TEST-PLAN.json') and v.get('inventory_sha256')==sha(ROOT/'SOURCE-SHA256SUMS'),'plan/source binding differs')
 expected=set()
 for line in (ROOT/'SOURCE-SHA256SUMS').read_text().splitlines():
  digest,name=line.split('  ',1);expected.add(name);require(sha(ROOT/name)==digest,'controller source changed')
 require(expected=={str(p.relative_to(ROOT)) for p in (ROOT/'test-source').rglob('*') if p.is_file()},'compiled source set differs')
 for row in plan['inputs']:require(sha(row['path'])==row['sha256'],'frozen historical/tool input changed')
 require(v.get('source_ref')==plan['source_ref']==a.server_ref==a.agent_ref,'compiled source reference differs')
 require(v.get('resolved_settings')==plan['expected_settings'] and read(ROOT/'build/preflight.stdout.log')==plan['expected_settings'],'build settings differ')
 reused=[];current_freeze=sha(ROOT/'SHA256SUMS')
 require(v.get('fresh_execution_source_freeze_sha256')==current_freeze,'fresh execution freeze differs')
 for phase in ['tests','builds']:
  commands=plan[phase];executions=v.get(phase);require(type(executions) is list and len(executions)==len(commands),'proof cardinality differs')
  for command,row in zip(commands,executions):
   prior=verify_command(command,row,current_freeze)
   if prior is not None:reused.append(prior)
 require([r['name'] for r in reused]==list(REUSED) and v.get('reused_proofs')==reused,'separate reused proof summary differs')
 for role,path in [('server',a.server),('agent',a.agent)]:
  b=v['binaries'][role];q=Path(path);require(str(q.resolve())==b['path'] and q.is_file() and not q.is_symlink() and os.access(q,os.X_OK),'binary path changed');require(sha(q)==b['sha256'] and q.stat().st_size==b['size'],'binary content changed')
  metadata=ROOT/'build/continuation-1'/(role+'-metadata.stdout.log');require(b.get('go_version_m_sha256')==sha(metadata) and 'go1.26.6' in metadata.read_text(),'compiled Go metadata differs')
 print(json.dumps({'accepted':True,'diagnostic':'controller-screen-build-tuple','reviewed_receipt_sha256':a.expected_receipt_sha256,'source_ref':plan['source_ref']}))
if __name__=='__main__':main()
