import pathlib,json,hashlib,shutil,re,collections,gzip,datetime
root=pathlib.Path.cwd();base=root/'tmp/recorder-allocation-next-20261017';screen=base/'screen-attempt-1';clean=base/'integration-clean-validation-1';out=base/'publishable-evidence';out.mkdir(exist_ok=True)
h=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
provenance=[]
def copy(src,dest):
 p=out/dest;p.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(src,p);provenance.append({'export':dest,'original':str(src.relative_to(root)),'sha256':h(p),'bytes':p.stat().st_size})
def zipped(src,dest):
 p=out/dest;p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes(gzip.compress(src.read_bytes(),mtime=0));provenance.append({'export':dest,'original':str(src.relative_to(root)),'uncompressed_sha256':h(src),'sha256':h(p),'bytes':p.stat().st_size})
def filtered_json(src,dest):
 p=out/dest;p.parent.mkdir(parents=True,exist_ok=True);rows=[]
 for line in src.read_text().splitlines():
  try:r=json.loads(line)
  except ValueError:continue
  if r.get('Action') and r['Action'] not in ('output','build-output'):
   rows.append({k:r[k] for k in ('Action','Package','Test','Elapsed') if k in r})
 p.write_text(''.join(json.dumps(r,separators=(',',':'))+'\n' for r in rows));provenance.append({'export':dest,'original':str(src.relative_to(root)),'original_sha256':h(src),'original_bytes':src.stat().st_size,'sha256':h(p),'bytes':p.stat().st_size,'transform':'Go JSON non-output actions; only Action/Package/Test/Elapsed; full original retained locally'})
def inventory_summary(src,dest):
 data=json.loads(src.read_text());p=out/dest;p.parent.mkdir(parents=True,exist_ok=True);p.write_text(json.dumps({'original':str(src.relative_to(root)),'sha256':h(src),'bytes':src.stat().st_size,'record_count':len(data) if isinstance(data,list) else sum(v.get('file_count',0) for v in data.values()),'per_arm_file_counts':{k:v.get('file_count') for k,v in data.items()} if isinstance(data,dict) else None,'full_inventory_retained_locally':True},indent=2)+'\n');provenance.append({'export':dest,'original':str(src.relative_to(root)),'original_sha256':h(src),'sha256':h(p),'bytes':p.stat().st_size,'transform':'inventory binding only'})
def correctness_receipt(src,dest):
 text=src.read_text();p=out/dest;p.parent.mkdir(parents=True,exist_ok=True);lines=[l for l in text.splitlines() if re.match(r'^(?:\s*--- (?:PASS|FAIL|SKIP): |=== (?:RUN|PAUSE|CONT) |PASS$|FAIL$)',l)];p.write_text('\n'.join(lines)+'\n');provenance.append({'export':dest,'original':str(src.relative_to(root)),'original_sha256':h(src),'original_bytes':src.stat().st_size,'sha256':h(p),'bytes':p.stat().st_size,'transform':'test action/result lines only; full original retained locally'})
def testjson(path):
 counts={'top_level':collections.Counter(),'subtests':collections.Counter(),'packages':collections.Counter()};fail=[]
 for line in path.read_text().splitlines():
  try:r=json.loads(line)
  except ValueError:continue
  a=r.get('Action');name=r.get('Test')
  if a in ('pass','fail','skip'):
   kind=('subtests' if '/' in name else 'top_level') if name else 'packages';counts[kind][a]+=1
   if a=='fail':fail.append({k:r.get(k) for k in ['Package','Test','Elapsed']})
 return {'counts':{k:dict(v) for k,v in counts.items()},'failures':fail,'raw_sha256':h(path)}
for name in ['SCREEN.md','candidate.patch','PARAMETER-MAP.md','parameter-map.json','source-citations.txt','evidence.json','manifest.json','SHA256SUMS','REPORT.md']:
 copy(base/name,'proposal/'+name)
for name in ['frozen-inputs.json','frozen-source-manifest.json','correctness-test-names.json','preparation-commands.json','prepare-and-test.py','recorder_nullable_id_screen_test.go','measure-fixed.py','measurement-commands.json','measurement-ledger.jsonl','postprocess-stateful.py','postprocessor-inputs.json','postrun-input-audit.json','analysis-invocation.json','analysis-status.json']:
 
 if name=='frozen-source-manifest.json':inventory_summary(screen/name,'screen/'+name+'.binding.json')
 else:copy(screen/name,'screen/'+name+('.txt' if name.endswith('.go') else ''))
for p in sorted((screen/'logs').iterdir()):
 if p.name in ['baseline-correctness.log','candidate-correctness.log','candidate-correctness-race.log']:correctness_receipt(p,'screen/logs/'+p.stem+'.results.txt')
 else:copy(p,'screen/logs/'+p.name)
