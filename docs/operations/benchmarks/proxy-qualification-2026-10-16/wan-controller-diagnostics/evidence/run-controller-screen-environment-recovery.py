#!/usr/bin/env python3
"""Fixed, bounded, single-attempt C/R/C. Requires separate root runtime grant."""
from pathlib import Path
import ctypes,hashlib,json,os,signal,subprocess,sys,time
ROOT=Path(__file__).resolve().parent
BUDGET=256<<20
LOG_LIMIT=16<<20
INVENTORY_SECONDS=1.0
POLL_SECONDS=.05
AUDIT_RESERVE=1<<20

def write_fresh(path,value):
 with path.open('x') as f:json.dump(value,f,indent=2,sort_keys=True);f.write('\n');f.flush();os.fsync(f.fileno())
def subreaper(value=None):
 libc=ctypes.CDLL(None,use_errno=True)
 current=ctypes.c_int()
 if libc.prctl(37,ctypes.byref(current),0,0,0)!=0:raise OSError(ctypes.get_errno(),'PR_GET_CHILD_SUBREAPER failed')
 if value is not None and libc.prctl(36,int(value),0,0,0)!=0:raise OSError(ctypes.get_errno(),'PR_SET_CHILD_SUBREAPER failed')
 return current.value

def process_table():
 table={}
 for p in Path('/proc').iterdir():
  if not p.name.isdigit():continue
  try:
   s=(p/'stat').read_text();fields=s[s.rfind(')')+2:].split();pid=int(p.name)
   table[pid]={'pid':pid,'ppid':int(fields[1]),'pgid':int(fields[2]),'sid':int(fields[3]),'start_ticks':int(fields[19]),'state':fields[0]}
  except (OSError,ValueError,IndexError):continue
 return table

def retain_owned(table,known):
 changed=True
 while changed:
  changed=False
  for pid,row in table.items():
   parent=table.get(row['ppid']);identity=(pid,row['start_ticks'])
   if identity in known:continue
   if row['ppid']==os.getpid() or (parent is not None and (parent['pid'],parent['start_ticks']) in known):
    known[identity]=row.copy();changed=True
 # Track inherited process groups/sessions, including children reparented after
 # launch. Every kill is additionally guarded by retained process start ticks.
 active=[v for pid,v in table.items() if (pid,v['start_ticks']) in known]
 groups={v['pgid'] for v in active};sessions={v['sid'] for v in active}
 for pid,row in table.items():
  if row['pgid'] in groups or row['sid'] in sessions:known.setdefault((pid,row['start_ticks']),row.copy())

def note_adopted(table,launcher_pid,known,adoptions):
 for pid,row in table.items():
  if row['ppid']==os.getpid() and pid!=launcher_pid:
   identity=(pid,row['start_ticks']);known.setdefault(identity,row.copy())
   adoptions.setdefault(identity,{**row,'wait_status':None,'exit_code':None,'term_signal':None,'wait_error':None})

def reap_adopted(launcher_pid,known,adoptions):
 table=process_table();note_adopted(table,launcher_pid,known,adoptions)
 for identity,entry in adoptions.items():
  if entry['wait_status'] is not None or entry['wait_error'] is not None:continue
  pid=identity[0];current=table.get(pid)
  if current is None or current['start_ticks']!=identity[1]:
   entry['wait_error']='adopted identity disappeared before wait status';continue
  try:
   waited,status=os.waitpid(pid,os.WNOHANG)
   if waited:
    entry['wait_status']=status
    if os.WIFEXITED(status):entry['exit_code']=os.WEXITSTATUS(status)
    elif os.WIFSIGNALED(status):entry['term_signal']=os.WTERMSIG(status)
    else:entry['wait_error']='unrecognized adopted wait status'
  except ChildProcessError:entry['wait_error']='adopted identity lacks wait ownership'

def live_owned(table,known):
 return [row for pid,row in table.items() if (pid,row['start_ticks']) in known]
def signal_owned(known,sig):
 table=process_table();retain_owned(table,known)
 groups={r['pgid'] for r in live_owned(table,known)}
 for group in groups:
  if group==os.getpgrp():raise RuntimeError('refuse to signal supervisor group')
  try:os.killpg(group,sig)
  except ProcessLookupError:pass

def artifact_bytes():
 return sum(p.stat().st_size for p in ROOT.rglob('*') if p.is_file())

