<script setup lang="ts">
import { computed, inject, ref } from "vue";
import type { ComputedRef } from "vue";
import {
  ProxyState,
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  type DashboardProxyDimensionSummary,
  type DashboardTrafficBucket,
  type GetDashboardResponse,
  type GetPublicProxyConfigResponse,
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
  proxyFailureRequests,
  requestsPerSecond,
  statusClassCounts,
  successRate,
  windowByLabel,
  type DashboardTrafficBucketView,
} from "@/lib/dashboardStats";

type HotspotTab = "listeners" | "targets" | "routes" | "agents";

const dashboard = inject<ComputedRef<GetDashboardResponse | null>>("dashboard");
const publicProxyConfig = inject<ComputedRef<GetPublicProxyConfigResponse | null>>("publicProxyConfig");

const selectedWindowLabel = ref("1h");
const activeHotspotTab = ref<HotspotTab>("listeners");

const dashboardValue = computed(() => dashboard?.value ?? null);
const config = computed(() => publicProxyConfig?.value ?? null);
const status = computed(() => dashboardValue.value?.status ?? null);
const generatedAt = computed(() => dashboardValue.value?.generatedAtUnixMillis ?? BigInt(Date.now()));
const selectedWindow = computed(() => windowByLabel(dashboardValue.value, selectedWindowLabel.value));
const allWindows = computed(() => dashboardValue.value?.windows ?? []);
const hasAnyProxyEvents = computed(() => allWindows.value.some((window) => window.proxyRequests > 0n));

const proxyState = computed(() => status.value?.proxy?.state ?? ProxyState.UNSPECIFIED);
const proxyIsRunning = computed(() => proxyState.value === ProxyState.RUNNING || status.value?.proxyRunning === true);
const proxyError = computed(() => status.value?.proxy?.lastError || status.value?.proxyLastError || "");
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

