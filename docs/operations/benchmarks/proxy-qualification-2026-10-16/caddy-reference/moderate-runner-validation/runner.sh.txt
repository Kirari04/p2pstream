#!/usr/bin/env bash
# Finite Caddy/HAProxy reference pass using the existing verified load driver.
set -Eeuo pipefail
cd "$(dirname "$0")/.."
exec python3 - "$@" <<'PY'
import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import shutil
import socket
import statistics
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser(description='Finite matched Caddy reverse-proxy diagnostic')
parser.add_argument('--caddy-binary', required=True)
parser.add_argument('--driver-binary', required=True)
parser.add_argument('--candidate-binary')
parser.add_argument('--candidate-label', default='candidate')
parser.add_argument('--driver-source-ref', required=True)
parser.add_argument('--harness-source', required=True, help='exact committed proxy_qualification_test.go used by both binaries; no overlays')
parser.add_argument('--candidate-source-ref')
parser.add_argument('--runs', type=int, default=3)
parser.add_argument('--duration-seconds', type=int, default=10)
parser.add_argument('--warmup-seconds', type=int, default=2)
parser.add_argument('--idle-per-host', type=int, default=32)
parser.add_argument('--concurrency', default='16,128')
parser.add_argument('--protocol', choices=['h1', 'h2', 'both'], default='both')
parser.add_argument('--matched-h2-single-connection', action='store_true',
                    help='prewarm one H2 client connection and require every timed H2 row to reuse only that connection')
parser.add_argument('--moderate-load-calibration',
                    help='completed frozen 36-row reference root; run three alternating candidate/HAProxy pairs at half the lower per-cell median')
args = parser.parse_args()
if args.candidate_binary and not args.candidate_source_ref:
    parser.error('--candidate-source-ref is required with --candidate-binary')
concurrencies = [int(value) for value in args.concurrency.split(',')]
if not (1 <= args.runs <= 10 and 1 <= args.duration_seconds <= 60 and 0 <= args.warmup_seconds <= 10
        and 1 <= args.idle_per_host <= 1024 and concurrencies and all(1 <= n <= 1536 for n in concurrencies)):
    parser.error('run, timing, idle or concurrency value is outside the finite diagnostic bounds')
for tool in ['docker', 'openssl', 'haproxy', 'taskset', 'go', 'git', 'findmnt', 'lscpu']:
    if not shutil.which(tool):
        parser.error(f'{tool} is required')
root = Path.cwd()
caddy = Path(args.caddy_binary).resolve(strict=True)
driver = Path(args.driver_binary).resolve(strict=True)
candidate = Path(args.candidate_binary).resolve(strict=True) if args.candidate_binary else None
haproxy = Path(shutil.which('haproxy')).resolve(strict=True)
harness = Path(args.harness_source).resolve(strict=True)
harness_bytes = harness.read_bytes()
source_commits = {}
for label, reference in [('driver', args.driver_source_ref), ('candidate', args.candidate_source_ref)]:
    if reference:
        commit = subprocess.check_output(['git', 'rev-parse', reference + '^{commit}'], text=True).strip()
        committed_harness = subprocess.check_output(['git', 'show', commit + ':internal/server/proxy_qualification_test.go'])
        if committed_harness != harness_bytes:
            parser.error(f'{label} harness differs from the immutable source commit; commit the harness before qualification')
        source_commits[label] = commit

