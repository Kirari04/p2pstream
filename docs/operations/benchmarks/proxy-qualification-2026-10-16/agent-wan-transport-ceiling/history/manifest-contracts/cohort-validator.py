import ipaddress,json,math,re,sys
from pathlib import Path

(pair,order_position,variant,source_ref,server_sha,agent_sha,measured_rtt,configured_rtt,fixture_smoke,expected_payload_sha,build_manifest_path,
 initial_server_path,initial_agent_path,live_server_path,live_agent_path,
 terminal_server_path,terminal_agent_path,phases_dir,results_path,snapshots_path)=sys.argv[1:]
pair=int(pair); order_position=int(order_position); measured_rtt=float(measured_rtt); configured_rtt=int(configured_rtt); fixture_smoke=fixture_smoke=="1"
phase_dir=Path(phases_dir)
sha=re.compile(r"[0-9a-f]{64}\Z")
expected_arm={(1,1):"aggregate-yamux",(1,2):"raw-tls",(2,1):"raw-tls",(2,2):"aggregate-yamux"}
if (pair,order_position) not in expected_arm or variant!=expected_arm[(pair,order_position)]:
    raise ValueError("pair/order/variant input does not match the predeclared arm order")
if not re.fullmatch(r"[0-9a-f]{40}",source_ref) or not sha.fullmatch(server_sha) or not sha.fullmatch(agent_sha) or not sha.fullmatch(expected_payload_sha):
    raise ValueError("source or binary identity input is invalid")
minimum_rtt,maximum_rtt=((5,25) if fixture_smoke else (60,120))
if configured_rtt!=(10 if fixture_smoke else 80) or not math.isfinite(measured_rtt) or not minimum_rtt <= measured_rtt <= maximum_rtt:
    raise ValueError("measured RTT input is outside the diagnostic envelope")
schedule=[
 ("conditioning-download-lane-1",1,"download","conditioning","fresh-stream"),
 ("conditioning-download-lane-2",2,"download","conditioning","fresh-stream"),
 ("conditioning-download-lane-3",3,"download","conditioning","fresh-stream"),
 ("conditioning-download-lane-4",4,"download","conditioning","fresh-stream"),
 ("conditioning-upload-lane-1",1,"upload","conditioning","fresh-stream"),
 ("conditioning-upload-lane-2",2,"upload","conditioning","fresh-stream"),
 ("conditioning-upload-lane-3",3,"upload","conditioning","fresh-stream"),
 ("conditioning-upload-lane-4",4,"upload","conditioning","fresh-stream"),
 ("retained-download-lane-1",1,"download","retained","new-persistent"),
 ("retained-upload-lane-2",2,"upload","retained","new-persistent"),
 ("timed-1-download-lane-3",3,"download","timed","new-persistent"),
 ("timed-1-upload-lane-4",4,"upload","timed","new-persistent"),
 ("timed-2-upload-lane-1",1,"upload","timed","direction-transition"),
 ("timed-2-download-lane-2",2,"download","timed","direction-transition"),
 ("timed-3-download-lane-3",3,"download","timed","same-direction-reuse"),
 ("timed-3-upload-lane-4",4,"upload","timed","same-direction-reuse"),
]
assert len(schedule)==16

def load(path): return json.loads(Path(path).read_text())
def integer(value,name,minimum=0):
    if isinstance(value,bool) or not isinstance(value,int) or value < minimum: raise ValueError(f"{name} must be integer >= {minimum}")
    return value
def signed_integer(value,name):
    if isinstance(value,bool) or not isinstance(value,int): raise ValueError(f"{name} must be an integer")
    return value
def number(value,name,positive=False):
    if isinstance(value,bool) or not isinstance(value,(int,float)) or not math.isfinite(value) or (positive and value <= 0): raise ValueError(f"{name} invalid")
    return float(value)
def string(value,name,empty_ok=True):
    if not isinstance(value,str) or (not empty_ok and not value): raise ValueError(f"{name} invalid")
    return value
def address(value,name):
    string(value,name,False)
    host,port=value.rsplit(":",1)
    ipaddress.ip_address(host.strip("[]")); integer(int(port),name+" port",1)
    if int(port)>65535: raise ValueError(f"{name} port invalid")

