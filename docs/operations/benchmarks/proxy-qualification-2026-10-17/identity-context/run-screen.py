#!/usr/bin/env python3
"""Finite offline screen. Run only after root grants the serialized CPU slot."""
import hashlib,importlib.util,json,os,pathlib,re,signal,statistics,sys,time
ROOT=pathlib.Path(__file__).resolve().parent
WORKTREE=ROOT.parent.parent
GO='/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go'
ENV=json.loads((ROOT/'PLAN.json').read_text())['environment']
SUPERVISOR=WORKTREE/'tmp/wan-controller-screen-20261017/run-controller-policy.py'
ARTIFACT_ROOTS=[ROOT,WORKTREE/'tmp/wan-early-share-dependencies-20261017',WORKTREE/'tmp/wan-route-reuse-checks-20261017']
REVIEWED=None
sys.dont_write_bytecode=True
owned=None
def artifact_bytes():
 total=0
 for root in ARTIFACT_ROOTS:
  for path in root.rglob('*'):
   try:
    if path.is_file():total+=path.stat().st_size
   except FileNotFoundError:pass
 return total
FROZEN=json.loads((ROOT/'frozen-inputs.json').read_text())
TEST_NAMES=[]
for filename in ['client_identity_test.go','client_identity_consumers_test.go','management_client_identity_test.go','client_identity_context_test.go']:
 TEST_NAMES.extend(re.findall(r'^func (Test\w+)\(', (ROOT/'baseline/internal/server'/filename).read_text(),re.M))
TEST_PATTERN='^('+'|'.join(TEST_NAMES)+')$'
CASES={f'{shape}/{parent}/{mode}' for shape in ['direct','invalid-peer','configured-untrusted','trusted-single','trusted-chain','conflicting-sources','four-disabled-headers'] for parent in ['background','deadline'] for mode in ['resolve','resolve-and-read']}
RX=re.compile(r'^BenchmarkResolvedIdentityContext/(\S+)\s+(\d+)\s+([\d.]+) ns/op\s+(\d+) B/op\s+(\d+) allocs/op$')
records=[]

def sha(path):return hashlib.sha256(path.read_bytes()).hexdigest()
def verify():
 if REVIEWED is None:raise RuntimeError('missing reviewed identity')
 if sha(ROOT/'REVIEWED-INPUTS.json')!=REVIEWED:raise RuntimeError('reviewed input manifest changed')
 for name,want in json.loads((ROOT/'REVIEWED-INPUTS.json').read_text()).items():
  if sha(pathlib.Path(name))!=want:raise RuntimeError('reviewed executable/plan input changed: '+name)
 for variant,files in FROZEN['files'].items():
  actual={str(f.relative_to(ROOT/variant)) for f in (ROOT/variant).rglob('*') if f.is_file()}
  if actual!=set(files):raise RuntimeError('source file set changed: '+variant)
  for name,want in files.items():
   path=ROOT/variant/name
   if path.is_symlink() or sha(path)!=want:raise RuntimeError('source changed: '+variant+'/'+name)
def run(name,argv,cwd,limit,extra=None,executable_sha=None):
 verify()
 if executable_sha is not None and sha(pathlib.Path(argv[3]))!=executable_sha:raise RuntimeError('test executable changed before '+name)
 env=os.environ.copy();env.update(ENV);env.update(extra or {})
 prefix=ROOT/'logs'/name;prefix.parent.mkdir(exist_ok=True)
 command=['/usr/bin/env','-C',str(cwd),*argv]
 print('START',name,flush=True)
 error=None;status=None
 try:
  status=owned.execute_owned(command,env,prefix,limit,16<<20)
 except BaseException as exc:error=exc
 finally:
  audit_path=prefix.with_suffix('.outer.json')
  audit=json.loads(audit_path.read_text()) if audit_path.exists() else None
  logs={}
  for stream in ['stdout','stderr']:
   log=prefix.with_suffix('.'+stream+'.log')
   if log.exists():logs[stream]={'path':str(log.relative_to(ROOT)),'sha256':sha(log),'bytes':log.stat().st_size}
  record={'name':name,'argv':command,'cwd':str(cwd),'environment':dict(ENV,**(extra or {})),'exit_code':status,'logs':logs,'outer_audit':str(audit_path.relative_to(ROOT)),'outer_sha256':sha(audit_path) if audit_path.exists() else None,'accepted_cleanup':audit.get('accepted_cleanup',False) if audit else False,'error':str(error) if error else None,'expected_executable_sha256':executable_sha}
  records.append(record);(ROOT/'commands.json').write_text(json.dumps(records,indent=2)+'\n')
 verify()
 if executable_sha is not None and sha(pathlib.Path(argv[3]))!=executable_sha:raise RuntimeError('test executable changed after '+name)
 print('END',name,'exit',status,flush=True)
 if error is not None:raise error
 if status!=0 or not record['accepted_cleanup']:raise RuntimeError('command failed or cleanup was unnatural: '+name)
 return prefix.with_suffix('.stdout.log')

