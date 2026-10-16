import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time

repo = Path('/home/user/.codex/worktrees/astra-proxy-performance/p2pstream')
reference = sys.argv[1]
assert re.fullmatch(r'[0-9a-f]{40}', reference)
assert subprocess.check_output(['git', 'rev-parse', reference + '^{commit}'], cwd=repo, text=True).strip() == reference
bundle = repo / 'tmp/refund-control-split-source.Vb4KG6'
go = Path('/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go')
base_ref = '5eeccaaa797254d7bde4db9854a337a02c3bc28e'
root = Path(tempfile.mkdtemp(prefix='p2pstream-wan-refund-causal.'))
root.chmod(0o700)
Path('/tmp/p2pstream-wan-refund-causal-root-path').write_text(str(root) + '\n')
scripts = root / 'frozen/scripts'
scripts.mkdir(parents=True)
for name in ('qualify-proxy-agent-refund-emission-causal.sh', 'qualify-proxy-agent-refund-emission-causal-stability.sh'):
    path = scripts / name
    path.write_bytes(subprocess.check_output(['git', 'show', reference + ':scripts/' + name], cwd=repo))
    path.chmod(0o700)

def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

manifest_path = bundle / 'meta/manifest.json'
manifest = json.loads(manifest_path.read_text())
assert manifest['base_ref'] == base_ref and manifest['ordinary_tags'] == 'off'
roles = {
    'driver_and_origin': manifest['driver_origin'],
    'current_server': manifest['variants']['refund-gathered-control']['server'],
    'current_agent': manifest['variants']['refund-gathered-control']['agent'],
    'split_server': manifest['variants']['refund-split-control']['server'],
    'split_agent': manifest['variants']['refund-split-control']['agent'],
}
env = dict(os.environ)
env.update(GOTOOLCHAIN='local', GOWORK='off', GOFLAGS='')
env['PATH'] = str(go.parent) + os.pathsep + env['PATH']
actual_build = {}
for role, expected in roles.items():
    path = bundle / expected['path']
    assert re.fullmatch(r'[0-9a-f]{64}', expected['sha256'])
    assert sha(path) == expected['sha256']
    build_id = subprocess.check_output([str(go), 'tool', 'buildid', str(path)], env=env, text=True).strip()
    build_info = subprocess.check_output([str(go), 'version', '-m', str(path)], env=env, text=True)
    assert build_id and build_id == expected['build_id']
    assert build_info.splitlines()[0].endswith(': go1.26.6')
    assert not re.search(r'\b-tags(?:=|\s)', build_info)
    actual_build[role] = dict(path=str(path), sha256=expected['sha256'], build_id=build_id, build_info=build_info)
assert roles['driver_and_origin']['sha256'] == roles['current_server']['sha256']
assert roles['current_server']['sha256'] != roles['split_server']['sha256']
assert roles['current_agent']['sha256'] != roles['split_agent']['sha256']
(root / 'actual-binary-build-info.json').write_text(json.dumps(actual_build, indent=2) + '\n')
wrapper = scripts / 'qualify-proxy-agent-refund-emission-causal-stability.sh'
command = ['bash', str(wrapper), '--runner', str(scripts / 'qualify-proxy-agent-refund-emission-causal.sh'),
    '--driver-binary', str(bundle / roles['driver_and_origin']['path']),
    '--current-server-binary', str(bundle / roles['current_server']['path']),
    '--current-agent-binary', str(bundle / roles['current_agent']['path']),
    '--split-server-binary', str(bundle / roles['split_server']['path']),
    '--split-agent-binary', str(bundle / roles['split_agent']['path']),
    '--base-source-ref', base_ref, '--current-source-ref', base_ref, '--split-source-ref', base_ref,
    '--common-scaffold', str(bundle / manifest['overlays']['common_scaffold']['path']),
    '--split-overlay', str(bundle / manifest['overlays']['split_arm_toggle']['path']),
    '--overlay-readme', str(bundle / manifest['overlays']['readme']['path']),
    '--source-hashes', str(bundle / manifest['overlays']['source_hashes']['path']),
    '--build-provenance', str(bundle / 'meta/build-provenance.txt'), '--build-manifest', str(manifest_path)]
(root / 'command.json').write_text(json.dumps(dict(immutable_runner_ref=reference, base_ref=base_ref,
    wrapper_sha256=sha(wrapper), runner_sha256=sha(scripts / 'qualify-proxy-agent-refund-emission-causal.sh'),
    build_manifest_sha256=sha(manifest_path), cpu_affinity=sorted(os.sched_getaffinity(0)),
    command=command + ['--output-dir', str(root / 'cohort')]), indent=2) + '\n')
print('Frozen refund-emission diagnostic root:', root, flush=True)
preflight = command + ['--output-dir', str(root / 'preflight'), '--preflight-only']
with (root / 'preflight.log').open('w') as log:
    result = subprocess.run(preflight, cwd=repo, env=env, stdout=log, stderr=subprocess.STDOUT)
assert result.returncode == 0, 'wrapper preflight failed; preserved preflight.log'
started = time.monotonic()
with (root / 'launcher.log').open('w') as log:
    result = subprocess.run(command + ['--output-dir', str(root / 'cohort')], cwd=repo, env=env, stdout=log, stderr=subprocess.STDOUT)
elapsed = time.monotonic() - started
(root / 'exit.json').write_text(json.dumps(dict(exit_code=result.returncode, elapsed_seconds=elapsed), indent=2) + '\n')
print(json.dumps(dict(root=str(root), exit_code=result.returncode, elapsed_seconds=elapsed)), flush=True)
summary_path = root / 'cohort/results.summary.json'
if summary_path.exists():
    summary = json.loads(summary_path.read_text())
    print(json.dumps({key: summary[key] for key in ('acceptance', 'transition_screen', 'failures')}), flush=True)
raise SystemExit(result.returncode)
