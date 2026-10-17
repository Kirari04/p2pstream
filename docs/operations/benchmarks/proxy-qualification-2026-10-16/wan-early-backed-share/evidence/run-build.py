#!/usr/bin/env python3
"""One bounded offline build of the common off/on qualification binary pair."""
from pathlib import Path
import importlib.util
import json
import os
import signal
import sys
ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('early_share_build_proof', ROOT/'build-proof.py')
CHECK = importlib.util.module_from_spec(spec)
spec.loader.exec_module(CHECK)

def fresh(path, value):
    with path.open('x') as handle:
        json.dump(value, handle, indent=2, sort_keys=True)
        handle.write('\n')
        handle.flush()
        os.fsync(handle.fileno())

def source_bytes():
    return sum(p.stat().st_size for p in ROOT.rglob('*') if p.is_file())

def main():
    if sys.argv[1:] != ['--execute-approved-once']:
        raise SystemExit('requires separately reviewed one-shot build grant')
    plan = CHECK.read(ROOT/'BUILD-PLAN.json')
    grant = CHECK.read(ROOT/'BUILD-GRANT.json')
    CHECK.require(grant == {'approved':True, 'plan_sha256':CHECK.sha(ROOT/'BUILD-PLAN.json'), 'executor_sha256':CHECK.sha(__file__)}, 'build grant differs')
    CHECK.require(CHECK.sha(__file__) == plan['executor_sha256'], 'build executor changed')
    identity = CHECK.prerequisites(plan)
    output = ROOT/'build-once'
    output.mkdir()
    binary_dir = Path(plan['binary_directory'])
    binary_dir.mkdir()  # Never overwrite an earlier binary pair.
    supervisor = CHECK.module(plan['supervisor'], 'accepted_early_share_build_supervisor')
    supervisor.ROOT = Path(plan['artifact_inventory_root'])
    supervisor.BUDGET = plan['artifact_rejection_threshold_bytes']
    environment = os.environ.copy()
    environment.update(plan['environment'])
    environment.pop('P2PSTREAM_QUALIFICATION_EARLY_SHARE', None)
    environment.pop('P2PSTREAM_QUALIFICATION_EARLY_SHARE_PROOF_DIR', None)
    os.chdir(plan['source'])
    def interrupted(signum, frame):
        raise RuntimeError('build interrupted by signal '+str(signum))
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    rows, failure = [], None
    try:
        for command in plan['commands']:
            CHECK.prerequisites(plan)
            CHECK.require(source_bytes() <= plan['source_artifact_rejection_threshold_bytes'], 'source artifact bound exceeded')
            prefix = Path(command['log_prefix'])
            status = supervisor.execute_owned(command['argv'], environment, prefix, command['outer_deadline_seconds'], plan['per_stream_rejection_threshold_bytes'])
            CHECK.require(status == 0, 'build command failed: '+command['name'])
            files = CHECK.natural(prefix, command['argv'], plan['per_stream_rejection_threshold_bytes'])
            unchanged = CHECK.source(plan) == identity
            rows.append({'name':command['name'], 'status':status, 'source_unchanged':unchanged, 'files':files})
            CHECK.require(unchanged, 'compiled input changed')
        CHECK.require(CHECK.read(Path(plan['commands'][0]['log_prefix']).with_suffix('.stdout.log')) == plan['expected_settings'], 'resolved tool settings differ')
    except BaseException as error:
        failure = type(error).__name__+': '+str(error)
    unchanged = False
    try:
        unchanged = CHECK.prerequisites(plan) == identity
    except BaseException as error:
        failure = failure or type(error).__name__+': '+str(error)
    receipt = {'schema_version':1, 'diagnostic':'early-backed-share-build', 'accepted':False, 'failure':failure,
               'source_before_after_unchanged':unchanged, 'source_identity':identity, 'source_ref':plan['source_ref'],
               'plan_sha256':CHECK.sha(ROOT/'BUILD-PLAN.json'), 'baseline':plan['baseline'],
               'focused_result':{'path':plan['focused_result'], 'sha256':plan['focused_result_sha256']},
               'endpoint_execution':False, 'wan_traffic':False, 'commands':rows, 'binaries':{},
               'artifact_bytes':supervisor.artifact_bytes(), 'source_artifact_bytes':source_bytes(),
               'byte_threshold_semantics':'polled rejection; overshoot possible; complete logs retained; any observed overflow rejects'}
    if failure is None and unchanged and len(rows) == len(plan['commands']):
        for role, raw in plan['binaries'].items():
            path = Path(raw)
            metadata = Path(next(c['log_prefix'] for c in plan['commands'] if c['name']==role+'-metadata')).with_suffix('.stdout.log')
            receipt['binaries'][role] = {'path':str(path), 'sha256':CHECK.sha(path), 'size':path.stat().st_size, 'go_version_m_sha256':CHECK.sha(metadata)}
        receipt['accepted'] = True
        try:
            CHECK.verify_receipt(plan, receipt)
        except BaseException as error:
            receipt['accepted'] = False
            receipt['failure'] = type(error).__name__+': '+str(error)
    fresh(output/'receipt.json', receipt)
    print(json.dumps({'accepted':receipt['accepted'], 'commands':len(rows), 'failure':receipt['failure']}))
    if not receipt['accepted']:
        raise SystemExit(1)
if __name__ == '__main__':
    main()
