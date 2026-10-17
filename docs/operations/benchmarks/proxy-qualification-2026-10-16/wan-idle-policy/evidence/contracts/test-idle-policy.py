#!/usr/bin/env python3
"""Prospective offline tests; no namespace or endpoint operations."""
from pathlib import Path
import ast,copy,importlib.util,json,unittest
ROOT=Path(__file__).resolve().parents[1]
def module(name,path):
 spec=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);return m
P=module('proof',ROOT/'idle-policy-proof.py')
V=module('validator',ROOT/'validate-idle-policy.py')
def sample(pid,ns,value=1,t=10):return {'pid':pid,'ppid':1,'start_ticks':pid+100,'namespace':f'net:[{ns}]','namespace_device':4,'namespace_inode':ns,'value':value,'monotonic_ns':t,'kernel_release':'7.0.0-38-generic','boot_id':'fixed-boot','time_namespace':'time:[1]'}
def fixture(index=1):
 want=P.expected(index);host=sample(100,1);runner=sample(1,2);router=sample(3,3);target=sample(4,4,want)
 def subject(v):return {**v,'subject':copy.deepcopy(v),'resolution':'owned-pid'}
 router=subject(router);target=subject(target);original=copy.deepcopy(target);original['value']=1
 c={'schema_version':1,'phase':'configured-before-endpoints','arm_index':index,'expected_value':want,'sysctl_path':P.SYSCTL,'host_pre':host,'runner_pre':runner,'runner_after_set':runner,'router_pre':router,'router_after_set':router,'target_original':original,'setter':target,'target_after_set':target,'target_independent_readback':dict(target,resolution='namespace-file'),'role_map':{'agent':'target','origin':'target','server':'runner','load':'runner'},'load_scope_evidence':'source-derived exact direct taskset invocation; no observed load PID','configured_monotonic_ns':20}
 roles={'agent':subject(sample(11,4,want)),'origin':subject(sample(12,4,want)),'server':subject(sample(13,2))}
 ready={k:{'path':k,'pid':v['pid']} for k,v in roles.items()}
 pre={'schema_version':1,'phase':'endpoint-binding','arm_index':index,'expected_value':want,'runner':runner,'router':router,'target_helper':target,'roles':roles,'ready_bindings':ready,'monotonic_ns':30}
 post=copy.deepcopy(pre);post.update(phase='post-workload',monotonic_ns=40)
 hpre={'schema_version':1,'phase':'host-pre','sample':host};hpost={'schema_version':1,'phase':'host-post','sample':dict(host,monotonic_ns=50)}
 raw={'tcp_state':{'tcp_slow_start_after_idle':want}}
 term={'tcp_slow_start_after_idle_setup':want,'tcp_slow_start_after_idle_terminal':want,'tcp_slow_start_after_idle_read_error':False,'records':[{'raw_write_activity':copy.deepcopy(raw)} for _ in range(5)]}
 env=[{'response':{'send_counters':[{'raw_write_activity':copy.deepcopy(raw)} for _ in range(5)]}}]
 life=[{'event':'namespace-helpers-start','boot_id':'fixed-boot','runner_namespace':runner['namespace'],'agent':target,'router':router}]+[{'event':'process-exit','label':role if role=='origin' else 'fixed-'+role,'pid':row['pid'],'status':0,'forced':False,'group_joined':True} for role,row in roles.items()]
 return [copy.deepcopy(x) for x in (c,pre,post,hpre,hpost,index,term,env,life)]
