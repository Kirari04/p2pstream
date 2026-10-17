#!/usr/bin/env python3
"""Verify every qualification-only loose follow-up artifact."""
from __future__ import annotations
import argparse,csv,hashlib,json,re
from pathlib import Path,PurePosixPath
FIELDS=('placement','sha256','bytes','mode','category','destination','source');ALLOW_FIELDS=('sha256','category','destination','source');HEX64=re.compile(r'^[0-9a-f]{64}$')
def dstream(f):
 h=hashlib.sha256()
 for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
 return h.hexdigest()
def digest(p):
 with p.open('rb') as f:return dstream(f)
def safe(v):
 p=PurePosixPath(v)
 if not v or p.is_absolute() or '.' in p.parts or '..' in p.parts or p.as_posix()!=v:raise SystemExit(f'unsafe package path: {v!r}')
 return p
def load(root):
 with (root/'PACKAGE-MAP.tsv').open(newline='') as f:
  r=csv.DictReader(f,delimiter='\t')
  if tuple(r.fieldnames or())!=FIELDS:raise SystemExit('invalid package-map header')
  rows=list(r)
 with (root/'ALLOWLIST.tsv').open(newline='') as f:
  r=csv.DictReader(f,delimiter='\t')
  if tuple(r.fieldnames or())!=ALLOW_FIELDS:raise SystemExit('invalid allowlist header')
  allow=list(r)
 seen=set()
 for x in rows:
  safe(x['destination'])
  if x['destination'] in seen or x['placement']!='loose' or x['mode'] not in {'0644','0755'} or not HEX64.fullmatch(x['sha256']):raise SystemExit(f"invalid/duplicate map record: {x['destination']}")
  seen.add(x['destination'])
 left={(x['sha256'],x['category'],x['destination'],x['source']) for x in allow};right={(x['sha256'],x['category'],x['destination'],x['source']) for x in rows}
 if len(left)!=len(allow) or len(right)!=len(rows) or left!=right:raise SystemExit('PACKAGE-MAP.tsv is not a one-to-one partition of ALLOWLIST.tsv')
 with (root/'SECRET-SCAN-INPUT-ALLOWLIST.tsv').open(newline='') as f:
  r=csv.DictReader(f,delimiter='\t');scan_rows=list(r)
  if tuple(r.fieldnames or())!=('sha256','bytes','placement','category','destination','source'):raise SystemExit('invalid secret-scan allowlist header')
 scan_tuples={(x['sha256'],x['bytes'],x['placement'],x['category'],x['destination'],x['source']) for x in scan_rows};map_tuples={(x['sha256'],x['bytes'],x['placement'],x['category'],x['destination'],x['source']) for x in rows}
 if len(scan_tuples)!=len(scan_rows) or scan_tuples!=map_tuples:raise SystemExit('secret-scan selection does not exactly match PACKAGE-MAP.tsv')
 scan=json.loads((root/'SECRET-SCAN.json').read_text());binding=json.loads((root/'SECRET-SCAN-BINDING.json').read_text())
 if not scan.get('accepted') or scan.get('candidate_files')!=len(rows) or scan.get('candidate_bytes')!=sum(int(x['bytes']) for x in rows) or scan.get('genuine_hit_artifacts') or scan.get('values_printed')!=0:raise SystemExit('secret scan is not accepted for the mapped selection')
 if binding['inputs'].get('SECRET-SCAN.json')!=digest(root/'SECRET-SCAN.json') or binding['inputs'].get('SECRET-SCAN-INPUT-ALLOWLIST.tsv')!=digest(root/'SECRET-SCAN-INPUT-ALLOWLIST.tsv'):raise SystemExit('secret-scan binding mismatch')
 return rows
def verify_root(root):
 expected={}
 for line in (root/'SHA256SUMS').read_text().splitlines():
  value,mark,name=line.partition('  ')
  if mark!='  ' or not HEX64.fullmatch(value) or name in expected:raise SystemExit('invalid root checksum record')
  safe(name);expected[name]=value
 index=root/'SHA256SUMS'
 actual={p.relative_to(root).as_posix() for p in root.rglob('*') if p.is_file() and p!=index}
 if actual!=set(expected):raise SystemExit(f'root checksum coverage mismatch: expected={len(expected)} actual={len(actual)}')
 for name,value in expected.items():
  path=root/name
  if path.is_symlink() or digest(path)!=value:raise SystemExit(f'root checksum mismatch: {name}')
def main():
 p=argparse.ArgumentParser();p.add_argument('--package-root',required=True,type=Path);p.add_argument('--check-plan',action='store_true');a=p.parse_args();root=a.package_root.resolve();rows=load(root)
 if a.check_plan:print(f"valid loose package map: {len(rows)} files");return
 loose={r['destination']:r for r in rows}
 for name,row in loose.items():
  path=root/name
  if path.is_symlink() or not path.is_file() or path.stat().st_size!=int(row['bytes']) or digest(path)!=row['sha256'] or path.stat().st_mode&0o777!=int(row['mode'],8):raise SystemExit(f'loose artifact mismatch: {name}')
 verify_root(root);print(f'verified {len(loose)} loose artifacts and complete root checksums')
if __name__=='__main__':main()
