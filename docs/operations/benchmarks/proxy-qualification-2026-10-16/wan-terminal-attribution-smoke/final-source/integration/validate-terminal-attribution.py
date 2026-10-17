#!/usr/bin/env python3
"""Prospective, separate terminal attribution. Never rewrites legacy evidence."""
import argparse
import hashlib
import importlib.util
import json
import os
import stat
from pathlib import Path

U64 = (1 << 64) - 1
STATE_FIELDS = {"sequence", "problems", "stop", "stop_generation", "close_begin", "close_end", "joined", "raw_errors", "raw_shorts", "active_plain", "active_raw", "legacy_close_overlap", "records", "tcp_bound", "physical_close_calls", "physical_close_error", "underlying_escaped"}
RAW_FIELDS = {"begin", "return", "after", "generation", "attempt", "written", "error", "net_closed", "short", "plain", "stack"}
PLAIN_FIELDS = {"begin", "end", "attempt", "control", "headers"}
OWNER_FIELDS = {"lane", "generation", "session", "local", "remote", "state"}


def uint(v, positive=False, maximum=U64):
    return type(v) is int and (1 if positive else 0) <= v <= maximum


def require(ok, message):
    if not ok:
        raise ValueError(message)


def controls(headers):
    if type(headers) is not list or not 1 <= len(headers) <= 17:
        return False
    for h in headers:
        if type(h) is not dict or set(h) != {"type", "flags", "stream", "value"}:
            return False
        if not all(uint(h[k], maximum=m) for k, m in (("type", 255), ("flags", 65535), ("stream", (1 << 32)-1), ("value", (1 << 32)-1))):
            return False
        typ, flags, stream, value = (h[k] for k in ("type", "flags", "stream", "value"))
        if typ == 1:
            valid = flags in (0, 1, 2, 4, 5, 6, 8) and (stream != 0 or flags == 0 and value > 0 and value % 4096 == 0) and (flags != 8 or value == 0)
        elif typ == 2:
            valid = stream == 0 and flags in (1, 2)
        elif typ == 3:
            valid = flags == stream == value == 0
        elif typ == 4:
            valid = flags == 0 and stream > 0 and value > 0 and value % 4096 == 0
        elif typ == 5:
            valid = flags == 0 and value > 0 and value % 4096 == 0
        else:
            valid = False
        if not valid:
            return False
    return True



STACK_FIELDS = {"go_version", "complete", "functions"}


def application_stack(stack):
    if type(stack) is not dict or set(stack) != STACK_FIELDS or stack["go_version"] != "go1.26.6" or stack["complete"] is not True:
        return False
    names = stack["functions"]
    if type(names) is not list or len(names) > 32 or not all(type(x) is str and 0 < len(x) <= 192 for x in names):
        return False
    names = list(names)
    def consume(name):
        if not names or names[0] != name:
            return False
        names.pop(0); return True
    def optional(name):
        if names and names[0] == name:
            names.pop(0)
    if not all(consume(x) for x in (
        "p2pstream/internal/agent.(*qualificationRawWriteOwner).terminalFinishRawLocked",
        "p2pstream/internal/agent.(*qualificationRawWriteOwner).finishRawWriteWithTCP",
        "p2pstream/internal/agent.(*qualificationObservedRawConn).Write")):
        return False
    optional("p2pstream/internal/agent.(*qualificationObservedSyscallRawConn).Write")
    if not all(consume(x) for x in ("crypto/tls.(*Conn).write", "crypto/tls.(*Conn).writeRecordLocked", "crypto/tls.(*Conn).Write")):
        return False
    optional("net/http.(*readWriteCloserBody).Write")
    if not all(consume(x) for x in ("p2pstream/internal/agent.(*qualificationObservedTunnelBody).Write",
                                   "github.com/hashicorp/yamux.(*Session).sendLoop", "github.com/hashicorp/yamux.(*Session).send")):
        return False
    optional("github.com/hashicorp/yamux.newSession.gowrap2")
    return consume("runtime.goexit") and not names


def exclusive_close(state):
    return (state["tcp_bound"] is True and type(state["physical_close_calls"]) is int and state["physical_close_calls"] == 1
            and state["physical_close_error"] is False and state["underlying_escaped"] is False)


