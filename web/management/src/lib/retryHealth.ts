export type RetryWindowLabel = "5m" | "1h" | "24h" | "30d";

export type RetryHealthLike = Readonly<{
  matchedRequests: bigint;
  retriedRequests: bigint;
  recoveredRequests: bigint;
  exhaustedRequests: bigint;
}>;

export type RetryTrendBucketLike = Readonly<{
  bucketUnixMillis: bigint;
  matchedRequests: bigint;
  retriedRequests: bigint;
  retryAttempts: bigint;
  recoveredRequests: bigint;
  exhaustedRequests: bigint;
  skippedRequests: bigint;
}>;

export type RetryHealthSignal = Readonly<{
  label: "No matched traffic" | "Stable" | "Watch" | "Elevated" | "Action needed";
  tone: "neutral" | "success" | "watch" | "warning" | "danger";
}>;

const RETRY_WINDOW_CONFIG: Record<RetryWindowLabel, Readonly<{ bucketMs: number; buckets: number }>> = {
  "5m": { bucketMs: 60_000, buckets: 5 },
  "1h": { bucketMs: 5 * 60_000, buckets: 12 },
  "24h": { bucketMs: 60 * 60_000, buckets: 24 },
  "30d": { bucketMs: 24 * 60 * 60_000, buckets: 30 },
};

export function retryRate(retriedRequests: bigint, matchedRequests: bigint): number {
  return safeRatio(retriedRequests, matchedRequests);
}

export function retryRecoveryRate(summary: RetryHealthLike | null | undefined): number | null {
  if (!summary) return null;
  const completedRetryOutcomes = summary.recoveredRequests + summary.exhaustedRequests;
  if (completedRetryOutcomes <= 0n) return null;
  return safeRatio(summary.recoveredRequests, completedRetryOutcomes);
}

export function retryHealthSignal(summary: RetryHealthLike | null | undefined): RetryHealthSignal {
  if (!summary || summary.matchedRequests <= 0n) return { label: "No matched traffic", tone: "neutral" };
  if (summary.exhaustedRequests > 0n) return { label: "Action needed", tone: "danger" };

  const triggerRate = retryRate(summary.retriedRequests, summary.matchedRequests);
  if (triggerRate >= 0.05) return { label: "Elevated", tone: "warning" };
  if (triggerRate >= 0.01) return { label: "Watch", tone: "watch" };
  return { label: "Stable", tone: "success" };
}

export function filledRetryTrendBuckets(
  rows: readonly RetryTrendBucketLike[] | undefined,
  nowMillis: bigint | number,
  windowLabel: RetryWindowLabel,
): RetryTrendBucketLike[] {
  const config = RETRY_WINDOW_CONFIG[windowLabel];
  const now = toSafeNumber(nowMillis);
  const endBucket = Math.floor(now / config.bucketMs) * config.bucketMs;
  const byBucket = new Map((rows ?? []).map((row) => [row.bucketUnixMillis.toString(), row]));
  const result: RetryTrendBucketLike[] = [];

  for (let index = config.buckets - 1; index >= 0; index -= 1) {
    const bucketUnixMillis = BigInt(endBucket - index * config.bucketMs);
    result.push(byBucket.get(bucketUnixMillis.toString()) ?? emptyRetryTrendBucket(bucketUnixMillis));
  }
  return result;
}

function emptyRetryTrendBucket(bucketUnixMillis: bigint): RetryTrendBucketLike {
  return {
    bucketUnixMillis,
    matchedRequests: 0n,
    retriedRequests: 0n,
    retryAttempts: 0n,
    recoveredRequests: 0n,
    exhaustedRequests: 0n,
    skippedRequests: 0n,
  };
}

function safeRatio(numerator: bigint, denominator: bigint): number {
  if (numerator <= 0n || denominator <= 0n) return 0;
  const scaled = (numerator * 1_000_000n) / denominator;
  return Number(scaled) / 1_000_000;
}

function toSafeNumber(value: bigint | number): number {
  if (typeof value === "number") return Number.isFinite(value) ? Math.max(0, value) : 0;
  if (value <= 0n) return 0;
  const maximum = BigInt(Number.MAX_SAFE_INTEGER);
  return Number(value > maximum ? maximum : value);
}
