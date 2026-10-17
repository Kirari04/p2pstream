import copy,json,subprocess,sys
from pathlib import Path
runner=Path(sys.argv[1]); root=Path(sys.argv[2]); root.mkdir(parents=True,exist_ok=True)
text=runner.read_text(); marker='python3 - "$pair" "$order_position"'
start=text.index(marker); start=text.index("<<'PY'",start)+len("<<'PY'"); start=text.index('\n',start)+1; end=text.index('\nPY\n',start)
validator=root/'cohort-validator.py'; validator.write_text(text[start:end]+'\n')
payload='ad818ce955253fd9030cbefa352527321b308cfc574cb4bbee9cbcd0d4930790'; source_ref='b'*40; binary='a'*64
schedule=[('conditioning-download-lane-1',1,'download','conditioning','fresh-stream'),('conditioning-download-lane-2',2,'download','conditioning','fresh-stream'),('conditioning-download-lane-3',3,'download','conditioning','fresh-stream'),('conditioning-download-lane-4',4,'download','conditioning','fresh-stream'),('conditioning-upload-lane-1',1,'upload','conditioning','fresh-stream'),('conditioning-upload-lane-2',2,'upload','conditioning','fresh-stream'),('conditioning-upload-lane-3',3,'upload','conditioning','fresh-stream'),('conditioning-upload-lane-4',4,'upload','conditioning','fresh-stream'),('retained-download-lane-1',1,'download','retained','new-persistent'),('retained-upload-lane-2',2,'upload','retained','new-persistent'),('timed-1-download-lane-3',3,'download','timed','new-persistent'),('timed-1-upload-lane-4',4,'upload','timed','new-persistent'),('timed-2-upload-lane-1',1,'upload','timed','direction-transition'),('timed-2-download-lane-2',2,'download','timed','direction-transition'),('timed-3-download-lane-3',3,'download','timed','same-direction-reuse'),('timed-3-upload-lane-4',4,'upload','timed','same-direction-reuse')]
tcp_keys={'state','ca_state','retransmits','probes','backoff','options','rto_us','ato_us','snd_mss','rcv_mss','unacked','sacked','lost','retrans','fackets','last_data_sent_ms','last_ack_sent_ms','last_data_recv_ms','last_ack_recv_ms','pmtu','rcv_ssthresh','rtt_us','rttvar_us','snd_ssthresh','snd_cwnd','advmss','reordering','rcv_rtt_us','rcv_space','total_retrans','pacing_rate','delivery_rate','bytes_acked','bytes_received','bytes_sent','notsent_bytes','snd_wnd','rcv_wnd'}
def config(role):
 d={'physical_lanes':5,'data_lanes':4,'tls_minimum_version':771,'tls_next_protocol':'http/1.1','tls_server_name':'transport-ceiling.test','tls_client_certificates':False,'yamux_enabled':True,'yamux_accept_backlog':256,'control_incoming_hard_bound':-1 if role=='server' else 4,'data_incoming_hard_bound':-1 if role=='server' else 16,'initial_byte_window_bytes':524288,'maximum_byte_window_bytes':1073741824,'initial_session_allocation_bytes':2097152,'maximum_session_allocation_bytes':1073741824,'initial_stream_allocation_bytes':524288,'maximum_stream_allocation_bytes':268435456,'maximum_data_frame_bytes':32768,'keep_alive_interval_nanoseconds':20000000000,'connection_write_timeout_nanoseconds':10000000000,'stream_open_timeout_nanoseconds':10000000000,'stream_close_timeout_nanoseconds':30000000000}
 return d
def sockets(role,connected=True):
 out=[]
 for lane in range(5):
  sp=f'10.211.0.1:{10000+lane}'; ap=f'10.212.0.2:{20000+lane}'
  local,remote=(sp,ap) if role=='server' else (ap,sp)
  out.append({'lane':lane,'lane_role':'control' if lane==0 else 'data','local_address':local,'remote_address':remote,'connection_identity':f'id-{lane}','tls_version':772,'tls_cipher_suite':4865,'connected':connected,'tcp_info_captured':connected,'tcp_info_error':'','tcp_state':1 if connected else 7})
 return out
def pool(stage):
 initial=8388608; growth=1048576 if stage!='initial' else 0; high=initial+growth
 return {'limit_bytes':2147483648,'initial_reserved_bytes':initial,'growth_reserved_bytes':growth,'current_bytes':0 if stage=='terminal' else high,'high_water_bytes':high,'denied_reservations':0,'release_count':5 if stage=='terminal' else 0,'final_bytes':0}
