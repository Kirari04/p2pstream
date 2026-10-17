"""Read only the fixed screen's immutable logs; never launch workloads."""
import datetime
import decimal
import hashlib
import json
import pathlib
import re
import statistics
import sys

manifest_path = pathlib.Path(sys.argv[1]).resolve()
manifest = json.loads(manifest_path.read_text())
source = pathlib.Path(__file__).resolve()
sha = lambda data: hashlib.sha256(data).hexdigest()
assert sha(source.read_bytes()) == manifest['postprocessor_sha256'], 'postprocessor source differs'
for label, item in manifest['inputs'].items():
    assert sha(pathlib.Path(item['path']).read_bytes()) == item['sha256'], 'input differs: ' + label

out = pathlib.Path(manifest['screen_directory'])
freeze = json.loads((out / 'frozen-inputs.json').read_text())
commands = json.loads((out / 'measurement-commands.json').read_text())
ledger = [json.loads(line) for line in (out / 'measurement-ledger.jsonl').read_text().splitlines() if line.strip()]
postrun = json.loads((out / 'postrun-input-audit.json').read_text())
assert postrun['accepted'] is True and postrun['remaining_test_or_benchmark_processes'] == []
assert postrun['frozen_input_manifest_sha256'] == sha((out / 'frozen-inputs.json').read_bytes())
assert postrun['collector_postprocessing_exit_status'] == 1
assert freeze['measurement_environment']['events_per_op'] == 512
assert freeze['measurement_environment']['samples_per_arm_per_case'] == 10
assert freeze['measurement_environment']['benchtime'] == '300ms'
assert freeze['retention_criteria'] == {
    'all_correctness_and_race_tests_pass': True,
    'bytes_saved_per_event_min': 64,
    'allocs_saved_per_event_min': 4,
    'median_candidate_baseline_ns_ratio_max': 1.03,
    'extra_samples_or_retries': False,
}
expected_cases = {
    'BenchmarkPreparedProxyRequestEventBatchFlush/background',
    'BenchmarkPreparedProxyRequestEventBatchFlush/production_deadline',
    *[f'BenchmarkRecorderNullableIDFixedBatchInsert/{shape}/{context}'
      for shape in ['all_null', 'ordinary_direct', 'all_small', 'all_large']
      for context in ['background', 'production_deadline']],
}
assert len(expected_cases) == 10 and set(freeze['benchmark_cases']) == expected_cases
expected_order = [(pair, arm) for pair in range(1, 11)
                  for arm in (['baseline', 'candidate'] if pair % 2 else ['candidate', 'baseline'])]
assert len(commands) == 20
assert [(entry['pair'], entry['arm']) for entry in commands] == expected_order
assert len(ledger) == 21 and ledger[0]['state'] == 'started'
assert [entry['state'] for entry in ledger[1:]] == ['sample_finished'] * 20
assert ledger[0]['frozen_input_manifest_sha256'] == sha((out / 'frozen-inputs.json').read_bytes())
assert set(path.name for path in (out / 'logs').glob('pair-*.log')) == {entry['label'] + '.log' for entry in commands}