def execute_owned(argv,env,prefix,timeout_seconds=930,log_limit=LOG_LIMIT):
 stdout=prefix.with_suffix('.stdout.log');stderr=prefix.with_suffix('.stderr.log');record=prefix.with_suffix('.outer.json')
 known={};adoptions={};forced=False;reason='';status=None;proc=None;previous_subreaper=None;start=time.monotonic()
 write_fresh(prefix.with_suffix('.started.json'),{'schema_version':1,'state':'running','argv':argv,'deadline_seconds':timeout_seconds,'artifact_rejection_threshold_bytes':BUDGET,'per_stream_rejection_threshold_bytes':log_limit,'threshold_semantics':'polled rejection; overshoot possible; complete logs retained; any observed overflow rejects','stream_check_seconds':POLL_SECONDS,'artifact_tree_check_seconds':INVENTORY_SECONDS})
 try:
  if artifact_bytes()>max(0,BUDGET-AUDIT_RESERVE):raise RuntimeError('total task artifact bound exceeded before launch')
  if any(row['ppid']==os.getpid() for row in process_table().values()):raise RuntimeError('supervisor already owns an unrelated child')
  previous_subreaper=subreaper(True)
  if subreaper()!=1:raise RuntimeError('subreaper ownership was not enabled')
  with stdout.open('xb') as out,stderr.open('xb') as err:
   proc=subprocess.Popen(argv,env=env,stdout=out,stderr=err,start_new_session=True)
   initial=process_table().get(proc.pid)
   if initial is None:raise RuntimeError('launcher identity disappeared before audit')
   known[(proc.pid,initial['start_ticks'])]=initial.copy()
   last_inventory=time.monotonic();last_artifact_bytes=artifact_bytes()
   while True:
    status=proc.poll();now=time.monotonic()
    if stdout.stat().st_size>log_limit or stderr.stat().st_size>log_limit:reason='per-stream log bound exceeded'
    elif now-start>timeout_seconds:reason='outer launcher deadline exceeded'
    # Timed execution inventories the full process/task trees at most once per
    # second. Direct child status and two log sizes use only the fast poll.
    if now-last_inventory>=INVENTORY_SECONDS or status is not None or reason:
     table=process_table();retain_owned(table,known);note_adopted(table,proc.pid,known,adoptions)
     if adoptions:reason='unexpected adopted descendant'
     last_artifact_bytes=artifact_bytes();last_inventory=now
    if last_artifact_bytes>max(0,BUDGET-AUDIT_RESERVE):reason='total task artifact bound exceeded (1 MiB reserved for durable audits)'
    if reason:forced=True;break
    if status is not None:break
    time.sleep(POLL_SECONDS)
   if not forced:
    # The mature shell has completed its own namespace/group cleanup. Give only
    # the audit a short reap interval; any survivor makes this an invalid arm.
    deadline=time.monotonic()+2
    while time.monotonic()<deadline:
     table=process_table();retain_owned(table,known);note_adopted(table,proc.pid,known,adoptions)
     if not live_owned(table,known):break
     time.sleep(.05)
    if live_owned(process_table(),known) or adoptions:forced=True;reason='owned process/group remained or unexpected adoption after mature runner exit'
 except BaseException as error:
  forced=True;reason=type(error).__name__+': '+str(error)
 finally:
  if proc is not None:
   table=process_table();retain_owned(table,known);note_adopted(table,proc.pid,known,adoptions)
   if adoptions and not forced:forced=True;reason='unexpected adopted descendant before final cleanup'
  if forced and proc is not None:
   # TERM first preserves the mature cleanup traps; KILL only after bounded
   # grace. Every forced path remains rejected even after successful cleanup.
   signal_owned(known,signal.SIGTERM);deadline=time.monotonic()+10
   while time.monotonic()<deadline:
    reap_adopted(proc.pid,known,adoptions)
    if proc.poll() is not None and not live_owned(process_table(),known):break
    time.sleep(.05)
   if live_owned(process_table(),known):signal_owned(known,signal.SIGKILL)
   try:proc.wait(timeout=5)
   except subprocess.TimeoutExpired:reason+='; launcher did not reap after KILL'
   deadline=time.monotonic()+2
   while time.monotonic()<deadline:
    reap_adopted(proc.pid,known,adoptions)
    if not live_owned(process_table(),known):break
    time.sleep(.05)
  if proc is not None:reap_adopted(proc.pid,known,adoptions)
  table=process_table();retain_owned(table,known);remaining=live_owned(table,known)
  if proc is not None:status=proc.poll()
  if adoptions and not forced:forced=True;reason='unexpected adopted descendant or unknown wait status at final audit'
  value={'schema_version':1,'ownership_mode':'linux-child-subreaper','argv':argv,'status':status,'forced':forced,'failure_reason':reason,'remaining_owned':remaining,'adopted_descendants':list(adoptions.values()),'owned_identities':list(known.values()),'elapsed_seconds':time.monotonic()-start,'artifact_bytes':artifact_bytes(),'artifact_rejection_threshold_bytes':BUDGET,'stdout_bytes':stdout.stat().st_size if stdout.exists() else 0,'stderr_bytes':stderr.stat().st_size if stderr.exists() else 0,'per_stream_rejection_threshold_bytes':log_limit,'threshold_semantics':'polled rejection; overshoot possible; complete logs retained; any observed overflow rejects','stream_check_seconds':POLL_SECONDS,'artifact_tree_check_seconds':INVENTORY_SECONDS,'accepted_cleanup':not forced and remaining==[]}
  write_fresh(record,value)
  if previous_subreaper is not None and not remaining:subreaper(previous_subreaper)
 if forced or remaining or status not in [0,1]:raise RuntimeError('outer command invalid; durable outer audit: '+str(record))
 return status

