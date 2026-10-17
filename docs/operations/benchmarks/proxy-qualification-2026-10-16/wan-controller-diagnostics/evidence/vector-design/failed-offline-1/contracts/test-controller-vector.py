#!/usr/bin/env python3
"""Focused offline outcome controls for the new whole-retained-vector diagnostic."""
from pathlib import Path
import copy,importlib.util,math,unittest
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('vector',ROOT/'runner-source/integration/validate-controller-vector.py')
vector=importlib.util.module_from_spec(spec);spec.loader.exec_module(vector)
class WholeVector(unittest.TestCase):
 def fixture(self,index,rates=(32_000_000,)*4,read=10):
  rows=[]
  for n,(stage,direction) in enumerate(vector.SCHEDULE,1):
   rows.append({'diagnostic_sequence':n,'measurement_stage':stage,'transfer_direction':direction,'configured_payload_bytes':268435456,'configured_rtt_ms':80,'public_protocol':'h2','useful_transfer_bytes_per_second':36_000_000,'wall_seconds':8.0,'response_copy_attribution':{'matches':[{'handler_elapsed_ns':8_000_000_000,'copy':{'upstream_read':{'total_ns':read},'downstream_write':{'total_ns':100}}}]}})
  for n,rate in zip(vector.RETAINED,rates):rows[n-1]['useful_transfer_bytes_per_second']=rate
  issues=[] if all(rows[n-1]['useful_transfer_bytes_per_second']>=vector.FLOOR for n in vector.DOWNLOADS) else ['preserved mature download floor failure']
  return rows,{'integrity_issues':[],'conditioning_issues':[],'performance_issues':issues,'accepted':not issues}
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
 def test_candidate_all_original_floors_and_control_independence(self):
  for index in [0,2]:
   for rates in [(14_000_000,27_000_000,27_000_000,35_000_000),(40_000_000,)*4]:
    arm=self.arm(index,rates);self.assertTrue(arm['eligible_for_diagnostic'])
    self.assertEqual(arm['mature_performance_accepted'],not arm['mature_performance_issues'])
  for n in vector.DOWNLOADS:
   rows,att=self.fixture(1);rows[n-1]['useful_transfer_bytes_per_second']=vector.FLOOR-1;att.update(accepted=False,performance_issues=['mature floor failure'])
   arm=vector.decide_arm(1,rows,att);self.assertFalse(arm['eligible_for_diagnostic']);self.assertFalse(arm['mature_performance_accepted']);self.assertEqual(arm['mature_performance_issues'],att['performance_issues'])
  rows,att=self.fixture(1);att.update(accepted=False,performance_issues=['independent mature performance failure'])
  self.assertFalse(vector.decide_arm(1,rows,att)['eligible_for_diagnostic'])
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
   self.assertFalse(result['arms'][index]['mature_performance_accepted']);self.assertEqual(result['arms'][index]['mature_performance_issues'],arms[index]['mature_performance_issues'])
if __name__=='__main__':unittest.main()
