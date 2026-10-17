import datetime,hashlib,json,pathlib,subprocess,tempfile,sys
out=pathlib.Path(__file__).resolve().parent;root=out/'source';commands=[]
short_tmp=tempfile.mkdtemp(prefix='rc.',dir='/tmp');(out/'owned-temp-path.txt').write_text(short_tmp+'\n')
go='/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go'
prefix=['taskset','-c','0-7','env','PATH=/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin:/usr/local/go/bin:/usr/sbin:/usr/bin:/bin','GOTOOLCHAIN=local','GOMAXPROCS=4','CGO_ENABLED=1','GOWORK=off','GOFLAGS=','TMPDIR='+short_tmp,go]
(out/'toolchain.json').write_text(json.dumps({'version':subprocess.check_output([go,'version'],text=True).strip(),'goroot':subprocess.check_output(prefix+['env','GOROOT'],text=True).strip(),'owned_temp':short_tmp},indent=2)+'\n')
checks=[('normal-root-suite',['test','-json','-count=1','-timeout=20m','./...']),('focused-recorder-race',['test','-race','-json','-count=1','-timeout=10m','./internal/server','-run','^(TestPreparedProxyRequestEvent.*|TestObservabilityRecorder.*|TestCloseObservabilityRecorder.*)$']),('recorder-vet',['vet','./internal/db','./internal/server'])]
for label,args in checks:
 record={'label':label,'argv':prefix+args,'cwd':str(root),'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat()}
 with (out/(label+'.log')).open('w') as log:result=subprocess.run(prefix+args,cwd=root,stdout=log,stderr=subprocess.STDOUT)
 record['exit_status']=result.returncode;record['finished_at']=datetime.datetime.now(datetime.timezone.utc).isoformat();commands.append(record)
 (out/'commands.json').write_text(json.dumps(commands,indent=2)+'\n');print(label,'exit',result.returncode,flush=True)
 if result.returncode:break
rows=json.loads((out/'source-files.json').read_text());changed=[r['path'] for r in rows if hashlib.sha256((root/r['path']).read_bytes()).hexdigest()!=r['sha256']]
summary={'accepted':len(commands)==3 and all(x['exit_status']==0 for x in commands),'checks':commands,'source_changed':changed,'no_remeasurement':True}
(out/'summary.json').write_text(json.dumps(summary,indent=2)+'\n')
sys.exit(0 if summary['accepted'] and not changed else 1)
