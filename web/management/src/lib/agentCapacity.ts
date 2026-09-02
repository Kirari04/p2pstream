export type AgentCapacityState = "offline" | "unreported" | "aligned" | "server_capped" | "pressured" | "degraded";

export interface AgentCapacityInput {
  connected: boolean;
  activeRequests: bigint;
  advertisedMaxConcurrentStreams: bigint;
  negotiatedMaxConcurrentStreams: bigint;
  tunnelCapacityAdaptive: boolean;
  currentTunnelAdmissionLimit: bigint;
  latestStats?: {
    memorySysMb: bigint;
    tunnelCapacityAdaptive?: boolean;
    tunnelStreamsInUse: bigint;
    tunnelAdmissionLimit: bigint;
    memoryPressure: string;
    memoryUsageBytes: bigint;
    memoryLimitBytes: bigint;
    memorySource: string;
    fileDescriptorsUsed?: bigint;
    fileDescriptorsLimit?: bigint;
    resourcePressureReason?: string;
    resourceSampleError?: string;
    resourceLastGoodUnixMillis?: bigint;
  };
}

export interface AgentCapacitySummary {
  state: AgentCapacityState;
  adaptive: boolean;
  active: bigint;
  advertised: bigint;
  negotiated: bigint;
  admissionLimit: bigint;
  headroom: bigint;
  utilizationPercent: number;
  pressure: string;
  memorySysMb?: bigint;
  memoryUsageBytes?: bigint;
  memoryLimitBytes?: bigint;
  memoryPercent?: number;
  memorySource: string;
  fileDescriptorsUsed?: bigint;
  fileDescriptorsLimit?: bigint;
  fileDescriptorsPercent?: number;
  pressureReason: string;
  sensorDegraded: boolean;
  lastGoodUnixMillis?: bigint;
}

export function summarizeAgentCapacity(agent: AgentCapacityInput): AgentCapacitySummary {
  const resourceStats = !agent.tunnelCapacityAdaptive || agent.latestStats?.tunnelCapacityAdaptive === true
    ? agent.latestStats
    : undefined;
  const statsActive = positive(resourceStats?.tunnelStreamsInUse ?? 0n);
  const active = statsActive > 0n ? statsActive : positive(agent.activeRequests);
  const advertised = positive(agent.advertisedMaxConcurrentStreams);
  const negotiated = positive(agent.negotiatedMaxConcurrentStreams);
  const reportedLimit = positive(resourceStats?.tunnelAdmissionLimit ?? agent.currentTunnelAdmissionLimit);
  const admissionLimit = agent.tunnelCapacityAdaptive
    ? reportedLimit
    : negotiated;
  const headroom = admissionLimit > active ? admissionLimit - active : 0n;
  const utilizationPercent = admissionLimit > 0n
    ? Math.min(100, Number((active * 100n) / admissionLimit))
    : active > 0n ? 100 : 0;
  const pressure = normalizedPressure(resourceStats?.memoryPressure);

  let state: AgentCapacityState = "aligned";
  if (!agent.connected) {
    state = "offline";
  } else if (negotiated === 0n) {
    state = "unreported";
  } else if (pressure === "soft" || pressure === "critical") {
    state = "pressured";
  } else if (agent.tunnelCapacityAdaptive && pressure === "unknown") {
    state = "degraded";
  } else if (!agent.tunnelCapacityAdaptive && advertised > negotiated) {
    state = "server_capped";
  }

  const memory = resourceStats?.memorySysMb;
  const memoryUsageBytes = positiveOptional(resourceStats?.memoryUsageBytes);
  const memoryLimitBytes = positiveOptional(resourceStats?.memoryLimitBytes);
  const memoryPercent = memoryUsageBytes !== undefined && memoryLimitBytes !== undefined && memoryLimitBytes > 0n
    ? Math.min(100, Number((memoryUsageBytes * 1000n) / memoryLimitBytes) / 10)
    : undefined;
  const fileDescriptorsUsed = positiveOptional(resourceStats?.fileDescriptorsUsed);
  const fileDescriptorsLimit = positiveOptional(resourceStats?.fileDescriptorsLimit);
  const fileDescriptorsPercent = fileDescriptorsUsed !== undefined && fileDescriptorsLimit !== undefined && fileDescriptorsLimit > 0n
    ? Math.min(100, Number((fileDescriptorsUsed * 1000n) / fileDescriptorsLimit) / 10)
    : undefined;
  const lastGoodUnixMillis = positiveOptional(resourceStats?.resourceLastGoodUnixMillis);
  return {
    state,
    adaptive: agent.tunnelCapacityAdaptive,
    active,
    advertised,
    negotiated,
    admissionLimit,
    headroom,
    utilizationPercent,
    pressure,
    memorySysMb: memory !== undefined && memory >= 0n ? memory : undefined,
    memoryUsageBytes,
    memoryLimitBytes,
    memoryPercent,
    memorySource: resourceStats?.memorySource ?? "",
    fileDescriptorsUsed,
    fileDescriptorsLimit,
    fileDescriptorsPercent,
    pressureReason: resourceStats?.resourcePressureReason ?? "",
    sensorDegraded: (resourceStats?.resourceSampleError ?? "") !== "" || (agent.tunnelCapacityAdaptive && pressure === "unknown"),
    lastGoodUnixMillis,
  };
}

function normalizedPressure(value: string | undefined): string {
  switch (value?.trim().toLowerCase()) {
    case "healthy":
    case "soft":
    case "critical":
      return value.trim().toLowerCase();
    default:
      return "unknown";
  }
}

function positive(value: bigint): bigint {
  return value > 0n ? value : 0n;
}

function positiveOptional(value: bigint | undefined): bigint | undefined {
  return value !== undefined && value >= 0n ? value : undefined;
}
