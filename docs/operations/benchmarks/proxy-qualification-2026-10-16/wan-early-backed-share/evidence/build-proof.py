#!/usr/bin/env python3
"""Read-only exact source, prerequisite and endpoint-build proof checks."""
from pathlib import Path
import contextlib
import hashlib
import importlib.util
import io
import json
import os
import stat
import sys
ROOT = Path(__file__).resolve().parent

def require(value, message):
    if not value:
        raise ValueError(message)

def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def read(path, limit=16 << 20):
    path = Path(path)
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and not path.is_symlink() and 0 < info.st_size <= limit, 'nonregular or unbounded proof: '+str(path))
    return json.loads(path.read_text())

def module(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result

def source(plan):
    tree, inventory = Path(plan['source']), Path(plan['inventory'])
    require(sha(inventory) == plan['inventory_sha256'], 'source inventory changed')
    expected = {}
    for line in inventory.read_text().splitlines():
        checksum, relative = line.split('  ', 1)
        prefix = tree.name+'/'
        require(relative.startswith(prefix), 'source inventory prefix differs')
        path = tree/relative[len(prefix):]
        require(path not in expected and not path.is_symlink() and path.resolve().is_relative_to(tree.resolve()), 'aliased source input')
        expected[path] = checksum
    require(len(expected) == 468 and not any(p.is_symlink() for p in tree.rglob('*')) and set(expected) == {p for p in tree.rglob('*') if p.is_file()}, 'compiled source set differs')
    for path, checksum in expected.items():
        require(sha(path) == checksum, 'compiled input changed: '+str(path))
    return {'inventory_sha256': sha(inventory), 'source_file_count': len(expected)}

def pins(plan):
    for path, checksum in plan['pinned_inputs'].items():
        require(sha(path) == checksum, 'pinned build prerequisite changed: '+path)

def natural(prefix, argv, limit):
    prefix = Path(prefix)
    outer, started = read(prefix.with_suffix('.outer.json')), read(prefix.with_suffix('.started.json'))
    require(outer['argv'] == started['argv'] == argv and type(outer['status']) is int and outer['status'] == 0 and outer['forced'] is False
            and outer['accepted_cleanup'] is True and outer['remaining_owned'] == [] and outer.get('remaining_after_cleanup', []) == []
            and outer['adopted_descendants'] == [], 'command did not exit/join naturally')
    hashes = {}
    for suffix in ['outer.json', 'started.json', 'stdout.log', 'stderr.log']:
        path = prefix.with_suffix('.'+suffix)
        require(path.is_file() and not path.is_symlink(), 'nonregular command proof')
        if suffix.endswith('.log'):
            require(path.stat().st_size <= limit and outer[suffix.split('.')[0]+'_bytes'] == path.stat().st_size, 'command log bytes differ/overflow')
        hashes[suffix] = {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}
    return hashes

def prerequisites(plan):
    pins(plan)
    identity = source(plan)
    focus = module(plan['focused_executor'], 'early_share_focused_read_checks')
    fp = read(plan['focused_plan'])
    require(sha(plan['focused_plan']) == plan['focused_plan_sha256'], 'focused plan changed')
    require(focus.source_identity(fp) == identity and focus.reused_normal(fp)['new_source_identity'] == identity, 'focused source/equivalence differs')
    result = read(plan['focused_result'])
    require(sha(plan['focused_result']) == plan['focused_result_sha256'] and result['accepted'] is True and result['inputs_unchanged'] is True
            and result['source_identity'] == identity and result['plan_sha256'] == sha(plan['focused_plan'])
            and result['normal_proof_reused'] is True and result['fresh_command_count'] == 2 and len(result['commands']) == 3, 'focused aggregate not accepted')
    require(result['commands'][0] == focus.reused_normal(fp), 'reused normal proof changed')
    for command, row in zip(fp['commands'], result['commands'][1:]):
        require(row['name'] == command['name'] and row['proof_origin'] == 'fresh-continuation-1' and row['status'] == 0 and row['source_unchanged'] is True, 'fresh focused proof differs')
        files = natural(command['log_prefix'], command['argv'], fp['per_stream_rejection_threshold_bytes'])
        for field, suffix in [('outer_sha256','outer.json'), ('stdout_sha256','stdout.log'), ('stderr_sha256','stderr.log')]:
            require(row[field] == files[suffix]['sha256'], 'focused proof hash differs')
    # Invoke the original read-only verifier with its original receipt and binaries.
    # It remains authoritative only for that historical tuple, never the new pair.
    baseline = plan['baseline']
    guard = module(baseline['verifier'], 'original_immutable_controller_build_guard')
    original_argv = sys.argv
    try:
        sys.argv = [baseline['verifier'], '--receipt', baseline['receipt'], '--expected-receipt-sha256', baseline['receipt_sha256']]
        for role in ['server', 'agent']:
            sys.argv += ['--'+role, baseline['binaries'][role], '--'+role+'-ref', baseline['source_ref']]
        with contextlib.redirect_stdout(io.StringIO()):
            guard.main()
    finally:
        sys.argv = original_argv
    return identity

def verify_receipt(plan, receipt):
    identity = prerequisites(plan)
    require(receipt['schema_version'] == 1 and receipt['diagnostic'] == 'early-backed-share-build' and receipt['accepted'] is True
            and receipt['source_before_after_unchanged'] is True and receipt['endpoint_execution'] is False and receipt['wan_traffic'] is False
            and receipt['plan_sha256'] == sha(ROOT/'BUILD-PLAN.json') and receipt['source_identity'] == identity
            and receipt['source_ref'] == plan['source_ref'] and receipt['baseline'] == plan['baseline']
            and receipt['focused_result'] == {'path':plan['focused_result'], 'sha256':plan['focused_result_sha256']}, 'build receipt scope/binding differs')
    require(len(receipt['commands']) == len(plan['commands']), 'build command cardinality differs')
    for command, row in zip(plan['commands'], receipt['commands']):
        require(row['name'] == command['name'] and row['status'] == 0 and row['source_unchanged'] is True
                and row['files'] == natural(command['log_prefix'], command['argv'], plan['per_stream_rejection_threshold_bytes']), 'build command evidence differs')
    require(read(Path(plan['commands'][0]['log_prefix']).with_suffix('.stdout.log')) == plan['expected_settings'], 'resolved Go settings differ')
    for role, raw in plan['binaries'].items():
        path = Path(raw)
        metadata = Path(next(c['log_prefix'] for c in plan['commands'] if c['name'] == role+'-metadata')).with_suffix('.stdout.log')
        require(path.is_file() and not path.is_symlink() and os.access(path, os.X_OK) and 0 < path.stat().st_size <= plan['per_binary_rejection_threshold_bytes'], 'binary missing/type/size differs')
        expected = {'path':str(path), 'sha256':sha(path), 'size':path.stat().st_size, 'go_version_m_sha256':sha(metadata)}
        require(receipt['binaries'][role] == expected and 'go1.26.6' in metadata.read_text() and '-tags=p2pstream_qualification' in metadata.read_text(), 'new binary/metadata differs')
    require(receipt['artifact_bytes'] <= plan['artifact_rejection_threshold_bytes'] and receipt['source_artifact_bytes'] <= plan['source_artifact_rejection_threshold_bytes'], 'recorded build artifact bound failed')
    return identity
