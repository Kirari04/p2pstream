#!/usr/bin/env python3
"""Focused adversarial controls. Prepared only; execute after root approval."""
from pathlib import Path
import copy,hashlib,importlib.util,json,unittest
ROOT=Path(__file__).resolve().parents[1];OLD=ROOT.parent/'wan-terminal-attribution-20261017'
P=ROOT/'runner-source/integration'
spec=importlib.util.spec_from_file_location('screen',P/'validate-controller-screen.py');screen=importlib.util.module_from_spec(spec);spec.loader.exec_module(screen)
class Decisions(unittest.TestCase):
 def setUp(self):
  run=OLD/'runner-source/integration/runs/terminal-sustained-once/h2'
  self.rows=[json.loads(l) for l in (run/'results.jsonl').read_text().splitlines()];self.att=json.loads((run/'attestation.json').read_text())
 def arm(self,index):
  rows=copy.deepcopy(self.rows);att=copy.deepcopy(self.att)
  for r in rows:
   n=r['diagnostic_sequence'];r['useful_transfer_bytes_per_second']=28_000_000 if n in [11,14] and index!=1 else 36_000_000
   if n in [11,14]:r['response_copy_attribution']['matches'][0]['copy']['upstream_read']['total_ns']=7_000_000_000 if index==1 else 9_000_000_000
  if index==1:att['performance_issues']=[];att['accepted']=True
  return rows,att
 def test_full_bracket_positive(self):
  arms=[screen.decide_arm(i,*self.arm(i)) for i in range(3)];self.assertTrue(screen.decide_screen(arms)['accepted'])
 def test_every_stop_and_outcome_boundary(self):
  for index in range(3):
   for field in ['integrity_issues','conditioning_issues']:
    rows,a=self.arm(index);a[field]=['failed'];self.assertRaises(ValueError,screen.decide_arm,index,rows,a)
   rows,a=self.arm(index);self.assertRaises(ValueError,screen.decide_arm,index,rows[:-1],a)
  for n in [11,14]:
   for index in [0,2]:
    rows,a=self.arm(index);rows[n-1]['useful_transfer_bytes_per_second']=screen.FLOOR;self.assertRaises(ValueError,screen.decide_arm,index,rows,a)
   rows,a=self.arm(1);a['performance_issues']=['candidate floor failed'];self.assertRaises(ValueError,screen.decide_arm,1,rows,a)
  rows,a=self.arm(0);rows[14]['useful_transfer_bytes_per_second']=screen.FLOOR-1;self.assertRaises(ValueError,screen.decide_arm,0,rows,a)
  rows,a=self.arm(1);rows[10]['useful_transfer_bytes_per_second']=float('nan');self.assertRaises(ValueError,screen.decide_arm,1,rows,a)
  arms=[screen.decide_arm(i,*self.arm(i)) for i in range(3)]
  self.assertRaises(ValueError,screen.decide_screen,arms[:2])
  for n in ['11','14']:
   for kind in ['rate','read','order']:
    bad=copy.deepcopy(arms)
    if kind=='rate':bad[1]['speeds'][int(n)]=1.08*max(bad[0]['speeds'][int(n)],bad[2]['speeds'][int(n)])-1
    if kind=='read':bad[1]['read_ns'][n]=bad[0]['read_ns'][n]
    if kind=='order':bad[1]['arm_index']=2
    self.assertRaises(ValueError,screen.decide_screen,bad)
