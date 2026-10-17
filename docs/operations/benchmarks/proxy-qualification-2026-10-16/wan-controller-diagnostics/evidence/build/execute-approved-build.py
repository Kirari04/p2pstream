#!/usr/bin/env python3
"""Execute the root-approved immutable local plan once, with no endpoint runs."""
from pathlib import Path
import hashlib,importlib.util,json,os,sys,time
ROOT=Path(__file__).resolve().parents[1]
OUTPUT=ROOT/'build'
def sha(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def fresh(path,value):
 with path.open('x') as f:json.dump(value,f,indent=2,sort_keys=True);f.write('\n');f.flush();os.fsync(f.fileno())
def snapshot():
 rows=json.loads((ROOT/'FROZEN-INPUTS.json').read_text())
 for row in rows:
  if sha(row['path'])!=row['sha256']:raise ValueError('frozen input changed: '+row['path'])
 expected=set()
 for line in (ROOT/'SOURCE-SHA256SUMS').read_text().splitlines():
  digest,name=line.split('  ',1);expected.add(name)
  if sha(ROOT/name)!=digest:raise ValueError('source changed: '+name)
 if expected!={str(p.relative_to(ROOT)) for p in (ROOT/'test-source').rglob('*') if p.is_file()}:raise ValueError('source file set changed')
 for row in plan['inputs']:
  if sha(row['path'])!=row['sha256']:raise ValueError('plan input changed: '+row['path'])
 if (ROOT/'REVIEWED-RECEIPT-SHA256').read_text().strip()!='BLOCKED_PENDING_REVIEWED_CONTROLLER_BUILD_RECEIPT':raise ValueError('runtime token changed')
 return rows
def log_bytes():return sum(p.stat().st_size for p in OUTPUT.glob('*.log'))
def main():
 before=snapshot();fresh(OUTPUT/'pre-input-hashes.json',before)
 result={'schema_version':1,'diagnostic':'controller-screen-build','accepted':False,'endpoint_execution':False,'wan_traffic':False,'plan_sha256':sha(ROOT/'BUILD-TEST-PLAN.json'),'inventory_sha256':sha(ROOT/'SOURCE-SHA256SUMS'),'source_ref':plan['source_ref'],'tests':[],'builds':[],'binaries':{},'started_unix':time.time(),'byte_threshold_semantics':plan['bounds']['byte_bound_semantics'],'complete_logs_retained':True}
 fresh(OUTPUT/'grant.json',{'source_freeze_sha256':'12d0eda626e79501dcacd49360d87dea911ee310406cdf732d0d734e8ea81a22','plan_sha256':result['plan_sha256'],'inventory_sha256':result['inventory_sha256'],'scope':'local frozen tests/build only; sequential single attempt; stop first failure; no endpoint/WAN/namespaces; runtime token blocked'})
 try:
  for phase in ['tests','builds']:
   for command in plan[phase]:
    snapshot();name=command['name'];prefix=OUTPUT/name
    print(json.dumps({'event':'start','phase':phase,'name':name}),flush=True)
    os.chdir(command['cwd']);error=None;started=time.time()
    # The exact argv already contains its approved GNU timeout. The outer
    # process audit gives only cleanup time beyond that command deadline.
    argv=command['argv'];seconds=next(float(v[:-1]) for v in argv if v.endswith('s') and v[:-1].isdigit() and v not in ['5s'])
    try:outer.execute_owned(argv,os.environ.copy(),prefix,seconds+30,command['max_stdout_bytes'])
    except BaseException as caught:error=type(caught).__name__+': '+str(caught)
    audit=json.loads(prefix.with_suffix('.outer.json').read_text());stdout=prefix.with_suffix('.stdout.log');stderr=prefix.with_suffix('.stderr.log')
    row={k:command[k] for k in ['name','argv','cwd']}
    row.update({'started_unix':started,'finished_unix':time.time(),'exit_status':audit['status'],'forced':audit['forced'],'remaining_group_members':audit['remaining_owned'],'outer_audit_sha256':sha(prefix.with_suffix('.outer.json')),'stdout_sha256':sha(stdout) if stdout.exists() else None,'stderr_sha256':sha(stderr) if stderr.exists() else None,'stdout_bytes':audit['stdout_bytes'],'stderr_bytes':audit['stderr_bytes'],'output_bytes':audit['stdout_bytes']+audit['stderr_bytes'],'adopted_descendants':audit['adopted_descendants'],'owned_identities':audit['owned_identities']})
    if error:row['supervisor_error']=error
    result[phase].append(row)
    fresh(OUTPUT/(name+'.command-proof.json'),row)
    print(json.dumps({'event':'finish','name':name,'exit_status':row['exit_status'],'forced':row['forced'],'output_bytes':row['output_bytes']}),flush=True)
    if error or row['exit_status']!=0 or row['forced'] or row['remaining_group_members'] or row['adopted_descendants'] or row['output_bytes']>command['max_output_bytes'] or row['stdout_bytes']>command['max_stdout_bytes'] or row['stderr_bytes']>command['max_stderr_bytes'] or log_bytes()>plan['bounds']['max_new_build_logs_bytes']:raise ValueError(name+' failed approved natural-exit/output contract')
    if name=='preflight':
     result['resolved_settings']=json.loads(stdout.read_text())
     if result['resolved_settings']!=plan['expected_settings']:raise ValueError('resolved Go settings differ')
    snapshot()
  after=snapshot();fresh(OUTPUT/'post-input-hashes.json',after)
  if before!=after:raise ValueError('source/input before-after differs')
  for role in ['agent','server']:
   binary=OUTPUT/('current-'+role+'-controller.test')
   if not binary.is_file() or binary.is_symlink() or not os.access(binary,os.X_OK):raise ValueError('built binary absent/nonexecutable')
   result['binaries'][role]={'path':str(binary),'size':binary.stat().st_size,'sha256':sha(binary),'go_version_m_sha256':sha(OUTPUT/(role+'-metadata.stdout.log'))}
  result.update({'accepted':True,'source_before_after_unchanged':True,'pre_input_hashes_sha256':sha(OUTPUT/'pre-input-hashes.json'),'post_input_hashes_sha256':sha(OUTPUT/'post-input-hashes.json'),'artifact_bytes':outer.artifact_bytes(),'log_bytes':log_bytes(),'finished_unix':time.time()})
  if result['artifact_bytes']>outer.BUDGET-outer.AUDIT_RESERVE:raise ValueError('final artifact rejection threshold exceeded')
  fresh(OUTPUT/'receipt.json',result)
 except BaseException as error:
  result.update({'accepted':False,'failure':type(error).__name__+': '+str(error),'artifact_bytes':outer.artifact_bytes(),'log_bytes':log_bytes(),'finished_unix':time.time()})
  fresh(OUTPUT/'failed-run.json',result);print(json.dumps({'event':'failed','failure':result['failure']}),flush=True);raise
 print(json.dumps({'event':'complete','receipt_sha256':sha(OUTPUT/'receipt.json'),'binaries':result['binaries']}),flush=True)
if __name__=='__main__':
 plan=json.loads((ROOT/'BUILD-TEST-PLAN.json').read_text())
 if sha(ROOT/'SHA256SUMS')!='12d0eda626e79501dcacd49360d87dea911ee310406cdf732d0d734e8ea81a22' or sha(ROOT/'BUILD-TEST-PLAN.json')!='4bd921978e886a6894597de5b8ca810228ccada16a4db15912fae7e05808cc22':raise SystemExit('approved freeze changed')
 spec=importlib.util.spec_from_file_location('approved_outer',ROOT/'run-controller-screen.py');outer=importlib.util.module_from_spec(spec);spec.loader.exec_module(outer)
 main()
