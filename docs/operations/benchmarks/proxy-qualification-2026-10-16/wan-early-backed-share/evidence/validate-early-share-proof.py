#!/usr/bin/env python3
"""Pure checker for bounded startup/post-join sidecars; no endpoint operations."""
from pathlib import Path
import json
import re
import stat

ORDER = (False, True, False)
THRESHOLD = 512 << 10
CAP = 4 << 20
UNIT = 4096
CAPACITY = 64
PROCESS = {'pid', 'start_ticks', 'namespace', 'namespace_device', 'namespace_inode', 'pid_namespace', 'boot_id'}
STATE = {'ByteWindow', 'AllocationWindow', 'PoolReceiveLimit', 'PoolTotalUnits', 'ReservationLeases'}
OBSERVATION = {'ThresholdProgressBytes', 'Enabled', 'Maximum', 'NegotiatedMaximum', 'Target', 'Before', 'After', 'Outcome', 'PublicationStage', 'Error'}
RECORD = {'ordinal', 'sample_unix_ns', 'wrapper_identity', 'session_identity', 'stream_id', 'local_address', 'remote_address', 'observation'}
STARTUP = {'schema_version', 'phase', 'role', 'selected_policy', 'process', 'observed_unix_ns', 'threshold_bytes', 'maximum_target_bytes', 'capacity'}
TERMINAL = STARTUP | {'attempts_started', 'attempts_completed', 'active_attempts', 'dropped_events', 'records', 'sealed', 'producer_joined', 'event_sessions_observed', 'event_sessions_joined', 'event_sessions_live_before_join', 'complete', 'issues'}


def require(value, message):
    if not value:
        raise ValueError(message)


def integer(value, positive=False):
    return type(value) is int and value >= int(positive)


def read(path, limit=256 << 10):
    path = Path(path)
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and not path.is_symlink() and 0 < info.st_size <= limit,
            'proof input type/byte bound differs')
    return json.loads(path.read_text())


def owner_keys(role, terminal, envelopes):
    owners = {}
    def add(key):
        return owners.setdefault(key, {'streams': set(), 'maxima': set(), 'maximum_by_stream': {}})
    if role == 'agent':
        for record in terminal['records']:
            if record['lane'] in (1, 2, 3, 4):
                raw = record['raw_write_activity']
                add((record['session_identity'], raw['local_address'], raw['remote_address']))
    for envelope in envelopes:
        response = envelope['response']
        growth = response if role == 'agent' else response['server_receive_growth']
        for row in growth['byte_credit_traces']:
            if row['lane'] not in (1, 2, 3, 4):
                continue
            key = (row['session_identity'], row['local_address'], row['remote_address'])
            context = add(key)
            for kind in ('receiver_records', 'peer_records', 'wait_records'):
                context['streams'].update(v['stream_id'] for v in row['trace'][kind])
        # The original server contract deliberately has a null PRE snapshot.
        snapshot = growth.get('credit_snapshot')
        if snapshot is not None:
            require(role == 'server' and growth['phase'] == 'post' and type(snapshot) is dict,
                    'unexpected common credit snapshot phase/type')
            for row in snapshot['sessions']:
                if row['lane'] in (1, 2, 3, 4):
                    snap = row['snapshot']
                    add((row['session_identity'], snap['local_address'], snap['remote_address']))['streams'].update(v['id'] for v in snap['streams'])
        for event in growth['events']:
            if event['lane'] not in (1, 2, 3, 4):
                continue
            context = add((event['session_identity'], event['local_address'], event['remote_address']))
            context['streams'].add(event['stream_id'])
            context['maxima'].add(event['maximum'])
            context['maximum_by_stream'].setdefault(event['stream_id'], set()).add(event['maximum'])
    require(len(owners) == 4 and all(v['streams'] and v['maxima'] for v in owners.values()),
            'four complete common data-owner/stream/ceiling contexts required')
    return owners


