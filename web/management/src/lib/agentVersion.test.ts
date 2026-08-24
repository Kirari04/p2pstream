import { describe, expect, test } from "bun:test";
import { agentBuildStatus, shortBuildCommit } from "./agentVersion";

describe("agentBuildStatus", () => {
  test("marks the same release as current for agents that predate commit reporting", () => {
    expect(agentBuildStatus({ agentVersion: "v1.2.3", serverVersion: "v1.2.3", serverCommit: "server-sha" })).toEqual({
      state: "current",
      label: "Matches server",
    });
  });

  test("detects an older release as updateable", () => {
    expect(agentBuildStatus({ agentVersion: "v1.9.12", serverVersion: "v2.0.0" })).toEqual({
      state: "update_available",
      label: "Update available",
    });
  });

  test("does not call an agent newer than the server outdated", () => {
    expect(agentBuildStatus({ agentVersion: "v2.1.0", serverVersion: "v2.0.9" })).toEqual({
      state: "ahead",
      label: "Ahead of server",
    });
  });

  test("uses commit identity to distinguish mutable staging builds", () => {
    expect(agentBuildStatus({
      agentVersion: "staging",
      agentCommit: "old-sha",
      serverVersion: "staging",
      serverCommit: "new-sha",
    })).toEqual({ state: "different", label: "Different build" });
  });

  test("does not claim an older mutable-tag agent matches without a reported commit", () => {
    expect(agentBuildStatus({
      agentVersion: "staging",
      serverVersion: "staging",
      serverCommit: "new-sha",
    })).toEqual({ state: "unverified", label: "Build unverified" });
  });

  test("reports custom version mismatches without inferring direction", () => {
    expect(agentBuildStatus({ agentVersion: "nightly", serverVersion: "dev" })).toEqual({
      state: "different",
      label: "Differs from server",
    });
  });

  test("handles missing agent and server build data", () => {
    expect(agentBuildStatus({ serverVersion: "v1.0.0" })).toEqual({ state: "unknown", label: "Not reported" });
    expect(agentBuildStatus({ agentVersion: "v1.0.0" })).toEqual({ state: "reported", label: "Reported" });
  });
});

test("shortBuildCommit bounds build labels", () => {
  expect(shortBuildCommit(" 0123456789abcdef ")).toBe("0123456789ab");
  expect(shortBuildCommit("abc", 2)).toBe("ab");
});
