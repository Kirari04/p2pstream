#!/usr/bin/env python3
"""Verify the prospective compiled tuple; never execute endpoint binaries."""
from pathlib import Path
import argparse,hashlib,json,os,re,stat
ROOT=Path(__file__).resolve().parents[2]
def sha(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def require(ok,message):
    if not ok:raise ValueError(message)
def read(path,limit=1<<20):
    p=Path(path);m=p.lstat();require(stat.S_ISREG(m.st_mode) and not p.is_symlink() and 0<m.st_size<=limit,'receipt input not bounded regular file')
    return json.loads(p.read_text())
def main():
    parser=argparse.ArgumentParser();parser.add_argument('--receipt',required=True);parser.add_argument('--expected-receipt-sha256',required=True)
    for name in ['server','agent','server-ref','agent-ref']:parser.add_argument('--'+name,required=True)
    args=parser.parse_args();require(re.fullmatch('[0-9a-f]{64}',args.expected_receipt_sha256) is not None,'reviewed receipt hash absent')
    require(sha(args.receipt)==args.expected_receipt_sha256,'reviewed receipt hash changed')
    value=read(args.receipt);plan_path=ROOT/'build-runner-review/COMMANDS.json';plan=read(plan_path)
    require(value.get('schema_version')==1 and value.get('diagnostic')=='prospective-terminal-attribution-build' and value.get('accepted') is True,'build not accepted')
    require(value.get('endpoint_execution') is False and value.get('wan_traffic') is False,'receipt scope changed')
    require(value.get('plan_sha256')==sha(plan_path) and value.get('source_before_after_unchanged') is True,'build plan/source binding changed')
    for row in plan['inputs']:require(sha(row['path'])==row['sha256'],'pinned build/test/tool input changed')
    require(value.get('build_inventory_sha256')==sha(plan['build_inventory']) and value.get('tested_inventory_sha256')==sha(ROOT/'successor/TEST-SOURCE-SHA256SUMS'),'source inventory differs')
    expected=set()
    for line in Path(plan['build_inventory']).read_text().splitlines():
        digest,name=line.split('  ',1);expected.add(name);require(sha(ROOT/name)==digest,'compiled source changed')
    require(expected=={str(p.relative_to(ROOT)) for p in (ROOT/'test-source').rglob('*') if p.is_file()},'compiled source set changed')
    local=read(ROOT/'local-window-2/RESULT.json');require(value.get('local_result_sha256')==sha(ROOT/'local-window-2/RESULT.json') and local.get('complete') is True,'local proof absent')
    require([x.get('name') for x in local['executions']]==['preflight','python-contracts','normal','race'],'local proof cardinality differs')
    require(all(x.get('accepted') is True and type(x.get('exit_status')) is int and x['exit_status']==0 and x.get('forced') is False and x.get('remaining_group_members')==[] for x in local['executions']),'local proof lacks natural success')
    require(value.get('production_reference')==plan['production_reference'] and value.get('source_ref')==plan['source_ref']==args.server_ref==args.agent_ref,'source tuple differs')
    require(value.get('resolved_settings')==plan['expected_settings'],'resolved build settings differ')
    executions=value.get('executions');require(type(executions) is list and len(executions)==len(plan['commands']),'build execution cardinality differs')
    for row,command in zip(executions,plan['commands']):
        require(row.get('name')==command['name'] and row.get('argv')==command['argv'] and row.get('cwd')==command['cwd'],'build argv/cwd changed')
        require(type(row.get('exit_status')) is int and row['exit_status']==0 and row.get('forced') is False and row.get('remaining_group_members')==[],'build did not join naturally')
        for stream in ['stdout','stderr']:require(row.get(stream+'_sha256')==sha(ROOT/'build'/(row['name']+'.'+stream+'.log')),'build log changed')
    for role,path in [('server',args.server),('agent',args.agent)]:
        entry=value['binaries'][role];p=Path(path);require(str(p.resolve())==entry['path'] and p.is_file() and not p.is_symlink() and os.access(p,os.X_OK),'requested endpoint path differs')
        require(entry['sha256']==sha(p) and entry['size']==p.stat().st_size and entry['go_version_m_sha256']==sha(ROOT/'build'/(role+'-metadata.stdout.log')),'requested endpoint binary/metadata differs')
    print(json.dumps({'accepted':True,'diagnostic':'prospective-terminal-build-tuple','reviewed_receipt_sha256':args.expected_receipt_sha256,'source_ref':plan['source_ref']}))
if __name__=='__main__':main()
