import { describe, expect, test } from "bun:test";
import {
  PublicTlsCertificateLeafState,
  PublicTlsCertificateSource,
  PublicTlsCertificateStatus,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  DAY_MILLIS,
  publicTlsAttentionReason,
  publicTlsExpiryStatus,
  publicTlsExpiryWindowMillis,
  publicTlsLeafNames,
  publicTlsLeafUnavailableReason,
  publicTlsNeedsRenewal,
  shortFingerprint,
  type PublicTlsLeafFields,
  type PublicTlsTimingFields,
} from "@/lib/publicTlsPresentation";

const NOW = Date.UTC(2027, 5, 1, 12);

function cert(
  lifetimeDays: number,
  remainingDays: number,
  overrides: Partial<PublicTlsTimingFields> = {},
): PublicTlsTimingFields {
  const expires = NOW + remainingDays * DAY_MILLIS;
  return {
    source: PublicTlsCertificateSource.ACME,
    enabled: true,
    status: PublicTlsCertificateStatus.READY,
    issuedAtUnixMillis: BigInt(Math.round(expires - lifetimeDays * DAY_MILLIS)),
    expiresAtUnixMillis: BigInt(Math.round(expires)),
    nextRenewalAtUnixMillis: 0n,
    ...overrides,
  };
}

const manual = { source: PublicTlsCertificateSource.MANUAL };

describe("publicTlsExpiryWindowMillis", () => {
  test("ACME uses a sixth of the lifetime capped at 14 days", () => {
    expect(publicTlsExpiryWindowMillis(cert(90, 60))).toBe(14 * DAY_MILLIS);
    expect(publicTlsExpiryWindowMillis(cert(45, 30))).toBe(7.5 * DAY_MILLIS);
    expect(publicTlsExpiryWindowMillis(cert(6, 5))).toBe(DAY_MILLIS);
  });

  test("manual uses a third of the lifetime capped at 30 days", () => {
    expect(publicTlsExpiryWindowMillis(cert(365, 200, manual))).toBe(30 * DAY_MILLIS);
    expect(publicTlsExpiryWindowMillis(cert(90, 60, manual))).toBe(30 * DAY_MILLIS);
    expect(publicTlsExpiryWindowMillis(cert(45, 30, manual))).toBe(15 * DAY_MILLIS);
  });

  test("unknown or inverted lifetime falls back to the cap", () => {
    expect(publicTlsExpiryWindowMillis(cert(45, 30, { issuedAtUnixMillis: 0n }))).toBe(14 * DAY_MILLIS);
    expect(publicTlsExpiryWindowMillis(cert(-1, 30, manual))).toBe(30 * DAY_MILLIS);
  });
});

describe("publicTlsAttentionReason for ACME", () => {
  test("a 90-day certificate is calm at 20 days left and flagged under 14", () => {
    expect(publicTlsAttentionReason(cert(90, 20), NOW)).toBe("");
    expect(publicTlsAttentionReason(cert(90, 13.9), NOW)).toBe("expiring");
  });

  test("a 45-day certificate inside the old 30-day window is calm", () => {
    expect(publicTlsAttentionReason(cert(45, 29), NOW)).toBe("");
    expect(publicTlsAttentionReason(cert(45, 8), NOW)).toBe("");
    expect(publicTlsAttentionReason(cert(45, 7), NOW)).toBe("expiring");
  });

  test("a 6-day certificate is only flagged in its last day", () => {
    expect(publicTlsAttentionReason(cert(6, 2), NOW)).toBe("");
    expect(publicTlsAttentionReason(cert(6, 0.5), NOW)).toBe("expiring");
    expect(publicTlsNeedsRenewal(cert(6, 2), NOW)).toBe(false);
  });

  test("errors always need attention", () => {
    expect(publicTlsAttentionReason(cert(90, 80, { status: PublicTlsCertificateStatus.ERROR }), NOW)).toBe("error");
  });

  test("renewal is overdue only after a 24 hour grace period", () => {
    const scheduled = (hoursAgo: number) => cert(45, 20, { nextRenewalAtUnixMillis: BigInt(NOW - hoursAgo * 3_600_000) });
    expect(publicTlsAttentionReason(scheduled(23), NOW)).toBe("");
    expect(publicTlsAttentionReason(scheduled(25), NOW)).toBe("overdue");
    expect(publicTlsNeedsRenewal(scheduled(25), NOW)).toBe(true);
  });

  test("expired certificates need attention", () => {
    expect(publicTlsAttentionReason(cert(90, -1), NOW)).toBe("expired");
  });

  test("disabled mappings and unknown expiry stay calm", () => {
    expect(publicTlsAttentionReason(cert(90, 1, { enabled: false }), NOW)).toBe("");
    expect(publicTlsAttentionReason(cert(90, 1, { expiresAtUnixMillis: 0n }), NOW)).toBe("");
  });
});

