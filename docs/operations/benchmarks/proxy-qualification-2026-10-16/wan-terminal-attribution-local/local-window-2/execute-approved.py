from pathlib import Path
import hashlib,json,subprocess,os,signal,time,re,sys
root=Path(__file__).resolve().parents[1];out=Path(__file__).resolve().parent
manifest=root/'successor/PREDECLARED-COMMANDS-3.json'
assert hashlib.sha256(manifest.read_bytes()).hexdigest()=='c91bf02c52c9c5027683a60c7e81d9d51b867e0f2c51cc6ddbb15bb07a63f3fa'
plan=json.loads(manifest.read_text());summary={'authorized_manifest_sha256':hashlib.sha256(manifest.read_bytes()).hexdigest(),'executions':[],'stopped':False,'no_wan':True}
def source_audit():
 bad=[]
 for line in (root/'successor/TEST-SOURCE-SHA256SUMS').read_text().splitlines():
  sha,name=line.split('  ',1)
  if hashlib.sha256((root/name).read_bytes()).hexdigest()!=sha:bad.append(name)
 tool=plan['toolchain'];tool_ok=hashlib.sha256(Path(tool['path']).read_bytes()).hexdigest()==tool['sha256']
 return {'source_manifest_sha256':hashlib.sha256((root/'successor/TEST-SOURCE-SHA256SUMS').read_bytes()).hexdigest(),'changed_sources':bad,'toolchain_hash_matches':tool_ok}
def group_members(pgid):
 found=[]
 for p in Path('/proc').iterdir():
  if p.name.isdigit():
   try:
    fields=(p/'stat').read_text().rsplit(')',1)[1].split()
    if int(fields[2])==pgid:found.append(int(p.name))
   except (FileNotFoundError,ProcessLookupError,PermissionError):pass
 return found
commands=[dict(plan['resolved_build_settings_command'],name='preflight',expected_exit=0)]+plan['commands']
for command in commands:
 name=command['name'];before=source_audit();assert not before['changed_sources'] and before['toolchain_hash_matches']
 row={'name':name,'argv':command['argv'],'cwd':command['cwd'],'before':before,'started_unix':time.time()}
 (out/(name+'.invocation.json')).write_text(json.dumps(row,indent=2)+'\n')
 stdout=out/(name+'.stdout.log');stderr=out/(name+'.stderr.log');forced=False
 with stdout.open('xb') as so,stderr.open('xb') as se:
  proc=subprocess.Popen(command['argv'],cwd=command['cwd'],stdout=so,stderr=se,start_new_session=True);row['pid']=proc.pid
  deadline=time.monotonic()+(15 if name=='preflight' else 250)
  while proc.poll() is None:
   if time.monotonic()>deadline or stdout.stat().st_size+stderr.stat().st_size>command['max_output_bytes']:
    forced=True;os.killpg(proc.pid,signal.SIGTERM)
    try:proc.wait(timeout=5)
    except subprocess.TimeoutExpired:os.killpg(proc.pid,signal.SIGKILL);proc.wait()
    break
   time.sleep(.1)
  row['exit_status']=proc.wait()
 row['finished_unix']=time.time();row['forced']=forced;row['remaining_group_members']=group_members(proc.pid);row['after']=source_audit()
 row['output_bytes']=stdout.stat().st_size+stderr.stat().st_size
 text=stdout.read_text(errors='replace')+'\n'+stderr.read_text(errors='replace')
 row['go_run_count']=len(re.findall(r'^=== RUN\s',text,re.M));row['go_pass_count']=len(re.findall(r'^\s*--- PASS:',text,re.M));row['go_fail_count']=len(re.findall(r'^\s*--- FAIL:',text,re.M));row['go_skip_count']=len(re.findall(r'^\s*--- SKIP:',text,re.M))
 row['python_summary']=re.findall(r'^Ran .+ tests? in .+$',text,re.M)
 ok=row['exit_status']==0 and not forced and not row['remaining_group_members'] and not row['after']['changed_sources'] and row['after']['toolchain_hash_matches'] and row['output_bytes']<=command['max_output_bytes']
 if name=='preflight' and ok:
  settings=json.loads(stdout.read_text());row['resolved_settings']=settings;ok=all(settings.get(k)==v for k,v in command['expected'].items());row['settings_match']=ok
 row['accepted']=ok
 (out/(name+'.result.json')).write_text(json.dumps(row,indent=2)+'\n');summary['executions'].append(row)
 summary['stopped']=not ok;summary['stop_at']=name if not ok else None
 (out/'RESULT.json').write_text(json.dumps(summary,indent=2)+'\n')
 print(json.dumps({'name':name,'exit_status':row['exit_status'],'accepted':ok,'go_run_count':row['go_run_count'],'go_pass_count':row['go_pass_count'],'go_fail_count':row['go_fail_count'],'python_summary':row['python_summary'],'remaining_group_members':row['remaining_group_members']}),flush=True)
 if not ok:sys.exit(1)
summary['complete']=True;(out/'RESULT.json').write_text(json.dumps(summary,indent=2)+'\n')
