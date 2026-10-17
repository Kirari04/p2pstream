#!/usr/bin/env python3
"""Read-only closure audit of the stopped32-row run; preserves strict rejection."""
from pathlib import Path
import hashlib
import json
import os
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1] / 'wan-early-share-screen-20261017'
OLD = ROOT.parent / 'wan-controller-screen-20261017'
RUN = ROOT / 'runs/early-share-aba-once'
ORDER = (0, 1, 0)
RECEIPT = 'd5115080339eea5a1881376c2d936b70122099be576a5c18866ef98c754d31bb'
SCHEDULE = ([('warmup', 'download')] * 4 + [('warmup', 'upload')] * 4
            + [('retained-warmup', 'download'), ('retained-warmup', 'upload'),
               ('steady', 'download'), ('steady', 'upload'), ('steady', 'upload'),
               ('steady', 'download'), ('steady', 'download'), ('steady', 'upload')])


def load(path):
    return json.loads(path.read_text())


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def reference(path):
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}


def command(argv):
    result = subprocess.run(argv, capture_output=True, text=True, timeout=10)
    return {'status': result.returncode, 'stdout': result.stdout, 'stderr': result.stderr}


def same_namespace(a, b):
    return all(a[key] == b[key] for key in ('namespace', 'namespace_device', 'namespace_inode'))


def same_process(a, b):
    return a['pid'] == b['pid'] and a['start_ticks'] == b['start_ticks'] and same_namespace(a, b)


