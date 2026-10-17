#!/usr/bin/python3
import hashlib,json,pathlib,sys
old=pathlib.Path(sys.argv[1]); new=pathlib.Path(sys.argv[2])
expected={9:(16,12032),11:(8,4224),14:(8,7168),15:(7,3392)}
def sha(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  while b:=f.read(1024*1024): h.update(b)
 return h.hexdigest()
rows=[]; issues=[]
for seq,(want_count,want_ns) in expected.items():
 op=old/f'sequence-{seq}.reduction.json'; np=new/f'sequence-{seq}.reduction.json'
 o=json.loads(op.read_text()); n=json.loads(np.read_text()); ro=o['reduction']; rn=n['reduction']
 exact_fields={
  'markers': o['markers']==n['markers'],
  'state_ns': ro['state_ns']==rn['state_ns'],
  'region_duration_ns': ro['region_duration_ns']==rn['region_duration_ns'],
  'partition_sum_ns': ro['partition_sum_ns']==rn['partition_sum_ns'],
  'partition_delta_ns': ro['partition_delta_ns']==rn['partition_delta_ns'],
  'handler_transition_count': ro['handler_transition_count']==rn['handler_transition_count'],
  'input_sha256': o['input_sha256']==n['input_sha256'],
  'expanded_input_sha256': o['expanded_input_sha256']==n['expanded_input_sha256'],
  'canonical_ledger_sha256': o['canonical_ledger_sha256']==n['canonical_ledger_sha256'],
  'parser_status_sha256': o['parsed_tool_status_sha256']==n['parsed_tool_status_sha256'],
  'canonicalizer_status_sha256': o['canonicalizer_status_sha256']==n['canonicalizer_status_sha256'],
 }
 checks={
  'corrected_accepted': n['accepted'] is True,
  'corrected_errors_empty': n['errors']==[],
  'missing_stack_zero': rn['missing_stack_ns']==0,
  'snapshot_count_expected': rn['waiting_status_snapshot_count']==want_count,
  'snapshot_carried_ns_expected': rn['waiting_status_snapshot_carried_ns']==want_ns,
  'partition_conserved': rn['partition_delta_ns']==0 and rn['partition_sum_ns']==rn['region_duration_ns'],
  'original_preserved_false': o['accepted'] is False,
  'original_missing_matches_context': ro['missing_stack_ns']==want_ns,
 }
 bad=[k for k,v in {**exact_fields,**checks}.items() if not v]
 if bad: issues.append({'sequence':seq,'failed':bad})
 rows.append({
  'sequence':seq,
  'canonical_input':str(old/f'sequence-{seq}.intern'),
  'canonical_input_sha256':sha(old/f'sequence-{seq}.intern'),
  'original_reduction':str(op),'original_reduction_sha256':sha(op),
  'corrected_reduction':str(np),'corrected_reduction_sha256':sha(np),
  'exact_unchanged':exact_fields,'checks':checks,
  'markers':n['markers'],
  'region_duration_ns':rn['region_duration_ns'],'state_ns':rn['state_ns'],
  'partition_sum_ns':rn['partition_sum_ns'],'partition_delta_ns':rn['partition_delta_ns'],
  'handler_transition_count':rn['handler_transition_count'],
  'waiting_status_snapshot_count':rn['waiting_status_snapshot_count'],
  'waiting_status_snapshot_carried_ns':rn['waiting_status_snapshot_carried_ns'],
  'original_missing_stack_ns':ro['missing_stack_ns'],'corrected_missing_stack_ns':rn['missing_stack_ns'],
  'corrected_unknown_stack_ns':rn['unknown_stack_ns'],
  'corrected_attribution_complete':rn['attribution_complete'],
  'corrected_waiting_category_ns':rn['waiting_category_ns'],
 })
report={
 'schema_version':1,
 'scope':'separate corrected offline reductions of the same frozen sustained H1 canonical inputs; original reductions/index unchanged',
 'accepted':not issues,
 'local_parser_acceptance_only':True,
 'unknown_stack_interpretation':'unresolved attribution; corrected acceptance does not make attribution complete',
 'independent_context':'tmp/continuation-20261016/wan-read-wait-stack-intern-source/independent-canonical-partition-source/runs/sustained-h1-1/missing-wait-context.json',
 'issues':issues,'rows':rows}
(new/'comparison-audit.json').write_text(json.dumps(report,indent=2,sort_keys=True)+'\n')
if issues: raise SystemExit(1)
