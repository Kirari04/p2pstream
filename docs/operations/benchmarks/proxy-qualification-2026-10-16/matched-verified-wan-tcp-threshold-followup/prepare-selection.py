#!/usr/bin/env python3
"""Prepare stat-only loose supplement inventory from exact frozen input roots."""
from __future__ import annotations
import csv,hashlib,json,stat
from pathlib import Path,PurePosixPath
HERE=Path(__file__).resolve().parent;WORKTREE=HERE.parents[3]
FIELDS=('placement','bytes','mode','category','state','destination','source')
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def read(name):
 with (HERE/name).open(newline='') as f:return list(csv.DictReader(f,delimiter='\t'))
def write(name,fields,rows):
 with (HERE/name).open('x',newline='') as f:
  w=csv.DictWriter(f,fieldnames=fields,delimiter='\t',lineterminator='\n');w.writeheader();w.writerows(rows)
def regular(value,directory=False):
 q=PurePosixPath(value)
 if not value or q.is_absolute() or q.as_posix()!=value or '..' in q.parts:raise SystemExit('unsafe source path')
 p=WORKTREE
 for part in q.parts:
  p/=part
  if p.is_symlink():raise SystemExit('symlink source component')
 if not (p.is_dir() if directory else p.is_file() and stat.S_ISREG(p.stat().st_mode)):raise SystemExit('missing or invalid source')
 return p
def main():
 bindings=read('ACCEPTED-BINDINGS.tsv')
 for row in bindings:
  path=regular(row['path'])
  if digest(path)!=row['sha256']:raise SystemExit('frozen source binding changed')
  if row['kind']=='ledger':
   for line in path.read_text().splitlines():
    sha,rel=line.split('  ',1);source=(path.parent/PurePosixPath(rel)).relative_to(WORKTREE).as_posix();file=regular(source)
    if digest(file)!=sha:raise SystemExit('frozen ledger artifact changed')
 rows={}
 for spec in read('SELECTION-ROOTS.plan.tsv'):
  if spec['placement']!='loose' or spec['rule'] not in {'file','tree'}:raise SystemExit('invalid supplement root')
  root=regular(spec['source'],directory=spec['rule']=='tree');paths=[root] if spec['rule']=='file' else sorted(root.rglob('*'))
  for path in paths:
   if path.is_symlink():raise SystemExit('symlink in supplement root')
   if not path.is_file():continue
   source=path.relative_to(WORKTREE).as_posix();regular(source)
   if '__pycache__' in path.parts or path.suffix in {'.pyc','.key','.test','.a','.o','.db','.sqlite','.sqlite3'} or path.name.endswith('.key.pem'):raise SystemExit('forbidden artifact under frozen source selection')
   prefix='tmp/continuation-20261016/';dest='evidence/'+(source[len(prefix):] if source.startswith(prefix) else source)
   row={'placement':'loose','bytes':str(path.stat().st_size),'mode':'0755' if path.stat().st_mode&0o111 else '0644','category':spec['category'],'state':spec['state'],'destination':dest,'source':source}
   if source in rows:raise SystemExit('duplicate supplement source')
   rows[source]=row
 selected=sorted(rows.values(),key=lambda r:r['destination'])
 if len({r['destination'] for r in selected})!=len(selected):raise SystemExit('duplicate supplement destination')
 write('PROSPECTIVE-INVENTORY.tsv',FIELDS,selected)
 base='tmp/continuation-20261016/wan-tcp-threshold-history-source/runner-source-1/runs/close-race-reproduction-attempt-1';ledger=regular(base+'/records/SHA256SUMS.txt');omitted=[]
 for name in ('gocache-normal','gocache-race','gopath','tmp-normal','tmp-race'):
  omitted.append({'source':base+'/'+name,'reason':'fresh Go cache/module/temp workspace; outside authoritative evidence records','authoritative_ledger':base+'/records/SHA256SUMS.txt','ledger_sha256':digest(ledger),'payload_scanned_or_hashed':'false'})
 write('OMITTED-ROOTS.tsv',('source','reason','authoritative_ledger','ledger_sha256','payload_scanned_or_hashed'),omitted)
 summary={'schema_version':1,'phase':'exact-frozen-selection','mapped_files':len(selected),'mapped_bytes':sum(int(r['bytes']) for r in selected),'all_placement':'loose','archive_planned':False,'bindings_verified':len(bindings),'omitted_workspace_roots':len(omitted),'omitted_payloads_scanned_or_hashed':False,'source_bytes_copied':False,'secret_scan_executed':False,'qualification_accepted':False}
 (HERE/'SELECTION-SUMMARY.json').write_text(json.dumps(summary,indent=2,sort_keys=True)+'\n');print(json.dumps(summary,sort_keys=True))
if __name__=='__main__':main()