for p in sorted((screen/'postprocessing-reviewed').iterdir()):copy(p,'screen/postprocessing-reviewed/'+p.name)
for name in ['input-manifest.json','source-files.json','retained-two-file.patch','run-checks.py','toolchain.json','commands.json','summary.json','check-runner.log','setup-preflight-failure.json']:
 
 if name=='source-files.json':inventory_summary(clean/name,'integration-clean/'+name+'.binding.json')
 else:copy(clean/name,'integration-clean/'+name)
for name in ['normal-root-suite.log','focused-recorder-race.log','recorder-vet.log']:
 filtered_json(clean/name,'integration-clean/'+name+'.results.jsonl')
invalid=base/'integration-validation-1'
for p in sorted(invalid.iterdir()):
 if p.is_file():
  if p.name.endswith('.log'):filtered_json(p,'integration-invalid-scope/'+p.name+'.results.jsonl')
  else:copy(p,'integration-invalid-scope/'+p.name)
counts={name:testjson(clean/(name+'.log')) for name in ['normal-root-suite','focused-recorder-race']};counts['invalid_first_normal']=testjson(invalid/'normal-root-suite.log')
initial={}
for name in ['baseline-correctness','candidate-correctness','candidate-correctness-race']:
 text=(screen/'logs'/(name+'.log')).read_text();passes=re.findall(r'^--- PASS: (\S+)',text,re.M);subs=re.findall(r'^\s+--- PASS: (\S+)',text,re.M)
 initial[name]={'top_level_pass':len(passes),'subtest_pass':len(subs),'top_level_names':passes,'exit_status':0,'raw_sha256':h(screen/'logs'/(name+'.log'))}
counts['isolated_screen_correctness']=initial
(out/'test-counts.json').write_text(json.dumps(counts,indent=2)+'\n')
result=json.loads((screen/'postprocessing-reviewed/results.json').read_text());manifest=json.loads((clean/'input-manifest.json').read_text());summary=json.loads((clean/'summary.json').read_text());assert result['accepted'] and summary['accepted'];assert not summary['source_changed']
runtime=h(root/'internal/db/proxy_request_event_inserter.go');test=h(root/'internal/server/observability_prepared_insert_test.go');assert runtime=='8a267bc1ee80ac408d089fbde27db37af756ed6585a221016d1d2d22d3b3bcd2';assert test=='d8fa5fba12361152287710f7f3797174637e8660f90c95ffb09ccd739c0b8fc3'
identity={'screen_baseline':'27c8e31f3826ab5326d941b20644c1f56159d99d','screen_candidate_patch_sha256':h(base/'candidate.patch'),'measured_candidate_runtime_sha256':runtime,'retained_runtime_sha256':runtime,'permanent_test_sha256':test,'permanent_test_added_lines':81,'permanent_test_is_separate_from_timed_overlay':True,'clean_integration':manifest,'measurement_collector_exit_status':1,'collector_failure_phase':'analysis after all 20 timed invocations exited0','reviewed_postprocessor_exit_status':0,'timed_invocations':20,'samples':200,'extra_timed_samples_or_retries':0,'all_ten_fixed_cases_accepted':True,'no_app_throughput_or_parity_claim':True,'excluded':['binaries','source/archive copies','runtime databases','environment credentials']}
(out/'source-identity.json').write_text(json.dumps(identity,indent=2)+'\n')
lines=['# Recorder nullable-ID allocation screen and retained implementation','','The fixed screen passed all ten predeclared cases. The retained helper is byte-identical to the measured candidate. The eight nullable IDs convert to nil/int64 before interface boxing; SQL ordinals, batch sizes, schedules, transactions, cancellation, rollups and cache touches remain unchanged.','','`proposal/REPORT.md` and `proposal/SCREEN.md` are frozen pre-execution documents; their preparatory wording is historical. This index records the outcome.','','## Fixed measurement outcome','','Exactly ten balanced pairs (AB/BA alternating), 300 ms per case, CPU0/GOMAXPROCS1, Go1.26.6; 512 events/op, unchanged 16-row SQL groups. Every one of the 20 timed invocations exited0. All 200 samples remain bound to raw logs and line hashes. Criteria were >=64 B/event and >=4 allocations/event saved, and candidate/baseline median ns/op <=1.03 for every case.','','| Case | Baseline median ns/op | Candidate median ns/op | Ratio | B saved/event | Allocs saved/event |','|---|---:|---:|---:|---:|---:|']
for name,r in result['summary'].items():
 label=name.replace('BenchmarkPreparedProxyRequestEventBatchFlush/','full flush/').replace('BenchmarkRecorderNullableIDFixedBatchInsert/','insert/')
 lines.append(f"| {label} | {r['medians']['baseline']['ns/op']:.1f} | {r['medians']['candidate']['ns/op']:.1f} | {r['candidate_baseline_ns_ratio']:.6f} | {r['saved_bytes_per_event']:.6f} | {r['saved_allocs_per_event']:.0f} |")
