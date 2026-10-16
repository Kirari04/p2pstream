#!/usr/bin/env python3
import hashlib, json, pathlib, subprocess, sys
root = pathlib.Path(sys.argv[1])
issues=[]
def load(path): return json.loads((root/path).read_text())
def lines(path): return [json.loads(x) for x in (root/path).read_text().splitlines() if x]
def check(ok,msg):
    if not ok: issues.append(msg)
rows=lines('h1/results.jsonl')
check(len(rows)==8,'expected eight completed conditioning rows before target sequence 9')
expected=[(i,'warmup','download',i) for i in range(1,5)]+[(i,'warmup','upload',i-4) for i in range(5,9)]
cert_der=subprocess.run(['openssl','x509','-in',str(root/'tls/public.test.crt.pem'),'-outform','DER'],check=True,capture_output=True).stdout
leaf=hashlib.sha256(cert_der).hexdigest()
row_summaries=[]
for row,want in zip(rows,expected):
    seq,stage,direction,rep=want
    check((row.get('diagnostic_sequence'),row.get('measurement_stage'),row.get('transfer_direction'),row.get('repetition'))==want,f'row {seq} schedule mismatch')
    check(row.get('protocol')=='h1' and row.get('public_protocol')=='h1',f'row {seq} protocol mismatch')
    check(row.get('attempted')==1 and row.get('useful_completed')==1 and row.get('errors')==0 and row.get('status_codes')=={'200':1},f'row {seq} response mismatch')
    tls=row.get('wan_response_tls',[])
    check(len(tls)==1 and tls[0].get('http_major')==1 and tls[0].get('negotiated_alpn')=='http/1.1' and tls[0].get('peer_leaf_der_sha256')==leaf,f'row {seq} TLS mismatch')
    upstream=row.get('upstream_transport_trace',{})
    copy=row.get('response_copy_attribution',{})
    check(upstream.get('sequence')==str(seq) and len(upstream.get('matches',[]))==1 and not upstream.get('overflow'),f'row {seq} upstream trace mismatch')
    check(copy.get('sequence')==str(seq) and len(copy.get('matches',[]))==1 and not copy.get('overflow'),f'row {seq} copy record mismatch')
    metric=copy.get('matches',[{}])[0].get('copy',{})
    expected_bytes=4194304 if direction=='download' else 32
    check(metric.get('complete') is True and metric.get('body_wraps')==1 and metric.get('downstream_read_from_calls')==0,f'row {seq} copy completeness mismatch')
    check(metric.get('upstream_read',{}).get('bytes')==expected_bytes and metric.get('downstream_write',{}).get('bytes')==expected_bytes,f'row {seq} copy bytes mismatch')
    check(metric.get('read_errors')==0 and metric.get('write_errors')==0 and metric.get('short_writes')==0,f'row {seq} copy error counters nonzero')
    row_summaries.append({'sequence':seq,'stage':stage,'direction':direction,'repetition':rep,'wall_seconds':row.get('wall_seconds'),'useful_transfer_bytes_per_second':row.get('useful_transfer_bytes_per_second'),'origin_connection_id':tls[0].get('origin_connection_id') if tls else None})
records=lines('h1/results.read-wait-trace-records.jsonl')
check(len(records)==1,'expected one rejected target-sequence trace control record')
record=records[0] if records else {}
check(record.get('sequence')==9 and record.get('pre_runtime_status')==0 and record.get('pre_cgroup_status')==0 and record.get('pre_resource_status')==0 and record.get('pre_healthy_status')==1 and record.get('complete') is False and record.get('valid') is False,'sequence 9 pre-health rejection contract mismatch')
check(not (root/'h1/results.read-wait-traces/runtime-trace-sequence-9.out').exists(),'unexpected sequence 9 raw trace')
lifecycle=lines('h1/results.lifecycle.jsonl')
check(len(lifecycle)==2 and lifecycle[0].get('event')=='namespace-helpers-start' and lifecycle[1].get('event')=='forced-cleanup' and lifecycle[1].get('invalidates_success') is True,'inner runner lifecycle mismatch')
supervisor=load('h1/supervisor-lifecycle.json')
check(supervisor.get('runner_status')==1 and supervisor.get('group_joined') is True and supervisor.get('forced') is False and supervisor.get('invalidates_success') is True,'supervisor lifecycle mismatch')
check(supervisor.get('server_scope',{}).get('systemd_run_status')==0 and supervisor.get('server_scope',{}).get('resource_status')==1 and supervisor.get('server_scope',{}).get('unit_joined') is True and supervisor.get('server_scope',{}).get('unit_query_status')==0,'scope lifecycle mismatch')
unit=load('h1/server-scope/unit-final.json')
check(unit.get('joined') is True and (unit.get('load_state'),unit.get('active_state'),unit.get('sub_state'))==('not-found','inactive','dead') and unit.get('query_status')==0,'unit terminal tuple mismatch')
pre=load('h1/server-scope/server-cgroup-pre-traffic.json')
check(pre.get('memory_max_bytes')==4294967296 and pre.get('memory_swap_max_bytes')==0 and len(pre.get('members',[]))==1 and all(pre.get('memory_events',{}).get(k)==0 for k in ('high','max','oom','oom_kill','oom_group_kill')),'pre-traffic cgroup mismatch')
final=load('final-status.json')
check(final.get('accepted') is False and final.get('h1',{}).get('status')==1 and final.get('h1',{}).get('safe_to_continue') is False and final.get('h2',{}).get('started') is False and final.get('h2',{}).get('status')==125 and final.get('pre_post_hashes_equal') is True,'pair final status mismatch')
value={'schema_version':1,'accepted':False,'offline_audit_complete':not issues,'issues':issues,'failure':{'phase':'H1 before target sequence 9 transfer','gate':'pre_healthy_status','cause':'server runtime snapshot has agent_tunnels and no agent_capacity; generated runner tested .agent_capacity.pressure','trace_record':record},'completed_rows':row_summaries,'certificate_leaf_der_sha256':leaf,'trace_files_created':0,'parser_status':int((root/'h1/read-wait-reductions/parser-all.status').read_text()),'analyzer_status':int((root/'h1/read-wait-reductions/analyzer-all.status').read_text()),'resource_post_status':int((root/'h1/server-scope/server-resource.status').read_text()),'inner_lifecycle':lifecycle,'supervisor_lifecycle':supervisor,'unit_final':unit,'pre_traffic_cgroup':pre,'pair_final':final,'no_retry':True}
print(json.dumps(value,indent=2,sort_keys=True))
sys.exit(0 if not issues else 1)
