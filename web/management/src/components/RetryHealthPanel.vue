<script setup lang="ts">
import { computed } from "vue";
import { NEmpty } from "naive-ui";
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
  if (millis <= 0) return "-";
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
  <section class="retry-health" aria-labelledby="retry-health-heading">
    <div class="panel-heading retry-health__heading">
      <div>
        <span class="retry-health__eyebrow">Resilience telemetry</span>
        <h4 id="retry-health-heading">Retry Health</h4>
        <p>How often retry rules activate, whether transport recovers, and where first attempts fail.</p>
      </div>
      <span class="retry-signal" :class="`retry-signal--${signal.tone}`">
        <span aria-hidden="true" />
        {{ signal.label }}
      </span>
    </div>

    <template v-if="retryHealth && retryHealth.matchedRequests > 0n">
      <dl class="retry-kpis">
        <div>
          <dt>Policy matched</dt>
          <dd>{{ formatNumber(retryHealth.matchedRequests) }}</dd>
          <small>requests eligible for a rule</small>
        </div>
        <div>
          <dt>Retry trigger rate</dt>
          <dd>{{ formatPercent(triggerRate) }}</dd>
          <small>{{ formatNumber(retryHealth.retriedRequests) }} requests retried</small>
        </div>
        <div>
          <dt>Extra attempts</dt>
          <dd>{{ formatNumber(retryHealth.retryAttempts) }}</dd>
          <small>beyond original requests</small>
        </div>
        <div>
          <dt>Transport recovery</dt>
          <dd>{{ optionalPercent(transportRecoveryRate) }}</dd>
          <small>{{ formatNumber(retryHealth.recoveredRequests) }} recovered</small>
        </div>
        <div :class="{ 'retry-kpi--danger': retryHealth.exhaustedRequests > 0n }">
          <dt>Not recovered</dt>
          <dd>{{ formatNumber(retryHealth.exhaustedRequests) }}</dd>
          <small>{{ formatNumber(retryHealth.skippedRequests) }} skipped by safety</small>
        </div>
        <div>
          <dt>Retried latency</dt>
          <dd>{{ formatDuration(retryHealth.avgRetriedDurationMs) }}</dd>
          <small>{{ formatDuration(retryHealth.avgMatchedDurationMs) }} across matched</small>
        </div>
      </dl>

      <div class="retry-trend-card">
        <div class="retry-trend-card__heading">
          <div>
            <h5>Retry trigger trend</h5>
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

      <div class="retry-details-grid">
        <div class="retry-detail-card retry-detail-card--rules">
          <div class="retry-detail-card__heading">
            <div>
              <h5>Rules</h5>
              <p>Policy volume and outcomes.</p>
            </div>
            <RouterLink to="/policies/retries">Manage rules</RouterLink>
          </div>
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
          <NEmpty v-else size="small" description="No retry rule rollups in this window." />
        </div>

        <div class="retry-detail-card">
          <div class="retry-detail-card__heading">
            <div>
              <h5>First-failed agents</h5>
              <p>The agent whose upstream attempt failed. Local proxy failures are not attributed.</p>
            </div>
          </div>
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
          <NEmpty v-else size="small" description="No attributed first-agent failures in this window." />
        </div>

        <div class="retry-detail-card">
          <div class="retry-detail-card__heading">
            <div>
              <h5>First error kinds</h5>
              <p>First transport condition recorded for traffic matched by a retry rule.</p>
            </div>
          </div>
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
          <NEmpty v-else size="small" description="No first-attempt retry errors in this window." />
        </div>
      </div>
    </template>

    <div v-else class="retry-empty-state">
      <div class="retry-empty-state__pulse" aria-hidden="true"><span /></div>
      <div>
        <h5>No requests matched a retry rule in this window</h5>
        <p>The panel will begin charting trigger rate, recovery, and failure attribution when eligible traffic arrives.</p>
      </div>
      <RouterLink to="/policies/retries">Review retry rules</RouterLink>
    </div>
  </section>
</template>

<style scoped>
.retry-health {
  display: grid;
  min-width: 0;
  gap: 1rem;
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-top: 2px solid color-mix(in srgb, var(--app-warning) 70%, var(--app-accent));
  border-radius: 6px;
  background:
    linear-gradient(135deg, color-mix(in srgb, var(--app-warning) 5%, transparent), transparent 34%),
    var(--app-panel-muted);
  padding: 1rem;
}

.panel-heading,
.retry-trend-card__heading,
.retry-detail-card__heading {
  display: flex;
  align-items: start;
  justify-content: space-between;
  gap: 1rem;
}

.retry-health__heading {
  align-items: center;
}

.panel-heading h4 {
  color: var(--app-text);
  font-size: 0.95rem;
  font-weight: 700;
}

.panel-heading p {
  color: var(--app-text-muted);
  font-size: 0.82rem;
}

.retry-health__eyebrow {
  display: block;
  margin-bottom: 0.2rem;
  color: var(--app-warning);
  font-family: var(--font-mono);
  font-size: 0.68rem;
  font-weight: 750;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.retry-signal {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 0.45rem;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-panel);
  padding: 0.35rem 0.6rem;
  color: var(--app-text-muted);
  font-size: 0.72rem;
  font-weight: 700;
}

