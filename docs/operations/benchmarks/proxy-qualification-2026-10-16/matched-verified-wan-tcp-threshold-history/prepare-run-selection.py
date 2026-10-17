#!/usr/bin/env python3
"""Prepare ledger-bound run selections; never copy, compress, or print file bytes."""
from __future__ import annotations
import argparse,csv,hashlib,json,re,stat
from pathlib import Path,PurePosixPath
HEX=re.compile(r'^[0-9a-f]{64}$')
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def safe(v):
 p=PurePosixPath(v)
 if not v or p.is_absolute() or '..' in p.parts or p.as_posix()!=v:raise SystemExit('unsafe relative path')
 return p
def regular(root,v,directory=False):
 p=root
 for part in safe(v).parts:
  p=p/part
  if p.is_symlink():raise SystemExit('symlink in selected path')
 if not (p.is_dir() if directory else p.is_file() and stat.S_ISREG(p.stat().st_mode)):raise SystemExit('missing or invalid selected path')
 return p
def table(out,name,fields,rows):
 with (out/name).open('x',newline='') as f:
  w=csv.DictWriter(f,fieldnames=fields,delimiter='\t',lineterminator='\n');w.writeheader();w.writerows(rows)
def main():
 p=argparse.ArgumentParser();p.add_argument('--worktree',type=Path,required=True);p.add_argument('--config',type=Path,default=Path(__file__).with_name('RUN-SELECTIONS.plan.json'));p.add_argument('--output',type=Path,required=True);a=p.parse_args();root=a.worktree.resolve();config=json.loads(a.config.read_text())
 if config.get('schema_version')!=1:raise SystemExit('unsupported configuration')
 helper=config['mature_helpers'];source=regular(root,helper['source'])
 if digest(source)!=helper['sha256']:raise SystemExit('mature helper hash mismatch')
 scope={'__file__':str(source),'__name__':'_mature_selection'};exec(compile(source.read_bytes(),str(source),'exec'),scope)
 selected=[];omitted=[];summaries=[];pending=[];names=set()
 for run in config['runs']:
  name=run['name']
  if not re.fullmatch(r'[a-z0-9-]+',name) or name in names:raise SystemExit('invalid or duplicate run name')
  names.add(name)
  if run['state']=='pending':pending.append(name);continue
  if run['rule'] not in {'tiny-safe','sustained-safe'}:raise SystemExit('unsupported run rule')
  runroot=regular(root,run['root'],directory=True);ledger=regular(root,run['ledger']);expected=run['ledger_sha256']
  if not HEX.fullmatch(expected) or digest(ledger)!=expected:raise SystemExit('run ledger hash mismatch')
  entries={}
  for line in ledger.read_text().splitlines():
   sha,sep,rel=line.partition('  ')
   if sep!='  ' or not HEX.fullmatch(sha) or rel in entries:raise SystemExit('invalid or duplicate ledger entry')
   safe(rel);entries[rel]=sha
  actual=set()
  for f in runroot.rglob('*'):
   if f.is_symlink():raise SystemExit('symlink in frozen run')
   if f.is_file():actual.add(f.relative_to(runroot).as_posix())
  if actual!=set(entries):raise SystemExit('frozen run ledger file coverage mismatch')
  count=0;size=0
  for rel,sha in sorted(entries.items()):
   f=regular(runroot,rel);n=f.stat().st_size;source_path=(PurePosixPath(run['root'])/rel).as_posix();relative=PurePosixPath(rel);reason=scope['omitted'](relative,run['rule']);size+=n
   common={'run':name,'sha256':sha,'bytes':str(n),'source':source_path,'full_run_ledger_sha256':expected}
   if reason:
    omitted.append({**common,'reason':scope['run_omission_reason'](relative)})
   else:
    category='tiny-run' if run['rule']=='tiny-safe' else 'sustained-run';spec={'category':category,'placement':'loose'}
    selected.append({**common,'placement':scope['selected_placement'](spec,relative),'mode':'0755' if f.stat().st_mode&0o111 else '0644','category':category,'destination':scope['destination'](source_path)});count+=1
  summaries.append({'run':name,'state':run['state'],'full_files':len(entries),'full_bytes':size,'selected_files':count,'omitted_files':len(entries)-count,'ledger_sha256':expected})
 if len({r['destination'] for r in selected})!=len(selected):raise SystemExit('duplicate selected destination')
 a.output.mkdir(parents=True,exist_ok=False)
 table(a.output,'RUN-SELECTED.plan.tsv',('run','sha256','bytes','placement','mode','category','destination','source','full_run_ledger_sha256'),selected)
 table(a.output,'RUN-OMISSIONS.plan.tsv',('run','sha256','bytes','source','reason','full_run_ledger_sha256'),omitted)
 result={'schema_version':1,'phase':'ledger-bound-stat-only','config_sha256':digest(a.config),'mature_helper_sha256':helper['sha256'],'runs':summaries,'pending_runs':pending,'selected_files':len(selected),'omitted_files':len(omitted),'artifact_hashes_recomputed':False,'artifact_bytes_copied':False,'secret_scan_executed':False,'archive_built':False}
 (a.output/'SUMMARY.json').write_text(json.dumps(result,indent=2,sort_keys=True)+'\n')
 files=sorted(a.output.iterdir());(a.output/'SHA256SUMS.txt').write_text(''.join(f'{digest(f)}  {f.name}\n' for f in files))
 print(json.dumps(result,sort_keys=True))
if __name__=='__main__':main()
