import { describe, expect, test } from "bun:test";
import {
  PublicBackendForwardMode,
  PublicBackendHealthStatus,
  PublicBackendHealthTraceOutcome,
  PublicBackendHealthTraceSource,
  PublicBackendLoadBalancing,
  PublicBackendType,
  PublicAcmeChallengeType,
  PublicAcmeCa,
  PublicTlsCertificateSource,
  PublicTlsCertificateStatus,
  type Agent,
  type PublicBackend,
  type PublicBackendAgent,
  type PublicBackendHealthTrace,
  type PublicTlsCertificate,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  backendAgentAvailabilitySummary,
  backendAgentHealthLabel,
  backendAgentSummary,
  backendHealthLabel,
  backendHealthSeverity,
  formatUnixMillis,
  healthTraceOutcomeLabel,
  healthTraceOutcomeSeverity,
  healthTraceReasonSummary,
  healthTraceSourceLabel,
  healthTraceTargetLabel,
  healthTraceTransitionSummary,
  tlsCertificateRenewalSummary,
  tlsCertificateValiditySummary,
} from "@/lib/publicProxyLabels";

describe("publicProxyLabels", () => {
  test("formats certificate validity from and until", () => {
    const cert = tlsCertificate({
      issuedAtUnixMillis: BigInt(Date.UTC(2026, 4, 15)),
      expiresAtUnixMillis: BigInt(Date.UTC(2036, 4, 13)),
    });

    expect(tlsCertificateValiditySummary(cert)).toBe(
      `Valid ${formatUnixMillis(cert.issuedAtUnixMillis)} - ${formatUnixMillis(cert.expiresAtUnixMillis)}`,
    );
  });

  test("formats certificate validity with only expiry", () => {
    const cert = tlsCertificate({
      expiresAtUnixMillis: BigInt(Date.UTC(2027, 0, 1)),
    });

    expect(tlsCertificateValiditySummary(cert)).toBe(`Valid until ${formatUnixMillis(cert.expiresAtUnixMillis)}`);
  });

  test("formats ACME renewal separately from validity", () => {
    const cert = tlsCertificate({
      source: PublicTlsCertificateSource.ACME,
      nextRenewalAtUnixMillis: BigInt(Date.UTC(2026, 11, 1)),
    });

    expect(tlsCertificateRenewalSummary(cert)).toBe(`Renews ${formatUnixMillis(cert.nextRenewalAtUnixMillis)}`);
    expect(tlsCertificateRenewalSummary(tlsCertificate())).toBe("");
  });

  test("summarizes agent-pool availability", () => {
    const backend = publicBackend({
      agentAssignments: [
        backendAgent(1n, PublicBackendHealthStatus.HEALTHY, true),
        backendAgent(2n, PublicBackendHealthStatus.DISCONNECTED, false),
        backendAgent(3n, PublicBackendHealthStatus.UNHEALTHY, false),
      ],
    });

    expect(backendAgentAvailabilitySummary(backend, [])).toBe("1/3 agents available");
    expect(backendHealthLabel(backend)).toBe("1/3 agents available");
    expect(backendHealthSeverity(backend)).toBe("warn");
  });

  test("formats per-agent health in agent summaries", () => {
    const backend = publicBackend({
      agentAssignments: [
        backendAgent(1n, PublicBackendHealthStatus.HEALTHY, true),
        backendAgent(2n, PublicBackendHealthStatus.DISCONNECTED, false),
        backendAgent(3n, PublicBackendHealthStatus.UNHEALTHY, false),
      ],
    });
    const agents = [agent(1n, "agent-a"), agent(2n, "agent-b"), agent(3n, "agent-c")];

    expect(backendAgentHealthLabel(backend.agentAssignments[0])).toBe("healthy");
    expect(backendAgentHealthLabel(backend.agentAssignments[1])).toBe("disconnected");
    expect(backendAgentSummary(backend, [], agents)).toBe(
      "agent-a (agent-a) healthy x100, agent-b (agent-b) disconnected x100, agent-c (agent-c) unhealthy x100",
    );
  });

  test("formats health trace labels and summaries", () => {
    const trace = healthTrace({
      outcome: PublicBackendHealthTraceOutcome.FAILURE,
      source: PublicBackendHealthTraceSource.ACTIVE_CHECK,
      agentId: 2n,
      agentPublicId: "agent-b",
      statusCode: 503n,
      expectedStatusMin: 200n,
      expectedStatusMax: 399n,
      statusBefore: PublicBackendHealthStatus.HEALTHY,
      statusAfter: PublicBackendHealthStatus.UNHEALTHY,
      availableAfter: false,
      errorKind: "unexpected_status",
    });

    expect(healthTraceOutcomeLabel(trace.outcome)).toBe("Failed");
    expect(healthTraceOutcomeSeverity(trace.outcome)).toBe("danger");
    expect(healthTraceSourceLabel(trace.source)).toBe("Active check");
    expect(healthTraceReasonSummary(trace)).toBe("HTTP 503 outside 200-399");
    expect(healthTraceTransitionSummary(trace)).toBe("Healthy -> Unhealthy / unavailable");
    expect(healthTraceTargetLabel(trace, [agent(2n, "agent-b")])).toBe("agent-b (agent-b)");
  });
});

