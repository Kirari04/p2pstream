import datetime,hashlib,json,pathlib,subprocess,tempfile
root=pathlib.Path.cwd();out=root/'tmp/recorder-allocation-next-20261017/integration-validation-1';commands=[]
short_tmp=tempfile.mkdtemp(prefix='ri.',dir='/tmp')
(out/'owned-temp-path.txt').write_text(short_tmp+'\n')
go='/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go'
prefix=['taskset','-c','0-7','env','PATH=/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin:/usr/local/go/bin:/usr/sbin:/usr/bin:/bin','GOTOOLCHAIN=local','GOMAXPROCS=4','CGO_ENABLED=1','GOWORK=off','GOFLAGS=','TMPDIR='+short_tmp,go]
manifest={'head':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'go_version':subprocess.check_output([go,'version'],text=True).strip(),'runtime_source_sha256':hashlib.sha256((root/'internal/db/proxy_request_event_inserter.go').read_bytes()).hexdigest(),'permanent_test_sha256':hashlib.sha256((root/'internal/server/observability_prepared_insert_test.go').read_bytes()).hexdigest(),'screen_candidate_runtime_sha256':json.loads((root/'tmp/recorder-allocation-next-20261017/manifest.json').read_text())['candidate_runtime_sha256'],'owned_temp':short_tmp,'no_remeasurement':True}
assert manifest['runtime_source_sha256']==manifest['screen_candidate_runtime_sha256']
(out/'input-manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
checks=[('normal-root-suite',['test','-json','-count=1','-timeout=20m','./...']),('focused-recorder-race',['test','-race','-json','-count=1','-timeout=10m','./internal/server','-run','^(TestPreparedProxyRequestEvent.*|TestObservabilityRecorder.*|TestCloseObservabilityRecorder.*)$']),('recorder-vet',['vet','./internal/db','./internal/server'])]
for label,args in checks:
 record={'label':label,'argv':prefix+args,'cwd':str(root),'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat()}
 with (out/(label+'.log')).open('w') as log:result=subprocess.run(prefix+args,cwd=root,stdout=log,stderr=subprocess.STDOUT)
 record['exit_status']=result.returncode;record['finished_at']=datetime.datetime.now(datetime.timezone.utc).isoformat();commands.append(record)
 (out/'commands.json').write_text(json.dumps(commands,indent=2)+'\n')
 print(label,'exit',result.returncode,flush=True)
summary={'accepted':all(x['exit_status']==0 for x in commands),'checks':commands,'runtime_source_unchanged':hashlib.sha256((root/'internal/db/proxy_request_event_inserter.go').read_bytes()).hexdigest()==manifest['runtime_source_sha256'],'permanent_test_unchanged':hashlib.sha256((root/'internal/server/observability_prepared_insert_test.go').read_bytes()).hexdigest()==manifest['permanent_test_sha256']}
(out/'summary.json').write_text(json.dumps(summary,indent=2)+'\n')
if not summary['accepted']:raise SystemExit(1)