.retry-signal > span {
  width: 0.5rem;
  height: 0.5rem;
  border-radius: 50%;
  background: var(--app-text-muted);
  box-shadow: 0 0 0 0.18rem color-mix(in srgb, var(--app-text-muted) 14%, transparent);
}

.retry-signal--success {
  border-color: color-mix(in srgb, var(--app-success) 40%, var(--app-border));
  color: var(--app-success);
}

.retry-signal--success > span {
  background: var(--app-success);
  box-shadow: 0 0 0 0.18rem color-mix(in srgb, var(--app-success) 14%, transparent);
}

.retry-signal--watch,
.retry-signal--warning {
  border-color: color-mix(in srgb, var(--app-warning) 44%, var(--app-border));
  color: var(--app-warning);
}

.retry-signal--watch > span,
.retry-signal--warning > span {
  background: var(--app-warning);
  box-shadow: 0 0 0 0.18rem color-mix(in srgb, var(--app-warning) 14%, transparent);
}

.retry-signal--danger {
  border-color: color-mix(in srgb, var(--app-error) 44%, var(--app-border));
  color: var(--app-error);
}

.retry-signal--danger > span {
  background: var(--app-error);
  box-shadow: 0 0 0 0.18rem color-mix(in srgb, var(--app-error) 14%, transparent);
}

.retry-kpis {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  margin: 0;
  border-block: 1px solid var(--app-border-subtle);
  padding-block: 0.8rem;
}

.retry-kpis > div {
  display: grid;
  min-width: 0;
  gap: 0.15rem;
  padding-inline: 0.8rem;
}

.retry-kpis > div:first-child {
  padding-left: 0;
}

.retry-kpis > div + div {
  border-left: 1px solid var(--app-border-subtle);
}

.retry-kpis dt,
.retry-rules-table thead,
.retry-detail-card__heading p {
  color: var(--app-text-muted);
  font-size: 0.68rem;
}

.retry-kpis dt {
  font-weight: 700;
  letter-spacing: 0.025em;
  text-transform: uppercase;
}

.retry-kpis dd {
  margin: 0;
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 1rem;
  font-weight: 750;
}

.retry-kpis small,
.retry-rules-table small {
  color: var(--app-text-muted);
  font-size: 0.68rem;
}

.retry-kpi--danger dd,
.retry-table-danger {
  color: var(--app-error);
}

.retry-trend-card,
.retry-detail-card {
  min-width: 0;
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
  background: color-mix(in srgb, var(--app-panel) 72%, transparent);
}

.retry-trend-card {
  padding: 0.9rem;
}

.retry-trend-card__heading,
.retry-detail-card__heading {
  gap: 0.75rem;
}

.retry-trend-card h5,
.retry-detail-card h5,
.retry-empty-state h5 {
  color: var(--app-text);
  font-size: 0.8rem;
  font-weight: 750;
}

.retry-trend-card__heading p,
.retry-detail-card__heading p,
.retry-empty-state p,
.retry-definition {
  color: var(--app-text-muted);
  font-size: 0.72rem;
}

.retry-trend-legend {
  display: flex;
  flex: none;
  gap: 0.75rem;
  color: var(--app-text-muted);
  font-size: 0.68rem;
}

.retry-trend-legend span {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
}

.retry-legend-swatch {
  width: 0.45rem;
  height: 0.45rem;
  border-radius: 2px;
}

.retry-legend-swatch--trigger {
  background: var(--app-warning);
}

.retry-legend-swatch--failed {
  background: var(--app-error);
}

.retry-chart {
  display: grid;
  height: 9.5rem;
  grid-template-columns: 3.2rem minmax(0, 1fr);
  gap: 0.55rem;
  margin-top: 0.8rem;
}

.retry-chart__scale {
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.62rem;
  text-align: right;
}

.retry-chart__plot {
  display: grid;
  min-width: 0;
  grid-auto-columns: minmax(2px, 1fr);
  grid-auto-flow: column;
  align-items: end;
  gap: clamp(2px, 0.35vw, 7px);
  border-bottom: 1px solid var(--app-border);
  background-image: linear-gradient(to bottom, var(--app-border-subtle) 1px, transparent 1px);
  background-size: 100% 50%;
}

.retry-chart__bucket {
  position: relative;
  height: 100%;
}

.retry-chart__bar {
  position: absolute;
  right: 0;
  bottom: 0;
  left: 0;
  min-height: 0;
  border-radius: 2px 2px 0 0;
  background: linear-gradient(180deg, var(--app-warning), color-mix(in srgb, var(--app-warning) 70%, var(--app-accent)));
  opacity: 0.88;
}

.retry-chart__bar--failed {
  z-index: 1;
  background: var(--app-error);
  opacity: 1;
}

.retry-chart__axis {
  display: flex;
  justify-content: space-between;
  margin: 0.3rem 0 0 3.75rem;
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.62rem;
}

