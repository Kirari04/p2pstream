<script setup lang="ts">
import { computed, type Component } from "vue";
import { Cpu as MemoryIcon, Database as DiskIcon, Layers3 as EntriesIcon } from "@lucide/vue";
import type { PublicCacheSettings, PublicCacheStorageStats } from "@/gen/proto/p2pstream/v1/management_pb";
import { cacheStorageBudgetUsage, type CacheStorageBudgetUsage } from "@/lib/cacheStorageUsage";
import { formatBytes, formatNumber } from "@/lib/dashboardStats";

type UsageRow = CacheStorageBudgetUsage & {
  key: "disk" | "memory" | "entries";
  label: string;
  icon: Component;
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
    usageRow("disk", "Disk bodies", DiskIcon, props.stats.diskBytesUsed, props.settings.maxDiskBytes, formatBytes),
    usageRow("memory", "Memory tier", MemoryIcon, props.stats.memoryBytesUsed, props.settings.maxMemoryBytes, formatBytes),
    usageRow("entries", "Cache entries", EntriesIcon, props.stats.entriesUsed, props.settings.maxEntries, formatNumber),
  ];
});

function usageRow(
  key: UsageRow["key"],
  label: string,
  icon: Component,
  used: bigint,
  limit: bigint,
  format: (value: bigint) => string,
): UsageRow {
  const usage = cacheStorageBudgetUsage(used, limit);
  return {
    ...usage,
    key,
    label,
    icon,
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
      <div>
        <div class="cache-usage__eyebrow">
          <span class="cache-usage__pulse" aria-hidden="true"></span>
          Live cache footprint
        </div>
        <h4 id="cache-usage-heading">Storage usage</h4>
      </div>
      <p>Compared with saved limits · refreshes every 5 seconds</p>
    </div>

    <div v-if="usageRows.length" class="cache-usage__grid">
      <article
        v-for="row in usageRows"
        :key="row.key"
        class="cache-usage__item"
        :class="[`cache-usage__item--${row.key}`, { 'cache-usage__item--over': row.overage > 0n }]"
      >
        <div class="cache-usage__metric">
          <span class="cache-usage__icon" aria-hidden="true"><component :is="row.icon" /></span>
          <div>
            <span>{{ row.label }}</span>
            <p><strong>{{ row.usedLabel }}</strong><small> / {{ row.limitLabel }}</small></p>
          </div>
        </div>
        <div
          class="cache-usage__track"
          role="progressbar"
          aria-valuemin="0"
          aria-valuemax="100"
          :aria-valuenow="Math.round(row.fillPercent)"
          :aria-valuetext="`${row.usedLabel} used of ${row.limitLabel}; ${row.remainingLabel}`"
          :aria-label="`${row.label} budget usage`"
        >
          <span :style="{ transform: `scaleX(${(row.fillPercent / 100).toString()})` }"></span>
        </div>
        <div class="cache-usage__foot">
          <span :class="{ 'cache-usage__overage': row.overage > 0n }">{{ row.remainingLabel }}</span>
          <span>{{ percentLabel(row.usedPercent) }} used</span>
        </div>
      </article>
    </div>
    <p v-else class="cache-usage__unavailable">Usage data is temporarily unavailable. Saved storage limits are still enforced.</p>
  </section>
</template>

<style scoped>
.cache-usage {
  position: relative;
  display: grid;
  gap: 0.875rem;
  overflow: hidden;
  border: 1px solid var(--app-border-subtle);
  border-radius: 0.625rem;
  padding: 0.875rem;
  background:
    linear-gradient(135deg, color-mix(in srgb, var(--app-accent-soft) 46%, transparent), transparent 38%),
    var(--app-panel-muted);
}

.cache-usage::after {
  position: absolute;
  inset: 0 0 auto;
  height: 1px;
  background: linear-gradient(90deg, var(--app-accent), transparent 58%);
  content: "";
  opacity: 0.55;
}

.cache-usage__header {
  display: flex;
  align-items: end;
  justify-content: space-between;
  gap: 1rem;
}

.cache-usage__header h4,
.cache-usage__header p,
.cache-usage__metric p,
.cache-usage__unavailable {
  margin: 0;
}

.cache-usage__header h4 {
  margin-top: 0.125rem;
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
}

.cache-usage__header > p,
.cache-usage__unavailable {
  color: var(--app-text-muted);
  font-size: 0.7rem;
  line-height: 1.4;
}

.cache-usage__eyebrow {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  color: var(--app-accent);
  font-size: 0.625rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  line-height: 1;
  text-transform: uppercase;
}

.cache-usage__pulse {
  width: 0.375rem;
  height: 0.375rem;
  border-radius: 999px;
  background: var(--app-success);
  box-shadow: 0 0 0 0.2rem color-mix(in srgb, var(--app-success) 14%, transparent);
}

.cache-usage__grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0.625rem;
}

.cache-usage__item {
  --usage-color: var(--app-accent);
  display: grid;
  min-width: 0;
  gap: 0.625rem;
  border: 1px solid var(--app-border-subtle);
  border-radius: 0.5rem;
  padding: 0.75rem;
  background: color-mix(in srgb, var(--app-panel) 92%, transparent);
}

.cache-usage__item--memory {
  --usage-color: var(--app-success);
}

.cache-usage__item--entries {
  --usage-color: color-mix(in srgb, var(--app-accent) 62%, var(--app-success));
}

.cache-usage__item--over {
  --usage-color: var(--app-error);
  border-color: color-mix(in srgb, var(--app-error) 38%, var(--app-border));
}

.cache-usage__metric {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.625rem;
}

.cache-usage__icon {
  display: inline-flex;
  width: 1.75rem;
  height: 1.75rem;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border-radius: 0.4rem;
  background: color-mix(in srgb, var(--usage-color) 11%, var(--app-panel));
  color: var(--usage-color);
}

.cache-usage__icon :deep(svg) {
  width: 0.875rem;
  height: 0.875rem;
  stroke-width: 1.8;
}

.cache-usage__metric > div {
  min-width: 0;
}

.cache-usage__metric > div > span {
  display: block;
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 0.6875rem;
  font-weight: 600;
  line-height: 1.3;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cache-usage__metric p {
  overflow: hidden;
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 0.75rem;
  line-height: 1.45;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cache-usage__metric strong {
  font-size: 0.875rem;
  font-weight: 600;
}

.cache-usage__metric small {
  color: var(--app-text-muted);
  font-size: 0.6875rem;
}

.cache-usage__track {
  height: 0.35rem;
  overflow: hidden;
  border-radius: 999px;
  background: color-mix(in srgb, var(--app-border) 72%, var(--app-panel));
}

.cache-usage__track > span {
  display: block;
  width: 100%;
  height: 100%;
  border-radius: inherit;
  background: var(--usage-color);
  transform-origin: left center;
  transition: transform 320ms ease;
}

.cache-usage__foot {
  display: flex;
  min-width: 0;
  justify-content: space-between;
  gap: 0.5rem;
  color: var(--app-text-muted);
  font-size: 0.625rem;
  line-height: 1.35;
}

.cache-usage__foot span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cache-usage__overage {
  color: var(--app-error);
  font-weight: 600;
}

@media (max-width: 760px) {
  .cache-usage__header {
    align-items: start;
    flex-direction: column;
    gap: 0.25rem;
  }

  .cache-usage__grid {
    grid-template-columns: 1fr;
  }
}

@media (prefers-reduced-motion: reduce) {
  .cache-usage__track > span {
    transition: none;
  }
}
</style>