metric_units = {'ns/op', 'B/op', 'allocs/op', 'events/op'}
samples = []
raw_log_bindings = []
for invocation, ledger_entry in zip(commands, ledger[1:]):
    expected_label = f"pair-{invocation['pair']:02}-{invocation['arm']}"
    assert invocation['label'] == expected_label
    assert invocation['exit_status'] == 0
    assert {key: ledger_entry[key] for key in invocation} == invocation
    expected_argv = ['taskset', '-c', '0', 'env', 'GOMAXPROCS=1',
                     str(out / 'bin' / (invocation['arm'] + '.test')),
                     '-test.run', '^$', '-test.bench',
                     '^(BenchmarkPreparedProxyRequestEventBatchFlush|BenchmarkRecorderNullableIDFixedBatchInsert)$',
                     '-test.benchmem', '-test.benchtime=300ms', '-test.count=1', '-test.cpu=1', '-test.timeout=180s']
    assert invocation['argv'] == expected_argv
    assert invocation['cwd'] == str(out / 'sources' / invocation['arm'])
    path = out / 'logs' / (expected_label + '.log')
    raw = path.read_bytes()
    raw_hash = sha(raw)
    assert raw_hash == invocation['log_sha256']
    assert raw_hash == manifest['raw_logs'][expected_label]['sha256']
    raw_lines = raw.splitlines(keepends=True)
    seen = set()
    pending = None
    saw_pass = False
    for line_number, raw_line in enumerate(raw_lines, 1):
        line = raw_line.decode('utf-8').rstrip('\r\n')
        rest = line.strip()
        if not rest:
            continue
        if line.startswith('Benchmark'):
            parts = line.split(None, 1)
            raw_name = parts[0]
            name = raw_name[:-2] if raw_name.endswith('-1') else raw_name
            assert name in expected_cases, (expected_label, line_number, 'unknown benchmark', raw_name)
            assert pending is None, (expected_label, line_number, 'new benchmark before prior metrics')
            assert name not in seen, (expected_label, line_number, 'duplicate benchmark', name)
            pending = {'case': name, 'benchmark_name': raw_name, 'name_line_number': line_number,
                       'name_line_sha256': sha(raw_line), 'name_line': line}
            rest = parts[1].strip() if len(parts) == 2 else ''
            if not rest:
                continue
        if rest.startswith('{'):
            log_entry = json.loads(rest)
            assert isinstance(log_entry, dict) and 'level' in log_entry and 'message' in log_entry
            continue
        if re.match(r'^\d+\s+', rest):
            assert pending is not None, (expected_label, line_number, 'orphan metric record')
            tokens = rest.split()
            assert re.fullmatch(r'[1-9]\d*', tokens[0]), (expected_label, line_number, 'invalid iteration count')
            assert len(tokens) == 9, (expected_label, line_number, 'unknown/missing metric token', tokens)
            metrics = {}
            numeric_text = {}
            for value, unit in zip(tokens[1::2], tokens[2::2]):
                assert unit in metric_units and unit not in metrics, (expected_label, line_number, 'unknown/duplicate metric', unit)
                number = decimal.Decimal(value)
                assert number.is_finite() and number >= 0, (expected_label, line_number, 'invalid metric', value)
                metrics[unit] = float(number)
                numeric_text[unit] = value
            assert set(metrics) == metric_units
            assert metrics['ns/op'] > 0 and metrics['events/op'] == 512
            sample = {'pair': invocation['pair'], 'arm': invocation['arm'], 'raw_log': str(path),
                      'raw_log_sha256': raw_hash, **pending, 'metric_line_number': line_number,
                      'metric_line_sha256': sha(raw_line), 'metric_line': line,
                      'iterations': int(tokens[0]), 'metrics': metrics, 'metric_text': numeric_text}
            samples.append(sample)
            seen.add(pending['case'])
            pending = None
            continue
        if rest == 'PASS':
            assert pending is None and seen == expected_cases, (expected_label, line_number, 'incomplete metrics before PASS')
            assert not saw_pass, (expected_label, line_number, 'duplicate PASS')
            saw_pass = True
            continue
        assert not saw_pass, (expected_label, line_number, 'unexpected content after PASS')
        assert any(rest.startswith(prefix) for prefix in ('goos: ', 'goarch: ', 'pkg: ', 'cpu: ')), (expected_label, line_number, 'unknown output line', rest)
    assert pending is None and seen == expected_cases and saw_pass, (expected_label, 'missing metrics/PASS')
    raw_log_bindings.append({'label': expected_label, 'path': str(path), 'sha256': raw_hash,
                             'line_count': len(raw_lines), 'case_count': len(seen)})

assert len(samples) == 200
identities = {(row['pair'], row['arm'], row['case']) for row in samples}
assert len(identities) == 200
summary = {}
failed_cases = []
for case in sorted(expected_cases):
    medians = {}
    for arm in ['baseline', 'candidate']:
        values = [row for row in samples if row['case'] == case and row['arm'] == arm]
        assert len(values) == 10 and {row['pair'] for row in values} == set(range(1, 11))
        medians[arm] = {unit: statistics.median(row['metrics'][unit] for row in values) for unit in sorted(metric_units)}
    baseline, candidate = medians['baseline'], medians['candidate']
    saved_bytes = (baseline['B/op'] - candidate['B/op']) / 512
    saved_allocs = (baseline['allocs/op'] - candidate['allocs/op']) / 512
    ratio = candidate['ns/op'] / baseline['ns/op']
    accepted = saved_bytes >= 64 and saved_allocs >= 4 and ratio <= 1.03
    summary[case] = {'medians': medians, 'saved_bytes_per_event': saved_bytes,
                     'saved_allocs_per_event': saved_allocs, 'candidate_baseline_ns_ratio': ratio, 'accepted': accepted}
    if not accepted:
        failed_cases.append(case)

result = {'accepted': not failed_cases, 'failed_cases': failed_cases,
          'analysis_only_correction': 'Stateful association of split benchmark-name and metric lines in existing immutable raw logs.',
          'frozen_collector_failure_preserved': True, 'timed_invocations': 20, 'samples': 200,
          'extra_samples_or_retries': False, 'criteria': freeze['retention_criteria'],
          'frozen_input_manifest_sha256': sha((out / 'frozen-inputs.json').read_bytes()),
          'postprocessor_manifest_sha256': sha(manifest_path.read_bytes()), 'postprocessor_sha256': sha(source.read_bytes()),
          'recorded_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
          'raw_logs': raw_log_bindings, 'summary': summary}
destination = out / 'postprocessing-reviewed'
destination.mkdir(exist_ok=False)
(destination / 'all-200-samples.json').write_text(json.dumps(samples, indent=2) + '\n')
(destination / 'results.json').write_text(json.dumps(result, indent=2) + '\n')
print(json.dumps(result, indent=2))
