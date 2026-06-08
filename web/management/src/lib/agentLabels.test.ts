import { describe, expect, test } from "bun:test";
import type { Agent } from "@/gen/proto/p2pstream/v1/management_pb";
import {
  AGENT_ID_SYSTEM_LABEL_KEY,
  agentLabelKeySuggestions,
  agentLabelPairs,
  agentLabelRowsToRecord,
  agentLabelValueSuggestions,
  agentMatchesSelector,
  selectorRowsFromLabels,
  selectorRowsToRecord,
  systemAgentLabelPairs,
  userAgentLabelPairs,
  validateSelectorRows,
  validateUserAgentLabelRows,
} from "@/lib/agentLabels";

describe("agentLabels", () => {
  test("splits user and system labels", () => {
    const labels = {
      site: "home",
      [AGENT_ID_SYSTEM_LABEL_KEY]: "agent-public-id",
    };

    expect(agentLabelPairs(labels).map((label) => [label.key, label.value, label.system])).toEqual([
      [AGENT_ID_SYSTEM_LABEL_KEY, "agent-public-id", true],
      ["site", "home", false],
    ]);
    expect(userAgentLabelPairs(labels).map((label) => label.key)).toEqual(["site"]);
    expect(systemAgentLabelPairs(labels).map((label) => label.key)).toEqual([AGENT_ID_SYSTEM_LABEL_KEY]);
  });

  test("validates user labels", () => {
    expect(validateUserAgentLabelRows([{ key: "site", value: "home" }])).toBe("");
    expect(validateUserAgentLabelRows([{ key: "empty", value: "" }])).toBe("");
    expect(validateUserAgentLabelRows([{ key: "p2pstream.io/agent-id", value: "a" }])).toContain("system-owned");
    expect(validateUserAgentLabelRows([{ key: "site", value: "a" }, { key: " site ", value: "b" }])).toContain("Duplicate");
    expect(validateUserAgentLabelRows([{ key: "bad\nkey", value: "a" }])).toContain("line breaks");
  });

  test("builds user label payload records", () => {
    expect(agentLabelRowsToRecord([
      { key: " site ", value: " home " },
      { key: "role", value: "" },
    ])).toEqual({ site: "home", role: "" });
  });

  test("validates selector labels while allowing system keys and empty values", () => {
    expect(validateSelectorRows([{ key: AGENT_ID_SYSTEM_LABEL_KEY, value: "agent-public-id" }])).toBe("");
    expect(validateSelectorRows([{ key: "role", value: "" }])).toBe("");
    expect(validateSelectorRows([])).toContain("at least one");
    expect(validateSelectorRows([{ key: "", value: "home" }])).toContain("key is required");
    expect(validateSelectorRows([{ key: "role", value: "a" }, { key: "role", value: "b" }])).toContain("Duplicate");
  });

  test("round trips selector labels without collapsing multiple entries", () => {
    const rows = selectorRowsFromLabels({ site: "home", role: "app" });

    expect(rows.map((row) => row.key)).toEqual(["role", "site"]);
    expect(selectorRowsToRecord(rows)).toEqual({ site: "home", role: "app" });
  });

  test("matches agents only when all selector labels match", () => {
    const selected = agent({ labels: { site: "home", role: "app" } });
    const wrongRole = agent({ labels: { site: "home", role: "db" } });

    expect(agentMatchesSelector(selected, { site: "home", role: "app" })).toBe(true);
    expect(agentMatchesSelector(wrongRole, { site: "home", role: "app" })).toBe(false);
    expect(agentMatchesSelector(selected, {})).toBe(false);
  });

  test("suggests label keys and values from agents", () => {
    const agents = [
      agent({ labels: { site: "home", role: "app", [AGENT_ID_SYSTEM_LABEL_KEY]: "agent-a" } }),
      agent({ labels: { site: "office", zone: "dmz", [AGENT_ID_SYSTEM_LABEL_KEY]: "agent-b" } }),
    ];

    expect(agentLabelKeySuggestions(agents)).toEqual([AGENT_ID_SYSTEM_LABEL_KEY, "role", "site", "zone"]);
    expect(agentLabelValueSuggestions(agents, "site")).toEqual(["home", "office"]);
    expect(agentLabelValueSuggestions(agents, "missing")).toEqual([]);
  });
});

function agent(overrides: Partial<Agent> = {}): Agent {
  return {
    $typeName: "p2pstream.v1.Agent",
    id: 1n,
    publicId: "agent-a",
    name: "agent-a",
    enabled: true,
    connected: true,
    activeRequests: 0n,
    createdAtUnixMillis: 0n,
    updatedAtUnixMillis: 0n,
    lastConnectedAtUnixMillis: 0n,
    lastDisconnectedAtUnixMillis: 0n,
    labels: {},
    ...overrides,
  };
}