def parse(log):
 result={}
 for line in log.read_text().splitlines():
  m=RX.fullmatch(line)
  if not m:continue
  name=m[1]
  if name in result:raise RuntimeError('duplicate sample: '+name)
  result[name]={'iterations':int(m[2]),'ns_per_op':float(m[3]),'bytes_per_op':int(m[4]),'allocs_per_op':int(m[5]),'raw_line':line,'raw_line_sha256':hashlib.sha256(line.encode()).hexdigest(),'log':str(log.relative_to(ROOT)),'log_sha256':sha(log)}
 if set(result)!=CASES:raise RuntimeError('sample case mismatch: '+str(set(result)^CASES))
 return result

def main():
 global owned
 verify()
 spec=importlib.util.spec_from_file_location('accepted_owned_supervisor',SUPERVISOR)
 owned=importlib.util.module_from_spec(spec);spec.loader.exec_module(owned)
 owned.ROOT=ROOT;owned.BUDGET=4<<30;owned.artifact_bytes=artifact_bytes
 (ROOT/'bin').mkdir(exist_ok=True);(ROOT/'runtime-tmp').mkdir(exist_ok=True)
 for variant in ['baseline','candidate']:
  binary=ROOT/'bin'/f'{variant}.test'
  run('build-'+variant,['taskset','-c','8-11',GO,'test','-mod=readonly','-p=2','-c','-o',str(binary),'./internal/server'],ROOT/variant,300)
  run('correctness-'+variant,['taskset','-c','8-11',str(binary),'-test.run='+TEST_PATTERN,'-test.v','-test.count=1','-test.timeout=180s'],ROOT/variant,240)
 binary=ROOT/'bin/candidate-race.test'
 run('build-candidate-race',['taskset','-c','8-11',GO,'test','-mod=readonly','-p=2','-race','-c','-o',str(binary),'./internal/server'],ROOT/'candidate',300)
 run('correctness-candidate-race',['taskset','-c','8-11',str(binary),'-test.run='+TEST_PATTERN,'-test.v','-test.count=1','-test.timeout=180s'],ROOT/'candidate',240)
 binaries={v:sha(ROOT/'bin'/f'{v}.test') for v in ['baseline','candidate']}
 observations=[]
 for pair in range(1,11):
  order=['baseline','candidate'] if pair%2 else ['candidate','baseline']
  for variant in order:
   log=run(f'pair-{pair:02d}-{variant}',['taskset','-c','8',str(ROOT/'bin'/f'{variant}.test'),'-test.run=^$','-test.bench=^BenchmarkResolvedIdentityContext$','-test.benchmem','-test.benchtime=150ms','-test.count=1','-test.timeout=90s'],ROOT/variant,120,{'GOMAXPROCS':'1'},binaries[variant])
   observations.append({'pair':pair,'variant':variant,'binary_sha256':binaries[variant],'samples':parse(log)})
   (ROOT/'all-samples.json').write_text(json.dumps(observations,indent=2)+'\n')
 by={(r['pair'],r['variant']):r['samples'] for r in observations};results={};failures=[]
 for case in sorted(CASES):
  baseline=[by[pair,'baseline'][case] for pair in range(1,11)];candidate=[by[pair,'candidate'][case] for pair in range(1,11)]
  ratio=statistics.median(s['ns_per_op'] for s in candidate)/statistics.median(s['ns_per_op'] for s in baseline)
  pairs=[{'pair':pair,'time_ratio':by[pair,'candidate'][case]['ns_per_op']/by[pair,'baseline'][case]['ns_per_op'],'bytes_saved':by[pair,'baseline'][case]['bytes_per_op']-by[pair,'candidate'][case]['bytes_per_op'],'allocations_saved':by[pair,'baseline'][case]['allocs_per_op']-by[pair,'candidate'][case]['allocs_per_op']} for pair in range(1,11)]
  passed=ratio<=1.03 and all(p['time_ratio']<=1.10 and p['bytes_saved']>=32 and p['allocations_saved']>=2 for p in pairs)
  results[case]={'baseline_median_ns_per_op':statistics.median(s['ns_per_op'] for s in baseline),'candidate_median_ns_per_op':statistics.median(s['ns_per_op'] for s in candidate),'median_time_ratio':ratio,'pairs':pairs,'passed':passed}
  if not passed:failures.append(case)
 (ROOT/'results.json').write_text(json.dumps({'retained':not failures,'failed_cases':failures,'case_count':len(CASES),'sample_count':len(CASES)*len(observations),'results':results},indent=2)+'\n')
 print('SCREEN', 'PASS' if not failures else 'REJECT', 'failed cases:',failures,flush=True)

if __name__=='__main__':
 if len(sys.argv)!=3 or sys.argv[1]!='--execute-approved-once':raise SystemExit('requires exact reviewed manifest hash and root runtime grant')
 REVIEWED=sys.argv[2]
 if not re.fullmatch('[a-f0-9]{64}',REVIEWED):raise SystemExit('invalid reviewed manifest identity')
 signal.signal(signal.SIGTERM,lambda *_: (_ for _ in ()).throw(RuntimeError('screen supervisor received TERM')))
 try:main()
 except BaseException as exc:
  (ROOT/'failure.json').write_text(json.dumps({'error':str(exc),'type':type(exc).__name__,'completed_commands':len(records)},indent=2)+'\n')
  raise
