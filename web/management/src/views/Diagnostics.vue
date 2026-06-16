<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
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

const selectedWindowLabel = ref<WindowLabel>("1h");
const sampleLimit = ref(25);
const diagnostics = ref<GetDashboardDiagnosticsResponse | null>(null);
const isLoading = ref(false);
const error = ref("");
let requestSequence = 0;

const outcome = computed(() => diagnostics.value?.outcome);
const statusCodes = computed(() => diagnostics.value?.statusCodes ?? []);
const maxStatusRequests = computed(() => Math.max(1, ...statusCodes.value.map((row) => toNumber(row.requests))));
const dimensionSections = computed(() => [
  { title: "Error kinds", rows: diagnostics.value?.errorKinds ?? [], empty: "No proxy failures in this window." },
  { title: "Listeners", rows: diagnostics.value?.problemListeners ?? [], empty: "No problem listeners in this window." },
  { title: "Routes", rows: diagnostics.value?.problemRoutes ?? [], empty: "No problem routes in this window." },
  { title: "Route targets", rows: diagnostics.value?.problemRouteTargets ?? [], empty: "No problem targets in this window." },
  { title: "Agents", rows: diagnostics.value?.problemAgents ?? [], empty: "No problem agents in this window." },
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
</script>

<template>
  <div class="diagnostics-page">
    <section class="diagnostics-header">
      <div>
        <h3>Diagnostics</h3>
        <p>Proxy outcomes, response distribution, failure dimensions, and recent problem samples.</p>
      </div>
      <div class="header-controls">
        <div class="window-tabs" role="tablist" aria-label="Diagnostics window">
          <button
            v-for="label in windowLabels"
            :key="label"
            type="button"
            role="tab"
            :aria-selected="selectedWindowLabel === label"
            :class="{ active: selectedWindowLabel === label }"
            @click="selectedWindowLabel = label"
          >
            {{ label }}
          </button>
        </div>
        <select v-model.number="sampleLimit" class="sample-select" aria-label="Sample limit">
          <option v-for="option in sampleOptions" :key="option" :value="option">{{ option }} samples</option>
        </select>
      </div>
    </section>

    <section v-if="error" class="diagnostics-error">{{ error }}</section>

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
      <div v-else class="empty-state">No status codes in this window.</div>
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
        <div v-else class="empty-state compact">{{ section.empty }}</div>
      </div>
    </section>

    <section class="diagnostics-panel">
      <div class="panel-heading">
        <div>
          <h4>Recent Samples</h4>
          <p>Newest non-success responses and proxy/internal failures.</p>
        </div>
      </div>
      <div class="table-scroll">
        <table class="samples-table">
          <thead>
            <tr>
              <th>Time</th>
              <th>Request</th>
              <th>Path prefix</th>
              <th>Status</th>
              <th>Error kind</th>
              <th>Listener</th>
              <th>Route</th>
              <th>Target</th>
              <th>Agent</th>
              <th>Duration</th>
              <th>Down</th>
              <th>Up</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="sample in diagnostics?.recentSamples ?? []" :key="`${sample.occurredAtUnixMillis.toString()}-${sample.statusCode.toString()}-${sample.errorKind}`">
              <td>{{ formatSampleTime(sample.occurredAtUnixMillis) }}</td>
              <td class="name-cell" :title="sampleContext(sample)">{{ sampleContext(sample) }}</td>
              <td class="name-cell" :title="formatPathPrefix(sample.pathPrefix)">{{ formatPathPrefix(sample.pathPrefix) }}</td>
              <td>
                <span class="status-pill" :class="`tone-${statusTone(sample.statusCode)}`">{{ sampleStatusLabel(sample) }}</span>
              </td>
              <td class="name-cell" :title="sample.errorKind || '-'">{{ sample.errorKind || "-" }}</td>
              <td class="name-cell" :title="sample.listenerLabel || '-'">{{ sample.listenerLabel || "-" }}</td>
              <td class="name-cell" :title="sample.routeLabel || '-'">{{ sample.routeLabel || "-" }}</td>
              <td class="name-cell" :title="sample.routeTargetLabel || '-'">{{ sample.routeTargetLabel || "-" }}</td>
              <td class="name-cell" :title="sample.agentLabel || '-'">{{ sample.agentLabel || "-" }}</td>
              <td>{{ formatDuration(sample.durationMs) }}</td>
              <td>{{ formatBytes(sample.responseBytes) }}</td>
              <td>{{ formatBytes(sample.requestBytes) }}</td>
            </tr>
            <tr v-if="!(diagnostics?.recentSamples ?? []).length">
              <td colspan="12" class="empty-row">No recent problem samples in this window.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
</template>

<style scoped>
.diagnostics-page {
  display: grid;
  gap: 1.5rem;
}

.diagnostics-header {
  display: flex;
  align-items: end;
  justify-content: space-between;
  gap: 1rem;
}

.diagnostics-header h3 {
  color: #fff;
  font-size: 1.25rem;
  font-weight: 700;
  letter-spacing: 0;
}

.diagnostics-header p,
.panel-heading p {
  color: #888;
  font-size: 0.82rem;
}

.header-controls {
  display: inline-flex;
  flex-wrap: wrap;
  justify-content: end;
  gap: 0.5rem;
}

.window-tabs {
  display: inline-grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  overflow: hidden;
  border: 1px solid #333;
  border-radius: 6px;
  background: #050505;
  padding: 0.2rem;
}

.window-tabs button {
  min-width: 0;
  height: 2rem;
  border-radius: 4px;
  color: #888;
  font-size: 0.78rem;
  font-weight: 650;
  letter-spacing: 0;
  padding: 0 0.75rem;
  transition: background 140ms ease, color 140ms ease;
}

.window-tabs button:hover {
  background: #111;
  color: #fff;
}

.window-tabs button.active {
  background: #fff;
  color: #000;
}

.sample-select {
  height: 2.4rem;
  border: 1px solid #333;
  border-radius: 6px;
  background: #050505;
  color: #ededed;
  font-size: 0.8rem;
  outline: none;
  padding: 0 0.6rem;
}

.diagnostics-error {
  border: 1px solid rgb(239 68 68 / 45%);
  border-radius: 6px;
  background: #000;
  color: #fca5a5;
  font-size: 0.85rem;
  padding: 0.85rem 1rem;
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
  border: 1px solid #333;
  border-radius: 6px;
  background: #000;
}

.summary-item {
  display: grid;
  gap: 0.25rem;
  padding: 1rem;
}

.summary-item span {
  color: #888;
  font-size: 0.72rem;
  font-weight: 700;
  letter-spacing: 0;
  text-transform: uppercase;
}

.summary-item strong {
  color: #fff;
  font-size: 1.25rem;
  font-weight: 700;
}

.summary-item small {
  overflow: hidden;
  color: #777;
  font-size: 0.72rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.diagnostics-panel {
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

.panel-heading.compact {
  display: block;
}

.panel-heading h4 {
  color: #fff;
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
  border-top: 1px solid #222;
  padding-top: 0.65rem;
}

.status-label {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.55rem;
}

.status-label strong {
  color: #ededed;
  font-size: 0.9rem;
  font-weight: 700;
}

.status-bar-track {
  overflow: hidden;
  height: 0.65rem;
  border: 1px solid #222;
  border-radius: 999px;
  background: #050505;
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
  color: #777;
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
  border: 1px solid #333;
  border-radius: 999px;
  background: #080808;
  color: #d4d4d8;
  font-size: 0.72rem;
  font-weight: 750;
}

.tone-success {
  background: #22c55e;
  border-color: #22c55e;
  color: #03130a;
}

.tone-redirect {
  background: #38bdf8;
  border-color: #38bdf8;
  color: #03111a;
}

.tone-client-error {
  background: #f59e0b;
  border-color: #f59e0b;
  color: #1f1300;
}

.tone-server-error {
  background: #ef4444;
  border-color: #ef4444;
  color: #210505;
}

.tone-neutral {
  background: #d4d4d8;
  border-color: #d4d4d8;
  color: #09090b;
}

.breakdown-grid {
  grid-template-columns: repeat(1, minmax(0, 1fr));
}

.dimension-row {
  display: grid;
  gap: 0.35rem;
  border-top: 1px solid #222;
  padding-top: 0.55rem;
}

.dimension-name {
  min-width: 0;
  overflow: hidden;
  color: #ededed;
  font-size: 0.82rem;
  font-weight: 650;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dimension-counts {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0.35rem;
  color: #777;
  font-size: 0.72rem;
}

.dimension-counts span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.table-scroll {
  overflow-x: auto;
}

.samples-table {
  width: 100%;
  min-width: 1120px;
  border-collapse: collapse;
  font-size: 0.78rem;
}

.samples-table th,
.samples-table td {
  border-top: 1px solid #222;
  padding: 0.65rem 0.5rem;
  text-align: right;
  white-space: nowrap;
}

.samples-table th {
  color: #777;
  font-size: 0.68rem;
  font-weight: 700;
  letter-spacing: 0;
  text-transform: uppercase;
}

.samples-table td {
  color: #d4d4d8;
}

.samples-table th:first-child,
.samples-table td:first-child,
.samples-table th:nth-child(2),
.samples-table td:nth-child(2),
.samples-table th:nth-child(3),
.samples-table td:nth-child(3),
.samples-table th:nth-child(5),
.samples-table td:nth-child(5),
.samples-table th:nth-child(6),
.samples-table td:nth-child(6),
.samples-table th:nth-child(7),
.samples-table td:nth-child(7),
.samples-table th:nth-child(8),
.samples-table td:nth-child(8),
.samples-table th:nth-child(9),
.samples-table td:nth-child(9) {
  text-align: left;
}

.name-cell {
  max-width: 12rem;
  overflow: hidden;
  text-overflow: ellipsis;
}

.empty-state,
.empty-row {
  color: #777;
  font-size: 0.82rem;
}

.empty-state {
  border-top: 1px solid #222;
  padding-top: 0.7rem;
}

.empty-state.compact {
  font-size: 0.78rem;
}

.empty-row {
  text-align: center !important;
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

@media (max-width: 720px) {
  .diagnostics-header {
    align-items: stretch;
    flex-direction: column;
  }

  .header-controls {
    justify-content: stretch;
  }

  .window-tabs {
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
