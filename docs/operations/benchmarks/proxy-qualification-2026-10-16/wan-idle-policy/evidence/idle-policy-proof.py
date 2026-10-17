#!/usr/bin/env python3
"""Bounded private-netns idle-policy manipulation and independent proof checks."""
from pathlib import Path
import argparse,json,os,stat,subprocess,sys,time
ROOT=Path(__file__).resolve().parent
SYSCTL='/proc/sys/net/ipv4/tcp_slow_start_after_idle'
ORDER=(1,0,1)
def require(ok,message):
 if not ok:raise ValueError(message)
def expected(index):
 require(type(index) is int and 0<=index<3,'arm index outside fixed domain');return ORDER[index]
def read(path):
 p=Path(path);m=p.lstat();require(stat.S_ISREG(m.st_mode) and not p.is_symlink() and 0<m.st_size<=1<<20,'proof input type/bound differs');return json.loads(p.read_text())
def fresh(path,value):
 with Path(path).open('x') as f:json.dump(value,f,indent=2,sort_keys=True);f.write('\n')
def identity(pid):
 require(type(pid) is int and pid>0,'invalid process PID');s=Path(f'/proc/{pid}/stat').read_text();fields=s[s.rfind(')')+2:].split();p=Path(f'/proc/{pid}/ns/net');m=p.stat()
 return {'pid':pid,'ppid':int(fields[1]),'start_ticks':int(fields[19]),'namespace':os.readlink(p),'namespace_device':m.st_dev,'namespace_inode':m.st_ino}
def current():
 raw=Path(SYSCTL).read_bytes();require(raw in (b'0',b'0\n',b'1',b'1\n'),'sysctl not exact zero/one');return {**identity(os.getpid()),'value':int(raw),'monotonic_ns':time.monotonic_ns(),'time_namespace':os.readlink('/proc/self/ns/time'),'kernel_release':os.uname().release,'boot_id':Path('/proc/sys/kernel/random/boot_id').read_text().strip()}
def parent_sample():
 value=current();parent=identity(os.getppid());require(same_namespace(value,parent),'parent sample crosses namespace');return {**value,**parent}
def same_namespace(a,b):return all(a[k]==b[k] for k in ('namespace','namespace_device','namespace_inode'))
def same_process(a,b):return a['pid']==b['pid'] and a['start_ticks']==b['start_ticks'] and same_namespace(a,b)
def sample(pid,by_file=False):
 before=identity(pid)
 argv=['nsenter',f'--net=/proc/{pid}/ns/net'] if by_file else ['nsenter','-t',str(pid),'-n']
 command=argv+[sys.executable,'-I','-B',str(Path(__file__).resolve()),'self']
 result=subprocess.run(command,check=True,capture_output=True,text=True,timeout=5,env={'PATH':'/usr/bin:/bin','PYTHONDONTWRITEBYTECODE':'1'})
 require(len(result.stdout)<=8192 and len(result.stderr)<=8192,'namespace read output exceeds bound');value=json.loads(result.stdout)
 require(same_process(before,identity(pid)) and same_namespace(before,value),'namespace/process changed during read')
 return {**value,'subject':before,'resolution':'namespace-file' if by_file else 'owned-pid'}
def checked_target(target,runner,host,router):
 require(not same_namespace(target,runner) and not same_namespace(target,host) and not same_namespace(target,router),'target aliases protected namespace')
 require(not same_namespace(runner,host) and not same_namespace(router,runner) and not same_namespace(router,host),'private namespace roles alias')
def write_inside(value,target,runner,host):
 require(type(value) is int and value in (0,1),'write value outside exact domain');here=current()
 require(same_namespace(here,target) and not same_namespace(here,runner) and not same_namespace(here,host),'setter is outside verified target')
 # The sole write in this derivative: fixed path, exact enum, inside checked ns.
 with open(SYSCTL,'w',encoding='ascii') as stream:stream.write(str(value))
 after=current();require(after['value']==value and same_namespace(after,target),'target write readback differs');return after
