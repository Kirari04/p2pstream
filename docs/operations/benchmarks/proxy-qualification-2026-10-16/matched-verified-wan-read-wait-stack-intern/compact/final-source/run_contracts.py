#!/usr/bin/env python3
from __future__ import annotations
import argparse, hashlib, json, os, shlex, shutil, struct, subprocess, sys
from pathlib import Path

ROOT=Path('/home/user/.codex/worktrees/astra-proxy-performance/p2pstream')
BASE=ROOT/'tmp/continuation-20261016/wan-read-wait-stack-intern-source'
CAP=ROOT/'tmp/continuation-20261016/wan-read-wait-capacity-source'
ZERO=ROOT/'tmp/continuation-20261016/wan-read-wait-zero-pc-analyzer-source'
sys.path.insert(0,str(BASE))
import stack_intern_format as fmt
import analyze_interned_trace as analyzer_module
import intern_parsed_trace as intern_module

CASES=[]
for protocol,run,baseline in (
 ('h1','tiny-read-wait-capacity-smoke-3/h1','smoke3-h1-zero-pc-offline-reduction-1'),
 ('h2','tiny-read-wait-capacity-smoke3-h2-only-1','smoke3-h2-only-zero-pc-offline-reduction-1')):
 for seq in (9,11,13):
  arm=CAP/'runs'/run
  CASES.append(dict(name=f'{protocol}-sequence-{seq}',protocol=protocol,sequence=seq,
   raw=arm/f'results.read-wait-traces/runtime-trace-sequence-{seq}.out',
   parsed=arm/f'read-wait-reductions/sequence-{seq}.parsed.txt',
   baseline=ZERO/'runs'/baseline/f'sequence-{seq}.reduction.json'))

def sha(path):
 h=hashlib.sha256()
 with path.open('rb') as f:
  for b in iter(lambda:f.read(1<<20),b''):h.update(b)
 return h.hexdigest()
def fresh_write(path,data):
 fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
 with os.fdopen(fd,'wb') as f:f.write(data);f.flush();os.fsync(f.fileno())
def invoke(name,argv,cwd,out):
 (out/f'{name}.invocation.txt').write_text(shlex.join(map(str,argv))+'\n')
 p=subprocess.run(list(map(str,argv)),cwd=cwd,stdin=subprocess.DEVNULL,capture_output=True)
 fresh_write(out/f'{name}.stdout',p.stdout);fresh_write(out/f'{name}.stderr',p.stderr)
 fresh_write(out/f'{name}.status',f'{p.returncode}\n'.encode())
 return p.returncode

def expanded_identity(path):
 h=hashlib.sha256();n=0;sync_count=0;wall_sync_count=0;bad_sync=[]
 reader=fmt.CanonicalReader(path)
 for line in reader.iter_lines():
  h.update(line);n+=len(line)
  if b' Sync ' in line and line.startswith(b'M=-1 P=-1 G=-1 Sync '):
   sync_count+=1
   if b' Wall=' in line:
    wall_sync_count+=1
    if not line.rstrip(b'\n').endswith(b'+02:00'):bad_sync.append(line.decode('utf-8','replace'))
 assert reader.summary is not None
 return n,h.hexdigest(),reader.summary,sync_count,wall_sync_count,bad_sync

def require_invalid(path,needle):
 try:list(fmt.CanonicalReader(path).iter_lines())
 except fmt.InvalidCanonicalTrace as e:
  if needle not in str(e):raise AssertionError(f'{needle!r} not in {e!r}')
  return str(e)
 raise AssertionError('malformed canonical stream accepted')

def rec(tag,payload):return fmt.RECORD_HEADER.pack(tag,len(payload))+payload

