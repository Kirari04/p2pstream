#!/usr/bin/env python3
"""One finite namespace-only proof. No TCP socket, endpoint, test or build."""
from pathlib import Path
import hashlib,importlib.util,json,os,signal,subprocess,sys,time
ROOT=Path(__file__).resolve().parent
OLD=ROOT.parent/'wan-controller-screen-20261017'
def module(name,path):
 spec=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);return m
P=module('idle_proof',ROOT/'idle-policy-proof.py');SUP=module('owned_supervisor',OLD/'run-controller-policy.py');SUP.ROOT=ROOT
HOLDER="import pathlib,sys,time;p=pathlib.Path(sys.argv[1]);d=time.monotonic()+18\nwhile not p.exists():\n if time.monotonic()>d:raise SystemExit(1)\n time.sleep(.02)\n"
def inside(output):
 start=time.monotonic();children=[];forced=False
 try:
  for role in ('agent','router'):
   stop=output/(role+'.stop');proc=subprocess.Popen(['unshare','--net',sys.executable,'-I','-B','-c',HOLDER,str(stop)])
   children.append((proc,stop));deadline=time.monotonic()+2
   while time.monotonic()<deadline:
    if proc.poll() is not None:raise RuntimeError('private namespace holder exited early')
    if not P.same_namespace(P.identity(proc.pid),P.current()):break
    time.sleep(.01)
   P.require(not P.same_namespace(P.identity(proc.pid),P.current()),'holder namespace was not private')
  agent=P.identity(children[0][0].pid);router=P.identity(children[1][0].pid);host=P.read(output/'host-pre.json')['sample'];runner=P.current()
  rejected=False
  try:P.checked_target(runner,runner,host,router)
  except ValueError:rejected=True
  P.require(rejected,'protected alias did not reject before any write')
  for index,want in enumerate((1,0,1)):
   subprocess.run([sys.executable,'-I','-B',str(ROOT/'idle-policy-proof.py'),'configure',str(output/f'configured-{index}.json'),str(index),str(output/'host-pre.json'),str(agent['pid']),str(agent['start_ticks']),agent['namespace'],str(router['pid']),str(router['start_ticks']),router['namespace']],check=True,timeout=6)
   P.require(P.read(output/f'configured-{index}.json')['target_independent_readback']['value']==want,'actual probe readback differs')
  P.fresh(output/'inner-result.json',{'accepted':True,'purpose':'namespace-only manipulation; no TCP endpoints','protected_alias_rejected_before_write':rejected,'policy_readbacks':[1,0,1],'elapsed_seconds':time.monotonic()-start})
 finally:
  for proc,stop in children:stop.touch()
  for proc,stop in children:
   try:P.require(proc.wait(timeout=2)==0,'holder did not exit naturally zero')
   except BaseException:
    forced=True
    if proc.poll() is None:proc.terminate()
    try:proc.wait(timeout=1)
    except subprocess.TimeoutExpired:proc.kill();proc.wait(timeout=1)
  P.fresh(output/'helper-closure.json',{'natural_zero_joined':not forced,'statuses':[p.returncode for p,s in children]})
  P.require(not forced,'probe helper cleanup required force')
def main():
 if sys.argv[1:2]==['--inside']:
  inside(Path(sys.argv[2]));return
 P.require(sys.argv[1:]==['--execute-approved-once'],'requires separately approved once-only namespace probe')
 digest=hashlib.sha256((ROOT/'SOURCE.json').read_bytes()).hexdigest();grant=P.read(ROOT/'PROBE-GRANT.json')
 P.require(grant=={'source_manifest_sha256':digest,'namespace_probe_approved':True},'probe grant differs')
 for entry in P.read(ROOT/'SOURCE.json')['files']:P.require(hashlib.sha256(Path(entry['path']).read_bytes()).hexdigest()==entry['sha256'],'frozen source/reference changed')
 output=ROOT/'probe-once';output.mkdir();before=P.current();P.fresh(output/'host-pre.json',{'schema_version':1,'phase':'host-pre','sample':before})
 status=SUP.execute_owned(['timeout','--signal=TERM','--kill-after=2s','20s','unshare','--user','--map-root-user','--pid','--fork','--kill-child=TERM','--mount','--mount-proc','--net',sys.executable,'-I','-B',str(Path(__file__).resolve()),'--inside',str(output)],os.environ.copy(),output/'namespace-probe',30,1<<20)
 after=P.current();P.fresh(output/'host-post.json',{'schema_version':1,'phase':'host-post','sample':after})
 P.require(status==0 and P.same_process(before,after) and before['value']==after['value'],'probe status or protected host identity/value differs')
 P.require(P.read(output/'inner-result.json')['accepted'] is True and P.read(output/'helper-closure.json')['natural_zero_joined'] is True,'namespace probe did not join naturally')
 P.fresh(output/'PROBE-RESULT.json',{'accepted':True,'outer_status':status,'host_unchanged':True,'policy_order':[1,0,1],'endpoint_execution':False,'traffic':False})
if __name__=='__main__':
 signal.signal(signal.SIGTERM,SUP.terminate);main()
