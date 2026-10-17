#!/usr/bin/env python3
"""Saved original96 outcome only; never run a validator, endpoint or probe."""
import argparse
import hashlib
import json
import math
import errno
import os
import time
from pathlib import Path
import statistics
import stat

ROOT = Path(__file__).resolve().parent
QUAL = ROOT.parent/'wan-route-reuse-qualification-20261017'
RUN = QUAL/'runs/original96-once'
EXEC = QUAL/'execution-once'
BUILD = ROOT.parent/'wan-route-reuse-build-20261017'
BUILD_RECEIPT_SHA = 'd428e45fd861c8ed7a4346920e53c2a56d1fe678660fd343707f9841f53af624'
INPUTS = {}

def require(value, message):
    if not value:
        raise ValueError(message)

def raw(path, maximum=16 << 20):
    path = Path(path)
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and not path.is_symlink() and 0 < info.st_size <= maximum, 'nonregular or unbounded input: '+str(path))
    data = path.read_bytes()
    INPUTS[str(path)] = {'sha256':hashlib.sha256(data).hexdigest(), 'bytes':len(data)}
    return data

def read(path): return json.loads(raw(path))
def lines(path): return [json.loads(line) for line in raw(path).decode().splitlines() if line.strip()]
def status(name): return int(raw(RUN/name, 64).strip())

def pooled(rows):
    # Exact original runner formula: median of all nine steady samples per
    # mode/direction, then aggregate median / legacy median. No pair pooling.
    output = {}
    for direction in ('download', 'upload'):
        medians = {}
        samples = {}
        for mode in ('legacy', 'aggregate-v1'):
            values = [r['useful_transfer_bytes_per_second'] for r in rows if r.get('measurement_stage') == 'steady' and r.get('transport_mode') == mode and r.get('transfer_direction') == direction]
            require(len(values) == 9 and all(type(v) in (int,float) and math.isfinite(v) and v > 0 for v in values), 'missing/nonfinite pooled9 samples')
            medians[mode] = statistics.median(values)
            samples[mode] = values
        ratio = medians['aggregate-v1']/medians['legacy']
        output[direction] = {'samples_bytes_per_second':samples, 'median_bytes_per_second':medians,
                             'aggregate_to_legacy_ratio':ratio, 'minimum_ratio':.90, 'formula_pass':ratio >= .90}
    return output

