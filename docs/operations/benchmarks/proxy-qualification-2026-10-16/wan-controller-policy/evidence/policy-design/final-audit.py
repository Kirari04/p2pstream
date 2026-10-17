#!/usr/bin/env python3
"""Read-only post-run closure audit; never rerun or reclassify a benchmark."""
from pathlib import Path
import hashlib
import json
import os
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / 'policy-design'
RUN = ROOT / 'runner-source/integration/runs/controller-crc-policy-once'
EXPECTED_LEDGER = 'a3a9e60a6dc2bed3dd2ec679cde5923b5fb848647e8f19b862f9449b9856022d'
EXPECTED_RECEIPT = 'd5115080339eea5a1881376c2d936b70122099be576a5c18866ef98c754d31bb'
ORDER = ('cubic', 'reno', 'cubic')
SCHEDULE = ([('warmup', 'download')] * 4 + [('warmup', 'upload')] * 4
            + [('retained-warmup', 'download'), ('retained-warmup', 'upload'),
               ('steady', 'download'), ('steady', 'upload'), ('steady', 'upload'),
               ('steady', 'download'), ('steady', 'download'), ('steady', 'upload')])


def load(path):
    return json.loads(path.read_text())


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def jsonl(path):
    return [json.loads(line) for line in path.read_text().splitlines()]


def command(argv):
    result = subprocess.run(argv, capture_output=True, text=True, timeout=10)
    return {'status': result.returncode, 'stdout': result.stdout, 'stderr': result.stderr}


