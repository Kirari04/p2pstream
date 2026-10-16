import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time

repo = Path('/home/user/.codex/worktrees/astra-proxy-performance/p2pstream')
reference = sys.argv[1]
assert re.fullmatch(r'[0-9a-f]{40}', reference)
assert subprocess.check_output(['git', 'rev-parse', reference + '^{commit}'], cwd=repo, text=True).strip() == reference
bundle = repo / 'tmp/tunnel-lowat-source.u0JuCU/artifacts/bundle'
go = Path('/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go')
base_ref = '5eeccaaa797254d7bde4db9854a337a02c3bc28e'
root = Path(tempfile.mkdtemp(prefix='p2pstream-wan-tunnel-lowat-causal.'))
root.chmod(0o700)
Path('/tmp/p2pstream-wan-tunnel-lowat-causal-root-path').write_text(str(root) + '\n')
scripts = root / 'frozen/scripts'
scripts.mkdir(parents=True)
for name in ('qualify-proxy-agent-tunnel-lowat-causal.sh', 'qualify-proxy-agent-tunnel-lowat-causal-stability.sh'):
    path = scripts / name
    path.write_bytes(subprocess.check_output(['git', 'show', reference + ':scripts/' + name], cwd=repo))
    path.chmod(0o700)

def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

assert sha(scripts / 'qualify-proxy-agent-tunnel-lowat-causal.sh') == '0adef903ef51c3bffe153b2a528442dda1ed8e9678078123520cb4c70c556d4b'
assert sha(scripts / 'qualify-proxy-agent-tunnel-lowat-causal-stability.sh') == 'a813e75d1bde257fb337e0054f3cb902a7f0a9a6a4d89dc1815c24137de4f2e5'
manifest_path = bundle / 'build-manifest.json'
manifest = json.loads(manifest_path.read_text())
assert sha(manifest_path) == '1c3cc0f51ca0cbabc9d45b3b6d52cdba5334ef8170d870112b02dfcd83e34d06'
assert manifest['base_source']['ref'] == base_ref and manifest['build']['tags'] == []
assert manifest['build']['go_version'] == 'go1.26.6'
assert manifest['kind'] == 'p2pstream-tunnel-lowat-causal-bundle-v1'
roles = {
    'driver_and_origin': manifest['driver_origin'],
    'control_server': manifest['variants']['tunnel-lowat-default-control']['binaries']['server'],
    'control_agent': manifest['variants']['tunnel-lowat-default-control']['binaries']['agent'],
    'lowat_server': manifest['variants']['tunnel-lowat-32k']['binaries']['server'],
    'lowat_agent': manifest['variants']['tunnel-lowat-32k']['binaries']['agent'],
}
env = dict(os.environ)
env.update(GOTOOLCHAIN='local', GOWORK='off', GOFLAGS='')
env['PATH'] = str(go.parent) + os.pathsep + env['PATH']
owned_tmp = Path(tempfile.mkdtemp(prefix='tunnel-lowat-launch-tmp.', dir=repo / 'tmp'))
for key in ('TMPDIR', 'TEMP', 'TMP', 'GOTMPDIR'):
    env[key] = str(owned_tmp)
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
assert roles['driver_and_origin']['sha256'] == roles['control_server']['sha256']
assert roles['control_server']['sha256'] != roles['lowat_server']['sha256']
assert roles['control_agent']['sha256'] != roles['lowat_agent']['sha256']
(root / 'launcher.py').write_bytes(Path(__file__).read_bytes())
(root / 'actual-binary-build-info.json').write_text(json.dumps(actual_build, indent=2) + '\n')
wrapper = scripts / 'qualify-proxy-agent-tunnel-lowat-causal-stability.sh'
command = ['bash', str(wrapper), '--runner', str(scripts / 'qualify-proxy-agent-tunnel-lowat-causal.sh'),
    '--driver-binary', str(bundle / roles['driver_and_origin']['path']),
    '--control-server-binary', str(bundle / roles['control_server']['path']),
    '--control-agent-binary', str(bundle / roles['control_agent']['path']),
    '--lowat-server-binary', str(bundle / roles['lowat_server']['path']),
    '--lowat-agent-binary', str(bundle / roles['lowat_agent']['path']),
    '--base-source-ref', base_ref, '--control-source-ref', base_ref, '--lowat-source-ref', base_ref,
    '--common-scaffold', str(bundle / manifest['overlays']['common_scaffold']['path']),
    '--lowat-32k-overlay', str(bundle / manifest['overlays']['lowat_32k_overlay']['path']),
    '--overlay-readme', str(bundle / manifest['evidence']['readme']['path']),
    '--source-hashes', str(bundle / manifest['evidence']['source_hashes']['path']),
    '--build-provenance', str(bundle / 'build-provenance.txt'), '--build-manifest', str(manifest_path)]
