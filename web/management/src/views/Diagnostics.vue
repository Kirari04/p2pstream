<script setup lang="ts">
import { computed, h, onMounted, ref, watch } from "vue";
import { NAlert, NButton, NButtonGroup, NDataTable, NEmpty, NSelect } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
import type {
  DashboardDiagnosticsSample,
  DashboardProxyDimensionSummary,
  DashboardStatusCodeSummary,
  GetDashboardDiagnosticsResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  formatBytes,
  formatDuration,
  formatNumber,
  formatPathPrefix,
  formatPercent,
  statusTone,
} from "@/lib/dashboardStats";

type WindowLabel = "5m" | "1h" | "24h" | "30d";

const managementClient = useManagementClient();
const windowLabels: WindowLabel[] = ["5m", "1h", "24h", "30d"];
const sampleOptions = [25, 50, 100];
const sampleSelectOptions = sampleOptions.map((option) => ({
  label: `${option.toString()} samples`,
  value: option,
}));

const selectedWindowLabel = ref<WindowLabel>("1h");
const sampleLimit = ref(25);
const diagnostics = ref<GetDashboardDiagnosticsResponse | null>(null);
const isLoading = ref(false);
const error = ref("");
let requestSequence = 0;

const outcome = computed(() => diagnostics.value?.outcome);
const statusCodes = computed(() => diagnostics.value?.statusCodes ?? []);
const recentSamples = computed(() => diagnostics.value?.recentSamples ?? []);
const recentSampleRowKeys = computed(() => {
  const keys = new WeakMap<DashboardDiagnosticsSample, string>();
  const seen = new Map<string, number>();
  for (const sample of recentSamples.value) {
    const baseKey = sampleRowBaseKey(sample);
    const occurrence = seen.get(baseKey) ?? 0;
    seen.set(baseKey, occurrence + 1);
    keys.set(sample, `${baseKey}-${occurrence.toString()}`);
  }
  return keys;
});
const maxStatusRequests = computed(() => Math.max(1, ...statusCodes.value.map((row) => toNumber(row.requests))));
const dimensionSections = computed(() => [
  { title: "Error kinds", rows: diagnostics.value?.errorKinds ?? [], empty: "No proxy failures in this window." },
  { title: "Listeners", rows: diagnostics.value?.problemListeners ?? [], empty: "No problem listeners in this window." },
  { title: "Routes", rows: diagnostics.value?.problemRoutes ?? [], empty: "No problem routes in this window." },
  { title: "Route targets", rows: diagnostics.value?.problemRouteTargets ?? [], empty: "No problem targets in this window." },
  { title: "Agents", rows: diagnostics.value?.problemAgents ?? [], empty: "No problem agents in this window." },
]);
const sampleColumns = computed<DataTableColumns<DashboardDiagnosticsSample>>(() => [
  {
    title: "Time",
    key: "time",
    width: 150,
    render: (sample) => formatSampleTime(sample.occurredAtUnixMillis),
  },
  {
    title: "Request",
    key: "request",
    width: 150,
    ellipsis: { tooltip: true },
    render: sampleContext,
  },
  {
    title: "Path prefix",
    key: "pathPrefix",
    width: 160,
    ellipsis: { tooltip: true },
    render: (sample) => formatPathPrefix(sample.pathPrefix),
  },
  {
    title: "Status",
    key: "status",
    width: 100,
    render: (sample) => h("span", { class: ["status-pill", `tone-${statusTone(sample.statusCode)}`] }, sampleStatusLabel(sample)),
  },
  {
    title: "Error kind",
    key: "errorKind",
    width: 160,
    ellipsis: { tooltip: true },
    render: (sample) => sample.errorKind || "-",
  },
  {
    title: "Listener",
    key: "listener",
    width: 150,
    ellipsis: { tooltip: true },
    render: (sample) => sample.listenerLabel || "-",
  },
  {
    title: "Route",
    key: "route",
    width: 150,
    ellipsis: { tooltip: true },
    render: (sample) => sample.routeLabel || "-",
  },
  {
    title: "Target",
    key: "target",
    width: 160,
    ellipsis: { tooltip: true },
    render: (sample) => sample.routeTargetLabel || "-",
  },
  {
    title: "Agent",
    key: "agent",
    width: 150,
    ellipsis: { tooltip: true },
    render: (sample) => sample.agentLabel || "-",
  },
  {
    title: "Duration",
    key: "duration",
    width: 110,
    render: (sample) => formatDuration(sample.durationMs),
  },
  {
    title: "Down",
    key: "down",
    width: 100,
    render: (sample) => formatBytes(sample.responseBytes),
  },
  {
    title: "Up",
    key: "up",
    width: 100,
    render: (sample) => formatBytes(sample.requestBytes),
  },
]);

