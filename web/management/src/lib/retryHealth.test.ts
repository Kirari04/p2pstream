import { describe, expect, test } from "bun:test";
import {
  filledRetryTrendBuckets,
  retryHealthSignal,
  retryRate,
  retryRecoveryRate,
  type RetryTrendBucketLike,
} from "@/lib/retryHealth";

describe("retryHealth", () => {
  test("computes retry trigger and transport recovery rates", () => {
    expect(retryRate(5n, 100n)).toBe(0.05);
    expect(retryRate(5n, 0n)).toBe(0);
    expect(retryRecoveryRate({ matchedRequests: 100n, retriedRequests: 5n, recoveredRequests: 4n, exhaustedRequests: 1n })).toBe(0.8);
    expect(retryRecoveryRate({ matchedRequests: 100n, retriedRequests: 0n, recoveredRequests: 0n, exhaustedRequests: 0n })).toBeNull();
  });

  test("classifies quiet, elevated, and exhausted retry traffic", () => {
    expect(retryHealthSignal(undefined)).toEqual({ label: "No matched traffic", tone: "neutral" });
    expect(retryHealthSignal(summary({ retriedRequests: 0n }))).toEqual({ label: "Stable", tone: "success" });
    expect(retryHealthSignal(summary({ retriedRequests: 1n }))).toEqual({ label: "Watch", tone: "watch" });
    expect(retryHealthSignal(summary({ retriedRequests: 5n }))).toEqual({ label: "Elevated", tone: "warning" });
    expect(retryHealthSignal(summary({ exhaustedRequests: 1n }))).toEqual({ label: "Action needed", tone: "danger" });
  });

  test("fills the selected window and preserves returned buckets", () => {
    const now = 60n * 60n * 1000n;
    const existing = bucket({
      bucketUnixMillis: now - 5n * 60n * 1000n,
      matchedRequests: 8n,
      retriedRequests: 2n,
    });
    const filled = filledRetryTrendBuckets([existing], now, "1h");

    expect(filled).toHaveLength(12);
    expect(filled[10]?.matchedRequests).toBe(8n);
    expect(filled[10]?.retriedRequests).toBe(2n);
    expect(filled[0]?.matchedRequests).toBe(0n);
  });
});

function summary(overrides: Partial<Parameters<typeof retryHealthSignal>[0]> = {}) {
  return {
    matchedRequests: 100n,
    retriedRequests: 0n,
    recoveredRequests: 0n,
    exhaustedRequests: 0n,
    ...overrides,
  };
}

function bucket(overrides: Partial<RetryTrendBucketLike> = {}): RetryTrendBucketLike {
  return {
    bucketUnixMillis: 0n,
    matchedRequests: 0n,
    retriedRequests: 0n,
    retryAttempts: 0n,
    recoveredRequests: 0n,
    exhaustedRequests: 0n,
    skippedRequests: 0n,
    ...overrides,
  };
}
