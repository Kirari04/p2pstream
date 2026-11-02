import {
  PublicTlsCertificateLeafState,
  PublicTlsCertificateSource,
  PublicTlsCertificateStatus,
  type PublicTlsCertificate,
} from "@/gen/proto/p2pstream/v1/management_pb";

export const DAY_MILLIS = 86_400_000;

/** ACME: flag once less than this share of the lifetime remains. */
export const ACME_ATTENTION_LIFETIME_FRACTION = 1 / 6;
/** ACME: the remaining-lifetime attention window never exceeds this. */
export const ACME_ATTENTION_MAX_MILLIS = 14 * DAY_MILLIS;
/** ACME: a scheduled renewal this far in the past counts as overdue. */
export const ACME_RENEWAL_OVERDUE_GRACE_MILLIS = DAY_MILLIS;
/** Manual: warn once less than this share of the lifetime remains. */
export const MANUAL_EXPIRY_LIFETIME_FRACTION = 1 / 3;
/** Manual: the expiry warning window never exceeds this. */
export const MANUAL_EXPIRY_MAX_MILLIS = 30 * DAY_MILLIS;

export type PublicTlsTimingFields = Pick<
  PublicTlsCertificate,
  "source" | "enabled" | "status" | "issuedAtUnixMillis" | "expiresAtUnixMillis" | "nextRenewalAtUnixMillis"
>;

export type PublicTlsAttentionReason = "" | "error" | "overdue" | "expired" | "expiring";

function isAcme(cert: Pick<PublicTlsCertificate, "source">): boolean {
  return cert.source === PublicTlsCertificateSource.ACME;
}

function lifetimeMillis(cert: PublicTlsTimingFields): number {
  if (cert.issuedAtUnixMillis === 0n || cert.expiresAtUnixMillis === 0n) return 0;
  const lifetime = Number(cert.expiresAtUnixMillis - cert.issuedAtUnixMillis);
  return lifetime > 0 ? lifetime : 0;
}

/**
 * How much remaining validity triggers an expiry warning for this certificate.
 * ACME: min(14 days, lifetime / 6), because the server already renews at
 * min(30 days, lifetime / 3) and only a stalled renewal should get attention.
 * Manual: min(30 days, lifetime / 3), leaving operators lead time to replace
 * the file themselves. An unknown issue date falls back to the cap.
 */
export function publicTlsExpiryWindowMillis(cert: PublicTlsTimingFields): number {
  const acme = isAcme(cert);
  const cap = acme ? ACME_ATTENTION_MAX_MILLIS : MANUAL_EXPIRY_MAX_MILLIS;
  const fraction = acme ? ACME_ATTENTION_LIFETIME_FRACTION : MANUAL_EXPIRY_LIFETIME_FRACTION;
  const lifetime = lifetimeMillis(cert);
  if (!lifetime) return cap;
  return Math.min(cap, lifetime * fraction);
}

/** Why the certificate needs operator attention, or "" when it does not. Disabled mappings never do. */
export function publicTlsAttentionReason(cert: PublicTlsTimingFields, nowMillis = Date.now()): PublicTlsAttentionReason {
  if (!cert.enabled) return "";
  if (isAcme(cert)) {
    if (cert.status === PublicTlsCertificateStatus.ERROR) return "error";
    if (cert.nextRenewalAtUnixMillis !== 0n && nowMillis > Number(cert.nextRenewalAtUnixMillis) + ACME_RENEWAL_OVERDUE_GRACE_MILLIS) {
      return "overdue";
    }
  }
  if (cert.expiresAtUnixMillis === 0n) return "";
  const remaining = Number(cert.expiresAtUnixMillis) - nowMillis;
  if (remaining <= 0) return "expired";
  if (remaining < publicTlsExpiryWindowMillis(cert)) return "expiring";
  return "";
}

/** Whether to offer the inline Renew action: enabled ACME mappings that need attention. */
export function publicTlsNeedsRenewal(cert: PublicTlsTimingFields, nowMillis = Date.now()): boolean {
  return isAcme(cert) && publicTlsAttentionReason(cert, nowMillis) !== "";
}

export interface PublicTlsExpiryStatus {
  label: string;
  type: "default" | "success" | "warning" | "error";
}

/** Expiry tag for the table: days left, coloured by the certificate's own warning window. */
export function publicTlsExpiryStatus(cert: PublicTlsTimingFields, nowMillis = Date.now()): PublicTlsExpiryStatus {
  if (cert.expiresAtUnixMillis === 0n) return { label: "Expiry unknown", type: "default" };
  const remaining = Number(cert.expiresAtUnixMillis) - nowMillis;
  if (remaining <= 0) return { label: "Expired", type: "error" };
  const days = Math.ceil(remaining / DAY_MILLIS);
  const label = `${days.toLocaleString("en-US")} day${days === 1 ? "" : "s"} left`;
  return { label, type: remaining < publicTlsExpiryWindowMillis(cert) ? "warning" : "success" };
}

/** Short sentence-case explanation for the attention reason, for titles and screen readers. */
export function publicTlsAttentionText(reason: PublicTlsAttentionReason): string {
  switch (reason) {
    case "error": return "Last renewal failed";
    case "overdue": return "Renewal is overdue";
    case "expired": return "Certificate has expired";
    case "expiring": return "Expires soon";
    default: return "";
  }
}

export type PublicTlsLeafFields = Pick<PublicTlsCertificate, "source" | "leafState" | "leafDnsNames" | "leafIpAddresses">;

/** Why leaf certificate facts are unavailable, or "" when they are present. */
export function publicTlsLeafUnavailableReason(cert: PublicTlsLeafFields): string {
  switch (cert.leafState) {
    case PublicTlsCertificateLeafState.AVAILABLE: return "";
    case PublicTlsCertificateLeafState.MISSING:
      return isAcme(cert) ? "Not issued yet" : "Certificate file not found";
    case PublicTlsCertificateLeafState.UNREADABLE: return "Certificate file could not be read";
    case PublicTlsCertificateLeafState.INVALID: return "Certificate file is not a valid certificate";
    default: return "Certificate details unavailable";
  }
}

/** Every subject alternative name on the leaf: DNS names first, then IP addresses. Never truncated. */
export function publicTlsLeafNames(cert: PublicTlsLeafFields): string[] {
  return [...cert.leafDnsNames, ...cert.leafIpAddresses];
}

/** Shortened fingerprint for display; the full value stays available for copy and hover. */
export function shortFingerprint(sha256: string): string {
  if (sha256.length <= 24) return sha256;
  return `${sha256.slice(0, 12)}…${sha256.slice(-8)}`;
}
