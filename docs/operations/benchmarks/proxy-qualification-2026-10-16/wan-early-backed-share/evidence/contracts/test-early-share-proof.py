#!/usr/bin/env python3
"""Focused pure sidecar-boundary contracts; no sockets, endpoints or traffic."""
from pathlib import Path
import copy
import importlib.util
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('early_share_proof', ROOT / 'validate-early-share-proof.py')
proof = importlib.util.module_from_spec(spec)
spec.loader.exec_module(proof)


class EarlyShareProof(unittest.TestCase):
    def fixture(self, enabled=True, role='agent'):
        process = {'pid': 10, 'start_ticks': 123, 'namespace': 'net:[44]', 'namespace_device': 1,
                   'namespace_inode': 44, 'pid_namespace': 'pid:[55]', 'boot_id': 'boot'}
        startup = {'schema_version': 1, 'phase': 'startup-before-endpoints', 'role': role,
                   'selected_policy': enabled, 'process': process, 'observed_unix_ns': 100,
                   'threshold_bytes': 524288, 'maximum_target_bytes': 4194304, 'capacity': 64}
        before = {'ByteWindow': 524288, 'AllocationWindow': 524288, 'PoolReceiveLimit': 8388608,
                  'PoolTotalUnits': 2048, 'ReservationLeases': 2}
        after = dict(before, ByteWindow=2097152, AllocationWindow=2097152) if enabled else dict(before)
        observation = {'ThresholdProgressBytes': 524288, 'Enabled': enabled, 'Maximum': 67108864,
                       'NegotiatedMaximum': 67108864, 'Target': 2097152, 'Before': before, 'After': after,
                       'Outcome': 'granted' if enabled else 'disabled',
                       'PublicationStage': 'byte_complete' if enabled else 'none', 'Error': ''}
        record = {'ordinal': 1, 'sample_unix_ns': 200, 'wrapper_identity': '0xabc',
                  'session_identity': '0xdef', 'stream_id': 1, 'local_address': 'a', 'remote_address': 'b',
                  'observation': observation}
        terminal = dict(startup, phase='terminal-after-join', observed_unix_ns=300,
                        attempts_started=1, attempts_completed=1, active_attempts=0, dropped_events=0,
                        records=[record], sealed=True, producer_joined=True,
                        event_sessions_observed=1, event_sessions_joined=1,
                        event_sessions_live_before_join=0, complete=True, issues=[])
        endpoint = {'subject': process.copy(), 'boot_id': 'boot'}
        lifecycle = [{'event': 'process-exit', 'label': 'fixture-' + role, 'pid': 10,
                      'status': 0, 'forced': False, 'group_joined': True}]
        return startup, terminal, role, enabled, endpoint, lifecycle, {('0xdef', 'a', 'b'): {'streams': {1}, 'maxima': {67108864}, 'maximum_by_stream': {1: {67108864}}}}

    def test_symmetric_disabled_and_real_grant(self):
        for role in ('server', 'agent'):
            for enabled in (False, True):
                result = proof.validate_role(*self.fixture(enabled, role))
                self.assertTrue(result['accepted'])
                self.assertEqual(result['selected_policy'], enabled)
                self.assertEqual(result['outcomes'], {'granted' if enabled else 'disabled': 1})

    def test_unknown_malformed_unjoined_or_wrong_owner_proofs_reject(self):
        for kind in ('wrong-policy', 'unknown-field', 'bool-count', 'dropped', 'active', 'unsealed',
                     'live-session', 'missing-record', 'repeat-wrapper', 'wrong-owner', 'wrong-process',
                     'reversed-clock', 'forced-exit', 'unbounded-error', 'wrong-trigger', 'wrong-ordinal', 'unjoined-stream', 'bad-pool-units', 'invented-ceiling', 'zero-window', 'over-operator-window', 'over-negotiated-allocation', 'unaligned-allocation'):
            args = list(self.fixture()); startup, terminal = args[:2]
            record = terminal['records'][0]; observation = record['observation']
            if kind == 'wrong-policy': startup['selected_policy'] = False
            elif kind == 'unknown-field': terminal['unchecked'] = True
            elif kind == 'bool-count': terminal['attempts_completed'] = True
            elif kind == 'dropped': terminal['dropped_events'] = 1
            elif kind == 'active': terminal['active_attempts'] = 1
            elif kind == 'unsealed': terminal['sealed'] = False
            elif kind == 'live-session': terminal['event_sessions_live_before_join'] = 1
            elif kind == 'missing-record': terminal['records'] = []
            elif kind == 'repeat-wrapper':
                terminal['records'].append(copy.deepcopy(record)); terminal['records'][1]['ordinal'] = 2
                terminal['attempts_started'] = terminal['attempts_completed'] = 2
            elif kind == 'wrong-owner': record['session_identity'] = '0xother'
            elif kind == 'wrong-process': args[4]['subject']['pid'] = 11
            elif kind == 'reversed-clock': record['sample_unix_ns'] = 99
            elif kind == 'forced-exit': args[5][0]['forced'] = True
            elif kind == 'unbounded-error': observation['Error'] = 'public text' * 1000
            elif kind == 'wrong-trigger': observation['ThresholdProgressBytes'] -= 1
            elif kind == 'wrong-ordinal': record['ordinal'] = 2
            elif kind == 'unjoined-stream': record['stream_id'] = 999
            elif kind == 'bad-pool-units': observation['Before']['PoolTotalUnits'] -= 1
            elif kind == 'invented-ceiling': observation['Maximum'] += 4096
            elif kind == 'zero-window': observation['Before']['ByteWindow'] = 0
            elif kind == 'over-operator-window': observation['Before']['ByteWindow'] = observation['Maximum'] + 1
            elif kind == 'over-negotiated-allocation': observation['Before']['AllocationWindow'] = observation['NegotiatedMaximum'] + 4096
            elif kind == 'unaligned-allocation': observation['Before']['AllocationWindow'] += 1
            with self.subTest(kind=kind):
                self.assertRaises(ValueError, proof.validate_role, *args)

    def test_backing_caps_order_and_noop_truthfulness_reject(self):
        for kind in ('pool-growth', 'new-reservation', 'wrong-unit-target', 'cap-violation',
                     'wrong-order', 'fake-grant', 'disabled-publication', 'error-outcome'):
            args = list(self.fixture(kind != 'disabled-publication'))
            observation = args[1]['records'][0]['observation']
            if kind == 'pool-growth': observation['After']['PoolReceiveLimit'] += 4096
            elif kind == 'new-reservation': observation['After']['ReservationLeases'] += 1
            elif kind == 'wrong-unit-target': observation['Target'] -= 1
            elif kind == 'cap-violation': observation['Target'] = 8 << 20
            elif kind == 'wrong-order': observation['PublicationStage'] = 'allocation_complete'
            elif kind == 'fake-grant':
                observation['Before']['ByteWindow'] = observation['Before']['AllocationWindow'] = 2097152
            elif kind == 'disabled-publication': observation['After']['ByteWindow'] += 4096
            elif kind == 'error-outcome': observation.update(Outcome='byte_error', Error='byte_error')
            with self.subTest(kind=kind):
                self.assertRaises(ValueError, proof.validate_role, *args)

    def test_terminal_drain_noop_and_closed_stream_remain_explicit(self):
        for outcome, stage in [('no_larger_target', 'allocation_complete'), ('stream_closed', 'allocation_attempted')]:
            args = list(self.fixture()); observation = args[1]['records'][0]['observation']
            if outcome == 'no_larger_target':
                observation['Before']['ByteWindow'] = observation['Before']['AllocationWindow'] = 2097152
            observation.update(Outcome=outcome, PublicationStage=stage, After=observation['Before'].copy())
            self.assertEqual(proof.validate_role(*args)['outcomes'], {outcome: 1})

    def test_saved_common_pre_null_and_post_stream_shapes(self):
        root = ROOT.parent / 'wan-idle-policy-screen-20261017/runs/idle-101-once/arm-1-idle-0/results.sender-wait-controls'
        pre = proof.read(root / 'sequence-9-pre-capacity.json', 16 << 20)
        post = proof.read(root / 'sequence-9-post-capacity.json', 16 << 20)
        self.assertIsNone(pre['response']['server_receive_growth']['credit_snapshot'])
        envelopes = [proof.read(p, 16 << 20) for p in sorted(root.glob('*-capacity.json'))]
        contexts = proof.owner_keys('server', {}, envelopes)
        self.assertEqual(len(contexts), 4)
        row = post['response']['server_receive_growth']['credit_snapshot']['sessions'][1]
        key = (row['session_identity'], row['snapshot']['local_address'], row['snapshot']['remote_address'])
        self.assertTrue({v['id'] for v in row['snapshot']['streams']} <= contexts[key]['streams'])
        terminal = proof.read(root / 'final-raw-write-terminal.json', 16 << 20)
        senders = [proof.read(p, 16 << 20) for p in sorted(root.glob('*-sender.json'))]
        agent = proof.owner_keys('agent', terminal, senders)
        self.assertEqual(len(agent), 4)
        for envelope in senders:
            for event in envelope['response']['events']:
                if event['lane'] in (1, 2, 3, 4):
                    key = (event['session_identity'], event['local_address'], event['remote_address'])
                    self.assertIn(event['stream_id'], agent[key]['streams'])
                    self.assertIn(event['maximum'], agent[key]['maxima'])


if __name__ == '__main__':
    unittest.main()