function bucketHeight(bucket: DashboardTrafficBucketView): string {
  if (bucket.requests === 0n) return "0%";
  return `${Math.max(8, Math.round((Number(bucket.requests) / maxBucketRequests.value) * 100)).toString()}%`;
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
</script>

<template>
  <div v-if="dashboardValue && status" class="overview-page">
    <section class="overview-header">
      <div>
        <h3>Proxy Overview</h3>
        <p>Operational stats from retained proxy request events.</p>
      </div>
      <div class="window-tabs" role="tablist" aria-label="Dashboard window">
        <button
          v-for="window in ['5m', '1h', '24h', '30d']"
          :key="window"
          type="button"
          role="tab"
          :aria-selected="selectedWindowLabel === window"
          :class="{ active: selectedWindowLabel === window }"
          @click="selectedWindowLabel = window"
        >
          {{ window }}
        </button>
      </div>
    </section>

    <section class="overview-grid metric-strip">
      <div class="metric-card">
        <p class="metric-kicker">Proxy</p>
        <div class="metric-value">
          <span class="signal-dot" :class="proxyStateClass(proxyState)" />
          {{ proxyStateLabel(proxyState) }}
        </div>
        <p class="metric-subline">{{ runningListeners }}/{{ enabledListeners }} listeners running</p>
      </div>

      <div class="metric-card">
        <p class="metric-kicker">Requests</p>
        <div class="metric-value">{{ formatNumber(selectedWindow?.proxyRequests) }}</div>
        <p class="metric-subline">{{ selectedRequestRate() }}</p>
      </div>

      <div class="metric-card">
        <p class="metric-kicker">Success</p>
        <div class="metric-value text-green-300">{{ formatPercent(successRate(selectedWindow)) }}</div>
        <p class="metric-subline">{{ formatNumber(nonSuccessRequests(selectedWindow)) }} non-success / {{ formatNumber(proxyFailureRequests(selectedWindow)) }} proxy failures</p>
      </div>

      <div class="metric-card">
        <p class="metric-kicker">Latency</p>
        <div class="metric-value">{{ formatDuration(selectedWindow?.proxyAvgDurationMs) }}</div>
        <p class="metric-subline">max {{ formatDuration(selectedWindow?.proxyMaxDurationMs) }}</p>
      </div>

      <div class="metric-card">
        <p class="metric-kicker">Throughput</p>
        <div class="metric-value">{{ selectedDownloadRate() }}</div>
        <p class="metric-subline">up {{ selectedUploadRate() }}</p>
      </div>

      <div class="metric-card">
        <p class="metric-kicker">Agents</p>
        <div class="metric-value">{{ connectedAgents }}/{{ enabledAgents }}</div>
        <p class="metric-subline">{{ agentsMetricSubline() }}</p>
      </div>
    </section>

    <section class="dashboard-panel cache-panel">
      <div class="panel-heading">
        <div>
          <h4>Cache Behavior</h4>
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

    <section v-if="!hasAnyProxyEvents" class="empty-panel">
      <div>
        <h4>No proxy requests recorded yet.</h4>
        <p>Start a listener and send traffic through the proxy to populate these metrics.</p>
      </div>
    </section>

    <section class="overview-main-grid">
      <div class="dashboard-panel trend-panel">
        <div class="panel-heading">
          <div>
            <h4>Traffic Trend</h4>
            <p>Last 60 minutes, grouped into five-minute buckets.</p>
          </div>
          <span class="signal-pill">{{ formatNumber(trafficBuckets.reduce((sum, bucket) => sum + bucket.requests, 0n)) }} req</span>
        </div>

        <div v-if="trafficBuckets.some((bucket) => bucket.requests > 0n)" class="trend-bars">
          <div
            v-for="bucket in trafficBuckets"
            :key="bucket.bucketUnixMillis.toString()"
            class="trend-slot"
            :title="bucketTitle(bucket)"
          >
            <div class="trend-track">
              <div class="trend-bar" :style="{ height: bucketHeight(bucket) }">
                <div class="trend-error" :style="{ height: bucketErrorHeight(bucket) }" />
              </div>
            </div>
          </div>
        </div>
        <div v-else class="stable-empty">No proxy traffic in the last hour.</div>
      </div>

      <div class="dashboard-panel">
        <div class="panel-heading">
          <div>
            <h4>Problem Signals</h4>
            <p>Selected window plus last-hour error kinds.</p>
          </div>
          <div class="panel-actions">
            <router-link to="/diagnostics" class="diagnostics-link">View diagnostics</router-link>
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
          <div v-if="proxyError" class="error-row">
            <span>Proxy last error</span>
            <strong>{{ proxyError }}</strong>
          </div>
          <div v-for="error in dashboardValue.topErrorKinds" :key="error.label" class="error-row" :title="error.label">
            <span>{{ errorKindLabel(error.label) }}</span>
            <strong>{{ formatNumber(error.requests) }}</strong>
          </div>
          <div v-if="!proxyError && !dashboardValue.topErrorKinds.length" class="stable-empty compact">No proxy failures in the last hour.</div>
        </div>
      </div>
    </section>

    <section class="dashboard-panel">
      <div class="panel-heading">
        <div>
          <h4>Hotspots</h4>
          <p>Top in the last hour.</p>
        </div>
        <div class="mini-tabs" role="tablist" aria-label="Hotspot dimension">
          <button
            v-for="tab in hotspotTabs"
            :key="tab.key"
            type="button"
            role="tab"
            :aria-selected="activeHotspotTab === tab.key"
            :class="{ active: activeHotspotTab === tab.key }"
            @click="activeHotspotTab = tab.key"
          >
            {{ tab.label }}
          </button>
        </div>
      </div>

      <div class="table-scroll">
        <table class="hotspot-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Requests</th>
              <th>Success</th>
              <th>Non-success</th>
              <th>Avg latency</th>
              <th>Down</th>
              <th>Up</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in hotspotRows" :key="`${activeHotspotTab}-${row.id.toString()}-${row.label}`">
              <td class="name-cell">{{ row.label }}</td>
              <td>{{ formatNumber(row.requests) }}</td>
              <td>{{ rowSuccess(row) }}</td>
              <td>{{ formatNumber(rowNonSuccess(row)) }}</td>
              <td>{{ formatDuration(row.avgDurationMs) }}</td>
              <td>{{ formatBytes(row.responseBytes) }}</td>
              <td>{{ formatBytes(row.requestBytes) }}</td>
            </tr>
            <tr v-if="!hotspotRows.length">
              <td colspan="7" class="empty-row">No hotspot data for this dimension.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section class="dashboard-panel">
      <div class="panel-heading">
        <div>
          <h4>Configuration Snapshot</h4>
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
  color: #fff;
  font-size: 1.25rem;
  font-weight: 700;
  letter-spacing: 0;
}

