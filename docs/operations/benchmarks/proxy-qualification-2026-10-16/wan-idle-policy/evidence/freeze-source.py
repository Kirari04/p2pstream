"""Freeze only necessary derivative/reference source; never execute work."""
from pathlib import Path
import hashlib,json
ROOT=Path(__file__).resolve().parent
OLD=ROOT.parent/'wan-controller-screen-20261017'
plan=ROOT.parent/'wan-regression-causal-reset-20261017/IDLE-POLICY-PLAN.md'
files=list(ROOT.glob('*.py'))+list(ROOT.glob('*.sh'))+list(ROOT.glob('*.diff'))+[ROOT/'COMMANDS.json',ROOT/'README.md',ROOT/'contracts/test-idle-policy.py',ROOT/'STATIC-CHECKS.json',plan,OLD/'run-controller-policy.py',OLD/'FROZEN-INPUTS.json',OLD/'policy-design/SOURCE.json',OLD/'REVIEWED-RECEIPT-SHA256',OLD/'build/receipt.json']
for name in ['validate-controller-policy.py','validate-controller-arm-integrity.py','run-wan-below-tls-arm-joined-ps.sh','generated/qualify-proxy-agent-growth-wan-below-tls.sh','verify-terminal-build.py','validate-wan-below-tls-arm.py','validate-arm-outcome.py','validate-raw-write-activity.py','validate-server-receive-growth.py','validate-tcp-state.py','validate-byte-credit-recycle.py','validate-sender-wait-snapshot.py','validate-copy-record.py','validate-terminal-attribution.py','validate-post-join-raw-closure.py','LEGACY-VALIDATOR.json']:
 files.append(OLD/'runner-source/integration'/name)
saved=OLD/'runner-source/integration/runs/controller-crc-policy-once'
files.extend([saved/f'arm-{i}.decision.json' for i in range(3)]+[saved/'arm-2-cubic/results.sender-wait-controls/sequence-9-post-sender.json'])
rows=[]
for p in sorted(set(files)):
 data=p.read_bytes();rows.append({'path':str(p.resolve()),'bytes':len(data),'sha256':hashlib.sha256(data).hexdigest()})
(ROOT/'SOURCE.json').write_text(json.dumps({'schema_version':1,'source_only':True,'runtime_production_binding':'accepted original27c8e31+observer/controller build receipt; no newGo','policy_order':[1,0,1],'controller_order':['cubic']*3,'reference_inventory':str((OLD/'FROZEN-INPUTS.json').resolve()),'files':rows},indent=2,sort_keys=True)+'\n')