def record_shape(v):
    require(type(v) is dict and set(v) == RAW_FIELDS, "raw attribution schema differs")
    for k in RAW_FIELDS - {"error", "net_closed", "short", "plain", "stack"}:
        require(uint(v[k]), "raw attribution integer invalid")
    for k in ("error", "net_closed", "short"):
        require(type(v[k]) is bool, "raw attribution boolean invalid")
    stack = v["stack"]
    require(type(stack) is dict and set(stack) == STACK_FIELDS and type(stack["go_version"]) is str and len(stack["go_version"]) <= 64 and type(stack["complete"]) is bool, "stack metadata schema invalid")
    require(stack["functions"] is None or type(stack["functions"]) is list and len(stack["functions"]) <= 32 and all(type(x) is str and 0 < len(x) <= 192 for x in stack["functions"]), "stack metadata exceeds bound")
    p = v["plain"]
    require(type(p) is dict and set(p) == PLAIN_FIELDS, "plaintext schema differs")
    require(all(uint(p[k]) for k in ("begin", "end", "attempt")) and type(p["control"]) is bool, "plaintext fields invalid")
    require(p["headers"] is None or type(p["headers"]) is list and len(p["headers"]) <= 17, "plaintext metadata exceeds bound")
    if p["control"]:
        require(application_stack(stack), "plaintext control lacks exact live TLS application ancestry")
        require(controls(p["headers"]) and p["attempt"] == 12*len(p["headers"]), "control identity does not parse exactly")
    else:
        require(p["headers"] is None, "unknown plaintext carries claimed control headers")
    require(v["written"] <= v["attempt"], "raw bytes exceed attempt")
    require(0 < v["begin"] < v["return"] < v["after"], "raw return/capture ordering invalid")


def control_overlap(v, s, generation):
    p = v["plain"]
    return (exclusive_close(s) and application_stack(v["stack"]) and p["control"] and controls(p["headers"]) and p["attempt"] == 12*len(p["headers"])
            and v["generation"] == generation
            and 0 < p["begin"] < v["begin"] < s["stop"] < s["close_begin"] < v["after"] < p["end"] < s["joined"])


