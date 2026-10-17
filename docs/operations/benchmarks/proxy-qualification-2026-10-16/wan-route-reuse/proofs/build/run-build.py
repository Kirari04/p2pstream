#!/usr/bin/env python3
"""One root-approved bounded ordinary endpoint build; no endpoints are run."""
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('route_reuse_build_proof', ROOT/'build-proof.py')
CHECK = importlib.util.module_from_spec(spec)
spec.loader.exec_module(CHECK)

def fresh(path, value):
    with path.open('x') as handle:
        json.dump(value, handle, indent=2, sort_keys=True); handle.write('\n')
        handle.flush(); os.fsync(handle.fileno())

def bytes_under(roots):
    total = 0
    for raw in roots:
        for path in Path(raw).rglob('*'):
            try:
                if path.is_file(): total += path.stat().st_size
            except FileNotFoundError:
                pass
    return total

def main():
    if sys.argv[1:] != ['--execute-approved-once']:
        raise SystemExit('requires exact separate root build grant')
    plan = CHECK.read(ROOT/'PLAN.json')
    grant = CHECK.read(ROOT/'ROOT-BUILD-GRANT.json')
    CHECK.require(grant == {'approved':True, 'plan_sha256':CHECK.sha(ROOT/'PLAN.json'), 'executor_sha256':CHECK.sha(__file__)}, 'build grant differs')
    CHECK.require(CHECK.sha(__file__) == plan['executor_sha256'], 'build executor changed')
    identity = CHECK.prerequisites(plan)
    output = ROOT/'build-once'; output.mkdir()
    Path(plan['binary_directory']).mkdir() # no overwrite/retry
    supervisor = CHECK.module(plan['supervisor'], 'accepted_route_reuse_build_supervisor')
    supervisor.BUDGET = plan['combined_artifact_limit_bytes']
    supervisor.artifact_bytes = lambda: bytes_under(plan['artifact_roots'])
    env = {k:v for k,v in os.environ.items() if not k.startswith('P2PSTREAM_QUALIFICATION_')}
    env.update(plan['environment'])
    os.chdir(plan['source'])
    def interrupted(signum, frame): raise RuntimeError('build interrupted: '+str(signum))
    signal.signal(signal.SIGTERM, interrupted); signal.signal(signal.SIGINT, interrupted)
    rows, failure = [], None
    try:
        for command in plan['commands']:
            CHECK.prerequisites(plan)
            CHECK.require(bytes_under([ROOT]) <= plan['proof_artifact_limit_bytes'], 'proof artifact threshold exceeded')
            prefix = Path(command['log_prefix'])
            status = supervisor.execute_owned(command['argv'], env, prefix, command['outer_deadline_seconds'], plan['per_stream_limit_bytes'])
            CHECK.require(status == 0, 'build command failed: '+command['name'])
            files = CHECK.natural(plan, prefix, command['argv'], plan['per_stream_limit_bytes'])
            unchanged = CHECK.source(plan) == identity
            rows.append({'name':command['name'], 'status':status, 'source_unchanged':unchanged, 'files':files})
            CHECK.require(unchanged, 'compiled source changed')
    except BaseException as error:
        failure = type(error).__name__+': '+str(error)
    unchanged = False
    try: unchanged = CHECK.prerequisites(plan) == identity
    except BaseException as error: failure = failure or type(error).__name__+': '+str(error)
    receipt = {'schema_version':1, 'purpose':'ordinary-route-reuse-qualification-build', 'accepted':False, 'failure':failure,
               'source_before_after_unchanged':unchanged, 'source_identity':identity, 'source_ref':plan['source_ref'],
               'plan_sha256':CHECK.sha(ROOT/'PLAN.json'), 'focused_result':{'path':plan['focused_result'], 'sha256':plan['focused_result_sha256']},
               'endpoint_execution':False, 'wan_traffic':False, 'commands':rows, 'binaries':{},
               'artifact_bytes':supervisor.artifact_bytes(), 'proof_artifact_bytes':bytes_under([ROOT]),
               'byte_threshold_semantics':'polled rejection; overshoot possible; complete logs retained; any observed overflow rejects'}
    if failure is None and unchanged and len(rows) == len(plan['commands']):
        for role, raw in plan['binaries'].items():
            path = Path(raw)
            metadata = Path(next(c['log_prefix'] for c in plan['commands'] if c['name'] == role+'-metadata')).with_suffix('.stdout.log')
            receipt['binaries'][role] = {'path':str(path), 'sha256':CHECK.sha(path), 'size':path.stat().st_size, 'go_version_m_sha256':CHECK.sha(metadata)}
        receipt['accepted'] = True
        try: CHECK.verify_receipt(plan, receipt)
        except BaseException as error:
            receipt['accepted'] = False; receipt['failure'] = type(error).__name__+': '+str(error)
    fresh(output/'receipt.json', receipt)
    print(json.dumps({'accepted':receipt['accepted'], 'commands':len(rows), 'failure':receipt['failure']}))
    if not receipt['accepted']: raise SystemExit(1)

if __name__ == '__main__': main()
