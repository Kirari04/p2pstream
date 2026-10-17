from pathlib import Path
import hashlib
import importlib.util
import json
import os

ROOT = Path(__file__).resolve().parent
OLD = ROOT.parent / 'wan-controller-screen-20261017'
SOURCE = OLD / 'test-source'
GO = Path('/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go')

def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def fresh(path, value):
    with path.open('x') as handle:
        json.dump(value, handle, indent=2, sort_keys=True)
        handle.write('\n')

def main():
    pins = {
        SOURCE / 'go.mod': '165d373cd30192573bfd30e0f0997b393c433368d3ac01df8b178aaf4bdb5d65',
        SOURCE / 'go.sum': '7e451e21339534258c770d5d4669cffd5c126d38e91026e09f35fa93c89173f2',
        GO: '29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45',
    }
    for path, expected in pins.items():
        if digest(path) != expected:
            raise ValueError('pinned input differs: ' + str(path))
    spec = importlib.util.spec_from_file_location('owned_dependency_restore', OLD / 'run-controller-screen.py')
    supervisor = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(supervisor)
    supervisor.ROOT = ROOT
    supervisor.BUDGET = 768 << 20
    cache = ROOT / 'module-cache'
    if cache.exists():
        raise ValueError('fresh dedicated dependency cache required')
    env = os.environ.copy()
    env.update(GOMAXPROCS='2', GOENV='off', GOWORK='off', GOFLAGS='', GOTOOLCHAIN='local',
               GOPROXY='https://proxy.golang.org', GOSUMDB='sum.golang.org',
               GOPRIVATE='', GONOPROXY='', GONOSUMDB='', GOMODCACHE=str(cache))
    argv = ['taskset', '-c', '8-11', 'timeout', '--signal=TERM', '--kill-after=5s', '240s',
            str(GO), 'list', '-deps', '-test', '-mod=readonly', '-tags=p2pstream_qualification',
            '-f', '{{if not .Standard}}{{.ImportPath}} {{if .Module}}{{.Module.Path}}@{{.Module.Version}}{{end}}{{end}}',
            './internal/agent', './internal/server', './internal/tunnel']
    fresh(ROOT / 'PLAN.json', {'action': 'pinned package/test dependency resolution only',
          'compilation': False, 'test_execution': False, 'endpoint_execution': False,
          'argv': argv, 'cwd': str(SOURCE), 'cache': str(cache),
          'input_hashes': {str(p): h for p, h in pins.items()},
          'supervisor_sha256': digest(OLD / 'run-controller-screen.py'),
          'source_sha256': digest(Path(__file__)), 'cache_and_output_rejection_bytes': supervisor.BUDGET,
          'per_stream_rejection_bytes': 1 << 20, 'outer_deadline_seconds': 270,
          'checksum_policy': 'readonly pinned go.mod/go.sum plus official module proxy/checksum database'})
    os.chdir(SOURCE)
    failure = None
    try:
        supervisor.execute_owned(argv, env, ROOT / 'resolve', 270, 1 << 20)
    except BaseException as error:
        failure = type(error).__name__ + ': ' + str(error)
    audit = json.loads((ROOT / 'resolve.outer.json').read_text())
    unchanged = all(digest(p) == h for p, h in pins.items())
    accepted = (failure is None and audit['status'] == 0 and not audit['forced']
                and not audit['adopted_descendants'] and not audit['remaining_owned'] and unchanged)
    fresh(ROOT / 'RESULT.json', {'accepted': accepted, 'failure': failure,
          'inputs_unchanged': unchanged, 'outer_sha256': digest(ROOT / 'resolve.outer.json'),
          'stdout_sha256': digest(ROOT / 'resolve.stdout.log'), 'stderr_sha256': digest(ROOT / 'resolve.stderr.log'),
          'artifact_bytes': supervisor.artifact_bytes(), 'test_execution': False, 'endpoint_execution': False})
    print(json.dumps({'accepted': accepted, 'status': audit['status'], 'forced': audit['forced'],
                      'inputs_unchanged': unchanged, 'artifact_bytes': supervisor.artifact_bytes(), 'failure': failure}))
    if not accepted:
        raise SystemExit(1)

if __name__ == '__main__':
    main()