configuration_roles={"server","agent"}
configuration_variants={"aggregate-yamux","raw-tls"}
build_manifest=load(build_manifest_path)
manifest_configuration=build_manifest.get("configuration") if isinstance(build_manifest,dict) else None
if not isinstance(manifest_configuration,dict) or set(manifest_configuration)!=configuration_roles:
    raise ValueError("build manifest configuration roles")
configuration_key_set=None
for manifest_role in sorted(configuration_roles):
    role_configuration=manifest_configuration.get(manifest_role)
    if not isinstance(role_configuration,dict) or set(role_configuration)!=configuration_variants:
        raise ValueError(f"build manifest {manifest_role} configuration variants")
    for manifest_variant in sorted(configuration_variants):
        expected=role_configuration.get(manifest_variant)
        if not isinstance(expected,dict) or not expected:
            raise ValueError(f"build manifest {manifest_role}/{manifest_variant} configuration schema")
        if configuration_key_set is None:
            configuration_key_set=set(expected)
        elif set(expected)!=configuration_key_set:
            raise ValueError("build manifest configuration key sets differ")
        for key,item in expected.items():
            if not isinstance(key,str) or not key:
                raise ValueError("build manifest configuration key invalid")
            if isinstance(item,bool):
                continue
            if isinstance(item,int):
                continue
            if isinstance(item,str):
                continue
            raise ValueError(f"build manifest configuration type invalid: {manifest_role}/{manifest_variant}/{key}")

def configuration(value,role):
    expected=manifest_configuration[role][variant]
    if not isinstance(value,dict) or set(value)!=set(expected):
        raise ValueError(f"{role} configuration schema")
    for key,expected_item in expected.items():
        item=value[key]
        if isinstance(expected_item,bool):
            if not isinstance(item,bool): raise ValueError(f"{role}.configuration.{key} must be boolean")
        elif isinstance(expected_item,int):
            if isinstance(item,bool) or not isinstance(item,int): raise ValueError(f"{role}.configuration.{key} must be integer")
        elif isinstance(expected_item,str):
            if not isinstance(item,str): raise ValueError(f"{role}.configuration.{key} must be string")
        else:
            raise ValueError(f"{role}.configuration.{key} has unsupported manifest type")
    if value!=expected:
        raise ValueError(f"{role} configuration differs from pinned manifest")

socket_keys={"lane","lane_role","local_address","remote_address","connection_identity","tls_version","tls_cipher_suite","connected","tcp_info_captured","tcp_info_error","tcp_state"}
socket_identity_keys=socket_keys-{"connected","tcp_info_captured","tcp_info_error","tcp_state"}
def sockets(rows,role,connected,require_established=False):
    if not isinstance(rows,list) or len(rows)!=5: raise ValueError(f"{role} must have five sockets")
    by_lane={}
    for row in rows:
        if not isinstance(row,dict) or set(row)!=socket_keys: raise ValueError(f"{role} socket schema")
        lane=integer(row["lane"],"lane")
        if lane in by_lane or lane>4: raise ValueError(f"{role} lane invalid")
        expected_role="control" if lane==0 else "data"
        if row["lane_role"]!=expected_role or row["connected"] is not connected: raise ValueError(f"{role} lane state invalid")
        if not isinstance(row["tcp_info_captured"],bool) or not isinstance(row["tcp_info_error"],str): raise ValueError(f"{role} lane TCP observation schema")
        integer(row["tcp_state"],f"{role}.tcp_state")
        if require_established and (row["tcp_info_captured"] is not True or row["tcp_info_error"]!="" or row["tcp_state"]!=1): raise ValueError(f"{role} lane is not observed TCP_ESTABLISHED")
        address(row["local_address"],"local_address"); address(row["remote_address"],"remote_address")
        string(row["connection_identity"],"connection_identity",False)
        integer(row["tls_version"],"tls_version",1); integer(row["tls_cipher_suite"],"tls_cipher_suite",1)
        by_lane[lane]=row
    if set(by_lane)!=set(range(5)): raise ValueError(f"{role} lane set invalid")
    return by_lane

