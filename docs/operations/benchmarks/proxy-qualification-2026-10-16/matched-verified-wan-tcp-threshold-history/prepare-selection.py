#!/usr/bin/env python3
"""Prepare exact candidate inventory from frozen roots and safe run partitions."""
from __future__ import annotations
import csv,hashlib,json,stat
from pathlib import Path,PurePosixPath
HERE=Path(__file__).resolve().parent;WORKTREE=HERE.parents[2]
FIELDS=('placement','bytes','mode','category','state','destination','source')
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def read(p):
 with p.open(newline='') as f:return list(csv.DictReader(f,delimiter='\t'))
def write(name,fields,rows):
 with (HERE/name).open('x',newline='') as f:
  w=csv.DictWriter(f,fieldnames=fields,delimiter='\t',lineterminator='\n');w.writeheader();w.writerows(rows)
def regular(value,directory=False):
 q=PurePosixPath(value)
 if q.is_absolute() or q.as_posix()!=value or '..' in q.parts:raise SystemExit('unsafe source')
 p=WORKTREE
 for part in q.parts:
  p/=part
  if p.is_symlink():raise SystemExit('symlink source component')
 if not (p.is_dir() if directory else p.is_file() and stat.S_ISREG(p.stat().st_mode)):raise SystemExit('invalid source')
 return p
def main():
 cfg=json.loads((HERE/'RUN-SELECTIONS.plan.json').read_text());h=cfg['mature_helpers'];helper=regular(h['source'])
 if digest(helper)!=h['sha256']:raise SystemExit('mature helper changed')
 ns={'__file__':str(helper),'__name__':'_mature_selection'};exec(compile(helper.read_bytes(),str(helper),'exec'),ns)
 rows={};excluded=[];omitted_roots=[]
 def add(path,spec,relative):
  source=path.relative_to(WORKTREE).as_posix();regular(source);reason=ns['omitted'](PurePosixPath(relative),'tree')
  if reason:excluded.append({'source':source,'bytes':path.stat().st_size,'reason':reason});return
  row={'placement':spec['placement'],'bytes':str(path.stat().st_size),'mode':'0755' if path.stat().st_mode&0o111 else '0644','category':spec['category'],'state':spec['state'],'destination':ns['destination'](source),'source':source}
  if source in rows and rows[source]!=row:raise SystemExit('conflicting duplicate source selection')
  rows[source]=row
 for spec in read(HERE/'SELECTION-ROOTS.plan.tsv'):
  if spec['placement'] not in {'loose','archive'}:raise SystemExit('invalid placement')
  if spec['rule']=='file':
   path=regular(spec['source']);add(path,spec,path.name);continue
  root=regular(spec['source'],directory=True)
  if spec['rule']=='build-run-no-binaries':
   paths=list((root/'records').rglob('*'))+[p for p in root.iterdir() if p.is_file() and (p.name=='FINAL' or p.name.endswith('-build-receipt.json'))]
   ledger=root/'records/SHA256SUMS.txt'
   for child in root.iterdir():
    if child.is_dir() and child.name!='records':omitted_roots.append({'source':child.relative_to(WORKTREE).as_posix(),'reason':'copied source tree, workspace, cache, or binary output','authoritative_ledger':ledger.relative_to(WORKTREE).as_posix(),'ledger_sha256':digest(ledger)})
  elif spec['rule']=='tree':paths=list(root.rglob('*'))
  else:raise SystemExit('unsupported source selection rule')
  for path in sorted(paths):
   if path.is_symlink():raise SystemExit('symlink in source selection')
   if path.is_file():add(path,spec,path.relative_to(root).as_posix())
 runroot=HERE/'run-selection-attempt-1';runselected=read(runroot/'RUN-SELECTED.plan.tsv');runomitted=read(runroot/'RUN-OMISSIONS.plan.tsv')
 for row in runselected:
  path=regular(row['source'])
  if path.stat().st_size!=int(row['bytes']):raise SystemExit('selected run size changed')
  rec={k:row[k] for k in FIELDS if k!='state'};rec['state']='closed-failed-preserved'
  if rec['source'] in rows:raise SystemExit('run source already selected by broad root')
  rows[rec['source']]=rec
 selected=sorted(rows.values(),key=lambda x:x['destination'])
 if len({r['destination'] for r in selected})!=len(selected):raise SystemExit('duplicate destination')
 write('PROSPECTIVE-INVENTORY.tsv',FIELDS,selected)
 write('EXCLUDED-NONSOURCE.plan.tsv',('source','bytes','reason'),excluded)
 write('OMITTED-ROOTS.plan.tsv',('source','reason','authoritative_ledger','ledger_sha256'),omitted_roots)
 allomissions=[]
 for run in cfg['runs']:
  if run['state']=='pending':continue
  prefix=run['root']+'/'
  omissions=[{'sha256':row['sha256'],'bytes':row['bytes'],'relative_path':row['source'][len(prefix):],'reason':row['reason'],'full_run_ledger_sha256':row['full_run_ledger_sha256']} for row in runomitted if row['run']==run['name']]
  write(run['name'].upper()+'-OMISSIONS.plan.tsv',('sha256','bytes','relative_path','reason','full_run_ledger_sha256'),omissions)
  allomissions.extend({'run':run['name'],'source':prefix+row['relative_path'],**{k:row[k] for k in ('sha256','bytes','reason','full_run_ledger_sha256')}} for row in omissions)
 write('EXCLUDED-RUN-ARTIFACTS.tsv',('run','source','sha256','bytes','reason','full_run_ledger_sha256'),allomissions)
 receipt=regular('tmp/continuation-20261016/wan-tcp-threshold-history-source/build-source-1/runs/audited-build-attempt-3/audited-tcp-threshold-history-build-receipt.json');j=json.loads(receipt.read_text());binary=[]
 for role in ('agent','server'):
  meta=j[role+'_binary'];p=receipt.parent/meta['path'];source=p.relative_to(WORKTREE).as_posix();regular(source)
  if meta['sha256']!=j[role+'_binary_sha256']:raise SystemExit('binary receipt hash mismatch')
  binary.append({'role':role,'source':source,'sha256':j[role+'_binary_sha256'],'bytes':p.stat().st_size,'reason':'audited binary omitted; hash from accepted immutable build receipt','authoritative_receipt':receipt.relative_to(WORKTREE).as_posix(),'receipt_sha256':digest(receipt)})
 write('HASH-ONLY-BINARIES.tsv',('role','source','sha256','bytes','reason','authoritative_receipt','receipt_sha256'),binary)
 summary={'schema_version':1,'phase':'prospective-stat-only','selected_files':len(selected),'selected_bytes':sum(int(r['bytes']) for r in selected),'archive_files':sum(r['placement']=='archive' for r in selected),'archive_source_bytes':sum(int(r['bytes']) for r in selected if r['placement']=='archive'),'loose_files':sum(r['placement']=='loose' for r in selected),'loose_bytes':sum(int(r['bytes']) for r in selected if r['placement']=='loose'),'omitted_run_artifacts':len(allomissions),'omitted_roots':len(omitted_roots),'excluded_nonsource_files':len(excluded),'hash_only_binaries':len(binary),'source_bytes_copied':False,'archive_built':False,'secret_scan_executed':False}
 (HERE/'SELECTION-SUMMARY.plan.json').write_text(json.dumps(summary,indent=2,sort_keys=True)+'\n');print(json.dumps(summary,sort_keys=True))
if __name__=='__main__':main()
