import datetime,json,os,pathlib,subprocess
repo=pathlib.Path.cwd();out=repo/'tmp/recorder-allocation-next-20261017/screen-attempt-1';commands=[]
test_names=['TestRecorderNullableIDPersistenceSingleAndBatch','TestRecorderNullableIDEdgeValuesMatchGeneratedDatabaseInsert','TestPreparedProxyRequestEventBatchPreservesAtomicRowsRollupsAndTouches','TestPreparedProxyRequestEventBatchCancellationRollsBack','TestObservabilityRecorderFlushesQueuedEventsAsAggregatedRollups','TestObservabilityRecorderCoalescesPublicCacheTouches','TestObservabilityRecorderCoalescesCacheTouchesPerGeneration','TestObservabilityRecorderBoundsPendingEventsWhenDatabaseFails','TestCloseObservabilityRecorderFlushesQueuedEvents','TestCloseObservabilityRecorderCancelsActiveFlushAndDrains','TestObservabilityRecorderCanceledCloseCanRetryWithFreshContext']
regex='^('+'|'.join(test_names)+')$'
(out/'correctness-test-names.json').write_text(json.dumps(test_names,indent=2)+'\n')
def run(label,argv,cwd):
    entry={'label':label,'argv':argv,'cwd':str(cwd),'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat()};commands.append(entry)
    with (out/'logs'/(label+'.log')).open('w') as log:p=subprocess.run(argv,cwd=cwd,stdout=log,stderr=subprocess.STDOUT)
    entry['exit_status']=p.returncode;entry['finished_at']=datetime.datetime.now(datetime.timezone.utc).isoformat()
    (out/'preparation-commands.json').write_text(json.dumps(commands,indent=2)+'\n')
    print(label,'exit',p.returncode,flush=True)
    if p.returncode:raise SystemExit(p.returncode)
build_prefix=['taskset','-c','0-7','env','PATH=/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin:/usr/local/go/bin:/usr/bin:/bin','GOTOOLCHAIN=local','GOMAXPROCS=4','CGO_ENABLED=1','GOWORK=off','GOFLAGS=','TMPDIR='+str(out/'go-tmp'),'/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go']
run('baseline-correctness',['taskset','-c','0-7','env','GOMAXPROCS=4',str(out/'bin/baseline.test'),'-test.run',regex,'-test.v','-test.timeout=120s'],out/'sources/baseline')
run('candidate-build',build_prefix+['test','-c','-o',str(out/'bin/candidate.test'),'./internal/server'],out/'sources/candidate')
run('candidate-correctness',['taskset','-c','0-7','env','GOMAXPROCS=4',str(out/'bin/candidate.test'),'-test.run',regex,'-test.v','-test.timeout=120s'],out/'sources/candidate')
run('candidate-race-build',build_prefix+['test','-race','-c','-o',str(out/'bin/candidate-race.test'),'./internal/server'],out/'sources/candidate')
run('candidate-correctness-race',['taskset','-c','0-7','env','GOMAXPROCS=4',str(out/'bin/candidate-race.test'),'-test.run',regex,'-test.v','-test.timeout=120s'],out/'sources/candidate')