describe("publicTlsAttentionReason for manual certificates", () => {
  test("warns with lead time but never offers ACME renewal", () => {
    expect(publicTlsAttentionReason(cert(365, 31, manual), NOW)).toBe("");
    expect(publicTlsAttentionReason(cert(365, 29, manual), NOW)).toBe("expiring");
    expect(publicTlsNeedsRenewal(cert(365, 29, manual), NOW)).toBe(false);
  });

  test("short manual certificates warn at a third of their lifetime", () => {
    expect(publicTlsAttentionReason(cert(30, 11, manual), NOW)).toBe("");
    expect(publicTlsAttentionReason(cert(30, 9, manual), NOW)).toBe("expiring");
  });

  test("ignores renewal and error state", () => {
    expect(publicTlsAttentionReason(cert(365, 200, { ...manual, status: PublicTlsCertificateStatus.ERROR, nextRenewalAtUnixMillis: BigInt(NOW - 10 * DAY_MILLIS) }), NOW)).toBe("");
  });
});

describe("publicTlsExpiryStatus", () => {
  test("labels days left and colours by the certificate's window", () => {
    expect(publicTlsExpiryStatus(cert(45, 20), NOW)).toEqual({ label: "20 days left", type: "success" });
    expect(publicTlsExpiryStatus(cert(45, 5), NOW)).toEqual({ label: "5 days left", type: "warning" });
    expect(publicTlsExpiryStatus(cert(365, 20, manual), NOW)).toEqual({ label: "20 days left", type: "warning" });
    expect(publicTlsExpiryStatus(cert(6, 0.4), NOW)).toEqual({ label: "1 day left", type: "warning" });
    expect(publicTlsExpiryStatus(cert(90, -2), NOW)).toEqual({ label: "Expired", type: "error" });
    expect(publicTlsExpiryStatus(cert(90, 10, { expiresAtUnixMillis: 0n }), NOW)).toEqual({ label: "Expiry unknown", type: "default" });
  });
});

describe("leaf facts", () => {
  const leaf = (overrides: Partial<PublicTlsLeafFields>): PublicTlsLeafFields => ({
    source: PublicTlsCertificateSource.ACME,
    leafState: PublicTlsCertificateLeafState.AVAILABLE,
    leafDnsNames: [],
    leafIpAddresses: [],
    ...overrides,
  });

  test("explains missing facts per state and source", () => {
    expect(publicTlsLeafUnavailableReason(leaf({}))).toBe("");
    expect(publicTlsLeafUnavailableReason(leaf({ leafState: PublicTlsCertificateLeafState.MISSING }))).toBe("Not issued yet");
    expect(publicTlsLeafUnavailableReason(leaf({ leafState: PublicTlsCertificateLeafState.MISSING, source: PublicTlsCertificateSource.MANUAL }))).toBe("Certificate file not found");
    expect(publicTlsLeafUnavailableReason(leaf({ leafState: PublicTlsCertificateLeafState.UNREADABLE }))).toBe("Certificate file could not be read");
    expect(publicTlsLeafUnavailableReason(leaf({ leafState: PublicTlsCertificateLeafState.INVALID }))).toBe("Certificate file is not a valid certificate");
    expect(publicTlsLeafUnavailableReason(leaf({ leafState: PublicTlsCertificateLeafState.UNSPECIFIED }))).toBe("Certificate details unavailable");
  });

  test("lists every name without truncation", () => {
    const names = Array.from({ length: 40 }, (_, index) => `host-${index}.example.com`);
    expect(publicTlsLeafNames(leaf({ leafDnsNames: names, leafIpAddresses: ["192.0.2.1"] }))).toEqual([...names, "192.0.2.1"]);
  });

  test("shortens long fingerprints only", () => {
    const sha = "0123456789abcdef".repeat(4);
    expect(shortFingerprint(sha)).toBe("0123456789ab…89abcdef");
    expect(shortFingerprint("abc")).toBe("abc");
    expect(shortFingerprint("")).toBe("");
  });
});
