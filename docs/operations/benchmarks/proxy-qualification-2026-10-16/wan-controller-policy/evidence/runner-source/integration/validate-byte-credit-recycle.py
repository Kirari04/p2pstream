#!/usr/bin/env python3
"""Fail-closed validator for selected normal byte-credit recycle traces."""
from __future__ import annotations
from typing import Any

UINT16_MAX=(1<<16)-1; UINT32_MAX=(1<<32)-1; UINT64_MAX=(1<<64)-1
LIMIT=4096
ACCEPTED_WINDOW_UPDATE_FLAGS=0x3 # SYN|ACK; positive FIN/RST and unknown bits are outside the accepted domain.
SNAPSHOT_FIELDS={"schema_version","enabled","complete","reset_performed","ambiguous","overflow","orphan_overflow","active_receiver_publications","active_peer_waits","dropped_receiver_records","dropped_peer_records","dropped_wait_records","receiver_records","peer_records","wait_records"}
RECEIVER_FIELDS={"relative_ordinal","stream_id","flags","kind","delta","cumulative","receive_window_after","eligibility_observed_unix_ns","enqueue_observed_unix_ns","completion_result_observed_unix_ns","eligibility_to_enqueue_observed_ns","enqueue_to_completion_result_observed_ns","eligibility_to_completion_result_observed_ns","enqueue_observed","completion_result_observed","active","ambiguous","outcome"}
PEER_FIELDS={"relative_ordinal","stream_id","flags","delta","cumulative","byte_credit_before","byte_credit_after","handler_entry_unix_ns","credit_applied_unix_ns","notify_observed_unix_ns","handler_to_credit_applied_ns","credit_applied_to_notify_observed_ns","handler_to_notify_observed_ns","notify_result","active_wait_token"}
WAIT_FIELDS={"token","stream_id","decision_credit","entry_ordinal","wake_ordinal","retry_ordinal","entry_cumulative","wake_cumulative","retry_cumulative","entry_byte_credit","wake_byte_credit","retry_byte_credit","entry_observed_unix_ns","wake_observed_unix_ns","retry_observed_unix_ns","entry_to_wake_observed_ns","wake_to_retry_observed_ns","entry_to_retry_observed_ns","outcome","classification","range_first_ordinal","range_last_ordinal","active","ambiguous","terminal","advanced","retry_zero_after_advance"}
SERVER_SESSION_FIELDS={"agent_id","agent_public_id","lane","session_identity","local_address","remote_address","trace"}
AGENT_SESSION_FIELDS={"lane","aggregate","session_identity","local_address","remote_address","trace"}

def uint(v:Any,maximum:int=UINT64_MAX,positive:bool=False)->bool:
 return type(v) is int and (v>0 if positive else v>=0) and v<=maximum

def boolean(v:Any)->bool:return type(v) is bool

def text(v:Any)->bool:return type(v) is str and bool(v) and v==v.strip() and v not in {"yamux:local","yamux:remote"}

def monotonic_triplet(value:dict[str,Any],names:tuple[str,str,str],durations:tuple[str,str,str],path:str,issues:list[str])->None:
 first,middle,last=(value.get(n) for n in names)
 if not all(uint(v,positive=True) for v in (first,middle,last)) or not first<=middle<=last:
  issues.append(f"{path} observed timestamp order is invalid")
 one,two,total=(value.get(n) for n in durations)
 if not all(uint(v) for v in (one,two,total)) or total!=one+two:
  issues.append(f"{path} monotonic duration conservation failed")
 # Deliberately do not derive monotonic durations from Unix timestamp subtraction.

