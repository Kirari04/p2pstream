import { describe, expect, test } from "bun:test";
import type {
  AgentConnectionSession,
  AgentUptimeSummary,
  DashboardProxyDimensionSummary,
  DashboardTrafficBucket,
  DashboardWindowSummary,
  GetDashboardResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  agentUptimeSummaryById,
  bytesPerSecond,
  cacheActivityRequests,
  cacheHitRate,
  cacheLookupRequests,
  filledTrafficBuckets,
  formatByteRate,
  formatBytes,
  formatDuration,
  formatLongDuration,
  formatNumber,
  formatPercent,
  fleetUptimePercent,
  formatPathPrefix,
  nonSuccessRate,
  nonSuccessRequests,
  proxyFailureRequests,
  recentDisconnectCount,
  statusTone,
  statusClassCounts,
  successRate,
  windowByLabel,
} from "@/lib/dashboardStats";
import { DashboardProxyDimension } from "@/gen/proto/p2pstream/v1/management_pb";

describe("dashboardStats", () => {
  test("finds windows by label", () => {
    const dashboard = dashboardResponse({ windows: [windowSummary({ label: "5m" }), windowSummary({ label: "1h" })] });

    expect(windowByLabel(dashboard, "1h")?.label).toBe("1h");
    expect(windowByLabel(dashboard, "24h")).toBeNull();
  });

  test("computes success, non-success, and proxy failure metrics", () => {
    const window = windowSummary({
      proxyRequests: 10n,
      proxySuccess: 7n,
      proxyClientError: 2n,
      proxyServerError: 1n,
      proxyInternalError: 4n,
    });

    expect(successRate(window)).toBe(0.7);
    expect(nonSuccessRequests(window)).toBe(3n);
    expect(proxyFailureRequests(window)).toBe(4n);
    expect(nonSuccessRate(window)).toBe(0.3);
    expect(successRate(windowSummary({ proxyRequests: 0n }))).toBe(0);
  });

  test("maps status tones and formats path prefixes", () => {
    expect(statusTone(200n)).toBe("success");
    expect(statusTone(301n)).toBe("redirect");
    expect(statusTone(404n)).toBe("client-error");
    expect(statusTone(502n)).toBe("server-error");
    expect(statusTone(102n)).toBe("neutral");
    expect(formatPathPrefix("/api/users/...")).toBe("/api/users/...");
    expect(formatPathPrefix("")).toBe("-");
    expect(formatPathPrefix(undefined)).toBe("-");
  });

  test("computes cache hit rate from lookups and excludes bypasses", () => {
    const window = windowSummary({
      proxyCacheHits: 7n,
      proxyCacheMisses: 3n,
      proxyCacheBypasses: 20n,
    });

    expect(cacheHitRate(window)).toBe(0.7);
    expect(cacheLookupRequests(window)).toBe(10n);
    expect(cacheActivityRequests(window)).toBe(30n);
    expect(cacheHitRate(windowSummary())).toBe(0);
  });

  test("formats byte sizes and rates", () => {
    expect(formatBytes(512n)).toBe("512 B");
    expect(formatBytes(1536n)).toBe("1.5 KiB");
    expect(formatBytes(10n * 1024n * 1024n)).toBe("10 MiB");
    expect(formatByteRate(2048)).toBe("2.0 KiB/s");
    expect(formatByteRate(null)).toBe("-");
  });

  test("formats durations, percentages, and large numbers", () => {
    expect(formatDuration(250n)).toBe("250 ms");
    expect(formatDuration(1500n)).toBe("1.5 s");
    expect(formatDuration(0n)).toBe("-");
    expect(formatPercent(0.034)).toBe("3.4%");
    expect(formatPercent(0.91)).toBe("91%");
    expect(formatNumber(12345678901234567890n)).toBe("12,345,678,901,234,567,890");
  });

  test("formats long durations for uptime displays", () => {
    expect(formatLongDuration(42_000n)).toBe("42s");
    expect(formatLongDuration(5n * 60n * 1000n)).toBe("5m");
    expect(formatLongDuration(3n * 60n * 60n * 1000n + 5n * 60n * 1000n)).toBe("3h 5m");
    expect(formatLongDuration(2n * 24n * 60n * 60n * 1000n + 3n * 60n * 60n * 1000n)).toBe("2d 3h");
    expect(formatLongDuration(0n)).toBe("-");
  });

  test("indexes agent uptime summaries by id and computes fleet uptime", () => {
    const summaries = [
      agentUptimeSummary({ agentId: 10n, uptimeMillis: 90n, downtimeMillis: 10n }),
      agentUptimeSummary({ agentId: 11n, uptimeMillis: 50n, downtimeMillis: 50n }),
    ];

    expect(agentUptimeSummaryById(summaries).get("10")?.agentId).toBe(10n);
    expect(fleetUptimePercent(summaries)).toBe(0.7);
    expect(fleetUptimePercent([])).toBeNull();
  });

  test("counts recent completed disconnects", () => {
    const now = 1_800_000_000_000n;
    const sessions = [
      agentConnectionSession({ active: true, disconnectedAtUnixMillis: 0n }),
      agentConnectionSession({ disconnectedAtUnixMillis: now - 10n * 60n * 1000n }),
      agentConnectionSession({ disconnectedAtUnixMillis: now - 2n * 24n * 60n * 60n * 1000n }),
    ];

    expect(recentDisconnectCount(sessions, now)).toBe(1);
  });

  test("derives byte rates from the actual selected window duration", () => {
    const window = windowSummary({ sinceUnixMillis: 1_000n });

    expect(bytesPerSecond(6_000n, window, 4_000n)).toBe(2000);
    expect(bytesPerSecond(6_000n, windowSummary({ sinceUnixMillis: 4_000n }), 4_000n)).toBeNull();
  });

  test("fills missing traffic buckets and preserves existing buckets", () => {
    const now = 60n * 60n * 1000n;
    const existingBucket = bucket({ bucketUnixMillis: now - 5n * 60n * 1000n, requests: 3n, clientError: 1n, requestBytes: 100n, responseBytes: 200n });
    const filled = filledTrafficBuckets([existingBucket], now);

    expect(filled).toHaveLength(12);
    expect(filled[10]?.requests).toBe(3n);
    expect(filled[10]?.nonSuccess).toBe(1n);
    expect(filled[10]?.totalBytes).toBe(300n);
    expect(filled[0]?.requests).toBe(0n);
  });

  test("maps status class counts", () => {
    const counts = statusClassCounts([
      dimensionSummary({ label: "2xx", requests: 7n }),
      dimensionSummary({ label: "5xx", requests: 2n }),
    ]);

    expect(counts["2xx"]).toBe(7n);
    expect(counts["3xx"]).toBe(0n);
    expect(counts["5xx"]).toBe(2n);
  });
});

