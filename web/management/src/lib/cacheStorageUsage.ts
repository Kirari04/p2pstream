export type CacheStorageBudgetUsage = {
  used: bigint;
  limit: bigint;
  remaining: bigint;
  overage: bigint;
  usedPercent: number;
  fillPercent: number;
};

export function cacheStorageBudgetUsage(used: bigint, limit: bigint): CacheStorageBudgetUsage {
  const safeUsed = used > 0n ? used : 0n;
  const safeLimit = limit > 0n ? limit : 0n;
  const remaining = safeUsed < safeLimit ? safeLimit - safeUsed : 0n;
  const overage = safeUsed > safeLimit ? safeUsed - safeLimit : 0n;
  const usedPercent = safeLimit > 0n
    ? Number((safeUsed * 1000n) / safeLimit) / 10
    : 0;
  return {
    used: safeUsed,
    limit: safeLimit,
    remaining,
    overage,
    usedPercent,
    fillPercent: Math.min(100, usedPercent),
  };
}
