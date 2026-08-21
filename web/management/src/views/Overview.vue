<script setup lang="ts">
import { computed, inject, ref } from "vue";
import {
  ArrowRight as ArrowRightIcon,
  CircleAlert as ErrorIcon,
  ShieldCheck as HealthyIcon,
  TriangleAlert as WarningIcon,
} from "@lucide/vue";
import { NButton, NButtonGroup, NDataTable } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
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
    minWidth: 220,
    ellipsis: { tooltip: true },
    render: (row) => row.label,
  },
  {
    title: "Requests",
    key: "requests",
    width: 130,
    render: (row) => formatNumber(row.requests),
  },
  {
    title: "Success",
    key: "success",
    width: 110,
    render: rowSuccess,
  },
  {
    title: "Non-success",
    key: "nonSuccess",
    width: 130,
    render: (row) => formatNumber(rowNonSuccess(row)),
  },
  {
    title: "Avg latency",
    key: "avgLatency",
    width: 130,
    render: (row) => formatDuration(row.avgDurationMs),
  },
  {
    title: "Down",
    key: "down",
    width: 120,
    render: (row) => formatBytes(row.responseBytes),
  },
  {
    title: "Up",
    key: "up",
    width: 120,
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

function proxyStateClass(state: ProxyState): string {
  if (state === ProxyState.RUNNING || proxyIsRunning.value) return "signal-good";
  if (state === ProxyState.STARTING || state === ProxyState.STOPPING) return "signal-warn";
  if (state === ProxyState.ERROR) return "signal-bad";
  return "signal-muted";
}

function selectedRequestRate(): string {
  const rate = requestsPerSecond(selectedWindow.value, generatedAt.value);
  if (rate === null) return "-";
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
  if (row.requests === 0n) return "-";
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
    <section class="overview-header">
      <div>
        <h1>Proxy Overview</h1>
        <p>Health, traffic, and configuration signals for the selected environment.</p>
      </div>
      <NButtonGroup class="window-tabs" role="group" aria-label="Dashboard window" size="small">
        <NButton
          v-for="window in windowLabels"
          :key="window"
          attr-type="button"
          :aria-pressed="selectedWindowLabel === window"
          :type="selectedWindowLabel === window ? 'primary' : 'default'"
          @click="selectedWindowLabel = window"
        >
          {{ window }}
        </NButton>
      </NButtonGroup>
    </section>

    <section class="status-summary" aria-labelledby="proxy-health-title">
      <div class="status-summary__identity">
        <p id="proxy-health-title">Proxy health</p>
        <div class="status-summary__state">
          <span class="signal-dot" :class="proxyStateClass(proxyState)" aria-hidden="true" />
          <strong>{{ proxyStateLabel(proxyState) }}</strong>
        </div>
        <span>{{ runningListeners }}/{{ enabledListeners }} listeners running</span>
      </div>

      <dl class="status-summary__facts">
        <div>
          <dt>Requests</dt>
          <dd>{{ formatNumber(selectedWindow?.proxyRequests) }}</dd>
          <small>{{ selectedRequestRate() }}</small>
        </div>
        <div>
          <dt>Success</dt>
          <dd>{{ formatPercent(successRate(selectedWindow)) }}</dd>
          <small>{{ formatNumber(nonSuccessRequests(selectedWindow)) }} non-success</small>
        </div>
        <div>
          <dt>Latency</dt>
          <dd>{{ formatDuration(selectedWindow?.proxyAvgDurationMs) }}</dd>
          <small>pMax {{ formatDuration(selectedWindow?.proxyMaxDurationMs) }}</small>
        </div>
        <div>
          <dt>Throughput</dt>
          <dd>{{ selectedDownloadRate() }}</dd>
          <small>up {{ selectedUploadRate() }}</small>
        </div>
        <div>
          <dt>Agents</dt>
          <dd>{{ connectedAgents }}/{{ enabledAgents }}</dd>
          <small>{{ agentsMetricSubline() }}</small>
        </div>
      </dl>
    </section>

    <section class="attention-panel" aria-labelledby="attention-title" aria-live="polite">
      <div class="attention-panel__heading">
        <div>
          <h2 id="attention-title">Needs attention</h2>
          <p>Runtime and configuration signals that may affect public traffic.</p>
        </div>
        <span v-if="attentionItems.length" class="attention-panel__count">
          {{ attentionItems.length }} {{ attentionItems.length === 1 ? "signal" : "signals" }}
        </span>
      </div>

      <div v-if="attentionItems.length" class="attention-list">
        <article v-for="item in attentionItems" :key="item.key" class="attention-row" :class="`attention-row--${item.severity}`">
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
        </article>
      </div>

      <div v-else class="attention-healthy">
        <HealthyIcon aria-hidden="true" />
        <div>
          <h3>No urgent signals</h3>
          <p>The proxy, enabled listeners, connected agents, and traffic policy checks look healthy.</p>
        </div>
        <router-link to="/monitor/diagnostics" class="attention-row__action">
          Open diagnostics
          <ArrowRightIcon aria-hidden="true" />
        </router-link>
      </div>
    </section>

    <section v-if="!hasAnyProxyEvents" class="empty-panel">
      <div>
        <h2>No proxy requests recorded yet.</h2>
        <p>Start a listener and send traffic through the proxy to populate these metrics.</p>
      </div>
    </section>

    <section class="overview-main-grid">
      <div class="dashboard-panel trend-panel">
        <div class="panel-heading">
          <div>
            <h2>Traffic Trend</h2>
            <p>Last 60 minutes, grouped into five-minute buckets.</p>
          </div>
          <span class="signal-pill">{{ formatNumber(trafficBuckets.reduce((sum, bucket) => sum + bucket.requests, 0n)) }} req</span>
        </div>

        <div v-if="trafficBuckets.some((bucket) => bucket.requests > 0n)" class="trend-bars" aria-hidden="true">
          <div
            v-for="bucket in trafficBuckets"
            :key="bucket.bucketUnixMillis.toString()"
            class="trend-slot"
            :title="bucketTitle(bucket)"
          >
            <div class="trend-track">
              <div class="trend-bar" :style="bucketBarStyle(bucket)">
                <div class="trend-error" :style="{ height: bucketErrorHeight(bucket) }" />
              </div>
            </div>
          </div>
        </div>
        <ul v-if="trafficBuckets.some((bucket) => bucket.requests > 0n)" class="visually-hidden" aria-label="Traffic trend values">
          <li v-for="bucket in trafficBuckets" :key="`accessible-${bucket.bucketUnixMillis.toString()}`">
            {{ bucketTitle(bucket) }}
          </li>
        </ul>
        <div v-else class="stable-empty">No proxy traffic in the last hour.</div>
      </div>

      <div class="dashboard-panel">
        <div class="panel-heading">
          <div>
            <h2>Problem Signals</h2>
            <p>Selected window plus last-hour error kinds.</p>
          </div>
          <div class="panel-actions">
            <router-link to="/monitor/diagnostics" class="diagnostics-link">View diagnostics</router-link>
            <span class="signal-pill" :class="selectedWindow?.proxySlowRequests ? 'warn' : ''">
              {{ formatNumber(selectedWindow?.proxySlowRequests) }} slow
            </span>
          </div>
        </div>

        <div class="status-class-grid">
          <div v-for="label in ['2xx', '3xx', '4xx', '5xx']" :key="label" class="status-class">
            <span>{{ label }}</span>
            <strong>{{ formatNumber(statusCounts[label]) }}</strong>
          </div>
        </div>

        <div class="error-list">
          <div v-for="error in dashboardValue.topErrorKinds" :key="error.label" class="error-row" :title="error.label">
            <span>{{ errorKindLabel(error.label) }}</span>
            <strong>{{ formatNumber(error.requests) }}</strong>
          </div>
          <div v-if="!dashboardValue.topErrorKinds.length" class="stable-empty compact">No proxy failures in the last hour.</div>
        </div>
      </div>
    </section>

    <section class="dashboard-panel cache-panel">
      <div class="panel-heading">
        <div>
          <h2>Cache Behavior</h2>
          <p>Selected window, based on retained proxy request events.</p>
        </div>
        <span class="signal-pill" :class="cacheHasActivity ? '' : 'warn'">
          {{ formatNumber(cacheActivity) }} cache events
        </span>
      </div>

      <div class="cache-stat-grid">
        <div class="cache-stat">
          <span>Hit rate</span>
          <strong>{{ formatPercent(cacheHitRate(selectedWindow)) }}</strong>
          <small>{{ formatNumber(cacheLookups) }} lookups</small>
        </div>
        <div class="cache-stat">
          <span>Hits</span>
          <strong>{{ formatNumber(selectedWindow?.proxyCacheHits) }}</strong>
          <small>served {{ formatBytes(selectedWindow?.proxyCacheHitBytes) }}</small>
        </div>
        <div class="cache-stat">
          <span>Misses</span>
          <strong>{{ formatNumber(selectedWindow?.proxyCacheMisses) }}</strong>
          <small>{{ formatNumber(selectedWindow?.proxyCacheStored) }} stored, {{ formatBytes(selectedWindow?.proxyCacheStoredBytes) }}</small>
        </div>
        <div class="cache-stat">
          <span>Bypass</span>
          <strong>{{ formatNumber(selectedWindow?.proxyCacheBypasses) }}</strong>
          <small>{{ formatNumber(selectedWindow?.proxyCacheStoreFailed) }} store failed</small>
        </div>
      </div>

      <p v-if="!cacheHasActivity" class="cache-empty">No cache activity in this window.</p>
    </section>

    <section class="dashboard-panel">
      <div class="panel-heading">
        <div>
          <h2>Hotspots</h2>
          <p>Top in the last hour.</p>
        </div>
        <NButtonGroup class="mini-tabs" role="group" aria-label="Hotspot dimension" size="small">
          <NButton
            v-for="tab in hotspotTabs"
            :key="tab.key"
            attr-type="button"
            :aria-pressed="activeHotspotTab === tab.key"
            :type="activeHotspotTab === tab.key ? 'primary' : 'default'"
            @click="activeHotspotTab = tab.key"
          >
            {{ tab.label }}
          </NButton>
        </NButtonGroup>
      </div>

      <NDataTable
        :columns="hotspotColumns"
        :data="hotspotRows"
        :row-key="hotspotRowKey"
        :pagination="false"
        :bordered="false"
        :single-line="false"
        :scroll-x="960"
        size="small"
      />
    </section>

    <section class="dashboard-panel">
      <div class="panel-heading">
        <div>
          <h2>Configuration Snapshot</h2>
          <p>Current proxy objects loaded in management config.</p>
        </div>
      </div>
      <div class="snapshot-grid">
        <div v-for="item in configSnapshot" :key="item.label" class="snapshot-item">
          <span>{{ item.label }}</span>
          <strong>{{ item.value }}</strong>
          <small>{{ item.detail }}</small>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.overview-page {
  display: grid;
  gap: 1.5rem;
}

.overview-header {
  display: flex;
  align-items: end;
  justify-content: space-between;
  gap: 1rem;
}

.overview-header h3 {
  color: var(--app-text);
  font-size: 1.25rem;
  font-weight: 700;
  letter-spacing: 0;
}

.overview-header p,
.panel-heading p,
.metric-subline {
  color: var(--app-text-muted);
  font-size: 0.82rem;
}

.overview-grid {
  display: grid;
  gap: 0.75rem;
}

.metric-strip {
  grid-template-columns: repeat(1, minmax(0, 1fr));
}

.metric-card,
.dashboard-panel,
.empty-panel {
  min-width: 0;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
}

.metric-card {
  display: grid;
  gap: 0.35rem;
  padding: 1rem;
}

.metric-kicker {
  color: var(--app-text-muted);
  font-size: 0.72rem;
  font-weight: 700;
  letter-spacing: 0;
  text-transform: uppercase;
}

.metric-value {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.45rem;
  color: var(--app-text);
  font-size: 1.25rem;
  font-weight: 700;
  letter-spacing: 0;
}

.window-tabs,
.mini-tabs {
  flex-shrink: 0;
}

.signal-dot {
  width: 0.55rem;
  height: 0.55rem;
  flex: 0 0 auto;
  border-radius: 999px;
}

.signal-good {
  background: var(--app-success);
}

.signal-warn {
  background: var(--app-warning);
}

.signal-bad {
  background: var(--app-error);
}

.signal-muted {
  background: var(--app-text-muted);
}

.overview-main-grid {
  display: grid;
  gap: 1rem;
}

.dashboard-panel {
  display: grid;
  gap: 1rem;
  padding: 1rem;
}

.panel-heading {
  display: flex;
  align-items: start;
  justify-content: space-between;
  gap: 1rem;
}

.panel-actions {
  display: inline-flex;
  flex-wrap: wrap;
  justify-content: end;
  gap: 0.45rem;
}

.diagnostics-link {
  display: inline-flex;
  min-height: 1.55rem;
  align-items: center;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel);
  color: var(--app-text);
  font-size: 0.72rem;
  font-weight: 700;
  padding: 0 0.55rem;
  white-space: nowrap;
}

.diagnostics-link:hover {
  background: var(--app-border-subtle);
}

.panel-heading h2,
.empty-panel h2 {
  color: var(--app-text);
  font-size: 0.95rem;
  font-weight: 700;
  letter-spacing: 0;
}

.signal-pill {
  display: inline-flex;
  min-height: 1.55rem;
  align-items: center;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-panel-muted);
  color: var(--app-text);
  font-size: 0.72rem;
  font-weight: 650;
  padding: 0 0.55rem;
  white-space: nowrap;
}

