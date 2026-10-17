#!/usr/bin/env python3
"""Execute only the seven conditionally approved continuation commands once."""
from pathlib import Path
import importlib.util,json,os,time
ROOT=Path(__file__).resolve().parents[1]
def module(name,path):
 spec=importlib.util.spec_from_file_location(name,path);value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value
helpers=module('original_build_helpers',ROOT/'build/execute-approved-build.py')
outer=module('frozen_outer',ROOT/'run-controller-screen.py')
verify=module('frozen_verifier',ROOT/'runner-source/integration/verify-terminal-build.py')
sha=helpers.sha;fresh=helpers.fresh
plan=json.loads((ROOT/'BUILD-TEST-PLAN.json').read_text());helpers.plan=plan
FREEZE='25aba314c036019a2c283e6944aa8838ff2e4d90eced2be857864fbcd265f331'
OUTPUT=ROOT/'build/continuation-1'
def wrap(command):
 proof=Path(command['log_directory'])/(command['name']+'.command-proof.json')
 row=json.loads(proof.read_text());row.update({'proof_origin':command['proof_origin'],'log_directory':command['log_directory'],'execution_source_freeze_sha256':verify.ORIGINAL_FREEZE if command['proof_origin']=='reused-original-freeze' else FREEZE,'command_proof_sha256':sha(proof)})
 prior=verify.verify_command(command,row,FREEZE)
 return row,prior
def log_bytes():return sum(p.stat().st_size for p in (ROOT/'build').rglob('*.log'))
def main():
 if sha(ROOT/'SHA256SUMS')!=FREEZE or sha(ROOT/'BUILD-TEST-PLAN.json')!='698959601a56caa0761b7e274336ae4520f45762119292d06cc95932acdeed91':raise ValueError('approved continuation freeze changed')
 before=helpers.snapshot()
 if OUTPUT.exists() or (ROOT/'build/receipt.json').exists():raise ValueError('fresh continuation directory/receipt required')
 OUTPUT.mkdir();fresh(OUTPUT/'pre-input-hashes.json',before)
 fresh(OUTPUT/'grant.json',{'source_freeze_sha256':FREEZE,'plan_sha256':sha(ROOT/'BUILD-TEST-PLAN.json'),'independent_review_sha256':'8a83ec88a9427d753fb3ba3260e8bea4e9c4193b7fca1b6ce4bea74c74af30a7','scope':'exact seven fresh local commands once; four pinned reused proofs; no WAN/endpoint execution; token blocked'})
 result={'schema_version':2,'diagnostic':'controller-screen-build','accepted':False,'endpoint_execution':False,'wan_traffic':False,'plan_sha256':sha(ROOT/'BUILD-TEST-PLAN.json'),'inventory_sha256':sha(ROOT/'SOURCE-SHA256SUMS'),'source_ref':plan['source_ref'],'fresh_execution_source_freeze_sha256':FREEZE,'tests':[],'builds':[],'reused_proofs':[],'binaries':{},'resolved_settings':json.loads((ROOT/'build/preflight.stdout.log').read_text()),'started_unix':time.time(),'complete_logs_retained':True,'byte_threshold_semantics':plan['bounds']['byte_bound_semantics']}
 try:
  if result['resolved_settings']!=plan['expected_settings']:raise ValueError('reused resolved settings differ')
  for phase in ['tests','builds']:
   for command in plan[phase]:
    if command['proof_origin']=='reused-original-freeze':
     row,prior=wrap(command);result[phase].append(row);result['reused_proofs'].append(prior);print(json.dumps({'event':'reuse','name':command['name']}),flush=True);continue
    helpers.snapshot();name=command['name'];prefix=OUTPUT/name;argv=command['argv'];started=time.time();error=None
    if command['log_directory']!=str(OUTPUT):raise ValueError('fresh command directory differs')
    print(json.dumps({'event':'start','name':name}),flush=True);os.chdir(command['cwd'])
    seconds=next(float(v[:-1]) for v in argv if v.endswith('s') and v[:-1].isdigit() and v!='5s')
    try:outer.execute_owned(argv,os.environ.copy(),prefix,seconds+30,command['max_stdout_bytes'])
    except BaseException as caught:error=type(caught).__name__+': '+str(caught)
    audit=json.loads(prefix.with_suffix('.outer.json').read_text());stdout=prefix.with_suffix('.stdout.log');stderr=prefix.with_suffix('.stderr.log')
    proof={k:command[k] for k in ['name','argv','cwd']}
    proof.update({'started_unix':started,'finished_unix':time.time(),'exit_status':audit['status'],'forced':audit['forced'],'remaining_group_members':audit['remaining_owned'],'outer_audit_sha256':sha(prefix.with_suffix('.outer.json')),'stdout_sha256':sha(stdout) if stdout.exists() else None,'stderr_sha256':sha(stderr) if stderr.exists() else None,'stdout_bytes':audit['stdout_bytes'],'stderr_bytes':audit['stderr_bytes'],'output_bytes':audit['stdout_bytes']+audit['stderr_bytes'],'adopted_descendants':audit['adopted_descendants'],'owned_identities':audit['owned_identities']})
    if error:proof['supervisor_error']=error
    fresh(OUTPUT/(name+'.command-proof.json'),proof)
    print(json.dumps({'event':'finish','name':name,'exit_status':proof['exit_status'],'forced':proof['forced'],'output_bytes':proof['output_bytes']}),flush=True)
    if error or proof['exit_status']!=0 or proof['forced'] or proof['remaining_group_members'] or proof['adopted_descendants'] or proof['output_bytes']>command['max_output_bytes'] or log_bytes()>plan['bounds']['max_new_build_logs_bytes']:raise ValueError(name+' failed approved natural-exit/output contract')
    row,prior=wrap(command);result[phase].append(row);helpers.snapshot()
  after=helpers.snapshot();fresh(OUTPUT/'post-input-hashes.json',after)
  if before!=after:raise ValueError('source/input before-after differs')
  for role in ['agent','server']:
   binary=ROOT/'build'/('current-'+role+'-controller.test')
   if not binary.is_file() or binary.is_symlink() or not os.access(binary,os.X_OK):raise ValueError('built binary absent/nonexecutable')
   result['binaries'][role]={'path':str(binary),'size':binary.stat().st_size,'sha256':sha(binary),'go_version_m_sha256':sha(OUTPUT/(role+'-metadata.stdout.log'))}
  result.update({'accepted':True,'source_before_after_unchanged':True,'pre_input_hashes_sha256':sha(OUTPUT/'pre-input-hashes.json'),'post_input_hashes_sha256':sha(OUTPUT/'post-input-hashes.json'),'artifact_bytes':outer.artifact_bytes(),'log_bytes':log_bytes(),'finished_unix':time.time()})
  if result['artifact_bytes']>outer.BUDGET-outer.AUDIT_RESERVE:raise ValueError('final artifact rejection threshold exceeded')
  fresh(ROOT/'build/receipt.json',result)
 except BaseException as error:
  result.update({'accepted':False,'failure':type(error).__name__+': '+str(error),'artifact_bytes':outer.artifact_bytes(),'log_bytes':log_bytes(),'finished_unix':time.time()});fresh(OUTPUT/'failed-run.json',result);print(json.dumps({'event':'failed','failure':result['failure']}),flush=True);raise
 print(json.dumps({'event':'complete','receipt_sha256':sha(ROOT/'build/receipt.json'),'binaries':result['binaries']}),flush=True)
if __name__=='__main__':main()
