<script setup lang="ts">
import { computed, inject, ref } from "vue";
import {
  ArrowRight as ArrowRightIcon,
  CircleAlert as ErrorIcon,
  ShieldCheck as HealthyIcon,
  TriangleAlert as WarningIcon,
} from "@lucide/vue";
import { NDataTable, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import MetricCard from "@/components/ui/MetricCard.vue";
import MetricStrip from "@/components/ui/MetricStrip.vue";
import PageHeader from "@/components/ui/PageHeader.vue";
import SectionCard from "@/components/ui/SectionCard.vue";
import SegmentedControl from "@/components/ui/SegmentedControl.vue";
import { dashboardKey, publicProxyConfigKey } from "@/composables/managementContextKeys";
import {
  ProxyState,
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  type DashboardProxyDimensionSummary,
  type DashboardTrafficBucket,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  bytesPerSecond,
  cacheActivityRequests,
  cacheHitRate,
  cacheLookupRequests,
  filledTrafficBuckets,
  formatByteRate,
  formatBytes,
  formatDuration,
  formatNumber,
  formatPercent,
  fleetUptimePercent,
  nonSuccessRequests,
  requestsPerSecond,
  statusClassCounts,
  successRate,
  windowByLabel,
  type DashboardTrafficBucketView,
} from "@/lib/dashboardStats";
import { deriveOverviewAttention } from "@/lib/overviewAttention";

type HotspotTab = "listeners" | "targets" | "routes" | "agents";

const dashboard = inject(dashboardKey, computed(() => null));
const publicProxyConfig = inject(publicProxyConfigKey, computed(() => null));

const selectedWindowLabel = ref("1h");
const activeHotspotTab = ref<HotspotTab>("listeners");
const windowLabels = ["5m", "1h", "24h", "30d"];

const dashboardValue = computed(() => dashboard?.value ?? null);
const config = computed(() => publicProxyConfig?.value ?? null);
const status = computed(() => dashboardValue.value?.status ?? null);
const agentStreamCapacity = computed(() => dashboardValue.value?.agentStreamCapacity ?? null);
const generatedAt = computed(() => dashboardValue.value?.generatedAtUnixMillis ?? BigInt(Date.now()));
const selectedWindow = computed(() => windowByLabel(dashboardValue.value, selectedWindowLabel.value));
const allWindows = computed(() => dashboardValue.value?.windows ?? []);
const hasAnyProxyEvents = computed(() => allWindows.value.some((window) => window.proxyRequests > 0n));

const proxyState = computed(() => status.value?.proxy?.state ?? ProxyState.UNSPECIFIED);
const proxyIsRunning = computed(() => proxyState.value === ProxyState.RUNNING || status.value?.proxyRunning === true);
const listeners = computed(() => config.value?.listeners ?? []);
const listenerStatuses = computed(() => config.value?.proxy?.listeners ?? status.value?.proxy?.listeners ?? []);
const routeTargets = computed(() => config.value?.routeTargets ?? []);
const routes = computed(() => config.value?.routes ?? []);
const agents = computed(() => config.value?.agents ?? []);
const rateLimitRules = computed(() => config.value?.rateLimitRules ?? []);
const trafficShaperRules = computed(() => config.value?.trafficShaperRules ?? []);
const tlsCertificates = computed(() => config.value?.tlsCertificates ?? []);

const enabledListeners = computed(() => listeners.value.filter((listener) => listener.enabled).length);
const runningListeners = computed(() => listenerStatuses.value.filter((listener) => listener.running && !listener.disabled).length);
const enabledAgents = computed(() => agents.value.filter((agent) => agent.enabled).length);
const connectedAgents = computed(() => agents.value.filter((agent) => agent.connected).length);
const activeAgentRequests = computed(() => agents.value.reduce((sum, agent) => sum + Number(agent.activeRequests || 0n), 0));
const fleetUptime = computed(() => fleetUptimePercent(dashboardValue.value?.agentUptimeSummaries));
const statusCounts = computed(() => statusClassCounts(dashboardValue.value?.statusClasses));
const trafficBuckets = computed(() => filledTrafficBuckets(dashboardValue.value?.trafficBuckets, generatedAt.value));
const maxBucketRequests = computed(() => Math.max(1, ...trafficBuckets.value.map((bucket) => Number(bucket.requests))));
const cacheLookups = computed(() => cacheLookupRequests(selectedWindow.value));
const cacheActivity = computed(() => cacheActivityRequests(selectedWindow.value));
const cacheHasActivity = computed(() => cacheActivity.value > 0n);
const attentionItems = computed(() => deriveOverviewAttention({
  status: status.value,
  config: config.value,
  trafficWindow: selectedWindow.value,
}));

const hotspotTabs: Array<{ key: HotspotTab; label: string }> = [
  { key: "listeners", label: "Listeners" },
  { key: "targets", label: "Targets" },
  { key: "routes", label: "Routes" },
  { key: "agents", label: "Agents" },
];

const hotspotRows = computed(() => {
  const current = dashboardValue.value;
  if (!current) return [];
  switch (activeHotspotTab.value) {
    case "targets":
      return current.topRouteTargets;
    case "routes":
      return current.topRoutes;
    case "agents":
      return current.topAgents;
    default:
      return current.topListeners;
  }
});
const hotspotColumns = computed<DataTableColumns<DashboardProxyDimensionSummary>>(() => [
  {
    title: "Name",
    key: "name",
    minWidth: 200,
    ellipsis: { tooltip: true },
    render: (row) => row.label,
  },
  {
    title: "Requests",
    key: "requests",
    width: 100,
    render: (row) => formatNumber(row.requests),
  },
  {
    title: "Success",
    key: "success",
    width: 90,
    render: rowSuccess,
  },
  {
    title: "Non-success",
    key: "nonSuccess",
    width: 100,
    render: (row) => formatNumber(rowNonSuccess(row)),
  },
  {
    title: "Avg latency",
    key: "avgLatency",
    width: 100,
    render: (row) => formatDuration(row.avgDurationMs),
  },
  {
    title: "Down",
    key: "down",
    width: 90,
    render: (row) => formatBytes(row.responseBytes),
  },
  {
    title: "Up",
    key: "up",
    width: 90,
    render: (row) => formatBytes(row.requestBytes),
  },
]);

const configSnapshot = computed(() => {
  const directTargets = routeTargets.value.filter((target) => target.targetType === PublicRouteTargetType.PROXY && target.transport !== PublicRouteTargetTransport.AGENT).length;
  const agentTargets = routeTargets.value.filter((target) => target.targetType === PublicRouteTargetType.PROXY && target.transport === PublicRouteTargetTransport.AGENT).length;
  const staticTargets = routeTargets.value.filter((target) => target.targetType === PublicRouteTargetType.STATIC).length;
  return [
    { label: "Listeners", value: `${enabledListeners.value}/${listeners.value.length}`, detail: `${runningListeners.value} running` },
    { label: "Targets", value: formatNumber(BigInt(routeTargets.value.length)), detail: `${directTargets} direct, ${agentTargets} agent, ${staticTargets} static` },
    { label: "Routes", value: `${routes.value.filter((route) => route.enabled).length}/${routes.value.length}`, detail: "enabled / total" },
    { label: "Rate limits", value: `${rateLimitRules.value.filter((rule) => rule.enabled).length}/${rateLimitRules.value.length}`, detail: "enabled / total" },
    { label: "Shapers", value: `${trafficShaperRules.value.filter((rule) => rule.enabled).length}/${trafficShaperRules.value.length}`, detail: "enabled / total" },
    { label: "TLS", value: `${tlsCertificates.value.filter((cert) => cert.enabled).length}/${tlsCertificates.value.length}`, detail: "enabled / total" },
  ];
});

function proxyStateLabel(state: ProxyState): string {
  switch (state) {
    case ProxyState.STOPPED:
      return "Stopped";
    case ProxyState.STARTING:
      return "Starting";
    case ProxyState.RUNNING:
      return "Running";
    case ProxyState.STOPPING:
      return "Stopping";
    case ProxyState.ERROR:
      return "Error";
    default:
      return status.value?.proxyRunning ? "Running" : "Unknown";
  }
}

function proxyStateTone(state: ProxyState): "success" | "warning" | "error" | "muted" {
  const signal = proxyStateClass(state);
  if (signal === "signal-good") return "success";
  if (signal === "signal-warn") return "warning";
  if (signal === "signal-bad") return "error";
  return "muted";
}

const windowOptions = windowLabels.map((label) => ({ value: label, label }));

function proxyStateClass(state: ProxyState): string {
  if (state === ProxyState.RUNNING || proxyIsRunning.value) return "signal-good";
  if (state === ProxyState.STARTING || state === ProxyState.STOPPING) return "signal-warn";
  if (state === ProxyState.ERROR) return "signal-bad";
  return "signal-muted";
}

function selectedRequestRate(): string {
  const rate = requestsPerSecond(selectedWindow.value, generatedAt.value);
  if (rate === null) return "—";
  return `${rate >= 10 ? rate.toFixed(0) : rate.toFixed(2)} /s`;
}

function selectedDownloadRate(): string {
  return formatByteRate(bytesPerSecond(selectedWindow.value?.proxyResponseBytes, selectedWindow.value, generatedAt.value));
}

function selectedUploadRate(): string {
  return formatByteRate(bytesPerSecond(selectedWindow.value?.proxyRequestBytes, selectedWindow.value, generatedAt.value));
}

function agentsMetricSubline(): string {
  const active = `${formatNumber(activeAgentRequests.value)} active requests`;
  if (fleetUptime.value === null) return active;
  return `${formatPercent(fleetUptime.value)} uptime / ${active}`;
}

function rowNonSuccess(row: DashboardProxyDimensionSummary): bigint {
  return row.clientError + row.serverError;
}

function rowSuccess(row: DashboardProxyDimensionSummary): string {
  if (row.requests === 0n) return "—";
  return formatPercent(Number(row.success) / Math.max(1, Number(row.requests)));
}

function errorKindLabel(value: string): string {
  return value
    .split("_")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function bucketBarStyle(bucket: DashboardTrafficBucketView): Record<string, string> {
  if (bucket.requests === 0n) return { transform: "scaleY(0)" };
  const percent = Math.max(8, Math.round((Number(bucket.requests) / maxBucketRequests.value) * 100));
  return { transform: `scaleY(${(percent / 100).toString()})` };
}

function bucketErrorHeight(bucket: DashboardTrafficBucketView): string {
  if (bucket.requests === 0n || bucket.nonSuccess === 0n) return "0%";
  return `${Math.max(12, Math.round((Number(bucket.nonSuccess) / Number(bucket.requests)) * 100)).toString()}%`;
}

function bucketTitle(bucket: DashboardTrafficBucket | DashboardTrafficBucketView): string {
  const start = new Date(Number(bucket.bucketUnixMillis));
  const nonSuccess = "nonSuccess" in bucket ? bucket.nonSuccess : bucket.clientError + bucket.serverError;
  return `${start.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}: ${formatNumber(bucket.requests)} requests, ${formatNumber(nonSuccess)} non-success, down ${formatBytes(bucket.responseBytes)}, up ${formatBytes(bucket.requestBytes)}`;
}

function hotspotRowKey(row: DashboardProxyDimensionSummary): string {
  return `${activeHotspotTab.value}-${row.id.toString()}-${row.label}`;
}
</script>

<template>
  <div v-if="dashboardValue && status" class="overview-page">
    <PageHeader title="Proxy Overview" description="Health, traffic, and configuration signals for the selected environment.">
      <template #actions>
        <SegmentedControl v-model="selectedWindowLabel" :options="windowOptions" label="Dashboard window" />
      </template>
    </PageHeader>

    <MetricStrip variant="tiles" label="Proxy health" class="overview-health">
      <MetricCard label="Proxy health" :value="proxyStateLabel(proxyState)" :tone="proxyStateTone(proxyState)" dot :detail="`${runningListeners}/${enabledListeners} listeners running`" />
      <MetricCard label="Requests" :value="formatNumber(selectedWindow?.proxyRequests)" :detail="selectedRequestRate()" />
      <MetricCard label="Success" :value="selectedWindow?.proxyRequests ? formatPercent(successRate(selectedWindow)) : '—'" :detail="selectedWindow?.proxyRequests ? `${formatNumber(nonSuccessRequests(selectedWindow))} non-success` : 'Needs traffic'" />
      <MetricCard label="Latency" :value="formatDuration(selectedWindow?.proxyAvgDurationMs)" :detail="`max ${formatDuration(selectedWindow?.proxyMaxDurationMs)}`" />
      <MetricCard label="Throughput" :value="selectedDownloadRate()" :detail="`up ${selectedUploadRate()}`" />
      <MetricCard label="Agents" :value="`${connectedAgents}/${enabledAgents}`" :detail="agentsMetricSubline()" />
      <MetricCard
        label="Tunnel streams"
        :value="`${formatNumber(agentStreamCapacity?.totalInUse)}/${formatNumber(agentStreamCapacity?.totalCapacity)}`"
        :detail="`pooled ${formatNumber(agentStreamCapacity?.pooledInUse)}/${formatNumber(agentStreamCapacity?.pooledCapacity)} · ${formatNumber(agentStreamCapacity?.closing)} closing · ${formatNumber(agentStreamCapacity?.waiters)} waiting`"
      />
    </MetricStrip>

    <SectionCard title="Needs attention" description="Runtime and configuration signals that may affect public traffic." flush aria-live="polite">
      <template v-if="attentionItems.length" #badge>
        <NTag size="small" :bordered="false" :type="attentionItems.some((item) => item.severity === 'error') ? 'error' : 'warning'">
          {{ attentionItems.length }} {{ attentionItems.length === 1 ? "signal" : "signals" }}
        </NTag>
      </template>
      <ul v-if="attentionItems.length" class="attention-list">
        <li v-for="item in attentionItems" :key="item.key" class="attention-row" :class="`attention-row--${item.severity}`">
          <ErrorIcon v-if="item.severity === 'error'" class="attention-row__icon" aria-hidden="true" />
          <WarningIcon v-else class="attention-row__icon" aria-hidden="true" />
          <div class="attention-row__copy">
            <h3>{{ item.title }}</h3>
            <p>{{ item.detail }}</p>
          </div>
          <router-link :to="item.actionRoute" class="attention-row__action">
            {{ item.actionLabel }}
            <ArrowRightIcon aria-hidden="true" />
          </router-link>
        </li>
      </ul>
      <div v-else class="attention-row attention-row--healthy">
        <HealthyIcon class="attention-row__icon" aria-hidden="true" />
        <div class="attention-row__copy">
          <h3>No urgent signals</h3>
          <p>The proxy, enabled listeners, connected agents, and traffic policy checks look healthy.</p>
        </div>
        <router-link to="/monitor/diagnostics/overview" class="attention-row__action">
          Open diagnostics
          <ArrowRightIcon aria-hidden="true" />
        </router-link>
      </div>
    </SectionCard>

    <div class="overview-main-grid">
      <SectionCard title="Traffic trend" description="Last 60 minutes, grouped into five-minute buckets.">
        <template #actions>
          <span class="overview-meta">{{ formatNumber(trafficBuckets.reduce((sum, bucket) => sum + bucket.requests, 0n)) }} requests</span>
        </template>
        <template v-if="trafficBuckets.some((bucket) => bucket.requests > 0n)">
          <div class="trend-legend" aria-hidden="true">
            <span><i class="trend-legend__swatch trend-legend__swatch--requests" />Requests</span>
            <span><i class="trend-legend__swatch trend-legend__swatch--errors" />Non-success</span>
          </div>
          <div class="trend-bars" aria-hidden="true">
            <div
              v-for="bucket in trafficBuckets"
              :key="bucket.bucketUnixMillis.toString()"
              class="trend-slot"
              :title="bucketTitle(bucket)"
            >
              <div class="trend-bar" :style="bucketBarStyle(bucket)">
                <div class="trend-error" :style="{ height: bucketErrorHeight(bucket) }" />
              </div>
            </div>
          </div>
          <ul class="visually-hidden" aria-label="Traffic trend values">
            <li v-for="bucket in trafficBuckets" :key="`accessible-${bucket.bucketUnixMillis.toString()}`">
              {{ bucketTitle(bucket) }}
            </li>
          </ul>
        </template>
        <div v-else class="stable-empty">
          <strong>{{ hasAnyProxyEvents ? "No proxy traffic in the last hour." : "No proxy requests recorded yet." }}</strong>
          <span v-if="!hasAnyProxyEvents">Start a listener and send traffic through the proxy to populate these metrics.</span>
        </div>
      </SectionCard>

      <SectionCard title="Problem signals" description="Selected window plus last-hour error kinds.">
        <template #actions>
          <NTag size="small" :bordered="false" :type="selectedWindow?.proxySlowRequests ? 'warning' : 'default'">
            {{ formatNumber(selectedWindow?.proxySlowRequests) }} slow
          </NTag>
          <router-link to="/monitor/diagnostics/overview" class="overview-link">View diagnostics</router-link>
        </template>
        <dl class="status-class-grid">
          <div v-for="label in ['2xx', '3xx', '4xx', '5xx']" :key="label" class="status-class" :class="{ [`status-class--${label}`]: Boolean(statusCounts[label]) }">
            <dt>{{ label }}</dt>
            <dd>{{ formatNumber(statusCounts[label]) }}</dd>
          </div>
        </dl>
        <div class="error-list">
          <div v-for="error in dashboardValue.topErrorKinds" :key="error.label" class="error-row" :title="error.label">
            <span>{{ errorKindLabel(error.label) }}</span>
            <strong>{{ formatNumber(error.requests) }}</strong>
          </div>
          <div v-if="!dashboardValue.topErrorKinds.length" class="stable-empty stable-empty--compact">No proxy failures in the last hour.</div>
        </div>
      </SectionCard>
    </div>

    <SectionCard title="Cache behavior" description="Selected window, based on retained proxy request events.">
      <template #badge>
        <NTag size="small" :bordered="false" :type="cacheHasActivity ? 'default' : 'warning'">{{ formatNumber(cacheActivity) }} cache events</NTag>
      </template>
      <MetricStrip variant="tiles" label="Cache behavior">
        <MetricCard label="Hit rate" :value="cacheLookups ? formatPercent(cacheHitRate(selectedWindow)) : '—'" :detail="`${formatNumber(cacheLookups)} lookups`" />
        <MetricCard label="Hits" :value="formatNumber(selectedWindow?.proxyCacheHits)" :detail="`served ${formatBytes(selectedWindow?.proxyCacheHitBytes)}`" />
        <MetricCard label="Misses" :value="formatNumber(selectedWindow?.proxyCacheMisses)" :detail="`${formatNumber(selectedWindow?.proxyCacheStored)} stored, ${formatBytes(selectedWindow?.proxyCacheStoredBytes)}`" />
        <MetricCard label="Bypass" :value="formatNumber(selectedWindow?.proxyCacheBypasses)" :detail="`${formatNumber(selectedWindow?.proxyCacheStoreFailed)} store failed`" />
      </MetricStrip>
      <p v-if="!cacheHasActivity" class="overview-meta cache-empty">No cache activity in this window.</p>
    </SectionCard>

    <SectionCard title="Hotspots" description="Top in the last hour." flush>
      <template #actions>
        <SegmentedControl v-model="activeHotspotTab" :options="hotspotTabs.map((tab) => ({ value: tab.key, label: tab.label }))" label="Hotspot dimension" />
      </template>
      <NDataTable
        :columns="hotspotColumns"
        :data="hotspotRows"
        :row-key="hotspotRowKey"
        :pagination="false"
        :bordered="false"
        :single-line="false"
        :scroll-x="770"
        size="small"
      />
    </SectionCard>

    <SectionCard title="Configuration snapshot" description="Current proxy objects loaded in management config.">
      <MetricStrip label="Configuration snapshot">
        <MetricCard v-for="item in configSnapshot" :key="item.label" :label="item.label" :value="String(item.value)" :detail="item.detail" />
      </MetricStrip>
    </SectionCard>
  </div>
</template>

<style scoped>
.overview-page {
  display: grid;
  min-width: 0;
  gap: 1.25rem;
}

.overview-health {
  grid-template-columns: repeat(auto-fit, minmax(8rem, 1fr));
}

.overview-health :deep(.metric-card__detail) {
  white-space: normal;
}

.overview-meta {
  color: var(--app-text-muted);
  font-size: 0.8125rem;
}

.overview-link {
  color: var(--app-accent);
  font-size: 0.8125rem;
  font-weight: 500;
}

.overview-link:hover {
  text-decoration: underline;
}

.attention-list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.attention-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 0.875rem;
  padding: 0.875rem 1.25rem;
}

.attention-list .attention-row + .attention-row {
  border-top: 1px solid var(--app-border-subtle);
}

.attention-row__icon {
  width: 1.125rem;
  height: 1.125rem;
  color: var(--app-text-muted);
}

.attention-row--error .attention-row__icon { color: var(--app-error); }
.attention-row--warning .attention-row__icon { color: var(--app-warning); }
.attention-row--healthy .attention-row__icon { color: var(--app-success); }

.attention-row__copy {
  display: grid;
  min-width: 0;
  gap: 0.125rem;
}

.attention-row__copy h3 {
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
}

.attention-row__copy p {
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  overflow-wrap: anywhere;
}

.attention-row__action {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  border-radius: 6px;
  color: var(--app-accent);
  font-size: 0.8125rem;
  font-weight: 500;
  padding: 0.25rem 0.375rem;
  white-space: nowrap;
}

.attention-row__action:hover {
  background: var(--app-accent-soft);
}

.attention-row__action:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: 1px;
}