def receiver_record(v:Any,path:str,issues:list[str])->dict[str,Any]:
 if type(v) is not dict or set(v)!=RECEIVER_FIELDS:issues.append(f"{path} fields changed");return {}
 for name,maxv,pos in (("relative_ordinal",UINT64_MAX,True),("stream_id",UINT32_MAX,True),("flags",UINT16_MAX,False),("delta",UINT32_MAX,True),("cumulative",UINT64_MAX,True),("receive_window_after",UINT32_MAX,False)):
  if not uint(v.get(name),maxv,pos):issues.append(f"{path}.{name} invalid")
 if uint(v.get("flags"),UINT16_MAX) and v["flags"]&~ACCEPTED_WINDOW_UPDATE_FLAGS:issues.append(f"{path}.flags includes terminal or unknown bits")
 for name in ("enqueue_observed","completion_result_observed","active","ambiguous"):
  if not boolean(v.get(name)):issues.append(f"{path}.{name} invalid")
 if v.get("kind") not in {"normal_read","forced_growth"}:issues.append(f"{path}.kind invalid")
 if v.get("outcome") not in {"pending","enqueue_shutdown","enqueue_timeout","success","error","completion_shutdown","completion_timeout"}:issues.append(f"{path}.outcome invalid")
 if v.get("outcome")!="success" or v.get("enqueue_observed") is not True or v.get("completion_result_observed") is not True or v.get("active") is not False or v.get("ambiguous") is not False:
  issues.append(f"{path} is not an unambiguous successful publication")
 monotonic_triplet(v,("eligibility_observed_unix_ns","enqueue_observed_unix_ns","completion_result_observed_unix_ns"),("eligibility_to_enqueue_observed_ns","enqueue_to_completion_result_observed_ns","eligibility_to_completion_result_observed_ns"),path,issues)
 return v

def peer_record(v:Any,path:str,issues:list[str])->dict[str,Any]:
 if type(v) is not dict or set(v)!=PEER_FIELDS:issues.append(f"{path} fields changed");return {}
 for name,maxv,pos in (("relative_ordinal",UINT64_MAX,True),("stream_id",UINT32_MAX,True),("flags",UINT16_MAX,False),("delta",UINT32_MAX,True),("cumulative",UINT64_MAX,True),("byte_credit_before",UINT32_MAX,False),("byte_credit_after",UINT32_MAX,True),("active_wait_token",UINT64_MAX,False)):
  if not uint(v.get(name),maxv,pos):issues.append(f"{path}.{name} invalid")
 if uint(v.get("flags"),UINT16_MAX) and v["flags"]&~ACCEPTED_WINDOW_UPDATE_FLAGS:issues.append(f"{path}.flags includes terminal or unknown bits")
 before,delta,after=v.get("byte_credit_before"),v.get("delta"),v.get("byte_credit_after")
 if all(uint(x,UINT32_MAX) for x in (before,delta,after)) and (before+delta>UINT32_MAX or after!=before+delta):issues.append(f"{path} byte-credit application does not conserve")
 if v.get("notify_result") not in {"delivered","coalesced"}:issues.append(f"{path}.notify_result invalid")
 monotonic_triplet(v,("handler_entry_unix_ns","credit_applied_unix_ns","notify_observed_unix_ns"),("handler_to_credit_applied_ns","credit_applied_to_notify_observed_ns","handler_to_notify_observed_ns"),path,issues)
 return v