def main():
 ap=argparse.ArgumentParser();ap.add_argument('--output',required=True,type=Path)
 ap.add_argument('--verified-six-root',type=Path);a=ap.parse_args()
 out=a.output.resolve(); assert out.is_dir() and not any(out.iterdir());os.chmod(out,0o700)
 verified_six=None if a.verified_six_root is None else a.verified_six_root.resolve(strict=True)
 if verified_six is not None:assert verified_six.is_dir()
 results=[]
 for case in CASES:
  d=(out/case['name']) if verified_six is None else (verified_six/case['name'])
  if verified_six is None:d.mkdir(mode=0o700)
  canon=d/'trace.intern';ledger=d/'trace.ledger.json';ps=d/'parser.status';cs=d/'canonicalizer.status'
  if verified_six is None:
   argv=['/usr/bin/python3.14',BASE/'run_interned_trace_parser.py','--raw-trace',case['raw'],
    '--working-directory',d,'--canonical-output',canon,'--ledger',ledger,
    '--parser-status',ps,'--canonicalizer-status',cs,'--parser-stderr',d/'parser.stderr',
    '--canonicalizer-stderr',d/'canonicalizer.stderr','--process-record',d/'process.json']
   ast=invoke('adapter',argv,ROOT,d);assert ast==0
  else:
   assert (d/'adapter.status').read_bytes()==b'0\n'
  proc=json.loads((d/'process.json').read_text());assert proc['accepted'] is True
  for k in ('parser','canonicalizer'):
   assert proc[k]['status']==0 and proc[k]['joined'] is True and proc[k]['group_gone'] is True
   assert proc[k]['term_attempted'] is False and proc[k]['kill_attempted'] is False
  n,h,meta,sync_count,wall_sync_count,bad_sync=expanded_identity(canon)
  assert sync_count>0 and wall_sync_count>0 and bad_sync==[]
  assert n==case['parsed'].stat().st_size and h==sha(case['parsed'])
  reduction=d/'reduction.json'
  if verified_six is None:
   argv=['/usr/bin/python3.14',BASE/'analyze_interned_trace.py','--input',canon,
    '--tool-status',ps,'--canonicalizer-status',cs,'--ledger',ledger,'--output',reduction]
   rs=invoke('analyzer',argv,ROOT,d);assert rs==0
  else:
   assert (d/'analyzer.status').read_bytes()==b'0\n'
  actual=json.loads(reduction.read_text());baseline=json.loads(case['baseline'].read_text())
  assert actual['accepted'] is True and actual['errors']==[]
  assert actual['markers']==baseline['markers'] and actual['reduction']==baseline['reduction']
  results.append(dict(name=case['name'],raw_bytes=case['raw'].stat().st_size,
   parsed_bytes=n,canonical_bytes=canon.stat().st_size,definition_count=meta['definition_count'],
   definition_bytes=meta['definition_bytes'],record_count=meta['record_count'],
   expanded_sha256=h,canonical_sha256=sha(canon),sync_count=sync_count,
   sync_wall_count=wall_sync_count,
   sync_timezone='Europe/Zurich +02:00',reduction_equal=True,
   reused_verified_run=verified_six is not None))

 cdir=out/'focused-contracts';cdir.mkdir(mode=0o700)
 fixture=(ZERO/'fixtures/valid-resource-goid.txt').read_bytes()
 empty=fixture+b'M=1 P=0 G=99 Metric Time=220 Name="background"\nStack=\n\n'
 missing=(ZERO/'fixtures/invalid-missing-wait-stack.txt').read_bytes()
 for name,data,expect_analyzer in (('empty-background',empty,0),('missing-positive-wait',missing,1)):
  d=cdir/name;d.mkdir(mode=0o700);src=d/'input.txt';fresh_write(src,data)
  canon=d/'trace.intern';ledger=d/'trace.ledger.json'
  st=invoke('canonicalizer',['/usr/bin/python3.14',BASE/'intern_parsed_trace.py','--input',src,
   '--output',canon,'--ledger',ledger],ROOT,d);assert st==0
  n,h,_,_,_,_=expanded_identity(canon);assert n==len(data) and h==hashlib.sha256(data).hexdigest()
  fresh_write(d/'parser.status',b'0\n')
  st=invoke('analyzer',['/usr/bin/python3.14',BASE/'analyze_interned_trace.py','--input',canon,
   '--tool-status',d/'parser.status','--canonicalizer-status',d/'canonicalizer.status',
   '--ledger',ledger,'--output',d/'reduction.json'],ROOT,d)
  assert st==expect_analyzer
  if expect_analyzer:assert 'positive waiting interval has no TransitionStack' in json.loads((d/'reduction.json').read_text())['errors']

 # Exact status gates using the accepted empty-stack artifact.
 ed=cdir/'empty-background'
 for label,arg in (('parser-nonzero','--tool-status'),('canonicalizer-nonzero','--canonicalizer-status')):
  d=cdir/label;d.mkdir(mode=0o700);fresh_write(d/'bad.status',b'1\n')
  argv=['/usr/bin/python3.14',BASE/'analyze_interned_trace.py','--input',ed/'trace.intern',
   '--tool-status',ed/'parser.status','--canonicalizer-status',ed/'canonicalizer.status',
   '--ledger',ed/'trace.ledger.json','--output',d/'reduction.json']
  argv[argv.index(arg)+1]=d/'bad.status'
  assert invoke('analyzer',argv,ROOT,d)==1

 # Direct decoder malformed records; each target error occurs before footer validation.
 md=cdir/'malformed';md.mkdir(mode=0o700)
 header=b'M=1 P=0 G=1 Metric Time=1 Value=1\nStack=\n';body=b'\tfn @ 0x1\n\t\t/file.go:1\n'
 digest=hashlib.sha256(body).digest();meta=fmt.STACK_META.pack(1,digest,len(body))
 bad={
  'missing-footer':fmt.MAGIC+rec(fmt.TAG_LITERAL,b'M=1 P=0 G=1 Metric Time=1 Value=1\n'),
  'dangling-reference':fmt.MAGIC+rec(fmt.TAG_LITERAL,header)+rec(fmt.TAG_REFERENCE,meta),
  'stack-without-marker':fmt.MAGIC+rec(fmt.TAG_DEFINITION,meta+body),
  'literal-replaces-stack':fmt.MAGIC+rec(fmt.TAG_LITERAL,header)+rec(fmt.TAG_LITERAL,b'\n'),
  'malformed-stack':fmt.MAGIC+rec(fmt.TAG_LITERAL,header)+rec(fmt.TAG_DEFINITION,fmt.STACK_META.pack(1,hashlib.sha256(b'bad\n').digest(),4)+b'bad\n'),
  'unknown-tag':fmt.MAGIC+rec(77,b'x'),
 }
 needles={'missing-footer':'footer is missing','dangling-reference':'dangling stack reference',
  'stack-without-marker':'lacks preceding stack marker','literal-replaces-stack':'replaces required stack record',
  'malformed-stack':'incomplete frame pair','unknown-tag':'unknown canonical record tag'}
 invalid=[]
 for name,data in bad.items():
  p=md/f'{name}.intern';fresh_write(p,data);invalid.append({'name':name,'error':require_invalid(p,needles[name])})
 # Bounded-state contracts use reduced test-only constants and restore them.
 sample=(out if verified_six is None else verified_six)/'h1-sequence-9/trace.intern'
 for name,attribute,value,needle in (
  ('logical-expanded-bound','MAX_EXPANDED_BYTES',10,'logical expanded text exceeds'),
  ('definition-count-bound','MAX_STACK_DEFINITIONS',0,'definition count exceeds'),
  ('definition-bytes-bound','MAX_DEFINITION_BYTES',1,'dictionary exceeds')):
  original=getattr(fmt,attribute);setattr(fmt,attribute,value)
  try:invalid.append({'name':name,'error':require_invalid(sample,needle)})
  finally:setattr(fmt,attribute,original)
 p=md/'physical-record-bound.intern'
 fresh_write(p,fmt.MAGIC+fmt.RECORD_HEADER.pack(fmt.TAG_LITERAL,fmt.MAX_CANONICAL_BYTES+1))
 invalid.append({'name':'physical-record-bound','error':require_invalid(p,'record length exceeds')})

 # Deterministic status mutation between analyzer passes must fail closed.
 d=cdir/'status-mutated-between-passes';d.mkdir(mode=0o700)
 six_status_root=out if verified_six is None else verified_six
 shutil.copyfile(six_status_root/'h1-sequence-9/parser.status',d/'parser.status')
 shutil.copyfile(six_status_root/'h1-sequence-9/canonicalizer.status',d/'canonicalizer.status')
 original_reduce=analyzer_module.reduce_states
 def mutate_status(path,markers):
  value=original_reduce(path,markers);(d/'parser.status').write_bytes(b'1\n');return value
 analyzer_module.reduce_states=mutate_status
 old_argv=sys.argv[:]
 try:
  sys.argv=['analyze_interned_trace.py','--input',str(sample),'--tool-status',str(d/'parser.status'),
   '--canonicalizer-status',str(d/'canonicalizer.status'),'--ledger',str(six_status_root/'h1-sequence-9/trace.ledger.json'),
   '--output',str(d/'reduction.json')]
  assert analyzer_module.main()==1
 finally:
  analyzer_module.reduce_states=original_reduce;sys.argv=old_argv
 assert 'status changed during streaming passes' in json.loads((d/'reduction.json').read_text())['errors'][0]

 # Producer collision detection compares exact bytes even when digest repeats.
 d=cdir/'forced-digest-collision';d.mkdir(mode=0o700)
 collision_input=(b'M=1 P=0 G=1 Metric Time=1 V=1\nStack=\n\tfn1 @ 0x1\n\t\t/a.go:1\n\n'
  b'M=1 P=0 G=1 Metric Time=2 V=2\nStack=\n\tfn2 @ 0x2\n\t\t/b.go:2\n\n')
 fresh_write(d/'input.txt',collision_input)
 real_sha256=intern_module.hashlib.sha256
 class FixedDigest:
  def __init__(self,data=b''):self.data=bytearray(data)
  def update(self,data):self.data.extend(data)
  def digest(self):return b'X'*32
  def hexdigest(self):return '58'*32
 intern_module.hashlib.sha256=FixedDigest
 try:
  try:intern_module.canonicalize(str(d/'input.txt'),d/'trace.intern',d/'trace.ledger.json')
  except intern_module.InternError as error:assert 'collision' in str(error)
  else:raise AssertionError('forced unequal stack collision accepted')
 finally:intern_module.hashlib.sha256=real_sha256
 assert not (d/'trace.intern').exists() and not (d/'trace.ledger.json').exists()
 # Producer error cleanup: unterminated line owns no partial output/ledger.
 d=cdir/'producer-cleanup';d.mkdir(mode=0o700);fresh_write(d/'bad.txt',b'unterminated')
 st=invoke('canonicalizer',['/usr/bin/python3.14',BASE/'intern_parsed_trace.py','--input',d/'bad.txt',
  '--output',d/'trace.intern','--ledger',d/'trace.ledger.json'],ROOT,d)
 assert st==1 and not (d/'trace.intern').exists() and not (d/'trace.ledger.json').exists()
 summary={'accepted':True,'six':results,'focused_invalid':invalid,'case_count':len(results),
  'contract_count':len(invalid)+7,'coverage_note':'bounded format/status/collision/cleanup plus six natural adapter joins; signal TERM/KILL injection remains a later dedicated adapter contract'}
 fresh_write(out/'summary.json',(json.dumps(summary,indent=2,sort_keys=True)+'\n').encode())
 return 0
if __name__=='__main__':raise SystemExit(main())
