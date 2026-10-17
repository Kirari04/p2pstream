#!/usr/bin/env python3
"""Read-only post-run audit; records rejection without reclassifying evidence."""
from pathlib import Path
import hashlib, json, os, subprocess, time
ROOT=Path(__file__).resolve().parents[1]
OUT=ROOT/'vector-design'
RUN=ROOT/'runner-source/integration/runs/controller-crc-vector-once'
ARM=RUN/'arm-0-cubic'
def load(p): return json.loads(p.read_text())
def sha(p): return hashlib.sha256(p.read_bytes()).hexdigest()
changed=[]
for name in ['FROZEN-INPUTS.json','vector-design/SOURCE.json']:
 data=load(ROOT/name)
 for row in data if isinstance(data,list) else data['files']:
  p=Path(row['path'])
  if not p.is_file() or sha(p)!=row['sha256']: changed.append(str(p))
for row in load(ROOT/'BUILD-TEST-PLAN.json')['inputs']:
 p=Path(row['path'])
 if not p.is_file() or sha(p)!=row['sha256']: changed.append(str(p))
ledgers=['SHA256SUMS','build/runtime-SHA256SUMS','build/environment-recovery/RUNTIME-SHA256SUMS','build/joined-ps/FINAL-SHA256SUMS','vector-design/SHA256SUMS']
for name in ledgers:
 for line in (ROOT/name).read_text().splitlines():
  digest,relative=line.split('  ',1);p=ROOT/relative
  if not p.is_file() or sha(p)!=digest: changed.append(str(p))
for row in load(ROOT/'build/receipt.json')['binaries'].values():
 p=Path(row['path'])
 if sha(p)!=row['sha256'] or p.stat().st_size!=row['size']: changed.append(str(p))
outer=[dict(path=str(p),**load(p)) for p in sorted(RUN.glob('*.outer.json'))]
identities={(v['pid'],v['start_ticks']) for a in outer for v in a['owned_identities']}
namespaces={k:set() for k in ['user','mnt','net','pid']}
for doc in [load(ARM/'outer-namespaces.json')['owned'],load(ARM/'server-scope/runner-owned-namespaces.json')['namespaces']]:
 for k in namespaces: namespaces[k].add(doc[k])
lifecycle=[json.loads(s) for s in (ARM/'results.lifecycle.jsonl').read_text().splitlines()]
for v in lifecycle:
 if v.get('event')=='namespace-helpers-start':
  for k in ['agent','router']: namespaces['net'].add(v[k]['namespace'])
remaining=[];namespace_remaining=[];unreadable=0
for p in Path('/proc').iterdir():
 if not p.name.isdigit(): continue
 try:
  s=(p/'stat').read_text();fields=s[s.rfind(')')+2:].split()
  if (int(p.name),int(fields[19])) in identities: remaining.append({'pid':int(p.name),'start_ticks':int(fields[19])})
 except (OSError,ValueError,IndexError): pass
 for k,values in namespaces.items():
  try:
   value=os.readlink(p/'ns'/k)
   if value in values: namespace_remaining.append({'pid':int(p.name),'kind':k,'namespace':value})
  except OSError: unreadable+=1
invocation=dict(s.split('=',1) for s in (ARM/'server-scope-invocation.txt').read_text().splitlines())
cgroup=Path('/sys/fs/cgroup')/invocation['scope_cgroup'].lstrip('/')
unit=subprocess.run(['systemctl','--user','show',invocation['unit'],'--property=LoadState,ActiveState,SubState'],capture_output=True,text=True,timeout=10)
netns=subprocess.run(['ip','netns','list'],capture_output=True,text=True,check=True,timeout=10)
rows=[json.loads(s) for s in (ARM/'results.jsonl').read_text().splitlines()]
att=load(ARM/'attestation.json');independent=load(ARM/'independent-controller-attestation.json')
exits=[v for v in lifecycle if v.get('event')=='process-exit']
audit={'schema_version':1,'status':'rejected structural conditioning; 16 first-control transfers; no Reno or final CUBIC','finished_unix':time.time(),'screen_final':load(RUN/'screen-final.json'),'source_sha256':sha(OUT/'SOURCE.json'),'plan_sha256':sha(OUT/'PLAN.json'),'runtime_grant_sha256':sha(OUT/'RUNTIME-GRANT.json'),'build_receipt_sha256':sha(ROOT/'build/receipt.json'),'input_changes':changed,'prior_ledgers':[{'path':n,'sha256':sha(ROOT/n)} for n in ledgers],'pre_post_hashes_equal':load(ARM/'pre-hashes.json')==load(ARM/'post-hashes.json'),'independent_attestation_exact_match':att==independent,'attestation':{k:att.get(k) for k in ['accepted','integrity_issues','conditioning_issues','performance_issues']},'completed_transfer_rows':len(rows),'unlaunched_arms':[1,2],'all16_metrics':[{'sequence':v['diagnostic_sequence'],'direction':v['transfer_direction'],'rate':v['useful_transfer_bytes_per_second'],'wall_seconds':v['wall_seconds'],'response_bytes':v['response_bytes'],'errors':v['errors'],'status_codes':v['status_codes'],'persisted_event_delta':v['persisted_event_delta']} for v in rows],'retained_copies':{str(v['diagnostic_sequence']):v['response_copy_attribution'] for v in rows if v['diagnostic_sequence'] in [9,11,14,15]},'natural_endpoint_and_helper_exits':exits,'mature_supervisor_lifecycle':load(ARM/'supervisor-lifecycle.json'),'outer_commands':[{k:v.get(k) for k in ['path','status','forced','accepted_cleanup','remaining_owned','adopted_descendants','failure_reason','elapsed_seconds']} for v in outer],'remaining_pid_start_identities':remaining,'private_namespace_ids':{k:sorted(v) for k,v in namespaces.items()},'remaining_private_namespace_processes':namespace_remaining,'unavailable_proc_namespace_reads':unreadable,'scope_cgroup_exists':cgroup.exists(),'scope_unit_query':{'status':unit.returncode,'stdout':unit.stdout,'stderr':unit.stderr},'named_netns_after':netns.stdout.splitlines(),'tracked_status':subprocess.run(['git','status','--porcelain','--untracked-files=no'],capture_output=True,text=True,check=True).stdout,'no_additional_test_build_or_traffic':True}
assert not changed and audit['pre_post_hashes_equal'] and att==independent
assert len(rows)==16 and len(exits)==5 and all(v['status']==0 and v['forced'] is False and v['group_joined'] is True for v in exits)
assert not remaining and not namespace_remaining and not cgroup.exists() and not netns.stdout.strip()
assert all(v['forced'] is False and not v['remaining_owned'] and not v['adopted_descendants'] and v['accepted_cleanup'] is True for v in outer)
with (OUT/'FINAL-AUDIT.json').open('x') as f: json.dump(audit,f,indent=2,sort_keys=True);f.write('\n')
files=sorted(p for tree in [OUT,RUN] for p in tree.rglob('*') if p.is_file() and p!=OUT/'FINAL-SHA256SUMS')
with (OUT/'FINAL-SHA256SUMS').open('x') as f:
 for p in files: f.write(sha(p)+'  '+str(p.relative_to(ROOT))+'\n')
print(json.dumps({'audit_sha256':sha(OUT/'FINAL-AUDIT.json'),'ledger_sha256':sha(OUT/'FINAL-SHA256SUMS'),'files':len(files),'all_inputs_unchanged':not changed,'no_live_remnants':not remaining and not namespace_remaining,'completed_rows':len(rows),'outcome':audit['status']}))