def wait_record(v:Any,path:str,issues:list[str])->dict[str,Any]:
 if type(v) is not dict or set(v)!=WAIT_FIELDS:issues.append(f"{path} fields changed");return {}
 for name,maxv,pos in (("token",UINT64_MAX,True),("stream_id",UINT32_MAX,True),("decision_credit",UINT32_MAX,False),("entry_ordinal",UINT64_MAX,False),("wake_ordinal",UINT64_MAX,False),("retry_ordinal",UINT64_MAX,False),("entry_cumulative",UINT64_MAX,False),("wake_cumulative",UINT64_MAX,False),("retry_cumulative",UINT64_MAX,False),("entry_byte_credit",UINT32_MAX,False),("wake_byte_credit",UINT32_MAX,False),("retry_byte_credit",UINT32_MAX,False),("range_first_ordinal",UINT64_MAX,False),("range_last_ordinal",UINT64_MAX,False)):
  if not uint(v.get(name),maxv,pos):issues.append(f"{path}.{name} invalid")
 for name in ("active","ambiguous","terminal","advanced","retry_zero_after_advance"):
  if not boolean(v.get(name)):issues.append(f"{path}.{name} invalid")
 if v.get("decision_credit")!=0:issues.append(f"{path} is not a byte-zero decision")
 if v.get("outcome")!="retry" or v.get("classification") not in {"applied_range","pre_entry_buffered","stale"} or v.get("active") is not False or v.get("ambiguous") is not False or v.get("terminal") is not False:
  issues.append(f"{path} is not a complete nonterminal retry")
 eo,wo,ro=v.get("entry_ordinal"),v.get("wake_ordinal"),v.get("retry_ordinal");ec,wc,rc=v.get("entry_cumulative"),v.get("wake_cumulative"),v.get("retry_cumulative")
 if all(uint(x) for x in (eo,wo,ro)) and not eo<=wo<=ro:issues.append(f"{path} ordinal snapshots regress")
 if all(uint(x) for x in (ec,wc,rc)) and not ec<=wc<=rc:issues.append(f"{path} cumulative snapshots regress")
 advanced=uint(eo) and uint(ro) and ro>eo
 if v.get("advanced") is not bool(advanced):issues.append(f"{path}.advanced disagrees with ordinal range")
 first,last=v.get("range_first_ordinal"),v.get("range_last_ordinal")
 if advanced:
  if first!=eo+1 or last!=ro or v.get("classification")!="applied_range":issues.append(f"{path} applied range is inconsistent")
  if v.get("retry_zero_after_advance") is not False or v.get("retry_byte_credit")==0:issues.append(f"{path} advanced credit did not produce usable retry credit")
 else:
  if first!=0 or last!=0 or v.get("classification") not in {"pre_entry_buffered","stale"}:issues.append(f"{path} no-advance classification is inconsistent")
  if v.get("classification")=="pre_entry_buffered" and (not uint(v.get("entry_byte_credit"),UINT32_MAX,True)):issues.append(f"{path} pre-entry-buffered classification lacks entry credit")
  if v.get("classification")=="stale" and v.get("entry_byte_credit")!=0:issues.append(f"{path} stale classification has entry credit")
  if v.get("retry_zero_after_advance") is not False:issues.append(f"{path} no-advance retry-zero marker is not false")
 monotonic_triplet(v,("entry_observed_unix_ns","wake_observed_unix_ns","retry_observed_unix_ns"),("entry_to_wake_observed_ns","wake_to_retry_observed_ns","entry_to_retry_observed_ns"),path,issues)
 return v

def snapshot(v:Any,path:str,issues:list[str])->dict[str,Any]:
 if type(v) is not dict or set(v)!=SNAPSHOT_FIELDS:issues.append(f"{path} fields changed");return {}
 if type(v.get("schema_version")) is not int or v.get("schema_version")!=1 or v.get("enabled") is not True or v.get("complete") is not True or v.get("reset_performed") is not True or v.get("ambiguous") is not False or v.get("overflow") is not False or v.get("orphan_overflow") is not False:
  issues.append(f"{path} health/completion flags are invalid")
 for name in ("active_receiver_publications","active_peer_waits","dropped_receiver_records","dropped_peer_records","dropped_wait_records"):
  if not uint(v.get(name)) or v.get(name)!=0:issues.append(f"{path}.{name} is not zero")
 for name,fn in (("receiver_records",receiver_record),("peer_records",peer_record),("wait_records",wait_record)):
  values=v.get(name)
  if type(values) is not list or len(values)>LIMIT:issues.append(f"{path}.{name} missing or exceeds {LIMIT}");continue
  for i,row in enumerate(values):fn(row,f"{path}.{name}[{i}]",issues)
  if name in {"receiver_records","peer_records"} and type(values) is list:
   grouped={}
   for row in values:
    if type(row) is dict and uint(row.get("stream_id"),UINT32_MAX,True):grouped.setdefault(row["stream_id"],[]).append(row)
   for stream,rows in grouped.items():
    total=0
    for ordinal,row in enumerate(rows,1):
     if uint(row.get("delta"),UINT32_MAX,True):total+=row["delta"]
     if row.get("relative_ordinal")!=ordinal or row.get("cumulative")!=total:issues.append(f"{path}.{name} stream {stream} ordinal/cumulative conservation failed at {ordinal}")
 return v

