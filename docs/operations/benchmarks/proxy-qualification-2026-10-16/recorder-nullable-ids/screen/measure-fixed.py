import datetime,hashlib,json,pathlib,re,statistics,subprocess,sys
repo=pathlib.Path.cwd();out=repo/'tmp/recorder-allocation-next-20261017/screen-attempt-1'
freeze=json.loads((out/'frozen-inputs.json').read_text())
for name,item in freeze['executables'].items():
 assert hashlib.sha256(pathlib.Path(item['path']).read_bytes()).hexdigest()==item['sha256'],name
assert hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest()==freeze['measurement_script_sha256']
expected=set(freeze['benchmark_cases']);commands=[]
ledger=out/'measurement-ledger.jsonl'
assert not ledger.exists(), 'measurement already started; no retry permitted'
with ledger.open('x') as f:f.write(json.dumps({'state':'started','at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'frozen_input_manifest_sha256':hashlib.sha256((out/'frozen-inputs.json').read_bytes()).hexdigest(),'pairs':10,'samples_per_arm_per_case':10,'benchtime':'300ms','affinity':'CPU0','GOMAXPROCS':1,'no_extra_samples_or_retries':True})+'\n')
for pair in range(10):
 for arm in (['baseline','candidate'] if pair%2==0 else ['candidate','baseline']):
  label=f'pair-{pair+1:02}-{arm}';path=out/'logs'/(label+'.log')
  argv=['taskset','-c','0','env','GOMAXPROCS=1',str(out/'bin'/(arm+'.test')),'-test.run','^$','-test.bench','^(BenchmarkPreparedProxyRequestEventBatchFlush|BenchmarkRecorderNullableIDFixedBatchInsert)$','-test.benchmem','-test.benchtime=300ms','-test.count=1','-test.cpu=1','-test.timeout=180s']
  record={'label':label,'pair':pair+1,'arm':arm,'argv':argv,'cwd':str(out/'sources'/arm),'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat()}
  with path.open('x') as log:p=subprocess.run(argv,cwd=out/'sources'/arm,stdout=log,stderr=subprocess.STDOUT)
  record['exit_status']=p.returncode;record['finished_at']=datetime.datetime.now(datetime.timezone.utc).isoformat();record['log_sha256']=hashlib.sha256(path.read_bytes()).hexdigest();commands.append(record)
  (out/'measurement-commands.json').write_text(json.dumps(commands,indent=2)+'\n')
  print(label,'exit',p.returncode,flush=True)
  with ledger.open('a') as f:f.write(json.dumps({'state':'sample_finished',**record})+'\n')
  if p.returncode:raise SystemExit(p.returncode)
results={arm:{case:[] for case in expected} for arm in ['baseline','candidate']}
for command in commands:
 path=out/'logs'/(command['label']+'.log');seen=set()
 for line in path.read_text().splitlines():
  if not line.startswith('Benchmark'):continue
  parts=line.split()
  if len(parts)<3:continue
  case=re.sub(r'-\d+$','',parts[0])
  metrics={unit:float(value) for value,unit in re.findall(r'([0-9.eE+\-]+)\s+(ns/op|B/op|allocs/op|events/op)',line)}
  if not metrics:continue
  assert case in expected,case
  assert case not in seen,case
  assert metrics.get('events/op')==512,(case,metrics)
  assert all(key in metrics for key in ['ns/op','B/op','allocs/op']), (case,metrics)
  seen.add(case);results[command['arm']][case].append({'pair':command['pair'],**metrics})
 assert seen==expected,(command['label'],sorted(expected-seen))
summary={};failures=[]
for case in sorted(expected):
 medians={}
 for arm in ['baseline','candidate']:
  rows=results[arm][case];assert len(rows)==10,(arm,case,len(rows))
  medians[arm]={key:statistics.median(row[key] for row in rows) for key in ['ns/op','B/op','allocs/op','events/op']}
 b,c=medians['baseline'],medians['candidate']
 saving_bytes=(b['B/op']-c['B/op'])/512;saving_allocs=(b['allocs/op']-c['allocs/op'])/512;ratio=c['ns/op']/b['ns/op']
 passed=saving_bytes>=64 and saving_allocs>=4 and ratio<=1.03
 summary[case]={'medians':medians,'saved_bytes_per_event':saving_bytes,'saved_allocs_per_event':saving_allocs,'candidate_baseline_ns_ratio':ratio,'accepted':passed}
 if not passed:failures.append(case)
report={'accepted':not failures,'failures':failures,'frozen_input_manifest_sha256':hashlib.sha256((out/'frozen-inputs.json').read_bytes()).hexdigest(),'criteria':freeze['retention_criteria'],'samples':results,'summary':summary}
(out/'measurement-results.json').write_text(json.dumps(report,indent=2)+'\n')
with ledger.open('a') as f:f.write(json.dumps({'state':'finished','at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'accepted':report['accepted'],'failed_cases':failures,'samples':len(commands)*len(expected)})+'\n')
print(json.dumps({'accepted':report['accepted'],'failures':failures,'summary':summary},indent=2),flush=True)
