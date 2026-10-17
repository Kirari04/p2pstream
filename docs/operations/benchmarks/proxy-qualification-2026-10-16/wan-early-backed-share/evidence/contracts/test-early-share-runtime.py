#!/usr/bin/env python3
"""Finite new-boundary controls; original numerical/sidecar suites stay frozen."""
from pathlib import Path
import copy
import hashlib
import importlib.util
import json
import tempfile
import sys
import unittest
from unittest.mock import patch
ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('early_runtime_classifier', ROOT/'validate-early-share.py')
V = importlib.util.module_from_spec(spec)
spec.loader.exec_module(V)
SAVED = ROOT.parent/'wan-controller-screen-20261017/runner-source/integration/runs/controller-crc-policy-once'

class RuntimeBoundary(unittest.TestCase):
    def arms(self, candidate=36_720_000):
        # These are explicitly synthetic fixtures derived from saved decision
        # shapes. They never rewrite or relabel those historical measurements.
        rows = [copy.deepcopy(V.BASE.load(SAVED/f'arm-{i}.decision.json')) for i in range(3)]
        for i, arm in enumerate(rows):
            rate = candidate if i == 1 else 34_000_000
            arm.update(controller='cubic', idle_policy=1,
                       idle_namespace_proof={'accepted': True, 'expected_value': 1},
                       early_share_policy=V.SHARE.ORDER[i],
                       early_share_proof={'accepted': True, 'selected_policy': V.SHARE.ORDER[i]})
            arm['speeds'] = {str(n): rate for n in range(1, 17)}
            for retained in arm['retained_downloads']:
                retained.update(bytes_per_second=rate, wall_seconds=268435456/rate,
                                upstream_read_ns=7_000_000_000 if i == 1 else 8_000_000_000)
            arm['mature_attestation'].update(integrity_issues=[], conditioning_issues=[],
                    performance_issues=[], validation_issues=[], continuation_safe=True, accepted=True)
            arm.update(mature_accepted=True, mature_conditioning_issues=[], mature_performance_issues=[])
        return rows

    def test_original_five_criteria_boundary_and_complete_negative(self):
        result = V.decide_screen(self.arms())
        self.assertTrue(result['accepted'])
        self.assertEqual(len(result['criteria']), 5)
        self.assertFalse(result['original_qualification_cleared'])
        below = V.decide_screen(self.arms(36_719_999))
        self.assertFalse(below['criteria']['pooled_rate_at_least_108pct_faster_control'])
        slow = self.arms(1_000_000)
        issues = ['synthetic slow candidate']
        slow[1]['mature_attestation'].update(accepted=False, performance_issues=issues, validation_issues=issues)
        slow[1].update(mature_accepted=False, mature_performance_issues=issues)
        result = V.decide_screen(slow)
        self.assertFalse(result['accepted'])
        self.assertEqual(len(result['arms']), 3)
        self.assertEqual(result['arms'][1]['mature_performance_issues'], issues)
        with self.assertRaises(ValueError): V.decide_screen(slow[:2])
        slow[1]['eligible_for_diagnostic'] = False
        with self.assertRaises(ValueError): V.decide_screen(slow)

    def test_selector_and_default_idle_intent_are_independent(self):
        self.assertEqual(V.PROOF.ORDER, (1, 1, 1))
        self.assertEqual(V.SHARE.ORDER, (False, True, False))
        self.assertEqual(V.BASE.ORDER, ('cubic',)*3)
        for key, value in [('early_share_policy', False), ('idle_policy', 0), ('idle_policy', True)]:
            arms = self.arms(); arms[1][key] = value
            with self.assertRaises(ValueError): V.decide_screen(arms)
        arms = self.arms(); arms[1]['idle_namespace_proof']['expected_value'] = True
        with self.assertRaises(ValueError): V.decide_screen(arms)
        for index in [-1, True, 3]:
            with self.assertRaises(ValueError): V.PROOF.expected(index)

    def test_mature_then_namespace_then_sidecar_and_failure_stops_later_checks(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); (root/'results.sender-wait-controls').mkdir()
            for name in ['idle-configured.json', 'idle-endpoints-endpoint-binding.json',
                         'idle-endpoints-post-workload.json', 'idle-host-pre.json', 'idle-host-post.json',
                         'results.sender-wait-controls/final-raw-write-terminal.json']:
                (root/name).write_text('{}')
            (root/'results.lifecycle.jsonl').write_text('')
            for failed in [None, 'mature', 'namespace', 'sidecar']:
                calls = []
                def step(name, result):
                    def run(*args):
                        calls.append(name)
                        if failed == name: raise ValueError('synthetic '+name+' rejection')
                        return result
                    return run
                with patch.object(V.BASE, 'validate_arm', step('mature', {'eligible_for_diagnostic': True})), \
                     patch.object(V.PROOF, 'validate', step('namespace', {'accepted': True, 'expected_value': 1})), \
                     patch.object(V.SHARE, 'validate', step('sidecar', {'accepted': True, 'selected_policy': True})):
                    if failed:
                        with self.assertRaises(ValueError): V.validate_arm(1, root, 0)
                    else:
                        self.assertTrue(V.validate_arm(1, root, 0)['early_share_proof']['accepted'])
                self.assertEqual(calls, ['mature', 'namespace', 'sidecar'][:1+['mature','namespace','sidecar'].index(failed)] if failed else ['mature','namespace','sidecar'])

    def test_launcher_continues_slow_middle_but_stops_structural_invalidity(self):
        spec = importlib.util.spec_from_file_location('early_runtime_launcher_contract', ROOT/'run-early-share.py')
        launcher = importlib.util.module_from_spec(spec); spec.loader.exec_module(launcher)
        for structural in [False, True]:
            with tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary); old = root/'old'; old.mkdir()
                (old/'FROZEN-INPUTS.json').write_text('[]')
                source = json.dumps({'files': []}); (root/'SOURCE.json').write_text(source)
                (root/'RUNTIME-GRANT.json').write_text(json.dumps({'source_manifest_sha256':hashlib.sha256(source.encode()).hexdigest(),
                    'runtime_approved':True,'maximum_arms':3,'maximum_transfers':48,'early_share_order':[0,1,0],
                    'idle_policy_order':[1,1,1],'controller_order':['cubic']*3}))
                commands = json.loads((ROOT/'COMMANDS.json').read_text())
                for i, command in enumerate(commands['arms']):
                    command['output_dir'] = str(root/f'arm-{i}')
                    command['env']['P2PSTREAM_QUALIFICATION_EARLY_SHARE_PROOF_DIR'] = command['output_dir']
                (root/'COMMANDS.json').write_text(json.dumps(commands))
                started, finalized = [], []
                def execute(argv, env, prefix, *bounds):
                    if prefix.name in ['arm-0','arm-1','arm-2']:
                        index = int(prefix.name[-1]); started.append(index)
                        return int(index == 1)  # Candidate mature performance fails.
                    if 'arm' in argv:
                        index = int(argv[argv.index('arm')+1])
                        if structural and index == 1: return 1
                        Path(argv[-1]).write_text(json.dumps({'eligible_for_diagnostic':True}))
                        return 0
                    self.assertIn('screen', argv)
                    Path(argv[-1]).write_text(json.dumps({'accepted':False}))
                    return 1  # A complete negative is retained as negative.
                with patch.object(launcher,'ROOT',root), patch.object(launcher,'OLD',old), \
                     patch.object(launcher.SUP,'execute_owned',execute), \
                     patch.object(launcher.SUP,'finish_screen',lambda directory,screen:finalized.append(copy.deepcopy(screen))), \
                     patch.object(sys,'argv',['run-early-share.py','--execute-approved-once']):
                    if structural:
                        with self.assertRaises(RuntimeError): launcher.main()
                    else: launcher.main()
                self.assertEqual(started, [0,1] if structural else [0,1,2])
                self.assertEqual(len(finalized), 1)
                self.assertFalse(finalized[0]['accepted'])
                self.assertEqual(finalized[0]['completed_arms'], 1 if structural else 3)
                self.assertEqual(finalized[0]['comparison_incomplete'], structural)

    def test_exact_command_pair_policy_and_timed_function_bindings(self):
        commands = json.loads((ROOT/'COMMANDS.json').read_text())
        build = json.loads((ROOT/'BUILD-CONTINUATION-1-PLAN.json').read_text())
        self.assertEqual(len(commands['arms']), 3)
        for i, command in enumerate(commands['arms']):
            self.assertEqual(command['env'], {'P2PSTREAM_QUALIFICATION_DATA_CONTROLLER':'cubic',
                    'P2PSTREAM_IDLE_ARM_INDEX':str(i), 'P2PSTREAM_QUALIFICATION_EARLY_SHARE':str(int(i==1)),
                    'P2PSTREAM_QUALIFICATION_EARLY_SHARE_PROOF_DIR':command['output_dir']})
            self.assertIs(type(command['idle_policy']), int); self.assertEqual(command['idle_policy'], 1)
            self.assertIs(type(command['early_share_policy']), int); self.assertEqual(command['early_share_policy'], int(i==1))
            argv = command['argv']
            for flag, expected in [('--server-binary',build['binaries']['server']),
                    ('--aggregate-agent-binary',build['binaries']['agent']),
                    ('--server-source-ref',build['source_ref']),('--aggregate-agent-source-ref',build['source_ref']),
                    ('--protocol','h2')]: self.assertEqual(argv[argv.index(flag)+1], expected)
        base = (ROOT.parent/'wan-idle-policy-screen-20261017/runner.sh').read_text()
        current = (ROOT/'runner.sh').read_text()
        start = base.index('run_transfer() {'); end = base.index('\nrun_maybe_sender_wait_transfer()',start)
        body = base[start:end]
        self.assertIn(body, current)
        self.assertEqual(hashlib.sha256(body.encode()).hexdigest(), 'b86593361c76c3d2903d722b1b0292fa1bc833210d5efb2438dea1aac08ad798')
        launcher = (ROOT/'run-early-share.py').read_text()
        self.assertNotIn('mature_accepted', launcher)
        self.assertLess(launcher.index('for index,command'), launcher.index("'screen',*decisions"))

if __name__ == '__main__': unittest.main()