def validate_role(startup, terminal, role, expected, endpoint, lifecycle, owners):
    require(type(startup) is dict and set(startup) == STARTUP and type(terminal) is dict and set(terminal) == TERMINAL,
            'early-share sidecar fields differ')
    for value, phase in ((startup, 'startup-before-endpoints'), (terminal, 'terminal-after-join')):
        require(type(value['schema_version']) is int and value['schema_version'] == 1
                and value['phase'] == phase and value['role'] == role
                and type(value['selected_policy']) is bool and value['selected_policy'] is expected,
                'independent early-share startup/terminal role/policy differs')
        require(all(type(value[k]) is int and value[k] == v for k, v in
                    [('threshold_bytes', THRESHOLD), ('maximum_target_bytes', CAP), ('capacity', CAPACITY)]),
                'fixed trigger/cap/record bound differs')
        require(integer(value['observed_unix_ns'], True), 'sidecar clock malformed')
        process = value['process']
        require(type(process) is dict and set(process) == PROCESS
                and all(integer(process[k], True) for k in ['pid', 'start_ticks', 'namespace_inode'])
                and integer(process['namespace_device'])
                and all(type(process[k]) is str and process[k] for k in ['namespace', 'pid_namespace', 'boot_id']),
                'sidecar process/namespace identity malformed')
        subject = endpoint['subject']
        require(all(process[k] == subject[k] for k in ['pid', 'start_ticks', 'namespace', 'namespace_device', 'namespace_inode'])
                and process['boot_id'] == endpoint['boot_id'], 'sidecar endpoint/namespace ownership differs')
    require(startup['process'] == terminal['process'] and startup['observed_unix_ns'] <= terminal['observed_unix_ns'],
            'sidecar process replaced or timestamps reverse')
    exits = [v for v in lifecycle if v.get('event') == 'process-exit' and v.get('label', '').endswith('-' + role)]
    require(len(exits) == 1 and exits[0]['pid'] == startup['process']['pid']
            and type(exits[0]['status']) is int and exits[0]['status'] == 0
            and exits[0]['forced'] is False and exits[0]['group_joined'] is True,
            'early-share endpoint did not join naturally zero')
    records = terminal['records']
    require(type(records) is list and 0 < len(records) <= CAPACITY, 'missing or over-bound attempt records')
    require(terminal['complete'] is True and terminal['issues'] == [] and terminal['sealed'] is True
            and terminal['producer_joined'] is True
            and all(type(terminal[k]) is int and terminal[k] == 0 for k in
                    ['active_attempts', 'dropped_events', 'event_sessions_live_before_join'])
            and all(type(terminal[k]) is int and terminal[k] == len(records) for k in
                    ['attempts_started', 'attempts_completed']), 'incomplete/dropped/active attempt proof')
    wrappers = set()
    sessions = set()
    outcomes = {}
    for ordinal, record in enumerate(records, 1):
        require(type(record) is dict and set(record) == RECORD and type(record['ordinal']) is int
                and record['ordinal'] == ordinal, 'record shape/ordinal differs')
        require(integer(record['sample_unix_ns'], True)
                and startup['observed_unix_ns'] <= record['sample_unix_ns'] <= terminal['observed_unix_ns'],
                'attempt-start sample outside endpoint sidecar lifetime')
        require(type(record['wrapper_identity']) is str and re.fullmatch(r'0x[0-9a-f]+', record['wrapper_identity'])
                and record['wrapper_identity'] not in wrappers and integer(record['stream_id'], True),
                'wrapper attempt repeats or stream identity malformed')
        wrappers.add(record['wrapper_identity'])
        owner = (record['session_identity'], record['local_address'], record['remote_address'])
        require(owner in owners, 'early-share event is not an existing data owner')
        context = owners[owner]
        require(record['stream_id'] in context['streams'], 'early-share stream lacks existing common credit/stream evidence')
        sessions.add(record['session_identity'])
        observation = record['observation']
        require(type(observation) is dict and set(observation) == OBSERVATION
                and type(observation['Enabled']) is bool and observation['Enabled'] is expected
                and type(observation['ThresholdProgressBytes']) is int
                and observation['ThresholdProgressBytes'] == THRESHOLD,
                'record mode/threshold/schema differs')
        for state in (observation['Before'], observation['After']):
            require(type(state) is dict and set(state) == STATE
                    and all(integer(v) and v <= (1 << 32) - 1 for v in state.values()),
                    'backing/window fields malformed')
        before, after = observation['Before'], observation['After']
        require(0 < before['PoolReceiveLimit'] <= 1 << 30
                and before['PoolReceiveLimit'] % UNIT == 0
                and before['PoolTotalUnits'] * UNIT == before['PoolReceiveLimit'],
                'pool byte/unit dimensions violate negotiated backing contract')
        require(all(before[k] == after[k] for k in ['PoolReceiveLimit', 'PoolTotalUnits', 'ReservationLeases']),
                'early-share changed backing/reservation ownership')
        maxima = context['maximum_by_stream'].get(record['stream_id'], context['maxima'])
        require(integer(observation['Maximum'], True) and integer(observation['NegotiatedMaximum'], True)
                and observation['Maximum'] <= observation['NegotiatedMaximum'] <= 256 << 20
                and observation['Maximum'] in maxima,
                'operator/negotiated ceilings malformed')
        require(all(0 < state['ByteWindow'] <= observation['Maximum']
                    and 0 < state['AllocationWindow'] <= observation['NegotiatedMaximum']
                    and state['AllocationWindow'] % UNIT == 0 for state in (before, after)),
                'byte/allocation windows violate operator/negotiated unit bounds')
        target = min(CAP, before['PoolReceiveLimit'] // 4, observation['Maximum'], observation['NegotiatedMaximum']) // UNIT * UNIT
        require(type(observation['Target']) is int and observation['Target'] == target,
                'target differs from exact backed/4MiB/operator/negotiated unit floor')
        outcome, stage, error = observation['Outcome'], observation['PublicationStage'], observation['Error']
        require(outcome in {'disabled', 'no_larger_target', 'stream_closed', 'granted'} and error == '',
                'invalid-owner/shutdown/publication-error/unknown early outcome')
        if not expected:
            require(outcome == 'disabled' and stage == 'none' and after == before, 'disabled control published credit')
        elif outcome == 'stream_closed':
            require(stage == 'allocation_attempted' and after == before, 'closed stream granted credit')
        elif outcome == 'no_larger_target':
            require(stage == 'allocation_complete' and target <= min(before['ByteWindow'], before['AllocationWindow'])
                    and after == before, 'no-op changed credit or had a larger target')
        else:
            require(outcome == 'granted' and stage == 'byte_complete'
                    and target > min(before['ByteWindow'], before['AllocationWindow'])
                    and after['ByteWindow'] == max(target, before['ByteWindow'])
                    and after['AllocationWindow'] == max(target, before['AllocationWindow']),
                    'grant lacks exact allocation-before-byte completion')
        outcomes[outcome] = outcomes.get(outcome, 0) + 1
    require(all(type(terminal[k]) is int and terminal[k] == len(sessions) for k in
                ['event_sessions_observed', 'event_sessions_joined']), 'retained event-session join incomplete')
    return {'accepted': True, 'role': role, 'selected_policy': expected, 'attempts': len(records),
            'outcomes': outcomes, 'physical_data_owners': len(owners),
            'sample_timestamp_semantics': 'growth-call sample/attempt start, not publication completion'}


def validate(root, index):
    require(type(index) is int and 0 <= index < 3, 'independent arm index differs')
    root = Path(root)
    controls = root / 'results.sender-wait-controls'
    terminal = read(controls / 'final-raw-write-terminal.json', 16 << 20)
    capacities = [read(p, 16 << 20) for p in sorted(controls.glob('*-capacity.json'))]
    senders = [read(p, 16 << 20) for p in sorted(controls.glob('*-sender.json'))]
    endpoint = read(root / 'idle-endpoints-endpoint-binding.json', 16 << 20)
    lifecycle_path = root / 'results.lifecycle.jsonl'
    info = lifecycle_path.lstat()
    require(stat.S_ISREG(info.st_mode) and not lifecycle_path.is_symlink() and 0 < info.st_size <= 16 << 20,
            'canonical lifecycle type/16MiB bound differs')
    lifecycle = [json.loads(line) for line in lifecycle_path.read_text().splitlines()]
    roles = [validate_role(read(root / f'early-share-{role}-startup.json'),
                           read(root / f'early-share-{role}-terminal.json'), role, ORDER[index],
                           endpoint['roles'][role], lifecycle, owner_keys(role, terminal, senders if role == 'agent' else capacities))
             for role in ('server', 'agent')]
    if ORDER[index]:
        require(all(role['outcomes'].get('granted', 0) > 0 for role in roles),
                'candidate never actually granted early backed credit in both receiver roles')
    return {'accepted': True, 'arm_index': index, 'selected_policy': ORDER[index], 'roles': roles}
