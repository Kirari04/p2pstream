import type {
  DashboardProxyDimensionSummary,
  DashboardTrafficBucket,
  DashboardWindowSummary,
  GetDashboardResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

export const DASHBOARD_TREND_BUCKETS = 12;
export const DASHBOARD_TREND_BUCKET_MS = 5 * 60 * 1000;

export type DashboardTrafficBucketView = {
  bucketUnixMillis: bigint;
  requests: bigint;
  success: bigint;
  clientError: bigint;
  serverError: bigint;
  internalError: bigint;
  requestBytes: bigint;
  responseBytes: bigint;
  avgDurationMs: bigint;
  errors: bigint;
  totalBytes: bigint;
};

export function windowByLabel(
  dashboard: GetDashboardResponse | null | undefined,
  label: string,
): DashboardWindowSummary | null {
  return dashboard?.windows.find((window) => window.label === label) ?? null;
}

export function statusClassCounts(statusClasses: DashboardProxyDimensionSummary[] | undefined): Record<string, bigint> {
  const counts: Record<string, bigint> = {
    "2xx": 0n,
    "3xx": 0n,
    "4xx": 0n,
    "5xx": 0n,
  };
  for (const item of statusClasses ?? []) {
    if (item.label in counts) {
      counts[item.label] = item.requests;
    }
  }
  return counts;
}

export function successRate(window: DashboardWindowSummary | null | undefined): number {
  if (!window || window.proxyRequests === 0n) return 0;
  return ratio(window.proxySuccess, window.proxyRequests);
}

export function errorRate(window: DashboardWindowSummary | null | undefined): number {
  if (!window || window.proxyRequests === 0n) return 0;
  const errors = window.proxyClientError + window.proxyServerError + window.proxyInternalError;
  return ratio(errors, window.proxyRequests);
}

export function requestsPerSecond(window: DashboardWindowSummary | null | undefined, nowMillis: bigint | number): number | null {
  if (!window) return null;
  const seconds = elapsedSeconds(window, nowMillis);
  if (seconds <= 0) return null;
  return toSafeNumber(window.proxyRequests) / seconds;
}

export function bytesPerSecond(
  bytes: bigint | number | null | undefined,
  window: DashboardWindowSummary | null | undefined,
  nowMillis: bigint | number,
): number | null {
  if (bytes === null || bytes === undefined || !window) return null;
  const seconds = elapsedSeconds(window, nowMillis);
  if (seconds <= 0) return null;
  return toSafeNumber(bytes) / seconds;
}

export function formatBytes(value: bigint | number | null | undefined): string {
  if (value === null || value === undefined) return "-";
  const bytes = toSafeNumber(value);
  if (bytes < 1024) return `${Math.round(bytes).toString()} B`;
  const units = ["KiB", "MiB", "GiB", "TiB", "PiB"];
  let scaled = bytes / 1024;
  let unitIndex = 0;
  while (scaled >= 1024 && unitIndex < units.length - 1) {
    scaled /= 1024;
    unitIndex += 1;
  }
  return `${scaled >= 10 ? scaled.toFixed(0) : scaled.toFixed(1)} ${units[unitIndex]}`;
}

export function formatByteRate(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "-";
  return `${formatBytes(value)}/s`;
}

export function formatDuration(value: bigint | number | null | undefined): string {
  if (value === null || value === undefined) return "-";
  const millis = toSafeNumber(value);
  if (millis <= 0) return "-";
  if (millis < 1000) return `${Math.round(millis).toString()} ms`;
  if (millis < 60_000) return `${(millis / 1000).toFixed(1)} s`;
  return `${(millis / 60_000).toFixed(1)} min`;
}

export function formatPercent(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "-";
  return `${(value * 100).toFixed(value > 0 && value < 0.1 ? 1 : 0)}%`;
}

export function formatNumber(value: bigint | number | null | undefined): string {
  if (value === null || value === undefined) return "0";
  if (typeof value === "bigint") {
    return groupIntegerString(value.toString());
  }
  if (!Number.isFinite(value)) return "0";
  return new Intl.NumberFormat().format(Math.round(value));
}

export function filledTrafficBuckets(
  buckets: DashboardTrafficBucket[] | undefined,
  nowMillis: bigint | number,
): DashboardTrafficBucketView[] {
  const now = toSafeNumber(nowMillis);
  const endBucket = Math.floor(now / DASHBOARD_TREND_BUCKET_MS) * DASHBOARD_TREND_BUCKET_MS;
  const byBucket = new Map<string, DashboardTrafficBucket>();
  for (const bucket of buckets ?? []) {
    byBucket.set(bucket.bucketUnixMillis.toString(), bucket);
  }

  const filled: DashboardTrafficBucketView[] = [];
  for (let index = DASHBOARD_TREND_BUCKETS - 1; index >= 0; index -= 1) {
    const bucketUnixMillis = BigInt(endBucket - index * DASHBOARD_TREND_BUCKET_MS);
    const existing = byBucket.get(bucketUnixMillis.toString());
    const requests = existing?.requests ?? 0n;
    const success = existing?.success ?? 0n;
    const clientError = existing?.clientError ?? 0n;
    const serverError = existing?.serverError ?? 0n;
    const internalError = existing?.internalError ?? 0n;
    const requestBytes = existing?.requestBytes ?? 0n;
    const responseBytes = existing?.responseBytes ?? 0n;
    filled.push({
      bucketUnixMillis,
      requests,
      success,
      clientError,
      serverError,
      internalError,
      requestBytes,
      responseBytes,
      avgDurationMs: existing?.avgDurationMs ?? 0n,
      errors: clientError + serverError + internalError,
      totalBytes: requestBytes + responseBytes,
    });
  }
  return filled;
}

function elapsedSeconds(window: DashboardWindowSummary, nowMillis: bigint | number): number {
  const now = toSafeNumber(nowMillis);
  const since = toSafeNumber(window.sinceUnixMillis);
  return Math.max(0, (now - since) / 1000);
}

function ratio(numerator: bigint, denominator: bigint): number {
  if (denominator === 0n) return 0;
  return toSafeNumber(numerator) / Math.max(1, toSafeNumber(denominator));
}

function toSafeNumber(value: bigint | number): number {
  if (typeof value === "number") {
    if (!Number.isFinite(value)) return 0;
    return Math.max(0, Math.min(value, Number.MAX_SAFE_INTEGER));
  }
  if (value <= 0n) return 0;
  const max = BigInt(Number.MAX_SAFE_INTEGER);
  return Number(value > max ? max : value);
}

function groupIntegerString(value: string): string {
  const negative = value.startsWith("-");
  const digits = negative ? value.slice(1) : value;
  const grouped = digits.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return negative ? `-${grouped}` : grouped;
}
