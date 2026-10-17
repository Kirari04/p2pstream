import copy,json,subprocess,sys
from pathlib import Path
validator=Path(sys.argv[1]); root=Path(sys.argv[2]); actual=Path(sys.argv[3]); manifest_source=Path(sys.argv[4])
root.mkdir(parents=True,exist_ok=True)
source_ref='40e1046c2f68ee3d545dfd582cd424c2f3604b2a'; server='98d67ff45e6f4f0da317b7929c9cf7dce704f3946bcf710c9f4a5ae66f254c67'; agent='62a0ba54f0d62310e23607bf89a2738b951882529913c71be15a89d42849c9ac'; payload='ad818ce955253fd9030cbefa352527321b308cfc574cb4bbee9cbcd0d4930790'
base_manifest=json.loads(manifest_source.read_text())
files={
 'initial-server':'pair-1-aggregate-yamux-server-initial.json','initial-agent':'pair-1-aggregate-yamux-agent-initial.json',
 'live-server':'pair-1-aggregate-yamux-server-live-post16.json','live-agent':'pair-1-aggregate-yamux-agent-live-post16.json',
 'terminal-server':'pair-1-aggregate-yamux-server-terminal.json','terminal-agent':'pair-1-aggregate-yamux-agent-terminal.json'}

def run(name,expected,mutate_docs=None,mutate_manifest=None):
 d=root/name; d.mkdir()
 docs={k:json.loads((actual/v).read_text()) for k,v in files.items()}
 manifest=copy.deepcopy(base_manifest)
 if mutate_docs: mutate_docs(docs)
 if mutate_manifest: mutate_manifest(manifest)
 for k,v in docs.items(): (d/(k+'.json')).write_text(json.dumps(v,sort_keys=True)+'\n')
 mp=d/'build-manifest.json'; mp.write_text(json.dumps(manifest,sort_keys=True)+'\n')
 out=d/'results.jsonl'; snaps=d/'snapshots.jsonl'; out.write_text(''); snaps.write_text('')
 args=[sys.executable,str(validator),'1','1','aggregate-yamux',source_ref,server,agent,(actual/'pair-1-aggregate-yamux-measured-rtt-ms.txt').read_text().strip(),'10','1',payload,str(mp),
       str(d/'initial-server.json'),str(d/'initial-agent.json'),str(d/'live-server.json'),str(d/'live-agent.json'),str(d/'terminal-server.json'),str(d/'terminal-agent.json'),str(actual/'pair-1-aggregate-yamux-phases'),str(out),str(snaps)]
 p=subprocess.run(args,text=True,capture_output=True)
 (d/'command.json').write_text(json.dumps(args,indent=2)+'\n'); (d/'stdout.log').write_text(p.stdout); (d/'stderr.log').write_text(p.stderr); (d/'status').write_text(str(p.returncode)+'\n')
 print(name,expected,p.returncode,sep='\t'); return p.returncode==expected
cases=[
 ('valid-actual',0,None,None),
 ('endpoint-missing-key',1,lambda d:d['initial-server']['configuration'].pop('keepalive_interval_nanoseconds'),None),
 ('endpoint-bool-for-int',1,lambda d:d['live-agent']['configuration'].__setitem__('keepalive_interval_nanoseconds',True),None),
 ('endpoint-wrong-value',1,lambda d:d['terminal-server']['configuration'].__setitem__('keepalive_interval_nanoseconds',1),None),
 ('manifest-missing-agent-raw',1,None,lambda m:m['configuration']['agent'].pop('raw-tls')),
 ('manifest-server-raw-not-object',1,None,lambda m:m['configuration']['server'].__setitem__('raw-tls',[])),
 ('manifest-key-set-differs',1,None,lambda m:m['configuration']['server']['aggregate-yamux'].__setitem__('unexpected',0)),
 ('manifest-invalid-field-type',1,None,lambda m:m['configuration']['agent']['raw-tls'].__setitem__('keepalive_interval_nanoseconds',0.0)),
]
failed=False
for args in cases: failed |= not run(*args)
raise SystemExit(1 if failed else 0)
