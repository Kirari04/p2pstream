import { describe, expect, test } from "bun:test";
import type {
  DashboardProxyDimensionSummary,
  DashboardTrafficBucket,
  DashboardWindowSummary,
  GetDashboardResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  bytesPerSecond,
  filledTrafficBuckets,
  formatByteRate,
  formatBytes,
  formatDuration,
  formatNumber,
  formatPercent,
  statusClassCounts,
  successRate,
  errorRate,
  windowByLabel,
} from "@/lib/dashboardStats";
import { DashboardProxyDimension } from "@/gen/proto/p2pstream/v1/management_pb";

describe("dashboardStats", () => {
  test("finds windows by label", () => {
    const dashboard = dashboardResponse({ windows: [windowSummary({ label: "5m" }), windowSummary({ label: "1h" })] });

    expect(windowByLabel(dashboard, "1h")?.label).toBe("1h");
    expect(windowByLabel(dashboard, "24h")).toBeNull();
  });

  test("computes success and error rates", () => {
    const window = windowSummary({
      proxyRequests: 10n,
      proxySuccess: 7n,
      proxyClientError: 2n,
      proxyServerError: 1n,
    });

    expect(successRate(window)).toBe(0.7);
    expect(errorRate(window)).toBe(0.3);
    expect(successRate(windowSummary({ proxyRequests: 0n }))).toBe(0);
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
    expect(filled[10]?.errors).toBe(1n);
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
    topBackends: [],
    topRoutes: [],
    topAgents: [],
    topErrorKinds: [],
    statusClasses: [],
    trafficBuckets: [],
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