.retry-definition {
  margin-top: 0.8rem;
  border-left: 2px solid var(--app-warning);
  padding-left: 0.6rem;
  line-height: 1.5;
}

.retry-details-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.65fr) repeat(2, minmax(13rem, 1fr));
  gap: 0.75rem;
}

.retry-detail-card {
  align-content: start;
  padding: 0.75rem;
}

.retry-detail-card__heading a,
.retry-empty-state > a {
  color: var(--app-accent);
  font-size: 0.72rem;
  font-weight: 700;
  text-decoration: none;
}

.retry-detail-card__heading a:hover,
.retry-empty-state > a:hover {
  text-decoration: underline;
}

.retry-rules-table-shell {
  overflow-x: auto;
  margin-top: 0.65rem;
}

.retry-rules-table {
  width: 100%;
  min-width: 42rem;
  border-collapse: collapse;
  color: var(--app-text);
  font-size: 0.72rem;
}

.retry-rules-table th,
.retry-rules-table td {
  border-top: 1px solid var(--app-border-subtle);
  padding: 0.55rem 0.45rem;
  text-align: right;
  vertical-align: top;
}

.retry-rules-table th:first-child {
  text-align: left;
}

.retry-rules-table tbody th,
.retry-rules-table tbody td strong,
.retry-rules-table tbody td {
  font-family: var(--font-mono);
  font-weight: 650;
}

.retry-rules-table tbody th {
  max-width: 12rem;
}

.retry-rules-table bdi,
.retry-rules-table small {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  unicode-bidi: isolate;
  white-space: nowrap;
}

.retry-failure-list {
  display: grid;
  gap: 0;
  margin: 0.65rem 0 0;
  padding: 0;
  list-style: none;
}

.retry-failure-list button {
  display: grid;
  width: 100%;
  min-width: 0;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 0.2rem 0.5rem;
  border: 0;
  border-top: 1px solid var(--app-border-subtle);
  background: transparent;
  padding: 0.55rem 0;
  color: var(--app-text);
  cursor: pointer;
  text-align: left;
}

.retry-failure-list button:hover,
.retry-failure-row--selected {
  color: var(--app-accent);
}

.retry-failure-row--selected {
  box-shadow: inset 2px 0 0 var(--app-accent);
  padding-left: 0.5rem !important;
}

.retry-failure-list bdi {
  min-width: 0;
  overflow: hidden;
  font-family: var(--font-mono);
  font-size: 0.72rem;
  font-weight: 650;
  text-overflow: ellipsis;
  unicode-bidi: isolate;
  white-space: nowrap;
}

.retry-failure-list strong {
  font-family: var(--font-mono);
  font-size: 0.75rem;
}

.retry-failure-list span {
  grid-column: 1 / -1;
  color: var(--app-text-muted);
  font-size: 0.66rem;
}

.retry-empty-state {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 0.9rem;
  border: 1px dashed var(--app-border);
  border-radius: 6px;
  background: color-mix(in srgb, var(--app-panel) 68%, transparent);
  padding: 1rem;
}

.retry-empty-state__pulse {
  display: grid;
  width: 2.25rem;
  height: 2.25rem;
  place-items: center;
  border: 1px solid color-mix(in srgb, var(--app-warning) 38%, var(--app-border));
  border-radius: 50%;
}

.retry-empty-state__pulse > span {
  width: 0.55rem;
  height: 0.55rem;
  border-radius: 50%;
  background: var(--app-warning);
  box-shadow: 0 0 0 0.3rem color-mix(in srgb, var(--app-warning) 12%, transparent);
}

@media (max-width: 1180px) {
  .retry-kpis {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .retry-kpis > div:nth-child(4) {
    border-left: 0;
  }

  .retry-kpis > div:nth-child(n + 4) {
    border-top: 1px solid var(--app-border-subtle);
    padding-top: 0.65rem;
  }

  .retry-details-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .retry-detail-card--rules {
    grid-column: 1 / -1;
  }
}

@media (max-width: 860px) {
  .retry-details-grid {
    grid-template-columns: 1fr;
  }

  .retry-detail-card--rules {
    grid-column: auto;
  }
}

@media (max-width: 520px) {
  .retry-health__heading,
  .retry-trend-card__heading,
  .retry-detail-card__heading {
    display: grid;
  }

  .retry-signal {
    justify-self: start;
  }

  .retry-kpis {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .retry-kpis > div,
  .retry-kpis > div:nth-child(4) {
    border-top: 1px solid var(--app-border-subtle);
    border-left: 0;
    padding: 0.65rem 0;
  }

  .retry-kpis > div:nth-child(even) {
    border-left: 1px solid var(--app-border-subtle);
    padding-left: 0.65rem;
  }

  .retry-chart {
    grid-template-columns: 2.6rem minmax(0, 1fr);
  }

  .retry-chart__axis {
    margin-left: 3.15rem;
  }

  .retry-empty-state {
    grid-template-columns: auto minmax(0, 1fr);
  }

  .retry-empty-state > a {
    grid-column: 2;
  }
}

@media (pointer: coarse) {
  .retry-failure-list button {
    min-height: 44px;
  }
}
</style>
