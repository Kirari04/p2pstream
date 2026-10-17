#!/usr/bin/env python3
"""Build the explicitly mapped qualification-only threshold-history evidence package."""
from __future__ import annotations
import argparse,csv,gzip,hashlib,json,os,shutil,stat,tarfile,tempfile
from pathlib import Path,PurePosixPath
HERE=Path(__file__).resolve().parent
ALLOWLIST=HERE/'ALLOWLIST.tsv'; PACKAGE_MAP=HERE/'PACKAGE-MAP.tsv'
BUNDLE_NAME='ancillary-threshold-history-evidence.tar.gz'; BUNDLE_HASH_NAME='ANCILLARY-BUNDLE.sha256'
MAP_FIELDS=('placement','sha256','bytes','mode','category','destination','source'); ALLOW_FIELDS=('sha256','category','destination','source')
METADATA={name:name for name in ('README.md', 'ATTEMPT-HISTORY.md', 'EVIDENCE-GAPS.md', 'EXCLUSIONS.md', 'SECURITY-SCAN.md', 'SECRET-SCAN.json', 'SECRET-SCAN-BINDING.json', 'SECRET-SCAN-INPUT-ALLOWLIST.tsv', 'SELECTION-INVENTORY.tsv', 'SELECTION-ROOTS.tsv', 'SELECTION-SUMMARY.json', 'EXCLUDED-RUN-ARTIFACTS.tsv', 'HASH-ONLY-BINARIES.tsv', 'ACCEPTED-BINDINGS.tsv', 'WAN-ASTRA-REVIEW.md', 'IMMUTABLE-CUTOFF.json', 'scan-candidates.py', 'build-evidence-package.py', 'verify-evidence-package.py', 'prepare-selection.py', 'prepare-run-selection.py', 'RUN-SELECTIONS.plan.json', 'REUSE-SOURCES.tsv', 'PROPOSED-ARCHIVE-PLAN.md', 'SELECTION-VERIFICATION.json', 'OMITTED-ROOTS.plan.tsv', 'TINY1-OMISSIONS.plan.tsv', 'TINY2-OMISSIONS.plan.tsv')}
def digest(path):
 h=hashlib.sha256()
 with path.open('rb') as f:
  for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
 return h.hexdigest()
def safe(value,label):
 p=PurePosixPath(value)
 if not value or p.is_absolute() or '.' in p.parts or '..' in p.parts or p.as_posix()!=value:raise SystemExit(f'unsafe {label}: {value!r}')
 return p
def regular(root,value):
 cur=root
 for part in safe(value,'source').parts:
  cur=cur/part
  try:m=cur.lstat().st_mode
  except FileNotFoundError as e:raise SystemExit(f'missing source: {cur}') from e
  if stat.S_ISLNK(m):raise SystemExit(f'symlink source component: {cur}')
 if not cur.is_file() or not stat.S_ISREG(cur.stat().st_mode):raise SystemExit(f'source is not a regular file: {cur}')
 return cur
def guard(row):
 s=PurePosixPath(row['source']); low=s.as_posix().lower()
 bad=('.key.pem','.ready.json','/.env','/__pycache__/','.pyc','/gocache','/gopath','/build/bin/','/bin/current-','/audited-build-attempt-1/source/','/baseline/source/','/candidate/source/','/proxy-agent-growth.')
 if any(x in low for x in bad) or s.suffix in {'.a','.test','.db','.sqlite','.sqlite3'}:raise SystemExit(f"forbidden selected artifact: {row['source']}")
 if s.name in {'source.tar','source-8ac4c8d.tar'}:raise SystemExit(f"full source archive selected: {row['source']}")