lines += ['','The results establish this bounded allocation screen only. They establish no whole-application throughput improvement, HTTP/WAN result or native/HAProxy/Caddy parity.','','## Preserved failures and corrections','','The frozen collector exited1 after all timings completed because old flush benchmark JSON logs separated the benchmark header from its numeric line. Its source, failure traceback, raw records and ledger are retained unchanged. A separately reviewed stateful postprocessor ran once, accepted the same 200 samples, and enforced exact known case/log sets, one metric record per header, 512 events/op, raw line/hash binding, and the original criteria. No timed command was repeated. An earlier setup path failure before compilation is also retained.','','The first integration invocation ran in the evidence-filled worktree and discovered ignored tmp source fragments: 128 package failures, all under p2pstream/tmp, no outside-tmp failures. It is invalid as integration evidence. The serial runner had already entered race compilation when the scope correction arrived; the owned runner and compiler were terminated, and vet never launched. Its partial race output and interruption receipt are retained. A clean tracked-source archive was then used; git apply initially skipped ignored-path inputs, caught by the sourcehash check before any test, so the two exact approved files were explicitly copied and rehashed.','','## Correctness and source binding','','The isolated screen correctness set passed 11 top-level tests in each of baseline, candidate, and candidate race, with persisted single/batched23-field coverage, invalid payload→NULL, generated-SQL edge/error equivalence, rollups/touches, FK rollback and cancellation. The permanent regression adds81lines to existing fixtures; it compares generated SQL database outcomes for each of8 IDs across NULL, invalid nonzero, valid zero, negative and signed extremes, using single inserts and16-row batches. It does not duplicate the helper as an oracle.','','Clean integration uses tracked HEAD '+manifest['head']+' plus the exact approved two-file patch. Normal `go test ./...`, focused recorder race and relevant vet all exited0 with no source changes. Test counts (top-level and subtests separated) are in `test-counts.json`; filtered Go JSON results omit application Output; original raw log hashes/bytes bind full local logs. No failed check was treated as a pass.','','Source and executable identity are in `source-identity.json`, `screen/frozen-inputs.json`, and file inventories. The runtime helper hash is `'+runtime+'`; the permanent test hash is `'+test+'`. The screen overlay is separate from the permanent test and remains frozen.','','## Artifact layout','','- `screen/logs/pair-*.log`: all20 raw timing logs, byte-for-byte.','- `screen/postprocessing-reviewed/all-200-samples.json`: every sample with original raw line/hash provenance.','- `screen/postprocessing-reviewed/results.json`: medians and unchanged acceptance decisions.','- `screen/`: frozen collector, reviewed correction, overlays, commands, correctness and identity receipts. Source/overlay Go bytes use .go.txt destinations; full inventories remain local and are bound by count/hash/size summaries.','- `integration-clean/`: clean source inventory, exact composite patch, commands, filtered correctness results and outcomes, with original raw hash/size bindings.','- `integration-invalid-scope/`: invalid first invocation, partial race compile and cancellation receipts.','- `proposal/`: frozen source proof and screen design.','','All original failures and raw records remain in their original ignored evidence directories. This export excludes binaries, source archives, source tree copies, full application logs and environment credentials. The 20 required raw benchmark logs contain only locally generated SQLite fixtures and temporary paths; no remote service credentials were supplied to these commands. Integration logs omit all Output content; focused screen correctness logs export only test action/result lines. `provenance.json` binds exported files to their originals; `SHA256SUMS` binds every exported artifact.']
(out/'README.md').write_text('\n'.join(lines)+'\n');(out/'provenance.json').write_text(json.dumps(provenance,indent=2)+'\n')
rows=[{'path':str(p.relative_to(out)),'sha256':h(p),'bytes':p.stat().st_size} for p in sorted(out.rglob('*')) if p.is_file() and p not in (out/'SHA256SUMS',out/'artifact-index.json')];(out/'artifact-index.json').write_text(json.dumps(rows,indent=2)+'\n');(out/'SHA256SUMS').write_text(''.join(h(p)+'  '+str(p.relative_to(out))+'\n' for p in sorted(out.rglob('*')) if p.is_file() and p != out/'SHA256SUMS'))
print(json.dumps({'export':str(out),'files':len(rows)+2,'bytes':sum(p.stat().st_size for p in out.rglob('*') if p.is_file()),'sha256sums_sha256':h(out/'SHA256SUMS'),'counts':counts},indent=2))
