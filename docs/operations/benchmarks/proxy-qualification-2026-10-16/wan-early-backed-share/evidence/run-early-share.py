#!/usr/bin/env python3
"""Fixed off/on/off early-share source derivative; a separate exact root runtime grant is required."""
from pathlib import Path
import hashlib,importlib.util,json,os,signal,sys
ROOT=Path(__file__).resolve().parent
OLD=ROOT.parent/'wan-controller-screen-20261017'
spec=importlib.util.spec_from_file_location('unchanged_owned_supervisor',OLD/'run-controller-policy.py');SUP=importlib.util.module_from_spec(spec);spec.loader.exec_module(SUP)
SUP.ROOT=ROOT
ORDER=(0,1,0)
def main():
 if sys.argv[1:]!=['--execute-approved-once']:raise SystemExit('requires separate approved once-only runtime command')
 source=ROOT/'SOURCE.json';digest=hashlib.sha256(source.read_bytes()).hexdigest();grant=json.loads((ROOT/'RUNTIME-GRANT.json').read_text())
 if grant!={'source_manifest_sha256':digest,'runtime_approved':True,'maximum_arms':3,'maximum_transfers':48,'early_share_order':[0,1,0],'idle_policy_order':[1,1,1],'controller_order':['cubic']*3}:raise SystemExit('runtime grant differs from exact frozen source')
 for entry in json.loads(source.read_text())['files']:
  if hashlib.sha256(Path(entry['path']).read_bytes()).hexdigest()!=entry['sha256']:raise SystemExit('frozen derivative/reference input changed')
 for entry in json.loads((OLD/'FROZEN-INPUTS.json').read_text()):
  if hashlib.sha256(Path(entry['path']).read_bytes()).hexdigest()!=entry['sha256']:raise SystemExit('original frozen source/runner/plan input changed')
 frozen=json.loads((ROOT/'COMMANDS.json').read_text());runroot=ROOT/'runs/early-share-aba-once'
 if runroot.exists():raise SystemExit('fresh complete screen required; no retry')
 runroot.mkdir(parents=True);decisions=[];screen={'schema_version':1,'accepted':False,'diagnostic_positive':False,'original_qualification_cleared':False,'completed_arms':0,'comparison_incomplete':True,'stopped':True,'arm_states':[{'index':i,'early_share_policy':v,'idle_policy':1,'controller':'cubic','state':'unstarted'} for i,v in enumerate(ORDER)]}
 try:
  for index,command in enumerate(frozen['arms']):
   if (type(command['index']) is not int or command['index']!=index or command['controller']!='cubic' or type(command['idle_policy']) is not int or command['idle_policy']!=1 or type(command['early_share_policy']) is not int or command['early_share_policy']!=ORDER[index] or command['env']!={'P2PSTREAM_QUALIFICATION_DATA_CONTROLLER':'cubic','P2PSTREAM_IDLE_ARM_INDEX':str(index),'P2PSTREAM_QUALIFICATION_EARLY_SHARE':str(ORDER[index]),'P2PSTREAM_QUALIFICATION_EARLY_SHARE_PROOF_DIR':command['output_dir']}):raise ValueError('independent fixed early-share/default-idle/controller binding differs')
   screen['arm_states'][index]['state']='attempted';env=os.environ.copy();env.update(command['env'])
   SUP.write_fresh(runroot/f'arm-{index}.invocation.json',command)
   status=SUP.execute_owned(command['argv'],env,runroot/f'arm-{index}',930)
   screen['arm_states'][index].update(state='completed-awaiting-validation',mature_outer_status=status)
   decision=runroot/f'arm-{index}.decision.json'
   checked=SUP.execute_owned([sys.executable,'-I','-B',str(ROOT/'validate-early-share.py'),'arm',str(index),command['output_dir'],str(status),str(decision)],{'PATH':'/usr/bin:/bin','PYTHONDONTWRITEBYTECODE':'1'},runroot/f'arm-{index}-validation',60,1<<20)
   if checked!=0:screen['arm_states'][index]['state']='invalid';raise RuntimeError('independent arm validation failed')
   screen['arm_states'][index].update(state='measurement-valid',decision=str(decision));decisions.append(str(decision));screen['completed_arms']=len(decisions)
  status=SUP.execute_owned([sys.executable,'-I','-B',str(ROOT/'validate-early-share.py'),'screen',*decisions,str(runroot/'decision.json')],{'PATH':'/usr/bin:/bin','PYTHONDONTWRITEBYTECODE':'1'},runroot/'screen-validation',20,1<<20)
  if not (runroot/'decision.json').is_file():raise RuntimeError('complete decision output missing')
  screen['comparison_incomplete']=False
  if status!=0:screen.update(accepted=False,diagnostic_positive=False,stopped=False,failure='complete three-arm early-share diagnostic did not meet all five effect criteria');return
  screen.update(accepted=True,stopped=False,diagnostic_positive=True,original_qualification_cleared=False,interpretation='demand-triggered already-backed aggregate receive-share diagnostic only')
 except BaseException as error:screen['failure']=type(error).__name__+': '+str(error);raise
 finally:SUP.finish_screen(runroot,screen)
if __name__=='__main__':
 signal.signal(signal.SIGTERM,SUP.terminate);main()
