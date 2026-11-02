<script setup lang="ts">
import { computed } from "vue";
import MetricCard from "@/components/ui/MetricCard.vue";
import MetricStrip from "@/components/ui/MetricStrip.vue";
import type { PublicCacheSettings, PublicCacheStorageStats } from "@/gen/proto/p2pstream/v1/management_pb";
import { cacheStorageBudgetUsage, type CacheStorageBudgetUsage } from "@/lib/cacheStorageUsage";
import { formatBytes, formatNumber } from "@/lib/dashboardStats";

type UsageRow = CacheStorageBudgetUsage & {
  key: "disk" | "memory" | "entries";
  label: string;
  usedLabel: string;
  limitLabel: string;
  remainingLabel: string;
};

const props = defineProps<{
  settings?: PublicCacheSettings | null;
  stats?: PublicCacheStorageStats | null;
}>();

const usageRows = computed<UsageRow[]>(() => {
  if (!props.settings || !props.stats) return [];
  return [
    usageRow("disk", "Disk bodies", props.stats.diskBytesUsed, props.settings.maxDiskBytes, formatBytes),
    usageRow("memory", "Memory tier", props.stats.memoryBytesUsed, props.settings.maxMemoryBytes, formatBytes),
    usageRow("entries", "Cache entries", props.stats.entriesUsed, props.settings.maxEntries, formatNumber),
  ];
});

function usageRow(
  key: UsageRow["key"],
  label: string,
  used: bigint,
  limit: bigint,
  format: (value: bigint) => string,
): UsageRow {
  const usage = cacheStorageBudgetUsage(used, limit);
  return {
    ...usage,
    key,
    label,
    usedLabel: format(usage.used),
    limitLabel: format(usage.limit),
    remainingLabel: usage.overage > 0n
      ? `${format(usage.overage)} over budget`
      : `${format(usage.remaining)} remaining`,
  };
}

function percentLabel(percent: number): string {
  if (!Number.isFinite(percent)) return "0%";
  return `${percent > 0 && percent < 10 ? percent.toFixed(1) : Math.round(percent).toString()}%`;
}
</script>

<template>
  <section class="cache-usage" aria-labelledby="cache-usage-heading">
    <div class="cache-usage__header">
      <h3 id="cache-usage-heading" class="copy-sm weight-semibold base-text">Storage usage</h3>
      <p class="copy-xs muted-text">Latest successful dashboard refresh, compared with saved limits</p>
    </div>

    <MetricStrip v-if="usageRows.length" variant="strip" label="Cache storage usage">
      <MetricCard
        v-for="row in usageRows"
        :key="row.key"
        :label="row.label"
        :value="row.usedLabel"
        :tone="row.overage > 0n ? 'error' : 'default'"
      >
        <template #detail>
          <span class="cache-usage__detail">
            <span class="cache-usage__line">of {{ row.limitLabel }} · {{ percentLabel(row.usedPercent) }} used</span>
            <span
              class="cache-usage__track"
              :class="{ 'cache-usage__track--over': row.overage > 0n }"
              role="progressbar"
              aria-valuemin="0"
              aria-valuemax="100"
              :aria-valuenow="Math.round(row.fillPercent)"
              :aria-valuetext="`${row.usedLabel} used of ${row.limitLabel}; ${row.remainingLabel}`"
              :aria-label="`${row.label} budget usage`"
            >
              <span :style="{ transform: `scaleX(${(row.fillPercent / 100).toString()})` }"></span>
            </span>
            <span class="cache-usage__line" :class="{ 'cache-usage__overage': row.overage > 0n }">{{ row.remainingLabel }}</span>
          </span>
        </template>
      </MetricCard>
    </MetricStrip>
    <p v-else class="copy-xs muted-text">Usage data is temporarily unavailable. Saved storage limits are still enforced.</p>
  </section>
</template>

<style scoped>
.cache-usage {
  display: grid;
  min-width: 0;
  gap: 0.75rem;
}

.cache-usage__header {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: 0.25rem 1rem;
}

.cache-usage__detail {
  display: grid;
  min-width: 0;
  gap: 0.375rem;
  padding-top: 0.125rem;
}

.cache-usage__line {
  overflow: hidden;
  font-variant-numeric: tabular-nums;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cache-usage__track {
  display: block;
  height: 0.25rem;
  overflow: hidden;
  border-radius: 999px;
  background: var(--app-border-subtle);
}

.cache-usage__track > span {
  display: block;
  width: 100%;
  height: 100%;
  border-radius: inherit;
  background: var(--app-accent);
  transform-origin: left center;
  transition: transform 320ms ease;
}

.cache-usage__track--over > span {
  background: var(--app-error);
}

.cache-usage__overage {
  color: var(--app-error);
  font-weight: 500;
}

@media (prefers-reduced-motion: reduce) {
  .cache-usage__track > span {
    transition: none;
  }
}
</style>
