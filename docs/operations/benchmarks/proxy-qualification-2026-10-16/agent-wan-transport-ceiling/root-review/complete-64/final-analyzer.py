import json,re,statistics,sys
from pathlib import Path
source,snap_source,topology_source,destination,runner_sha,server_sha,agent_sha,source_ref,expected_payload_sha,revalidation_ok,fixture_smoke,configured_rtt,one_way_netem,pair_count=sys.argv[1:]
fixture_smoke=fixture_smoke=="1"; configured_rtt=int(configured_rtt); one_way_netem=int(one_way_netem); pair_count=int(pair_count)
rows=[json.loads(x) for x in Path(source).read_text().splitlines() if x.strip()]
snapshots=[json.loads(x) for x in Path(snap_source).read_text().splitlines() if x.strip()]
topologies=[json.loads(x) for x in Path(topology_source).read_text().splitlines() if x.strip()]
all_order={1:["aggregate-yamux","raw-tls"],2:["raw-tls","aggregate-yamux"]}
expected_order={pair:all_order[pair] for pair in range(1,pair_count+1)}
pairs=tuple(range(1,pair_count+1))
expected_rows=pair_count*32; expected_snapshots=pair_count*6; expected_topologies=pair_count*2; expected_namespaces=pair_count*6
minimum_rtt,maximum_rtt=((5,25) if fixture_smoke else (60,120))
non_timing=[]
timing_failures=[]
if pair_count!=(1 if fixture_smoke else 2) or configured_rtt!=(10 if fixture_smoke else 80) or one_way_netem!=(5 if fixture_smoke else 40):
    non_timing.append("mode-specific pair count or RTT configuration is invalid")
if revalidation_ok!="1": non_timing.append("raw evidence failed pure-validator replay or byte-identity comparison")
if len(rows)!=expected_rows: non_timing.append(f"expected {expected_rows} rows, found {len(rows)}")
if len(snapshots)!=expected_snapshots: non_timing.append(f"expected {expected_snapshots} snapshots, found {len(snapshots)}")
if len(topologies)!=expected_topologies: non_timing.append(f"expected {expected_topologies} topology records, found {len(topologies)}")
row_shape_ok=all(isinstance(r,dict) and all(k in r for k in ("pair","order_position","variant","phase_index","measured_rtt_ms","useful_bytes_per_second","phase_name","lane","direction","stage","reuse_kind")) for r in rows)
snapshot_shape_ok=all(isinstance(s,dict) and all(k in s for k in ("pair","variant","stage","server","agent")) for s in snapshots)
if not row_shape_ok: non_timing.append("one or more result rows are not objects with the required final-analysis fields")
if not snapshot_shape_ok: non_timing.append("one or more snapshots are not objects with the required final-analysis fields")
expected_sequence=[]
for pair in pairs:
    for position,variant in enumerate(expected_order[pair],1):
        expected_sequence.extend((pair,position,variant,phase) for phase in range(16))
actual_sequence=[(r.get("pair"),r.get("order_position"),r.get("variant"),r.get("phase_index")) if isinstance(r,dict) else None for r in rows]
if actual_sequence!=expected_sequence: non_timing.append(f"{expected_rows}-row chronological pair/arm/phase sequence mismatch")
expected_snapshot_sequence=[]
for pair in pairs:
    for variant in expected_order[pair]:
        expected_snapshot_sequence.extend((pair,variant,stage) for stage in ("initial","live-post16","terminal"))
