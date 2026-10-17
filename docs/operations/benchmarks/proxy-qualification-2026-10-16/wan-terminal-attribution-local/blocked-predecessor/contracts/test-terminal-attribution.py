#!/usr/bin/env python3
"""Small source contract suite. Legacy validation is independently hash-pinned.

The minimal raw fixtures test this adapter only, not the full legacy schema.
"""
import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("terminal", Path(__file__).resolve().parents[1]/"validate-terminal-attribution.py")
terminal = importlib.util.module_from_spec(spec); spec.loader.exec_module(terminal)


def fixture():
    attr = {"schema_version": 1, "expected_owners": 5, "intent_complete": True, "invalid": False, "raw_sha256": "0"*64, "owners": []}
    raw = {"records": []}
    for lane in range(5):
        state = {"sequence": 5, "problems": 0, "stop": 1, "stop_generation": lane+1, "close_begin": 2, "close_end": 3, "joined": 4,
                 "raw_errors": 0, "raw_shorts": 0, "active_plain": False, "active_raw": False, "legacy_close_overlap": False, "records": None}
        owner = {"lane": lane, "generation": lane+1, "session": f"session-{lane}", "local": f"127.0.0.1:{10000+lane}", "remote": "127.0.0.1:20000", "state": state}
        attr["owners"].append(owner)
        counters = {"error_calls": 0, "error_attempt_bytes": 0, "error_written_bytes": 0, "short_calls": 0}
        activity = {"generation": lane+1, "local_address": owner["local"], "remote_address": owner["remote"], "outside": counters,
                    "contained": copy.deepcopy(counters), "straddling": copy.deepcopy(counters), "ambiguous": False, "overflow": False, "tcp_state": {"overflow": False}}
        raw["records"].append({"lane": lane, "session_identity": owner["session"], "raw_write_activity": activity})
    return attr, raw, {"accepted": True, "issues": []}


def cancelled():
    attr, raw, legacy = fixture()
    s = attr["owners"][4]["state"]
    s.update(sequence=9, stop=3, close_begin=4, close_end=5, joined=9, raw_errors=1,
             records=[{"begin": 2, "return": 6, "after": 7, "generation": 5, "attempt": 34, "written": 0, "error": True, "net_closed": True, "short": False,
                       "plain": {"begin": 1, "end": 8, "attempt": 12, "control": True, "headers": [{"type": 3, "flags": 0, "stream": 0, "value": 0}]}}])
    raw["records"][4]["raw_write_activity"]["outside"].update(error_calls=1, error_attempt_bytes=34)
    legacy.update(accepted=False, issues=["raw-write final record 4.raw_write_activity.outside contains error or short raw writes"])
    return attr, raw, legacy