class Bindings(unittest.TestCase):
 def setUp(self):
  run=OLD/'runner-source/integration/runs/terminal-sustained-once/h2';c=run/'results.sender-wait-controls'
  self.terminal=json.loads((c/'final-raw-write-terminal.json').read_text());self.envelopes=[json.loads(p.read_text()) for p in sorted(c.glob('*-sender.json'))]
  self.ns=next(json.loads(l) for l in (run/'results.lifecycle.jsonl').read_text().splitlines() if json.loads(l)['event']=='namespace-helpers-start')
  self.side={'schema_version':1,'requested':'cubic','namespace':self.ns['agent']['namespace'],'parent_namespace':self.ns['runner_namespace'],'default':'cubic','available':['reno','cubic'],'allowed':['reno','cubic'],'bindings':[]}
  for r in self.terminal['records']:
   v=r['raw_write_activity'];self.side['bindings'].append({k:v[k] for k in ['lane','generation','local_address','remote_address']}|{'requested':'cubic','readback':'cubic','explicit_set':v['lane']>0,'before_tls':True})
 def validate(self,side=None,term=None,env=None,expected='cubic'):
  screen.validate_bindings(side or self.side,term or self.terminal,env or self.envelopes,self.ns,expected)
 def test_baseline_and_independent_expected(self):
  self.validate();self.assertRaises(ValueError,self.validate,expected='reno')
 def test_selection_owner_namespace_and_snapshot_rejection(self):
  for kind in ['enum','lane0','readback','lane','generation','tuple','beforetls','set','missing','namespace','allowed','default']:
   b=copy.deepcopy(self.side)
   if kind=='enum':b['requested']='CUBIC'
   if kind=='lane0':b['bindings'][0]['readback']='reno'
   if kind=='readback':b['bindings'][2]['readback']='reno'
   if kind=='lane':b['bindings'][2]['lane']=3
   if kind=='generation':b['bindings'][2]['generation']+=1
   if kind=='tuple':b['bindings'][2]['local_address']='attacker'
   if kind=='beforetls':b['bindings'][2]['before_tls']=False
   if kind=='set':b['bindings'][2]['explicit_set']=False
   if kind=='missing':b['bindings'].pop()
   if kind=='namespace':b['namespace']=b['parent_namespace']
   if kind=='allowed':b['allowed']=['cubic']
   if kind=='default':b['default']='reno'
   self.assertRaises((ValueError,KeyError),self.validate,b)
  env=copy.deepcopy(self.envelopes);env[0]['response']['send_counters'][2]['raw_write_activity']['tcp_state']['bind_tcp_info']['congestion_control']='reno';self.assertRaises(ValueError,self.validate,env=env)
class SourceBindings(unittest.TestCase):
 def test_root_sidecar_preserves_mature_exact_control_set(self):
  import shutil,subprocess,sys,tempfile
  oldrun=OLD/'runner-source/integration/runs/terminal-sustained-once/h2'
  with tempfile.TemporaryDirectory() as temporary:
   run=Path(temporary)/'arm';shutil.copytree(oldrun,run)
   before={p.name for p in (run/'results.sender-wait-controls').iterdir()}
   def evaluate(name):
    att=run/name
    args=[sys.executable,'-I','-B',str(P/'validate-wan-below-tls-arm.py')]+[str(run/x) for x in ['results.jsonl','results.lifecycle.jsonl','results.summary.json','outer-namespaces.json','supervisor-lifecycle.json','pre-hashes.json','post-hashes.json']]+[str(att),'h2','sustained']
    status=subprocess.run(args,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=20)
    self.assertEqual(status.returncode,1)
    return json.loads(att.read_text())
   relocated=evaluate('compatibility-before.json')
   original=json.loads((oldrun/'attestation.json').read_text())
   # Relocation alone changes the mature validator's path-bound facts. Preserve
   # that discrepancy; isolate only the effect of adding the root sidecar.
   self.assertNotEqual(relocated,original)
   (run/'controller-selection.json').write_text('{"schema_version":1}')
   self.assertEqual(before,{p.name for p in (run/'results.sender-wait-controls').iterdir()})
   with_sidecar=evaluate('compatibility-after.json')
   self.assertEqual(with_sidecar,relocated) # no additional integrity/control-set discrepancy
 def test_unmodified_mature_validators(self):
  for p in P.glob('validate-*.py'):
   if p.name.startswith('validate-controller'):continue
   self.assertEqual(p.read_bytes(),(OLD/'runner-source/integration'/p.name).read_bytes())
 def test_runner_namespace_and_receipt_guard(self):
  source=(P/'generated/qualify-proxy-agent-growth-wan-below-tls.sh').read_text()
  self.assertEqual(source.count('P2PSTREAM_QUALIFICATION_DATA_CONTROLLER="$P2PSTREAM_QUALIFICATION_DATA_CONTROLLER"'),2)
  self.assertEqual(source.count('P2PSTREAM_QUALIFICATION_CONTROLLER_NETNS="$agent_namespace_identity"'),2)
  runner=(P/'run-wan-below-tls-arm.sh').read_text();self.assertIn('BLOCKED_PENDING_REVIEWED_CONTROLLER_BUILD_RECEIPT',runner)
  self.assertIn(hashlib.sha256((P/'generated/qualify-proxy-agent-growth-wan-below-tls.sh').read_bytes()).hexdigest(),runner)
 def test_independent_source_copy(self):
  for row in json.loads((ROOT/'BASE-SOURCE-INVENTORY.json').read_text())['files']:
   self.assertNotEqual((ROOT/row['path']).stat().st_ino,(OLD/row['path']).stat().st_ino)