actual_snapshot_sequence=[(s.get("pair"),s.get("variant"),s.get("stage")) if isinstance(s,dict) else None for s in snapshots]
if actual_snapshot_sequence!=expected_snapshot_sequence: non_timing.append(f"{expected_snapshots}-snapshot chronological pair/arm/stage sequence mismatch")
expected_topology_sequence=[(pair,position,variant) for pair in pairs for position,variant in enumerate(expected_order[pair],1)]
actual_topology_sequence=[(t.get("pair"),t.get("order_position"),t.get("variant")) if isinstance(t,dict) else None for t in topologies]
if actual_topology_sequence!=expected_topology_sequence: non_timing.append(f"{expected_topologies}-topology chronological pair/arm sequence mismatch")
namespace_ids=[]
helper_process_instances=[]
boot_ids_seen=[]
cpu_sets_seen=[]
topology_rtts={}
boot_id_pattern=re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\Z")
instance_keys={"boot_id","helper_pid","helper_start_ticks","netns_inode","arm_generation"}
for generation,topology in enumerate(topologies,1):
    if (not isinstance(topology,dict) or isinstance(topology.get("schema_version"),bool) or topology.get("schema_version")!=1 or
        isinstance(topology.get("pair"),bool) or not isinstance(topology.get("pair"),int) or
        isinstance(topology.get("order_position"),bool) or not isinstance(topology.get("order_position"),int) or
        not isinstance(topology.get("variant"),str) or not isinstance(topology.get("namespace_identities"),dict)):
        non_timing.append("malformed topology record")
        continue
    identities=topology["namespace_identities"]
    instances=topology.get("namespace_instances")
    runner_identity=topology.get("runner_namespace_identity")
    if (set(identities)!={"server","router","agent"} or
        not all(isinstance(v,str) and re.fullmatch(r"net:\[[0-9]+\]",v) for v in identities.values()) or
        not isinstance(runner_identity,str) or not re.fullmatch(r"net:\[[0-9]+\]",runner_identity) or
        runner_identity in identities.values()):
        non_timing.append("invalid topology namespace identities")
        continue
    if not isinstance(instances,dict) or set(instances)!={"server","router","agent"}:
        non_timing.append("invalid topology namespace helper instances")
        continue
    arm_inodes=[]; arm_processes=[]
    for role in ("server","router","agent"):
        instance=instances.get(role)
        if not isinstance(instance,dict) or set(instance)!=instance_keys:
            non_timing.append(f"invalid {role} namespace helper instance schema")
            continue
        instance_boot=instance.get("boot_id")
        instance_pid=instance.get("helper_pid")
        instance_start=instance.get("helper_start_ticks")
        instance_inode=instance.get("netns_inode")
        instance_generation=instance.get("arm_generation")
        if (not isinstance(instance_boot,str) or not boot_id_pattern.fullmatch(instance_boot) or
            isinstance(instance_pid,bool) or not isinstance(instance_pid,int) or instance_pid<=1 or
            any(isinstance(item,bool) or not isinstance(item,int) or item<=0 for item in (instance_start,instance_inode,instance_generation))):
            non_timing.append(f"invalid {role} namespace helper instance values")
            continue
        if instance_generation!=generation or identities[role]!=f"net:[{instance_inode}]":
            non_timing.append(f"{role} namespace helper binding/generation mismatch")
        arm_inodes.append(instance_inode)
        arm_processes.append((instance_boot,instance_pid,instance_start))
        namespace_ids.append(identities[role])
        helper_process_instances.append((instance_boot,instance_pid,instance_start))
        boot_ids_seen.append(instance_boot)
    if len(arm_inodes)!=3 or len(set(arm_inodes))!=3 or len(arm_processes)!=3 or len(set(arm_processes))!=3:
        non_timing.append("within-arm namespace/helper instances are aliased")
    prior=topology.get("prior_joined_generation")
    joined=topology.get("joined_generation")
    if isinstance(prior,bool) or not isinstance(prior,int) or prior!=generation-1 or topology.get("namespace_helpers_joined") is not True or isinstance(joined,bool) or not isinstance(joined,int) or joined!=generation:
        non_timing.append("topology helper teardown chronology is invalid")
    configured=topology.get("configured")
    if not isinstance(configured,dict) or configured!={"rtt_ms":configured_rtt,"one_way_netem_ms":one_way_netem,"rate_bits_per_second":1000000000,"server_tunnel":"10.211.0.1:17443","server_admin":"127.0.0.1:18080","agent_admin":"127.0.0.1:18080"}:
        non_timing.append("topology configuration differs from the predeclared fixture")
    if not isinstance(topology.get("network_state"),dict) or set(topology["network_state"])!={"server","router","agent"}:
        non_timing.append("topology network-state evidence missing")
    cpu_sets=topology.get("cpu_sets")
    if not isinstance(cpu_sets,dict) or set(cpu_sets)!={"server","agent","load"} or not all(isinstance(v,str) and v for v in cpu_sets.values()):
        non_timing.append("topology CPU-set evidence missing")
    else:
        cpu_sets_seen.append(cpu_sets)
    topology_rtt=topology.get("measured_rtt_ms")
    if isinstance(topology_rtt,bool) or not isinstance(topology_rtt,(int,float)) or not minimum_rtt <= topology_rtt <= maximum_rtt:
        non_timing.append("topology measured RTT is outside the diagnostic envelope")
    else:
        topology_rtts[(topology.get("pair"),topology.get("variant"))]=topology_rtt