.overview-header p,
.panel-heading p,
.metric-subline {
  color: #888;
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
  border: 1px solid #333;
  border-radius: 6px;
  background: #000;
}

.metric-card {
  display: grid;
  gap: 0.35rem;
  padding: 1rem;
}

.metric-kicker {
  color: #888;
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
  color: #ededed;
  font-size: 1.25rem;
  font-weight: 700;
  letter-spacing: 0;
}

.window-tabs,
.mini-tabs {
  display: inline-grid;
  overflow: hidden;
  border: 1px solid #333;
  border-radius: 6px;
  background: #050505;
  padding: 0.2rem;
}

.window-tabs {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.mini-tabs {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.window-tabs button,
.mini-tabs button {
  min-width: 0;
  border-radius: 4px;
  color: #888;
  font-size: 0.78rem;
  font-weight: 650;
  letter-spacing: 0;
  transition: background 140ms ease, color 140ms ease;
}

.window-tabs button {
  height: 2rem;
  padding: 0 0.75rem;
}

.mini-tabs button {
  height: 1.85rem;
  padding: 0 0.55rem;
}

.window-tabs button:hover,
.mini-tabs button:hover {
  background: #111;
  color: #fff;
}

.window-tabs button.active,
.mini-tabs button.active {
  background: #fff;
  color: #000;
}

.signal-dot {
  width: 0.55rem;
  height: 0.55rem;
  flex: 0 0 auto;
  border-radius: 999px;
}

.signal-good {
  background: #22c55e;
}

.signal-warn {
  background: #f59e0b;
}

.signal-bad {
  background: #ef4444;
}

.signal-muted {
  background: #666;
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
  border: 1px solid #333;
  border-radius: 6px;
  background: #fff;
  color: #000;
  font-size: 0.72rem;
  font-weight: 700;
  padding: 0 0.55rem;
  white-space: nowrap;
}

.diagnostics-link:hover {
  background: #d4d4d8;
}

.panel-heading h4,
.empty-panel h4 {
  color: #fff;
  font-size: 0.95rem;
  font-weight: 700;
  letter-spacing: 0;
}

.signal-pill {
  display: inline-flex;
  min-height: 1.55rem;
  align-items: center;
  border: 1px solid #333;
  border-radius: 999px;
  background: #080808;
  color: #d4d4d8;
  font-size: 0.72rem;
  font-weight: 650;
  padding: 0 0.55rem;
  white-space: nowrap;
}

.signal-pill.warn {
  border-color: rgb(245 158 11 / 55%);
  color: #fbbf24;
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
  border: 1px solid #222;
  border-radius: 4px;
  background: #050505;
}

.trend-bar {
  position: relative;
  width: 100%;
  min-height: 0;
  background: #d4d4d8;
  transition: height 160ms ease;
}

.trend-error {
  position: absolute;
  right: 0;
  bottom: 0;
  left: 0;
  background: #ef4444;
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
  border: 1px solid #222;
  border-radius: 6px;
  background: #050505;
  padding: 0.75rem;
}

.status-class span,
.snapshot-item span,
.cache-stat span {
  color: #888;
  font-size: 0.72rem;
  font-weight: 650;
}

.status-class strong,
.snapshot-item strong,
.cache-stat strong {
  color: #fff;
  font-size: 1rem;
  font-weight: 700;
}

.snapshot-item small,
.cache-stat small {
  overflow: hidden;
  color: #777;
  font-size: 0.72rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cache-empty {
  color: #777;
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
  border-top: 1px solid #1f1f1f;
  padding-top: 0.45rem;
}

.error-row span {
  overflow: hidden;
  color: #d4d4d8;
  font-size: 0.8rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.error-row strong {
  color: #fca5a5;
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
  border-top: 1px solid #222;
  padding: 0.65rem 0.5rem;
  text-align: right;
  white-space: nowrap;
}

.hotspot-table th {
  color: #777;
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
  color: #fff;
  font-weight: 650;
  text-overflow: ellipsis;
}

.empty-row,
.stable-empty {
  color: #777;
  font-size: 0.82rem;
  text-align: center;
}

.stable-empty {
  display: grid;
  min-height: 8rem;
  place-items: center;
  border: 1px solid #222;
  border-radius: 6px;
  background: #050505;
}

.stable-empty.compact {
  min-height: 3rem;
}

.empty-panel {
  padding: 1rem;
}

.empty-panel p {
  margin-top: 0.25rem;
  color: #888;
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
</style>