class ContinuationProofs(unittest.TestCase):
 def setUp(self):
  import tempfile
  self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
  spec=importlib.util.spec_from_file_location('receipt',P/'verify-terminal-build.py');self.receipt=importlib.util.module_from_spec(spec);spec.loader.exec_module(self.receipt)
  self.receipt.ROOT=Path(self.temp.name);self.current='a'*64
 def fixture(self,reused):
  name='preflight' if reused else 'python-contracts';origin='reused-original-freeze' if reused else 'fresh-continuation-1'
  directory=self.receipt.ROOT/('build' if reused else 'build/continuation-1');directory.mkdir(parents=True,exist_ok=True)
  command={'name':name,'argv':['fixture-command'],'cwd':'/fixture','proof_origin':origin,'log_directory':str(directory),'max_output_bytes':1024,'max_stdout_bytes':1024,'max_stderr_bytes':1024}
  paths={kind:directory/(name+'.'+suffix) for kind,suffix in [('command_proof','command-proof.json'),('outer','outer.json'),('started','started.json'),('stdout','stdout.log'),('stderr','stderr.log')]}
  paths['stdout'].write_text('ok\n');paths['stderr'].write_text('')
  outer={'argv':command['argv'],'status':0,'forced':False,'remaining_owned':[],'adopted_descendants':[],'accepted_cleanup':True,'stdout_bytes':3,'stderr_bytes':0}
  paths['outer'].write_text(json.dumps(outer));paths['started'].write_text(json.dumps({'argv':command['argv']}))
  sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
  proof={k:command[k] for k in ['name','argv','cwd']};proof.update({'exit_status':0,'forced':False,'remaining_group_members':[],'adopted_descendants':[],'outer_audit_sha256':sha(paths['outer']),'stdout_sha256':sha(paths['stdout']),'stderr_sha256':sha(paths['stderr']),'stdout_bytes':3,'stderr_bytes':0,'output_bytes':3})
  paths['command_proof'].write_text(json.dumps(proof))
  row=proof|{'proof_origin':origin,'log_directory':str(directory),'execution_source_freeze_sha256':self.receipt.ORIGINAL_FREEZE if reused else self.current,'command_proof_sha256':sha(paths['command_proof'])}
  if reused:command['reused_proof']={'execution_source_freeze_sha256':self.receipt.ORIGINAL_FREEZE,'files':{kind:{'path':str(path),'sha256':sha(path)} for kind,path in paths.items()}}
  return command,row,paths
 def test_reused_and_fresh_origins_are_explicit(self):
  for reused in [True,False]:
   command,row,_=self.fixture(reused);self.assertEqual(self.receipt.verify_command(command,row,self.current) is not None,reused)
   for key,value in [('proof_origin','fresh-continuation-1' if reused else 'reused-original-freeze'),('log_directory','/wrong'),('execution_source_freeze_sha256','b'*64),('command_proof_sha256','c'*64)]:
    bad=copy.deepcopy(row);bad[key]=value;self.assertRaises(ValueError,self.receipt.verify_command,command,bad,self.current)
 def test_reused_file_hashes_and_paths_are_immutable(self):
  command,row,paths=self.fixture(True)
  for kind in paths:
   bad=copy.deepcopy(command);bad['reused_proof']['files'][kind]['sha256']='d'*64;self.assertRaises(ValueError,self.receipt.verify_command,bad,row,self.current)
   bad=copy.deepcopy(command);bad['reused_proof']['files'][kind]['path']='/wrong';self.assertRaises(ValueError,self.receipt.verify_command,bad,row,self.current)
  paths['started'].write_text('{"argv":["tampered"]}');self.assertRaises(ValueError,self.receipt.verify_command,command,row,self.current)
 def test_fresh_log_tamper_and_remaining_owner_reject(self):
  command,row,paths=self.fixture(False);paths['stdout'].write_text('tampered');self.assertRaises(ValueError,self.receipt.verify_command,command,row,self.current)
  for key,value in [('adopted_descendants',[{'pid':1}]),('remaining_group_members',[1]),('remaining_owned',[1]),('remaining_after_cleanup',[1])]:
   command,row,paths=self.fixture(False);proof=json.loads(paths['command_proof'].read_text());proof[key]=value;paths['command_proof'].write_text(json.dumps(proof));row.update(proof);row['command_proof_sha256']=hashlib.sha256(paths['command_proof'].read_bytes()).hexdigest();self.assertRaises(ValueError,self.receipt.verify_command,command,row,self.current)
