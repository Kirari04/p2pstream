#!/usr/bin/env python3
"""Point-in-time read-only transport defaults; no sysctl or socket mutations."""
from pathlib import Path
import json
import os
import stat
import re
import subprocess
import sys
import time

def require(value,message):
    if not value:raise ValueError(message)

def identity(pid):
    text=Path(f'/proc/{pid}/stat').read_text();fields=text[text.rfind(')')+2:].split()
    path=Path(f'/proc/{pid}/ns/net');info=path.stat()
    return {'pid':pid,'start_ticks':int(fields[19]),'namespace':os.readlink(path),'namespace_device':info.st_dev,'namespace_inode':info.st_ino,'pid_namespace':os.readlink(f'/proc/{pid}/ns/pid')}

def current():
    base=Path('/proc/sys/net/ipv4');controller=(base/'tcp_congestion_control').read_text().strip();idle=(base/'tcp_slow_start_after_idle').read_text().strip()
    require(controller=='cubic' and idle=='1','original qualification requires CUBIC/default idle1; no automatic setting change')
    return {**identity(os.getpid()),'parent':identity(os.getppid()),'controller_default':controller,'tcp_slow_start_after_idle':int(idle),'monotonic_ns':time.monotonic_ns(),'boot_id':Path('/proc/sys/kernel/random/boot_id').read_text().strip()}

def remote(pid):
    before=identity(pid)
    result=subprocess.run(['nsenter','-t',str(pid),'-n',sys.executable,'-I','-B',str(Path(__file__).resolve()),'internal-self'],check=True,capture_output=True,text=True,timeout=5,env={'PATH':'/usr/bin:/bin','PYTHONDONTWRITEBYTECODE':'1'})
    require(len(result.stdout)<=8192 and len(result.stderr)<=8192,'default readback output exceeds bound')
    value=json.loads(result.stdout)
    require(before==identity(pid) and all(value[k]==before[k] for k in ('namespace','namespace_device','namespace_inode')),'default readback process/namespace changed')
    return {**value,'subject':before}

def read(path):
    path=Path(path);info=path.lstat()
    require(stat.S_ISREG(info.st_mode) and not path.is_symlink() and 0<info.st_size<=1<<20,'default proof input type/bound differs')
    return json.loads(path.read_text())

