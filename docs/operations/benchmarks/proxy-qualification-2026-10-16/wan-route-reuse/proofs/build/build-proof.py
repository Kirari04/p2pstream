#!/usr/bin/env python3
"""Read-only binding of ordinary current-source endpoint builds and checks."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import stat
import subprocess

ROOT = Path(__file__).resolve().parent

def require(value, message):
    if not value:
        raise ValueError(message)

def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def read(path):
    path = Path(path)
    st = path.lstat()
    require(stat.S_ISREG(st.st_mode) and not path.is_symlink() and 0 < st.st_size <= 16 << 20, 'invalid bounded proof: '+str(path))
    return json.loads(path.read_text())

def module(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result

def pins(plan):
    for raw, expected in plan['pinned_inputs'].items():
        require(sha(raw) == expected, 'pinned input changed: '+raw)

def source(plan):
    focus = read(plan['focused_plan'])
    require(sha(plan['focused_plan']) == plan['focused_plan_sha256'], 'focused source plan changed')
    repo = Path(plan['source'])
    require(str(repo) == focus['worktree'], 'build/check worktree differs')
    names = subprocess.check_output(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z', '--', *focus['source_roots']], cwd=repo).decode().split('\0')
    files = {}
    for name in names:
        if not name:
            continue
        path = repo/name
        require(not path.is_symlink() and path.is_file(), 'aliased/missing source: '+name)
        files[name] = sha(path)
    require(files == focus['source_files'] and len(files) == 430, 'ordinary compiled source differs')
    identity = {'source_file_count':len(files), 'source_files_sha256':hashlib.sha256(json.dumps(files, sort_keys=True, separators=(',', ':')).encode()).hexdigest()}
    require(identity == plan['source_identity'], 'source identity differs')
    return identity

def natural(plan, prefix, argv, limit):
    # Reuse only the mature command-ownership checker, never its source schema,
    # diagnostic binaries, settings result or tagged compilation receipt.
    proof = module(plan['natural_proof_helper'], 'accepted_natural_command_proof')
    return proof.natural(prefix, argv, limit)

def prerequisites(plan):
    pins(plan)
    identity = source(plan)
    focused = read(plan['focused_result'])
    fp = read(plan['focused_plan'])
    require(sha(plan['focused_result']) == plan['focused_result_sha256'] and focused['accepted'] is True
            and focused['failure'] is None and focused['source_unchanged'] is True and focused['source_file_count'] == 430
            and focused['plan_sha256'] == plan['focused_plan_sha256'] and len(focused['commands']) == 2,
            'fresh production normal/race checks not accepted')
    for command, row in zip(fp['commands'], focused['commands']):
        require(row['name'] == command['name'] and row['status'] == 0 and row['source_unchanged'] is True
                and row['failures'] == [] and not set(fp['required_tests']) & set(row['skips'])
                and set(fp['required_tests']).issubset(row['passes']), 'focused test coverage differs')
        files = natural(plan, Path(plan['focused_result']).parent/command['name'], command['argv'], fp['log_limit_bytes'])
        require(row['audit_sha256'] == files['outer.json']['sha256'], 'focused natural proof differs')
    return identity

def verify_receipt(plan, receipt):
    identity = prerequisites(plan)
    require(receipt['schema_version'] == 1 and receipt['purpose'] == 'ordinary-route-reuse-qualification-build'
            and receipt['accepted'] is True and receipt['failure'] is None
            and receipt['plan_sha256'] == sha(ROOT/'PLAN.json') and receipt['source_ref'] == plan['source_ref']
            and receipt['source_identity'] == identity and receipt['source_before_after_unchanged'] is True
            and receipt['endpoint_execution'] is False and receipt['wan_traffic'] is False
            and receipt['focused_result'] == {'path':plan['focused_result'], 'sha256':plan['focused_result_sha256']}, 'build receipt binding differs')
    require(len(receipt['commands']) == len(plan['commands']), 'build command cardinality differs')
    for command, row in zip(plan['commands'], receipt['commands']):
        require(row['name'] == command['name'] and row['status'] == 0 and row['source_unchanged'] is True
                and row['files'] == natural(plan, command['log_prefix'], command['argv'], plan['per_stream_limit_bytes']), 'build command proof differs')
    settings = Path(plan['commands'][0]['log_prefix']).with_suffix('.stdout.log')
    require(read(settings) == plan['expected_settings'], 'fresh Go settings differ')
    for role, raw in plan['binaries'].items():
        path = Path(raw)
        require(path.is_file() and not path.is_symlink() and os.access(path, os.X_OK)
                and 0 < path.stat().st_size <= plan['per_binary_limit_bytes'], 'invalid endpoint binary')
        metadata = Path(next(c['log_prefix'] for c in plan['commands'] if c['name'] == role+'-metadata')).with_suffix('.stdout.log')
        text = metadata.read_text()
        require(text.splitlines()[0] == str(path)+': go1.26.6' and '\tbuild\t-tags=' not in text and '\tbuild\t-race=' not in text
                and '\tbuild\tCGO_ENABLED=1' in text and '\tbuild\tGOOS=linux' in text and '\tbuild\tGOARCH=amd64' in text,
                'ordinary endpoint Go metadata differs')
        require(receipt['binaries'][role] == {'path':str(path), 'sha256':sha(path), 'size':path.stat().st_size, 'go_version_m_sha256':sha(metadata)}, 'endpoint hash/metadata binding differs')
    require(receipt['artifact_bytes'] <= plan['combined_artifact_limit_bytes'] and receipt['proof_artifact_bytes'] <= plan['proof_artifact_limit_bytes'], 'build artifact threshold exceeded')
    return identity
