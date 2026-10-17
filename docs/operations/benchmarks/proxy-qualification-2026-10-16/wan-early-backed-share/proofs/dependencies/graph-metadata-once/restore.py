from pathlib import Path
import hashlib,importlib.util,json,os,signal
ROOT=Path(__file__).resolve().parent
WORK=ROOT.parents[2]
PLAN=ROOT/'PLAN.json'
def sha(p): return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def fresh(p,v):
 with p.open('x') as f: json.dump(v,f,indent=2); f.write('\n'); f.flush(); os.fsync(f.fileno())
def main():
 p=json.loads(PLAN.read_text())
 for path,h in p['pins'].items():
  if sha(path)!=h: raise ValueError('input changed: '+path)
 if sha(__file__)!=p['executor_sha256']: raise ValueError('executor changed')
 if (ROOT/'resolve.started.json').exists(): raise ValueError('already attempted')
 spec=importlib.util.spec_from_file_location('accepted_graph_metadata_supervisor',p['supervisor'])
 s=importlib.util.module_from_spec(spec); spec.loader.exec_module(s)
 s.ROOT=Path(p['artifact_root']); s.BUDGET=p['artifact_limit']
 env=os.environ.copy(); env.update(p['environment'])
 os.chdir(p['cwd'])
 def interrupted(signum,frame): raise RuntimeError('metadata restore interrupted')
 signal.signal(signal.SIGTERM,interrupted); signal.signal(signal.SIGINT,interrupted)
 failure=None
 try: s.execute_owned(p['argv'],env,ROOT/'resolve',270,1<<20)
 except BaseException as error: failure=type(error).__name__+': '+str(error)
 a=json.loads((ROOT/'resolve.outer.json').read_text())
 unchanged=all(sha(path)==h for path,h in p['pins'].items())
 accepted=failure is None and a['status']==0 and a['forced'] is False and a['accepted_cleanup'] is True and a['remaining_owned']==[] and a['adopted_descendants']==[] and unchanged
 result={'accepted':accepted,'failure':failure,'inputs_unchanged':unchanged,'plan_sha256':sha(PLAN),'files':{suffix:sha(ROOT/('resolve.'+suffix)) for suffix in ['started.json','outer.json','stdout.log','stderr.log']},'artifact_bytes':s.artifact_bytes(),'compilation':False,'test_execution':False,'endpoint_execution':False}
 fresh(ROOT/'RESULT.json',result)
 print(json.dumps(result))
 if not accepted: raise SystemExit(1)
if __name__=='__main__': main()
