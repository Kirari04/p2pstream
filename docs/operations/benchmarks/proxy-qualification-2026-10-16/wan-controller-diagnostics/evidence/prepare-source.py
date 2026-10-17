from pathlib import Path
import hashlib,shutil,json
root=Path(__file__).resolve().parent
old=root.parent/'wan-terminal-attribution-20261017'
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
rows=[]
for line in (old/'build-runner-review/BUILD-SOURCE-SHA256SUMS').read_text().splitlines():
 digest,name=line.split('  ',1); src=old/name; assert sha(src)==digest
 dst=root/name; dst.parent.mkdir(parents=True,exist_ok=True); assert not dst.exists();shutil.copy2(src,dst)
 assert src.stat().st_ino != dst.stat().st_ino
 rows.append({'path':name,'sha256':digest,'bytes':src.stat().st_size})
assert len(rows)==460
for src in (old/'runner-source/integration').rglob('*'):
 if src.is_file() and 'runs' not in src.relative_to(old/'runner-source/integration').parts and '__pycache__' not in src.parts:
  dst=root/src.relative_to(old);dst.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(src,dst)
(root/'BASE-SOURCE-INVENTORY.json').write_text(json.dumps({'source_ref':json.loads((old/'build/receipt.json').read_text())['source_ref'],'files':rows},indent=2)+'\n')
(root/'BASE-INPUTS.json').write_text(json.dumps([{'path':str(p),'sha256':sha(p)} for p in [old/'build/receipt.json',old/'build-runner-review/BUILD-SOURCE-SHA256SUMS',old/'smoke-freeze/COMMANDS.json',old/'sustained-plan-2/COMMANDS.json',old/'runner-source/integration/run-wan-below-tls-arm.sh',root.parent/'source-h2-next-review-20261017/ASSESSMENT.md',root.parent/'source-h2-next-review-20261017/INPUTS.json']],indent=2)+'\n')