class IdleContracts(unittest.TestCase):
 def call(self,f):
  original=P.read;P.read=lambda name:{'pid':{'agent':11,'origin':12,'server':13}[name]}
  try:return P.validate(*f)
  finally:P.read=original
 def test_exact_zero_and_one_controls(self):
  for index in range(3):self.assertTrue(self.call(fixture(index))['accepted'])
 def test_wrong_domain_and_scope_fail_closed(self):
  changes=[('config',lambda f:f[0].update(expected_value=True)),('index',lambda f:f.__setitem__(5,True)),('hostbool',lambda f:f[3]['sample'].update(value=True)),('schema',lambda f:f[0].update(schema_version=True)),('original-missing',lambda f:f[0].pop('target_original')),('original-foreign',lambda f:f[0]['target_original'].update(namespace='net:[9]')),('host-alias',lambda f:f[0]['target_after_set'].update(namespace='net:[1]',namespace_inode=1)),('runner-changed',lambda f:f[2]['runner'].update(value=0)),('router-changed',lambda f:f[2]['router'].update(value=0)),('target-changed',lambda f:f[2]['roles']['agent'].update(value=1)),('origin-missing',lambda f:f[1]['roles'].pop('origin')),('server-foreign',lambda f:f[1]['roles']['server'].update(namespace='net:[8]')),('subject-reused',lambda f:f[2]['roles']['agent']['subject'].update(start_ticks=99)),('clock',lambda f:f[2]['runner'].update(boot_id='other')),('old-hostpost',lambda f:f[4]['sample'].update(monotonic_ns=35)),('phase-order',lambda f:f[0].update(configured_monotonic_ns=31)),('cached-mismatch',lambda f:f[7][0]['response']['send_counters'][0]['raw_write_activity']['tcp_state'].update(tcp_slow_start_after_idle=1)),('terminal-mismatch',lambda f:f[6].update(tcp_slow_start_after_idle_terminal=1)),('cached-only',lambda f:f[0].pop('target_independent_readback')),('ready-mismatch',lambda f:f[1]['ready_bindings']['agent'].update(pid=99)),('forced',lambda f:f[8][1].update(forced=True)),('namespace-lifecycle',lambda f:f[8][0]['agent'].update(start_ticks=999)),('timestampbool',lambda f:f[2]['target_helper'].update(monotonic_ns=True)),('sample-subject-foreign',lambda f:f[2]['target_helper'].update(namespace='net:[8]',namespace_inode=8))]
  for label,change in changes:
   with self.subTest(label=label):
    f=fixture();change(f)
    with self.assertRaises((ValueError,KeyError)):self.call(f)
 def test_actual_old_evidence_size_boundary(self):
  saved=ROOT.parent/'wan-controller-screen-20261017/runner-source/integration/runs/controller-crc-policy-once'
  files=[saved/f'arm-{i}.decision.json' for i in range(3)]+[saved/'arm-2-cubic/results.sender-wait-controls/sequence-9-post-sender.json']
  for path in files:
   self.assertEqual(V.BASE.load(path),json.loads(path.read_text()))
  self.assertTrue(any(p.stat().st_size>1<<20 for p in files))
 def test_all_cubic_and_owner_identity(self):
  bindings=[];records=[];counters=[]
  for lane in range(5):
   ident={'lane':lane,'generation':lane+1,'local_address':f'a:{lane}','remote_address':f'b:{lane}'}
   b=dict(ident,requested='cubic',readback='cubic',before_tls=True,explicit_set=lane>0)
   raw=dict(ident,tcp_state={'bind_tcp_info':{'captured':True,'congestion_control':'cubic'}})
   bindings.append(b);records.append({'lane':lane,'session_identity':str(lane),'raw_write_activity':raw});counters.append({'lane':lane,'session_identity':str(lane),'raw_write_activity':copy.deepcopy(raw)})
  side={'schema_version':1,'requested':'cubic','namespace':'target','parent_namespace':'runner','default':'cubic','available':['cubic','reno'],'allowed':['cubic','reno'],'bindings':bindings};terminal={'records':records};env=[{'action':'pre','response':{'send_counters':counters}}];ns={'agent':{'namespace':'target'},'runner_namespace':'runner'}
  V.BASE.validate_bindings(side,terminal,env,ns,V.BASE.ORDER[1])
  bad=copy.deepcopy(env);bad[0]['response']['send_counters'][3]['raw_write_activity']['tcp_state']['bind_tcp_info']['congestion_control']='reno'
  with self.assertRaises(ValueError):V.BASE.validate_bindings(side,terminal,bad,ns,V.BASE.ORDER[1])
  bad=copy.deepcopy(env);bad[0]['response']['send_counters'][3]['raw_write_activity']['generation']=99
  with self.assertRaises(ValueError):V.BASE.validate_bindings(side,terminal,bad,ns,V.BASE.ORDER[1])
 def test_source_setter_scope_and_unchanged_timed_copy(self):
  old=(ROOT.parent/'wan-controller-screen-20261017/runner-source/integration/generated/qualify-proxy-agent-growth-wan-below-tls.sh').read_text();new=(ROOT/'runner.sh').read_text()
  begin=old.index('run_transfer() {');end=old.index('\nrun_maybe_sender_wait_transfer()',begin)
  self.assertIn(old[begin:end],new);self.assertLess(new.index('idle-policy-proof.py" configure'),new.index('origin_ready='))
  tree=ast.parse((ROOT/'idle-policy-proof.py').read_text());writes=[n for n in ast.walk(tree) if isinstance(n,ast.Call) and isinstance(n.func,ast.Name) and n.func.id=='open'];self.assertEqual(len(writes),1)
  self.assertEqual(V.BASE.ORDER,('cubic',)*3)
 def test_exact_boundary_slow_middle_and_all_three(self):
  def arms(candidate=36_720_000):
   rows=[]
   for index in range(3):
    rate=candidate if index==1 else 34_000_000;att={'integrity_issues':[],'conditioning_issues':[],'performance_issues':[],'validation_issues':[],'continuation_safe':True,'accepted':True}
    speeds={str(n):rate for n in range(1,17)}
    rows.append({'arm_index':index,'controller':'cubic','diagnostic':'whole-workload-controller-policy','eligible_for_diagnostic':True,'eligibility_issues':[],'mature_attestation':att,'mature_accepted':True,'mature_conditioning_issues':[],'mature_performance_issues':[],'speeds':speeds,'retained_downloads':[{'sequence':n,'bytes_per_second':rate,'wall_seconds':268435456/rate,'upstream_read_ns':7_000_000_000 if index==1 else 8_000_000_000,'downstream_write_ns':600_000_000,'handler_elapsed_ns':9_000_000_000} for n in (9,11,14,15)],'idle_policy':P.expected(index),'idle_namespace_proof':{'accepted':True,'expected_value':P.expected(index)}})
   return rows
  self.assertTrue(V.decide_screen(arms())['criteria']['pooled_rate_at_least_108pct_faster_control'])
  self.assertFalse(V.decide_screen(arms(36_719_999))['criteria']['pooled_rate_at_least_108pct_faster_control'])
  slow=arms(1_000_000);att=slow[1]['mature_attestation'];att.update(performance_issues=['synthetic slow candidate'],validation_issues=['synthetic slow candidate'],accepted=False);slow[1].update(mature_accepted=False,mature_performance_issues=['synthetic slow candidate'])
  result=V.decide_screen(slow);self.assertFalse(result['accepted']);self.assertEqual(len(result['arms']),3)
  with self.assertRaises(ValueError):V.decide_screen(arms()[:2])
  launcher=(ROOT/'run-idle-policy.py').read_text();self.assertNotIn('mature_accepted',launcher)
if __name__=='__main__':unittest.main()
