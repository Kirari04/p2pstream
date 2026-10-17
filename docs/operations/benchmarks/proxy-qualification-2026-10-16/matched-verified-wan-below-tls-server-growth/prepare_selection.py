#!/usr/bin/env python3
from __future__ import annotations
import csv,hashlib,json,os
from pathlib import Path,PurePosixPath
HERE=Path(__file__).resolve().parent
REPO=Path('/home/user/.codex/worktrees/astra-proxy-performance/p2pstream')
SRC=REPO/'tmp/continuation-20261016/wan-below-tls-raw-write-source'
OLD=REPO/'tmp/continuation-20261016/wan-below-tls-evidence-export/EXPLICIT-ALLOWLIST.plan.txt'
selected=set(x for x in OLD.read_text().splitlines() if x)
def addtree(rel):
 root=SRC/rel
 if not root.exists(): raise SystemExit(f'missing {rel}')
 for p in sorted(root.rglob('*')):
  if not p.is_symlink() and p.is_file(): selected.add(p.relative_to(SRC).as_posix())
for rel in ['implementation-source/server-growth/review','implementation-source/server-growth/runs','implementation-source/combined-build/preflight','implementation-source/combined-build/provenance','implementation-source/combined-build/review','implementation-source/raw-occupancy-connect-isolation/review','implementation-source/raw-occupancy-equivalence/review','implementation-source/raw-occupancy-equivalence/runs','implementation-source/raw-occupancy-ordinary-proof/review','implementation-source/raw-validator-invocation-source/review','provenance']: addtree(rel)
build=SRC/'implementation-source/combined-build/runs/audited-build-attempt-1'
for p in sorted(build.rglob('*')):
 if p.is_symlink() or not p.is_file(): continue
 rel=p.relative_to(SRC).as_posix(); low=rel.lower()
 if '/bin/' in low or '/gocache' in low or '/gopath' in low or '/repo' in low or '/audited-build-attempt-1/source/' in low or p.suffix in {'.test','.a'}: continue
 selected.add(rel)
runs=SRC/'implementation-source/final-integration-source/integration/runs'
for name in ['tiny-below-tls-smoke-1','tiny-below-tls-smoke-2','tiny-below-tls-smoke-3','sustained-below-tls-1','sustained-below-tls-2']:
 root=runs/name
 for p in sorted(root.rglob('*')):
  if p.is_symlink() or not p.is_file(): continue
  rel=p.relative_to(SRC).as_posix(); low=rel.lower(); parts=p.relative_to(root).parts
  if low.endswith('.key.pem') or '.ready.json' in low: continue
  if any(part.startswith('proxy-agent-growth.') for part in parts): continue
  if '/tmp/' in low or '/state/' in low or '/runtime/' in low or p.name=='server-app-ready.json': continue
  selected.add(rel)
def forbidden(rel):
 p=PurePosixPath(rel); low=rel.lower()
 if low.endswith(('.key.pem','.db','.sqlite','.sqlite3','.pyc','.test','.a')): return True
 if any(x in low for x in ['/__pycache__/','/gocache','/gopath','/build/bin/','/audited-build-attempt-1/bin/','/audited-build-attempt-1/source/','/baseline/source/','/candidate/source/']): return True
 if p.name in {'source.tar','source-8ac4c8d.tar'}: return True
 return False
selected={x for x in selected if not forbidden(x) and (SRC/x).is_file() and not (SRC/x).is_symlink()}
secret_values=[]
for p in runs.rglob('*'):
 if p.is_symlink() or not p.is_file(): continue
 low=p.name.lower()
 if low.endswith('.key.pem'):
  b=p.read_bytes()
  if b: secret_values.append((f'private-key:{p.relative_to(SRC)}',b))
 elif low.endswith('.ready.json'):
  try: obj=json.loads(p.read_text())
  except Exception: continue
  def walk(v,key=''):
   if isinstance(v,dict):
    for k,x in v.items(): walk(x,str(k).lower())
   elif isinstance(v,list):
    for x in v: walk(x,key)
   elif isinstance(v,str) and len(v)>=12 and any(t in key for t in ('token','secret','password','bearer','auth')): secret_values.append((f'ready-value:{p.relative_to(SRC)}:{key}',v.encode()))
  walk(obj)
