import json,subprocess,sys,tempfile
from pathlib import Path
runner=Path(sys.argv[1]); root=Path(sys.argv[2]); root.mkdir(parents=True,exist_ok=True)
text=runner.read_text(); marker='python3 - "$output" "$snapshots_output"'
start=text.index(marker); start=text.index("<<'PY'",start)+len("<<'PY'"); start=text.index('\n',start)+1; end=text.index('\nPY\n',start)
analyzer=root/'final-analyzer.py'; analyzer.write_text(text[start:end]+'\n')
sha='a'*64; source_ref='b'*40; payload='ad818ce955253fd9030cbefa352527321b308cfc574cb4bbee9cbcd0d4930790'
def make(name,smoke,mutation=None,growth=True):
 d=root/name; d.mkdir(); pairs=(1,) if smoke else (1,2); orders={1:['aggregate-yamux','raw-tls'],2:['raw-tls','aggregate-yamux']}; rtt=10 if smoke else 80; one=5 if smoke else 40
 rows=[]; snaps=[]; tops=[]; ns=100
 for pair in pairs:
  for pos,var in enumerate(orders[pair],1):
   tops.append({'schema_version':1,'pair':pair,'order_position':pos,'variant':var,'namespace_identities':{'server':f'net:[{ns}]','router':f'net:[{ns+1}]','agent':f'net:[{ns+2}]'},'configured':{'rtt_ms':rtt,'one_way_netem_ms':one,'rate_bits_per_second':1000000000,'server_tunnel':'10.211.0.1:17443','server_admin':'127.0.0.1:18080','agent_admin':'127.0.0.1:18080'},'cpu_sets':{'server':'0,1,2,3','agent':'4,5,6,7','load':'8'},'measured_rtt_ms':rtt,'network_state':{'server':{},'router':{},'agent':{}}}); ns+=3
   for stage in ('initial','live-post16','terminal'):
    g=1 if growth and var=='aggregate-yamux' and stage!='initial' else 0
    snaps.append({'pair':pair,'variant':var,'stage':stage,'server':{'pool_ledger':{'growth_reserved_bytes':g}},'agent':{'pool_ledger':{'growth_reserved_bytes':g}}})
   for phase in range(16):
    rate=33000000
    if var=='aggregate-yamux' and phase==8: rate=20000000
    rows.append({'pair':pair,'order_position':pos,'variant':var,'phase_index':phase,'measured_rtt_ms':rtt,'useful_bytes_per_second':rate,'phase_name':f'p{phase}','lane':phase%4+1,'direction':'download' if phase%2==0 else 'upload','stage':'conditioning' if phase<8 else 'timed','reuse_kind':'synthetic'})
 if mutation: mutation(rows,snaps,tops)
 for path,vals in ((d/'rows.jsonl',rows),(d/'snapshots.jsonl',snaps),(d/'topologies.jsonl',tops)): path.write_text(''.join(json.dumps(v)+'\n' for v in vals))
 args=[sys.executable,str(analyzer),str(d/'rows.jsonl'),str(d/'snapshots.jsonl'),str(d/'topologies.jsonl'),str(d/'summary.json'),sha,sha,sha,source_ref,payload,'1','1' if smoke else '0',str(rtt),str(one),str(len(pairs))]
 p=subprocess.run(args,text=True,capture_output=True); (d/'stdout.log').write_text(p.stdout); (d/'stderr.log').write_text(p.stderr); (d/'status').write_text(str(p.returncode)+'\n')
 return p.returncode,json.loads((d/'summary.json').read_text())
results=[]
code,s=make('default64-pass',False); results.append(('default64-pass',0,code,s['classification']))
code,s=make('smoke32-pass-no-growth',True,growth=False); results.append(('smoke32-pass-no-growth',0,code,s['classification']+':'+s['aggregate_growth']['status']))
code,s=make('smoke32-wrong-order',True,lambda r,s,t:r.__setitem__(slice(None),list(reversed(r)))); results.append(('smoke32-wrong-order',2,code,s['classification']))
code,s=make('smoke32-wrong-rtt',True,lambda r,s,t:r[0].__setitem__('measured_rtt_ms',80)); results.append(('smoke32-wrong-rtt',2,code,s['classification']))
code,s=make('default64-incomplete',False,lambda r,s,t:r.pop()); results.append(('default64-incomplete',2,code,s['classification']))
for row in results: print('\t'.join(map(str,row)))
if any(expected!=actual for _,expected,actual,_ in results): raise SystemExit(1)
