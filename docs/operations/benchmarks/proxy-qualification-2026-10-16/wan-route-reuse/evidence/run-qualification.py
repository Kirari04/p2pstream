#!/usr/bin/env python3
"""One original96 qualification, supervised without altering the owned runner."""
from pathlib import Path
import hashlib
import importlib.util
import json
import os
import signal
import sys
ROOT=Path(__file__).resolve().parent
SUP_PATH=ROOT.parent/'wan-controller-screen-20261017/run-controller-policy.py'
def sha(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def main():
    if sys.argv[1:]!=['--execute-approved-once']:raise SystemExit('separate exact original96 runtime grant required')
    source=json.loads((ROOT/'SOURCE.json').read_text());grant=json.loads((ROOT/'RUNTIME-GRANT.json').read_text())
    if grant!={'source_manifest_sha256':sha(ROOT/'SOURCE.json'),'runtime_approved':True,'maximum_transfers':96,'maximum_pairs':3}:raise ValueError('runtime grant differs')
    for row in source['files']:
        if sha(row['path'])!=row['sha256']:raise ValueError('frozen qualification input changed')
    runroot=ROOT/'runs/original96-once';execution=ROOT/'execution-once'
    if runroot.exists() or execution.exists():raise ValueError('fresh original96 output required; no retry')
    execution.mkdir()
    spec=importlib.util.spec_from_file_location('accepted_original96_supervisor',SUP_PATH);sup=importlib.util.module_from_spec(spec);spec.loader.exec_module(sup)
    sup.ROOT=ROOT;sup.BUDGET=256<<20
    argv=['bash',str(ROOT/'runner/run-owned-wan-stability.sh'),'--output-dir',str(runroot)]
    env=os.environ.copy();env['PYTHONDONTWRITEBYTECODE']='1'
    def interrupted(signum,frame):raise RuntimeError('qualification interrupted by signal '+str(signum))
    signal.signal(signal.SIGTERM,interrupted);signal.signal(signal.SIGINT,interrupted)
    status=None;failure=None;audit=None
    try:
        status=sup.execute_owned(argv,env,execution/'wrapper',13320,4<<20)
    except BaseException as error:
        failure=type(error).__name__+': '+str(error)
    unchanged=False
    try:
        unchanged=all(Path(row['path']).is_file() and not Path(row['path']).is_symlink() and sha(row['path'])==row['sha256'] for row in source['files'])
        audit_path=execution/'wrapper.outer.json'
        if audit_path.is_file():audit=json.loads(audit_path.read_text())
    except BaseException as error:
        failure=failure or type(error).__name__+': '+str(error)
    natural=(audit is not None and audit['forced'] is False and audit['accepted_cleanup'] is True
             and audit['remaining_owned']==[] and audit['adopted_descendants']==[])
    result={'schema_version':1,'wrapper_status':status,'failure':failure,'outer_natural_cleanup':natural,
            'source_before_after_unchanged':unchanged,'accepted':failure is None and type(status) is int and status==0 and natural and unchanged,
            'runtime_source_manifest_sha256':sha(ROOT/'SOURCE.json'),
            'outer_audit_sha256':sha(execution/'wrapper.outer.json') if audit is not None else None,
            'artifact_bytes':sup.artifact_bytes(),'artifact_rejection_threshold_bytes':sup.BUDGET,'original96_workload':True,'traffic_retry':False}
    if result['artifact_bytes']>sup.BUDGET-(1<<20):result['accepted']=False;result['artifact_bound_rejected']=True
    sup.write_fresh(execution/'RESULT.json',result)
    print(json.dumps(result,sort_keys=True))
    if not result['accepted']:raise SystemExit(1)
if __name__=='__main__':main()
