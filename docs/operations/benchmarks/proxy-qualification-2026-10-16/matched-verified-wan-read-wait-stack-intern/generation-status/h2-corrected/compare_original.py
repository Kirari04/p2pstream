#!/usr/bin/python3
import hashlib,json,pathlib,sys
orig=pathlib.Path(sys.argv[1]); out=pathlib.Path(sys.argv[2])
def sha(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  while b:=f.read(1<<20): h.update(b)
 return h.hexdigest()
rows=[]; issues=[]
for seq in (9,11,14,15):
 op=orig/f'sequence-{seq}.reduction.json'; np=out/f'sequence-{seq}.reduction.json'
 o=json.loads(op.read_text()); n=json.loads(np.read_text()); ro=o['reduction']; rn=n['reduction']
 exact={k:ro[k]==rn[k] for k in ('region_duration_ns','state_ns','partition_sum_ns','partition_delta_ns','handler_transition_count')}
 exact['markers']=o['markers']==n['markers']
 exact['canonical_input_identity']=all(o[k]==n[k] for k in ('input_sha256','expanded_input_sha256','canonical_ledger_sha256','parsed_tool_status_sha256','canonicalizer_status_sha256'))
 checks={
  'original_rejected':o['accepted'] is False,
  'original_single_expected_error':o['errors']==['positive waiting interval has no TransitionStack'],
  'corrected_accepted':n['accepted'] is True,
  'corrected_errors_empty':n['errors']==[],
  'corrected_missing_zero':rn['missing_stack_ns']==0,
  'snapshot_count_positive':rn['waiting_status_snapshot_count']>0,
  'snapshot_carried_equals_original_missing':rn['waiting_status_snapshot_carried_ns']==ro['missing_stack_ns'],
  'partition_conserved':rn['partition_delta_ns']==0 and rn['partition_sum_ns']==rn['region_duration_ns'],
 }
 failed=[k for k,v in {**exact,**checks}.items() if not v]
 if failed: issues.append({'sequence':seq,'failed':failed})
 rows.append({'sequence':seq,'exact_unchanged':exact,'checks':checks,
  'original_reduction_sha256':sha(op),'corrected_reduction_sha256':sha(np),
  'markers':n['markers'],'region_duration_ns':rn['region_duration_ns'],'state_ns':rn['state_ns'],
  'partition_sum_ns':rn['partition_sum_ns'],'partition_delta_ns':rn['partition_delta_ns'],
  'handler_transition_count':rn['handler_transition_count'],
  'original_missing_stack_ns':ro['missing_stack_ns'],'corrected_missing_stack_ns':rn['missing_stack_ns'],
  'waiting_status_snapshot_count':rn['waiting_status_snapshot_count'],
  'waiting_status_snapshot_carried_ns':rn['waiting_status_snapshot_carried_ns'],
  'unknown_stack_ns':rn['unknown_stack_ns'],'attribution_complete':rn['attribution_complete'],
  'waiting_category_ns':rn['waiting_category_ns']})
report={'schema_version':1,'accepted':not issues,
 'scope':'same frozen H2 canonical inputs; original arm/reductions/index unchanged',
 'local_parser_acceptance_only':True,
 'unknown_stack_interpretation':'unresolved attribution; corrected acceptance does not make attribution complete',
 'issues':issues,'rows':rows}
(out/'comparison-audit.json').write_text(json.dumps(report,indent=2,sort_keys=True)+'\n')
raise SystemExit(0 if not issues else 1)
