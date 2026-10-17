#!/usr/bin/env python3
"""Approved-command driver: compile two endpoints once; never execute either."""
from pathlib import Path
import hashlib,json,os,signal,subprocess,sys,time
ROOT=Path(__file__).resolve().parents[1]
def sha(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def groups(pgid):
    result=[]
    for p in Path('/proc').iterdir():
        if p.name.isdigit():
            try:
                if int((p/'stat').read_text().rsplit(')',1)[1].split()[2])==pgid:result.append(int(p.name))
            except (FileNotFoundError,ProcessLookupError,PermissionError):pass
    return result
def audit(plan):
    for row in plan['inputs']:
        if sha(row['path'])!=row['sha256']:raise ValueError('changed pinned input: '+row['path'])
    expected=set()
    for line in Path(plan['build_inventory']).read_text().splitlines():
        digest,name=line.split('  ',1);expected.add(name)
        if sha(ROOT/name)!=digest:raise ValueError('changed build source: '+name)
    actual={str(p.relative_to(ROOT)) for p in (ROOT/'test-source').rglob('*') if p.is_file()}
    if actual!=expected:raise ValueError('build source inventory cardinality changed')
    if sum(p.stat().st_size for p in ROOT.rglob('*') if p.is_file())>plan['artifact_budget_bytes']:raise ValueError('task artifact budget exceeded')
def main():
    plan_path=ROOT/'build-runner-review/COMMANDS.json';plan=json.loads(plan_path.read_text())
    audit(plan)
    output=ROOT/'build';output.mkdir(exist_ok=False)
    receipt={'schema_version':1,'diagnostic':'prospective-terminal-attribution-build','accepted':False,'endpoint_execution':False,'wan_traffic':False,
             'production_reference':plan['production_reference'],'source_ref':plan['source_ref'],'plan_sha256':sha(plan_path),'build_inventory_sha256':sha(plan['build_inventory']),
             'tested_inventory_sha256':sha(ROOT/'successor/TEST-SOURCE-SHA256SUMS'),'local_result_sha256':sha(ROOT/'local-window-2/RESULT.json'),'executions':[],'binaries':{}}
    failed=False
    try:
        for command in plan['commands']:
            audit(plan);name=command['name'];row={'name':name,'argv':command['argv'],'cwd':command['cwd'],'started_unix':time.time(),'forced':False}
            stdout=output/(name+'.stdout.log');stderr=output/(name+'.stderr.log')
            with stdout.open('xb') as so,stderr.open('xb') as se:
                proc=subprocess.Popen(command['argv'],cwd=command['cwd'],stdout=so,stderr=se,start_new_session=True);row['pid']=proc.pid;deadline=time.monotonic()+command['outer_seconds']
                while proc.poll() is None:
                    if time.monotonic()>deadline or stdout.stat().st_size+stderr.stat().st_size>command['max_output_bytes']:
                        row['forced']=True;os.killpg(proc.pid,signal.SIGTERM)
                        try:proc.wait(timeout=5)
                        except subprocess.TimeoutExpired:os.killpg(proc.pid,signal.SIGKILL);proc.wait()
                        break
                    time.sleep(.1)
                row['exit_status']=proc.wait()
            row.update(finished_unix=time.time(),remaining_group_members=groups(proc.pid),stdout_sha256=sha(stdout),stderr_sha256=sha(stderr),output_bytes=stdout.stat().st_size+stderr.stat().st_size)
            if row['remaining_group_members']:
                row['forced']=True;os.killpg(proc.pid,signal.SIGKILL)
                cleanup_deadline=time.monotonic()+5
                while groups(proc.pid) and time.monotonic()<cleanup_deadline:time.sleep(.1)
                row['remaining_after_cleanup']=groups(proc.pid)
            receipt['executions'].append(row)
            if row['exit_status']!=0 or row['forced'] or row['remaining_group_members'] or row['output_bytes']>command['max_output_bytes']:raise ValueError(name+' failed its bounded natural-exit contract')
            if name=='go-env':
                settings=json.loads(stdout.read_text());receipt['resolved_settings']=settings
                if any(settings.get(k)!=v for k,v in plan['expected_settings'].items()):raise ValueError('resolved build settings differ')
            audit(plan)
        for role in ['agent','server']:
            p=ROOT/'build'/('current-'+role+'-terminal.test')
            if not p.is_file() or p.is_symlink() or not os.access(p,os.X_OK):raise ValueError('built endpoint missing or not executable')
            receipt['binaries'][role]={'path':str(p),'sha256':sha(p),'size':p.stat().st_size,'go_version_m_sha256':sha(output/(role+'-metadata.stdout.log'))}
        receipt['source_before_after_unchanged']=True;receipt['accepted']=True
    except Exception as error:
        receipt['failure']=str(error);failed=True
    finally:
        (output/'receipt.json').write_text(json.dumps(receipt,indent=2)+'\n')
    raise SystemExit(1 if failed else 0)
if __name__=='__main__':main()