def sessions(envelope:Any,side:str,sequence:int,action:str,issues:list[str])->list[dict[str,Any]]:
 path=f"{side} {action}"
 if type(envelope) is not dict:issues.append(f"{path} envelope missing");return []
 if envelope.get("sequence")!=sequence or envelope.get("action")!=action or envelope.get("http_status")!=200 or envelope.get("authorization")!="bearer-present" or envelope.get("error")!="":issues.append(f"{path} envelope identity/status invalid")
 response=envelope.get("response")
 if type(response) is not dict:issues.append(f"{path} response missing");return []
 values=(response.get("server_receive_growth",{}).get("byte_credit_traces") if side=="server" else response.get("byte_credit_traces"))
 if type(values) is not list or len(values)!=5:issues.append(f"{path} trace session cardinality is not five");return []
 expected=SERVER_SESSION_FIELDS if side=="server" else AGENT_SESSION_FIELDS
 result=[];lanes=set();identities=set()
 for i,row in enumerate(values):
  p=f"{path}.sessions[{i}]"
  if type(row) is not dict or set(row)!=expected:issues.append(f"{p} fields changed");continue
  lane=row.get("lane");identity=row.get("session_identity")
  if type(lane) is not int or lane not in (0,1,2,3,4) or lane in lanes:issues.append(f"{p}.lane invalid/repeated")
  if not text(identity) or identity in identities:issues.append(f"{p}.session_identity invalid/repeated")
  if not text(row.get("local_address")) or not text(row.get("remote_address")):issues.append(f"{p} physical tuple invalid")
  if side=="server" and (not uint(row.get("agent_id"),positive=True) or not text(row.get("agent_public_id"))):issues.append(f"{p} agent identity invalid")
  if side=="agent" and row.get("aggregate") is not True:issues.append(f"{p}.aggregate is not true")
  snapshot(row.get("trace"),f"{p}.trace",issues);lanes.add(lane);identities.add(identity);result.append(row)
 if lanes!={0,1,2,3,4}:issues.append(f"{path} lane set changed")
 return result

def _one(rows:list[dict[str,Any]],lane:int,local:str,remote:str,path:str,issues:list[str])->dict[str,Any]:
 matches=[r for r in rows if r.get("lane")==lane and r.get("local_address")==local and r.get("remote_address")==remote]
 if len(matches)!=1:issues.append(f"{path} exact lane/tuple selection count is {len(matches)}");return {}
 return matches[0]

def selected_sender_evidence(sender_row:Any,normalized_raw:Any)->dict[str,Any]:
 issues=[]
 if type(sender_row) is not dict or type(normalized_raw) is not dict:
  return {"accepted":False,"generation":None,"byte_zero_wait_count":None,"issues":["selected sender evidence missing"]}
 raw=sender_row.get("raw_write_activity");ints=normalized_raw.get("ints")
 timing=sender_row.get("timing");causes=timing.get("credit_wait_by_cause") if type(timing) is dict else None
 byte_zero=causes.get("byte_zero") if type(causes) is dict else None
 generation=raw.get("generation") if type(raw) is dict else None
 normalized_generation=ints.get("generation") if type(ints) is dict else None
 count=sender_row.get("byte_credit_zero_observations")
 cause_count=byte_zero.get("count") if type(byte_zero) is dict else None
 if not uint(generation,positive=True) or generation!=normalized_generation:issues.append("selected sender raw generation binding failed")
 if not uint(count) or count!=cause_count:issues.append("selected sender byte-zero count binding failed")
 return {"accepted":not issues,"generation":generation,"byte_zero_wait_count":count,"issues":issues}