def digest_file(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def moderate_calibration(calibration_root):
    calibration_root = Path(calibration_root).resolve(strict=True)
    manifest_path = calibration_root / 'manifest.json'
    summary_path = calibration_root / 'summary.json'
    manifest = json.loads(manifest_path.read_text())
    summary = json.loads(summary_path.read_text())
    evidence = Path(manifest['reference_run_dir']).resolve(strict=True)
    if not evidence.is_relative_to(calibration_root):
        raise ValueError('reference results must belong to the frozen calibration root')
    results = evidence / 'results.jsonl'
    provenance_path = evidence / 'provenance.json'
    provenance = json.loads(provenance_path.read_text())
    settings = provenance.get('settings') or {}
    if (provenance.get('workload') != 'saturated-reference'
            or settings.get('runs') != 3 or settings.get('duration_seconds') != 10
            or settings.get('warmup_seconds') != 2 or settings.get('idle_per_host') != 128
            or settings.get('concurrency') != '16,128' or settings.get('protocol') != 'both'
            or not settings.get('matched_h2_single_connection') or settings.get('moderate_load_calibration')):
        raise ValueError('calibration must be the fixed warmed matched-H2 reference workload')
    if (manifest['source_ref'] != source_commits.get('candidate')
            or manifest['source_ref'] != source_commits.get('driver')
            or manifest['runner_sha256'] != digest_file(root / 'scripts/qualify-proxy-caddy.sh')
            or manifest['harness_sha256'] != digest_file(harness)
            or manifest['binaries']['candidate_and_driver']['sha256'] != digest_file(candidate)
            or manifest['binaries']['candidate_and_driver']['sha256'] != digest_file(driver)
            or manifest['results_sha256'] != digest_file(results)
            or manifest['summary_sha256'] != digest_file(summary_path)
            or manifest['reference_provenance_sha256'] != digest_file(provenance_path)
            or manifest['workload']['exit_code'] != 0
            or manifest['workload']['forced_container_cleanup']
            or manifest['workload']['forced_group_cleanup']
            or provenance['haproxy_binary_sha256'] != digest_file(haproxy)
            or provenance['caddy_binary_sha256'] != digest_file(caddy)):
        raise ValueError('calibration source, binary, harness, runner or result identity differs')
    available_cpus = sorted(os.sched_getaffinity(0))
    if (provenance['proxy_cpus'] != ','.join(map(str, available_cpus[:4]))
            or provenance['load_cpus'] != ','.join(map(str, available_cpus[4:] or available_cpus[:4]))
            or provenance['cgroup_limit_bytes'] != 4 << 30 or not provenance['cgroup_swap_disabled']):
        raise ValueError('calibration CPU/cgroup topology differs from the requested comparison')
    rows = [json.loads(line) for line in results.read_text().splitlines()]
    if len(rows) != 36 or summary['row_count'] != 36:
        raise ValueError('calibration must contain the complete fixed 36-row reference')
    selected = {}
    for protocol in ('h1', 'h2'):
        for concurrency in (16, 128):
            rates = {}
            for kind in ('candidate', 'haproxy', 'caddy'):
                cell = [row for row in rows if row['protocol'] == protocol
                        and row['concurrency'] == concurrency and row['proxy_kind'] == kind]
                if len(cell) != 3 or {row['iteration'] for row in cell} != {1, 2, 3}:
                    raise ValueError('calibration needs three complete repeats per proxy cell')
                for row in cell:
                    if (row['errors'] or row['attempted'] <= 0 or row['useful_completed'] != row['attempted']
                            or row['response_bytes'] != 256 * row['attempted']
                            or row.get('observed_http_major') != (1 if protocol == 'h1' else 2)
                            or row.get('observed_origin_http_major') != 1
                            or row.get('configured_duration_seconds') != 10 or row.get('warmup_seconds') != 2
                            or not all(math.isfinite(row[field]) and row[field] > 0
                                       for field in ('wall_seconds', 'p95_ms', 'p99_ms'))
                            or not math.isclose(row['useful_requests_per_second'], row['useful_completed']/row['wall_seconds'], rel_tol=1e-9)
                            or row.get('status_codes') != {'200': row['attempted']}):
                        raise ValueError('calibration payload/status/protocol checks failed')
                    if protocol == 'h2' and (row.get('client_tcp_connections') != 1
                            or row.get('h2_prewarm_requests') != 1
                            or not row.get('h2_prewarm_connection_reused')
                            or row.get('h2_prewarm_additional_connection', False)):
                        raise ValueError('calibration H2 connection topology is not matched')
                    if kind == 'candidate' and (not row.get('event_stats_captured')
                            or row.get('persisted_event_delta') != row['attempted']
                            or row.get('dropped_event_delta', 0) or row.get('event_queue_after_drain', 0)
                            or row.get('event_drain_error') or row.get('event_stats_error')
                            or (protocol == 'h2' and row.get('h2_prewarm_persisted_events') != 1)):
                        raise ValueError('calibration candidate event conservation failed')
                values = [row['useful_requests_per_second'] for row in cell]
                if not all(math.isfinite(value) and value > 0 for value in values):
                    raise ValueError('calibration throughput must be finite and positive')
                rates[kind] = dict(samples=values, median=statistics.median(values),
                                   minimum=min(values), maximum=max(values), max_to_min=max(values)/min(values))
            offered = math.floor(.5 * min(rates[kind]['median'] for kind in ('candidate', 'haproxy')))
            if offered < 1:
                raise ValueError('calibration selected a zero offered rate')
            if 20 * offered < concurrency:
                raise ValueError('the fixed offered schedule has fewer requests than clients')
            selected[f'{protocol}-c{concurrency}'] = dict(offered_rps=offered, requests=20*offered,
                                                        calibration=rates)
    return dict(root=str(calibration_root), source_ref=manifest['source_ref'],
                manifest_sha256=digest_file(manifest_path), summary_sha256=digest_file(summary_path),
                results_sha256=digest_file(results), reference_provenance_sha256=digest_file(provenance_path), cells=selected,
                calibration_scope='complete warmed three-repeat medians; spread reported without a variance rejection threshold',
                selection='floor(0.5 * lower candidate/HAProxy useful-throughput median); N=floor(20 * offeredRPS)',
                pairs=3, order=['candidate-then-haproxy', 'haproxy-then-candidate', 'candidate-then-haproxy'],
                effective_duration_seconds=0, effective_warmup_seconds=0,
                h1_initial_client_establishment_included=True, h2_validated_prewarm_requests=1,
                delivery=dict(useful_rps_minimum_fraction=.99, elapsed_maximum_fraction=1.01),
                p95_p99_ratio_maximum=1.25, scope='moderate load; does not qualify near-saturation latency')

def validate_moderate_result(result, rate):
    if (result.get('response_bytes') != 256 * rate['requests']
            or result.get('status_codes') != {'200': rate['requests']}
            or not all(math.isfinite(result[field]) and result[field] > 0
                       for field in ('useful_requests_per_second', 'wall_seconds', 'p95_ms', 'p99_ms'))
            or not math.isclose(result['useful_requests_per_second'], rate['requests']/result['wall_seconds'], rel_tol=1e-9)):
        raise ValueError('payload/status/measurement validation failed')
    schedule = result.get('offered_schedule') or {}
    planned = (rate['requests'] - 1) / rate['offered_rps']
    schedule_start = datetime.fromisoformat(schedule.get('started_utc', ''))
    dispatch_end = datetime.fromisoformat(schedule.get('dispatch_completed_utc', ''))
    if (schedule_start.utcoffset() != timedelta(0) or dispatch_end.utcoffset() != timedelta(0)
            or dispatch_end < schedule_start
            or not math.isclose(schedule.get('planned_seconds', -1), planned, rel_tol=1e-12, abs_tol=1e-9)
            or not planned - 1e-6 <= schedule.get('dispatch_completed_seconds', -1) <= result['wall_seconds']
            or result.get('offered_requests_per_second') != rate['offered_rps']
            or result['attempted'] != rate['requests']
            or result['useful_requests_per_second'] < .99 * rate['offered_rps']
            or result['wall_seconds'] > 1.01 * rate['requests'] / rate['offered_rps']):
        raise ValueError('schedule delivery failed')

moderate_plan = None
if args.moderate_load_calibration:
    if (not candidate or args.runs != 3 or args.protocol != 'both' or concurrencies != [16, 128]
            or not args.matched_h2_single_connection or args.idle_per_host != 128):
        parser.error('moderate load requires candidate, three pairs, both protocols, clients 16,128, idle128 and matched H2')
    try:
        moderate_plan = moderate_calibration(args.moderate_load_calibration)
    except (OSError, ValueError, KeyError, TypeError) as error:
        parser.error(f'invalid moderate-load calibration: {error}')
(root / 'tmp/caddy-reference').mkdir(parents=True, exist_ok=True)
run_dir = Path(tempfile.mkdtemp(prefix='run.', dir=root / 'tmp/caddy-reference')).resolve()
run_dir.chmod(0o700)
(run_dir / 'qualification-harness.go.txt').write_bytes(harness_bytes)
(run_dir / 'runner.sh').write_bytes((root / 'scripts/qualify-proxy-caddy.sh').read_bytes())
cpus = sorted(os.sched_getaffinity(0))
proxy_cpus = ','.join(map(str, cpus[:4]))
load_cpus = ','.join(map(str, cpus[4:] or cpus[:4]))
threads = min(4, len(cpus))
processes = []
containers = []
logs = []
origin_stop = run_dir / 'origin.stop'

def output(command):
    return subprocess.check_output([str(value) for value in command], text=True).strip()

def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def launch(command, log_name, env=None):
    log = open(run_dir / log_name, 'w')
    logs.append(log)
    process = subprocess.Popen([str(value) for value in command], stdout=log, stderr=subprocess.STDOUT, env=env)
    processes.append(process)
    return process

def stop(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)

def free_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]

