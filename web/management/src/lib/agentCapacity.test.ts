import { describe, expect, test } from "bun:test";
import { summarizeAgentCapacity, type AgentCapacityInput } from "./agentCapacity";

function capacityInput(overrides: Partial<AgentCapacityInput> = {}): AgentCapacityInput {
  return {
    connected: true,
    activeRequests: 0n,
    advertisedMaxConcurrentStreams: 256n,
    negotiatedMaxConcurrentStreams: 256n,
    tunnelCapacityAdaptive: false,
    currentTunnelAdmissionLimit: 256n,
    ...overrides,
  };
}

describe("summarizeAgentCapacity", () => {
  test("shows fixed negotiated headroom and process memory", () => {
    expect(summarizeAgentCapacity(capacityInput({
      activeRequests: 83n,
      latestStats: {
        memorySysMb: 64n,
        tunnelCapacityAdaptive: false,
        tunnelStreamsInUse: 0n,
        tunnelAdmissionLimit: 0n,
        memoryPressure: "",
        memoryUsageBytes: 0n,
        memoryLimitBytes: 0n,
        memorySource: "",
      },
    }))).toMatchObject({
      state: "aligned",
      adaptive: false,
      active: 83n,
      advertised: 256n,
      negotiated: 256n,
      admissionLimit: 256n,
      headroom: 173n,
      utilizationPercent: 32,
      memorySysMb: 64n,
    });
  });

  test("reports adaptive headroom from the live resource allowance", () => {
    const summary = summarizeAgentCapacity(capacityInput({
      advertisedMaxConcurrentStreams: 2048n,
      negotiatedMaxConcurrentStreams: 2048n,
      tunnelCapacityAdaptive: true,
      currentTunnelAdmissionLimit: 2048n,
      latestStats: {
        memorySysMb: 64n,
        tunnelCapacityAdaptive: true,
        tunnelStreamsInUse: 125n,
        tunnelAdmissionLimit: 2048n,
        memoryPressure: "healthy",
        memoryUsageBytes: 64n * 1024n * 1024n,
        memoryLimitBytes: 512n * 1024n * 1024n,
        memorySource: "cgroup_v2",
        fileDescriptorsUsed: 120n,
        fileDescriptorsLimit: 1024n,
        resourcePressureReason: "memory",
        resourceSampleError: "",
        resourceLastGoodUnixMillis: 1_700_000_000_000n,
      },
    }));

    expect(summary).toMatchObject({
      state: "aligned",
      adaptive: true,
      active: 125n,
      admissionLimit: 2048n,
      headroom: 1923n,
      pressure: "healthy",
      memoryPercent: 12.5,
      memorySource: "cgroup_v2",
      fileDescriptorsPercent: 11.7,
    });
  });

  test("calls out pressure and the reduced live allowance", () => {
    const summary = summarizeAgentCapacity(capacityInput({
      advertisedMaxConcurrentStreams: 2048n,
      negotiatedMaxConcurrentStreams: 2048n,
      tunnelCapacityAdaptive: true,
      currentTunnelAdmissionLimit: 300n,
      latestStats: {
        memorySysMb: 430n,
        tunnelCapacityAdaptive: true,
        tunnelStreamsInUse: 200n,
        tunnelAdmissionLimit: 300n,
        memoryPressure: "soft",
        memoryUsageBytes: 430n,
        memoryLimitBytes: 512n,
        memorySource: "host",
      },
    }));

    expect(summary.state).toBe("pressured");
    expect(summary.headroom).toBe(100n);
    expect(summary.utilizationPercent).toBe(66);
  });

  test("calls out a server-capped fixed negotiation", () => {
    const summary = summarizeAgentCapacity(capacityInput({
      activeRequests: 200n,
      advertisedMaxConcurrentStreams: 512n,
      negotiatedMaxConcurrentStreams: 256n,
    }));

    expect(summary.state).toBe("server_capped");
    expect(summary.headroom).toBe(56n);
    expect(summary.utilizationPercent).toBe(78);
  });

  test("distinguishes offline and legacy unreported agents", () => {
    expect(summarizeAgentCapacity(capacityInput({ connected: false })).state).toBe("offline");
    expect(summarizeAgentCapacity(capacityInput({
      negotiatedMaxConcurrentStreams: 0n,
      advertisedMaxConcurrentStreams: 0n,
      currentTunnelAdmissionLimit: 0n,
    })).state).toBe("unreported");
  });

  test("renders unknown adaptive sensing as degraded and preserves a zero allowance", () => {
    const summary = summarizeAgentCapacity(capacityInput({
      tunnelCapacityAdaptive: true,
      negotiatedMaxConcurrentStreams: 65_536n,
      currentTunnelAdmissionLimit: 999n,
      latestStats: {
        memorySysMb: 64n,
        tunnelCapacityAdaptive: true,
        tunnelStreamsInUse: 0n,
        tunnelAdmissionLimit: 0n,
        memoryPressure: "unknown",
        memoryUsageBytes: 0n,
        memoryLimitBytes: 0n,
        memorySource: "",
        resourcePressureReason: "sensor",
        resourceSampleError: "unavailable",
        resourceLastGoodUnixMillis: 0n,
      },
    }));

    expect(summary).toMatchObject({
      state: "degraded",
      admissionLimit: 0n,
      headroom: 0n,
      pressure: "unknown",
      pressureReason: "sensor",
      sensorDegraded: true,
    });
  });

  test("does not present a stale fixed-mode heartbeat as adaptive headroom", () => {
    const summary = summarizeAgentCapacity(capacityInput({
      tunnelCapacityAdaptive: true,
      negotiatedMaxConcurrentStreams: 65_536n,
      currentTunnelAdmissionLimit: 0n,
      latestStats: {
        memorySysMb: 64n,
        tunnelCapacityAdaptive: false,
        tunnelStreamsInUse: 12n,
        tunnelAdmissionLimit: 64n,
        memoryPressure: "healthy",
        memoryUsageBytes: 64n,
        memoryLimitBytes: 512n,
        memorySource: "host",
      },
    }));

    expect(summary).toMatchObject({ state: "degraded", admissionLimit: 0n, pressure: "unknown" });
  });
});
