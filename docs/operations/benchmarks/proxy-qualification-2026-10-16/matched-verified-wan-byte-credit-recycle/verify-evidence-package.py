#!/usr/bin/env python3
"""Verify every loose and archived artifact without extracting the archive."""
from __future__ import annotations
import argparse,csv,hashlib,re,tarfile
from pathlib import Path,PurePosixPath
BUNDLE_NAME='ancillary-byte-credit-recycle-evidence.tar.gz';BUNDLE_HASH_NAME='ANCILLARY-BUNDLE.sha256';FIELDS=('placement','sha256','bytes','mode','category','destination','source');ALLOW_FIELDS=('sha256','category','destination','source');HEX64=re.compile(r'^[0-9a-f]{64}$')
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
  if x['destination'] in seen or x['placement'] not in {'archive','loose'} or x['mode'] not in {'0644','0755'} or not HEX64.fullmatch(x['sha256']):raise SystemExit(f"invalid/duplicate map record: {x['destination']}")
  seen.add(x['destination'])
 left={(x['sha256'],x['category'],x['destination'],x['source']) for x in allow};right={(x['sha256'],x['category'],x['destination'],x['source']) for x in rows}
 if len(left)!=len(allow) or len(right)!=len(rows) or left!=right:raise SystemExit('PACKAGE-MAP.tsv is not a one-to-one partition of ALLOWLIST.tsv')
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
 if a.check_plan:print(f"valid package map: archive={sum(r['placement']=='archive' for r in rows)}; loose={sum(r['placement']=='loose' for r in rows)}");return
 loose={r['destination']:r for r in rows if r['placement']=='loose'};arc={r['destination']:r for r in rows if r['placement']=='archive'}
 for name,row in loose.items():
  path=root/name
  if path.is_symlink() or not path.is_file() or path.stat().st_size!=int(row['bytes']) or digest(path)!=row['sha256'] or path.stat().st_mode&0o777!=int(row['mode'],8):raise SystemExit(f'loose artifact mismatch: {name}')
 words=(root/BUNDLE_HASH_NAME).read_text().split();bundle=root/BUNDLE_NAME
 if len(words)!=2 or not HEX64.fullmatch(words[0]) or words[1]!=BUNDLE_NAME or digest(bundle)!=words[0]:raise SystemExit('ancillary bundle binding mismatch')
 with bundle.open('rb') as f:header=f.read(10)
 if len(header)!=10 or header[:2]!=b'\x1f\x8b' or int.from_bytes(header[4:8],'little')!=0:raise SystemExit('gzip header is not deterministic')
 seen=set()
 with tarfile.open(bundle,'r:gz') as tar:
  for m in tar:
   safe(m.name)
   if m.name in seen or m.name not in arc:raise SystemExit(f'unexpected/duplicate archive member: {m.name}')
   seen.add(m.name);row=arc[m.name]
   if not m.isfile() or m.issym() or m.islnk() or any((m.uid,m.gid,m.mtime)) or m.uname or m.gname:raise SystemExit(f'unsafe/non-deterministic archive member: {m.name}')
   if m.mode!=int(row['mode'],8) or m.size!=int(row['bytes']):raise SystemExit(f'archive metadata mismatch: {m.name}')
   f=tar.extractfile(m)
   if f is None or dstream(f)!=row['sha256']:raise SystemExit(f'archive content mismatch: {m.name}')
 if seen!=set(arc):raise SystemExit(f'missing archive members: {len(set(arc)-seen)}')
 verify_root(root);print(f'verified {len(arc)} archive entries and {len(loose)} loose artifacts')
if __name__=='__main__':main()