def evaluate():
    result = {'classification':'incomplete_or_invalid', 'original_qualification_cleared':False,
              'performance_validator_executed':False, 'issues':[], 'input_files':INPUTS,
              'scope':'Original96 contract only; no pressure/parity/security/release clearance.',
              'closure_scope':{'endpoint_groups':'15 canonical private-PID process-exit records; natural status0 and joined groups',
                               'outer_ownership':'separate hostPID/start-tick descendant inventory and final absence from the owned tree',
                               'namespace_ownership':'canonical outer private user/PID/mount/net plus helper PID/start/netns and read-only defaults proofs',
                               'per_endpoint_host_private_pid_mapping':'not captured; not claimed or retroactively required',
                               'private_namespace_disappearance':'separate final root audit if available; not inferred from a numeric private PID or a hostPID scan'}}
    # Preserve independently available closure/status facts even when an early
    # metadata failure prevents canonical attestation or a complete row cohort.
    result['available_closure_metadata'] = {}
    for label,path in [('driver',EXEC/'RESULT.json'),('outer_audit',EXEC/'wrapper.outer.json'),('supervisor',RUN/'supervisor-lifecycle.json'),('default_proof',RUN/'transport-defaults-validation.json')]:
        if path.exists():
            try:
                value = read(path)
                if label == 'outer_audit': value = {k:value.get(k) for k in ['status','forced','accepted_cleanup','remaining_owned','adopted_descendants','failure_reason']}
                result['available_closure_metadata'][label] = value
            except (OSError,ValueError,KeyError) as error:
                result['available_closure_metadata'][label] = {'read_error':type(error).__name__+': '+str(error)}
    try:
        rows = lines(RUN/'results.jsonl')
        result['row_count'] = len(rows)
        if len(rows) == 96:
            result['pooled_directions'] = pooled(rows)
            result['pooled_origin'] = 'transparent original-runner formula; descriptive until all canonical/source/closure prerequisites below hold'
        att = read(RUN/'h2-stability-attestation.json')
        result['canonical_attestation'] = att
        result['pair_gates'] = att.get('pair_gates')
        lifecycle = lines(RUN/'results.lifecycle.jsonl')
        result['natural_process_exit_count'] = sum(r.get('event') == 'process-exit' and r.get('status') == 0 and r.get('forced') is False and r.get('group_joined') is True and r.get('invalidates_success') is False for r in lifecycle)
        require(att.get('exact_complete_timing_cohort') is True and att.get('row_count') == len(rows) == 96
                and att.get('results_sha256') == INPUTS[str(RUN/'results.jsonl')]['sha256']
                and att.get('lifecycle_sha256') == INPUTS[str(RUN/'results.lifecycle.jsonl')]['sha256']
                and att.get('natural_endpoint_and_namespace_statuses') is True and att.get('outer_private_user_pid_mount_net') is True
                and att.get('outer_runner_group_joined') is True and result['natural_process_exit_count'] == 15,
                'canonical exact payload/status/H2/event/order/lifecycle attestation missing or stale')
        statuses = {name:status(name) for name in ['runner.status','validation.status','wrapper.status','transport-defaults-validation.status']}
        result['statuses'] = statuses
        defaults = read(RUN/'transport-defaults-validation.json')
        require(statuses['transport-defaults-validation.status'] == 0 and defaults.get('accepted') is True
                and defaults.get('read_only') is True and defaults.get('point_in_time_only') is True, 'default/private-helper proof rejected')
        driver = read(EXEC/'RESULT.json'); audit = read(EXEC/'wrapper.outer.json')
        result['outer_driver'] = driver
        result['outer_host_owned_identity_count'] = len(audit.get('owned_identities', []))
        result['outer_closure'] = {k:audit.get(k) for k in ['status','forced','accepted_cleanup','remaining_owned','adopted_descendants','failure_reason']}
        require(driver.get('original96_workload') is True and driver.get('traffic_retry') is False
                and driver.get('source_before_after_unchanged') is True and driver.get('outer_natural_cleanup') is True
                and driver.get('outer_audit_sha256') == INPUTS[str(EXEC/'wrapper.outer.json')]['sha256']
                and driver.get('artifact_bound_rejected', False) is False
                and driver['artifact_bytes'] <= driver['artifact_rejection_threshold_bytes']-(1 << 20), 'driver/source/byte closure rejected')
        require(audit.get('forced') is False and audit.get('accepted_cleanup') is True and audit.get('remaining_owned') == []
                and audit.get('adopted_descendants') == [] and audit.get('status') == driver['wrapper_status'] == statuses['wrapper.status']
                and all(type(r.get('pid')) is int and type(r.get('start_ticks')) is int and r['pid'] > 0 and r['start_ticks'] > 0 for r in audit['owned_identities']), 'outer hostPID/start cleanup rejected')
        source = read(QUAL/'SOURCE.json'); manifest = read(QUAL/'provenance/stability-manifest.json')
        require(driver.get('runtime_source_manifest_sha256') == INPUTS[str(QUAL/'SOURCE.json')]['sha256'], 'runtime source manifest differs')
        receipt = read(BUILD/'build-once/receipt.json'); bp = read(BUILD/'PLAN.json'); focused = read(bp['focused_plan'])
        require(INPUTS[str(BUILD/'build-once/receipt.json')]['sha256'] == BUILD_RECEIPT_SHA == manifest['current_build_receipt_sha256']
                and receipt.get('accepted') is True and receipt.get('source_before_after_unchanged') is True
                and receipt.get('source_identity') == bp['source_identity'] and receipt['source_identity']['source_file_count'] == 430
                and receipt['source_ref'] == manifest['source']['current_ref'], 'ordinary current build/source430 binding differs')
        bound = {r['path']:r['sha256'] for r in source['files']}
        for name, checksum in focused['source_files'].items():
            require(bound.get(str(Path(bp['source'])/name)) == checksum, 'runtime manifest omits production input: '+name)
        for role,key in [('server','current_server_binary'),('agent','current_agent_binary')]:
            require(manifest['binaries'][role] == receipt['binaries'][role]['path']
                    and manifest['sha256'][key] == bound.get(manifest['binaries'][role]) == receipt['binaries'][role]['sha256'], 'current binary before/after binding differs')
        legacy = read(QUAL/'provenance/LEGACY-REUSE.json')
        require(manifest['binaries']['legacy'] == legacy['binary']['path']
                and manifest['sha256']['legacy_agent_binary'] == bound.get(legacy['binary']['path']) == legacy['binary']['sha256'], 'legacy binary before/after binding differs')
        result['identities'] = {'current_ref':manifest['source']['current_ref'], 'legacy_ref':manifest['source']['legacy_ref'], 'binary_hashes':{k:manifest['sha256'][k] for k in ['current_server_binary','current_agent_binary','legacy_agent_binary']}, 'source_identity':receipt['source_identity'], 'build_receipt_sha256':BUILD_RECEIPT_SHA}
        pairs = att['pair_gates']
        require(len(pairs) == 3 and [r['pair'] for r in pairs] == [1,2,3], 'canonical paired cardinality differs')
        pair_pass = all(r[d]['pass'] is True for r in pairs for d in ('download','upload'))
        pool_pass = all(v['formula_pass'] for v in result['pooled_directions'].values())
        if (RUN/'results.summary.json').exists():
            summary = read(RUN/'results.summary.json'); result['canonical_pooled_summary'] = summary
            for direction,value in result['pooled_directions'].items():
                require(math.isclose(summary['acceptance'][direction+'_candidate_to_legacy_ratio'], value['aggregate_to_legacy_ratio'], rel_tol=1e-12), 'saved pooled summary differs from transparent formula')
        require(not pool_pass or 'canonical_pooled_summary' in result, 'canonical pooled summary missing despite both pooled timing gates passing')
        require(statuses['runner.status'] in (0,1) and statuses['validation.status'] in (0,1)
                and att['original_runner_status'] == statuses['runner.status'], 'canonical runtime/validator status invalid')
        positive = pair_pass and pool_pass
        if statuses['runner.status'] == 1:
            failing_direction = next((d for d in ('download','upload') if not result['pooled_directions'][d]['formula_pass']), None)
            require(failing_direction is not None, 'runner failed despite both pooled timing gates passing')
            ratio = result['pooled_directions'][failing_direction]['aggregate_to_legacy_ratio']
            message = f'{failing_direction} candidate/legacy median throughput ratio {ratio:.4f} is below 0.90'
            require(message in raw(RUN/'runner.stderr.log', 4 << 20).decode(), 'runner failure was not the canonical pooled timing rejection')
            result['canonical_pooled_rejection_message'] = message
        require((att['accepted'] is True) == (pair_pass and statuses['runner.status'] == 0), 'canonical paired attestation acceptance differs')
        require((statuses['runner.status'] == 0) == pool_pass and (statuses['validation.status'] == 0) == (att['accepted'] is True)
                and (statuses['wrapper.status'] == 0) == positive and (driver['accepted'] is True) == positive, 'saved failure does not match original timing gates; metadata remains invalid')
        result.update(classification='complete_valid_positive' if positive else 'complete_valid_original_gate_failure',
                      original_qualification_cleared=positive, original_contract_measurement_valid=True,
                      pooled_origin='canonical summary plus checked formula' if 'canonical_pooled_summary' in result else 'transparent original-runner formula; summary absent on authoritative pooled timing rejection')
    except (OSError, ValueError, KeyError, TypeError, ZeroDivisionError) as error:
        result['issues'].append(type(error).__name__+': '+str(error))
    return result