function tlsCertificate(overrides: Partial<PublicTlsCertificate> = {}): PublicTlsCertificate {
  return {
    $typeName: "p2pstream.v1.PublicTlsCertificate",
    id: 1n,
    listenerId: 1n,
    hostnamePattern: "app.example.com",
    certPath: "",
    keyPath: "",
    enabled: true,
    createdAtUnixMillis: 0n,
    updatedAtUnixMillis: 0n,
    source: PublicTlsCertificateSource.MANUAL,
    acmeChallengeType: PublicAcmeChallengeType.UNSPECIFIED,
    acmeCa: PublicAcmeCa.UNSPECIFIED,
    acmeEmail: "",
    dnsCredentialId: 0n,
    status: PublicTlsCertificateStatus.READY,
    lastError: "",
    issuedAtUnixMillis: 0n,
    expiresAtUnixMillis: 0n,
    nextRenewalAtUnixMillis: 0n,
    lastRenewalAttemptAtUnixMillis: 0n,
    ...overrides,
  };
}

function publicBackend(overrides: Partial<PublicBackend> = {}): PublicBackend {
  return {
    $typeName: "p2pstream.v1.PublicBackend",
    id: 1n,
    name: "backend",
    targetOrigin: "http://example.test",
    enabled: true,
    createdAtUnixMillis: 0n,
    updatedAtUnixMillis: 0n,
    backendType: PublicBackendType.PROXY_FORWARD,
    tlsSkipVerify: false,
    staticStatusCode: 200n,
    staticResponseHeaders: [],
    staticResponseBody: "",
    forwardMode: PublicBackendForwardMode.AGENT_POOL,
    loadBalancing: PublicBackendLoadBalancing.ROUND_ROBIN,
    agentAssignments: [],
    upstreamRequestHeaders: [],
    upstreamBasicAuth: undefined,
    healthCheck: undefined,
    upstreamResponseHeaderTimeoutMillis: 0n,
    ...overrides,
  };
}

function backendAgent(agentId: bigint, status: PublicBackendHealthStatus, available: boolean): PublicBackendAgent {
  return {
    $typeName: "p2pstream.v1.PublicBackendAgent",
    backendId: 1n,
    agentId,
    position: agentId - 1n,
    weight: 100n,
    enabled: true,
    health: {
      $typeName: "p2pstream.v1.PublicBackendAgentHealth",
      status,
      connected: status !== PublicBackendHealthStatus.DISCONNECTED,
      available,
      lastCheckedAtUnixMillis: 0n,
      lastError: "",
      passiveUnhealthyUntilUnixMillis: 0n,
      activeRequests: 0n,
    },
  };
}

function agent(id: bigint, publicId: string): Agent {
  return {
    $typeName: "p2pstream.v1.Agent",
    id,
    publicId,
    name: publicId,
    enabled: true,
    connected: true,
    activeRequests: 0n,
    createdAtUnixMillis: 0n,
    updatedAtUnixMillis: 0n,
    lastConnectedAtUnixMillis: 0n,
    lastDisconnectedAtUnixMillis: 0n,
    latestStats: undefined,
  };
}

function healthTrace(overrides: Partial<PublicBackendHealthTrace> = {}): PublicBackendHealthTrace {
  return {
    $typeName: "p2pstream.v1.PublicBackendHealthTrace",
    sequence: 1n,
    backendId: 1n,
    backendName: "backend",
    forwardMode: PublicBackendForwardMode.DIRECT,
    source: PublicBackendHealthTraceSource.ACTIVE_CHECK,
    outcome: PublicBackendHealthTraceOutcome.SUCCESS,
    agentId: 0n,
    agentPublicId: "",
    agentName: "",
    startedAtUnixMillis: 0n,
    finishedAtUnixMillis: 0n,
    durationMillis: 0n,
    method: "GET",
    url: "http://example.test/health",
    statusCode: 200n,
    expectedStatusMin: 200n,
    expectedStatusMax: 399n,
    timeoutMillis: 2000n,
    tlsSkipVerify: false,
    statusBefore: PublicBackendHealthStatus.UNKNOWN,
    statusAfter: PublicBackendHealthStatus.HEALTHY,
    availableBefore: true,
    availableAfter: true,
    healthyStreakBefore: 0n,
    healthyStreakAfter: 1n,
    unhealthyStreakBefore: 0n,
    unhealthyStreakAfter: 0n,
    passiveUnhealthyUntilUnixMillis: 0n,
    errorKind: "success",
    error: "",
    debugAttributes: {},
    ...overrides,
  };
}