def validate_pair(server_pre:Any,server_post:Any,agent_pre:Any,agent_post:Any,selected:Any,sequence:int)->dict[str,Any]:
 issues=[]
 spre=sessions(server_pre,"server",sequence,"pre",issues);spos=sessions(server_post,"server",sequence,"post",issues);apre=sessions(agent_pre,"agent",sequence,"pre",issues);apos=sessions(agent_post,"agent",sequence,"post",issues)
 # Pre drains establish a clean boundary even when they return earlier complete records.
 if type(selected) is not dict:issues.append("selected compound identity missing");selected={}
 lane=selected.get("lane");stream=selected.get("stream_id");generation=selected.get("generation");al=selected.get("agent_local_address");ar=selected.get("agent_remote_address");sl=selected.get("server_local_address");sr=selected.get("server_remote_address")
 asi=selected.get("agent_session_identity");ssi=selected.get("server_session_identity");aid=selected.get("agent_id");apid=selected.get("agent_public_id")
 sender_wait_count=selected.get("sender_byte_zero_wait_count")
 if type(lane) is not int or lane not in (1,2,3,4) or not uint(stream,UINT32_MAX,True) or not uint(generation,positive=True):issues.append("selected lane/generation/stream invalid")
 if not all(text(x) for x in (al,ar,sl,sr)) or al!=sr or ar!=sl:issues.append("selected inverse physical tuple invalid")
 if not text(asi) or not text(ssi) or not uint(aid,positive=True) or not text(apid):issues.append("selected session/agent identity invalid")
 if not uint(sender_wait_count):issues.append("selected sender byte-zero wait count invalid")
 server_pre_row=_one(spre,lane,sl,sr,"server pre",issues);agent_pre_row=_one(apre,lane,al,ar,"agent pre",issues)
 server=_one(spos,lane,sl,sr,"server post",issues);agent=_one(apos,lane,al,ar,"agent post",issues)
 if server_pre_row and server and server_pre_row.get("session_identity")!=server.get("session_identity"):issues.append("server pre/post session identity changed")
 if agent_pre_row and agent and agent_pre_row.get("session_identity")!=agent.get("session_identity"):issues.append("agent pre/post session identity changed")
 if server and (server.get("session_identity")!=ssi or server.get("agent_id")!=aid or server.get("agent_public_id")!=apid):issues.append("selected server ownership differs from server trace")
 if agent and agent.get("session_identity")!=asi:issues.append("selected agent ownership differs from agent trace")
 st=server.get("trace",{}) if server else {};at=agent.get("trace",{}) if agent else {}
 receiver=[r for r in st.get("receiver_records",[]) if type(r) is dict and r.get("stream_id")==stream]
 peer=[r for r in at.get("peer_records",[]) if type(r) is dict and r.get("stream_id")==stream]
 waits=[r for r in at.get("wait_records",[]) if type(r) is dict and r.get("stream_id")==stream]
 # A public download sends Yamux DATA from agent to server: the server publishes
 # receive credit, while the agent applies it and owns the byte-zero wait.
 if not receiver or not peer:issues.append("selected receiver/peer record set is empty")
 if uint(sender_wait_count) and len(waits)!=sender_wait_count:issues.append("selected wait-record count differs from independently validated sender byte-zero count")
 def ledger(rows):return [(r.get("relative_ordinal"),r.get("flags"),r.get("delta"),r.get("cumulative")) for r in rows]
 def contiguous(rows,path):
  total=0
  for i,r in enumerate(rows,1):
   total+=r.get("delta",0) if uint(r.get("delta"),UINT32_MAX,True) else 0
   if r.get("relative_ordinal")!=i or r.get("cumulative")!=total:issues.append(f"{path} ordinal/cumulative conservation failed at {i}")
 contiguous(receiver,"receiver");contiguous(peer,"peer")
 if ledger(receiver)!=ledger(peer):issues.append("cross-endpoint ordered byte-credit ledger mismatch")
 maxordinal=len(peer);maxcum=peer[-1].get("cumulative",0) if peer else 0
 def prefix_cumulative(ordinal):
  return 0 if ordinal==0 else (peer[ordinal-1].get("cumulative") if uint(ordinal) and ordinal<=len(peer) else None)
 tokens=set()
 for i,w in enumerate(waits):
  token=w.get("token")
  if token in tokens:issues.append(f"wait token repeated: {token!r}")
  tokens.add(token)
  for name in ("entry_ordinal","wake_ordinal","retry_ordinal"):
   if uint(w.get(name)) and w.get(name)>maxordinal:issues.append(f"wait[{i}].{name} exceeds selected peer ledger")
  for name in ("entry_cumulative","wake_cumulative","retry_cumulative"):
   if uint(w.get(name)) and w.get(name)>maxcum:issues.append(f"wait[{i}].{name} exceeds selected peer cumulative")
  for ordinal_name,cumulative_name in (("entry_ordinal","entry_cumulative"),("wake_ordinal","wake_cumulative"),("retry_ordinal","retry_cumulative")):
   expected_cumulative=prefix_cumulative(w.get(ordinal_name))
   if expected_cumulative is None or w.get(cumulative_name)!=expected_cumulative:issues.append(f"wait[{i}].{cumulative_name} does not equal the exact peer-ledger prefix")
 for i,p in enumerate(peer):
  token=p.get("active_wait_token")
  if not uint(token,positive=True):continue
  matches=[w for w in waits if w.get("token")==token and w.get("stream_id")==stream]
  if len(matches)!=1:issues.append(f"peer[{i}] active wait token has {len(matches)} matching selected waits");continue
  matched=matches[0]
  if not (uint(matched.get("entry_observed_unix_ns"),positive=True) and uint(p.get("credit_applied_unix_ns"),positive=True) and uint(matched.get("retry_observed_unix_ns"),positive=True) and matched["entry_observed_unix_ns"]<=p["credit_applied_unix_ns"]<=matched["retry_observed_unix_ns"]):
   issues.append(f"peer[{i}] active wait token application is outside its local wait interval")
 classifications=[]
 for r in receiver:classifications.append({"relative_ordinal":r.get("relative_ordinal"),"kind":r.get("kind"),"delta":r.get("delta"),"eligibility_to_enqueue_observed_ns":r.get("eligibility_to_enqueue_observed_ns"),"enqueue_to_completion_result_observed_ns":r.get("enqueue_to_completion_result_observed_ns")})
 return {"schema_version":1,"accepted":not issues,"sequence":sequence,"selected_lane":lane,"selected_generation":generation,"selected_stream_id":stream,"receiver_records":len(receiver),"peer_records":len(peer),"wait_records":len(waits),"joined_receiver_classification":classifications,"issues":issues,"limitations":["Unix timestamps validate only local ordering/containment; monotonic time.Time.Sub-derived duration fields supply segment conservation.","No timestamp arithmetic is performed across endpoints.","Receiver kind normal_read means the non-forced publication path; SYN/ACK flags separately identify initial/open positive updates, so kind alone does not claim consumed-byte recycling.","Positive initial/open updates use the same per-stream relative ordinal and cumulative convention at both endpoints and are included in the exact ordered join.","The interval before receiver eligibility and cross-process frame transit remain unmeasured."]}
