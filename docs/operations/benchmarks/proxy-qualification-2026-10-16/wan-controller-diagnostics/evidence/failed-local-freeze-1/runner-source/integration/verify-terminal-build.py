#!/usr/bin/env python3
"""Read-only controller build receipt guard; never execute an endpoint."""
from pathlib import Path
import argparse,hashlib,json,os,re,stat
ROOT=Path(__file__).resolve().parents[2]
def sha(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def require(ok,message):
 if not ok:raise ValueError(message)
def read(path,limit=4<<20):
 p=Path(path);m=p.lstat();require(stat.S_ISREG(m.st_mode) and not p.is_symlink() and 0<m.st_size<=limit,'unbounded or nonregular input');return json.loads(p.read_text())
def main():
 p=argparse.ArgumentParser()
 for name in ['receipt','expected-receipt-sha256','server','agent','server-ref','agent-ref']:p.add_argument('--'+name,required=True)
 a=p.parse_args();require(re.fullmatch('[0-9a-f]{64}',a.expected_receipt_sha256) is not None,'reviewed controller receipt missing');require(sha(a.receipt)==a.expected_receipt_sha256,'reviewed controller receipt changed')
 v=read(a.receipt);plan=read(ROOT/'BUILD-TEST-PLAN.json')
 require(v.get('schema_version')==1 and v.get('diagnostic')=='controller-screen-build' and v.get('accepted') is True,'controller build not accepted')
 require(v.get('endpoint_execution') is False and v.get('wan_traffic') is False and v.get('source_before_after_unchanged') is True,'build scope/source changed')
 require(v.get('plan_sha256')==sha(ROOT/'BUILD-TEST-PLAN.json') and v.get('inventory_sha256')==sha(ROOT/'SOURCE-SHA256SUMS'),'plan/source binding differs')
 expected=set()
 for line in (ROOT/'SOURCE-SHA256SUMS').read_text().splitlines():
  digest,name=line.split('  ',1);expected.add(name);require(sha(ROOT/name)==digest,'controller source changed')
 require(expected=={str(p.relative_to(ROOT)) for p in (ROOT/'test-source').rglob('*') if p.is_file()},'compiled source set differs')
 for row in plan['inputs']:require(sha(row['path'])==row['sha256'],'frozen historical/tool input changed')
 require(v.get('source_ref')==plan['source_ref']==a.server_ref==a.agent_ref,'compiled source reference differs')
 require(v.get('resolved_settings')==plan['expected_settings'] and read(ROOT/'build/preflight.stdout.log')==plan['expected_settings'],'build settings differ')
 for phase in ['tests','builds']:
  commands=plan[phase];executions=v.get(phase);require(type(executions) is list and len(executions)==len(commands),'proof cardinality differs')
  for command,row in zip(commands,executions):
   require(all(row.get(k)==command[k] for k in ['name','argv','cwd']),'proof command differs')
   require(type(row.get('exit_status')) is int and row['exit_status']==0 and row.get('forced') is False and row.get('remaining_group_members')==[],'proof did not join naturally')
   for stream in ['stdout','stderr']:require(row.get(stream+'_sha256')==sha(ROOT/'build'/(row['name']+'.'+stream+'.log')),'proof log changed')
 for role,path in [('server',a.server),('agent',a.agent)]:
  b=v['binaries'][role];q=Path(path);require(str(q.resolve())==b['path'] and q.is_file() and not q.is_symlink() and os.access(q,os.X_OK),'binary path changed');require(sha(q)==b['sha256'] and q.stat().st_size==b['size'],'binary content changed')
  metadata=ROOT/'build'/(role+'-metadata.stdout.log');require(b.get('go_version_m_sha256')==sha(metadata) and 'go1.26.6' in metadata.read_text(),'compiled Go metadata differs')
 print(json.dumps({'accepted':True,'diagnostic':'controller-screen-build-tuple','reviewed_receipt_sha256':a.expected_receipt_sha256,'source_ref':plan['source_ref']}))
if __name__=='__main__':main()