def validate(pre,post,host_pre,host_post,outer,lifecycle):
    require(all(type(v['schema_version']) is int and v['schema_version']==1 and v['read_only'] is True and v['point_in_time_only'] is True and v['socket_controller_observed'] is False for v in (pre,post,host_pre,host_post))
            and pre['phase']=='pre' and post['phase']=='post','default proof phases differ')
    starts=[v for v in lifecycle if v.get('event')=='namespace-helpers-start']
    require(len(starts)==1,'unique original helper identity required');start=starts[0]
    roles=[pre[k] for k in ('runner','agent_origin','router')]+[post[k] for k in ('runner','agent_origin','router')]
    hosts=[host_pre['sample'],host_post['sample']]
    for value in roles+hosts:
        require(type(value['tcp_slow_start_after_idle']) is int and value['tcp_slow_start_after_idle']==1
                and value['controller_default']=='cubic' and type(value['monotonic_ns']) is int and value['monotonic_ns']>0
                and value['boot_id']==outer['boot_id']==start['boot_id'],'typed original defaults/clock/boot differ')
        for identity_value in [value,value['parent']]+([value['subject']] if 'subject' in value else []):
            require(all(type(identity_value[k]) is int and identity_value[k]>0 for k in ('pid','start_ticks','namespace_inode'))
                    and type(identity_value['namespace_device']) is int and identity_value['namespace_device']>=0
                    and re.fullmatch(r'net:\[[0-9]+\]',identity_value['namespace'])
                    and re.fullmatch(r'pid:\[[0-9]+\]',identity_value['pid_namespace']),'process/namespace identity malformed')
    require(hosts[0]['parent']==hosts[1]['parent'] and all(v['namespace']==outer['parent']['net']
            and v['parent']['namespace']==outer['parent']['net'] and v['pid_namespace']==outer['parent']['pid'] for v in hosts),
            'host wrapper identity/outer parent namespace changed')
    require(hosts[0]['monotonic_ns']<=min(v['monotonic_ns'] for v in roles[:3])
            and max(v['monotonic_ns'] for v in roles[:3])<min(v['monotonic_ns'] for v in roles[3:])
            and max(v['monotonic_ns'] for v in roles[3:])<=hosts[1]['monotonic_ns'],'host/default pre/post ordering differs')
    require(pre['runner']['parent']==post['runner']['parent'] and all(v['namespace']==start['runner_namespace']
            and v['parent']['namespace']==start['runner_namespace'] and v['parent']['pid']==outer['shell_pid']
            and v['pid_namespace']==v['parent']['pid_namespace']==outer['owned']['pid'] for v in (pre['runner'],post['runner'])),
            'actual nested runner namespace/private PID1 binding differs')
    # The canonical runner creates another netns after the outer boundary's
    # snapshot. Its lifecycle namespace is authoritative; do not equate them.
    require(start['runner_namespace'] not in (outer['owned']['net'],outer['parent']['net']), 'nested canonical runner lost private network boundary')
    for role,label in [('agent_origin','agent'),('router','router')]:
        before,after=pre[role],post[role]
        require(before['subject']==after['subject'] and all(before['subject'][key]==start[label][key] for key in ('pid','start_ticks','namespace'))
                and all(v['namespace']==start[label]['namespace'] and v['pid_namespace']==outer['owned']['pid'] for v in (before,after)),
                'namespace helper lifecycle/pre/post identity differs')
        exits=[v for v in lifecycle if v.get('event')=='process-exit' and v.get('label')==label+'-namespace']
        require(len(exits)==1 and exits[0]['pid']==before['subject']['pid'] and type(exits[0]['status']) is int
                and exits[0]['status']==0 and exits[0]['forced'] is False and exits[0]['group_joined'] is True,
                'default helper identity did not join naturally')
    require(len({pre[k]['namespace'] for k in ('runner','agent_origin','router')})==3,'namespace default roles alias')
    return {'schema_version':1,'accepted':True,'read_only':True,'point_in_time_only':True,'socket_controller_observed':False,
            'scope':'same original nested runner and agent-origin/router helper namespaces; host wrapper parent stable; default CUBIC/idle1'}

def main():
    mode=sys.argv[1]
    if mode=='validate':
        require(len(sys.argv)==4,'validate OUTPUT_DIR OUTPUT required');directory=Path(sys.argv[2])
        lifecycle_path=directory/'results.lifecycle.jsonl';info=lifecycle_path.lstat()
        require(stat.S_ISREG(info.st_mode) and not lifecycle_path.is_symlink() and 0<info.st_size<=1<<20,'lifecycle input type/bound differs')
        value=validate(*[read(directory/name) for name in ('transport-defaults-pre.json','transport-defaults-post.json','transport-defaults-host-pre.json','transport-defaults-host-post.json','outer-namespaces.json')],
                       [json.loads(line) for line in lifecycle_path.read_text().splitlines()])
        with Path(sys.argv[3]).open('x') as handle:json.dump(value,handle,indent=2,sort_keys=True);handle.write('\n')
        return
    if mode=='internal-self':
        require(len(sys.argv)==2,'unexpected internal read arguments');print(json.dumps(current(),sort_keys=True));return
    if mode=='self':
        require(len(sys.argv)==3,'self OUTPUT required');value={'schema_version':1,'phase':'host-point-in-time','sample':current()}
    elif mode=='snapshot':
        require(len(sys.argv)==6 and sys.argv[3] in ('pre','post'),'snapshot OUTPUT pre|post AGENT_HELPER ROUTER_HELPER required')
        value={'schema_version':1,'phase':sys.argv[3],'runner':current(),'agent_origin':remote(int(sys.argv[4])),'router':remote(int(sys.argv[5]))}
        namespaces={(v['namespace_device'],v['namespace_inode']) for v in (value['runner'],value['agent_origin'],value['router'])}
        require(len(namespaces)==3,'transport-default namespace roles alias')
    else:raise ValueError('unknown default proof mode')
    value.update({'read_only':True,'point_in_time_only':True,'socket_controller_observed':False,'scope':'network-namespace defaults before first endpoint/after final workload; no per-socket TCP state claim'})
    with Path(sys.argv[2]).open('x') as handle:json.dump(value,handle,indent=2,sort_keys=True);handle.write('\n')
if __name__=='__main__':main()