ledger_keys={"limit_bytes","initial_reserved_bytes","growth_reserved_bytes","current_bytes","high_water_bytes","denied_reservations","release_count","final_bytes"}
categories={"session_metadata","stream_metadata","relay_buffers","frame_buffers","tls_app_buffers"}
def ledger(value,name,limit,terminal=False,raw=False,working=False):
    if not isinstance(value,dict): raise ValueError(f"{name} schema")
    required=ledger_keys | ({"categories","actual_heap_alloc_bytes","actual_open_fds","actual_socket_fds"} if working else set())
    if not required <= set(value): raise ValueError(f"{name} missing fields")
    for key in ledger_keys: integer(value[key],f"{name}.{key}")
    if value["limit_bytes"]!=limit or value["denied_reservations"]!=0: raise ValueError(f"{name} limit/denial")
    if not 0 <= value["current_bytes"] <= value["high_water_bytes"] <= value["limit_bytes"]: raise ValueError(f"{name} current/high-water accounting")
    if terminal and (value["current_bytes"]!=0 or value["final_bytes"]!=0): raise ValueError(f"{name} terminal ownership")
    if raw and any(value[key]!=0 for key in ("initial_reserved_bytes","growth_reserved_bytes","current_bytes","high_water_bytes","final_bytes","release_count")): raise ValueError(f"{name} raw ownership")
    if working:
        if value["initial_reserved_bytes"]!=0 or value["growth_reserved_bytes"]!=0: raise ValueError(f"{name} pool counters must remain zero")
        if not isinstance(value["categories"],dict) or set(value["categories"])!=categories: raise ValueError("working categories")
        for key,item in value["categories"].items(): integer(item,f"working.{key}")
        if sum(value["categories"].values())!=value["current_bytes"]: raise ValueError(f"{name} category accounting")
        if terminal and any(value["categories"].values()): raise ValueError(f"{name} terminal categories")
        if terminal and value["release_count"]<=0: raise ValueError(f"{name} terminal release count")
        integer(value["actual_heap_alloc_bytes"],f"{name}.actual_heap_alloc_bytes",1)
        integer(value["actual_open_fds"],f"{name}.actual_open_fds",1)
        integer(value["actual_socket_fds"],f"{name}.actual_socket_fds",0 if terminal else 1)
    elif not raw:
        reserved=value["initial_reserved_bytes"]+value["growth_reserved_bytes"]
        if value["high_water_bytes"]!=reserved: raise ValueError(f"{name} reservation high-water accounting")
        if value["initial_reserved_bytes"]<=0: raise ValueError(f"{name} missing initial pool reservation")
        if terminal:
            if value["release_count"]<4: raise ValueError(f"{name} terminal release count")
        elif value["current_bytes"]!=reserved or value["release_count"]!=0:
            raise ValueError(f"{name} live reservation accounting/release")

def base(value,role,completed,terminal=False,require_source=False):
    if not isinstance(value,dict): raise ValueError(f"{role} top schema")
    if integer(value.get("schema_version"),f"{role}.schema_version")!=1 or value.get("process_role")!=role or value.get("variant")!=variant: raise ValueError(f"{role} top identity")
    if require_source and value.get("source_ref")!=source_ref: raise ValueError(f"{role} source_ref")
    if integer(value.get("expected_phases"),f"{role}.expected_phases")!=16 or integer(value.get("completed_phases"),f"{role}.completed_phases")!=completed: raise ValueError(f"{role} phase count")
    if value.get("invalid_reason")!="": raise ValueError(f"{role} invalid_reason")
    configuration(value.get("configuration"),role)
    ledger(value.get("pool_ledger"),f"{role}.pool",2147483648,terminal=terminal,raw=variant=="raw-tls")
    ledger(value.get("working_ledger"),f"{role}.working",67108864,terminal=terminal,working=True)
    if variant=="aggregate-yamux" and completed==16 and not fixture_smoke and value["pool_ledger"]["growth_reserved_bytes"]<=0:
        raise ValueError(f"{role} aggregate pool never grew")

def readiness(value,role):
    base(value,role,0,require_source=True)
    if value.get("ready") is not True or integer(value.get("reconnect_count"),f"{role}.reconnect_count")!=0 or value.get("errors")!=[]:
        raise ValueError(f"{role} not cleanly ready")
    return sockets(value.get("physical_sockets"),role,True,True)

