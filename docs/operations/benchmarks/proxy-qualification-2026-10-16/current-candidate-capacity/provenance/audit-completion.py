import datetime, hashlib, json, pathlib, subprocess
repo=pathlib.Path.cwd();qual=repo/'tmp/final-qualification/27c8e31f3826ab5326d941b20644c1f56159d99d';out=qual/'capacity-independent-attempt-1'
read=lambda p:json.loads(p.read_text());sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
summary=read(out/'summary.json');failures=[]
expected={'combined-4g-server-plus-colocated-agent':3,'external-one-agent-separate-4g-cgroups':4,'external-four-agents-separate-4g-cgroups':7,'compat-current-agent-old-server-1-lane':4,'compat-current-agent-old-server-4-lane':4}
if summary.get('accepted') is not True or len(summary.get('topologies',{}))!=3 or len(summary.get('compatibility_bursts',{}))!=2:failures.append('stock summary not accepted')
if read(out/'status.json').get('exit_status')!=0:failures.append('orchestrator did not exit0')
receipts={};pids=set();rows={}
for name,count in expected.items():
 raw=out/'raw'/name
 if read(raw/'status.json')!={'exit_status':0,'emergency_cleanup':False}:failures.append(name+': runner exit/emergency contract failed')
 entries=[]
 for path in sorted(raw.glob('*.stop.exit.json')):
  r=read(path);pids.add(r['pid']);entries.append({'path':str(path.relative_to(out)),'sha256':sha(path),'receipt':r})
  if r.get('forced_cleanup') is not False or type(r.get('shell_exit_status')) is not int or r['shell_exit_status']!=0 or r.get('natural_exit_verified') is not True:failures.append(name+': unnatural exit:'+path.name)
  if r.get('container'):
   c=r.get('container_state',{})
   if c.get('Running') is not False or type(c.get('ExitCode')) is not int or c['ExitCode']!=0 or c.get('OOMKilled') is not False:failures.append(name+': invalid container receipt')
 if len(entries)!=count:failures.append(name+': unexpected receipt count')
 receipts[name]=entries
 for ready in raw.glob('*.ready.json'):
  pid=read(ready).get('pid')
  if type(pid) is int and pid>1:pids.add(pid)
 data=[json.loads(x) for x in (out/'results'/f'{name}.jsonl').read_text().splitlines() if x.strip()]
 rows[name]=[{key:r.get(key) for key in ['label','scenario','attempted','useful_completed','errors','status_classes','origin_peak_inflight','owner_peak_reservations','persisted_event_delta','dropped_event_delta','event_queue_after_drain','event_drain_error']} for r in data if r['label'].startswith(('candidate-','current-agent-'))]
 for row in data:
  for process in [row.get('server_process',{}),*row.get('agent_processes',[])]:
   pid=process.get('pid') if isinstance(process,dict) else None
   if type(pid) is int and pid>1:pids.add(pid)
post=read(out/'provenance/postrun-input-rehash-manifest.json')
if post.get('accepted') is not True:failures.append('postrun inputs/source/archive/evidence changed')
remaining=[]
for pid in sorted(pids):
 path=pathlib.Path('/proc')/str(pid)
 if path.exists():remaining.append({'pid':pid,'cmdline':path.joinpath('cmdline').read_bytes().replace(b'\0',b' ').decode(errors='replace')})
archive_bin_prefix=str(qual/'capacity-attempt-1/builds').encode()
archive_processes=[]
for proc in pathlib.Path('/proc').iterdir():
 if not proc.name.isdigit():continue
 try:args=proc.joinpath('cmdline').read_bytes().split(b'\0')
 except (PermissionError,FileNotFoundError,ProcessLookupError):continue
 if any(arg.startswith(archive_bin_prefix) and arg.endswith(b'.test') for arg in args):archive_processes.append(int(proc.name))
containers=subprocess.check_output(['docker','ps','-a','--filter','name=p2pstream-qualification','--format','{{json .}}'],text=True)
if remaining or archive_processes or containers.strip():failures.append('owned runtime remains')
report={'accepted':not failures,'failures':failures,'recorded_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'source_ref':'27c8e31f3826ab5326d941b20644c1f56159d99d','scope':'independent absolute candidate capacity and original mixed-version compatibility only','clears_legacy_performance_comparison':False,'clears_wan':False,'stock_summary_sha256':sha(out/'summary.json'),'input_rehash_manifest_sha256':sha(out/'provenance/input-rehash-manifest.json'),'postrun_input_rehash_manifest_sha256':sha(out/'provenance/postrun-input-rehash-manifest.json'),'stock_validator_sha256':post['validator_sha256'],'natural_receipt_count':sum(map(len,receipts.values())),'receipt_counts':{name:len(items) for name,items in receipts.items()},'receipts':receipts,'candidate_and_compatibility_rows':rows,'original_evidence_preserved':post['preserved_attempts'],'remaining_recorded_processes':remaining,'remaining_archive_binary_processes':archive_processes,'remaining_containers':[json.loads(x) for x in containers.splitlines() if x.strip()]}
(out/'completion-audit-manifest.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps({key:report[key] for key in ['accepted','failures','natural_receipt_count','receipt_counts','original_evidence_preserved','remaining_recorded_processes','remaining_archive_binary_processes','remaining_containers']},indent=2))
if failures:raise SystemExit(1)