if len(helper_process_instances)!=expected_namespaces or len(set(helper_process_instances))!=expected_namespaces: non_timing.append(f"fresh server/router/agent helper process instances were not unique for all {expected_topologies} arms")
if len(boot_ids_seen)!=expected_namespaces or len(set(boot_ids_seen))!=1: non_timing.append("topology helper instances do not share one captured kernel boot identity")
if len(cpu_sets_seen)!=expected_topologies or any(value!=cpu_sets_seen[0] for value in cpu_sets_seen[1:]): non_timing.append("endpoint/load CPU sets changed between arms")
by_pair={}
rtt_by_pair_arm={}
for pair in pairs:
    by_pair[pair]={}
    rtt_by_pair_arm[str(pair)]={}
    if revalidation_ok!="1" or not row_shape_ok or actual_sequence!=expected_sequence:
        continue
    pair_rows=[r for r in rows if r.get("pair")==pair]
    observed=[]
    for variant in expected_order[pair]:
        selected=sorted((r for r in pair_rows if r.get("variant")==variant),key=lambda r:r["phase_index"])
        if len(selected)!=16 or [r["phase_index"] for r in selected]!=list(range(16)):
            non_timing.append(f"pair {pair} {variant} phase order invalid")
            continue
        expected_position=expected_order[pair].index(variant)+1
        if any(r.get("order_position")!=expected_position for r in selected):
            non_timing.append(f"pair {pair} {variant} arm order invalid")
            continue
        observed.append(variant)
        by_pair[pair][variant]=selected
        rtts={r.get("measured_rtt_ms") for r in selected}
        rtt=next(iter(rtts)) if len(rtts)==1 else None
        if isinstance(rtt,bool) or not isinstance(rtt,(int,float)) or not minimum_rtt <= rtt <= maximum_rtt:
            non_timing.append(f"pair {pair} {variant} measured RTT invalid or inconsistent")
        else:
            rtt_by_pair_arm[str(pair)][variant]=rtt
            if topology_rtts.get((pair,variant))!=rtt: non_timing.append(f"pair {pair} {variant} row/topology RTT mismatch")
    if observed!=expected_order[pair]: non_timing.append(f"pair {pair} variant order invalid")

pair_results={}
for pair in pairs:
    if set(by_pair[pair])!={"aggregate-yamux","raw-tls"}: continue
    yamux=by_pair[pair]["aggregate-yamux"]; raw=by_pair[pair]["raw-tls"]
    y_warm=statistics.median(yamux[i]["useful_bytes_per_second"] for i in range(4))
    y_transition={str(i):yamux[i]["useful_bytes_per_second"] for i in (8,10,13)}
    y_reused=yamux[14]["useful_bytes_per_second"]
    yamux_gate={"fresh_download_median":y_warm,"fresh_download_minimum":29700000,
                "fresh_download_passed":y_warm>=29700000,
                "transition_samples":y_transition,"at_least_one_transition_below_floor":any(v<29700000 for v in y_transition.values()),
                "reused_lane3_download":y_reused,"reused_lane3_minimum":29700000,"reused_lane3_passed":y_reused>=29700000}
    yamux_gate["passed"]=yamux_gate["fresh_download_passed"] and yamux_gate["at_least_one_transition_below_floor"] and yamux_gate["reused_lane3_passed"]
    r_warm=statistics.median(raw[i]["useful_bytes_per_second"] for i in range(4))
    raw_ratios={str(i):raw[i]["useful_bytes_per_second"]/r_warm for i in (8,10,13,14)}
    raw_gate={"fresh_download_median":r_warm,"postwarm_to_fresh_median_ratios":raw_ratios,"minimum":.90,"passed":all(v>=.90 for v in raw_ratios.values())}
    phase_table={}
    for variant,selected in (("aggregate-yamux",yamux),("raw-tls",raw)):
        phase_table[variant]=[{"phase_index":r["phase_index"],"phase_name":r["phase_name"],"lane":r["lane"],
                              "direction":r["direction"],"stage":r["stage"],"reuse_kind":r["reuse_kind"],
                              "useful_bytes_per_second":r["useful_bytes_per_second"]} for r in selected]
    pair_results[str(pair)]={"order":expected_order[pair],"all_phase_rates":phase_table}
    if fixture_smoke:
        pair_results[str(pair)]["timing_localization_gates_evaluated"]=False
    else:
        pair_results[str(pair)].update({"yamux_stall_signature":yamux_gate,"raw_no_analogous_stall":raw_gate})
        if not yamux_gate["passed"]: timing_failures.append(f"pair {pair} did not reproduce the Yamux transition signature")
        if not raw_gate["passed"]: timing_failures.append(f"pair {pair} raw TLS had a postwarm download below 90% of its own warm median")

