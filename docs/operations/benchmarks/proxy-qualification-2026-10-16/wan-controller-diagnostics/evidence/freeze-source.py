"""Static-only final hash/diff/inventory freeze. Does not import/run proposal."""
from pathlib import Path
import ast,difflib,hashlib,json,subprocess
r=Path(__file__).resolve().parent;old=r.parent/'wan-terminal-attribution-20261017'
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
source=[];changes=[]
for p in sorted((r/'test-source').rglob('*')):
 if p.is_file():
  name=str(p.relative_to(r));source.append((sha(p),name));q=old/name
  if not q.exists() or q.read_bytes()!=p.read_bytes():changes.append(name)
assert len(source)==462 and len(changes)==3
(r/'SOURCE-SHA256SUMS').write_text(''.join(f'{h}  {p}\n' for h,p in source))
ref='27c8e31f3826ab5326d941b20644c1f56159d99d+controller-screen-source-'+sha(r/'SOURCE-SHA256SUMS')
commands=json.loads((r/'COMMANDS.json').read_text())
for arm in commands['arms']:
 for opt in ['--server-source-ref','--aggregate-agent-source-ref']:arm['argv'][arm['argv'].index(opt)+1]=ref
(r/'COMMANDS.json').write_text(json.dumps(commands,indent=2)+'\n')
plan=json.loads((r/'BUILD-TEST-PLAN.json').read_text());plan['source_ref']=ref;plan['build_receipt_schema']['source_ref']=ref
prior=json.loads((r/'failed-local-freeze-1/BUILD-TEST-PLAN.json').read_text())
assert sha(r/'failed-local-freeze-1/SHA256SUMS')=='12d0eda626e79501dcacd49360d87dea911ee310406cdf732d0d734e8ea81a22'
assert sha(r/'failed-local-freeze-1/SOURCE-SHA256SUMS')==sha(r/'SOURCE-SHA256SUMS')
assert not (r/'build/continuation-1').exists()
for before,after in zip(prior['tests']+prior['builds'],plan['tests']+plan['builds']):
 assert all(before[k]==after[k] for k in ['name','argv','cwd'])
 if after['proof_origin']=='reused-original-freeze':
  for row in after['reused_proof']['files'].values():assert sha(Path(row['path']))==row['sha256']
for row in plan['continuation']['preserved_attempt_files']:assert sha(Path(row['path']))==row['sha256']
for row in prior['inputs']:
 if not Path(row['path']).is_relative_to(r):assert sha(Path(row['path']))==row['sha256']
for row in plan['inputs']:
 if Path(row['path']).is_relative_to(r):row['sha256']=sha(Path(row['path']))
(r/'BUILD-TEST-PLAN.json').write_text(json.dumps(plan,indent=2)+'\n')
paths=[*changes,'runner-source/integration/run-wan-below-tls-arm.sh','runner-source/integration/generated/qualify-proxy-agent-growth-wan-below-tls.sh','runner-source/integration/verify-terminal-build.py','runner-source/integration/validate-controller-screen.py','runner-source/integration/validate-controller-arm-integrity.py','contracts/test-controller-screen.py','run-controller-screen.py']
diff=''
for name in paths:
 p=r/name;q=old/name
 if name.endswith('validate-controller-arm-integrity.py'):q=old/'runner-source/integration/validate-h1-continuation.py'
 before=q.read_text().splitlines(True) if q.exists() else []
 diff+=''.join(difflib.unified_diff(before,p.read_text().splitlines(True),fromfile='a/'+str(q.relative_to(old)) if q.exists() else '/dev/null',tofile='b/'+name))
(r/'SOURCE.diff').write_text(diff)
delta=''
for name in ['contracts/test-controller-screen.py','runner-source/integration/verify-terminal-build.py','runner-source/integration/run-wan-below-tls-arm.sh','BUILD-TEST-PLAN.json','REVIEW.md','freeze-source.py']:
 delta+=''.join(difflib.unified_diff((r/'failed-local-freeze-1'/name).read_text().splitlines(True),(r/name).read_text().splitlines(True),fromfile='failed-freeze/'+name,tofile='corrected/'+name))
(r/'CONTINUATION.diff').write_text(delta)
py=[r/'prepare-source.py',r/'patch-runner.py',r/'freeze-source.py',r/'run-controller-screen.py',r/'contracts/test-controller-screen.py',*[r/'runner-source/integration'/n for n in ['verify-terminal-build.py','validate-controller-screen.py','validate-controller-arm-integrity.py']]]
for p in py:ast.parse(p.read_text(),filename=str(p))
for p in [r/'runner-source/integration/run-wan-below-tls-arm.sh',r/'runner-source/integration/generated/qualify-proxy-agent-growth-wan-below-tls.sh']:subprocess.run(['bash','-n',str(p)],check=True)
for row in json.loads((r/'BASE-INPUTS.json').read_text()):assert sha(Path(row['path']))==row['sha256']
for row in plan['inputs']:assert sha(Path(row['path']))==row['sha256']
for p in (r/'runner-source/integration').glob('validate-*.py'):
 if not p.name.startswith('validate-controller'):assert p.read_bytes()==(old/'runner-source/integration'/p.name).read_bytes()
(r/'STATIC-CHECKS.json').write_text(json.dumps({'status':'corrected source-only static syntax/hash/diff checks passed; no continuation execution','python_ast_files':len(py),'bash_syntax_files':2,'gofmt':'compilation source unchanged since Go1.26.6 parser/formatter check','base_file_count':460,'result_source_files':462,'changed_compilation_paths':changes,'tracked_changes':False,'builds':0,'tests':0,'wan_transfers':0,'historical_local_attempt':{'commands':5,'natural_zero':4,'failed_python_contract_commands':1,'go_tests':0,'builds':0},'continuation_reused_proofs':4,'continuation_fresh_commands':7,'total_artifact_bytes':sum(p.stat().st_size for p in r.rglob('*') if p.is_file()),'future_task_artifact_rejection_threshold_bytes':256<<20,'byte_threshold_semantics':'polled rejection; overshoot possible; complete logs retained; any observed overflow rejects'},indent=2)+'\n')
exclude={'FROZEN-INPUTS.json','SHA256SUMS','REVIEWED-RECEIPT-SHA256'}
files=sorted(p for p in r.rglob('*') if p.is_file() and str(p.relative_to(r)) not in exclude and 'build' not in p.relative_to(r).parts and 'runs' not in p.relative_to(r).parts)
(r/'FROZEN-INPUTS.json').write_text(json.dumps([{'path':str(p),'sha256':sha(p)} for p in files],indent=2)+'\n')
files.append(r/'FROZEN-INPUTS.json')
(r/'SHA256SUMS').write_text(''.join(f'{sha(p)}  {p.relative_to(r)}\n' for p in sorted(files)))
print(json.dumps({'source_ref':ref,'source_inventory_sha256':sha(r/'SOURCE-SHA256SUMS'),'patch_sha256':sha(r/'SOURCE.diff'),'build_test_plan_sha256':sha(r/'BUILD-TEST-PLAN.json'),'commands_sha256':sha(r/'COMMANDS.json'),'freeze_sha256':sha(r/'SHA256SUMS'),'source_files':len(source),'frozen_files':len(files),'patch_bytes':len(diff)}))
