#!/usr/bin/env python3
"""Run the root-owned, frozen focused checks once with complete bounded logs."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys

ROOT = Path(__file__).resolve().parent
PLAN = ROOT / 'PLAN.json'

def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def write(path, value):
    with path.open('x') as f:
        json.dump(value, f, indent=2, sort_keys=True)
        f.write('\n')
        f.flush()
        os.fsync(f.fileno())

def identity(plan):
    repo = Path(plan['worktree'])
    names = subprocess.check_output(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z', '--', *plan['source_roots']], cwd=repo).decode().split('\0')
    files = {n: digest(repo / n) for n in names if n and (repo / n).is_file()}
    if files != plan['source_files']:
        raise ValueError('source file set or bytes changed')
    for name, expected in plan['pinned_inputs'].items():
        if digest(Path(name)) != expected:
            raise ValueError('pinned tool changed: ' + name)
    return len(files)

def main():
    if sys.argv[1:] != ['--execute-once']:
        raise SystemExit('expected --execute-once')
    plan = json.loads(PLAN.read_text())
    if digest(Path(__file__)) != plan['executor_sha256']:
        raise ValueError('executor changed')
    count = identity(plan)
    out = ROOT / 'run-once'
    out.mkdir()
    spec = importlib.util.spec_from_file_location('owned_check_supervisor', plan['supervisor'])
    supervisor = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(supervisor)
    supervisor.BUDGET = plan['combined_artifact_limit_bytes']
    def artifacts():
        total = 0
        for root in plan['artifact_roots']:
            for path in Path(root).rglob('*'):
                try:
                    if path.is_file():
                        total += path.stat().st_size
                except FileNotFoundError:
                    pass
        return total
    supervisor.artifact_bytes = artifacts
    def stopped(signum, frame):
        raise RuntimeError('check launcher interrupted: ' + str(signum))
    signal.signal(signal.SIGTERM, stopped)
    signal.signal(signal.SIGINT, stopped)
    env = {k: v for k, v in os.environ.items() if not k.startswith('P2PSTREAM_QUALIFICATION_')}
    env.update(plan['environment'])
    os.chdir(plan['worktree'])
    results, failure = [], None
    try:
        for command in plan['commands']:
            identity(plan)
            prefix = out / command['name']
            status = supervisor.execute_owned(command['argv'], env, prefix, command['outer_deadline_seconds'], plan['log_limit_bytes'])
            audit = json.loads(prefix.with_suffix('.outer.json').read_text())
            identity(plan)
            events = [json.loads(line) for line in prefix.with_suffix('.stdout.log').read_text().splitlines() if line.startswith('{')]
            passes = [e['Test'] for e in events if e.get('Action') == 'pass' and e.get('Test')]
            failures = [e.get('Test', '<package>') for e in events if e.get('Action') == 'fail']
            skipped = [e['Test'] for e in events if e.get('Action') == 'skip' and e.get('Test')]
            result = {'name': command['name'], 'status': status, 'passes': passes, 'failures': failures, 'skips': skipped, 'source_unchanged': True, 'audit_sha256': digest(prefix.with_suffix('.outer.json'))}
            results.append(result)
            if status != 0 or failures or audit['forced'] or not audit['accepted_cleanup'] or audit['remaining_owned'] or audit['adopted_descendants']:
                raise RuntimeError('check failed: ' + command['name'])
            if not set(plan['required_tests']).issubset(passes) or set(plan['required_tests']) & set(skipped):
                raise RuntimeError('required tests did not all pass: ' + command['name'])
    except BaseException as error:
        failure = type(error).__name__ + ': ' + str(error)
    unchanged = False
    try:
        identity(plan)
        unchanged = True
    except BaseException as error:
        failure = failure or str(error)
    accepted = failure is None and len(results) == len(plan['commands']) and unchanged
    result = {'accepted': accepted, 'failure': failure, 'commands': results, 'source_unchanged': unchanged, 'source_file_count': count, 'plan_sha256': digest(PLAN), 'artifact_bytes': artifacts(), 'complete_logs_retained': True, 'threshold_semantics': 'polled rejection; overshoot possible'}
    write(out / 'RESULT.json', result)
    print(json.dumps({'accepted': accepted, 'failure': failure, 'commands': [{k: (len(v) if isinstance(v, list) else v) for k, v in r.items()} for r in results]}))
    if not accepted:
        raise SystemExit(1)

if __name__ == '__main__':
    main()