async function loadDiagnostics() {
  const sequence = ++requestSequence;
  isLoading.value = true;
  error.value = "";
  try {
    const resp = await managementClient.getDashboardDiagnostics({
      windowLabel: selectedWindowLabel.value,
      sampleLimit: BigInt(sampleLimit.value),
    });
    if (sequence !== requestSequence) return;
    diagnostics.value = resp;
  } catch (err) {
    if (sequence !== requestSequence) return;
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (sequence === requestSequence) {
      isLoading.value = false;
    }
  }
}

watch([selectedWindowLabel, sampleLimit], () => {
  void loadDiagnostics();
});

onMounted(() => {
  void loadDiagnostics();
});

function statusWidth(row: DashboardStatusCodeSummary): string {
  return `${Math.max(2, Math.round((toNumber(row.requests) / maxStatusRequests.value) * 100)).toString()}%`;
}

function statusNonSuccess(row: DashboardStatusCodeSummary): bigint {
  return row.clientError + row.serverError;
}

function dimensionNonSuccess(row: DashboardProxyDimensionSummary): bigint {
  return row.clientError + row.serverError;
}

function dimensionProxyFailures(row: DashboardProxyDimensionSummary): bigint {
  return row.internalError;
}

function sampleStatusLabel(sample: DashboardDiagnosticsSample): string {
  return sample.statusCode > 0n ? sample.statusCode.toString() : "-";
}

function sampleContext(sample: DashboardDiagnosticsSample): string {
  const method = sample.method || "-";
  const host = sample.host || "-";
  return `${method} ${host}`;
}

function formatSampleTime(value: bigint): string {
  const millis = toNumber(value);
  if (millis <= 0) return "-";
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(new Date(millis));
}

function toNumber(value: bigint | number): number {
  if (typeof value === "number") {
    return Number.isFinite(value) ? Math.max(0, value) : 0;
  }
  if (value <= 0n) return 0;
  const max = BigInt(Number.MAX_SAFE_INTEGER);
  return Number(value > max ? max : value);
}

function sampleRowBaseKey(sample: DashboardDiagnosticsSample): string {
  return [
    sample.occurredAtUnixMillis.toString(),
    sample.method,
    sample.host,
    sample.pathPrefix,
    sample.statusCode.toString(),
    sample.errorKind,
    sample.listenerLabel,
    sample.routeLabel,
    sample.routeTargetLabel,
    sample.agentLabel,
    sample.durationMs.toString(),
    sample.requestBytes.toString(),
    sample.responseBytes.toString(),
  ].join("|");
}

function sampleRowKey(sample: DashboardDiagnosticsSample): string {
  return recentSampleRowKeys.value.get(sample) ?? sampleRowBaseKey(sample);
}
</script>