.attention-row__action svg {
  width: 0.875rem;
  height: 0.875rem;
}

.overview-main-grid {
  display: grid;
  gap: 1.25rem;
  grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr);
}

.trend-legend {
  display: flex;
  gap: 1rem;
  margin-bottom: 0.75rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.trend-legend span {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
}

.trend-legend__swatch {
  width: 0.625rem;
  height: 0.625rem;
  border-radius: 2px;
}

.trend-legend__swatch--requests { background: var(--app-accent); }
.trend-legend__swatch--errors { background: var(--app-error); }

.trend-bars {
  display: grid;
  height: 11rem;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  align-items: end;
  gap: 2px;
  border-bottom: 1px solid var(--app-border-subtle);
}

.trend-slot {
  display: flex;
  min-width: 0;
  height: 100%;
  align-items: end;
  justify-content: center;
  border-radius: 4px 4px 0 0;
}

.trend-slot:hover {
  background: var(--app-hover);
}

.trend-bar {
  position: relative;
  width: min(100%, 1.75rem);
  height: 100%;
  overflow: hidden;
  border-radius: 4px 4px 0 0;
  background: var(--app-accent);
  transform-origin: bottom center;
  transition: transform 160ms ease;
}

.trend-error {
  position: absolute;
  right: 0;
  bottom: 0;
  left: 0;
  border-top: 2px solid var(--app-panel);
  background: var(--app-error);
}

.status-class-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 0.5rem;
}