def configure(out,index,host_file,agent_pid,agent_ticks,agent_ns,router_pid,router_ticks,router_ns):
 want=expected(index);host=read(host_file)['sample'];runner=parent_sample();agent=identity(agent_pid);router=identity(router_pid)
 require(agent['start_ticks']==agent_ticks and agent['namespace']==agent_ns and router['start_ticks']==router_ticks and router['namespace']==router_ns,'owned helper binding differs')
 checked_target(agent,runner,host,router);before=sample(agent_pid);router_before=sample(router_pid)
 payload=json.dumps({'value':want,'target':agent,'runner':runner,'host':host})
 result=subprocess.run(['nsenter','-t',str(agent_pid),'-n',sys.executable,'-I','-B',str(Path(__file__).resolve()),'write',payload],check=True,capture_output=True,text=True,timeout=5,env={'PATH':'/usr/bin:/bin','PYTHONDONTWRITEBYTECODE':'1'})
 require(len(result.stdout)<=8192 and len(result.stderr)<=8192,'setter output exceeds bound');written=json.loads(result.stdout)
 after=sample(agent_pid);independent=sample(agent_pid,by_file=True);runner_after=parent_sample();router_after=sample(router_pid)
 require(same_process(agent,identity(agent_pid)) and same_process(router,identity(router_pid)),'helper changed after write')
 require(written['value']==after['value']==independent['value']==want,'independent target readback differs')
 require(runner_after['value']==runner['value'] and same_process(runner,runner_after) and router_after['value']==router_before['value'],'protected policy changed during setting')
 fresh(out,{'schema_version':1,'phase':'configured-before-endpoints','arm_index':index,'expected_value':want,'sysctl_path':SYSCTL,'host_pre':host,'runner_pre':runner,'runner_after_set':runner_after,'router_pre':router_before,'router_after_set':router_after,'target_original':before,'setter':written,'target_after_set':after,'target_independent_readback':independent,'role_map':{'agent':'target','origin':'target','server':'runner','load':'runner'},'load_scope_evidence':'source-derived exact direct taskset invocation; no observed load PID','configured_monotonic_ns':time.monotonic_ns()})
def endpoints(out,phase,index,config,agent_pid,origin_pid,server_pid,agent_ready,origin_ready,server_ready):
 require(phase in ('endpoint-binding','post-workload'),'invalid endpoint phase');c=read(config);want=expected(index);require(c['arm_index']==index and c['expected_value']==want,'configuration arm binding differs')
 ready={role:{'path':str(Path(path).resolve()),'pid':read(path)['pid']} for role,path in [('agent',agent_ready),('origin',origin_ready),('server',server_ready)]}
 require(all(ready[role]['pid']==pid for role,pid in [('agent',agent_pid),('origin',origin_pid),('server',server_pid)]),'actual readiness PID differs')
 runner=parent_sample();roles={role:sample(pid) for role,pid in [('agent',agent_pid),('origin',origin_pid),('server',server_pid)]}
 require(same_namespace(runner,c['runner_pre']) and runner['value']==c['runner_pre']['value'],'runner policy/namespace changed')
 for role in ('agent','origin'):require(same_namespace(roles[role],c['target_after_set']) and roles[role]['value']==want,'actual agent/origin scope differs')
 require(same_namespace(roles['server'],runner) and roles['server']['value']==runner['value'],'actual server scope differs')
 fresh(out,{'schema_version':1,'phase':phase,'arm_index':index,'expected_value':want,'runner':runner,'roles':roles,'ready_bindings':ready,'router':sample(c['router_pre']['subject']['pid']),'target_helper':sample(c['target_after_set']['subject']['pid']),'monotonic_ns':time.monotonic_ns()})
