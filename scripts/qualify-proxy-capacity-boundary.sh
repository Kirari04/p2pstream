#!/usr/bin/env bash
# Finite held-request boundary search. Execute only with exclusive benchmark CPU.
set -Eeuo pipefail
cd "$(dirname "$0")/.."
export P2PSTREAM_CAPACITY_BOUNDARY_RUNNER=$(realpath "$0")
exec python3 - "$@" <<'PY'
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile

parser = argparse.ArgumentParser(description='Find held-request capacity boundaries with immutable inputs')
parser.add_argument('--candidate-ref', required=True, help='committed source used to build every supplied test binary')
parser.add_argument('--candidate-binary', required=True)
parser.add_argument('--candidate-agent-binary', required=True)
parser.add_argument('--driver-binary', required=True)
parser.add_argument('--output-parent', default='tmp/capacity-boundary')
args = parser.parse_args()

def interrupted(signum, frame):
    raise KeyboardInterrupt

# Let the active point's exception path stop only its own process group and
# allow the frozen runner to clean up its named containers and children.
signal.signal(signal.SIGTERM, interrupted)
for tool in ('git', 'docker', 'go', 'jq', 'openssl', 'haproxy', 'python3', 'taskset', 'lscpu'):
    if not shutil.which(tool):
        parser.error(f'{tool} is required')

def command_output(command):
    return subprocess.check_output(command, text=True).strip()

