import { describe, expect, test } from "bun:test";
import { parsePendingServerUpdate, serverUpdatePhaseLabel, serverUpdateTerminal, serverUpdateElapsed, serverUpdateDuration, serverUpdateTone, serverUpdateExplanation, serverUpdatePaused, serverUpdateDiagnostics } from "./serverUpdates";

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

describe("server update operator progress", () => {
  test("elapsed time uses real timestamps, freezes completed outcomes, and tolerates old executors", () => {
    const operation = { phase: "validating", startedAtUnixMillis: 10_000n, updatedAtUnixMillis: 70_000n };
    expect(serverUpdateElapsed(operation, 105_000)).toEqual({ total: "1m 35s", stage: "35s" });
    for (const phase of ["succeeded", "rolled_back", "failed", "recovery_required"]) {
      expect(serverUpdateElapsed({ ...operation, phase }, 999_000)).toEqual({ total: "1m 00s", stage: "Unavailable" });
    }
    expect(serverUpdateElapsed({ ...operation, startedAtUnixMillis: 0n, updatedAtUnixMillis: 0n }, 105_000)).toEqual({ total: "Unavailable", stage: "Unavailable" });
    expect(serverUpdateElapsed(operation, 1)).toEqual({ total: "Unavailable", stage: "Unavailable" });
    expect(serverUpdateDuration(3_661_000)).toBe("1h 01m");
    expect(serverUpdateDuration(Number.NaN)).toBe("Unavailable");
  });
  test("outcomes distinguish unsuccessful activation, restored rollback, and blocked recovery", () => {
    expect(serverUpdatePhaseLabel("failed")).toBe("Update stopped before activation");
    expect(serverUpdatePhaseLabel("rolled_back")).toBe("Update failed · previous version restored");
    expect(serverUpdatePhaseLabel("recovery_required")).toBe("Host recovery required");
    expect(serverUpdateTone("succeeded")).toBe("success");
    expect(serverUpdateTone("rolled_back")).toBe("warning");
    expect(serverUpdateTone("recovery_required")).toBe("error");
    expect(serverUpdateExplanation("pulling")).toContain("before it stops the current server");
    expect(serverUpdateExplanation("validating")).toContain("page can reconnect before");
    expect(serverUpdateExplanation("future_stage")).toContain("unfamiliar stage");
    expect(serverUpdatePaused("recovery_required")).toBe(true);
    expect(serverUpdateTerminal("recovery_required")).toBe(false);
  });
  test("copy diagnostics includes only known operation fields and no plan or setup data", () => {
    const operation = { id: pending.operationId, phase: "succeeded", previousVersion: "v1.0.0", targetVersion: "v1.0.1", detail: "Cleanup warning <script>", startedAtUnixMillis: 10n, updatedAtUnixMillis: 20n, planToken: "secret-token", setupCommand: "secret-command" };
    const value = JSON.parse(serverUpdateDiagnostics("selected environment", pending.instanceId, operation));
    expect(value).toEqual({ environment: "selected environment", instanceId: pending.instanceId, operationId: pending.operationId, phase: "succeeded", previousVersion: "v1.0.0", targetVersion: "v1.0.1", detail: "Cleanup warning <script>", startedAtUnixMillis: "10", updatedAtUnixMillis: "20" });
    expect(JSON.stringify(value)).not.toContain("secret-");
  });
});
