import datetime
import hashlib
import json
import os
import pathlib
import subprocess
import sys

prepared_path = pathlib.Path(sys.argv[1])
destination = pathlib.Path(sys.argv[2])
prepared = json.loads(prepared_path.read_text())
report = {
    'recorded_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'prepared_manifest_sha256': hashlib.sha256(prepared_path.read_bytes()).hexdigest(),
    'independent_absolute_candidate_capacity': True,
    'clears_legacy_performance_comparison': False,
    'clears_wan': False,
    'files': {}, 'archives': {}, 'preserved_attempts': {}, 'failures': [],
}

def digest(data):
    return hashlib.sha256(data).hexdigest()

for label, item in prepared['inputs'].items():
    path = pathlib.Path(item['path'])
    actual = digest(path.read_bytes())
    report['files'][label] = {'path': str(path), 'sha256': actual, 'expected_sha256': item['sha256']}
    if actual != item['sha256']:
        report['failures'].append('input hash mismatch: ' + label)

original = json.loads(pathlib.Path(prepared['inputs']['original_clean_archive_manifest']['path']).read_text())
for label, archive in original['archives'].items():
    source = pathlib.Path(archive['source_directory'])
    files = []
    names = set()
    for item in archive['files']:
        names.add(item['path'])
        path = source / item['path']
        data = os.fsencode(os.readlink(path)) if item['mode'] == '120000' else path.read_bytes()
        actual = digest(data)
        blob = hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest()
        valid = (actual == item['sha256'] and blob == item['expected_blob']
                 and (item['mode'] == '120000' or bool(path.stat().st_mode & 0o111) == (item['mode'] == '100755')))
        files.append({'path': item['path'], 'sha256': actual, 'git_blob': blob, 'verified': valid})
        if not valid:
            report['failures'].append(f'{label}: source content/mode mismatch: {path}')
    actual_names = {str(path.relative_to(source)) for path in source.rglob('*') if path.is_symlink() or path.is_file()}
    if actual_names != names:
        report['failures'].append(label + ': extracted source file set differs')
    stream_hash = hashlib.sha256()
    child = subprocess.Popen(['git', 'archive', archive['ref']], stdout=subprocess.PIPE)
    while chunk := child.stdout.read(1024 * 1024):
        stream_hash.update(chunk)
    if child.wait() != 0:
        report['failures'].append(label + ': git archive failed')
    archive_hash = stream_hash.hexdigest()
    if archive_hash != prepared['archive_stream_sha256'][label]:
        report['failures'].append(label + ': streamed archive hash differs')
    report['archives'][label] = {
        'source_ref': archive['ref'], 'source_directory': str(source),
        'archive_stream_sha256': archive_hash, 'file_count': len(files), 'files': files,
        'intentional_overlay_paths': archive['intentional_overlay_paths'],
    }

for label, item in prepared['immutable_evidence_indexes'].items():
    index = json.loads(pathlib.Path(item['path']).read_text())
    root = pathlib.Path(item['root'])
    changed = [entry['path'] for entry in index if digest((root / entry['path']).read_bytes()) != entry['sha256']]
    report['preserved_attempts'][label] = {'files_verified': len(index), 'changed_files': changed}
    if changed:
        report['failures'].append(label + ': earlier attempt evidence changed')

stock = pathlib.Path(prepared['inputs']['stock_checkpoint']['path']).read_text()
runner = pathlib.Path(prepared['inputs']['independent_orchestrator']['path']).read_text()
marker = 'python3 - "$output_root/results" "$summary" "candidate-$candidate_ref" <<\'PY\'\n'
extract = lambda text: text.split(marker, 1)[1].split('\nPY\n', 1)[0] + '\n'
validator = extract(stock)
if extract(runner) != validator or digest(validator.encode()) != prepared['validator_sha256']:
    report['failures'].append('stock validator bytes differ')
report['validator_sha256'] = digest(validator.encode())
report['accepted'] = not report['failures']
destination.write_text(json.dumps(report, indent=2) + '\n')
print(json.dumps({'accepted': report['accepted'], 'failures': report['failures'],
                  'archive_file_counts': {name: row['file_count'] for name, row in report['archives'].items()},
                  'preserved_attempts': report['preserved_attempts']}, indent=2))
if not report['accepted']:
    raise SystemExit(1)