def sha(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as source:
        for block in iter(lambda: source.read(1 << 20), b''):
            digest.update(block)
    return digest.hexdigest()

def committed_file(name):
    return subprocess.check_output(['git', 'show', f'{commit}:{name}'])

commit = command_output(['git', 'rev-parse', args.candidate_ref + '^{commit}'])
own_source = Path(os.environ['P2PSTREAM_CAPACITY_BOUNDARY_RUNNER']).read_bytes()
if committed_file('scripts/qualify-proxy-capacity-boundary.sh') != own_source:
    parser.error('commit this exact boundary runner before starting qualification')
binaries = {
    'server': Path(args.candidate_binary).resolve(strict=True),
    'agent': Path(args.candidate_agent_binary).resolve(strict=True),
    'driver': Path(args.driver_binary).resolve(strict=True),
}
if any(not os.access(path, os.X_OK) for path in binaries.values()):
    parser.error('every supplied binary must be executable')
parent = Path(args.output_parent).resolve()
parent.mkdir(parents=True, exist_ok=True)
destination = Path(tempfile.mkdtemp(prefix='run.', dir=parent))
destination.chmod(0o700)
frozen = destination / 'frozen'
(frozen / 'scripts').mkdir(parents=True)
(frozen / 'tmp').mkdir()
runner = frozen / 'scripts/qualify-proxy-performance.sh'
runner.write_bytes(committed_file('scripts/qualify-proxy-performance.sh'))
runner.chmod(0o700)
(destination / 'boundary-runner.sh').write_bytes(own_source)
for role, source in binaries.items():
    target = frozen / f'{role}.test'
    shutil.copyfile(source, target)
    target.chmod(0o500)
    if sha(source) != sha(target):
        raise SystemExit(f'{role} binary changed while freezing inputs')
    binaries[role] = target
for role, source in (
    ('server', 'internal/server/proxy_qualification_test.go'),
    ('agent', 'internal/agent/proxy_qualification_external_test.go'),
):
    (destination / f'{role}-fixture.go.txt').write_bytes(committed_file(source))

manifest = {
    'started_at_utc': datetime.now(timezone.utc).isoformat(),
    'candidate_ref': commit,
    'binary_source_association': 'Caller asserts binaries were built from this clean commit; hashes freeze their identity.',
    'binary_sha256': {role: sha(path) for role, path in binaries.items()},
    'runner_sha256': sha(runner),
    'boundary_runner_sha256': hashlib.sha256(own_source).hexdigest(),
    'fixture_sha256': {role: sha(destination / f'{role}-fixture.go.txt') for role in ('server', 'agent')},
    'go_version': command_output(['go', 'version']),
    'kernel': command_output(['uname', '-a']),
    'haproxy_version': command_output(['haproxy', '-v']).splitlines()[0],
    'cpu_affinity': sorted(os.sched_getaffinity(0)),
    'cpu_topology': command_output(['lscpu', '-p=CPU,CORE,SOCKET,ONLINE']),
    'policy': {
        'initial_points': [2048, 4096], 'refinement_resolution': 256,
        'lower_probes_if_2048_fails': [1536, 1024, 512, 256],
        'additional_best_point_confirmations': 1,
        'fresh_processes_per_point': True, 'server_ballast_bytes': 1 << 30,
        'direct_topology': 'server plus idle colocated agent in one shared 4 GiB cgroup; independent ledgers',
        'remote_agent_topology': 'server 4 GiB plus one agent 4 GiB, distinct cgroups; four bulk lanes',
        'control_scope': 'HAProxy direct host process; no ballast or matching cgroup, not an agent or memory-parity gate',
        'meaning': 'Simultaneously held tiny requests, not sustained throughput or maximum body/header capacity',
    },
}
(destination / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
print(f'evidence={destination}', flush=True)
summary = {'candidate_ref': commit, 'shapes': {}, 'points': []}

def write_summary():
    temporary = destination / 'summary.json.tmp'
    temporary.write_text(json.dumps(summary, indent=2, sort_keys=True) + '\n')
    temporary.replace(destination / 'summary.json')

def event_checks(row):
    return (
        row.get('event_stats_captured') is True
        and row.get('persisted_event_delta') == row.get('attempted')
        and row.get('dropped_event_delta', 0) == 0
        and row.get('event_queue_after_drain', 0) == 0
        and not row.get('event_drain_error') and not row.get('event_stats_error')
    )

def classified_pre_http_events(row):
    # An accept rejected before HTTP parsing cannot produce a request event.
    # This exception only permits boundary refinement; it never passes a point.
    attempted = row.get('attempted', 0)
    persisted = row.get('persisted_event_delta', 0)
    responses = sum(row.get('status_classes', {}).values())
    rejections = row.get('admission_rejections', {})
    early = sum(rejections.get(key, 0) for key in ('public_connection_resource', 'public_connection_limit'))
    return (
        row.get('event_stats_captured') is True and early > 0
        and row.get('useful_completed', 0) < attempted
        and 0 <= responses <= persisted < attempted and attempted - persisted <= early
        and row.get('dropped_event_delta', 0) == 0 and row.get('event_queue_after_drain', 0) == 0
        and not row.get('event_drain_error') and not row.get('event_stats_error')
    )

def useful_checks(row, count):
    return (
        row.get('attempted') == count and row.get('useful_completed') == count
        and row.get('errors') == 0 and row.get('status_classes', {}).get('2xx') == count
        and sum(row.get('status_classes', {}).values()) == count
        and all(value == 0 for value in row.get('admission_rejections', {}).values())
    )

def constrained_checks(row, remote):
    processes = [row.get('server_process', {}), *row.get('agent_processes', [])]
    return (
        len(processes) == (2 if remote else 1)
        and len({process.get('cgroup_inode') for process in processes}) == len(processes)
        and all(
            process.get('container') and process.get('cgroup_inode', 0) > 0
            and process.get('cgroup_memory_max_bytes') == 1 << 32
            and process.get('cgroup_memory_events_captured') is True
            and process.get('oom_delta', 0) == 0 and process.get('oom_kill_delta', 0) == 0
            and process.get('peak_cgroup_memory_bytes', 0) > 0
            for process in processes
        )
    )

def point(path, protocol, count, confirmation=False):
    index = len(summary['points']) + 1
    target = destination / f'{index:02d}-{path}-{protocol}-{count}'
    target.mkdir()
    label = f'candidate-{commit}'
    result_path = target / 'results.jsonl'
    invocation = [str(runner), '--candidate-binary', str(binaries['server']),
                  '--candidate-agent-binary', str(binaries['agent']), '--driver-binary', str(binaries['driver']),
                  '--candidate-label', label, '--runs', '1', '--capacity-only', '--capacity-boundary-diagnostics',
                  '--capacity-concurrency', str(count), '--capacity-protocol', protocol,
                  '--ballast-mib', '1024', '--output', str(result_path)]
    if path == 'direct':
        invocation += ['--path-filter', 'direct']
    else:
        invocation += ['--external-agents', '1', '--external-agent-tunnels', '4']
    (target / 'command.json').write_text(json.dumps(invocation, indent=2) + '\n')
    with open(target / 'runner.log', 'w') as log:
        process = subprocess.Popen(invocation, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            exit_code = process.wait(timeout=1800)
        except BaseException:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=20)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
            raise
    rows = [json.loads(line) for line in result_path.read_text().splitlines() if line.strip()] if result_path.exists() else []
    candidates = [row for row in rows if row.get('label') == label]
    capacity_rows = [row for row in candidates if row.get('scenario') == f'capacity-{count}-{protocol}']
    recovery_rows = [row for row in candidates if row.get('scenario') == 'recovery']
    checks = {'runner_completed': exit_code == 0, 'capacity_row': len(capacity_rows) == 1, 'recovery_row': len(recovery_rows) == 1}
    event_mode = 'unproven'
    if len(capacity_rows) == 1:
        row = capacity_rows[0]
        owners = row.get('owner_peak_reservations', {})
        checks.update({
            'all_useful': useful_checks(row, count), 'origin_simultaneous': row.get('origin_peak_inflight') == count,
            'request_owner_simultaneous': owners.get('public_request') == count,
            'connection_shape': (owners.get('public_connection') == count and row.get('client_tcp_connections') == count)
                                if protocol == 'h1' else row.get('client_tcp_connections', 0) > 0,
            'correct_protocol': row.get('observed_http_major') == (1 if protocol == 'h1' else 2),
            'events': event_checks(row), 'constrained_no_oom': constrained_checks(row, path == 'remote-agent'),
            'ballast': row.get('server_ballast_bytes') == 1 << 30,
        })
        if checks['events']:
            event_mode = 'exact'
        elif classified_pre_http_events(row):
            event_mode = 'classified_pre_http_admission; failed point only'
        for agent in row.get('agent_processes', []):
            before, after = agent.get('agent_capacity_initial', {}), agent.get('agent_capacity_final', {})
            checks['agent_no_admission_rejections'] = checks.get('agent_no_admission_rejections', True) and all(
                after.get(key, 0) == before.get(key, 0) for key in ('rejected_pressure', 'rejected_fixed_limit')
            ) and bool(before) and bool(after)
    if len(recovery_rows) == 1:
        row = recovery_rows[0]
        checks['recovery'] = useful_checks(row, 512) and event_checks(row) and constrained_checks(row, path == 'remote-agent')
    safe = event_mode != 'unproven' and all(checks.get(key, False) for key in (
        'runner_completed', 'capacity_row', 'recovery_row', 'constrained_no_oom', 'recovery', 'ballast',
    ))
    result = {'path': path, 'protocol': protocol, 'concurrency': count, 'confirmation': confirmation,
              'checks': checks, 'accepted': all(checks.values()), 'safe_to_continue': safe,
              'capacity_event_conservation': event_mode,
              'exit_code': exit_code, 'evidence': str(target.relative_to(destination))}
    summary['points'].append(result)
    write_summary()
    print(json.dumps(result, sort_keys=True), flush=True)
    if not safe:
        raise RuntimeError(f'Infrastructure, memory, event conservation or recovery gate failed; inspect {target}')
    return result['accepted']

try:
    for path in ('direct', 'remote-agent'):
        for protocol in ('h1', 'h2'):
            shape = {'highest_confirmed_pass': None, 'lowest_observed_failure': None, 'tested_ceiling': 4096}
            summary['shapes'][f'{path}-{protocol}'] = shape
            lower, upper = 0, None
            if point(path, protocol, 2048):
                lower = 2048
                if point(path, protocol, 4096):
                    lower = 4096
                else:
                    upper = 4096
            else:
                upper = 2048
                for count in (1536, 1024, 512, 256):
                    if point(path, protocol, count):
                        lower = count
                        break
                    upper = count
            while lower and upper is not None and upper - lower > 256:
                middle = ((lower + upper) // 512) * 256
                if point(path, protocol, middle):
                    lower = middle
                else:
                    upper = middle
            if lower and point(path, protocol, lower, confirmation=True):
                shape['highest_confirmed_pass'] = lower
            elif lower:
                raise RuntimeError(f'Boundary confirmation was unstable for {path}/{protocol} at {lower}')
            shape['lowest_observed_failure'] = upper
            shape['interpretation'] = 'No passing point found' if not lower else (
                'At least the tested ceiling; maximum not established' if upper is None else
                'Observed pass/fail bracket; not a universal or exact maximum')
            write_summary()
    summary['completed'] = True
finally:
    write_summary()
print(f'summary={destination / "summary.json"}', flush=True)
PY