def classify(attribution, raw, legacy):
    """legacy must be recomputed by the hash-pinned original validator in CLI."""
    issues, accepted_exceptions, classes = [], set(), []
    try:
        require(type(attribution) is dict and set(attribution) == {"schema_version", "expected_owners", "intent_complete", "invalid", "raw_sha256", "owners"}, "attribution top schema differs")
        require(type(attribution["schema_version"]) is int and attribution["schema_version"] == 1, "attribution version differs")
        require(type(attribution["expected_owners"]) is int and attribution["expected_owners"] == 5 and attribution["intent_complete"] is True and attribution["invalid"] is False, "five-owner intent coverage is absent or invalid")
        owners = attribution["owners"]
        require(type(owners) is list and len(owners) == 5 and [o.get("lane") for o in owners if type(o) is dict] == list(range(5)), "owner ordering/cardinality differs")
        require(type(raw) is dict and type(raw.get("records")) is list and len(raw["records"]) == 5, "legacy raw owner cardinality differs")
        require(len({o.get("session") for o in owners}) == 5 and len({o.get("generation") for o in owners}) == 5, "repeated owner session or generation")
        require(type(legacy) is dict and type(legacy.get("accepted")) is bool and type(legacy.get("issues")) is list and all(type(x) is str for x in legacy["issues"]), "legacy verdict malformed")
        require(legacy["accepted"] == (not legacy["issues"]), "legacy accepted/issues disagree")
        any_overlap = False
        for i, (owner, old) in enumerate(zip(owners, raw["records"])):
            require(type(owner) is dict and set(owner) == OWNER_FIELDS, "owner schema differs")
            require(type(owner["lane"]) is int and owner["lane"] == i and uint(owner["generation"], True), "owner lane/generation invalid")
            require(all(type(owner[k]) is str and owner[k] and owner[k] == owner[k].strip() for k in ("session", "local", "remote")), "owner identity invalid")
            activity = old["raw_write_activity"]
            require(old["lane"] == i and old["session_identity"] == owner["session"] and activity["generation"] == owner["generation"] and activity["local_address"] == owner["local"] and activity["remote_address"] == owner["remote"], "owner does not bind the unchanged raw sidecar")
            s = owner["state"]
            require(type(s) is dict and set(s) == STATE_FIELDS, "state schema differs")
            require(all(uint(s[k]) for k in STATE_FIELDS - {"active_plain", "active_raw", "legacy_close_overlap", "records", "tcp_bound", "physical_close_error", "underlying_escaped"}), "state integer invalid")
            require(all(type(s[k]) is bool for k in ("active_plain", "active_raw", "legacy_close_overlap", "tcp_bound", "physical_close_error", "underlying_escaped")), "state boolean invalid")
            require(s["problems"] == 0 and not s["active_plain"] and not s["active_raw"] and s["stop_generation"] == owner["generation"], "independent terminal problem or live ownership")
            require(0 < s["stop"] < s["close_begin"] < s["close_end"] < s["joined"] <= s["sequence"], "terminal lifecycle ordering invalid")
            rows = s["records"]
            require(rows is None or type(rows) is list and len(rows) <= 16, "terminal records exceed bound")
            rows = rows or []
            errors_count, attempted, written, overlap = 0, 0, 0, False
            previous_after = 0
            for v in rows:
                record_shape(v)
                require(previous_after < v["begin"] and v["after"] < s["joined"], "overlapping/reordered/post-retirement raw records")
                previous_after = v["after"]
                require(v["generation"] == owner["generation"] and v["begin"] < s["close_begin"], "changed owner or newly started post-close write")
                require(v["plain"]["begin"] == 0 or v["plain"]["control"], "terminal interval contains DATA or unknown application plaintext")
                require(not v["short"], "nil-error short write")
                candidate = control_overlap(v, s, owner["generation"])
                if v["error"]:
                    require(candidate and v["net_closed"] and v["written"] == 0 and v["attempt"] > 0 and s["close_begin"] < v["return"], "error lacks exact control/local-close provenance")
                    errors_count += 1; attempted += v["attempt"]; written += v["written"]
                    classes.append({"lane": i, "raw_begin": v["begin"], "classification": "terminal_local_control_cancel"})
                else:
                    require(not v["net_closed"] and v["written"] == v["attempt"], "successful raw outcome inconsistent")
                    if candidate:
                        classes.append({"lane": i, "raw_begin": v["begin"], "classification": "completed_control_close_overlap"})
                if candidate:
                    overlap = True
            require(s["raw_errors"] == errors_count and s["raw_shorts"] == 0, "lifetime raw error totals do not conserve or contain pre-intent errors")
            outside = activity["outside"]
            require(outside["error_calls"] == errors_count and outside["error_attempt_bytes"] == attempted and outside["error_written_bytes"] == written and outside["short_calls"] == 0, "unchanged terminal raw error totals do not conserve")
            for name in ("contained", "straddling"):
                require(activity[name]["error_calls"] == 0 and activity[name]["short_calls"] == 0, "DATA/straddling error remains rejecting")
            prefix = f"raw-write final record {i}"
            if errors_count:
                accepted_exceptions.add(prefix + ".raw_write_activity.outside contains error or short raw writes")
            if s["legacy_close_overlap"]:
                require(overlap and activity["ambiguous"] is True and activity["overflow"] is False and activity["tcp_state"]["overflow"] is False, "legacy close-overlap reason lacks exact active control")
                any_overlap = True
                accepted_exceptions.update({
                    prefix + " complete is not true", prefix + " reset_performed is not true", prefix + " ambiguous is not false",
                    prefix + ".raw_write_activity.tcp_state is invalid, ambiguous, or overflowed",
                    prefix + ".raw_write_activity.tcp_state.complete is not true at final closure",
                    prefix + ".raw_write_activity.tcp_state.reset_performed is not true at final closure",
                    prefix + ".raw_write_activity.tcp_state.invalid is not false at final closure",
                })
        if any_overlap:
            accepted_exceptions.add("raw-write final closure is incomplete or invalid")
        issues.extend(x for x in legacy["issues"] if x not in accepted_exceptions)
    except (KeyError, TypeError, ValueError, IndexError) as error:
        issues.append(str(error))
    legacy_issues = legacy.get("issues", []) if type(legacy) is dict else []
    if type(legacy_issues) is not list or not all(type(x) is str for x in legacy_issues):
        legacy_issues = []
    return {"schema_version": 1, "scope": "prospective-terminal-attribution-only", "legacy_final_verdict": legacy,
            "classifications": classes, "attribution_accepted": not issues, "issues": issues,
            "recognized_legacy_issues": sorted(set(legacy_issues) & accepted_exceptions),
            "performance_gate_cleared": False, "tcp_unavailable_captures_remain_inconclusive": True}


