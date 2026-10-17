#!/usr/bin/env python3
"""Read-only independent reconstruction from all original timing/audit logs."""
import hashlib,json,pathlib,statistics
P=pathlib.Path(__file__).resolve().parent

def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def main():
 commands=json.loads((P/'commands.json').read_text())
 assert len(commands)==26,len(commands)
 for command in commands:
  audit=json.loads((P/command['outer_audit']).read_text())
  assert digest(P/command['outer_audit'])==command['outer_sha256']
  assert command['exit_code']==0 and command['accepted_cleanup']
  assert audit['status']==0 and audit['accepted_cleanup'] and not audit['forced']
  assert not audit['remaining_owned'] and not audit['adopted_descendants']
  for stream in command['logs'].values():
   assert digest(P/stream['path'])==stream['sha256']
   assert (P/stream['path']).stat().st_size==stream['bytes']
 frozen=json.loads((P/'frozen-inputs.json').read_text())
 for variant,files in frozen['files'].items():
  assert {str(f.relative_to(P/variant)) for f in (P/variant).rglob('*') if f.is_file()}==set(files)
  for name,want in files.items():assert digest(P/variant/name)==want
 ledger=json.loads((P/'all-samples.json').read_text())
 expected_order=[(pair,variant) for pair in range(1,11) for variant in (['baseline','candidate'] if pair%2 else ['candidate','baseline'])]
 assert [(row['pair'],row['variant']) for row in ledger]==expected_order
 cases={f'{shape}/{parent}/{mode}' for shape in ['direct','invalid-peer','configured-untrusted','trusted-single','trusted-chain','conflicting-sources','four-disabled-headers'] for parent in ['background','deadline'] for mode in ['resolve','resolve-and-read']}
 raw={}
 for pair,variant in expected_order:
  path=P/'logs'/f'pair-{pair:02d}-{variant}.stdout.log'
  samples={}
  for line in path.read_text().splitlines():
   if not line.startswith('BenchmarkResolvedIdentityContext/'):continue
   parts=line.split()
   assert len(parts)==8 and parts[3]=='ns/op' and parts[5]=='B/op' and parts[7]=='allocs/op',line
   name=parts[0].removeprefix('BenchmarkResolvedIdentityContext/')
   assert name not in samples
   samples[name]={'iterations':int(parts[1]),'ns':float(parts[2]),'bytes':int(parts[4]),'allocs':int(parts[6]),'line':line}
  assert set(samples)==cases
  raw[pair,variant]=samples
  saved=next(row for row in ledger if row['pair']==pair and row['variant']==variant)
  assert saved['binary_sha256']==digest(P/'bin'/f'{variant}.test')
  for name,sample in samples.items():
   old=saved['samples'][name]
   assert sample['iterations']==old['iterations'] and sample['ns']==old['ns_per_op'] and sample['bytes']==old['bytes_per_op'] and sample['allocs']==old['allocs_per_op']
   assert sample['line']==old['raw_line'] and hashlib.sha256(sample['line'].encode()).hexdigest()==old['raw_line_sha256']
   assert digest(path)==old['log_sha256']
 results={};failed=[]
 for name in sorted(cases):
  baseline=[raw[p,'baseline'][name] for p in range(1,11)];candidate=[raw[p,'candidate'][name] for p in range(1,11)]
  ratios=[c['ns']/b['ns'] for b,c in zip(baseline,candidate)]
  bytes_saved=[b['bytes']-c['bytes'] for b,c in zip(baseline,candidate)]
  allocs_saved=[b['allocs']-c['allocs'] for b,c in zip(baseline,candidate)]
  median_ratio=statistics.median(x['ns'] for x in candidate)/statistics.median(x['ns'] for x in baseline)
  accepted=median_ratio<=1.03 and max(ratios)<=1.10 and min(bytes_saved)>=32 and min(allocs_saved)>=2
  results[name]={'baseline_median_ns':statistics.median(x['ns'] for x in baseline),'candidate_median_ns':statistics.median(x['ns'] for x in candidate),'median_time_ratio':median_ratio,'max_pair_time_ratio':max(ratios),'bytes_saved_range':[min(bytes_saved),max(bytes_saved)],'allocations_saved_range':[min(allocs_saved),max(allocs_saved)],'accepted':accepted}
  if not accepted:failed.append(name)
 published=json.loads((P/'results.json').read_text())
 assert published['retained']==(not failed) and published['failed_cases']==failed
 for name,values in results.items():assert values['median_time_ratio']==published['results'][name]['median_time_ratio'] and values['accepted']==published['results'][name]['passed']
 tests={}
 for name in ['correctness-baseline','correctness-candidate','correctness-candidate-race']:
  lines=(P/'logs'/f'{name}.stdout.log').read_text().splitlines()
  top=[line.split()[2] for line in lines if line.startswith('--- PASS: ') and '/' not in line.split()[2]]
  assert len(top)==len(set(top)) and top
  assert not any(line.startswith('--- FAIL: ') for line in lines)
  tests[name]={'top_level_passes':len(top),'all_pass_records':sum(line.lstrip().startswith('--- PASS: ') for line in lines),'names':top}
 return {'retained':not failed,'case_count':len(cases),'samples':len(cases)*len(expected_order),'natural_commands':len(commands),'tests':tests,'results':results,'commands_sha256':digest(P/'commands.json'),'all_samples_sha256':digest(P/'all-samples.json'),'results_sha256':digest(P/'results.json'),'frozen_inputs_sha256':digest(P/'frozen-inputs.json')}
if __name__=='__main__':print(json.dumps(main(),indent=2))
