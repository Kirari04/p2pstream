<script setup lang="ts">
import { computed } from "vue";
import { NEmpty } from "naive-ui";
import MetricCard from "@/components/ui/MetricCard.vue";
import MetricStrip from "@/components/ui/MetricStrip.vue";
import SectionCard from "@/components/ui/SectionCard.vue";
import type {
  DashboardRetryFailureSummary,
  DashboardRetryHealthSummary,
  DashboardRetryRuleSummary,
  DashboardRetryTrendBucket,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { formatDuration, formatNumber, formatPercent } from "@/lib/dashboardStats";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";
import {
  filledRetryTrendBuckets,
  retryHealthSignal,
  retryRate,
  retryRecoveryRate,
  type RetryTrendBucketLike,
  type RetryWindowLabel,
} from "@/lib/retryHealth";

type RetryDimensionKey = "retry-agent" | "retry-error";

const props = defineProps<{
  retryHealth?: DashboardRetryHealthSummary;
  retryTrend?: DashboardRetryTrendBucket[];
  retryRules?: DashboardRetryRuleSummary[];
  retryFailedAgents?: DashboardRetryFailureSummary[];
  retryErrorKinds?: DashboardRetryFailureSummary[];
  generatedAtUnixMillis: bigint;
  windowLabel: RetryWindowLabel;
  selectedFilter?: Readonly<{ key: string; label: string }> | null;
}>();

const emit = defineEmits<{
  "select-dimension": [key: RetryDimensionKey, label: string, title: string];
}>();

const trend = computed(() => filledRetryTrendBuckets(
  props.retryTrend,
  props.generatedAtUnixMillis,
  props.windowLabel,
));
const triggerRate = computed(() => retryRate(
  props.retryHealth?.retriedRequests ?? 0n,
  props.retryHealth?.matchedRequests ?? 0n,
));
const transportRecoveryRate = computed(() => retryRecoveryRate(props.retryHealth));
const signal = computed(() => retryHealthSignal(props.retryHealth));
const trendScale = computed(() => {
  const peak = Math.max(0, ...trend.value.map((bucket) => retryRate(bucket.retriedRequests, bucket.matchedRequests)));
  return Math.max(0.05, Math.ceil(peak / 0.05) * 0.05);
});
const trendWindowLabel = computed(() => {
  const buckets = trend.value;
  if (!buckets.length) return "No trend window";
  return `${formatBucketTime(buckets[0]?.bucketUnixMillis ?? 0n)} – ${formatBucketTime(buckets[buckets.length - 1]?.bucketUnixMillis ?? 0n)}`;
});
const bucketDurationLabel = computed(() => {
  switch (props.windowLabel) {
    case "5m": return "minute";
    case "1h": return "5-minute";
    case "24h": return "hour";
    case "30d": return "day";
  }
});

function ruleTriggerRate(row: DashboardRetryRuleSummary): number {
  return retryRate(row.retriedRequests, row.matchedRequests);
}

function failureRecoveryRate(row: DashboardRetryFailureSummary): number | null {
  const completed = row.recoveredRequests + row.exhaustedRequests;
  return completed > 0n ? retryRate(row.recoveredRequests, completed) : null;
}

function trendHeight(retriedRequests: bigint, matchedRequests: bigint): string {
  const rate = retryRate(retriedRequests, matchedRequests);
  if (rate <= 0) return "0%";
  return `${Math.max(4, Math.min(100, (rate / trendScale.value) * 100)).toFixed(1)}%`;
}

function trendFailureHeight(exhaustedRequests: bigint, matchedRequests: bigint): string {
  const rate = retryRate(exhaustedRequests, matchedRequests);
  if (rate <= 0) return "0%";
  return `${Math.max(4, Math.min(100, (rate / trendScale.value) * 100)).toFixed(1)}%`;
}

function trendBucketLabel(bucket: RetryTrendBucketLike): string {
  return [
    formatBucketTime(bucket.bucketUnixMillis),
    `${formatPercent(retryRate(bucket.retriedRequests, bucket.matchedRequests))} retry trigger rate`,
    `${formatNumber(bucket.matchedRequests)} matched`,
    `${formatNumber(bucket.retryAttempts)} extra attempts`,
    `${formatNumber(bucket.recoveredRequests)} recovered`,
    `${formatNumber(bucket.exhaustedRequests)} exhausted`,
    `${formatNumber(bucket.skippedRequests)} skipped`,
  ].join(" · ");
}

function formatBucketTime(value: bigint): string {
  const millis = toNumber(value);
  if (millis <= 0) return "—";
  const date = new Date(millis);
  if (Number.isNaN(date.getTime())) return "Invalid time";
  if (props.windowLabel === "30d") {
    return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(date);
  }
  return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" }).format(date);
}

function optionalPercent(value: number | null): string {
  return value === null ? "—" : formatPercent(value);
}

function dimensionSelected(key: RetryDimensionKey, label: string): boolean {
  return props.selectedFilter?.key === key && props.selectedFilter.label === label;
}

function inspectionValue(value: string): string {
  return value ? diagnosticInspectionText(value) : "(empty)";
}

function toNumber(value: bigint): number {
  if (value <= 0n) return 0;
  const maximum = BigInt(Number.MAX_SAFE_INTEGER);
  return Number(value > maximum ? maximum : value);
}
</script>

<template>
  <section class="retry-health" aria-label="Retry health">
    <SectionCard title="Retry health" description="How often retry rules activate, whether transport recovers, and where first attempts fail.">
      <template #badge>
        <span class="retry-signal" :class="`retry-signal--${signal.tone}`">
          <span aria-hidden="true" />
          {{ signal.label }}
        </span>
      </template>

      <div v-if="retryHealth && retryHealth.matchedRequests > 0n" class="retry-summary">
        <MetricStrip variant="tiles" label="Retry health" class="retry-kpis">
          <MetricCard label="Policy matched" :value="formatNumber(retryHealth.matchedRequests)" detail="requests eligible for a rule" />
          <MetricCard label="Retry trigger rate" :value="formatPercent(triggerRate)" :detail="`${formatNumber(retryHealth.retriedRequests)} requests retried`" />
          <MetricCard label="Extra attempts" :value="formatNumber(retryHealth.retryAttempts)" detail="beyond original requests" />
          <MetricCard label="Transport recovery" :value="optionalPercent(transportRecoveryRate)" :detail="`${formatNumber(retryHealth.recoveredRequests)} recovered`" />
          <MetricCard
            label="Not recovered"
            :value="formatNumber(retryHealth.exhaustedRequests)"
            :tone="retryHealth.exhaustedRequests > 0n ? 'error' : 'default'"
            :detail="`${formatNumber(retryHealth.skippedRequests)} skipped by safety`"
          />
          <MetricCard label="Retried latency" :value="formatDuration(retryHealth.avgRetriedDurationMs)" :detail="`${formatDuration(retryHealth.avgMatchedDurationMs)} across matched`" />
        </MetricStrip>

        <div class="retry-trend">
          <div class="retry-trend__heading">
            <div>
              <h3>Retry trigger trend</h3>
              <p>{{ trendWindowLabel }} · each bar is a {{ bucketDurationLabel }} bucket</p>
            </div>
            <div class="retry-trend-legend" aria-label="Chart legend">
              <span><i class="retry-legend-swatch retry-legend-swatch--trigger" />triggered</span>
              <span><i class="retry-legend-swatch retry-legend-swatch--failed" />exhausted</span>
            </div>
          </div>
          <div class="retry-chart">
            <div class="retry-chart__scale" aria-hidden="true">
              <span>{{ formatPercent(trendScale) }}</span>
              <span>{{ formatPercent(trendScale / 2) }}</span>
              <span>0%</span>
            </div>
            <div
              class="retry-chart__plot"
              role="list"
              :aria-label="`Retry trigger rate over ${windowLabel}. Vertical scale ends at ${formatPercent(trendScale)}.`"
            >
              <div
                v-for="bucket in trend"
                :key="bucket.bucketUnixMillis.toString()"
                class="retry-chart__bucket"
                role="listitem"
                :aria-label="trendBucketLabel(bucket)"
                :title="trendBucketLabel(bucket)"
              >
                <span class="retry-chart__bar" :style="{ height: trendHeight(bucket.retriedRequests, bucket.matchedRequests) }" />
                <span
                  v-if="bucket.exhaustedRequests > 0n"
                  class="retry-chart__bar retry-chart__bar--failed"
                  :style="{ height: trendFailureHeight(bucket.exhaustedRequests, bucket.matchedRequests) }"
                />
              </div>
            </div>
          </div>
          <div class="retry-chart__axis" aria-hidden="true">
            <span>{{ formatBucketTime(trend[0]?.bucketUnixMillis ?? 0n) }}</span>
            <span>now</span>
          </div>
          <p class="retry-definition">
            “Recovered” means a later agent produced an HTTP response after a transport failure; that response may still be non-success.
            Health guidance: watch at 1% trigger rate, elevated at 5%, and action needed whenever retries exhaust.
          </p>
        </div>
      </div>

      <div v-else class="retry-empty-state">
        <div>
          <h3>No requests matched a retry rule in this window</h3>
          <p>The panel will begin charting trigger rate, recovery, and failure attribution when eligible traffic arrives.</p>
        </div>
        <RouterLink to="/policies/retries">Review retry rules</RouterLink>
      </div>
    </SectionCard>

    <template v-if="retryHealth && retryHealth.matchedRequests > 0n">
      <SectionCard title="Rules" description="Policy volume and outcomes." flush>
        <template #actions>
          <RouterLink class="retry-link" to="/policies/retries">Manage rules</RouterLink>
        </template>
        <div v-if="retryRules?.length" class="retry-rules-table-shell">
          <table class="retry-rules-table">
            <thead>
              <tr>
                <th scope="col">Rule</th>
                <th scope="col">Triggered</th>
                <th scope="col">Extra attempts</th>
                <th scope="col">Recovered</th>
                <th scope="col">Not recovered</th>
                <th scope="col">Latency</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in retryRules" :key="row.id.toString()">
                <th scope="row">
                  <bdi dir="ltr" :title="inspectionValue(row.label)">{{ diagnosticExcerpt(row.label, 46).text }}</bdi>
                  <small>#{{ row.id.toString() }} · {{ formatNumber(row.matchedRequests) }} matched</small>
                </th>
                <td><strong>{{ formatPercent(ruleTriggerRate(row)) }}</strong><small>{{ formatNumber(row.retriedRequests) }} req</small></td>
                <td>{{ formatNumber(row.retryAttempts) }}</td>
                <td>{{ formatNumber(row.recoveredRequests) }}</td>
                <td :class="{ 'retry-table-danger': row.exhaustedRequests > 0n }">
                  {{ formatNumber(row.exhaustedRequests) }}
                  <small>{{ formatNumber(row.skippedRequests) }} skipped</small>
                </td>
                <td>{{ formatDuration(row.avgRetriedDurationMs) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <NEmpty v-else size="small" class="retry-card-empty" description="No retry rule rollups in this window." />
      </SectionCard>

      <div class="retry-details-grid">
        <SectionCard title="First-failed agents" description="The agent whose upstream attempt failed. Local proxy failures are not attributed." flush>
          <ol v-if="retryFailedAgents?.length" class="retry-failure-list">
            <li v-for="row in retryFailedAgents" :key="`retry-agent-${row.id.toString()}-${row.label}`">
              <button
                type="button"
                :class="{ 'retry-failure-row--selected': dimensionSelected('retry-agent', row.label) }"
                :aria-pressed="dimensionSelected('retry-agent', row.label)"
                @click="emit('select-dimension', 'retry-agent', row.label, 'First-failed agent')"
              >
                <bdi dir="ltr" :title="inspectionValue(row.label)">{{ diagnosticExcerpt(row.label, 42).text }}</bdi>
                <strong>{{ formatNumber(row.affectedRequests) }}</strong>
                <span>
                  {{ optionalPercent(failureRecoveryRate(row)) }} recovered ·
                  {{ formatNumber(row.skippedRequests) }} skipped ·
                  {{ formatNumber(row.exhaustedRequests) }} exhausted
                </span>
              </button>
            </li>
          </ol>
          <NEmpty v-else size="small" class="retry-card-empty" description="No attributed first-agent failures in this window." />
        </SectionCard>

        <SectionCard title="First error kinds" description="First transport condition recorded for traffic matched by a retry rule." flush>
          <ol v-if="retryErrorKinds?.length" class="retry-failure-list">
            <li v-for="row in retryErrorKinds" :key="`retry-error-${row.label}`">
              <button
                type="button"
                :class="{ 'retry-failure-row--selected': dimensionSelected('retry-error', row.label) }"
                :aria-pressed="dimensionSelected('retry-error', row.label)"
                @click="emit('select-dimension', 'retry-error', row.label, 'First retry error')"
              >
                <bdi dir="ltr" :title="inspectionValue(row.label)">{{ diagnosticExcerpt(row.label, 42).text }}</bdi>
                <strong>{{ formatNumber(row.affectedRequests) }}</strong>
                <span>
                  {{ formatNumber(row.retryAttempts) }} extra attempts ·
                  {{ formatNumber(row.skippedRequests) }} skipped ·
                  {{ formatNumber(row.exhaustedRequests) }} exhausted
                </span>
              </button>
            </li>
          </ol>
          <NEmpty v-else size="small" class="retry-card-empty" description="No first-attempt retry errors in this window." />
        </SectionCard>
      </div>
    </template>
  </section>
</template>

<style scoped>
.retry-health {
  display: grid;
  min-width: 0;
  gap: 1.25rem;
}

.retry-signal {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 0.375rem;
  border-radius: 6px;
  background: var(--app-panel-muted);
  padding: 0.125rem 0.5rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-weight: 500;
}

.retry-signal > span {
  width: 0.5rem;
  height: 0.5rem;
  border-radius: 50%;
  background: currentColor;
}

.retry-signal--success { color: var(--app-success); }
.retry-signal--watch,
.retry-signal--warning { color: var(--app-warning); }
.retry-signal--danger { color: var(--app-error); }

.retry-summary {
  display: grid;
  min-width: 0;
  gap: 1.25rem;
}

.retry-health .retry-kpis {
  grid-template-columns: repeat(6, minmax(0, 1fr));
}

.retry-kpis :deep(.metric-card__detail) {
  white-space: normal;
}

.retry-trend {
  min-width: 0;
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 1rem;
}

.retry-trend__heading {
  display: flex;
  align-items: start;
  justify-content: space-between;
  gap: 0.75rem;
}

.retry-trend h3,
.retry-empty-state h3 {
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
}

.retry-trend__heading p,
.retry-empty-state p,
.retry-definition {
  margin-top: 0.125rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.5;
}

.retry-trend-legend {
  display: flex;
  flex: none;
  gap: 1rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.retry-trend-legend span {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
}

.retry-legend-swatch {
  width: 0.625rem;
  height: 0.625rem;
  border-radius: 2px;
}

.retry-legend-swatch--trigger { background: var(--app-warning); }
.retry-legend-swatch--failed { background: var(--app-error); }

.retry-chart {
  display: grid;
  height: 9.5rem;
  grid-template-columns: 3rem minmax(0, 1fr);
  gap: 0.5rem;
  margin-top: 0.875rem;
}

.retry-chart__scale {
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.6875rem;
  font-variant-numeric: tabular-nums;
  text-align: right;
}

/* Recessive grid: a single hairline at 50%, axis line at the base. */
.retry-chart__plot {
  display: grid;
  min-width: 0;
  grid-auto-columns: minmax(2px, 1fr);
  grid-auto-flow: column;
  align-items: end;
  gap: 2px;
  border-bottom: 1px solid var(--app-border);
  background-image: linear-gradient(to bottom, var(--app-border-subtle) 1px, transparent 1px);
  background-size: 100% 50%;
}

.retry-chart__bucket {
  position: relative;
  height: 100%;
  border-radius: 4px 4px 0 0;
}

.retry-chart__bucket:hover {
  background: var(--app-hover);
}

.retry-chart__bar {
  position: absolute;
  bottom: 0;
  left: 50%;
  width: min(100%, 0.75rem);
  min-height: 0;
  border-radius: 4px 4px 0 0;
  background: var(--app-warning);
  transform: translateX(-50%);
}

.retry-chart__bar--failed {
  z-index: 1;
  background: var(--app-error);
}

.retry-chart__axis {
  display: flex;
  justify-content: space-between;
  margin: 0.375rem 0 0 3.5rem;
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.6875rem;
}

.retry-definition {
  margin-top: 0.875rem;
  max-width: 80ch;
}

.retry-link,
.retry-empty-state > a {
  color: var(--app-accent);
  font-size: 0.8125rem;
  font-weight: 500;
}

.retry-link:hover,
.retry-empty-state > a:hover {
  text-decoration: underline;
}

.retry-details-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1.25rem;
}

.retry-card-empty {
  padding: 1.5rem 1rem;
}

.retry-rules-table-shell {
  overflow-x: auto;
}

.retry-rules-table {
  width: 100%;
  min-width: 42rem;
  border-collapse: collapse;
  color: var(--app-text);
  font-size: 0.8125rem;
}

.retry-rules-table thead th {
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-weight: 500;
}

.retry-rules-table th,
.retry-rules-table td {
  padding: 0.5rem 0.75rem;
  text-align: right;
  vertical-align: top;
}

.retry-rules-table tbody tr {
  border-top: 1px solid var(--app-border-subtle);
}

.retry-rules-table th:first-child {
  padding-left: 1.25rem;
  text-align: left;
}

.retry-rules-table th:last-child,
.retry-rules-table td:last-child {
  padding-right: 1.25rem;
}

.retry-rules-table tbody th,
.retry-rules-table tbody td {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  font-weight: 500;
}

.retry-rules-table tbody td strong {
  font-weight: 600;
}

.retry-rules-table tbody th {
  max-width: 14rem;
}

.retry-rules-table small {
  color: var(--app-text-muted);
  font-size: 0.6875rem;
}

.retry-rules-table bdi,
.retry-rules-table small {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  unicode-bidi: isolate;
  white-space: nowrap;
}

.retry-table-danger {
  color: var(--app-error);
}

.retry-failure-list {
  display: grid;
  margin: 0;
  padding: 0;
  list-style: none;
}

.retry-failure-list li + li {
  border-top: 1px solid var(--app-border-subtle);
}

.retry-failure-list button {
  display: grid;
  width: 100%;
  min-width: 0;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 0.25rem 0.75rem;
  border: 0;
  background: transparent;
  padding: 0.625rem 1.25rem;
  color: var(--app-text);
  cursor: pointer;
  font: inherit;
  text-align: left;
}

.retry-failure-list button:hover {
  background: var(--app-hover);
}

.retry-failure-list button:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: -2px;
}

.retry-failure-row--selected {
  box-shadow: inset 2px 0 0 var(--app-accent);
  background: var(--app-accent-soft) !important;
}

.retry-failure-list bdi {
  min-width: 0;
  overflow: hidden;
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  font-weight: 500;
  text-overflow: ellipsis;
  unicode-bidi: isolate;
  white-space: nowrap;
}

.retry-failure-row--selected bdi {
  color: var(--app-accent);
}

.retry-failure-list strong {
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.retry-failure-list span {
  grid-column: 1 / -1;
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.retry-empty-state {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 1rem;
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 1rem;
}

@media (max-width: 1180px) {
  .retry-health .retry-kpis {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (max-width: 860px) {
  .retry-details-grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 639px) {
  .retry-health .retry-kpis {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .retry-trend__heading {
    display: grid;
  }

  .retry-chart {
    grid-template-columns: 2.5rem minmax(0, 1fr);
  }

  .retry-chart__axis {
    margin-left: 3rem;
  }

  .retry-empty-state {
    grid-template-columns: 1fr;
  }

  .retry-failure-list button,
  .retry-rules-table th:first-child {
    padding-left: 1rem;
  }

  .retry-failure-list button,
  .retry-rules-table th:last-child,
  .retry-rules-table td:last-child {
    padding-right: 1rem;
  }
}

@media (pointer: coarse) {
  .retry-failure-list button {
    min-height: 44px;
  }
}
</style>
