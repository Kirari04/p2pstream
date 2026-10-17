#!/usr/bin/env python3
"""Finite offline qualification adapter controls; no sockets/endpoints/traffic."""
from pathlib import Path
import contextlib
import copy
import hashlib
import importlib.util
import io
import json
import runpy
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch
ROOT=Path(__file__).resolve().parents[1]
OLD=ROOT.parent/'continuation-20261016/current-wan-prep'
def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path);value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value
D=module('original96_defaults',ROOT/'prove-default-transport.py')
G=module('original96_runtime_guard',ROOT/'verify-runtime-inputs.py')
def write(path,value):path.parent.mkdir(parents=True,exist_ok=True);path.write_text(json.dumps(value)+'\n')
class Boundaries(unittest.TestCase):
    def defaults(self):
        def identity(pid,net,pidns=20):return {'pid':pid,'start_ticks':pid*100,'namespace':f'net:[{net}]','namespace_device':5,'namespace_inode':net,'pid_namespace':f'pid:[{pidns}]'}
        def sample(pid,net,when,parent,host=False):return {**identity(pid,net,1 if host else 20),'parent':copy.deepcopy(parent),'controller_default':'cubic','tcp_slow_start_after_idle':1,'monotonic_ns':when,'boot_id':'boot'}
        meta={'schema_version':1,'read_only':True,'point_in_time_only':True,'socket_controller_observed':False}
        host_parent=identity(800,13,1)
        hp={**meta,'sample':sample(801,13,1,host_parent,True)};hz={**meta,'sample':sample(802,13,20,host_parent,True)}
        pre={**meta,'phase':'pre','runner':sample(2,10,2,identity(1,10)),'agent_origin':sample(4,11,3,identity(2,10)),'router':sample(7,12,4,identity(2,10))}
        post={**meta,'phase':'post','runner':sample(3,10,10,identity(1,10)),'agent_origin':sample(8,11,11,identity(3,10)),'router':sample(9,12,12,identity(3,10))}
        for value in [pre,post]:value['agent_origin']['subject']=identity(5,11);value['router']['subject']=identity(6,12)
        outer={'boot_id':'boot','shell_pid':1,'owned':{'pid':'pid:[20]','net':'net:[99]'},'parent':{'pid':'pid:[1]','net':'net:[13]'}}
        lifecycle=[{'event':'namespace-helpers-start','boot_id':'boot','runner_namespace':'net:[10]','agent':identity(5,11),'router':identity(6,12)}]
        lifecycle += [{'event':'process-exit','label':role+'-namespace','pid':pid,'status':0,'forced':False,'group_joined':True} for role,pid in [('agent',5),('router',6)]]
        return [pre,post,hp,hz,outer,lifecycle]
    def test_nested_default_scope_and_typed_identity_failures(self):
        result=D.validate(*self.defaults());self.assertTrue(result['accepted']);self.assertFalse(result['socket_controller_observed'])
        for kind in ['bool-idle','wrong-controller','wrong-boot','stale-helper','host-parent-change','reverse-time','outer-net-confusion','forced-helper']:
            v=self.defaults()
            if kind=='bool-idle':v[0]['runner']['tcp_slow_start_after_idle']=True
            elif kind=='wrong-controller':v[1]['agent_origin']['controller_default']='reno'
            elif kind=='wrong-boot':v[0]['runner']['boot_id']='other'
            elif kind=='stale-helper':v[1]['router']['subject']['start_ticks']+=1
            elif kind=='host-parent-change':v[3]['sample']['parent']['start_ticks']+=1
            elif kind=='reverse-time':v[1]['runner']['monotonic_ns']=1
            elif kind=='outer-net-confusion':v[5][0]['runner_namespace']=v[4]['owned']['net']
            elif kind=='forced-helper':v[5][1]['forced']=True
            with self.subTest(kind=kind):self.assertRaises(ValueError,D.validate,*v)
    def guard_fixture(self,temporary):
        root=Path(temporary)
        for name in ['provenance/stability-manifest.json','provenance/LEGACY-REUSE.json','INPUTS.json','REVIEWED-BUILD-RECEIPT-SHA256','runner/qualify-proxy-agent-growth-owned.sh','runner/run-owned-wan-stability.sh','runner/validate-owned-h2-stability.py','prove-default-transport.py']:
            target=root/name;target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(ROOT/name,target)
        files=[{'path':str(root/'prove-default-transport.py'),'sha256':G.sha(root/'prove-default-transport.py')}]
        write(root/'SOURCE.json',{'files':files})
        write(root/'RUNTIME-GRANT.json',{'source_manifest_sha256':G.sha(root/'SOURCE.json'),'runtime_approved':True,'maximum_transfers':96,'maximum_pairs':3})
        return root
    def test_reviewed_current_and_legacy_tuple_and_wrong_identities(self):
        # This invokes only the accepted read-only build verifier, not Go or any
        # endpoint. The mini runtime manifest is isolated in this temp directory.
        with tempfile.TemporaryDirectory() as temporary:
            root=self.guard_fixture(temporary)
            with patch.object(G,'ROOT',root):
                values=G.verify(root/'provenance/stability-manifest.json')
                self.assertEqual(len(values),5);self.assertIn('public-route-idle-reuse-source-',values[3])
                original=json.loads((root/'provenance/stability-manifest.json').read_text())
                for kind in ['historical-current-ref','wrong-legacy','wrong-runner','wrong-receipt']:
                    value=copy.deepcopy(original)
                    if kind=='historical-current-ref':value['source']['current_ref']='8ac4c8df8237e37777ba2fac0da6f949da17bdea'
                    elif kind=='wrong-legacy':value['sha256']['legacy_agent_binary']='0'*64
                    elif kind=='wrong-runner':value['sha256']['owned_runner']='0'*64
                    else:value['current_build_receipt_sha256']='0'*64
                    write(root/'provenance/stability-manifest.json',value)
                    with self.subTest(kind=kind):self.assertRaises(ValueError,G.verify,root/'provenance/stability-manifest.json')
                write(root/'provenance/stability-manifest.json',original)
                write(root/'RUNTIME-GRANT.json',{'runtime_approved':False})
                self.assertRaises(ValueError,G.verify,root/'provenance/stability-manifest.json')
    def test_tuple_failure_cannot_be_hidden_by_five_partial_lines(self):
        import subprocess
        script='set -Eeuo pipefail\npython3 -c \'import sys; print("line\\n"*5,end=""); sys.exit(7)\' > "$1" || exit 125\nmapfile -t binding < "$1"\nprintf endpoint-started\n'
        with tempfile.TemporaryDirectory() as temporary:
            completed=subprocess.run(['bash','-c',script,'bash',str(Path(temporary)/'binding')],capture_output=True,text=True,timeout=5)
            self.assertEqual(completed.returncode,125);self.assertEqual(completed.stdout,'')
        wrapper=(ROOT/'runner/run-owned-wan-stability.sh').read_text()
        self.assertIn('> "$output_dir/verified-binding.txt" ||',wrapper)
        self.assertIn('mapfile -t binding < "$output_dir/verified-binding.txt"',wrapper)
    def validator_case(self,temporary,bad_pair=False,runner_status=0):
        # Shape-only source from the already retained positive validator fixture;
        # all rates/provenance below are synthetic, never historical controls.
        root=Path(temporary);saved=OLD/'contracts/stability-validator/final-cases/pass'
        if not saved.exists():saved=ROOT.parents[1]/'docs/operations/benchmarks/proxy-qualification-2026-10-16/current-production-wan/contracts/stability-validator/final-cases/pass'
        rows=[json.loads(line) for line in (saved/'results.jsonl').read_text().splitlines()]
        manifest=json.loads((ROOT/'provenance/stability-manifest.json').read_text())
        for i,row in enumerate(rows):
            rate=32_000_000
            if row['transport_mode']=='aggregate-v1':rate=28_000_000 if bad_pair and i<32 and row['transfer_direction']=='download' else 36_000_000
            row['useful_transfer_bytes_per_second']=rate;row['wall_seconds']=268435456/rate
            row['provenance'].update(server_source_ref=manifest['source']['current_ref'],aggregate_agent_source_ref=manifest['source']['current_ref'],legacy_agent_source_ref=manifest['source']['legacy_ref'],server_binary_sha256=manifest['sha256']['current_server_binary'],aggregate_agent_binary_sha256=manifest['sha256']['current_agent_binary'],legacy_agent_binary_sha256=manifest['sha256']['legacy_agent_binary'],harness_sha256=manifest['sha256']['owned_runner'])
        (root/'results.jsonl').write_text(''.join(json.dumps(v)+'\n' for v in rows))
        for name in ['results.lifecycle.jsonl','outer.json']:shutil.copyfile(saved/name,root/name)
        supervisor=json.loads((saved/'supervisor.json').read_text());supervisor['runner_status']=runner_status;write(root/'supervisor.json',supervisor);write(root/'manifest.json',manifest)
        argv=['validator',*[str(root/name) for name in ['results.jsonl','results.lifecycle.jsonl','attestation.json','outer.json','supervisor.json','manifest.json']]]
        status=0
        with patch.object(sys,'argv',argv),contextlib.redirect_stdout(io.StringIO()),contextlib.redirect_stderr(io.StringIO()):
            try:runpy.run_path(str(ROOT/'runner/validate-owned-h2-stability.py'),run_name='__main__')
            except SystemExit as error:status=error.code
        return status,json.loads((root/'attestation.json').read_text())
    def test_complete_original96_pair_gates_and_original_pooled_status_required(self):
        for bad_pair,runner_status,accepted in [(False,0,True),(True,0,False),(False,1,False)]:
            with tempfile.TemporaryDirectory() as temporary:
                status,value=self.validator_case(temporary,bad_pair,runner_status)
                self.assertEqual(value['row_count'],96);self.assertEqual(value['accepted'],accepted)
                self.assertEqual(status==0,accepted);self.assertEqual(len(value['pair_gates']),3)
                if bad_pair:self.assertFalse(value['pair_gates'][0]['download']['pass'])
    def test_original_timed_workload_and_pair_order_are_preserved(self):
        original=(OLD/'runner/qualify-proxy-agent-growth-owned.sh').read_text();current=(ROOT/'runner/qualify-proxy-agent-growth-owned.sh').read_text()
        start=original.index('run_transfer() {');end=original.index('\nrun_tiny_response()',start)
        self.assertIn(original[start:end],current)
        start=original.index('run_cohort() {');end=original.index('stop_process origin ',start)
        # Only the single terminal readback appears after the original cohort
        # loop; the complete warmup/pool/order body itself is byte-identical.
        body=original[start:end];self.assertIn(body,current)
        self.assertIn('close_idle_agent_transports "$server_admin"',body)
        self.assertIn('wait_aggregate_lanes_grown_and_quiescent',body)
        self.assertNotIn('p2pstream_qualification',(ROOT/'provenance/stability-manifest.json').read_text())
if __name__=='__main__':unittest.main()