def main():
    audit_path = OUT / 'FINAL-AUDIT.json'
    ledger_path = OUT / 'FINAL-SHA256SUMS'
    if audit_path.exists() or ledger_path.exists():
        raise SystemExit('fresh final audit and ledger required; no overwrite')
    issues = []
    changed = []

    def check(value, message):
        if not value:
            issues.append(message)

    def unchanged(path, digest):
        if not path.is_file() or path.is_symlink() or sha(path) != digest:
            changed.append(str(path))

    source = load(OUT / 'SOURCE.json')
    check(sha(OUT / 'SHA256SUMS') == EXPECTED_LEDGER, 'reviewed policy ledger differs')
    check(len(source['initial_existing_file_sha256']) == 1039, 'initial source inventory cardinality differs')
    check(len(source['files']) == 6, 'policy source inventory cardinality differs')
    for name, digest in source['initial_existing_file_sha256'].items():
        unchanged(Path(name), digest)
    for entry in source['files']:
        unchanged(Path(entry['path']), entry['sha256'])
    for entry in load(ROOT / 'FROZEN-INPUTS.json'):
        unchanged(Path(entry['path']), entry['sha256'])
    for entry in load(ROOT / 'BUILD-TEST-PLAN.json')['inputs']:
        unchanged(Path(entry['path']), entry['sha256'])
    ledgers = ['SHA256SUMS', 'build/runtime-SHA256SUMS',
               'build/environment-recovery/RUNTIME-SHA256SUMS',
               'build/joined-ps/FINAL-SHA256SUMS', 'vector-design/SHA256SUMS',
               'policy-design/SHA256SUMS']
    for name in ledgers:
        for line in (ROOT / name).read_text().splitlines():
            digest, relative = line.split('  ', 1)
            unchanged(ROOT / relative, digest)
    check(sha(ROOT / 'build/receipt.json') == EXPECTED_RECEIPT,
          'accepted build receipt differs')
    check((ROOT / 'REVIEWED-RECEIPT-SHA256').read_text().strip() == EXPECTED_RECEIPT,
          'reviewed receipt selector differs')
    for entry in load(ROOT / 'build/receipt.json')['binaries'].values():
        path = Path(entry['path'])
        unchanged(path, entry['sha256'])
        check(path.stat().st_size == entry['size'], 'accepted binary size differs: ' + str(path))
    check(not changed, 'frozen input content changed')

    screen = load(RUN / 'screen-final.json')
    decision = load(RUN / 'decision.json')
    check(screen.get('completed_arms') == 3 and screen.get('comparison_incomplete') is False
          and screen.get('stopped') is False, 'three-arm comparison did not complete')
    check(type(decision.get('diagnostic_positive')) is bool
          and decision.get('accepted') is decision.get('diagnostic_positive')
          and screen.get('accepted') is decision.get('accepted')
          and screen.get('diagnostic_positive') is decision.get('diagnostic_positive'),
          'saved comparison/final diagnostic results disagree')
    check(decision.get('original_qualification_cleared') is False
          and screen.get('original_qualification_cleared') is False,
          'diagnostic incorrectly claims qualification clearance')
    criteria = decision.get('criteria', {})
    check(type(criteria) is dict and bool(criteria)
          and all(type(value) is bool for value in criteria.values())
          and decision.get('diagnostic_positive') is all(criteria.values()),
          'saved diagnostic criterion conjunction differs')
    failed = decision.get('failed_criteria')
    check(type(failed) is list and all(type(value) is str for value in failed)
          and len(failed) == len(set(failed))
          and set(failed) == {key for key, value in criteria.items() if not value},
          'saved failed criteria differ')
    check(type(decision.get('arms')) is list and len(decision['arms']) == 3,
          'saved comparison arm vector differs')
    outer = [dict(path=str(path), **load(path)) for path in sorted(RUN.glob('*.outer.json'))]
    expected_outer = {f'arm-{index}.outer.json' for index in range(3)} | {
        f'arm-{index}-validation.outer.json' for index in range(3)} | {'screen-validation.outer.json'}
    check({Path(value['path']).name for value in outer} == expected_outer,
          'outer command audit set differs')
    check(all(value.get('forced') is False and value.get('remaining_owned') == []
              and value.get('adopted_descendants') == [] and value.get('accepted_cleanup') is True
              and value.get('failure_reason') == '' and type(value.get('status')) is int
              and value['status'] in (0, 1) for value in outer),
          'outer command force/adoption/remnant/status audit failed')
    identities = {(entry['pid'], entry['start_ticks']) for value in outer
                  for entry in value['owned_identities']}
    namespaces = {kind: set() for kind in ('user', 'mnt', 'net', 'pid')}
    arms = []
    for index, controller in enumerate(ORDER):
        arm = RUN / f'arm-{index}-{controller}'
        lifecycle = jsonl(arm / 'results.lifecycle.jsonl')
        rows = jsonl(arm / 'results.jsonl')
        att = load(arm / 'attestation.json')
        independent = load(arm / 'independent-controller-attestation.json')
        arm_decision = load(RUN / f'arm-{index}.decision.json')
        supervisor = load(arm / 'supervisor-lifecycle.json')
        exits = [value for value in lifecycle if value.get('event') == 'process-exit']
        check(len(rows) == 16 and [row['diagnostic_sequence'] for row in rows] == list(range(1, 17))
              and [(row['measurement_stage'], row['transfer_direction']) for row in rows] == SCHEDULE,
              f'arm {index} exact16 transfer schedule differs')
        check(len(exits) == 5 and all(type(value.get('status')) is int and value['status'] == 0
                  and value.get('forced') is False and value.get('group_joined') is True for value in exits),
              f'arm {index} five natural endpoint/helper exits failed')
        pre_post = load(arm / 'pre-hashes.json') == load(arm / 'post-hashes.json')
        check(pre_post and att == independent, f'arm {index} pre/post hashes or independent attestation differs')
        check(arm_decision == decision['arms'][index]
              and arm_decision['mature_attestation'] == att
              and arm_decision['arm_index'] == index and arm_decision['controller'] == controller
              and arm_decision['eligible_for_diagnostic'] is True,
              f'arm {index} comparison decision/mature evidence differs')
        mature_status = 0 if att['accepted'] else 1
        arm_outer = next(value for value in outer if Path(value['path']).name == f'arm-{index}.outer.json')
        validation_outer = next(value for value in outer
                                if Path(value['path']).name == f'arm-{index}-validation.outer.json')
        check(arm_outer['status'] == mature_status and supervisor['runner_status'] == mature_status
              and int((arm / 'validation.status').read_text()) == mature_status
              and validation_outer['status'] == 0,
              f'arm {index} saved mature/outer/validator statuses disagree')
        docs = [load(arm / 'outer-namespaces.json')['owned'],
                load(arm / 'server-scope/runner-owned-namespaces.json')['namespaces']]
        arm_namespaces = {kind: {doc[kind] for doc in docs} for kind in namespaces}
        for value in lifecycle:
            if value.get('event') == 'namespace-helpers-start':
                for helper in ('agent', 'router'):
                    arm_namespaces['net'].add(value[helper]['namespace'])
        for kind in namespaces:
            namespaces[kind].update(arm_namespaces[kind])
        invocation = dict(line.split('=', 1) for line in
                          (arm / 'server-scope-invocation.txt').read_text().splitlines())
        cgroup = Path('/sys/fs/cgroup') / invocation['scope_cgroup'].lstrip('/')
        unit = command(['systemctl', '--user', 'show', invocation['unit'],
                        '--property=LoadState,ActiveState,SubState'])
        unit_fields = dict(line.split('=', 1) for line in unit['stdout'].splitlines() if '=' in line)
        unit_closed = ((unit['status'] == 0 and unit_fields.get('ActiveState') == 'inactive')
                       or (unit_fields.get('LoadState') == 'not-found'
                           and unit_fields.get('ActiveState') in (None, 'inactive')))
        check(not cgroup.exists() and unit_closed, f'arm {index} cgroup/systemd unit remains or query is inconclusive')
        arms.append({'index': index, 'controller': controller, 'completed_transfer_rows': len(rows),
                     'pre_post_hashes_equal': pre_post, 'independent_attestation_exact_match': att == independent,
                     'attestation_sha256': sha(arm / 'attestation.json'),
                     'independent_attestation_sha256': sha(arm / 'independent-controller-attestation.json'),
                     'attestation_path': str(arm / 'attestation.json'),
                     'independent_attestation_path': str(arm / 'independent-controller-attestation.json'),
                     'mature_outcome': {key: att.get(key) for key in
                         ('accepted', 'integrity_issues', 'conditioning_issues', 'performance_issues')},
                     'arm_decision_path': str(RUN / f'arm-{index}.decision.json'),
                     'arm_decision_sha256': sha(RUN / f'arm-{index}.decision.json'),
                     'arm_statistics': {key: arm_decision.get(key) for key in
                         ('pooled_bytes_per_second', 'aggregate_upstream_read_ns',
                          'aggregate_active_transfer_rates', 'total_measured_wall_seconds',
                          'original_download_floor_outcomes', 'revisit_outcome_counts',
                          'origin_outcome_counts', 'retained_downloads')},
                     'all16_metrics': [{key: row[key] for key in
                         ('diagnostic_sequence', 'transfer_direction', 'useful_transfer_bytes_per_second',
                          'wall_seconds', 'response_bytes', 'errors', 'status_codes', 'persisted_event_delta')}
                         for row in rows], 'natural_endpoint_and_helper_exits': exits,
                     'mature_supervisor_lifecycle_path': str(arm / 'supervisor-lifecycle.json'),
                     'mature_supervisor_lifecycle_sha256': sha(arm / 'supervisor-lifecycle.json'),
                     'private_namespace_ids': {kind: sorted(values) for kind, values in arm_namespaces.items()},
                     'scope_cgroup': str(cgroup), 'scope_cgroup_exists': cgroup.exists(),
                     'scope_unit_query': unit, 'scope_unit_closed': unit_closed})
    final_outer = next(value for value in outer if Path(value['path']).name == 'screen-validation.outer.json')
    check(final_outer['status'] == (0 if decision['diagnostic_positive'] else 1),
          'final comparison process status differs from saved diagnostic')
    remaining = []
    namespace_remaining = []
    unreadable = 0
    for path in Path('/proc').iterdir():
        if not path.name.isdigit():
            continue
        try:
            text = (path / 'stat').read_text()
            fields = text[text.rfind(')') + 2:].split()
            identity = (int(path.name), int(fields[19]))
            if identity in identities:
                remaining.append({'pid': identity[0], 'start_ticks': identity[1]})
        except (OSError, ValueError, IndexError):
            pass
        for kind, values in namespaces.items():
            try:
                value = os.readlink(path / 'ns' / kind)
                if value in values:
                    namespace_remaining.append({'pid': int(path.name), 'kind': kind, 'namespace': value})
            except OSError:
                unreadable += 1
    netns = command(['ip', 'netns', 'list'])
    tracked = command(['git', 'status', '--porcelain', '--untracked-files=no'])
    check(not remaining and not namespace_remaining, 'retained PID/start identity or private namespace still live')
    check(netns['status'] == 0 and not netns['stdout'].strip(), 'named network namespaces remain or query failed')
    check(tracked['status'] == 0 and not tracked['stdout'].strip(), 'tracked working tree changed or status query failed')
    for arm in arms:
        prefix = f"arm-{arm['index']}"
        arm_identities = {(entry['pid'], entry['start_ticks']) for value in outer
                          if Path(value['path']).name in (prefix + '.outer.json', prefix + '-validation.outer.json')
                          for entry in value['owned_identities']}
        arm['remaining_pid_start_identities'] = [entry for entry in remaining
            if (entry['pid'], entry['start_ticks']) in arm_identities]
        arm['remaining_private_namespace_processes'] = [entry for entry in namespace_remaining
            if entry['namespace'] in arm['private_namespace_ids'][entry['kind']]]
        arm['no_live_remnants'] = (not arm['remaining_pid_start_identities']
                                   and not arm['remaining_private_namespace_processes']
                                   and not arm['scope_cgroup_exists'] and arm['scope_unit_closed'])
    outcome = ('complete positive policy diagnostic' if decision['diagnostic_positive']
               else 'complete negative policy diagnostic; prospective criteria not all satisfied')
    audit = {'schema_version': 1, 'audit_accepted': not issues,
             'status': outcome if not issues else 'post-run audit rejected; saved diagnostic preserved',
             'issues': issues, 'finished_unix': time.time(), 'screen_final': screen,
             'decision_path': str(RUN / 'decision.json'), 'decision_sha256': sha(RUN / 'decision.json'),
             'decision_summary': {key: decision.get(key) for key in
                 ('accepted', 'diagnostic_positive', 'original_qualification_cleared', 'criteria',
                  'failed_criteria', 'pooled_bytes_per_second', 'aggregate_upstream_read_ns',
                  'candidate_pooled_ratio_to_faster_control', 'candidate_pooled_ratios_to_each_control',
                  'control_pooled_ratio_0_to_2', 'candidate_rate_ratios_to_faster_corresponding_control',
                  'rate_endpoint', 'interpretation')},
             'source_sha256': sha(OUT / 'SOURCE.json'), 'plan_sha256': sha(OUT / 'PLAN.json'),
             'runtime_grant_sha256': sha(OUT / 'RUNTIME-GRANT.json'),
             'build_receipt_sha256': sha(ROOT / 'build/receipt.json'),
             'input_changes': sorted(set(changed)),
             'initial_existing_files_checked': len(source['initial_existing_file_sha256']),
             'new_source_entries_checked': len(source['files']),
             'prior_ledgers': [{'path': name, 'sha256': sha(ROOT / name)} for name in ledgers],
             'arms': arms, 'completed_transfer_rows': sum(arm['completed_transfer_rows'] for arm in arms),
             'unlaunched_arms': [], 'outer_commands': [{key: value.get(key) for key in
                 ('path', 'status', 'forced', 'accepted_cleanup', 'remaining_owned', 'adopted_descendants',
                  'failure_reason', 'elapsed_seconds')} for value in outer],
             'remaining_pid_start_identities': remaining,
             'private_namespace_ids': {kind: sorted(values) for kind, values in namespaces.items()},
             'remaining_private_namespace_processes': namespace_remaining,
             'unavailable_proc_namespace_reads': unreadable, 'named_netns_query': netns,
             'tracked_status_query': tracked, 'benchmark_reexecution': False,
             'no_additional_test_build_or_traffic': True}
    with audit_path.open('x') as stream:
        json.dump(audit, stream, indent=2, sort_keys=True)
        stream.write('\n')
    files = sorted(path for tree in (OUT, RUN) for path in tree.rglob('*')
                   if path.is_file() and path != ledger_path)
    with ledger_path.open('x') as stream:
        for path in files:
            stream.write(sha(path) + '  ' + str(path.relative_to(ROOT)) + '\n')
    print(json.dumps({'audit_accepted': not issues, 'audit_sha256': sha(audit_path),
                      'ledger_sha256': sha(ledger_path), 'files': len(files),
                      'completed_rows': audit['completed_transfer_rows'],
                      'diagnostic_positive': decision['diagnostic_positive'], 'status': audit['status'],
                      'issues': issues}))
    return 0 if not issues else 1


if __name__ == '__main__':
    raise SystemExit(main())