.signal-pill.warn {
  border-color: rgb(245 158 11 / 55%);
  color: var(--app-warning);
}

.trend-bars {
  display: grid;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  gap: 0.35rem;
  height: 12rem;
  align-items: end;
}

.trend-slot,
.trend-track {
  min-width: 0;
  height: 100%;
}

.trend-track {
  display: flex;
  align-items: end;
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 4px;
  background: var(--app-panel-muted);
}

.trend-bar {
  position: relative;
  width: 100%;
  height: 100%;
  min-height: 0;
  background: var(--app-accent);
  transform-origin: bottom center;
  transition: transform 160ms ease;
}

.trend-error {
  position: absolute;
  right: 0;
  bottom: 0;
  left: 0;
  background: var(--app-error);
}

.status-class-grid,
.snapshot-grid,
.cache-stat-grid {
  display: grid;
  gap: 0.6rem;
}

.status-class-grid {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.cache-stat-grid {
  grid-template-columns: repeat(1, minmax(0, 1fr));
}

.status-class,
.snapshot-item,
.cache-stat {
  display: grid;
  gap: 0.2rem;
  min-width: 0;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
  padding: 0.75rem;
}

.status-class span,
.snapshot-item span,
.cache-stat span {
  color: var(--app-text-muted);
  font-size: 0.72rem;
  font-weight: 650;
}

.status-class strong,
.snapshot-item strong,
.cache-stat strong {
  color: var(--app-text);
  font-size: 1rem;
  font-weight: 700;
}

.snapshot-item small,
.cache-stat small {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 0.72rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cache-empty {
  color: var(--app-text-muted);
  font-size: 0.8rem;
}

.error-list {
  display: grid;
  gap: 0.45rem;
}

.error-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 0.75rem;
  align-items: center;
  border-top: 1px solid var(--app-border);
  padding-top: 0.45rem;
}

.error-row span {
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.8rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.error-row strong {
  color: var(--app-error);
  font-size: 0.8rem;
  font-weight: 650;
}

.table-scroll {
  overflow-x: auto;
}

.hotspot-table {
  width: 100%;
  min-width: 760px;
  border-collapse: collapse;
  font-size: 0.8rem;
}

.hotspot-table th,
.hotspot-table td {
  border-top: 1px solid var(--app-border);
  padding: 0.65rem 0.5rem;
  text-align: right;
  white-space: nowrap;
}

.hotspot-table th {
  color: var(--app-text-muted);
  font-size: 0.68rem;
  font-weight: 700;
  letter-spacing: 0;
  text-transform: uppercase;
}

.hotspot-table th:first-child,
.hotspot-table td:first-child {
  text-align: left;
}

.name-cell {
  max-width: 18rem;
  overflow: hidden;
  color: var(--app-text);
  font-weight: 650;
  text-overflow: ellipsis;
}

.empty-row,
.stable-empty {
  color: var(--app-text-muted);
  font-size: 0.82rem;
  text-align: center;
}

.stable-empty {
  display: grid;
  min-height: 8rem;
  place-items: center;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
}

.stable-empty.compact {
  min-height: 3rem;
}

.empty-panel {
  padding: 1rem;
}

.empty-panel p {
  margin-top: 0.25rem;
  color: var(--app-text-muted);
  font-size: 0.82rem;
}

.snapshot-grid {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

@media (min-width: 640px) {
  .metric-strip {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .snapshot-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .cache-stat-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }
}

@media (min-width: 1024px) {
  .metric-strip {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .overview-main-grid {
    grid-template-columns: minmax(0, 1.35fr) minmax(20rem, 0.65fr);
  }
}

@media (min-width: 1280px) {
  .metric-strip {
    grid-template-columns: repeat(6, minmax(0, 1fr));
  }

  .snapshot-grid {
    grid-template-columns: repeat(6, minmax(0, 1fr));
  }
}

@media (max-width: 720px) {
  .overview-header,
  .panel-heading {
    align-items: stretch;
    flex-direction: column;
  }

  .window-tabs,
  .mini-tabs {
    width: 100%;
  }

  .status-class-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .trend-bars {
    gap: 0.25rem;
    height: 9rem;
  }
}

/* Health-first command center */

.overview-page {
  gap: 1.75rem;
}

.overview-header h1 {
  margin: 0;
  color: var(--app-text);
  font-size: 1.5rem;
  font-weight: 650;
  letter-spacing: -0.02em;
  line-height: 1.25;
  text-wrap: balance;
}

.overview-header p {
  max-width: 65ch;
  margin: 0.375rem 0 0;
  line-height: 1.55;
  text-wrap: pretty;
}

.status-summary {
  display: grid;
  min-width: 0;
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 10px;
  background: var(--app-panel);
}

.status-summary__identity {
  display: grid;
  align-content: center;
  gap: 0.25rem;
  padding: 1rem;
  background: var(--app-panel-muted);
}

.status-summary__identity > p,
.status-summary__facts dt {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-weight: 600;
}

.status-summary__identity > span,
.status-summary__facts small {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.45;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.status-summary__state {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.status-summary__state strong {
  font-size: 1.125rem;
  font-weight: 650;
}

.status-summary__facts {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  margin: 0;
}

.status-summary__facts > div {
  display: grid;
  min-width: 0;
  align-content: center;
  gap: 0.25rem;
  border-bottom: 1px solid var(--app-border-subtle);
  padding: 0.875rem 1rem;
}

.status-summary__facts > div:nth-child(odd) {
  border-right: 1px solid var(--app-border-subtle);
}

.status-summary__facts > div:last-child {
  grid-column: 1 / -1;
  border-bottom: 0;
}

.status-summary__facts dd {
  margin: 0;
  color: var(--app-text);
  font-size: 1rem;
  font-variant-numeric: tabular-nums;
  font-weight: 650;
}

.attention-panel {
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 10px;
  background: var(--app-panel);
}

.attention-panel__heading {
  display: flex;
  align-items: start;
  justify-content: space-between;
  gap: 1rem;
  padding: 1rem 1.125rem;
}

.attention-panel__heading h2,
.attention-row h3,
.attention-healthy h3 {
  margin: 0;
  color: var(--app-text);
  font-size: 0.9375rem;
  font-weight: 650;
}

.attention-panel__heading p,
.attention-row p,
.attention-healthy p {
  max-width: 70ch;
  margin: 0.25rem 0 0;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  line-height: 1.5;
  overflow-wrap: anywhere;
}

.attention-panel__count {
  flex: 0 0 auto;
  border-radius: 999px;
  background: color-mix(in srgb, var(--app-warning) 12%, var(--app-panel));
  color: var(--app-warning);
  font-size: 0.75rem;
  font-weight: 650;
  padding: 0.25rem 0.625rem;
}

.attention-list {
  border-top: 1px solid var(--app-border-subtle);
}

.attention-row,
.attention-healthy {
  display: grid;
  min-width: 0;
  grid-template-columns: auto minmax(0, 1fr);
  gap: 0.75rem;
  align-items: start;
  padding: 0.875rem 1.125rem;
}

.attention-row + .attention-row {
  border-top: 1px solid var(--app-border-subtle);
}

.attention-row--error {
  background: color-mix(in srgb, var(--app-error) 5%, var(--app-panel));
}

.attention-row--warning {
  background: color-mix(in srgb, var(--app-warning) 5%, var(--app-panel));
}

.attention-row__icon,
.attention-healthy > svg {
  width: 1rem;
  height: 1rem;
  margin-top: 0.125rem;
  flex: 0 0 auto;
}

.attention-row--error .attention-row__icon {
  color: var(--app-error);
}

.attention-row--warning .attention-row__icon {
  color: var(--app-warning);
}

.attention-row__copy {
  min-width: 0;
}

.attention-row__action {
  display: inline-flex;
  min-height: 2rem;
  align-items: center;
  justify-content: center;
  gap: 0.375rem;
  grid-column: 2;
  justify-self: start;
  border-radius: 6px;
  color: var(--app-accent);
  font-size: 0.8125rem;
  font-weight: 600;
}

.attention-row__action:hover {
  color: color-mix(in srgb, var(--app-accent) 80%, var(--app-text));
  text-decoration: underline;
  text-underline-offset: 0.2em;
}

.attention-row__action:focus-visible {
  outline: 3px solid var(--app-focus);
  outline-offset: 2px;
}

.attention-row__action svg {
  width: 0.875rem;
  height: 0.875rem;
}

.attention-healthy {
  border-top: 1px solid var(--app-border-subtle);
  background: color-mix(in srgb, var(--app-success) 5%, var(--app-panel));
}

.attention-healthy > svg {
  color: var(--app-success);
}

.dashboard-panel,
.empty-panel {
  border-radius: 10px;
  background: var(--app-panel);
}

.dashboard-panel {
  gap: 1.125rem;
  padding: 1.125rem;
}

.panel-heading h2,
.empty-panel h2 {
  font-size: 1rem;
  font-weight: 650;
}

.status-class,
.snapshot-item,
.cache-stat {
  border: 0;
  border-radius: 0;
  background: transparent;
  padding: 0.25rem 0.75rem;
}

.status-class + .status-class,
.snapshot-item + .snapshot-item,
.cache-stat + .cache-stat {
  border-left: 1px solid var(--app-border-subtle);
}

.trend-track {
  border: 0;
  background: var(--app-panel-muted);
}

.stable-empty {
  border-color: var(--app-border-subtle);
  background: transparent;
}

@media (min-width: 640px) {
  .status-summary {
    grid-template-columns: minmax(10rem, 0.9fr) minmax(0, 3.1fr);
  }

  .status-summary__facts {
    grid-template-columns: repeat(5, minmax(0, 1fr));
  }

  .status-summary__facts > div,
  .status-summary__facts > div:nth-child(odd) {
    border-right: 1px solid var(--app-border-subtle);
    border-bottom: 0;
  }

  .status-summary__facts > div:last-child {
    grid-column: auto;
    border-right: 0;
  }

  .attention-row,
  .attention-healthy {
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
  }

  .attention-row__action {
    grid-column: 3;
    justify-self: end;
    white-space: nowrap;
  }
}

@media (max-width: 639px) {
  .attention-panel__heading {
    align-items: stretch;
    flex-direction: column;
  }

  .attention-panel__count {
    align-self: flex-start;
  }

  .status-class + .status-class:nth-child(odd),
  .snapshot-item + .snapshot-item:nth-child(odd),
  .cache-stat + .cache-stat:nth-child(odd) {
    border-left: 0;
  }
}
</style>