function dashboardResponse(overrides: Partial<GetDashboardResponse> = {}): GetDashboardResponse {
  return {
    $typeName: "p2pstream.v1.GetDashboardResponse",
    status: undefined,
    windows: [],
    agentConnections: undefined,
    retentionDays: 0n,
    generatedAtUnixMillis: 0n,
    topListeners: [],
    topRouteTargets: [],
    topRoutes: [],
    topAgents: [],
    topErrorKinds: [],
    statusClasses: [],
    trafficBuckets: [],
    agentUptimeSummaries: [],
    recentAgentConnections: [],
    ...overrides,
  };
}

function windowSummary(overrides: Partial<DashboardWindowSummary> = {}): DashboardWindowSummary {
  return {
    $typeName: "p2pstream.v1.DashboardWindowSummary",
    label: "1h",
    sinceUnixMillis: 0n,
    proxyRequests: 0n,
    proxySuccess: 0n,
    proxyClientError: 0n,
    proxyServerError: 0n,
    proxyInternalError: 0n,
    proxyAvgDurationMs: 0n,
    agentSamples: 0n,
    agentReqSuccess: 0n,
    agentReqClientError: 0n,
    agentReqServerError: 0n,
    agentReqInternalError: 0n,
    agentBytesReceived: 0n,
    agentBytesSent: 0n,
    agentAvgMemoryMb: 0n,
    agentMaxMemoryMb: 0n,
    agentAvgGoroutines: 0n,
    agentMaxGoroutines: 0n,
    agentAvgCpuPercent: 0,
    agentMaxCpuPercent: 0,
    proxyRequestBytes: 0n,
    proxyResponseBytes: 0n,
    proxyTotalBytes: 0n,
    proxyAvgRequestBytes: 0n,
    proxyAvgResponseBytes: 0n,
    proxyMaxDurationMs: 0n,
    proxySlowRequests: 0n,
    proxyCacheHits: 0n,
    proxyCacheMisses: 0n,
    proxyCacheBypasses: 0n,
    proxyCacheStored: 0n,
    proxyCacheStoreFailed: 0n,
    proxyCacheHitBytes: 0n,
    proxyCacheStoredBytes: 0n,
    ...overrides,
  };
}

function dimensionSummary(overrides: Partial<DashboardProxyDimensionSummary> = {}): DashboardProxyDimensionSummary {
  return {
    $typeName: "p2pstream.v1.DashboardProxyDimensionSummary",
    dimension: DashboardProxyDimension.STATUS_CLASS,
    id: 0n,
    label: "",
    requests: 0n,
    success: 0n,
    clientError: 0n,
    serverError: 0n,
    internalError: 0n,
    avgDurationMs: 0n,
    requestBytes: 0n,
    responseBytes: 0n,
    ...overrides,
  };
}

function bucket(overrides: Partial<DashboardTrafficBucket> = {}): DashboardTrafficBucket {
  return {
    $typeName: "p2pstream.v1.DashboardTrafficBucket",
    bucketUnixMillis: 0n,
    requests: 0n,
    success: 0n,
    clientError: 0n,
    serverError: 0n,
    internalError: 0n,
    requestBytes: 0n,
    responseBytes: 0n,
    avgDurationMs: 0n,
    ...overrides,
  };
}

function agentUptimeSummary(overrides: Partial<AgentUptimeSummary> = {}): AgentUptimeSummary {
  return {
    $typeName: "p2pstream.v1.AgentUptimeSummary",
    agentId: 0n,
    agentPublicId: "",
    agentName: "",
    enabled: true,
    connected: false,
    currentConnectedAtUnixMillis: 0n,
    currentUptimeMillis: 0n,
    currentOfflineSinceUnixMillis: 0n,
    currentDowntimeMillis: 0n,
    uptimeMillis: 0n,
    downtimeMillis: 0n,
    uptimePercent: 0,
    connectionCount: 0n,
    disconnectCount: 0n,
    observedSinceUnixMillis: 0n,
    observedUntilUnixMillis: 0n,
    lastConnectedAtUnixMillis: 0n,
    lastDisconnectedAtUnixMillis: 0n,
    ...overrides,
  };
}

function agentConnectionSession(overrides: Partial<AgentConnectionSession> = {}): AgentConnectionSession {
  return {
    $typeName: "p2pstream.v1.AgentConnectionSession",
    id: 0n,
    agentId: 0n,
    agentPublicId: "",
    agentName: "",
    connectedAtUnixMillis: 0n,
    disconnectedAtUnixMillis: 0n,
    durationMillis: 0n,
    active: false,
    ...overrides,
  };
}