.status-class {
  display: grid;
  gap: 0.125rem;
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 0.625rem 0.75rem;
}

.status-class dt {
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.75rem;
}

.status-class dd {
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 1rem;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.status-class--4xx dd { color: var(--app-warning); }
.status-class--5xx dd { color: var(--app-error); }

.error-list {
  display: grid;
  margin-top: 0.75rem;
}

.error-row {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  border-top: 1px solid var(--app-border-subtle);
  padding: 0.5rem 0;
  font-size: 0.8125rem;
}

.error-row span {
  min-width: 0;
  overflow: hidden;
  color: var(--app-text-muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.error-row strong {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.stable-empty {
  display: grid;
  min-height: 9rem;
  place-content: center;
  gap: 0.25rem;
  border-radius: 8px;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  padding: 1rem;
  text-align: center;
}

.stable-empty strong {
  color: var(--app-text);
  font-weight: 600;
}

.stable-empty--compact {
  min-height: 0;
  margin-top: 0.25rem;
  background: transparent;
  padding: 0.5rem 0;
  text-align: left;
  place-content: start;
}

.cache-empty {
  margin-top: 0.75rem;
}

@media (max-width: 1023px) {
  .overview-main-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}

@media (max-width: 639px) {
  .attention-row {
    grid-template-columns: auto minmax(0, 1fr);
    padding-inline: 1rem;
  }

  .attention-row__action {
    grid-column: 2;
    justify-self: start;
  }

  .status-class-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
