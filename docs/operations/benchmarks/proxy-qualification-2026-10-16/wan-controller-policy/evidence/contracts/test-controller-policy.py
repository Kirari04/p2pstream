#!/usr/bin/env python3
"""Focused offline outcome controls for the new whole-workload controller-policy diagnostic."""
from pathlib import Path
import copy,importlib.util,math,unittest
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('policy',ROOT/'runner-source/integration/validate-controller-policy.py')
vector=importlib.util.module_from_spec(spec);spec.loader.exec_module(vector)
class WholeVector(unittest.TestCase):
 def fixture(self,index,rates=(32_000_000,)*4,read=10):
  rows=[];expectations=[]
  for n,(stage,direction) in enumerate(vector.SCHEDULE,1):
   key=f'lane-{(n-9)%4+1}';origin=f'origin-{(n-9)%4+1}';got_ns=n*1_000_000_000;revisit=n>=13
   trace={'sequence':str(n),'method':'GET' if direction=='download' else 'POST','path':'/agent/bulk' if direction=='download' else '/agent/bulk-upload','started_unix_ns':got_ns-1,'handler_return_unix_ns':got_ns+10,'callback_overflow':False,'got_conn':[{'connection_type':'stream','local_address':'local','remote_address':key,'reused':revisit,'was_idle':revisit,'idle_time_ns':3_999_999_995 if revisit else 0,'observed_unix_ns':got_ns}],'put_idle_conn':[{'observed_unix_ns':got_ns+5}]}
   rows.append({'diagnostic_sequence':n,'measurement_stage':stage,'transfer_direction':direction,'configured_payload_bytes':268435456,'configured_rtt_ms':80,'public_protocol':'h2','useful_transfer_bytes_per_second':36_000_000,'wall_seconds':268435456/36_000_000,'wan_response_tls':[{'origin_connection_id':origin}],'upstream_transport_trace':{'matches':[trace],'overflow':False,'sequence':str(n),'store_count':n},'response_copy_attribution':{'matches':[{'handler_elapsed_ns':8_000_000_000,'copy':{'upstream_read':{'total_ns':read},'downstream_write':{'total_ns':100}}}]}})
   if 9<=n<=12:expectations.append({'sequence':n,'kind':'first-post-close-visit','lane_position':n-8,'connection_key':f'stream|local|{key}','satisfied':True})
   if n>=13:expectations.append({'sequence':n,'kind':'same-key-revisit','lane_position':n-12,'connection_key':f'stream|local|{key}','expected_connection_key':f'stream|local|{key}','prior_put_idle_unix_ns':(n-4)*1_000_000_000+5,'got_conn_unix_ns':got_ns,'elapsed_idle_ns':3_999_999_995,'httptrace_idle_time_ns':3_999_999_995,'prior_origin_connection_id':origin,'observed_origin_connection_id':origin,'satisfied':True})
  for n,rate in zip(vector.RETAINED,rates):self.rate(rows,n,rate)
  issues=[] if all(rows[n-1]['useful_transfer_bytes_per_second']>=vector.FLOOR for n in vector.DOWNLOADS) else ['preserved mature download floor failure']
  return rows,{'integrity_issues':[],'conditioning_issues':[],'performance_issues':issues,'validation_issues':issues.copy(),'continuation_safe':True,'accepted':not issues,'reuse_expectations':expectations}
 def rate(self,rows,n,rate):
  rows[n-1]['wall_seconds']=268435456/rate
  rows[n-1]['useful_transfer_bytes_per_second']=268435456/rows[n-1]['wall_seconds']
 def performance(self,att,issues):
  att['performance_issues']=issues.copy();att['validation_issues']=att['conditioning_issues']+issues;att['accepted']=not att['validation_issues']
 def reconnect(self,rows,att,n):
  rows[n-1]['wan_response_tls'][0]['origin_connection_id']=f'replaced-{n}'
  got=rows[n-1]['upstream_transport_trace']['matches'][0]['got_conn'][0]
  got.update(reused=False,was_idle=False,idle_time_ns=0)
  expected=att['reuse_expectations'][n-9];expected.update(observed_origin_connection_id=f'replaced-{n}',httptrace_idle_time_ns=0,satisfied=False)
  att['conditioning_issues'].append(f'sequence {n} fully evidenced reuse/idle/origin conditioning was not retained')
  att['conditioning_issues'].sort();self.performance(att,att['performance_issues'])
 def arm(self,index,rates=(32_000_000,)*4,read=10):return vector.decide_arm(index,*self.fixture(index,rates,read))
 def screen(self,control0=(32_000_000,)*4,candidate=(36_000_000,)*4,control2=(32_000_000,)*4):
  return [self.arm(0,control0),self.arm(1,candidate,8),self.arm(2,control2)]
 def test_pooling_is_equal_byte_harmonic_and_all_rows_reported(self):
  arm=self.arm(0,(20_000_000,40_000_000,20_000_000,40_000_000))
  self.assertAlmostEqual(arm['pooled_bytes_per_second'],26_666_666.666666668)
  self.assertEqual(set(arm['speeds']),{str(n) for n in range(1,17)})
  self.assertEqual([r['sequence'] for r in arm['retained_downloads']],[9,11,14,15])
  self.assertEqual(arm['aggregate_upstream_read_ns'],40)
 def test_pooled_primary_uses_faster_complete_control(self):
  arms=self.screen((20_000_000,)*4,(32_000_000,)*4,(30_000_000,)*4)
  decision=vector.decide_screen(arms)
  self.assertFalse(decision['criteria']['pooled_rate_at_least_108pct_faster_control'])
  self.assertFalse(decision['accepted'])
  self.assertAlmostEqual(decision['candidate_pooled_ratio_to_faster_control'],32/30)
  self.assertTrue(vector.decide_screen(self.screen())['accepted'])
 def test_108pct_pooled_boundary(self):
  self.assertTrue(vector.decide_screen(self.screen((30_000_000,)*4,(32_400_000,)*4,(30_000_000,)*4))['accepted'])
  self.assertFalse(vector.decide_screen(self.screen((30_000_000,)*4,(32_399_999,)*4,(30_000_000,)*4))['accepted'])
 def test_exact_95pct_boundary_and_one_row_cannot_hide(self):
  controls=(40_000_000,20_000_000,20_000_000,20_000_000)
  arms=self.screen(controls,(38_000_000,40_000_000,40_000_000,40_000_000),controls)
  self.assertTrue(vector.decide_screen(arms)['accepted'])
  rates=(math.nextafter(38_000_000,0),40_000_000,40_000_000,40_000_000)
  result=vector.decide_screen(self.screen(controls,rates,controls))
  self.assertTrue(result['criteria']['pooled_rate_at_least_108pct_faster_control'])
  self.assertFalse(result['criteria']['each_retained_rate_at_least_95pct_faster_control'])
  self.assertFalse(result['accepted'])
 def test_read_must_be_positive_and_strictly_below_both(self):
  for read0,read1,read2 in [(10,10,11),(8,9,10),(10,10,10)]:
   arms=[self.arm(0,read=read0),self.arm(1,(36_000_000,)*4,read1),self.arm(2,read=read2)]
   self.assertFalse(vector.decide_screen(arms)['criteria']['aggregate_read_strictly_below_both_controls'])
  rows,att=self.fixture(1);rows[8]['response_copy_attribution']['matches'][0]['copy']['upstream_read']['total_ns']=0
  self.assertRaises(ValueError,vector.decide_arm,1,rows,att)
 def test_candidate_all_original_floors_are_final_outcomes(self):
  for index in [0,1,2]:
   for n in vector.DOWNLOADS:
    rows,att=self.fixture(index);self.rate(rows,n,vector.FLOOR-1);self.performance(att,['mature floor failure'])
    arm=vector.decide_arm(index,rows,att)
    self.assertTrue(arm['eligible_for_diagnostic']);self.assertFalse(arm['mature_accepted'])
    self.assertEqual(arm['mature_performance_issues'],att['performance_issues'])
    if index==1:
     result=vector.decide_screen([self.arm(0),arm,self.arm(2)])
     self.assertFalse(result['diagnostic_positive']);self.assertFalse(result['criteria']['candidate_all_original_download_floors'])
     self.assertEqual(len(result['arms']),3)
  self.assertTrue(self.arm(1,(vector.FLOOR,)*4)['eligible_for_diagnostic'])
 def test_nonfinite_shapes_and_structural_failures_reject(self):
  for bad in [float('nan'),float('inf'),0,-1,True]:
   rows,att=self.fixture(0);rows[0]['useful_transfer_bytes_per_second']=bad;self.assertRaises(ValueError,vector.decide_arm,0,rows,att)
  for field in ['integrity_issues','conditioning_issues']:
   rows,att=self.fixture(0);att[field]=['failure'];self.assertRaises(ValueError,vector.decide_arm,0,rows,att)
  rows,att=self.fixture(0);self.assertRaises(ValueError,vector.decide_arm,0,rows[:-1],att)
  rows[8]['measurement_stage']='steady';self.assertRaises(ValueError,vector.decide_arm,0,rows,att)
  for kind in ['missing','order','retained','rate','ineligible']:
   arms=self.screen()
   if kind=='missing':arms.pop()
   if kind=='order':arms.reverse()
   if kind=='retained':arms[0]['retained_downloads'].pop()
   if kind=='rate':arms[0]['speeds']['9']=float('nan')
   if kind=='ineligible':arms[1]['eligible_for_diagnostic']=False
   self.assertRaises(ValueError,vector.decide_screen,arms)
 def test_performance_failures_are_preserved_in_complete_result(self):
  arms=self.screen((14_000_000,27_000_000,27_000_000,35_000_000),(38_000_000,)*4,(20_000_000,27_000_000,27_000_000,35_000_000))
  result=vector.decide_screen(arms);self.assertTrue(result['accepted'])
  for index in [0,2]:
   self.assertFalse(result['arms'][index]['mature_accepted']);self.assertEqual(result['arms'][index]['mature_performance_issues'],arms[index]['mature_performance_issues'])
 def test_each_fully_evidenced_reconnect_is_symmetric_and_preserved(self):
  for index in range(3):
   for n in range(13,17):
    rows,att=self.fixture(index);self.reconnect(rows,att,n);saved=copy.deepcopy(att)
    arm=vector.decide_arm(index,rows,att)
    self.assertTrue(arm['eligible_for_diagnostic']);self.assertFalse(arm['mature_accepted'])
    self.assertEqual(arm['mature_attestation'],saved);self.assertEqual(att,saved)
    self.assertEqual(arm['revisit_outcome_counts'],{'total':4,'satisfied':3,'unsatisfied':1})
    self.assertEqual(len(arm['raw_origin_traces']),16)
  arms=[]
  for index in range(3):
   rows,att=self.fixture(index,(36_000_000,)*4 if index==1 else (32_000_000,)*4,8 if index==1 else 10)
   for n in range(13,17):self.reconnect(rows,att,n)
   arms.append(vector.decide_arm(index,rows,att))
  self.assertTrue(vector.decide_screen(arms)['diagnostic_positive'])
  self.assertFalse(arms[1]['mature_accepted'])
 def test_reuse_missing_malformed_negative_unknown_or_uncorrelated_reject(self):
  for kind in ['missing-origin','missing-got','missing-put','negative-idle','negative-elapsed','flag-type','bool-time','unknown-conditioning','forged-conditioning','wrong-expectation','pool-failure','duplicate-trace','request-correlation','rate-wall','negative-wall','initial-not-fresh']:
   rows,att=self.fixture(0);r=rows[12];trace=r['upstream_transport_trace']['matches'][0];got=trace['got_conn'][0]
   if kind=='missing-origin':r['wan_response_tls']=[]
   elif kind=='missing-got':trace['got_conn']=[]
   elif kind=='missing-put':trace['put_idle_conn']=[]
   elif kind=='negative-idle':got['idle_time_ns']=-1
   elif kind=='negative-elapsed':got['observed_unix_ns']=1
   elif kind=='flag-type':got['reused']=1
   elif kind=='bool-time':got['observed_unix_ns']=True
   elif kind=='unknown-conditioning':att['conditioning_issues']=['unknown conditioning failure']
   elif kind=='forged-conditioning':att['conditioning_issues']=['sequence 13 fully evidenced reuse/idle/origin conditioning was not retained']
   elif kind=='wrong-expectation':att['reuse_expectations'][4]['satisfied']=False
   elif kind=='pool-failure':att['conditioning_issues']=['row 13 did not retain required aggregate receive-pool evidence']
   elif kind=='duplicate-trace':r['upstream_transport_trace']['matches'].append(copy.deepcopy(trace))
   elif kind=='request-correlation':trace['sequence']='14'
   elif kind=='rate-wall':r['wall_seconds']*=2
   elif kind=='negative-wall':r['wall_seconds']=-1
   elif kind=='initial-not-fresh':rows[8]['upstream_transport_trace']['matches'][0]['got_conn'][0]['reused']=True
   self.performance(att,att['performance_issues'])
   with self.subTest(kind=kind):self.assertRaises((ValueError,KeyError),vector.decide_arm,0,rows,att)
 def test_tunnel_key_and_reuse_origin_contradictions_reject(self):
  for kind in ['changed-key','false-same-origin','true-new-origin','flags-disagree']:
   rows,att=self.fixture(0);got=rows[12]['upstream_transport_trace']['matches'][0]['got_conn'][0];expected=att['reuse_expectations'][4]
   if kind=='changed-key':got['remote_address']='other-lane';expected['connection_key']='stream|local|other-lane'
   elif kind=='false-same-origin':got.update(reused=False,was_idle=False,idle_time_ns=0);expected['httptrace_idle_time_ns']=0
   elif kind=='true-new-origin':rows[12]['wan_response_tls'][0]['origin_connection_id']='new-origin';expected['observed_origin_connection_id']='new-origin'
   elif kind=='flags-disagree':got['was_idle']=False
   expected['satisfied']=False;att['conditioning_issues']=['sequence 13 fully evidenced reuse/idle/origin conditioning was not retained'];self.performance(att,[])
   with self.subTest(kind=kind):self.assertRaises(ValueError,vector.decide_arm,0,rows,att)
 def test_retained_origin_past_30_seconds_is_reported_timing_outcome(self):
  for index in range(3):
   rows,att=self.fixture(index)
   for n in range(13,17):
    trace=rows[n-1]['upstream_transport_trace']['matches'][0];got=trace['got_conn'][0];expected=att['reuse_expectations'][n-9]
    trace['started_unix_ns']+=31_000_000_000;trace['handler_return_unix_ns']+=31_000_000_000;trace['put_idle_conn'][0]['observed_unix_ns']+=31_000_000_000
    got['observed_unix_ns']+=31_000_000_000;got['idle_time_ns']+=31_000_000_000
    for key in ['got_conn_unix_ns','elapsed_idle_ns','httptrace_idle_time_ns']:expected[key]+=31_000_000_000
    expected['satisfied']=False;att['conditioning_issues'].append(f'sequence {n} fully evidenced reuse/idle/origin conditioning was not retained')
   self.performance(att,[]);arm=vector.decide_arm(index,rows,att)
   self.assertTrue(arm['eligible_for_diagnostic']);self.assertFalse(arm['mature_accepted'])
   self.assertEqual(arm['origin_outcome_counts'],{'retained':4,'fresh_replacement':0})
 def test_saved_discovery_fixture_classification_does_not_revise_old_verdict(self):
  import hashlib,json
  root=ROOT/'runner-source/integration/runs/controller-crc-vector-once/arm-0-cubic'
  paths=[root/'results.jsonl',root/'attestation.json']
  before=[hashlib.sha256(p.read_bytes()).hexdigest() for p in paths]
  rows=[json.loads(line) for line in paths[0].read_text().splitlines()];att=json.loads(paths[1].read_text())
  self.assertFalse(att['accepted']);self.assertFalse(att['reuse_expectations'][-1]['satisfied'])
  arm=vector.decide_arm(0,rows,att)
  self.assertTrue(arm['eligible_for_diagnostic']);self.assertFalse(arm['mature_accepted'])
  self.assertEqual([e['sequence'] for e in arm['reuse_expectations'][4:] if not e['satisfied']],[15,16])
  self.assertEqual(before,[hashlib.sha256(p.read_bytes()).hexdigest() for p in paths])
if __name__=='__main__':unittest.main()