class Supervisor(unittest.TestCase):
 def setUp(self):
  import tempfile
  self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
  spec=importlib.util.spec_from_file_location('outer',ROOT/'run-controller-screen.py');self.outer=importlib.util.module_from_spec(spec);spec.loader.exec_module(self.outer)
  self.outer.ROOT=Path(self.temp.name)
 def command(self,code):
  import sys
  return [sys.executable,'-I','-B','-c',code]
 def test_inventory_cadence_is_frozen(self):
  self.assertEqual(self.outer.INVENTORY_SECONDS,1.0);self.assertEqual(self.outer.POLL_SECONDS,.05)
 def test_natural_exit_and_unknown_status(self):
  import os
  self.assertEqual(self.outer.execute_owned(self.command('import time;time.sleep(.1)'),os.environ.copy(),self.outer.ROOT/'natural',2),0)
  self.assertRaises(RuntimeError,self.outer.execute_owned,self.command('import time;time.sleep(.1);raise SystemExit(42)'),os.environ.copy(),self.outer.ROOT/'bad-status',2)
  self.assertEqual(json.loads((self.outer.ROOT/'bad-status.outer.json').read_text())['status'],42)
 def test_rapid_parent_exit_with_separate_session_survivor(self):
  import os,sys
  code='import subprocess;subprocess.Popen(['+repr(sys.executable)+',"-c","import time;time.sleep(30)"],start_new_session=True)'
  self.assertRaises(RuntimeError,self.outer.execute_owned,self.command(code),os.environ.copy(),self.outer.ROOT/'rapid-exit',2)
  record=json.loads((self.outer.ROOT/'rapid-exit.outer.json').read_text());self.assertTrue(record['forced']);self.assertEqual(record['remaining_owned'],[]);self.assertTrue('remained' in record['failure_reason'] or 'adopted' in record['failure_reason']);self.assertEqual(record['ownership_mode'],'linux-child-subreaper');self.assertTrue(record['adopted_descendants']);self.assertTrue(all(a['wait_status'] is not None and a['wait_error'] is None for a in record['adopted_descendants']))
 def test_unexpected_adoption_even_natural_zero_is_rejected(self):
  import os,sys
  code='import subprocess;subprocess.Popen(['+repr(sys.executable)+',"-c","pass"],start_new_session=True)'
  self.assertRaises(RuntimeError,self.outer.execute_owned,self.command(code),os.environ.copy(),self.outer.ROOT/'adopted-zero',2)
  record=json.loads((self.outer.ROOT/'adopted-zero.outer.json').read_text());self.assertTrue(record['forced']);self.assertEqual(record['remaining_owned'],[]);self.assertTrue(record['adopted_descendants']);self.assertTrue(all(a['wait_status'] is not None and a['wait_error'] is None for a in record['adopted_descendants']))
 def test_deadline_and_cross_session_child_cleanup(self):
  import os,sys
  code='import subprocess,time;subprocess.Popen(['+repr(sys.executable)+',"-c","import time;time.sleep(30)"],start_new_session=True);time.sleep(30)'
  self.assertRaises(RuntimeError,self.outer.execute_owned,self.command(code),os.environ.copy(),self.outer.ROOT/'deadline',.25)
  record=json.loads((self.outer.ROOT/'deadline.outer.json').read_text());self.assertTrue(record['forced']);self.assertEqual(record['remaining_owned'],[]);self.assertIn('deadline',record['failure_reason'])
 def test_stream_bound_and_durable_failure(self):
  import os
  self.assertRaises(RuntimeError,self.outer.execute_owned,self.command('import os,time;os.write(1,b"x"*8192);time.sleep(30)'),os.environ.copy(),self.outer.ROOT/'logs',2,1024)
  record=json.loads((self.outer.ROOT/'logs.outer.json').read_text());self.assertTrue(record['forced']);self.assertEqual(record['remaining_owned'],[]);self.assertIn('log bound',record['failure_reason'])
 def test_final_artifact_failure_is_durable_and_nonzero(self):
  self.outer.BUDGET=1
  self.assertRaises(RuntimeError,self.outer.finish_screen,self.outer.ROOT,{'schema_version':1,'accepted':True,'stopped':False})
  record=json.loads((self.outer.ROOT/'screen-final.json').read_text());self.assertFalse(record['accepted']);self.assertTrue(record['stopped']);self.assertEqual(record['failure'],'final artifact bound exceeded')
 def test_artifact_bound_before_launch(self):
  import os
  self.outer.BUDGET=1
  self.assertRaises(RuntimeError,self.outer.execute_owned,self.command('raise SystemExit(0)'),os.environ.copy(),self.outer.ROOT/'budget',2)
  record=json.loads((self.outer.ROOT/'budget.outer.json').read_text());self.assertTrue(record['forced']);self.assertIsNone(record['status']);self.assertIn('artifact',record['failure_reason'])
if __name__=='__main__':unittest.main()
