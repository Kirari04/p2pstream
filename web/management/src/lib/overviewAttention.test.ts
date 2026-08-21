import { describe, expect, test } from "bun:test";
import { ProxyState, type PublicRateLimitRule } from "@/gen/proto/p2pstream/v1/management_pb";
import { deriveOverviewAttention, type OverviewAttentionInput } from "@/lib/overviewAttention";
import type { TrafficPolicyAttentionWarning } from "@/lib/trafficPolicyWorkbench";

describe("overviewAttention", () => {
  test("returns no attention items for a healthy runtime", () => {
    const items = deriveOverviewAttention({
      status: status(ProxyState.RUNNING),
      config: {
        listeners: [{ id: 1n, name: "public", enabled: true }],
        agents: [{ name: "edge", enabled: true, connected: true }],
      },
      trafficWindow: { proxyRequests: 20n },
    });

    expect(items).toEqual([]);
  });

  test("derives bounded plain-text runtime errors without reflecting listener labels", () => {
    const hostileLabel = `<img src=x onerror=alert(1)>${"x".repeat(500)}`;
    const items = deriveOverviewAttention({
      status: status(ProxyState.ERROR, `<script>alert(1)</script>\u202e${"z".repeat(500)}`, [
        listenerStatus({ listenerId: 7n, state: ProxyState.ERROR, lastError: "bind failed\u0000now" }),
      ]),
      config: {
        listeners: [{ id: 7n, name: hostileLabel, enabled: true }],
      },
    });

    expect(items.map((item) => item.key)).toEqual(["proxy-error", "listener-errors"]);
    expect(items[0]?.actionRoute).toBe("/monitor/diagnostics");
    expect(items[0]?.severity).toBe("error");
    expect(items[0]?.detail.length).toBeLessThanOrEqual(280);
    expect(items[0]?.detail).not.toContain("\u202e");
    expect(items[1]?.detail).toContain("bind failed�now");
    expect(JSON.stringify(items)).not.toContain(hostileLabel);
  });

  test("reports stopped proxy, down listeners, and disconnected enabled agents", () => {
    const items = deriveOverviewAttention({
      status: status(ProxyState.STOPPED, "", [
        listenerStatus({ listenerId: 1n, state: ProxyState.STOPPED }),
        listenerStatus({ listenerId: 2n, state: ProxyState.STOPPED }),
        listenerStatus({ listenerId: 3n, state: ProxyState.STOPPED, disabled: true }),
      ]),
      config: {
        listeners: [
          { id: 1n, enabled: true },
          { id: 2n, enabled: true },
          { id: 3n, enabled: false },
        ],
        agents: [
          { enabled: true, connected: false },
          { enabled: true, connected: false },
          { enabled: false, connected: false },
        ],
      },
    });

    expect(items.map((item) => item.key)).toEqual(["proxy-stopped", "listeners-down", "agents-disconnected"]);
    expect(items.find((item) => item.key === "listeners-down")?.title).toBe("2 enabled listeners are down");
    expect(items.find((item) => item.key === "agents-disconnected")?.actionRoute).toBe("/agent/fleet");
  });

  test("reports internal failures before non-success traffic", () => {
    const items = deriveOverviewAttention({
      trafficWindow: {
        proxyRequests: 1_234n,
        proxyClientError: 10n,
        proxyServerError: 5n,
        proxyInternalError: 2n,
      },
    });

    expect(items.map((item) => item.key)).toEqual(["proxy-failures", "non-success-responses"]);
    expect(items[0]?.detail).toContain("2 internal failures");
    expect(items[1]?.detail).toContain("15 client or server error responses of 1,234 total requests");
    expect(items[1]?.actionRoute).toBe("/monitor/traffic");
  });

  test("groups only actionable policy warnings by their destination", () => {
    const warnings: TrafficPolicyAttentionWarning[] = [
      warning("duplicate-priority", "rate-limit"),
      warning("duplicate-priority", "rate-limit"),
      warning("disabled-rule", "rate-limit"),
      warning("captcha-provider-missing", "waf"),
      warning("any-request-rule", "cache"),
    ];
    const items = deriveOverviewAttention({ policyWarnings: warnings });

    expect(items.map((item) => ({ key: item.key, route: item.actionRoute }))).toEqual([
      { key: "policy-rate-limit", route: "/policies/rate-limits" },
      { key: "policy-waf", route: "/policies/waf" },
    ]);
    expect(items[0]?.detail).toBe("2 actionable configuration warnings were detected.");
  });

  test("can derive policy warnings directly from the public configuration shape", () => {
    const rules = [
      minimalRateLimitRule(1n, 10n),
      minimalRateLimitRule(2n, 10n),
    ];
    const items = deriveOverviewAttention({ config: { rateLimitRules: rules } });

    expect(items).toHaveLength(1);
    expect(items[0]?.key).toBe("policy-rate-limit");
    expect(items[0]?.actionRoute).toBe("/policies/rate-limits");
  });
});

function status(
  state: ProxyState,
  lastError = "",
  listeners = [listenerStatus({ listenerId: 1n, state: ProxyState.RUNNING, running: true })],
): OverviewAttentionInput["status"] {
  return {
    proxyRunning: state === ProxyState.RUNNING,
    proxyLastError: lastError,
    proxy: { state, lastError, listeners },
  };
}

function listenerStatus(overrides: Partial<{
  listenerId: bigint;
  state: ProxyState;
  lastError: string;
  running: boolean;
  disabled: boolean;
}> = {}) {
  return {
    listenerId: 0n,
    state: ProxyState.UNSPECIFIED,
    lastError: "",
    running: false,
    disabled: false,
    ...overrides,
  };
}

function warning(
  code: TrafficPolicyAttentionWarning["code"],
  policyKind: NonNullable<TrafficPolicyAttentionWarning["policyKind"]>,
): TrafficPolicyAttentionWarning {
  return { code, policyKind, message: "untrusted warning detail" };
}

function minimalRateLimitRule(id: bigint, priority: bigint): PublicRateLimitRule {
  return {
    id,
    priority,
    name: `rule-${id.toString()}`,
    enabled: true,
    matchRule: undefined,
  } as unknown as PublicRateLimitRule;
}
