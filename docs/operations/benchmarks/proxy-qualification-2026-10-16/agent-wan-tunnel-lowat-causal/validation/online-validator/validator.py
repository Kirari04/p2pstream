import ipaddress
import json
import sys

label, stage = sys.argv[1:3]
server, agent = map(json.loads, sys.argv[3:5])
candidate = label == "tunnel-lowat-32k"
if label not in {"tunnel-lowat-default-control", "tunnel-lowat-32k"}:
    raise SystemExit(f"unknown LOWAT arm {label!r}")

def endpoint(value):
    if not isinstance(value, str) or not value:
        raise ValueError(value)
    if value.startswith("["):
        host, sep, port = value[1:].partition("]:")
        if not sep:
            raise ValueError(value)
    else:
        host, port = value.rsplit(":", 1)
    parsed_port = int(port)
    if parsed_port < 1 or parsed_port > 65535:
        raise ValueError(value)
    return str(ipaddress.ip_address(host)), parsed_port

error_fields = (
    "namespace_notsent_lowat_error", "notsent_lowat_before_error",
    "notsent_lowat_after_error", "send_buffer_before_error",
    "send_buffer_after_error", "receive_buffer_before_error",
    "receive_buffer_after_error", "setter_error", "unwrap_error",
    "syscall_conn_error", "control_error",
)
lowat_fields = (
    "requested_notsent_lowat", "namespace_notsent_lowat",
    "notsent_lowat_before", "notsent_lowat_after",
)
buffer_fields = (
    "send_buffer_before", "send_buffer_after",
    "receive_buffer_before", "receive_buffer_after",
)
boolean_fields = (
    "platform_supported", "apply_requested", "setter_attempted", "conclusive",
)

def exact_int(mapping, name, minimum, maximum):
    if name not in mapping or type(mapping[name]) is not int or not minimum <= mapping[name] <= maximum:
        raise SystemExit(f"{stage} socket field {name} is missing or invalid")
    return mapping[name]

def exact_bool(mapping, name):
    if name not in mapping or type(mapping[name]) is not bool:
        raise SystemExit(f"{stage} socket field {name} is missing or invalid")
    return mapping[name]

def empty_string(mapping, name):
    if name not in mapping or not isinstance(mapping[name], str) or mapping[name] != "":
        raise SystemExit(f"{stage} socket field {name} is missing, mistyped, or nonempty")


def validate_common(response, role):
    if not isinstance(response, dict):
        raise SystemExit(f"{stage} {role} snapshot is not an object")
    if exact_int(response, "schema_version", 1, 1) != 1 or response.get("process_role") != role:
        raise SystemExit(f"{stage} {role} snapshot schema/role mismatch")
    if "process_role" not in response or not isinstance(response["process_role"], str):
        raise SystemExit(f"{stage} {role} snapshot process role is missing or mistyped")
    exact_int(response, "captured_unix_nano", 1, (1 << 63) - 1)
    if exact_int(response, "capacity", 0, (1 << 31) - 1) != 64 or exact_int(response, "dropped_observations", 0, (1 << 64) - 1) != 0:
        raise SystemExit(f"{stage} {role} snapshot capacity/drop mismatch")
    if "observations" not in response:
        raise SystemExit(f"{stage} {role} snapshot lacks observations")
    rows = response["observations"]
    if not isinstance(rows, list) or len(rows) != 5:
        raise SystemExit(f"{stage} {role} has {len(rows) if isinstance(rows, list) else 'invalid'} observations, want exactly 5")
    for expected_index, row in enumerate(rows):
        if not isinstance(row, dict) or exact_int(row, "index", 0, 63) != expected_index:
            raise SystemExit(f"{stage} {role} observation indexes are not exactly 0..4")
        if "socket" not in row or not isinstance(row["socket"], dict):
            raise SystemExit(f"{stage} {role} observation socket is missing or mistyped")
        sock = row["socket"]
        for name in boolean_fields:
            exact_bool(sock, name)
        if not sock["conclusive"] or not sock["platform_supported"]:
            raise SystemExit(f"{stage} {role} contains a nonconclusive socket observation")
        for name in (*error_fields, "inconclusive_reason"):
            empty_string(sock, name)
        for name in lowat_fields:
            exact_int(sock, name, 0, (1 << 32) - 1)
        if sock["requested_notsent_lowat"] != 32768:
            raise SystemExit(f"{stage} {role} requested LOWAT is not exactly 32768")
        for name in buffer_fields:
            exact_int(sock, name, 1, (1 << 63) - 1)
        if "local_address" not in sock or "remote_address" not in sock:
            raise SystemExit(f"{stage} {role} contains a missing socket tuple")
        endpoint(sock["local_address"]); endpoint(sock["remote_address"])
        if candidate:
            if not sock["apply_requested"] or not sock["setter_attempted"]:
                raise SystemExit(f"{stage} {role} candidate did not request/attempt LOWAT")
            if sock["requested_notsent_lowat"] != 32768 or sock["notsent_lowat_after"] != 32768:
                raise SystemExit(f"{stage} {role} candidate LOWAT is not exactly 32768")
        else:
            if sock["apply_requested"] or sock["setter_attempted"]:
                raise SystemExit(f"{stage} {role} control attempted LOWAT mutation")
            if sock["notsent_lowat_after"] != sock["notsent_lowat_before"]:
                raise SystemExit(f"{stage} {role} control LOWAT changed")
    return rows

server_rows = validate_common(server, "server")
agent_rows = validate_common(agent, "agent")
for row in server_rows:
    exact_int(row, "lane", 0, 4)
    if "lane_role" not in row or not isinstance(row["lane_role"], str):
        raise SystemExit(f"{stage} server lane role is missing or mistyped")
    exact_bool(row, "aggregate")
if sorted(row["lane"] for row in server_rows) != list(range(5)):
    raise SystemExit(f"{stage} server lanes are not exactly 0..4")
for row in server_rows:
    lane = row["lane"]
    expected_role = "control" if lane == 0 else "bulk"
    if row.get("lane_role") != expected_role or row.get("aggregate") is not True:
        raise SystemExit(f"{stage} server lane {lane} role/mode mismatch")

agent_by_tuple = {}
for row in agent_rows:
    sock = row["socket"]
    key = (endpoint(sock["local_address"]), endpoint(sock["remote_address"]))
    if key in agent_by_tuple:
        raise SystemExit(f"{stage} duplicate agent socket tuple")
    agent_by_tuple[key] = row
matched = []
for row in sorted(server_rows, key=lambda item: item["lane"]):
    sock = row["socket"]
    reverse = (endpoint(sock["remote_address"]), endpoint(sock["local_address"]))
    peer = agent_by_tuple.pop(reverse, None)
    if peer is None:
        raise SystemExit(f"{stage} server lane {row['lane']} lacks a reverse agent tuple")
    matched.append({"lane": row["lane"], "server_index": row["index"], "agent_index": peer["index"]})
if agent_by_tuple:
    raise SystemExit(f"{stage} contains unmatched agent socket tuples")
print(json.dumps(matched, separators=(",", ":")))