def working(stage):
 cats={'session_metadata':0 if stage=='terminal' else 100,'stream_metadata':0,'relay_buffers':0,'frame_buffers':0,'tls_app_buffers':0}
 return {'limit_bytes':67108864,'initial_reserved_bytes':0,'growth_reserved_bytes':0,'current_bytes':sum(cats.values()),'high_water_bytes':100,'denied_reservations':0,'release_count':1 if stage=='terminal' else 0,'final_bytes':0,'categories':cats,'actual_heap_alloc_bytes':1,'actual_open_fds':1,'actual_socket_fds':0 if stage=='terminal' else 5}
def tcp(role,lane):
 row=sockets(role)[lane]; d={k:0 for k in tcp_keys}; d.update({'captured':True,'error':'','local_address':row['local_address'],'remote_address':row['remote_address'],'app_limited_available':False}); return d
def phases(role):
 out=[]
 for i,(name,lane,direction,stage,reuse) in enumerate(schedule):
  wall=1000000000; setup=1000
  out.append({'schema_version':1,'phase_index':i,'phase_name':name,'lane':lane,'direction':direction,'stage':stage,'reuse_kind':reuse,'configured_payload_bytes':268435456,'received_payload_bytes':268435456,'expected_sha256':payload,'received_sha256':payload,'phase_wall_nanoseconds':wall,'setup_nanoseconds':setup,'payload_completion_nanoseconds':wall-setup,'useful_bytes_per_second':268435456.0,'tcp_info':tcp(role,lane)})
 return out
def top(role,stage):
 completed=0 if stage=='initial' else 16; d={'schema_version':1,'process_role':role,'variant':'aggregate-yamux','source_ref':source_ref,'expected_phases':16,'completed_phases':completed,'invalid_reason':'','configuration':config(role),'pool_ledger':pool(stage),'working_ledger':working(stage),'reconnect_count':0,'errors':[]}
 if stage=='initial': d.update({'ready':True,'physical_sockets':sockets(role)})
 elif stage=='live': d.update({'ready':True,'graceful_shutdown':False,'same_socket_identities':True,'active_logical_streams':0,'joined_logical_relays':True,'joined_physical_relays':False,'physical_sockets_initial':sockets(role),'physical_sockets_terminal':sockets(role),'results':phases(role)})
 else: d.update({'ready':False,'graceful_shutdown':True,'same_socket_identities':True,'active_logical_streams':0,'joined_logical_relays':True,'joined_physical_relays':True,'physical_sockets_initial':sockets(role),'physical_sockets_terminal':sockets(role,False),'results':phases(role)})
 return d
def make(name,mutate=None):
 d=root/name; d.mkdir(); values={(stage,role):top(role,stage) for stage in ('initial','live','terminal') for role in ('server','agent')}
 responses=copy.deepcopy(values[('live','server')]['results'])
 if mutate: mutate(values,responses)
 for (stage,role),value in values.items(): (d/f'{stage}-{role}.json').write_text(json.dumps(value))
 pd=d/'phases'; pd.mkdir()
 for i,v in enumerate(responses): (pd/f'phase-{i:02d}.json').write_text(json.dumps(v))
 args=[sys.executable,str(validator),'1','1','aggregate-yamux',source_ref,binary,binary,'80','80','0',payload,str(d/'initial-server.json'),str(d/'initial-agent.json'),str(d/'live-server.json'),str(d/'live-agent.json'),str(d/'terminal-server.json'),str(d/'terminal-agent.json'),str(pd),str(d/'out.jsonl'),str(d/'snap.jsonl')]
 p=subprocess.run(args,text=True,capture_output=True); (d/'stderr.log').write_text(p.stderr); (d/'stdout.log').write_text(p.stdout); (d/'status').write_text(str(p.returncode)+'\n'); return p.returncode
cases=[('valid',0,None),('missing-config',1,lambda v,r:v[('initial','server')]['configuration'].pop('physical_lanes')),('bool-lane',1,lambda v,r:v[('live','server')]['results'][0].__setitem__('lane',True)),('wrong-digest',1,lambda v,r:v[('live','server')]['results'][0].__setitem__('received_sha256','0'*64)),('terminal-ledger-live',1,lambda v,r:v[('terminal','agent')]['pool_ledger'].__setitem__('current_bytes',1)),('tcp-not-established',1,lambda v,r:v[('live','server')]['physical_sockets_terminal'][0].__setitem__('tcp_state',7)),('identity-replaced',1,lambda v,r:v[('live','agent')]['physical_sockets_terminal'][1].__setitem__('connection_identity','replacement'))]
failed=False
for name,expected,mutate in cases:
 actual=make(name,mutate); print(name,expected,actual,sep='\t'); failed |= actual!=expected
raise SystemExit(1 if failed else 0)