def main():
    output = ROOT / 'INCOMPLETE-AUDIT.json'
    ledger = ROOT / 'INCOMPLETE-SHA256SUMS'
    if output.exists() or ledger.exists():
        raise SystemExit('fresh final audit and ledger required; no overwrite')
    issues = []
    changed = []

    def check(value, message):
        if not value:
            issues.append(message)

    def unchanged(path, digest, size=None):
        if (not path.is_file() or path.is_symlink() or sha(path) != digest
                or (size is not None and path.stat().st_size != size)):
            changed.append(str(path))

    def optional(path):
        if not path.is_file():
            return None
        try:
            return load(path)
        except (OSError, ValueError) as error:
            issues.append('unreadable JSON ' + str(path) + ': ' + str(error))
            return None

    source_bytes_before_audit = sum(path.stat().st_size for path in ROOT.rglob('*') if path.is_file())
    dependency_root = ROOT.parent/'wan-early-share-dependencies-20261017'
    dependency_bytes = sum(path.stat().st_size for path in dependency_root.rglob('*') if path.is_file())
    check(source_bytes_before_audit <= (256<<20)-(1<<20) and dependency_bytes <= 4<<30,
          'source/runtime or shared dependency/cache/temp/binary artifact threshold exceeded')
    source = load(ROOT / 'SOURCE.json')
    frozen = load(OLD / 'FROZEN-INPUTS.json')
    grant = load(ROOT / 'RUNTIME-GRANT.json')
    check(grant == {'source_manifest_sha256': sha(ROOT / 'SOURCE.json'),
                    'runtime_approved': True, 'maximum_arms': 3, 'maximum_transfers': 48,
                    'early_share_order': list(ORDER), 'idle_policy_order': [1,1,1], 'controller_order': ['cubic'] * 3},
          'runtime grant differs from exact frozen source and prospective order')
    check(len(source['files']) == source['file_count'] and len(frozen) == 540,
          'new/original frozen input cardinalities differ')
    historical = load(OLD/'policy-design/SOURCE.json')
    for raw, digest in historical['initial_existing_file_sha256'].items():
        unchanged(Path(raw), digest)
    for entry in historical['files']:
        unchanged(Path(entry['path']), entry['sha256'])
    for entry in source['files']:
        unchanged(Path(entry['path']), entry['sha256'], entry.get('bytes'))
    for entry in frozen:
        unchanged(Path(entry['path']), entry['sha256'])
    check(sha(OLD / 'build/receipt.json') == RECEIPT
          and (OLD / 'REVIEWED-RECEIPT-SHA256').read_text().strip() == RECEIPT,
          'accepted build receipt or selector differs')
    for entry in load(OLD / 'build/receipt.json')['binaries'].values():
        unchanged(Path(entry['path']), entry['sha256'], entry['size'])
    fresh_receipt = load(ROOT/'build-continuation-1/receipt.json')
    fresh_digest = (ROOT/'REVIEWED-RECEIPT-SHA256').read_text().strip()
    check(sha(ROOT/'build-continuation-1/receipt.json') == fresh_digest == source['build_receipt_sha256']
          and fresh_receipt['accepted'] is True and fresh_receipt['source_before_after_unchanged'] is True,
          'fresh derivative build/receipt token differs')
    for entry in fresh_receipt['binaries'].values():
        unchanged(Path(entry['path']), entry['sha256'], entry['size'])
    check(not changed, 'frozen source/reference/binary input changed')

    screen = load(RUN / 'screen-final.json')
    decision = optional(RUN / 'decision.json')
    complete = screen.get('comparison_incomplete') is False
    check(not complete and screen.get('accepted') is False and screen.get('diagnostic_positive') is False
          and screen.get('stopped') is True and screen.get('completed_arms') == 1
          and [value.get('state') for value in screen.get('arm_states', [])] == ['measurement-valid','invalid','unstarted']
          and not (RUN/'decision.json').exists() and not (RUN/'arm-1.decision.json').exists(),
          'saved32-row stop state or absent comparison/invalid-arm decision differs')
    check(screen.get('original_qualification_cleared') is False,
          'saved screen incorrectly claims original qualification clearance')
    check(type(screen.get('artifact_bytes')) is int and screen['artifact_bytes'] <= (256<<20)-(1<<20)
          and screen.get('artifact_rejection_threshold_bytes') == 256<<20, 'saved screen artifact bound differs')
    check(type(screen.get('comparison_incomplete')) is bool
          and type(screen.get('diagnostic_positive')) is bool,
          'saved final screen state malformed')
    if complete:
        check(screen.get('completed_arms') == 3 and screen.get('stopped') is False
              and decision is not None, 'saved complete comparison lacks three arms/result')
        if decision is not None:
            criteria = decision.get('criteria', {})
            failed = decision.get('failed_criteria')
            check(type(criteria) is dict and set(criteria) == {'candidate_all_original_download_floors', 'candidate_mature_performance_passed', 'pooled_rate_at_least_108pct_faster_control', 'aggregate_read_strictly_below_both_controls', 'each_retained_rate_at_least_95pct_faster_control'}
                  and all(type(value) is bool for value in criteria.values())
                  and type(decision.get('diagnostic_positive')) is bool
                  and decision['diagnostic_positive'] is all(criteria.values()),
                  'saved final criterion conjunction malformed')
            check(type(failed) is list and all(type(value) is str for value in failed)
                  and len(failed) == len(set(failed))
                  and set(failed) == {key for key, value in criteria.items() if not value},
                  'saved failed criterion set differs')
            check(decision.get('accepted') is decision.get('diagnostic_positive')
                  and screen.get('accepted') is decision.get('accepted')
                  and screen.get('diagnostic_positive') is decision.get('diagnostic_positive')
                  and decision.get('original_qualification_cleared') is False
                  and decision.get('early_share_order') == list(ORDER)
                  and decision.get('idle_policy_order') == [1,1,1]
                  and decision.get('controller_order') == ['cubic'] * 3
                  and type(decision.get('arms')) is list and len(decision['arms']) == 3,
                  'saved screen/comparison/order results disagree')
    else:
        check(screen.get('accepted') is False and screen.get('diagnostic_positive') is False,
              'incomplete screen reports a positive result')

    outer = [dict(path=str(path), **load(path)) for path in sorted(RUN.glob('*.outer.json'))]
    check(all(value.get('forced') is False and value.get('remaining_owned') == []
              and value.get('adopted_descendants') == [] and value.get('accepted_cleanup') is True
              and value.get('failure_reason') == '' and type(value.get('status')) is int
              and value['status'] in (0, 1) for value in outer),
          'outer command force/adoption/remnant/status audit failed')
    check({Path(value['path']).name for value in outer} == {f'arm-{i}{suffix}.outer.json' for i in (0,1) for suffix in ('','-validation')},
          'incomplete run must retain exactly four outer commands')
    expected_statuses = {'arm-0.outer.json':1,'arm-0-validation.outer.json':0,'arm-1.outer.json':1,'arm-1-validation.outer.json':1}
    check(all(value['status'] == expected_statuses.get(Path(value['path']).name) for value in outer),
          'saved incomplete run command statuses differ')
    if complete:
        expected_outer = {f'arm-{index}.outer.json' for index in range(3)} | {
            f'arm-{index}-validation.outer.json' for index in range(3)} | {'screen-validation.outer.json'}
        check({Path(value['path']).name for value in outer} == expected_outer,
              'complete comparison outer audit set differs')
    identities = {(entry['pid'], entry['start_ticks']) for value in outer
                  for entry in value['owned_identities']}
    namespaces = {kind: set() for kind in ('user', 'mnt', 'net', 'pid')}
    arms = []
    for index, early_policy in enumerate(ORDER):
        policy = 1
        arm = RUN / f"arm-{index}-share-{'on' if early_policy else 'off'}"
        state = next((value for value in screen.get('arm_states', []) if value.get('index') == index), None)
        summary = {'index': index, 'early_share_policy': bool(early_policy), 'idle_policy': policy, 'controller': 'cubic', 'saved_state': state}
        arms.append(summary)
        if not arm.is_dir():
            check(not complete and (state is None or state.get('state') == 'unstarted'),
                  f'arm {index} missing output for a started/complete arm')
            summary['output_present'] = False
            continue
        summary['output_present'] = True
        try:
            lifecycle = [json.loads(line) for line in (arm / 'results.lifecycle.jsonl').read_text().splitlines()]
            rows = [json.loads(line) for line in (arm / 'results.jsonl').read_text().splitlines()]
            summary['completed_transfer_rows'] = len(rows)
            att = load(arm / 'attestation.json')
            independent = load(arm / 'independent-controller-attestation.json')
            saved_arm_decision = optional(RUN / f'arm-{index}.decision.json')
            arm_decision = saved_arm_decision or {}
            check((saved_arm_decision is not None) == (index == 0), f'arm {index} saved decision presence differs')
            supervisor = load(arm / 'supervisor-lifecycle.json')
            exits = [value for value in lifecycle if value.get('event') == 'process-exit']
            check(len(rows) == 16 and [row['diagnostic_sequence'] for row in rows] == list(range(1, 17))
                  and [(row['measurement_stage'], row['transfer_direction']) for row in rows] == SCHEDULE,
                  f'arm {index} exact16 transfer schedule differs')
            check(all(row['configured_payload_bytes'] == 268435456 and row['errors'] == 0
                      and row['persisted_event_delta'] == 1 for row in rows),
                  f'arm {index} payload/error/event outcome differs')
            check(len(exits) == 5 and all(type(value.get('status')) is int and value['status'] == 0
                      and value.get('forced') is False and value.get('group_joined') is True for value in exits),
                  f'arm {index} five natural endpoint/helper exits failed')
            pre_post = load(arm / 'pre-hashes.json') == load(arm / 'post-hashes.json')
            check(pre_post and att == independent, f'arm {index} hashes/independent mature attestation differs')
            if saved_arm_decision is not None:
                check(arm_decision['mature_attestation'] == att and arm_decision['arm_index'] == index
                      and arm_decision['controller'] == 'cubic' and arm_decision['idle_policy'] == policy
                      and arm_decision['eligible_for_diagnostic'] is True
                      and arm_decision['early_share_policy'] is bool(early_policy),
                      f'arm {index} saved decision/mature/treatment binding differs')
            import importlib.util
            proof_spec = importlib.util.spec_from_file_location('early_share_final_proof', ROOT/'validate-early-share-proof.py')
            proof_module = importlib.util.module_from_spec(proof_spec)
            proof_spec.loader.exec_module(proof_module)
            independent_share = proof_module.validate(arm,index)
            if saved_arm_decision is not None:
                check(independent_share == arm_decision['early_share_proof'], f'arm {index} independent early-share sidecar proof differs')
            if decision is not None:
                check(arm_decision == decision['arms'][index], f'arm {index} comparison decision differs')
            mature_status = 0 if att['accepted'] else 1
            arm_outer = next(value for value in outer if Path(value['path']).name == f'arm-{index}.outer.json')
            validation_outer = next(value for value in outer
                                    if Path(value['path']).name == f'arm-{index}-validation.outer.json')
            check(arm_outer['status'] == supervisor['runner_status'] == mature_status
                  and int((arm / 'validation.status').read_text()) == mature_status
                  and validation_outer['status'] == (0 if index == 0 else 1),
                  f'arm {index} mature/outer/validator statuses disagree')
            proof_names = ('idle-configured.json', 'idle-endpoints-endpoint-binding.json',
                           'idle-endpoints-post-workload.json', 'idle-host-pre.json', 'idle-host-post.json')
            config, pre, post, hp_doc, hz_doc = [load(arm / name) for name in proof_names]
            hp, hz = hp_doc['sample'], hz_doc['sample']
            runner, target, router = config['runner_pre'], config['target_after_set'], config['router_pre']
            if saved_arm_decision is not None:
                check(arm_decision['idle_namespace_proof'] == {
                    'accepted': True, 'arm_index': index, 'expected_value': policy,
                    'load_scope_observed': False, 'point_in_time_only': True},
                    f'arm {index} saved canonical scope proof summary differs')
            check(config['arm_index'] == pre['arm_index'] == post['arm_index'] == index
                  and config['expected_value'] == pre['expected_value'] == post['expected_value'] == policy
                  and config['role_map'] == {'agent': 'target', 'origin': 'target', 'server': 'runner', 'load': 'runner'}
                  and config['host_pre'] == hp and same_process(hp, hz) and hp['value'] == hz['value']
                  and hp_doc['phase'] == 'host-pre' and hz_doc['phase'] == 'host-post',
                  f'arm {index} canonical configured/host pre/post scope differs')
            check(all(type(value) is int for value in
                      (config['expected_value'], pre['expected_value'], post['expected_value'],
                       hp['value'], hz['value'], runner['value'], target['value'], router['value'])),
                  f'arm {index} canonical scope values are not exact integers')
            check(hp['monotonic_ns'] <= config['configured_monotonic_ns'] < pre['monotonic_ns']
                  < post['monotonic_ns'] <= hz['monotonic_ns'],
                  f'arm {index} host/configured/endpoint proof ordering differs')
            check(all(not same_namespace(target, protected) for protected in (hp, runner, router)),
                  f'arm {index} target aliases protected namespace')
            check(all(config[key]['value'] == policy and same_namespace(config[key], target)
                      for key in ('setter', 'target_after_set', 'target_independent_readback')),
                  f'arm {index} target setter/independent readbacks differ')
            check(config['target_independent_readback']['resolution'] == 'namespace-file'
                  and same_process(runner, config['runner_after_set'])
                  and runner['value'] == config['runner_after_set']['value']
                  and router['value'] == config['router_after_set']['value'],
                  f'arm {index} protected runner/router changed during setting')
            for endpoint in ('agent', 'origin', 'server'):
                before, after = pre['roles'][endpoint], post['roles'][endpoint]
                expected_namespace = runner if endpoint == 'server' else target
                expected_value = runner['value'] if endpoint == 'server' else policy
                check(same_process(before['subject'], after['subject'])
                      and same_namespace(before, expected_namespace) and same_namespace(after, expected_namespace)
                      and before['value'] == after['value'] == expected_value
                      and pre['ready_bindings'][endpoint] == post['ready_bindings'][endpoint],
                      f'arm {index} actual endpoint {endpoint} pre/post policy/identity differs')
                # Endpoint proof PIDs belong to their private PID namespace.
                # Host /proc identities come from the outer supervisor only;
                # private namespace scans independently cover inner endpoints.
            check(all(same_process(record['runner'], runner) and record['runner']['value'] == runner['value']
                      and same_process(record['router']['subject'], router['subject'])
                      and record['router']['value'] == router['value']
                      and same_namespace(record['target_helper'], target)
                      and record['target_helper']['value'] == policy for record in (pre, post)),
                  f'arm {index} canonical protected/helper pre/post proof differs')
            terminal = load(arm / 'results.sender-wait-controls/final-raw-write-terminal.json')
            cached_values = [terminal['tcp_slow_start_after_idle_setup'],
                             terminal['tcp_slow_start_after_idle_terminal']]
            raw_owners = [value['raw_write_activity'] for value in terminal['records']]
            for path in sorted((arm / 'results.sender-wait-controls').glob('*-sender.json')):
                raw_owners.extend(value['raw_write_activity']
                                  for value in load(path)['response']['send_counters'])
            cached_values.extend(value['tcp_state']['tcp_slow_start_after_idle'] for value in raw_owners)
            check(len(terminal['records']) == 5 and terminal['tcp_slow_start_after_idle_read_error'] is False
                  and all(type(value) is int and value == policy for value in cached_values),
                  f'arm {index} actual setup/terminal/cached-owner intended policy differs')
            docs = [load(arm / 'outer-namespaces.json')['owned'],
                    load(arm / 'server-scope/runner-owned-namespaces.json')['namespaces']]
            private = {kind: {doc[kind] for doc in docs} for kind in namespaces}
            for value in lifecycle:
                if value.get('event') == 'namespace-helpers-start':
                    for helper in ('agent', 'router'):
                        private['net'].add(value[helper]['namespace'])
            for kind in namespaces:
                namespaces[kind].update(private[kind])
            invocation = dict(line.split('=', 1) for line in
                              (arm / 'server-scope-invocation.txt').read_text().splitlines())
            cgroup = Path('/sys/fs/cgroup') / invocation['scope_cgroup'].lstrip('/')
            unit = command(['systemctl', '--user', 'show', invocation['unit'],
                            '--property=LoadState,ActiveState,SubState'])
            fields = dict(line.split('=', 1) for line in unit['stdout'].splitlines() if '=' in line)
            unit_closed = ((unit['status'] == 0 and fields.get('ActiveState') == 'inactive')
                           or (fields.get('LoadState') == 'not-found'
                               and fields.get('ActiveState') in (None, 'inactive')))
            check(not cgroup.exists() and unit_closed, f'arm {index} cgroup/unit remains or query inconclusive')
            summary.update({'completed_transfer_rows': len(rows), 'pre_post_hashes_equal': pre_post,
                'independent_attestation_exact_match': att == independent,
                'attestation': reference(arm / 'attestation.json'),
                'independent_attestation': reference(arm / 'independent-controller-attestation.json'),
                'arm_decision': reference(RUN/f'arm-{index}.decision.json') if saved_arm_decision is not None else None,
                'measurement_eligible': saved_arm_decision is not None,
                'strict_rejection_preserved': index == 1,
                'raw_validation_failure_evidence': [reference(arm/name) for name in (
                    'raw-post-join-supervisor-validation.json','prospective-terminal-attribution.json',
                    'results.sender-wait-controls/final-raw-write-validation.json',
                    'results.sender-wait-controls/final-raw-write-record.json',
                    'results.sender-wait-controls/final-raw-write-terminal.json',
                    'results.sender-wait-controls/final-raw-write-terminal.json.attribution.json')],
                'supervisor_lifecycle': reference(arm / 'supervisor-lifecycle.json'),
                'canonical_idle_proofs': [reference(arm / name) for name in proof_names],
                'early_share_sidecars': [reference(arm/f'early-share-{role}-{phase}.json') for role in ('server','agent') for phase in ('startup','terminal')],
                'independent_postrun_sidecar_observation': independent_share,
                'sidecar_observation_does_not_override_mature_rejection': True,
                'host_pre_post_unchanged': same_process(hp, hz) and hp['value'] == hz['value'],
                'host_value': hp['value'], 'runner_value': runner['value'], 'target_value': policy,
                'scope_limit': 'point-in-time endpoint/helper observations; load scope source-derived, no observed load PID',
                'mature_outcome': {key: att.get(key) for key in
                    ('accepted', 'integrity_issues', 'conditioning_issues', 'performance_issues')},
                'natural_endpoint_and_helper_exits': exits,
                'arm_statistics': {key: arm_decision.get(key) for key in
                    ('pooled_bytes_per_second', 'aggregate_upstream_read_ns', 'aggregate_active_transfer_rates',
                     'total_measured_wall_seconds', 'original_download_floor_outcomes',
                     'revisit_outcome_counts', 'origin_outcome_counts', 'retained_downloads')},
                'private_namespace_ids': {kind: sorted(values) for kind, values in private.items()},
                'scope_cgroup': str(cgroup), 'scope_cgroup_exists': cgroup.exists(),
                'scope_unit_query': unit, 'scope_unit_closed': unit_closed})
        except (OSError, ValueError, KeyError, IndexError, TypeError, StopIteration) as error:
            issues.append(f'arm {index} incomplete audit evidence: {type(error).__name__}: {error}')
            summary['evidence_incomplete'] = True
    check(sum(arm.get('completed_transfer_rows',0) for arm in arms) == 32
          and [arm['index'] for arm in arms if not arm.get('output_present')] == [2]
          and sum(len(arm.get('natural_endpoint_and_helper_exits',[])) for arm in arms) == 10,
          'stopped run32 rows/two completed arms/ten natural exits/unstarted third arm differ')
    if complete and decision is not None:
        import importlib.util
        classifier_spec = importlib.util.spec_from_file_location('early_share_final_classifier', ROOT/'validate-early-share.py')
        classifier = importlib.util.module_from_spec(classifier_spec)
        classifier_spec.loader.exec_module(classifier)
        try:
            check(classifier.decide_screen([load(RUN/f'arm-{index}.decision.json') for index in range(3)]) == decision,
                  'independent pure all-five decision recomputation differs')
        except (ValueError, KeyError, IndexError, TypeError) as error:
            issues.append('pure final decision failed: '+str(error))
        final_outer = next((value for value in outer
                            if Path(value['path']).name == 'screen-validation.outer.json'), None)
        check(final_outer is not None and final_outer['status'] == (0 if decision['diagnostic_positive'] else 1),
              'final comparison process status differs from saved diagnostic')
        check(sum(arm.get('completed_transfer_rows', 0) for arm in arms) == 48,
              'complete comparison does not retain all48 rows')
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
    check(tracked['status'] == 0 and not tracked['stdout'].strip(), 'tracked working tree changed or query failed')
    for arm in arms:
        if 'private_namespace_ids' in arm:
            arm['remaining_private_namespace_processes'] = [entry for entry in namespace_remaining
                if entry['namespace'] in arm['private_namespace_ids'][entry['kind']]]
            arm['no_live_private_scope'] = (not arm['remaining_private_namespace_processes']
                and not arm['scope_cgroup_exists'] and arm['scope_unit_closed'])
    status = 'incomplete early-share diagnostic; candidate rejected by unchanged strict raw closure/terminal attribution checks'
    audit = {'schema_version': 1, 'audit_accepted': not issues, 'status': status,
             'issues': issues, 'finished_unix': time.time(),
             'source_runtime_bytes_before_audit': source_bytes_before_audit, 'shared_dependency_cache_temp_binary_bytes': dependency_bytes,
             'source_runtime_rejection_threshold_bytes': 256<<20, 'shared_artifact_rejection_threshold_bytes':4<<30, 'saved_screen_final': screen,
             'comparison_incomplete': screen.get('comparison_incomplete'),
             'effect_criteria_evaluated': False,
             'completed_workload_arms': 2, 'measurement_eligible_arms': 1,
             'saved_failure_not_waived': True,
             'validation_failure_trace': reference(RUN/'arm-1-validation.stderr.log'),
             'closure_audit_acceptance_is_not_measurement_eligibility': True,
             'diagnostic_positive': screen.get('diagnostic_positive'), 'original_qualification_cleared': False,
             'source': reference(ROOT / 'SOURCE.json'), 'runtime_grant': reference(ROOT / 'RUNTIME-GRANT.json'),
             'audit_source': reference(Path(__file__).resolve()),
             'historical_build_receipt': reference(OLD/'build/receipt.json'),
             'build_receipt': reference(ROOT/'build-continuation-1/receipt.json'),
             'historical_existing_entries_checked': len(historical['initial_existing_file_sha256']),
             'historical_policy_source_entries_checked': len(historical['files']),
             'new_source_entries_checked': len(source['files']), 'original_frozen_entries_checked': len(frozen),
             'input_changes': sorted(set(changed)), 'arms': arms,
             'completed_transfer_rows': sum(arm.get('completed_transfer_rows', 0) for arm in arms),
             'unlaunched_arms': [arm['index'] for arm in arms if not arm.get('output_present')],
             'outer_commands': [{key: value.get(key) for key in
                 ('path', 'status', 'forced', 'accepted_cleanup', 'remaining_owned', 'adopted_descendants',
                  'failure_reason', 'elapsed_seconds')} for value in outer],
             'remaining_pid_start_identities': remaining,
             'private_namespace_ids': {kind: sorted(values) for kind, values in namespaces.items()},
             'remaining_private_namespace_processes': namespace_remaining,
             'unavailable_proc_namespace_reads': unreadable, 'named_netns_query': netns,
             'tracked_status_query': tracked, 'benchmark_reexecution': False,
             'no_additional_test_build_or_traffic': True}
    if decision is not None:
        audit['decision'] = reference(RUN / 'decision.json')
        audit['decision_summary'] = {key: decision.get(key) for key in
            ('accepted', 'diagnostic_positive', 'original_qualification_cleared', 'criteria', 'failed_criteria',
             'pooled_bytes_per_second', 'aggregate_upstream_read_ns', 'candidate_pooled_ratio_to_faster_control',
             'candidate_pooled_ratios_to_each_control', 'control_pooled_ratio_0_to_2',
             'candidate_rate_ratios_to_faster_corresponding_control', 'early_share_order', 'idle_policy_order', 'controller_order', 'interpretation')}
    with output.open('x') as stream:
        json.dump(audit, stream, indent=2, sort_keys=True)
        stream.write('\n')
    files = sorted(path for path in ROOT.rglob('*') if path.is_file() and path != ledger)
    with ledger.open('x') as stream:
        for path in files:
            stream.write(sha(path) + '  ' + str(path.relative_to(ROOT)) + '\n')
    print(json.dumps({'audit_accepted': not issues, 'status': status,
                      'audit_sha256': sha(output), 'ledger_sha256': sha(ledger), 'files': len(files),
                      'completed_rows': audit['completed_transfer_rows'],
                      'diagnostic_positive': screen.get('diagnostic_positive'), 'issues': issues}))
    return 0 if not issues else 1


if __name__ == '__main__':
    raise SystemExit(main())