def validate(config,pre,post,host_pre,host_post,index,terminal,envelopes,lifecycle):
 want=expected(index);require(all(type(v['schema_version']) is int and v['schema_version']==1 for v in (config,pre,post,host_pre,host_post)),'schema differs')
 def namespace_record(v):
  require(type(v['value']) is int and v['value'] in (0,1),'namespace policy outside exact integer domain')
  require(type(v['monotonic_ns']) is int and v['monotonic_ns']>0,'namespace monotonic timestamp malformed')
  if 'subject' in v:require(type(v['subject']['pid']) is int and v['subject']['pid']>0 and type(v['subject']['start_ticks']) is int and v['subject']['start_ticks']>0 and same_namespace(v,v['subject']),'sample subject namespace/identity differs')
  require(all(type(v[k]) is int and v[k]>0 for k in ('pid','start_ticks','namespace_inode')) and type(v['namespace_device']) is int,'namespace/process field types differ')
  require(v['boot_id']==host_pre['sample']['boot_id'] and v['kernel_release']==host_pre['sample']['kernel_release'] and v['time_namespace']==host_pre['sample']['time_namespace'],'namespace kernel/clock identity differs')
 for v in (host_pre['sample'],host_post['sample'],config['host_pre'],config['runner_pre'],config['runner_after_set'],config['router_pre'],config['router_after_set'],config['target_original'],config['setter'],config['target_after_set'],config['target_independent_readback'],pre['runner'],post['runner'],pre['router'],post['router'],pre['target_helper'],post['target_helper'],*pre['roles'].values(),*post['roles'].values()):namespace_record(v)
 require(config['phase']=='configured-before-endpoints' and pre['phase']=='endpoint-binding' and post['phase']=='post-workload','proof phase differs')
 for x in (config,pre,post):require(type(x['arm_index']) is int and x['arm_index']==index and type(x['expected_value']) is int and x['expected_value']==want,'independent expected value differs')
 require(config['sysctl_path']==SYSCTL and config['role_map']=={'agent':'target','origin':'target','server':'runner','load':'runner'} and config['load_scope_evidence']=='source-derived exact direct taskset invocation; no observed load PID','role/path/source-load binding differs')
 hp=host_pre['sample'];hz=host_post['sample'];require(host_pre['phase']=='host-pre' and host_post['phase']=='host-post' and same_process(hp,hz) and hp['value']==hz['value'],'protected outer parent changed')
 runner=config['runner_pre'];target=config['target_after_set'];router=config['router_pre'];checked_target(target,runner,hp,router)
 require(config['host_pre']==hp,'configuration host binding differs')
 original=config['target_original'];require(type(original['value']) is int and original['value'] in (0,1) and same_process(original['subject'],target['subject']) and same_namespace(original,target),'original target observation missing or mismatched')
 require(hp['monotonic_ns']<=config['configured_monotonic_ns']<pre['monotonic_ns']<post['monotonic_ns']<=hz['monotonic_ns'],'outer host does not enclose this arm')
 for v in (runner,target,router,config['setter'],pre['runner'],post['runner'],hz):require(v['boot_id']==hp['boot_id'] and v['time_namespace']==hp['time_namespace'] and v['kernel_release']==hp['kernel_release'],'clock/kernel namespace binding differs')
 require(same_process(runner,config['runner_after_set']) and runner['value']==config['runner_after_set']['value'],'runner changed at set')
 require(same_process(router['subject'],config['router_after_set']['subject']) and same_namespace(router,config['router_after_set']) and router['value']==config['router_after_set']['value'],'router changed at set')
 for key in ('setter','target_after_set','target_independent_readback'):
  v=config[key];require(type(v['value']) is int and v['value']==want and same_namespace(v,target),'target mutation/readback missing or wrong')
 require(config['target_independent_readback']['resolution']=='namespace-file','independent target readback missing')
 require(config['configured_monotonic_ns']<pre['monotonic_ns']<post['monotonic_ns'],'lifecycle phase ordering differs')
 for record in (pre,post):
  require(set(record['roles'])=={'agent','origin','server'},'endpoint role join missing')
  require(same_process(record['runner'],runner) and record['runner']['value']==runner['value'],'protected runner changed')
  require(same_process(record['router']['subject'],router['subject']) and record['router']['value']==router['value'],'protected router changed')
  require(same_process(record['target_helper']['subject'],target['subject']) and record['target_helper']['value']==want,'target helper changed')
  for role in ('agent','origin','server'):
   v=record['roles'][role];reference=runner if role=='server' else target;value=runner['value'] if role=='server' else want
   require(same_namespace(v,reference) and type(v['value']) is int and v['value']==value and same_namespace(v,v['subject']),'actual endpoint namespace/policy differs')
 for role in ('agent','origin','server'):
  require(same_process(pre['roles'][role]['subject'],post['roles'][role]['subject']),'endpoint process replaced')
  require(pre['ready_bindings'][role]==post['ready_bindings'][role] and pre['ready_bindings'][role]['pid']==pre['roles'][role]['subject']['pid'],'readiness endpoint binding differs')
  require(read(pre['ready_bindings'][role]['path'])['pid']==pre['roles'][role]['subject']['pid'],'saved readiness PID differs')
  exits=[v for v in lifecycle if v.get('event')=='process-exit' and (v.get('label')==role if role=='origin' else v.get('label','').endswith('-'+role))]
  require(len(exits)==1 and exits[0]['pid']==pre['roles'][role]['subject']['pid'] and type(exits[0]['status']) is int and exits[0]['status']==0 and exits[0]['forced'] is False and exits[0]['group_joined'] is True,'endpoint natural lifecycle join differs')
 events=[v for v in lifecycle if v.get('event')=='namespace-helpers-start'];require(len(events)==1,'namespace lifecycle binding missing')
 event=events[0];require(event['boot_id']==hp['boot_id'] and event['runner_namespace']==runner['namespace'],'namespace lifecycle runner differs')
 for role,key in [('agent','target_original'),('router','router_pre')]:
  subject=config[key]['subject'];require(all(event[role][k]==subject[k] for k in ('pid','start_ticks','namespace')),'owned namespace helper lifecycle differs')
 require(type(terminal['tcp_slow_start_after_idle_setup']) is int and terminal['tcp_slow_start_after_idle_setup']==want and type(terminal['tcp_slow_start_after_idle_terminal']) is int and terminal['tcp_slow_start_after_idle_terminal']==want and terminal['tcp_slow_start_after_idle_read_error'] is False,'agent setup/terminal intended value differs')
 raws=[r['raw_write_activity'] for r in terminal['records']]
 for envelope in envelopes:raws.extend(r['raw_write_activity'] for r in envelope['response']['send_counters'])
 require(len(terminal['records'])==5 and envelopes,'required owner evidence missing')
 for raw in raws:require(type(raw['tcp_state']['tcp_slow_start_after_idle']) is int and raw['tcp_state']['tcp_slow_start_after_idle']==want,'cached owner value differs from independent intent')
 return {'accepted':True,'arm_index':index,'expected_value':want,'load_scope_observed':False,'point_in_time_only':True}
def main():
 action=sys.argv[1];args=sys.argv[2:]
 if action=='self':print(json.dumps(current()))
 elif action=='write':p=json.loads(args[0]);print(json.dumps(write_inside(p['value'],p['target'],p['runner'],p['host'])))
 elif action=='host':phase,out=args;require(phase in ('host-pre','host-post'),'invalid host phase');fresh(out,{'schema_version':1,'phase':phase,'sample':parent_sample()})
 elif action=='configure':configure(args[0],int(args[1]),args[2],int(args[3]),int(args[4]),args[5],int(args[6]),int(args[7]),args[8])
 elif action=='endpoints':endpoints(args[0],args[1],int(args[2]),args[3],int(args[4]),int(args[5]),int(args[6]),args[7],args[8],args[9])
 else:raise ValueError('unknown proof action')
if __name__=='__main__':main()