def load_rows(root):
 with ALLOWLIST.open(newline='') as f:
  rr=csv.DictReader(f,delimiter='\t')
  if tuple(rr.fieldnames or())!=ALLOW_FIELDS:raise SystemExit('invalid allowlist header')
  allow=list(rr)
 with PACKAGE_MAP.open(newline='') as f:
  rr=csv.DictReader(f,delimiter='\t')
  if tuple(rr.fieldnames or())!=MAP_FIELDS:raise SystemExit('invalid package-map header')
  rows=list(rr)
 left={(r['sha256'],r['category'],r['destination'],r['source']) for r in allow}; right={(r['sha256'],r['category'],r['destination'],r['source']) for r in rows}
 if not rows or len(left)!=len(allow) or len(right)!=len(rows) or left!=right:raise SystemExit('PACKAGE-MAP.tsv is not a one-to-one partition of ALLOWLIST.tsv')
 with (HERE/'SECRET-SCAN-INPUT-ALLOWLIST.tsv').open(newline='') as f:
  rr=csv.DictReader(f,delimiter='\t');scan_rows=list(rr)
  if tuple(rr.fieldnames or())!=('sha256','bytes','placement','category','destination','source'):raise SystemExit('invalid secret-scan allowlist header')
 scan_tuples={(r['sha256'],r['bytes'],r['placement'],r['category'],r['destination'],r['source']) for r in scan_rows};map_tuples={(r['sha256'],r['bytes'],r['placement'],r['category'],r['destination'],r['source']) for r in rows}
 if len(scan_tuples)!=len(scan_rows) or scan_tuples!=map_tuples:raise SystemExit('secret-scan selection does not exactly match PACKAGE-MAP.tsv')
 dests=set(); sources=set()
 for row in rows:
  if row['placement'] not in {'archive','loose'} or row['mode'] not in {'0644','0755'}:raise SystemExit(f"invalid map record: {row['destination']}")
  safe(row['destination'],'destination');safe(row['source'],'source')
  if row['destination'] in dests or row['source'] in sources:raise SystemExit(f"duplicate map record: {row['destination']}")
  dests.add(row['destination']);sources.add(row['source']);guard(row);src=regular(root,row['source'])
  if src.stat().st_size!=int(row['bytes']) or digest(src)!=row['sha256']:raise SystemExit(f"source binding mismatch: {row['source']}")
 scan=json.loads((HERE/'SECRET-SCAN.json').read_text())
 if not scan.get('accepted') or scan.get('genuine_hit_artifacts') or scan.get('values_printed')!=0 or scan.get('candidate_files')!=len(rows) or scan.get('candidate_bytes')!=sum(int(r['bytes']) for r in rows):raise SystemExit('nonprinting exact-value scan or binding did not pass')
 binding=json.loads((HERE/'SECRET-SCAN-BINDING.json').read_text())
 if not binding.get('accepted') or binding['inputs'].get('SECRET-SCAN.json')!=digest(HERE/'SECRET-SCAN.json') or binding['inputs'].get('SECRET-SCAN-INPUT-ALLOWLIST.tsv')!=digest(HERE/'SECRET-SCAN-INPUT-ALLOWLIST.tsv'):raise SystemExit('secret-scan binding mismatch')
 return sorted(rows,key=lambda r:r['destination'])
def copy_exact(src,dst,mode):
 dst.parent.mkdir(parents=True,exist_ok=True)
 if dst.exists() or dst.is_symlink():raise SystemExit(f'output collision: {dst}')
 with src.open('rb') as i,dst.open('xb') as o:shutil.copyfileobj(i,o,1024*1024)
 dst.chmod(mode)
def bundle(rows,root,out):
 target=out/BUNDLE_NAME;fd,name=tempfile.mkstemp(prefix=f'.{BUNDLE_NAME}.',dir=out);os.close(fd);tmp=Path(name)
 try:
  with tmp.open('wb') as raw,gzip.GzipFile(filename='',mode='wb',fileobj=raw,compresslevel=9,mtime=0) as gz,tarfile.open(fileobj=gz,mode='w',format=tarfile.GNU_FORMAT) as tar:
   for row in rows:
    if row['placement']!='archive':continue
    src=regular(root,row['source']);info=tarfile.TarInfo(row['destination']);info.size=int(row['bytes']);info.mode=int(row['mode'],8);info.uid=info.gid=info.mtime=0;info.uname=info.gname='';info.type=tarfile.REGTYPE
    with src.open('rb') as f:tar.addfile(info,f)
  os.replace(tmp,target);target.chmod(0o644)
 finally:tmp.unlink(missing_ok=True)
 (out/BUNDLE_HASH_NAME).write_text(f'{digest(target)}  {BUNDLE_NAME}\n')
def root_index(root):
 idx=root/'SHA256SUMS';files=sorted(p for p in root.rglob('*') if p.is_file() and p!=idx);idx.write_text(''.join(f'{digest(p)}  {p.relative_to(root).as_posix()}\n' for p in files))
def main():
 p=argparse.ArgumentParser();a=p.add_mutually_exclusive_group(required=True);a.add_argument('--check-plan',action='store_true');a.add_argument('--build',action='store_true');p.add_argument('--source-root',required=True,type=Path);p.add_argument('--output-root',type=Path);args=p.parse_args();root=args.source_root.resolve();rows=load_rows(root);arc=[r for r in rows if r['placement']=='archive'];loose=[r for r in rows if r['placement']=='loose'];print(f"validated plan: archive={len(arc)} files/{sum(int(r['bytes']) for r in arc)} bytes; loose={len(loose)} files/{sum(int(r['bytes']) for r in loose)} bytes")
 if args.check_plan:
  if args.output_root is not None:p.error('--output-root is only valid with --build')
  return
 if args.output_root is None:p.error('--build requires --output-root')
 out=args.output_root.resolve()
 if out.exists() and (out.is_symlink() or any(out.iterdir())):raise SystemExit('output root must be absent or empty')
 out.mkdir(parents=True,exist_ok=True)
 for row in loose:copy_exact(regular(root,row['source']),out/row['destination'],int(row['mode'],8))
 bundle(rows,root,out)
 for src,dst in METADATA.items():copy_exact(HERE/src,out/dst,0o755 if src.endswith('.py') else 0o644)
 copy_exact(ALLOWLIST,out/'ALLOWLIST.tsv',0o644);copy_exact(PACKAGE_MAP,out/'PACKAGE-MAP.tsv',0o644);root_index(out);print(f'built package at {out}')
if __name__=='__main__':main()