class Contracts(unittest.TestCase):
    def test_clean_path(self):
        self.assertTrue(terminal.classify(*fixture())["attribution_accepted"])

    def test_control_cancel_preserves_legacy(self):
        inputs = cancelled(); before = copy.deepcopy(inputs)
        result = terminal.classify(*inputs)
        self.assertTrue(result["attribution_accepted"])
        self.assertFalse(result["legacy_final_verdict"]["accepted"])
        self.assertFalse(result["performance_gate_cleared"])
        self.assertEqual(inputs, before)

    def test_only_derived_close_ambiguity(self):
        a, r, l = cancelled()
        a["owners"][4]["state"]["legacy_close_overlap"] = True
        r["records"][4]["raw_write_activity"]["ambiguous"] = True
        l["issues"] += ["raw-write final record 4 ambiguous is not false", "raw-write final closure is incomplete or invalid"]
        self.assertTrue(terminal.classify(a,r,l)["attribution_accepted"])
        a["owners"][4]["state"]["problems"] = 16
        self.assertFalse(terminal.classify(a,r,l)["attribution_accepted"])

    def test_completed_control_overlap(self):
        a,r,l = cancelled(); s=a["owners"][4]["state"]; v=s["records"][0]
        s.update(stop=4,close_begin=5,close_end=6,raw_errors=0,legacy_close_overlap=True)
        v.update(**{"return":3,"written":34,"error":False,"net_closed":False})
        r["records"][4]["raw_write_activity"]["outside"].update(error_calls=0,error_attempt_bytes=0)
        r["records"][4]["raw_write_activity"]["ambiguous"]=True
        l["issues"]=["raw-write final record 4 ambiguous is not false", "raw-write final closure is incomplete or invalid"]
        self.assertTrue(terminal.classify(a,r,l)["attribution_accepted"])

    def test_rejection_matrix(self):
        changes = {
            "missing_coverage": lambda a,r,l: a.update(intent_complete=False),
            "missing_owner": lambda a,r,l: a["owners"].pop(),
            "changed_generation": lambda a,r,l: a["owners"][4].update(generation=99),
            "duplicate_identity": lambda a,r,l: a["owners"][4].update(session=a["owners"][0]["session"]),
            "wrong_stop_generation": lambda a,r,l: a["owners"][4]["state"].update(stop_generation=99),
            "pre_intent_error": lambda a,r,l: a["owners"][4]["state"].update(stop=7),
            "post_retirement": lambda a,r,l: a["owners"][4]["state"].update(joined=6),
            "unknown_plaintext": lambda a,r,l: a["owners"][4]["state"]["records"][0]["plain"].update(control=False,headers=None),
            "data": lambda a,r,l: a["owners"][4]["state"]["records"][0]["plain"]["headers"][0].update(type=0),
            "nonlocal_error": lambda a,r,l: a["owners"][4]["state"]["records"][0].update(net_closed=False),
            "partial_write": lambda a,r,l: a["owners"][4]["state"]["records"][0].update(written=1),
            "nil_short": lambda a,r,l: a["owners"][4]["state"]["records"][0].update(error=False,short=True),
            "wrong_capture_order": lambda a,r,l: a["owners"][4]["state"]["records"][0].update(after=6),
            "wrong_plain_end": lambda a,r,l: a["owners"][4]["state"]["records"][0]["plain"].update(end=6),
            "new_post_close_write": lambda a,r,l: a["owners"][4]["state"]["records"][0].update(begin=5),
            "overflow": lambda a,r,l: a["owners"][4]["state"].update(problems=1),
            "active_raw": lambda a,r,l: a["owners"][4]["state"].update(active_raw=True),
            "unexplained_legacy": lambda a,r,l: l["issues"].append("unexplained legacy failure"),
            "lost_error_total": lambda a,r,l: a["owners"][4]["state"].update(raw_errors=0),
            "lost_raw_error_total": lambda a,r,l: r["records"][4]["raw_write_activity"]["outside"].update(error_calls=0),
            "contained_error": lambda a,r,l: r["records"][4]["raw_write_activity"]["contained"].update(error_calls=1),
            "bool_integer": lambda a,r,l: a["owners"][4]["state"].update(problems=False),
        }
        for name, change in changes.items():
            with self.subTest(name=name):
                inputs=cancelled();change(*inputs)
                self.assertFalse(terminal.classify(*inputs)["attribution_accepted"])

    def test_control_shapes(self):
        for typ,flags,stream,value in [(1,0,1,123),(1,4,1,0),(1,0,0,4096),(2,1,0,7),(2,2,0,7),(3,0,0,0),(4,0,1,4096),(5,0,1,4096),(5,0,0,4096)]:
            self.assertTrue(terminal.controls([dict(type=typ,flags=flags,stream=stream,value=value)]))
        good=dict(type=3,flags=0,stream=0,value=0)
        self.assertTrue(terminal.controls([good]*17))
        for headers in [[],[good]*18,[dict(good,type=0)],[dict(good,type=99)],[dict(good,value=1)], [dict(type=4,flags=0,stream=1,value=1)],[dict(type=2,flags=3,stream=0,value=1)]]:
            self.assertFalse(terminal.controls(headers))


if __name__ == "__main__":
    unittest.main()