growth_by_pair={}
for pair in pairs:
    selected=[s for s in snapshots if isinstance(s,dict) and s.get("pair")==pair and s.get("variant")=="aggregate-yamux" and s.get("stage")=="live-post16"]
    if len(selected)==1:
        growth_by_pair[str(pair)]={role:selected[0].get(role,{}).get("pool_ledger",{}).get("growth_reserved_bytes") for role in ("server","agent")}
growth_exercised=bool(growth_by_pair) and all(isinstance(value,int) and not isinstance(value,bool) and value>0 for values in growth_by_pair.values() for value in values.values())
failures=non_timing+timing_failures
diagnostic_reproduced=not failures and not fixture_smoke
smoke_passed=fixture_smoke and not non_timing
classification=("fixture_smoke_pass" if smoke_passed else "inconclusive_non_timing_failure") if fixture_smoke else ("transport_ceiling_contrast_reproduced" if diagnostic_reproduced else ("complete_timing_rejection" if not non_timing else "inconclusive_non_timing_failure"))
summary={"scope":"fixture_smoke_only" if fixture_smoke else "diagnostic_only","classification":classification,
         "diagnostic_reproduced":diagnostic_reproduced,"fixture_smoke_passed":smoke_passed,"row_count":len(rows),"snapshot_count":len(snapshots),
         "topology_count":len(topologies),"fresh_namespace_instance_count":len(set(helper_process_instances)),"unique_netns_inode_count":len(set(namespace_ids)),
         "fixed_workload":{"pairs":pair_count,"order":["yamux-then-raw"] if fixture_smoke else ["yamux-then-raw","raw-then-yamux"],"transfers_per_variant":16,"total_transfers":expected_rows,"payload_bytes":268435456,"expected_payload_sha256":expected_payload_sha,"configured_rtt_ms":configured_rtt,"configured_one_way_netem_ms":one_way_netem,"measured_rtt_envelope_ms":{"minimum":minimum_rtt,"maximum":maximum_rtt},"measured_rtt_ms_by_pair_arm":rtt_by_pair_arm,
                           "total_phase_wall_used_for_both_arms":True,"tcp_info_collection":"post-transfer-only",
                           "tcp_info_unavailable_fields":["app_limited"],
                           "raw_lifecycle_difference":"persistent TLS and origin TCP relay per data lane; no Yamux Open/OpenRequest setup"},
         "gates":{"timing_localization_evaluated":not fixture_smoke,"yamux_fresh_download_median_minimum":None if fixture_smoke else 29700000,"yamux_transition_requires_at_least_one_subthreshold":None if fixture_smoke else True,"yamux_reused_lane3_minimum":None if fixture_smoke else 29700000,"raw_each_postwarm_to_own_warm_median_minimum":None if fixture_smoke else .90,"not_a_1_10_retention_or_legacy_acceptance_gate":True},
         "aggregate_growth":{"exercised":growth_exercised,"status":"exercised" if growth_exercised else ("untested_smoke_only" if fixture_smoke else "missing"),"live_growth_reserved_bytes_by_pair_role":growth_by_pair},
         "pairs":pair_results,"failures":failures,"non_timing_failures":non_timing,"timing_failures":timing_failures,"pure_validator_replay":revalidation_ok=="1",
         "provenance":{"runner_sha256":runner_sha,"server_binary_sha256":server_sha,"agent_binary_sha256":agent_sha,"source_ref":source_ref,"build_tag":"p2pstream_transport_ceiling"}}
Path(destination).write_text(json.dumps(summary,indent=2,sort_keys=True)+"\n")
raise SystemExit(0 if (smoke_passed if fixture_smoke else diagnostic_reproduced) else (2 if non_timing else 1))
