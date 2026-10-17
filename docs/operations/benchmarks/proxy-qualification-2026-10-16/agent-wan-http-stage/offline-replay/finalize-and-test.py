from pathlib import Path
import copy,difflib,hashlib,json,subprocess,sys
base=Path(__file__).resolve().parent; orig=(base/'validator-original.py').read_text(); fixed=(base/'validator-pool-categories.py').read_text()
fixed=fixed.replace('if not isinstance(cats,dict) or not {"initial_pool"}<=set(cats)<={"initial_pool","growth_pool"}:', 'expected_keys={"initial_pool","growth_pool"} if v["growth_reserved_bytes"]>0 else {"initial_pool"}\n  if not isinstance(cats,dict) or set(cats)!=expected_keys:')
anchor='rows=[]\nfor i in range(16):'
replacement='''for role,initial_doc,live_doc,terminal_doc in (("server",si_doc,sl_doc,st_doc),("agent",ai_doc,al_doc,at_doc)):
 initial_pool,live_pool,terminal_pool=(doc["pool_ledger"] for doc in (initial_doc,live_doc,terminal_doc))
 if initial_pool["initial_reserved_bytes"]!=live_pool["initial_reserved_bytes"]: raise ValueError(f"{role} initial pool reservation changed")
 for key in ("initial_reserved_bytes","growth_reserved_bytes","high_water_bytes"):
  if live_pool[key]!=terminal_pool[key]: raise ValueError(f"{role} terminal pool cumulative {key} changed")
rows=[]
for i in range(16):'''
assert fixed.count(anchor)==1; fixed=fixed.replace(anchor,replacement)
(base/'validator-pool-categories.py').write_text(fixed)
(base/'validator-correction.patch').write_text(''.join(difflib.unified_diff(orig.splitlines(True),fixed.splitlines(True),fromfile='validator-original.py',tofile='validator-pool-categories.py')))
args=json.loads((base/'arguments.json').read_text()); namespace={}; oldargv=sys.argv;sys.argv=['validator',*args]
try: exec(compile(fixed.split('si_doc,ai_doc=load(si_path)',1)[0],str(base/'validator-pool-categories.py'),'exec'),namespace)
finally: sys.argv=oldargv
ledger=namespace['ledger'];cases=[]
for role,offset in (('server',7),('agent',8)):
 for phase,idx,terminal in (('initial',offset,False),('live',offset+2,False),('terminal',offset+4,True)):
  value=json.loads(Path(args[idx]).read_text())['pool_ledger']
  ledger(value,role+' '+phase,2147483648,terminal=terminal); cases.append({'name':role+'-'+phase,'expected':'accept','passed':True})
initial=json.loads(Path(args[7]).read_text())['pool_ledger'];live=json.loads(Path(args[9]).read_text())['pool_ledger'];terminal=json.loads(Path(args[11]).read_text())['pool_ledger']
def reject(name,seed,mutate,terminal=False):
 v=copy.deepcopy(seed);mutate(v)
 try: ledger(v,name,2147483648,terminal=terminal)
 except ValueError: cases.append({'name':name,'expected':'reject','passed':True});return
 raise AssertionError('malformed pool accepted: '+name)
reject('missing-categories',initial,lambda v:v.pop('categories'))
reject('nonobject-categories',initial,lambda v:v.update(categories=[]))
reject('unknown-category',initial,lambda v:v['categories'].update(unknown=0))
reject('zero-growth-category-not-created',initial,lambda v:v['categories'].update(growth_pool=0))
reject('missing-initial-category',initial,lambda v:v['categories'].pop('initial_pool'))
reject('missing-positive-growth-category',live,lambda v:v['categories'].pop('growth_pool'))
for name,val in [('bool',False),('float',0.0),('negative',-1)]: reject(name+'-category',initial,lambda v,val=val:v['categories'].update(initial_pool=val))
reject('category-sum-mismatch',live,lambda v:v['categories'].update(initial_pool=v['categories']['initial_pool']+1))
def swapped(v): v['categories']['initial_pool'],v['categories']['growth_pool']=v['categories']['growth_pool'],v['categories']['initial_pool']
reject('swapped-categories-preserve-sum',live,swapped)
reject('terminal-nonzero-category',terminal,lambda v:v['categories'].update(initial_pool=1),terminal=True)
command=['python3',str(base/'validator-pool-categories.py'),*args]
r=subprocess.run(command,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=20)
(base/'replay.stdout').write_bytes(r.stdout);(base/'replay.stderr').write_bytes(r.stderr);(base/'replay.exit').write_text(str(r.returncode)+'\n')
if r.returncode: raise AssertionError(r.stderr.decode())
cases.append({'name':'complete-saved-cohort-replay','expected':'accept','passed':True})
# Cross-snapshot corruption must fail before derived output publication.
for role,idx in (('server',11),('agent',12)):
 original=json.loads(Path(args[idx]).read_text())
 for key in ('initial_reserved_bytes','growth_reserved_bytes','high_water_bytes'):
  mutant=copy.deepcopy(original)
  # Mutate all cumulative totals coherently so the individual terminal ledger remains valid.
  if key=='initial_reserved_bytes': mutant['pool_ledger']['initial_reserved_bytes']+=65536;mutant['pool_ledger']['high_water_bytes']+=65536
  elif key=='growth_reserved_bytes': mutant['pool_ledger']['growth_reserved_bytes']+=65536;mutant['pool_ledger']['high_water_bytes']+=65536
  else: continue # high_water binding is algebraically covered by the two cumulative totals.
  d=base/'negative-cases'/(role+'-'+key);d.mkdir(parents=True,exist_ok=True); path=d/'terminal.json';path.write_text(json.dumps(mutant))
  altered=list(args);altered[idx]=str(path);altered[14]=str(d/'results.jsonl');altered[15]=str(d/'snapshots.jsonl')
  result=subprocess.run(['python3',str(base/'validator-pool-categories.py'),*altered],stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=20)
  (d/'stderr.log').write_bytes(result.stderr);(d/'exit').write_text(str(result.returncode)+'\n')
  assert result.returncode!=0 and not Path(altered[14]).exists() and not Path(altered[15]).exists()
  cases.append({'name':role+'-cumulative-'+key+'-changed','expected':'reject','passed':True})
(base/'contracts.json').write_text(json.dumps({'cases':len(cases),'failed':[],'results':cases},indent=2)+'\n')
lineage=json.loads((base/'lineage.json').read_text());lineage['corrected_validator_sha256']=hashlib.sha256(fixed.encode()).hexdigest();lineage['patch_sha256']=hashlib.sha256((base/'validator-correction.patch').read_bytes()).hexdigest();lineage['contracts_sha256']=hashlib.sha256((base/'contracts.json').read_bytes()).hexdigest();lineage['replayed_exit_code']=r.returncode
for path,digest in lineage['input_files'].items(): assert hashlib.sha256(Path(path).read_bytes()).hexdigest()==digest
(base/'lineage.json').write_text(json.dumps(lineage,indent=2)+'\n')
print(json.dumps({'cases':len(cases),'failed':[],'classification':json.loads((base/'results.summary.json').read_text())['classification']},indent=2))