def await_ready(path, process):
    deadline = time.monotonic() + 25
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f'process exited before readiness; see {run_dir}')
        try:
            return json.loads(path.read_text())
        except (FileNotFoundError, json.JSONDecodeError):
            time.sleep(0.05)
    raise TimeoutError(f'no readiness file {path}')

def await_tls(port, process):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f'proxy exited before TLS readiness; see {run_dir}')
        try:
            result = subprocess.run(['openssl', 's_client', '-connect', f'127.0.0.1:{port}', '-servername', 'public.test'],
                                    input=b'', stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=1)
            if result.returncode == 0:
                return
        except subprocess.TimeoutExpired:
            pass
        time.sleep(0.05)
    raise TimeoutError('TLS proxy did not become ready')

def docker_process(binary, name, command, env):
    # Fixture SQLite writes must use disk, just like the existing production
    # qualification runner. A small /tmp tmpfs can fill during a sustained
    # raw-event run and would turn a storage-fixture failure into queue loss.
    scratch = run_dir / 'scratch' / name
    scratch.mkdir(mode=0o700, parents=True)
    containers.append(name)
    invocation = ['docker', 'run', '--name', name, '--network', 'host', '--user', f'{os.getuid()}:{os.getgid()}',
                  '--memory', '4g', '--memory-swap', '4g', '--cpuset-cpus', proxy_cpus, '--pids-limit', '8192',
                  '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                  '--mount', f'type=bind,source={scratch},target=/tmp',
                  '--mount', f'type=bind,source={binary},target=/proxy,readonly',
                  '--mount', f'type=bind,source={run_dir},target=/qualification']
    for key, value in env.items():
        invocation += ['--env', f'{key}={value}']
    invocation += ['debian:trixie-slim', '/proxy'] + command
    process = launch(invocation, f'{name}.log')
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f'container {name} exited; see {run_dir}')
        inspect = subprocess.run(['docker', 'inspect', '--format', '{{.State.Pid}}', name], capture_output=True, text=True)
        if inspect.returncode == 0 and inspect.stdout.strip().isdigit() and int(inspect.stdout) > 0:
            return process, inspect.stdout.strip()
        time.sleep(0.05)
    raise TimeoutError(f'container {name} did not start')

