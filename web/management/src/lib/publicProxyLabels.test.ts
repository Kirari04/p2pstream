import { describe, expect, test } from "bun:test";
import {
  PublicBackendForwardMode,
  PublicBackendHealthStatus,
  PublicBackendHealthTraceOutcome,
  PublicBackendHealthTraceSource,
  PublicAcmeChallengeType,
  PublicAcmeCa,
  PublicTlsCertificateSource,
  PublicTlsCertificateStatus,
  type Agent,
  type PublicBackendHealthTrace,
  type PublicTlsCertificate,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
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
    labels: {},
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