<template>
  <div class="diagnostics-page">
    <section class="diagnostics-header">
      <div>
        <h3>Diagnostics</h3>
        <p>Proxy outcomes, response distribution, failure dimensions, and recent problem samples.</p>
      </div>
      <div class="header-controls">
        <NButtonGroup class="window-tabs" role="tablist" aria-label="Diagnostics window" size="small">
          <NButton
            v-for="label in windowLabels"
            :key="label"
            attr-type="button"
            role="tab"
            :aria-selected="selectedWindowLabel === label"
            :type="selectedWindowLabel === label ? 'primary' : 'default'"
            @click="selectedWindowLabel = label"
          >
            {{ label }}
          </NButton>
        </NButtonGroup>
        <NSelect v-model:value="sampleLimit" class="sample-select" size="small" aria-label="Sample limit" :options="sampleSelectOptions" />
      </div>
    </section>

    <NAlert v-if="error" type="error" :show-icon="false">{{ error }}</NAlert>

    <section class="summary-strip" :class="{ loading: isLoading }">
      <div class="summary-item">
        <span>Requests</span>
        <strong>{{ formatNumber(outcome?.requests) }}</strong>
        <small>{{ diagnostics?.label || selectedWindowLabel }}</small>
      </div>
      <div class="summary-item">
        <span>Successful</span>
        <strong>{{ formatNumber(outcome?.success) }}</strong>
        <small>{{ formatPercent(outcome && outcome.requests > 0n ? toNumber(outcome.success) / Math.max(1, toNumber(outcome.requests)) : 0) }}</small>
      </div>
      <div class="summary-item">
        <span>Non-success</span>
        <strong>{{ formatNumber(outcome?.nonSuccess) }}</strong>
        <small>4xx + 5xx</small>
      </div>
      <div class="summary-item">
        <span>Proxy failures</span>
        <strong>{{ formatNumber(outcome?.proxyFailure) }}</strong>
        <small>error kind set</small>
      </div>
      <div class="summary-item">
        <span>Latency</span>
        <strong>{{ formatDuration(outcome?.avgDurationMs) }}</strong>
        <small>max {{ formatDuration(outcome?.maxDurationMs) }}</small>
      </div>
    </section>

    <section class="diagnostics-panel">
      <div class="panel-heading">
        <div>
          <h4>Status Codes</h4>
          <p>Exact response distribution for the selected window.</p>
        </div>
      </div>
      <div v-if="statusCodes.length" class="status-list">
        <div v-for="row in statusCodes" :key="row.statusCode.toString()" class="status-row">
          <div class="status-label">
            <span class="status-pill" :class="`tone-${statusTone(row.statusCode)}`">{{ row.statusCode.toString() }}</span>
            <strong>{{ formatNumber(row.requests) }}</strong>
          </div>
          <div class="status-bar-track">
            <div class="status-bar" :class="`tone-${statusTone(row.statusCode)}`" :style="{ width: statusWidth(row) }" />
          </div>
          <div class="status-meta">
            <span>{{ formatNumber(statusNonSuccess(row)) }} non-success</span>
            <span>{{ formatNumber(row.proxyFailure) }} failures</span>
            <span>{{ formatDuration(row.avgDurationMs) }}</span>
            <span>{{ formatBytes(row.responseBytes) }} down</span>
          </div>
        </div>
      </div>
      <NEmpty v-else size="small" description="No status codes in this window." />
    </section>

    <section class="breakdown-grid">
      <div v-for="section in dimensionSections" :key="section.title" class="diagnostics-panel">
        <div class="panel-heading compact">
          <h4>{{ section.title }}</h4>
        </div>
        <div v-if="section.rows.length" class="dimension-list">
          <div v-for="row in section.rows" :key="`${section.title}-${row.id.toString()}-${row.label}`" class="dimension-row">
            <div class="dimension-name" :title="row.label">{{ row.label || "unknown" }}</div>
            <div class="dimension-counts">
              <span>{{ formatNumber(row.requests) }} req</span>
              <span>{{ formatNumber(dimensionNonSuccess(row)) }} non-success</span>
              <span>{{ formatNumber(dimensionProxyFailures(row)) }} failures</span>
            </div>
          </div>
        </div>
        <NEmpty v-else size="small" class="panel-empty panel-empty--compact" :description="section.empty" />
      </div>
    </section>

    <section class="diagnostics-panel diagnostics-panel--table">
      <div class="panel-heading">
        <div>
          <h4>Recent Samples</h4>
          <p>Newest non-success responses and proxy/internal failures.</p>
        </div>
      </div>
      <div v-if="recentSamples.length" class="diagnostics-table-shell">
        <NDataTable
          :columns="sampleColumns"
          :data="recentSamples"
          :row-key="sampleRowKey"
          :pagination="false"
          :bordered="false"
          :single-line="false"
          :scroll-x="1530"
          size="small"
        />
      </div>
      <NEmpty v-else size="small" description="No recent problem samples in this window." />
    </section>
  </div>
</template>

<style scoped>
.diagnostics-page {
  display: grid;
  gap: 1.5rem;
}

.diagnostics-header {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: end;
  gap: 1rem;
}

.diagnostics-header > div:first-child {
  min-width: 0;
}

.diagnostics-header h3 {
  color: var(--app-text);
  font-size: 1.25rem;
  font-weight: 700;
  letter-spacing: 0;
}

.diagnostics-header p,
.panel-heading p {
  color: var(--app-text-muted);
  font-size: 0.82rem;
}

.header-controls {
  display: grid;
  grid-template-columns: auto 14rem;
  align-items: center;
  gap: 0.5rem;
  justify-content: end;
}

.window-tabs {
  min-width: 15rem;
}

.window-tabs :deep(.n-button) {
  min-width: 0;
  height: 2rem;
  font-size: 0.78rem;
  font-weight: 650;
  letter-spacing: 0;
  padding: 0 0.75rem;
}