try:
    subprocess.run(['docker', 'info'], check=True, stdout=subprocess.DEVNULL)
    cert, key, bundle = [run_dir / name for name in ['public.crt.pem', 'public.key.pem', 'public.bundle.pem']]
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes',
                    '-subj', '/CN=public.test', '-days', '1', '-keyout', str(key), '-out', str(cert)],
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    bundle.write_bytes(key.read_bytes() + cert.read_bytes())
    for path in [cert, key, bundle]:
        path.chmod(0o600)
    scratch_filesystem = output(['findmnt', '-no', 'FSTYPE', '--target', run_dir])
    if scratch_filesystem in ['tmpfs', 'ramfs']:
        raise RuntimeError('qualification scratch must reside on disk, not a memory filesystem')
    scratch_available_bytes = shutil.disk_usage(run_dir).free
    version = output([caddy, 'version'])
    (run_dir / 'caddy-build-info.txt').write_text(output(['go', 'version', '-m', caddy]) + '\n')
    meta = dict(started_utc=datetime.now(timezone.utc).isoformat(),
                workload='moderate-load-latency' if moderate_plan else 'saturated-reference',
                moderate_load_plan=moderate_plan,
                source_commits=source_commits, harness_sha256=sha(harness), test_overlays=[],
                source_contract='binaries built from clean archives of source_commits, without test or production overlays',
                caddy_version=version, caddy_binary_sha256=sha(caddy), driver_binary_sha256=sha(driver),
                candidate_binary_sha256=sha(candidate) if candidate else None, candidate_label=args.candidate_label,
                driver_build_info=output(['go', 'version', '-m', driver]),
                candidate_build_info=output(['go', 'version', '-m', candidate]) if candidate else None,
                script_sha256=sha(root / 'scripts/qualify-proxy-caddy.sh'),
                haproxy_path=str(haproxy), haproxy_binary_sha256=sha(haproxy), haproxy_version=output([haproxy, '-v']),
                go_version=output(['go', 'version']),
                kernel=dict(sysname=os.uname().sysname, release=os.uname().release,
                            version=os.uname().version, machine=os.uname().machine),
                cpu_topology=output(['lscpu', '-e=CPU,CORE,SOCKET,NODE,ONLINE']),
                docker_image_id=output(['docker', 'image', 'inspect', '--format', '{{.Id}}', 'debian:trixie-slim']),
                proxy_cpus=proxy_cpus, load_cpus=load_cpus, gomaxprocs=threads,
                settings=vars(args), cgroup_limit_bytes=4 << 30, cgroup_swap_disabled=True,
                scratch_storage='private per-container disk-backed bind mounted at /tmp',
                scratch_host_filesystem=scratch_filesystem, scratch_available_bytes_before=scratch_available_bytes,
                h2_connection_contract=('one validated prewarmed client connection reused by each timed H2 row'
                                        if args.matched_h2_single_connection else 'unmatched observed client connections'),
                limitations=['HAProxy remains a host process; its cgroup memory is not isolated.',
                             'Caddy official release and p2pstream may use different Go patch releases.',
                             'p2pstream includes its full policy/recorder pipeline and an idle in-process agent fixture.',
                             ('Matched single-connection H2 is a topology-controlled comparison, not HAProxy maximum multi-thread throughput.'
                              if args.matched_h2_single_connection else
                              'Public H2 connection counts are observed but not constrained across proxies.')])
    (run_dir / 'provenance.json').write_text(json.dumps(meta, indent=2) + '\n')
    ready = run_dir / 'origin.ready.json'
    env = os.environ.copy()
    env.update(P2PSTREAM_QUALIFICATION_ROLE='origin', P2PSTREAM_QUALIFICATION_READY=str(ready),
               P2PSTREAM_QUALIFICATION_STOP=str(origin_stop), P2PSTREAM_QUALIFICATION_ORIGIN_TLS='0', GOMAXPROCS=str(threads))
    origin = launch(['taskset', '-c', load_cpus, driver, '-test.run', '^TestProxyQualificationProcess$', '-test.timeout', '15m'], 'origin.log', env)
    origin_url = await_ready(ready, origin)['url']
    origin_address = origin_url.removeprefix('http://')
    results_path = run_dir / 'results.jsonl'
    print(f'{"Moderate-load" if moderate_plan else "Caddy"} diagnostic evidence: {run_dir}', flush=True)
    sequence = 0
    result_rows = []
    for iteration in range(1, args.runs + 1):
        kinds = ['candidate', 'haproxy'] if moderate_plan else ['caddy', 'haproxy'] + (['candidate'] if candidate else [])
        offset = (iteration - 1) % len(kinds)
        kinds = kinds[offset:] + kinds[:offset]
        for kind in kinds:
            port = free_port()
            admin_url = ''
            name = f'p2pstream-caddy-ref-{os.getpid()}-{iteration}-{kind}'
            if kind == 'caddy':
                caddyfile = run_dir / f'{name}.Caddyfile'
                caddyfile.write_text('''{
    admin off
    persist_config off
    auto_https off
    default_sni public.test
    servers {
        protocols h1 h2
    }
}
https://:PORT {
    bind 127.0.0.1
    tls /qualification/public.crt.pem /qualification/public.key.pem
    reverse_proxy ORIGIN {
        transport http {
            versions 1.1
            keepalive 90s
            keepalive_idle_conns 1024
            keepalive_idle_conns_per_host IDLE
        }
    }
}
'''.replace('PORT', str(port)).replace('ORIGIN', origin_address).replace('IDLE', str(args.idle_per_host)))
                config = run_dir / f'{name}.json'
                with open(config, 'w') as config_file:
                    subprocess.run([str(caddy), 'adapt', '--adapter', 'caddyfile', '--config', str(caddyfile)], check=True, stdout=config_file)
                proxy, sample_pid = docker_process(caddy, name, ['run', '--config', '/qualification/' + config.name],
                                                  {'GOMAXPROCS': str(threads), 'XDG_DATA_HOME': '/tmp/caddy-data', 'XDG_CONFIG_HOME': '/tmp/caddy-config'})
                label, envelope = 'caddy-' + version.split()[0] + f'-idle{args.idle_per_host}', 'caddy-process-4GiB-cgroup'
            elif kind == 'haproxy':
                config = run_dir / f'{name}.cfg'
                h2_stream_setting = '  tune.h2.fe.max-concurrent-streams 256\n' if args.matched_h2_single_connection else ''
                config.write_text(f'''global
  maxconn 4096
  nbthread {threads}
{h2_stream_setting}defaults
  mode http
  timeout connect 5s
  timeout client 30s
  timeout server 30s
frontend public
  bind 127.0.0.1:{port} ssl crt {bundle} alpn h2,http/1.1
  default_backend origin
backend origin
  server origin {origin_address}
''')
                proxy = launch(['taskset', '-c', proxy_cpus, haproxy, '-db', '-f', config], f'{name}.log')
                sample_pid = str(proxy.pid)
                label, envelope = 'haproxy-reference', 'haproxy-host-process'
            else:
                ready = run_dir / f'{name}.ready.json'
                stop_file = run_dir / f'{name}.stop'
                proxy, sample_pid = docker_process(candidate, name, ['-test.run', '^TestProxyQualificationProcess$', '-test.timeout', '15m'],
                    {'GOMAXPROCS': str(threads), 'P2PSTREAM_QUALIFICATION_ROLE': 'proxy',
                     'P2PSTREAM_QUALIFICATION_READY': '/qualification/' + ready.name,
                     'P2PSTREAM_QUALIFICATION_STOP': '/qualification/' + stop_file.name,
                     'P2PSTREAM_QUALIFICATION_ORIGIN_URL': origin_url, 'P2PSTREAM_QUALIFICATION_ORIGIN_TLS': '0',
                     'P2PSTREAM_QUALIFICATION_CERT': '/qualification/public.crt.pem',
                     'P2PSTREAM_QUALIFICATION_KEY': '/qualification/public.key.pem', 'P2PSTREAM_QUALIFICATION_BALLAST_MIB': '0'})
                state = await_ready(ready, proxy)
                port = int(state['url'].rsplit(':', 1)[1])
                admin_url = state['admin_url']
                label, envelope = args.candidate_label, 'p2pstream-server+in-process-real-agent'
            await_tls(port, proxy)
            protocols = ['h1', 'h2'] if args.protocol == 'both' else [args.protocol]
            for concurrency in concurrencies:
                for protocol in protocols:
                    sequence += 1
                    scenario = 'h1-keepalive-duration' if protocol == 'h1' else 'h2-multiplex-duration'
                    result_file = run_dir / f'{sequence}-{kind}-c{concurrency}-{protocol}.json'
                    rate = moderate_plan['cells'][f'{protocol}-c{concurrency}'] if moderate_plan else None
                    load_env = os.environ.copy()
                    load_env.update(P2PSTREAM_QUALIFICATION_ROLE='load', P2PSTREAM_QUALIFICATION_URL=f'https://127.0.0.1:{port}/direct/small',
                        P2PSTREAM_QUALIFICATION_SCENARIO=scenario, P2PSTREAM_QUALIFICATION_PROTOCOL=protocol,
                        P2PSTREAM_QUALIFICATION_CONCURRENCY=str(concurrency), P2PSTREAM_QUALIFICATION_REQUESTS=str(rate['requests'] if rate else concurrency),
                        P2PSTREAM_QUALIFICATION_OFFERED_RPS=str(rate['offered_rps'] if rate else 0),
                        P2PSTREAM_QUALIFICATION_DURATION_SECONDS='0' if rate else str(args.duration_seconds),
                        P2PSTREAM_QUALIFICATION_WARMUP_SECONDS='0' if rate else str(args.warmup_seconds), P2PSTREAM_QUALIFICATION_LABEL=label,
                        P2PSTREAM_QUALIFICATION_PATH='direct' if kind == 'candidate' else 'direct-control',
                        P2PSTREAM_QUALIFICATION_PIDS=sample_pid, P2PSTREAM_QUALIFICATION_ADMIN_URL=admin_url,
                        P2PSTREAM_QUALIFICATION_PROXY_CPU_SET=proxy_cpus, P2PSTREAM_QUALIFICATION_LOAD_CPU_SET=load_cpus,
                        P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE=envelope, P2PSTREAM_QUALIFICATION_SEQUENCE=str(sequence),
                        P2PSTREAM_QUALIFICATION_BALLAST_MIB='0', P2PSTREAM_QUALIFICATION_RESULT=str(result_file))
                    if protocol == 'h2' and args.matched_h2_single_connection:
                        load_env['P2PSTREAM_QUALIFICATION_H2_PREWARM'] = '1'
                    load_started_utc = datetime.now(timezone.utc).isoformat()
                    with open(str(result_file) + '.log', 'w') as log:
                        subprocess.run(['taskset', '-c', load_cpus, str(driver), '-test.run', '^TestProxyQualificationLoad$', '-test.timeout', '2m'],
                                       check=True, env=load_env, stdout=log, stderr=subprocess.STDOUT, timeout=125)
                    result = json.loads(result_file.read_text())
                    result.update(iteration=iteration, sequence=sequence, proxy_kind=kind,
                                  load_process_started_utc=load_started_utc, load_process_finished_utc=datetime.now(timezone.utc).isoformat())
                    if protocol == 'h2' and args.matched_h2_single_connection:
                        result['h2_connection_contract'] = 'single-prewarmed-client-connection'
                    if rate:
                        result['moderate_load_plan'] = rate
                    with open(results_path, 'a') as combined:
                        combined.write(json.dumps(result, separators=(',', ':')) + '\n')
                    result_rows.append(result)
                    print(f'{label} c{concurrency} {protocol}: {result["useful_requests_per_second"]:.1f} useful/s, errors={result["errors"]}', flush=True)
                    if rate:
                        try:
                            validate_moderate_result(result, rate)
                        except (ValueError, TypeError, KeyError, OverflowError) as error:
                            raise RuntimeError(f'moderate-load validation failed: {result_file}: {error}') from error
                    expected_major = 1 if protocol == 'h1' else 2
                    if result['errors'] or result['useful_completed'] != result['attempted'] or result.get('observed_http_major') != expected_major:
                        raise RuntimeError(f'payload/status/protocol qualification failed: {result_file}')
                    if protocol == 'h2' and args.matched_h2_single_connection:
                        if (result.get('h2_prewarm_requests') != 1 or result.get('client_tcp_connections') != 1
                                or not result.get('h2_prewarm_connection_reused')
                                or result.get('h2_prewarm_additional_connection', False)):
                            raise RuntimeError(f'matched H2 single-connection contract failed: {result_file}')
                        if kind == 'candidate' and result.get('h2_prewarm_persisted_events') != 1:
                            raise RuntimeError(f'candidate H2 prewarm event conservation failed: {result_file}')
                    if kind == 'candidate' and (not result.get('event_stats_captured') or result.get('persisted_event_delta') != result['attempted']
                            or result.get('dropped_event_delta', 0) or result.get('event_queue_after_drain', 0)
                            or result.get('event_drain_error') or result.get('event_stats_error')):
                        raise RuntimeError(f'candidate event conservation failed: {result_file}')
            if kind in ['caddy', 'candidate']:
                subprocess.run(['docker', 'rm', '-f', name], check=True, stdout=subprocess.DEVNULL)
            stop(proxy)
    if moderate_plan:
        cells = []
        for protocol in ('h1', 'h2'):
            for concurrency in (16, 128):
                medians = {}
                for kind in ('candidate', 'haproxy'):
                    selected = [row for row in result_rows if row['protocol'] == protocol
                                and row['concurrency'] == concurrency and row['proxy_kind'] == kind]
                    if len(selected) != 3:
                        raise RuntimeError('incomplete moderate-load latency matrix')
                    medians[kind] = {field: statistics.median(row[field] for row in selected)
                                     for field in ('p95_ms', 'p99_ms')}
                ratios = {field: medians['candidate'][field]/medians['haproxy'][field]
                          for field in ('p95_ms', 'p99_ms')}
                cells.append(dict(protocol=protocol, concurrency=concurrency, medians=medians,
                                  candidate_to_haproxy=ratios, passed=all(value <= 1.25 for value in ratios.values())))
        summary = dict(scope=moderate_plan['scope'], row_count=len(result_rows), cells=cells,
                       all_delivery_checks_passed=True, exact_payload_and_event_conservation=True,
                       all_tail_gates_passed=all(cell['passed'] for cell in cells))
        (run_dir / 'moderate-load.summary.json').write_text(json.dumps(summary, indent=2) + '\n')
        if not summary['all_tail_gates_passed']:
            raise RuntimeError('moderate-load p95/p99 qualification failed; complete results preserved')
finally:
    origin_stop.touch(exist_ok=True)
    for name in containers:
        subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for process in reversed(processes):
        stop(process)
    for log in logs:
        log.close()
PY