def reverse(server,agent):
    for lane in range(5):
        s,a=server[lane],agent[lane]
        if (s["local_address"]!=a["remote_address"] or s["remote_address"]!=a["local_address"] or
            s["tls_version"]!=a["tls_version"] or s["tls_cipher_suite"]!=a["tls_cipher_suite"]):
            raise ValueError(f"lane {lane} reverse tuple/TLS mismatch")

tcp_keys={"captured","error","local_address","remote_address","state","ca_state","retransmits","probes","backoff","options","rto_us","ato_us","snd_mss","rcv_mss","unacked","sacked","lost","retrans","fackets","last_data_sent_ms","last_ack_sent_ms","last_data_recv_ms","last_ack_recv_ms","pmtu","rcv_ssthresh","rtt_us","rttvar_us","snd_ssthresh","snd_cwnd","advmss","reordering","rcv_rtt_us","rcv_space","total_retrans","pacing_rate","delivery_rate","bytes_acked","bytes_received","bytes_sent","notsent_bytes","snd_wnd","rcv_wnd","app_limited_available"}
def tcp(value,name,socket):
    if not isinstance(value,dict) or set(value)!=tcp_keys or value["captured"] is not True or value["error"]!="": raise ValueError(f"{name} TCP_INFO")
    if value["app_limited_available"] is not False: raise ValueError(f"{name} unavailable app_limited must remain false")
    if value["local_address"]!=socket["local_address"] or value["remote_address"]!=socket["remote_address"]: raise ValueError(f"{name} TCP tuple")
    for key in tcp_keys-{"captured","error","local_address","remote_address","app_limited_available"}: integer(value[key],f"{name}.{key}")

phase_keys={"schema_version","phase_index","phase_name","lane","direction","stage","reuse_kind","configured_payload_bytes","received_payload_bytes","expected_sha256","received_sha256","phase_wall_nanoseconds","setup_nanoseconds","payload_completion_nanoseconds","useful_bytes_per_second","tcp_info"}
def phase(value,index,role,socket):
    if not isinstance(value,dict) or set(value)!=phase_keys: raise ValueError(f"{role} phase {index} schema")
    if integer(value["schema_version"],"schema_version")!=1 or integer(value["phase_index"],"phase_index")!=index: raise ValueError(f"{role} phase {index} identity")
    phase_name,lane,direction,stage,reuse=schedule[index]
    integer(value["lane"],"lane",1)
    if (value["phase_name"],value["lane"],value["direction"],value["stage"],value["reuse_kind"])!=(phase_name,lane,direction,stage,reuse): raise ValueError(f"{role} phase {index} schedule")
    if integer(value["configured_payload_bytes"],"configured_payload_bytes")!=268435456 or integer(value["received_payload_bytes"],"received_payload_bytes")!=268435456: raise ValueError(f"{role} phase {index} bytes")
    if not isinstance(value["expected_sha256"],str) or not isinstance(value["received_sha256"],str) or value["expected_sha256"]!=expected_payload_sha or value["received_sha256"]!=expected_payload_sha: raise ValueError(f"{role} phase {index} predetermined digest")
    wall=integer(value["phase_wall_nanoseconds"],"phase_wall",1)
    setup=integer(value["setup_nanoseconds"],"setup")
    payload=integer(value["payload_completion_nanoseconds"],"payload",1)
    if setup+payload!=wall: raise ValueError(f"{role} phase {index} timing boundary")
    rate=number(value["useful_bytes_per_second"],"rate",True)
    expected=268435456*1e9/wall
    if abs(rate-expected)>max(1e-6*expected,1.0): raise ValueError(f"{role} phase {index} total-wall rate")
    tcp(value["tcp_info"],f"{role} phase {index}",socket[lane])
    return value

initial_server=load(initial_server_path); initial_agent=load(initial_agent_path)
server_initial=readiness(initial_server,"server"); agent_initial=readiness(initial_agent,"agent"); reverse(server_initial,agent_initial)