def bounded_json(path, limit):
    metadata = path.lstat()
    require(stat.S_ISREG(metadata.st_mode) and not path.is_symlink(), "input is not a regular file")
    require(0 < metadata.st_size <= limit, "input exceeds bound")
    with path.open("rb") as stream:
        data = stream.read(limit+1)
    require(0 < len(data) <= limit, "input exceeds bound")
    require(len(data) == metadata.st_size, "input changed while read")
    return data, json.loads(data)


def evaluate_files(raw_path, attribution_path, control_directory):
    # References bind the reviewed 106-case validator, with no copied subtree.
    manifest = json.loads(Path(__file__).with_name("LEGACY-VALIDATOR.json").read_text())
    for entry in manifest["files"]:
        require(hashlib.sha256(Path(entry["path"]).read_bytes()).hexdigest() == entry["sha256"], "legacy validator source changed")
    spec = importlib.util.spec_from_file_location("legacy_terminal_validator", manifest["entrypoint"])
    require(spec is not None and spec.loader is not None, "legacy validator cannot load")
    module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
    raw_bytes, raw = bounded_json(raw_path, 4 << 20)
    _, attribution = bounded_json(attribution_path, 256 << 10)
    for path in (raw_path, attribution_path):
        metadata = path.lstat()
        require(metadata.st_uid == os.geteuid() and stat.S_IMODE(metadata.st_mode) == 0o600, "terminal sidecar is not owned 0600")
    require(attribution.get("raw_sha256") == hashlib.sha256(raw_bytes).hexdigest(), "attribution raw sidecar hash differs")
    legacy = module.validate_final_closure(raw, control_directory)
    # Independently preserve and validate the old record's status and exact
    # artifact hashes. A legacy rejection is data; nonzero agent exit is fatal.
    _, saved = bounded_json(control_directory/"final-raw-write-validation.json", 4 << 20)
    _, record = bounded_json(control_directory/"final-raw-write-record.json", 4 << 20)
    require(type(record) is dict and set(record) == {"schema_version", "diagnostic", "agent_natural_join_status", "terminal_file_status", "validation_status", "terminal_artifact", "validation_artifact", "valid"}, "legacy final record schema differs")
    require(type(record["schema_version"]) is int and record["schema_version"] == 1 and record["diagnostic"] == "raw-write-final-closure", "legacy final record identity differs")
    require(type(record["agent_natural_join_status"]) is int and record["agent_natural_join_status"] == 0 and type(record["terminal_file_status"]) is int and record["terminal_file_status"] == 0, "endpoint did not actually join naturally with status zero")
    expected_status = 0 if legacy.get("accepted") is True else 1
    require(type(record["validation_status"]) is int and record["validation_status"] == expected_status and record["valid"] is (expected_status == 0), "legacy status was rewritten or is unexpected")
    require(saved == legacy, "saved and independently recomputed legacy verdict differ")
    for key, path in (("terminal_artifact", raw_path), ("validation_artifact", control_directory/"final-raw-write-validation.json")):
        data, _ = bounded_json(path, 4 << 20)
        require(record[key] == {"path": str(path), "size": len(data), "sha256": hashlib.sha256(data).hexdigest()}, "legacy artifact binding changed")
    result = classify(attribution, raw, legacy)
    result["legacy_final_record"] = record
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--raw", type=Path, required=True)
    parser.add_argument("--attribution", type=Path, required=True)
    parser.add_argument("--control-directory", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    result = evaluate_files(args.raw, args.attribution, args.control_directory)
    with args.output.open("x") as out:
        json.dump(result, out, indent=2); out.write("\n")
    raise SystemExit(0 if result["attribution_accepted"] else 1)


if __name__ == "__main__":
    main()
