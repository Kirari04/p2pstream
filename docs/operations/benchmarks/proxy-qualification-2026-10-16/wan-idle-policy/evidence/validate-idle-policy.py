#!/usr/bin/env python3
"""Reuse exact mature/whole-workload classifier; add independent idle binding."""
from pathlib import Path
import importlib.util,json,sys
ROOT=Path(__file__).resolve().parent
OLD=ROOT.parent/'wan-controller-screen-20261017'
def module(name,path):
 spec=importlib.util.spec_from_file_location(name,path);value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value
BASE=module('unchanged_controller_policy',OLD/'runner-source/integration/validate-controller-policy.py')
BASE.ORDER=('cubic','cubic','cubic')
PROOF=module('idle_namespace_proof',ROOT/'idle-policy-proof.py')
def validate_arm(index,root,status):
 value=BASE.validate_arm(index,root,status)
 c=root/'results.sender-wait-controls'
 envelopes=[BASE.load(p) for p in sorted(c.glob('*-sender.json'))]
 proof=PROOF.validate(*[PROOF.read(root/name) for name in ('idle-configured.json','idle-endpoints-endpoint-binding.json','idle-endpoints-post-workload.json','idle-host-pre.json','idle-host-post.json')],index,BASE.load(c/'final-raw-write-terminal.json'),envelopes,[json.loads(line) for line in (root/'results.lifecycle.jsonl').read_text().splitlines()])
 value.update({'intervention':'private-agent-and-origin-namespace-idle/application-limited-cwnd-validation','idle_policy':PROOF.expected(index),'idle_namespace_proof':proof})
 return value
def decide_screen(arms):
 for index,arm in enumerate(arms):BASE.require(arm['idle_policy']==PROOF.expected(index) and arm['idle_namespace_proof']['accepted'] is True and arm['idle_namespace_proof']['expected_value']==PROOF.expected(index),'independent idle arm expectation differs')
 value=BASE.decide_screen(arms)
 value.update({'diagnostic':'whole-private-namespace-idle-policy','policy_order':[1,0,1],'controller_order':['cubic']*3,'interpretation':'fresh fixed-order1/0/1 private agent+origin namespace policy, affecting both idle restart and application-limited cwnd validation; unchanged classifier criteria; original mature outcomes preserved; no unique path/threshold creator, production tuning, originalWAN, HAProxy, parity, merge or release clearance'})
 return value
def main():
 if sys.argv[1]=='arm':value=validate_arm(int(sys.argv[2]),Path(sys.argv[3]),int(sys.argv[4]));out=Path(sys.argv[5])
 elif sys.argv[1]=='screen':value=decide_screen([BASE.load(Path(x)) for x in sys.argv[2:5]]);out=Path(sys.argv[5])
 else:raise ValueError('unknown validation mode')
 PROOF.fresh(out,value);raise SystemExit(0 if value.get('eligible_for_diagnostic',value.get('accepted',False)) else 1)
if __name__=='__main__':main()
