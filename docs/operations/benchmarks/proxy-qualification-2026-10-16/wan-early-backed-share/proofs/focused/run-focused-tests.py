#!/usr/bin/env python3
"""Three frozen local tunnel commands. Requires separate root execution grant."""
from pathlib import Path
import hashlib
import importlib.util
import json
import os
import signal
import sys

ROOT = Path(__file__).resolve().parent
PLAN_PATH = ROOT / 'FOCUSED-TEST-PLAN.json'

def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def fresh(path, value):
    with path.open('x') as handle:
        json.dump(value, handle, indent=2, sort_keys=True)
        handle.write('\n')
        handle.flush()
        os.fsync(handle.fileno())

def source_identity(plan):
    inventory = Path(plan['source_inventory'])
    if not plan['source_inventory_sha256'] or digest(inventory) != plan['source_inventory_sha256']:
        raise ValueError('source inventory not frozen or differs')
    source = Path(plan['source']).resolve()
    expected = {}
    for line in inventory.read_text().splitlines():
        checksum, relative = line.split('  ', 1)
        prefix = 'test-source/'
        if not relative.startswith(prefix):
            raise ValueError('unexpected source inventory prefix')
        path = source / relative[len(prefix):]
        if path.is_symlink() or not path.resolve().is_relative_to(source) or path in expected:
            raise ValueError('aliased or repeated source input')
        expected[path] = checksum
    if any(p.is_symlink() for p in source.rglob('*')):
        raise ValueError('symlink in frozen source tree')
    actual = {p for p in source.rglob('*') if p.is_file()}
    if actual != set(expected) or not expected:
        raise ValueError('source inventory completeness differs')
    for path, checksum in expected.items():
        if digest(path) != checksum:
            raise ValueError('source input changed: ' + str(path))
    return {'inventory_sha256': digest(inventory), 'source_file_count': len(expected)}

def main():
    if sys.argv[1:] != ['--execute-approved-once']:
        raise SystemExit('requires separately approved focused-test command')
    plan = json.loads(PLAN_PATH.read_text())
    if digest(Path(__file__)) != plan['executor_sha256']:
        raise ValueError('focused executor differs')
    for raw, expected in plan['pinned_inputs'].items():
        if digest(Path(raw)) != expected:
            raise ValueError('pinned tool/supervisor/input changed: ' + raw)
    identity = source_identity(plan)
    output = ROOT / 'focused-tests-once'
    output.mkdir()  # Exactly one attempt, including failed executions.
    cache, temporary = Path(plan['environment']['GOCACHE']), Path(plan['environment']['GOTMPDIR'])
    cache.mkdir(exist_ok=True)
    temporary.mkdir(exist_ok=True)
    supervisor_path = Path(plan['supervisor'])
    spec = importlib.util.spec_from_file_location('accepted_focused_test_supervisor', supervisor_path)
    supervisor = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(supervisor)
    supervisor.ROOT = Path(plan['artifact_inventory_root'])
    supervisor.BUDGET = plan['artifact_rejection_threshold_bytes']
    def received_signal(signum, frame):
        raise RuntimeError('focused test executor received signal ' + str(signum))
    signal.signal(signal.SIGTERM, received_signal)
    signal.signal(signal.SIGINT, received_signal)
    environment = os.environ.copy()
    environment.update(plan['environment'])
    environment.pop('P2PSTREAM_QUALIFICATION_EARLY_SHARE', None)
    environment.pop('P2PSTREAM_QUALIFICATION_EARLY_SHARE_PROOF_DIR', None)
    os.chdir(plan['source'])
    commands, failure = [], None
    try:
        for command in plan['commands']:
            source_identity(plan)
            prefix = output / command['name']
            status = supervisor.execute_owned(command['argv'], environment, prefix,
                        command['outer_deadline_seconds'], plan['per_stream_rejection_threshold_bytes'])
            audit = json.loads(prefix.with_suffix('.outer.json').read_text())
            unchanged = source_identity(plan) == identity
            entry = {'name': command['name'], 'status': status, 'source_unchanged': unchanged,
                     'outer_sha256': digest(prefix.with_suffix('.outer.json')),
                     'stdout_sha256': digest(prefix.with_suffix('.stdout.log')),
                     'stderr_sha256': digest(prefix.with_suffix('.stderr.log'))}
            commands.append(entry)
            fresh(prefix.with_suffix('.proof.json'), entry)
            if status != 0 or audit['forced'] or not audit['accepted_cleanup'] or audit['remaining_owned'] or audit['adopted_descendants'] or not unchanged:
                raise RuntimeError('command failed or did not exit/join naturally: ' + command['name'])
    except BaseException as error:
        failure = type(error).__name__ + ': ' + str(error)
    unchanged = False
    try:
        unchanged = source_identity(plan) == identity
        for raw, expected in plan['pinned_inputs'].items():
            if digest(Path(raw)) != expected:
                unchanged = False
    except BaseException as error:
        failure = failure or type(error).__name__ + ': ' + str(error)
    accepted = failure is None and len(commands) == 3 and unchanged
    result = {'accepted': accepted, 'commands': commands, 'failure': failure,
              'inputs_unchanged': unchanged, 'source_identity': identity,
              'plan_sha256': digest(PLAN_PATH), 'network_dependency_lookup': False,
              'endpoint_tests': False, 'endpoint_builds': False,
              'artifact_bytes': supervisor.artifact_bytes(),
              'artifact_rejection_threshold_bytes': supervisor.BUDGET,
              'complete_logs_retained': True,
              'threshold_semantics': 'polled rejection; overshoot possible; any observed overflow rejects'}
    fresh(output / 'RESULT.json', result)
    print(json.dumps({'accepted': accepted, 'completed_commands': len(commands), 'failure': failure}))
    if not accepted:
        raise SystemExit(1)

if __name__ == '__main__':
    main()