.sample-select {
  width: 14rem;
}

.summary-strip,
.breakdown-grid {
  display: grid;
  gap: 0.75rem;
}

.summary-strip {
  grid-template-columns: repeat(1, minmax(0, 1fr));
}

.summary-strip.loading {
  opacity: 0.72;
}

.summary-item,
.diagnostics-panel {
  min-width: 0;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
}

.summary-item {
  display: grid;
  gap: 0.25rem;
  padding: 1rem;
}

.summary-item span {
  color: var(--app-text-muted);
  font-size: 0.72rem;
  font-weight: 700;
  letter-spacing: 0;
  text-transform: uppercase;
}

.summary-item strong {
  color: var(--app-text);
  font-size: 1.25rem;
  font-weight: 700;
}

.summary-item small {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 0.72rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.diagnostics-panel {
  display: grid;
  gap: 1rem;
  padding: 1rem;
}

.diagnostics-panel--table {
  overflow: hidden;
}

.panel-heading {
  display: flex;
  align-items: start;
  justify-content: space-between;
  gap: 1rem;
}

.panel-heading.compact {
  display: block;
}

.panel-heading h4 {
  color: var(--app-text);
  font-size: 0.95rem;
  font-weight: 700;
  letter-spacing: 0;
}

.status-list,
.dimension-list {
  display: grid;
  gap: 0.55rem;
}

.status-row {
  display: grid;
  grid-template-columns: 8rem minmax(10rem, 1fr);
  gap: 0.55rem 0.75rem;
  align-items: center;
  border-top: 1px solid var(--app-border);
  padding-top: 0.65rem;
}

.status-label {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.55rem;
}

.status-label strong {
  color: var(--app-text);
  font-size: 0.9rem;
  font-weight: 700;
}

.status-bar-track {
  overflow: hidden;
  height: 0.65rem;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-panel-muted);
}

.status-bar {
  height: 100%;
  min-width: 0.4rem;
}

.status-meta {
  display: grid;
  grid-column: 2;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 0.4rem;
  color: var(--app-text-muted);
  font-size: 0.74rem;
}

.status-meta span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.status-pill {
  display: inline-flex;
  min-width: 3rem;
  height: 1.45rem;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-panel-muted);
  color: var(--app-text);
  font-size: 0.72rem;
  font-weight: 750;
}

.tone-success {
  background: var(--app-success);
  border-color: var(--app-success);
  color: white;
}

.tone-redirect {
  background: var(--app-accent);
  border-color: var(--app-accent);
  color: white;
}

.tone-client-error {
  background: var(--app-warning);
  border-color: var(--app-warning);
  color: white;
}

.tone-server-error {
  background: var(--app-error);
  border-color: var(--app-error);
  color: white;
}

.tone-neutral {
  background: var(--app-border-subtle);
  border-color: var(--app-border);
  color: var(--app-text);
}

.breakdown-grid {
  grid-template-columns: repeat(1, minmax(0, 1fr));
}

.dimension-row {
  display: grid;
  gap: 0.35rem;
  border-top: 1px solid var(--app-border);
  padding-top: 0.55rem;
}

.dimension-name {
  min-width: 0;
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.82rem;
  font-weight: 650;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dimension-counts {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0.35rem;
  color: var(--app-text-muted);
  font-size: 0.72rem;
}

.dimension-counts span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.diagnostics-table-shell {
  min-width: 0;
  max-width: 100%;
  overflow: hidden;
  overflow-x: auto;
}

.diagnostics-table-shell :deep(.n-data-table) {
  min-width: 0;
}

.panel-empty {
  align-self: start;
}

@media (min-width: 640px) {
  .summary-strip {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (min-width: 900px) {
  .summary-strip {
    grid-template-columns: repeat(5, minmax(0, 1fr));
  }

  .breakdown-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (min-width: 1180px) {
  .breakdown-grid {
    grid-template-columns: repeat(5, minmax(0, 1fr));
  }
}

@media (max-width: 860px) {
  .diagnostics-header {
    align-items: stretch;
    grid-template-columns: 1fr;
  }

  .header-controls {
    grid-template-columns: 1fr;
    justify-content: stretch;
  }

  .window-tabs {
    min-width: 0;
    width: 100%;
  }

  .sample-select {
    width: 100%;
  }

  .status-row {
    grid-template-columns: 1fr;
  }

  .status-meta {
    grid-column: 1;
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
