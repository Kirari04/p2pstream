import copy,json,subprocess,sys
from pathlib import Path
validator=Path(sys.argv[1]); root=Path(sys.argv[2]); source=Path(sys.argv[3]); root.mkdir(exist_ok=True)
base=json.loads(source.read_text())
args_tail=['ccb9c9cb683b555044033d68ff59112aa4bf21d9','40e1046c2f68ee3d545dfd582cd424c2f3604b2a','67d8434cdc2c616c89d2d60ea3cb2d57e37071278c8638942bc82581916e9037','8bdaac6f39e191b6848abfe765a37078e3bd86aae6d8f351d09a531077d651b1','ad818ce955253fd9030cbefa352527321b308cfc574cb4bbee9cbcd0d4930790','98d67ff45e6f4f0da317b7929c9cf7dce704f3946bcf710c9f4a5ae66f254c67','62a0ba54f0d62310e23607bf89a2738b951882529913c71be15a89d42849c9ac','BYgcXD-SYd2g-bJNeoeD/Z6HZzcwwyYyfJOEJDzW8/92rA6hgivcwJ0wvL5nse/6puSapsJomQbQ5Hv2Lzh','JIKNYLZonqODnNvVxDU-/9eYLU6zYGSdifdYeNA8u/RDp6yBTRhftkNRCh8uhL/Ew0Gm_iHMiRl9wzJiJ0Y','/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go','9ac87851f1b88a92f56cedc5bcf9cd2e87113fc559de7a5c83feabf982265a2c']
def run(name,expected,mutate=None):
 d=root/name; d.mkdir(); value=copy.deepcopy(base)
 if mutate: mutate(value)
 path=d/'manifest.json'; path.write_text(json.dumps(value,sort_keys=True)+'\n')
 p=subprocess.run([sys.executable,str(validator),str(path),*args_tail],text=True,capture_output=True)
 (d/'status').write_text(str(p.returncode)+'\n'); (d/'stdout.log').write_text(p.stdout); (d/'stderr.log').write_text(p.stderr)
 print(name,expected,p.returncode,sep='\t'); return p.returncode==expected
cases=[
 ('valid',0,None),
 ('config-value-change',1,lambda m:m['configuration']['server']['aggregate-yamux'].__setitem__('keepalive_interval_nanoseconds',1)),
 ('config-missing-arm',1,lambda m:m['configuration']['agent'].pop('raw-tls')),
 ('config-type-change',1,lambda m:m['configuration']['server']['raw-tls'].__setitem__('keepalive_interval_nanoseconds',False)),
]
raise SystemExit(0 if all(run(*c) for c in cases) else 1)