server_responses=[load(phase_dir/f"phase-{i:02d}.json") for i in range(16)]
live_server=load(live_server_path); live_agent=load(live_agent_path)
for value,role,initial in ((live_server,"server",server_initial),(live_agent,"agent",agent_initial)):
    base(value,role,16,require_source=True)
    if integer(value.get("reconnect_count"),f"{role}.reconnect_count")!=0 or integer(value.get("active_logical_streams"),f"{role}.active_logical_streams")!=0: raise ValueError(f"{role} live counters")
    if value.get("ready") is not True or value.get("graceful_shutdown") is not False or value.get("same_socket_identities") is not True or value.get("joined_logical_relays") is not True or value.get("joined_physical_relays") is not False or value.get("errors")!=[]: raise ValueError(f"{role} live state")
    first=sockets(value.get("physical_sockets_initial"),role,True,True); current=sockets(value.get("physical_sockets_terminal"),role,True,True)
    for lane in range(5):
        if any(first[lane][k]!=initial[lane][k] or current[lane][k]!=initial[lane][k] for k in socket_identity_keys): raise ValueError(f"{role} live lane {lane} identity")
reverse(sockets(live_server["physical_sockets_terminal"],"server live",True,True),sockets(live_agent["physical_sockets_terminal"],"agent live",True,True))
server_live_results=live_server.get("results"); agent_live_results=live_agent.get("results")
if not isinstance(server_live_results,list) or not isinstance(agent_live_results,list) or len(server_live_results)!=16 or len(agent_live_results)!=16: raise ValueError("live results count")

terminal_server=load(terminal_server_path); terminal_agent=load(terminal_agent_path)
for value,role,initial,live_results in ((terminal_server,"server",server_initial,server_live_results),(terminal_agent,"agent",agent_initial,agent_live_results)):
    base(value,role,16,terminal=True,require_source=True)
    if integer(value.get("reconnect_count"),f"{role}.reconnect_count")!=0 or integer(value.get("active_logical_streams"),f"{role}.active_logical_streams")!=0: raise ValueError(f"{role} terminal counters")
    if value.get("ready") is not False or value.get("graceful_shutdown") is not True or value.get("same_socket_identities") is not True or value.get("joined_logical_relays") is not True or value.get("joined_physical_relays") is not True or value.get("errors")!=[]: raise ValueError(f"{role} terminal state")
    first=sockets(value.get("physical_sockets_initial"),role,True,True); final=sockets(value.get("physical_sockets_terminal"),role,False)
    for lane in range(5):
        a,b=initial[lane],final[lane]
        if any(first[lane][k]!=a[k] or a[k]!=b[k] for k in socket_identity_keys): raise ValueError(f"{role} terminal lane {lane} identity")
    if value.get("results")!=live_results: raise ValueError(f"{role} terminal results changed")

joined=[]
for index in range(16):
    sr=phase(server_live_results[index],index,"server",server_initial)
    ar=phase(agent_live_results[index],index,"agent",agent_initial)
    if server_responses[index]!=sr: raise ValueError(f"server phase {index} response changed")
    without_tcp=lambda value:{k:v for k,v in value.items() if k!="tcp_info"}
    if without_tcp(sr)!=without_tcp(ar): raise ValueError(f"phase {index} role mismatch")
    row=without_tcp(sr)
    row.update({"pair":pair,"order_position":int(order_position),"variant":variant,"source_ref":source_ref,
                "server_binary_sha256":server_sha,"agent_binary_sha256":agent_sha,"configured_rtt_ms":configured_rtt,
                "measured_rtt_ms":measured_rtt,"server_tcp_info":sr["tcp_info"],"agent_tcp_info":ar["tcp_info"]})
    joined.append(row)

with Path(results_path).open("a") as out:
    for row in joined: out.write(json.dumps(row,separators=(",",":"),sort_keys=True)+"\n")
with Path(snapshots_path).open("a") as out:
    for stage,server,agent in (("initial",initial_server,initial_agent),("live-post16",live_server,live_agent),("terminal",terminal_server,terminal_agent)):
        out.write(json.dumps({"pair":pair,"variant":variant,"stage":stage,"server":server,"agent":agent},separators=(",",":"),sort_keys=True)+"\n")
