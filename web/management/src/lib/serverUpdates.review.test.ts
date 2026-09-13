import { describe, expect, test } from "bun:test";
import { parsePendingServerUpdate, serverUpdatePhaseLabel, serverUpdateTerminal } from "./serverUpdates";

const pending = {
  instanceId: "11111111-1111-4111-8111-111111111111",
  operationId: "33333333-3333-4333-8333-333333333333",
  planToken: "opaque-preview-bound-to-instance",
};

describe("server update review regressions", () => {
  test("only completed host outcomes release the new-update controls", () => {
    for (const phase of ["accepted", "pulling", "preparing", "stopping", "backing_up", "deploying", "validating", "committing", "rolling_back", "recovery_required", "future_phase"]) {
      expect(serverUpdateTerminal(phase)).toBe(false);
      expect(serverUpdatePhaseLabel(phase)).not.toBe("");
    }
    for (const phase of ["succeeded", "rolled_back", "failed"]) expect(serverUpdateTerminal(phase)).toBe(true);
  });
  test("a reloaded page preserves the exact retry identity and opaque preview", () => {
    expect(parsePendingServerUpdate(JSON.stringify(pending))).toEqual(pending);
    expect(parsePendingServerUpdate(JSON.stringify({ ...pending, extra: "ignored" }))).toEqual(pending);
  });
  test("corrupt or oversized browser records never throw or fabricate an operation", () => {
    for (const value of [null, "", "null", "42", "[]", "{", JSON.stringify({}), JSON.stringify({ ...pending, instanceId: "other-host" }), JSON.stringify({ ...pending, operationId: null }), JSON.stringify({ ...pending, planToken: "" }), JSON.stringify({ ...pending, planToken: "x".repeat(100_001) })]) {
      expect(parsePendingServerUpdate(value)).toBeNull();
    }
  });
});