hits=[]
for rel in sorted(selected):
 data=(SRC/rel).read_bytes(); labels=[label for label,value in secret_values if value and value in data]
 if labels: hits.append({'artifact':rel,'recovered_label_count':len(labels)})
for row in hits: selected.discard(row['artifact'])
report={'schema_version':1,'accepted':not hits,'candidate_files':len(selected),'recovered_value_labels':len(secret_values),'genuine_hit_artifacts':hits,'values_printed':0,'limitations':['Process-local trace bearer values that were never persisted cannot be recovered for exact comparison.','The scan checks exact recoverable private-key bytes and authentication-like values from excluded closed-run readiness JSON; it does not prove absence of every possible secret representation.']}
(HERE/'SECRET-SCAN.json').write_text(json.dumps(report,indent=2,sort_keys=True)+'\n')
loose_markers=('sustained-below-tls-2/final-status.json','sustained-below-tls-2/h1/results.jsonl','sustained-below-tls-2/h2/results.jsonl','sustained-below-tls-2/h1/results.summary.json','sustained-below-tls-2/h2/results.summary.json','sustained-below-tls-2/h1/attestation.json','sustained-below-tls-2/h2/attestation.json','sustained-below-tls-2/h1/results.lifecycle.jsonl','sustained-below-tls-2/h2/results.lifecycle.jsonl','sustained-below-tls-2/h1/supervisor-lifecycle.json','sustained-below-tls-2/h2/supervisor-lifecycle.json','sustained-below-tls-2/h1/raw-post-join-supervisor-validation.json','sustained-below-tls-2/h2/raw-post-join-supervisor-validation.json','sustained-below-tls-2/tls/public.test.crt.pem','sustained-below-tls-1-failure-freeze-1/','sustained-below-tls-2-result-freeze-1/','sustained2-h1-measurement-review-1/','sustained2-measurement-review-1/','sustained2-credit-publication-architecture-review-1/','file-backed-capacity-contract-attempt-1/records/RESULT.json','tiny-below-tls-smoke-3/final-status.json','tiny-below-tls-smoke-3/h1/attestation.json','tiny-below-tls-smoke-3/h2/attestation.json')
def category(rel):
 if '/integration/runs/' in rel:return 'run-evidence'
 if '/runs/' in rel:return 'offline-result'
 if '/review/' in rel:return 'source-review'
 if '/provenance/' in rel or rel.startswith('provenance/'):return 'provenance'
 return 'source-history'
def placement(rel):return 'loose' if any(m in rel for m in loose_markers) else 'archive'
def digest(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
 return h.hexdigest()
rows=[]
for rel in sorted(selected):
 p=SRC/rel; mode='0755' if (p.stat().st_mode&0o111) else '0644'; dest='evidence/'+rel
 rows.append({'placement':placement(rel),'sha256':digest(p),'bytes':str(p.stat().st_size),'mode':mode,'category':category(rel),'destination':dest,'source':rel})
with (HERE/'ALLOWLIST.tsv').open('w',newline='') as f:
 w=csv.DictWriter(f,fieldnames=('sha256','category','destination','source'),delimiter='\t',lineterminator='\n');w.writeheader();w.writerows({k:r[k] for k in w.fieldnames} for r in rows)
with (HERE/'PACKAGE-MAP.tsv').open('w',newline='') as f:
 w=csv.DictWriter(f,fieldnames=('placement','sha256','bytes','mode','category','destination','source'),delimiter='\t',lineterminator='\n');w.writeheader();w.writerows(rows)
summary={'schema_version':1,'files':len(rows),'bytes':sum(int(r['bytes']) for r in rows),'archive_files':sum(r['placement']=='archive' for r in rows),'archive_bytes':sum(int(r['bytes']) for r in rows if r['placement']=='archive'),'loose_files':sum(r['placement']=='loose' for r in rows),'loose_bytes':sum(int(r['bytes']) for r in rows if r['placement']=='loose'),'secret_scan_accepted':not hits,'secret_scan_hit_artifacts':len(hits)}
(HERE/'SELECTION-SUMMARY.json').write_text(json.dumps(summary,indent=2,sort_keys=True)+'\n');print(json.dumps(summary,sort_keys=True))