def observe_namespace_closure():
    """One final readable-/proc scan; never conflate private IDs with host PIDs."""
    outer = read(RUN/'outer-namespaces.json')
    pre = read(RUN/'transport-defaults-pre.json')
    post = read(RUN/'transport-defaults-post.json')
    boot = Path('/proc/sys/kernel/random/boot_id').read_text().strip()
    require(boot == outer['boot_id'], 'different boot; cannot join saved namespace identities')
    wanted = {kind:{outer['owned'][kind]} for kind in ('user','pid','mnt','net')}
    for phase in (pre,post):
        for role in ('runner','agent_origin','router'):
            require(phase[role]['boot_id'] == boot, 'namespace sample boot differs')
            wanted['net'].add(phase[role]['namespace'])
            wanted['pid'].add(phase[role]['pid_namespace'])
    start = time.monotonic_ns()
    matches, unreadable_examples = [], []
    unreadable = vanished = checked = 0
    pids = sorted(int(entry.name) for entry in os.scandir('/proc') if entry.name.isdecimal())
    require(len(pids) <= 20000, 'bounded one-shot namespace scan exceeds20000 host processes')
    for pid in pids:
        for kind in wanted:
            try:
                value = os.readlink(f'/proc/{pid}/ns/{kind}')
                checked += 1
                if value in wanted[kind]: matches.append({'host_pid':pid,'kind':kind,'namespace':value})
            except OSError as error:
                if error.errno in (errno.ENOENT,errno.ESRCH): vanished += 1
                elif error.errno in (errno.EACCES,errno.EPERM):
                    unreadable += 1
                    if len(unreadable_examples) < 64: unreadable_examples.append({'host_pid':pid,'kind':kind,'errno':error.errno})
                else: raise
    return {'read_only':True,'one_scan_only':True,'boot_id':boot,'started_monotonic_ns':start,'finished_monotonic_ns':time.monotonic_ns(),
            'captured_private_namespaces':{k:sorted(v) for k,v in wanted.items()},'host_processes_seen':len(pids),'readable_namespace_links_checked':checked,
            'matches':matches,'vanished_or_raced_links':vanished,'permission_denied_links':unreadable,'permission_denied_examples_first64':unreadable_examples,
            'finding':'private_namespace_still_observed' if matches else ('not_observed_among_readable_entries_with_permission_gaps' if unreadable else 'private_namespaces_not_observed_in_complete_readable_scan'),
            'scope':'post-cohort scan interval of host /proc namespace links; ENOENT/ESRCH entries vanished, EACCES/EPERM remain unknown; separate from natural lifecycle and no per-endpoint host/private PID mapping'}

def main():
    parser = argparse.ArgumentParser(); parser.add_argument('--output', required=True)
    parser.add_argument('--observe-namespace-closure', action='store_true')
    args = parser.parse_args(); output = Path(args.output).resolve()
    require(output.is_relative_to(ROOT) and not output.exists(), 'fresh analysis output under outcome root required')
    result = evaluate()
    if args.observe_namespace_closure:
        try:
            result['post_cohort_namespace_scan'] = observe_namespace_closure()
            if result['post_cohort_namespace_scan']['matches']:
                result.update(classification='incomplete_or_invalid', original_qualification_cleared=False, original_contract_measurement_valid=False)
                result['issues'].append('captured private namespace still observed after cohort; saved timing figures do not override closure failure')
        except (OSError,ValueError,KeyError) as error: result['post_cohort_namespace_scan'] = {'finding':'unavailable','error':type(error).__name__+': '+str(error)}
    with output.open('x') as handle:
        json.dump(result, handle, indent=2, sort_keys=True); handle.write('\n')
    print(json.dumps({k:result[k] for k in ['classification','original_qualification_cleared','issues']}))

if __name__ == '__main__': main()
