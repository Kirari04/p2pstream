import type { Agent } from "@/gen/proto/p2pstream/v1/management_pb";

export const RESERVED_AGENT_LABEL_PREFIX = "p2pstream.io/";
export const AGENT_ID_SYSTEM_LABEL_KEY = "p2pstream.io/agent-id";

export type AgentLabelPair = {
  id: string;
  key: string;
  value: string;
  system: boolean;
};

export type SelectorLabelRow = {
  id: string;
  key: string;
  value: string;
};

export function isSystemAgentLabel(key: string): boolean {
  return key.trim().startsWith(RESERVED_AGENT_LABEL_PREFIX);
}

export function agentLabelPairs(labels: Record<string, string> = {}): AgentLabelPair[] {
  return Object.entries(labels)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => ({
      id: `${isSystemAgentLabel(key) ? "system" : "user"}:${key}`,
      key,
      value,
      system: isSystemAgentLabel(key),
    }));
}

export function userAgentLabelPairs(labels: Record<string, string> = {}): AgentLabelPair[] {
  return agentLabelPairs(labels).filter((label) => !label.system);
}

export function systemAgentLabelPairs(labels: Record<string, string> = {}): AgentLabelPair[] {
  return agentLabelPairs(labels).filter((label) => label.system);
}

export function agentLabelRowsToRecord(rows: readonly Pick<AgentLabelPair, "key" | "value">[]): Record<string, string> {
  const labels: Record<string, string> = {};
  for (const row of rows) {
    labels[row.key.trim()] = row.value.trim();
  }
  return labels;
}

export function validateUserAgentLabelRows(rows: readonly Pick<AgentLabelPair, "key" | "value">[]): string {
  return validateLabelRows(rows, { reservedAllowed: false, requireOne: false, label: "Agent label" });
}

export function selectorRowsFromLabels(labels: Record<string, string> = {}): SelectorLabelRow[] {
  return Object.entries(labels)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => ({ id: `selector:${key}`, key, value }));
}

export function selectorRowsToRecord(rows: readonly SelectorLabelRow[]): Record<string, string> {
  const labels: Record<string, string> = {};
  for (const row of rows) {
    const key = row.key.trim();
    if (key) {
      labels[key] = row.value.trim();
    }
  }
  return labels;
}

export function validateSelectorRows(rows: readonly Pick<SelectorLabelRow, "key" | "value">[]): string {
  return validateLabelRows(rows, { reservedAllowed: true, requireOne: true, label: "Selector label" });
}

export function agentMatchesSelector(agent: Agent, selector: Record<string, string>): boolean {
  const entries = Object.entries(selector);
  return entries.length > 0 && entries.every(([key, value]) => agent.labels[key] === value);
}

export function agentLabelValueSuggestions(agents: readonly Agent[], key: string): string[] {
  const trimmedKey = key.trim();
  if (!trimmedKey) return [];
  const values = new Set<string>();
  for (const agent of agents) {
    const value = agent.labels[trimmedKey];
    if (value !== undefined) {
      values.add(value);
    }
  }
  return [...values].sort((a, b) => a.localeCompare(b));
}

export function agentLabelKeySuggestions(agents: readonly Agent[]): string[] {
  const keys = new Set<string>([AGENT_ID_SYSTEM_LABEL_KEY]);
  for (const agent of agents) {
    for (const key of Object.keys(agent.labels)) {
      keys.add(key);
    }
  }
  return [...keys].sort((a, b) => {
    if (a === AGENT_ID_SYSTEM_LABEL_KEY) return -1;
    if (b === AGENT_ID_SYSTEM_LABEL_KEY) return 1;
    return a.localeCompare(b);
  });
}

function validateLabelRows(
  rows: readonly Pick<AgentLabelPair, "key" | "value">[],
  options: { reservedAllowed: boolean; requireOne: boolean; label: string },
): string {
  const seen = new Set<string>();
  let populated = 0;
  for (const row of rows) {
    const key = row.key.trim();
    const value = row.value.trim();
    if (!key) {
      return `${options.label} key is required.`;
    }
    populated += 1;
    if (key.length > 128) {
      return `${options.label} keys must be 1-128 characters.`;
    }
    if (value.length > 256) {
      return `${options.label} values must be at most 256 characters.`;
    }
    if (/[\r\n]/.test(key) || /[\r\n]/.test(value)) {
      return `${options.label}s must not contain line breaks.`;
    }
    if (!options.reservedAllowed && isSystemAgentLabel(key)) {
      return "Labels under p2pstream.io/ are system-owned.";
    }
    if (seen.has(key)) {
      return `Duplicate ${options.label.toLowerCase()} key "${key}".`;
    }
    seen.add(key);
  }
  if (options.requireOne && populated === 0) {
    return "Agent targets need at least one selector label.";
  }
  return "";
}
