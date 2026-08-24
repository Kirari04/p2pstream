import { describe, expect, test } from "bun:test";
import { cacheStorageBudgetUsage } from "./cacheStorageUsage";

describe("cacheStorageBudgetUsage", () => {
  test("reports used, remaining, and percentage within budget", () => {
    expect(cacheStorageBudgetUsage(25n, 100n)).toEqual({
      used: 25n,
      limit: 100n,
      remaining: 75n,
      overage: 0n,
      usedPercent: 25,
      fillPercent: 25,
    });
  });

  test("reports overage while clamping the visual fill", () => {
    expect(cacheStorageBudgetUsage(125n, 100n)).toEqual({
      used: 125n,
      limit: 100n,
      remaining: 0n,
      overage: 25n,
      usedPercent: 125,
      fillPercent: 100,
    });
  });

  test("normalizes invalid negative counters", () => {
    expect(cacheStorageBudgetUsage(-1n, -1n)).toEqual({
      used: 0n,
      limit: 0n,
      remaining: 0n,
      overage: 0n,
      usedPercent: 0,
      fillPercent: 0,
    });
  });
});
