#!/usr/bin/env python3
"""Reuse exact mature/whole-workload classifier; add independent default-idle and early-share binding."""
from pathlib import Path
import importlib.util,json,sys
ROOT=Path(__file__).resolve().parent
OLD=ROOT.parent/'wan-controller-screen-20261017'
def module(name,path):
 spec=importlib.util.spec_from_file_location(name,path);value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value
BASE=module('unchanged_controller_policy',OLD/'runner-source/integration/validate-controller-policy.py')
BASE.ORDER=('cubic','cubic','cubic')
PROOF=module('idle_namespace_proof',ROOT/'idle-policy-proof.py')
SHARE=module('early_share_sidecar_proof',ROOT/'validate-early-share-proof.py')
def validate_arm(index,root,status):
 value=BASE.validate_arm(index,root,status)
 c=root/'results.sender-wait-controls'
 envelopes=[BASE.load(p) for p in sorted(c.glob('*-sender.json'))]
 proof=PROOF.validate(*[PROOF.read(root/name) for name in ('idle-configured.json','idle-endpoints-endpoint-binding.json','idle-endpoints-post-workload.json','idle-host-pre.json','idle-host-post.json')],index,BASE.load(c/'final-raw-write-terminal.json'),envelopes,[json.loads(line) for line in (root/'results.lifecycle.jsonl').read_text().splitlines()])
 share=SHARE.validate(root,index)
 value.update({'intervention':'demand-triggered-already-backed-aggregate-receive-share','idle_policy':PROOF.expected(index),'idle_namespace_proof':proof,'early_share_policy':SHARE.ORDER[index],'early_share_proof':share})
 return value
def decide_screen(arms):
 BASE.require(type(arms) is list and len(arms)==3,'complete three-arm early-share screen required')
 for index,arm in enumerate(arms):BASE.require(type(arm['idle_policy']) is int and arm['idle_policy']==PROOF.expected(index) and arm['idle_namespace_proof']['accepted'] is True and type(arm['idle_namespace_proof']['expected_value']) is int and arm['idle_namespace_proof']['expected_value']==PROOF.expected(index),'independent idle arm expectation differs')
 for index,arm in enumerate(arms):BASE.require(arm['early_share_policy'] is SHARE.ORDER[index] and arm['early_share_proof']['accepted'] is True and arm['early_share_proof']['selected_policy'] is SHARE.ORDER[index],'independent early-share arm expectation differs')
 value=BASE.decide_screen(arms)
 value.update({'diagnostic':'already-backed-receive-share','early_share_order':[0,1,0],'idle_policy_order':[1,1,1],'controller_order':['cubic']*3,'interpretation':'fresh fixed off/on/off demand-triggered receive share from already reserved pool, allCUBIC/default idle1; unchanged five classifier criteria and complete mature outcomes; no originalWAN, parity, merge or release clearance'})
 return value
def main():
 if sys.argv[1]=='arm':value=validate_arm(int(sys.argv[2]),Path(sys.argv[3]),int(sys.argv[4]));out=Path(sys.argv[5])
 elif sys.argv[1]=='screen':value=decide_screen([BASE.load(Path(x)) for x in sys.argv[2:5]]);out=Path(sys.argv[5])
 else:raise ValueError('unknown validation mode')
 PROOF.fresh(out,value);raise SystemExit(0 if value.get('eligible_for_diagnostic',value.get('accepted',False)) else 1)
if __name__=='__main__':main()