(root / 'command.json').write_text(json.dumps(dict(immutable_runner_ref=reference, base_ref=base_ref,
    wrapper_sha256=sha(wrapper), runner_sha256=sha(scripts / 'qualify-proxy-agent-tunnel-lowat-causal.sh'),
    build_manifest_sha256=sha(manifest_path), launcher_sha256=sha(root / 'launcher.py'), owned_tmp_environment={key: env[key] for key in ('TMPDIR', 'TEMP', 'TMP', 'GOTMPDIR')}, cpu_affinity=sorted(os.sched_getaffinity(0)),
    command=command + ['--output-dir', str(root / 'cohort')]), indent=2) + '\n')
print('Frozen physical tunnel LOWAT diagnostic root:', root, flush=True)
preflight = command + ['--output-dir', str(root / 'preflight'), '--preflight-only']
with (root / 'preflight.log').open('w') as log:
    result = subprocess.run(preflight, cwd=repo, env=env, stdout=log, stderr=subprocess.STDOUT)
assert result.returncode == 0, 'wrapper preflight failed; preserved preflight.log'
started = time.monotonic()
failure = None
forced_group_cleanup = False
with (root / 'launcher.log').open('w') as log:
    process = subprocess.Popen(command + ['--output-dir', str(root / 'cohort')], cwd=repo, env=env,
                               stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
    previous_sigterm = signal.getsignal(signal.SIGTERM)
    def terminate(signum, frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, terminate)
    try:
        process.wait(timeout=1800)
    except BaseException as exc:
        failure = exc
    finally:
        try:
            def group_alive():
                try:
                    os.killpg(process.pid, 0)
                    return True
                except ProcessLookupError:
                    return False
            if group_alive():
                forced_group_cleanup = True
                try:
                    os.killpg(process.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                deadline = time.monotonic() + 25
                while group_alive() and time.monotonic() < deadline:
                    process.poll()
                    time.sleep(.05)
                if group_alive():
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                if process.poll() is None:
                    process.wait(timeout=5)
        finally:
            signal.signal(signal.SIGTERM, previous_sigterm)
            elapsed = time.monotonic() - started
            (root / 'exit.json').write_text(json.dumps(dict(exit_code=process.returncode,
                elapsed_seconds=elapsed, exception=repr(failure) if failure else None, forced_group_cleanup=forced_group_cleanup), indent=2) + '\n')
result = process
if failure is not None:
    raise failure
if forced_group_cleanup:
    raise RuntimeError('outer launcher required forced owned-group cleanup; evidence preserved, qualification inconclusive')
print(json.dumps(dict(root=str(root), exit_code=result.returncode, elapsed_seconds=elapsed)), flush=True)
summary_path = root / 'cohort/results.summary.json'
if summary_path.exists():
    summary = json.loads(summary_path.read_text())
    print(json.dumps({key: summary[key] for key in ('acceptance', 'transition_screen', 'failures')}), flush=True)
raise SystemExit(result.returncode)