def finish_screen(runroot,screen):
 screen['artifact_bytes']=artifact_bytes();screen['artifact_rejection_threshold_bytes']=BUDGET
 screen['threshold_semantics']='polled rejection; overshoot possible; complete logs retained; any observed overflow rejects'
 screen['stream_check_seconds']=POLL_SECONDS;screen['artifact_tree_check_seconds']=INVENTORY_SECONDS
 if screen['artifact_bytes']>BUDGET-AUDIT_RESERVE:screen.update({'accepted':False,'stopped':True,'failure':'final artifact bound exceeded'})
 write_fresh(runroot/'screen-final.json',screen)
 if not screen['accepted']:raise RuntimeError(screen.get('failure','screen stopped without complete acceptance'))

def main():
 if sys.argv[1:]!=['--execute-approved-once']:raise SystemExit('requires separately approved runtime command')
 frozen=json.loads((ROOT/'COMMANDS-environment-recovery.json').read_text());grant=(ROOT/'REVIEWED-RECEIPT-SHA256').read_text().strip()
 if len(grant)!=64 or any(x not in '0123456789abcdef' for x in grant):raise SystemExit('reviewed build receipt absent')
 for entry in json.loads((ROOT/'FROZEN-INPUTS.json').read_text()):
  if hashlib.sha256(Path(entry['path']).read_bytes()).hexdigest()!=entry['sha256']:raise SystemExit('frozen source/runner/plan input changed')
 runroot=ROOT/'runner-source/integration/runs/controller-crc-environment-recovery-once'
 if runroot.exists():raise SystemExit('fresh complete screen output required; no retry')
 runroot.mkdir(parents=True);decisions=[];screen={'schema_version':1,'accepted':False,'completed_arms':0,'stopped':True}
 try:
  for index,command in enumerate(frozen['arms']):
   env=os.environ.copy();env['P2PSTREAM_QUALIFICATION_DATA_CONTROLLER']=('cubic','reno','cubic')[index]
   write_fresh(runroot/f'arm-{index}.invocation.json',command)
   status=execute_owned(command['argv'],env,runroot/f'arm-{index}',930)
   decision=runroot/f'arm-{index}.decision.json'
   check=[sys.executable,'-I','-B',str(ROOT/'runner-source/integration/validate-controller-screen.py'),'arm',str(index),command['output_dir'],str(status),str(decision)]
   checked=execute_owned(check,{'PATH':'/usr/bin:/bin','PYTHONDONTWRITEBYTECODE':'1'},runroot/f'arm-{index}-validation',60,1<<20)
   if checked!=0:raise RuntimeError('independent arm validation failed')
   decisions.append(str(decision));screen['completed_arms']=len(decisions)
   if index==1:
    a,b=[json.loads(Path(p).read_text()) for p in decisions]
    for seq in ['11','14']:
     if b['speeds'][seq]<1.08*a['speeds'][seq] or b['read_ns'][seq]>=a['read_ns'][seq]:raise RuntimeError('candidate failed first-control effect; no final arm, retry or tuning')
  status=execute_owned([sys.executable,'-I','-B',str(ROOT/'runner-source/integration/validate-controller-screen.py'),'screen',*decisions,str(runroot/'decision.json')],{'PATH':'/usr/bin:/bin'},runroot/'screen-validation',20,1<<20)
  if status!=0:raise RuntimeError('final C/R/C comparison failed')
  screen.update({'accepted':True,'stopped':False,'interpretation':'broad controller dependence only'})
 except BaseException as error:
  screen['failure']=type(error).__name__+': '+str(error);raise
 finally:
  finish_screen(runroot,screen)
def terminate(signum,frame):
 raise RuntimeError('supervisor received signal '+str(signum))
if __name__=='__main__':
 signal.signal(signal.SIGTERM,terminate)
 main()
