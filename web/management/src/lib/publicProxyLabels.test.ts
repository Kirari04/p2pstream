import { describe, expect, test } from "bun:test";
import {
  PublicAcmeChallengeType,
  PublicAcmeCa,
  PublicTlsCertificateSource,
  PublicTlsCertificateStatus,
  type PublicTlsCertificate,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  formatUnixMillis,
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
