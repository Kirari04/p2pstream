<script setup lang="ts">
import { computed, h, onMounted, ref, watch } from "vue";
import { NAlert, NButton, NButtonGroup, NDataTable, NDrawer, NDrawerContent, NEmpty, NSkeleton } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
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
  formatPercent,
  statusTone,
} from "@/lib/dashboardStats";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";
import { editorDrawerWidth } from "@/lib/naiveUi";

type WindowLabel = "5m" | "1h" | "24h" | "30d";
type DimensionKey = "error" | "listener" | "route" | "target" | "agent";
type DimensionFilter = Readonly<{ key: DimensionKey; label: string; title: string }>;

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
const selectedSample = ref<DashboardDiagnosticsSample | null>(null);
const isSampleDetailsOpen = ref(false);
const selectedDimension = ref<DimensionFilter | null>(null);
let requestSequence = 0;

const outcome = computed(() => diagnostics.value?.outcome);
const statusCodes = computed(() => diagnostics.value?.statusCodes ?? []);
const recentSamples = computed(() => diagnostics.value?.recentSamples ?? []);
const filteredRecentSamples = computed(() => {
  const filter = selectedDimension.value;
  if (!filter) return recentSamples.value;
  return recentSamples.value.filter((sample) => sampleDimensionValue(sample, filter.key) === filter.label);
});
const isInitialLoading = computed(() => isLoading.value && !diagnostics.value);
const generatedAtLabel = computed(() => diagnostics.value ? formatSampleTime(diagnostics.value.generatedAtUnixMillis) : "Not loaded");
const incidentTitle = computed(() => {
  if (!outcome.value) return "Waiting for diagnostics";
  if (outcome.value.proxyFailure > 0n) return `${formatNumber(outcome.value.proxyFailure)} proxy ${outcome.value.proxyFailure === 1n ? "failure" : "failures"} need investigation`;
  if (outcome.value.serverError > 0n) return `${formatNumber(outcome.value.serverError)} server ${outcome.value.serverError === 1n ? "error" : "errors"} in this window`;
  if (outcome.value.nonSuccess > 0n) return `${formatNumber(outcome.value.nonSuccess)} non-success ${outcome.value.nonSuccess === 1n ? "response" : "responses"} in this window`;
  return "No incidents detected in this window";
});
const incidentSummary = computed(() => {
  if (!outcome.value) return "Outcome data has not loaded.";
  if (outcome.value.proxyFailure > 0n) return "Start with the ranked failure dimensions, then inspect the matching request samples.";
  if (outcome.value.nonSuccess > 0n) return "Review status distribution and filter samples by the affected listener, route, target, or agent.";
  return "Requests completed without a captured non-success response or internal proxy failure.";
});
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
  { key: "error" as const, title: "Error kinds", rows: rankDimensionRows(diagnostics.value?.errorKinds), empty: "No proxy failures in this window." },
  { key: "listener" as const, title: "Listeners", rows: rankDimensionRows(diagnostics.value?.problemListeners), empty: "No problem listeners in this window." },
  { key: "route" as const, title: "Routes", rows: rankDimensionRows(diagnostics.value?.problemRoutes), empty: "No problem routes in this window." },
  { key: "target" as const, title: "Route targets", rows: rankDimensionRows(diagnostics.value?.problemRouteTargets), empty: "No problem targets in this window." },
  { key: "agent" as const, title: "Agents", rows: rankDimensionRows(diagnostics.value?.problemAgents), empty: "No problem agents in this window." },
]);
const selectedSampleDetails = computed(() => {
  const sample = selectedSample.value;
  if (!sample) return [];
  return [
    {
      label: "Occurred",
      value: `${formatSampleTime(sample.occurredAtUnixMillis)} · ${sample.occurredAtUnixMillis.toString()} ms since epoch`,
    },
    { label: "Method", value: inspectionValue(sample.method) },
    { label: "Host", value: inspectionValue(sample.host) },
    { label: "Path prefix", value: inspectionValue(sample.pathPrefix) },
    { label: "Status", value: sampleStatusLabel(sample) },
    { label: "Error kind", value: inspectionValue(sample.errorKind) },
    { label: "Retry rule", value: sample.retryRuleId > 0n ? `#${sample.retryRuleId.toString()}` : "(none)" },
    { label: "Retry attempts", value: sample.retryCount.toString() },
    { label: "Retry outcome", value: inspectionValue(sample.retryOutcome) },
    { label: "First retry error", value: inspectionValue(sample.retryErrorKind) },
    { label: "Listener", value: inspectionValue(sample.listenerLabel) },
    { label: "Route", value: inspectionValue(sample.routeLabel) },
    { label: "Target", value: inspectionValue(sample.routeTargetLabel) },
    { label: "Agent", value: inspectionValue(sample.agentLabel) },
    { label: "Duration", value: formatDuration(sample.durationMs) },
    { label: "Downloaded", value: formatBytes(sample.responseBytes) },
    { label: "Uploaded", value: formatBytes(sample.requestBytes) },
  ];
});
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
    width: 300,
    render: (sample) => {
      const method = diagnosticExcerpt(sample.method, 16);
      const host = diagnosticExcerpt(sample.host);
      const path = diagnosticExcerpt(sample.pathPrefix);
      return h("span", { class: "diagnostic-request-stack" }, [
        h("span", { class: "diagnostic-request-excerpt" }, [
          h("bdi", { class: "diagnostic-request-method", dir: "ltr" }, method.text),
          h("bdi", { class: "diagnostic-attacker-excerpt", dir: "ltr", title: inspectionValue(sample.host) }, host.text),
        ]),
        h("bdi", { class: "diagnostic-attacker-excerpt diagnostic-request-path", dir: "ltr", title: inspectionValue(sample.pathPrefix) }, path.text),
      ]);
    },
  },
  {
    title: "Outcome",
    key: "outcome",
    width: 190,
    render: (sample) => h("span", { class: "diagnostic-outcome-cell" }, [
      h("span", { class: ["status-pill", `tone-${statusTone(sample.statusCode)}`] }, sampleStatusLabel(sample)),
      attackerCell(sample.errorKind, 48),
      sample.retryCount > 0n ? h("span", { class: "diagnostic-retry-outcome" }, retrySampleLabel(sample)) : null,
    ]),
  },
  {
    title: "Resolved flow",
    key: "flow",
    width: 300,
    render: (sample) => h("span", { class: "diagnostic-flow-cell" }, [
      flowLine("Listener", sample.listenerLabel),
      flowLine("Route", sample.routeLabel),
      flowLine("Target", sample.routeTargetLabel),
      flowLine("Agent", sample.agentLabel),
    ]),
  },
  {
    title: "Duration",
    key: "duration",
    width: 110,
    render: (sample) => formatDuration(sample.durationMs),
  },
  {
    title: "Transfer",
    key: "transfer",
    width: 150,
    render: (sample) => h("span", { class: "diagnostic-transfer" }, [
      h("span", `↓ ${formatBytes(sample.responseBytes)}`),
      h("span", `↑ ${formatBytes(sample.requestBytes)}`),
    ]),
  },
  {
    title: "Details",
    key: "details",
    width: 90,
    fixed: "right",
    render: (sample) => h(
      NButton,
      {
        quaternary: true,
        size: "small",
        "aria-label": `View diagnostic sample from ${formatSampleTime(sample.occurredAtUnixMillis)}`,
        onClick: () => openSampleDetails(sample),
      },
      { default: () => "View" },
    ),
  },
]);

async function loadDiagnostics(clearCurrent = false) {
  const sequence = ++requestSequence;
  if (clearCurrent) diagnostics.value = null;
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
  selectedDimension.value = null;
  void loadDiagnostics(true);
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

function rankDimensionRows(rows: DashboardProxyDimensionSummary[] | undefined): DashboardProxyDimensionSummary[] {
  return [...(rows ?? [])].sort((left, right) => {
    const leftFailures = left.internalError;
    const rightFailures = right.internalError;
    if (leftFailures !== rightFailures) return leftFailures > rightFailures ? -1 : 1;
    const leftNonSuccess = dimensionNonSuccess(left);
    const rightNonSuccess = dimensionNonSuccess(right);
    if (leftNonSuccess !== rightNonSuccess) return leftNonSuccess > rightNonSuccess ? -1 : 1;
    if (left.requests === right.requests) return 0;
    return left.requests > right.requests ? -1 : 1;
  });
}

function sampleStatusLabel(sample: DashboardDiagnosticsSample): string {
  return sample.statusCode > 0n ? sample.statusCode.toString() : "-";
}

function retrySampleLabel(sample: DashboardDiagnosticsSample): string {
  const outcomeLabel = sample.retryOutcome || "attempted";
  return `${sample.retryCount.toString()} ${sample.retryCount === 1n ? "retry" : "retries"} · ${outcomeLabel}`;
}

function openSampleDetails(sample: DashboardDiagnosticsSample) {
  selectedSample.value = sample;
  isSampleDetailsOpen.value = true;
}

function selectDimension(key: DimensionKey, row: DashboardProxyDimensionSummary, title: string) {
  const next = { key, label: row.label, title };
  const current = selectedDimension.value;
  selectedDimension.value = current?.key === next.key && current.label === next.label ? null : next;
}

function dimensionSelected(key: DimensionKey, label: string): boolean {
  return selectedDimension.value?.key === key && selectedDimension.value.label === label;
}

function sampleDimensionValue(sample: DashboardDiagnosticsSample, key: DimensionKey): string {
  switch (key) {
    case "error": return sample.errorKind;
    case "listener": return sample.listenerLabel;
    case "route": return sample.routeLabel;
    case "target": return sample.routeTargetLabel;
    case "agent": return sample.agentLabel;
  }
}

function attackerCell(value: string, limit = 72) {
  return h("bdi", {
    class: "diagnostic-attacker-excerpt",
    dir: "ltr",
    title: inspectionValue(value),
  }, diagnosticExcerpt(value, limit).text);
}

function flowLine(label: string, value: string) {
  return h("span", { class: "diagnostic-flow-line" }, [
    h("span", { class: "diagnostic-flow-key" }, label),
    attackerCell(value, 52),
  ]);
}

function inspectionValue(value: string): string {
  return value ? diagnosticInspectionText(value) : "(empty)";
}

function formatSampleTime(value: bigint): string {
  const millis = toNumber(value);
  if (millis <= 0) return "-";
  const date = new Date(millis);
  if (Number.isNaN(date.getTime())) return "Invalid timestamp";
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(date);
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
    sample.retryRuleId.toString(),
    sample.retryCount.toString(),
    sample.retryOutcome,
    sample.retryErrorKind,
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
        <p>Proxy outcomes, response distribution, failure dimensions, and recent failure or retry samples.</p>
      </div>
      <div class="header-controls">
        <NButtonGroup class="window-tabs" role="group" aria-label="Diagnostics window" size="small">
          <NButton
            v-for="label in windowLabels"
            :key="label"
            attr-type="button"
            :aria-pressed="selectedWindowLabel === label"
            :type="selectedWindowLabel === label ? 'primary' : 'default'"
            @click="selectedWindowLabel = label"
          >
            {{ label }}
          </NButton>
        </NButtonGroup>
        <AccessibleSelect
          v-model:value="sampleLimit"
          accessible-label="Sample limit"
          class="sample-select"
          size="small"
          :options="sampleSelectOptions"
        />
        <NButton secondary size="small" attr-type="button" :loading="isLoading && Boolean(diagnostics)" @click="loadDiagnostics(false)">
          Refresh
        </NButton>
      </div>
    </section>

    <NAlert v-if="error" :type="diagnostics ? 'warning' : 'error'" :show-icon="false">
      <div class="diagnostics-alert">
        <div>
          <strong>{{ diagnostics ? "Refresh failed. Showing the last server snapshot." : "Diagnostics could not be loaded." }}</strong>
          <bdi dir="ltr">{{ diagnosticInspectionText(error) }}</bdi>
          <small v-if="diagnostics">Snapshot generated {{ generatedAtLabel }}.</small>
        </div>
        <NButton secondary size="small" attr-type="button" @click="loadDiagnostics(false)">Retry</NButton>
      </div>
    </NAlert>

    <section v-if="isInitialLoading" class="diagnostics-skeleton" aria-label="Loading diagnostics" aria-busy="true">
      <div class="diagnostics-skeleton__lead">
        <NSkeleton text width="35%" />
        <NSkeleton text :repeat="2" />
      </div>
      <div class="diagnostics-skeleton__facts">
        <NSkeleton v-for="index in 5" :key="index" text :repeat="2" />
      </div>
      <NSkeleton height="12rem" />
      <div class="diagnostics-skeleton__grid">
        <NSkeleton v-for="index in 5" :key="index" height="10rem" />
      </div>
    </section>

    <NEmpty v-else-if="!diagnostics" size="large" description="No authoritative diagnostics snapshot is available.">
      <template #extra>
        <NButton secondary attr-type="button" @click="loadDiagnostics(false)">Retry diagnostics</NButton>
      </template>
    </NEmpty>

    <template v-else>
      <p v-if="isLoading" class="diagnostics-refresh-state" role="status">
        Refreshing from the server. Values below remain the snapshot generated {{ generatedAtLabel }} until a new response arrives.
      </p>
      <section class="incident-summary" :aria-busy="isLoading">
        <div class="incident-summary__lead">
          <div class="incident-summary__eyebrow">
            <span>Incident snapshot</span>
            <small><bdi dir="ltr">{{ diagnostics.label ? diagnosticInspectionText(diagnostics.label) : selectedWindowLabel }}</bdi> · generated {{ generatedAtLabel }}</small>
          </div>
          <h4>{{ incidentTitle }}</h4>
          <p>{{ incidentSummary }}</p>
        </div>
        <dl class="incident-facts">
          <div>
            <dt>Requests</dt>
            <dd>{{ formatNumber(outcome?.requests) }}</dd>
            <small>{{ formatPercent(outcome && outcome.requests > 0n ? toNumber(outcome.success) / Math.max(1, toNumber(outcome.requests)) : 0) }} successful</small>
          </div>
          <div>
            <dt>Successful</dt>
            <dd>{{ formatNumber(outcome?.success) }}</dd>
            <small>2xx + 3xx</small>
          </div>
          <div>
            <dt>Non-success</dt>
            <dd>{{ formatNumber(outcome?.nonSuccess) }}</dd>
            <small>4xx + 5xx</small>
          </div>
          <div>
            <dt>Proxy failures</dt>
            <dd>{{ formatNumber(outcome?.proxyFailure) }}</dd>
            <small>error kind set</small>
          </div>
          <div>
            <dt>Latency</dt>
            <dd>{{ formatDuration(outcome?.avgDurationMs) }}</dd>
            <small>max {{ formatDuration(outcome?.maxDurationMs) }}</small>
          </div>
        </dl>
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

      <section class="breakdown-section" aria-labelledby="failure-dimensions-heading">
        <div class="panel-heading">
          <div>
            <h4 id="failure-dimensions-heading">Ranked failure dimensions</h4>
            <p>Select a row to filter the request samples by that exact value.</p>
          </div>
        </div>
        <div class="breakdown-grid">
          <div v-for="section in dimensionSections" :key="section.title" class="diagnostics-panel diagnostics-panel--dimension">
            <div class="panel-heading compact">
              <h5>{{ section.title }}</h5>
            </div>
            <ol v-if="section.rows.length" class="dimension-list">
              <li v-for="(row, index) in section.rows" :key="`${section.title}-${row.id.toString()}-${row.label}`">
                <button
                  type="button"
                  class="dimension-row"
                  :class="{ 'dimension-row--selected': dimensionSelected(section.key, row.label) }"
                  :aria-pressed="dimensionSelected(section.key, row.label)"
                  @click="selectDimension(section.key, row, section.title)"
                >
                  <span class="dimension-rank">{{ index + 1 }}</span>
                  <bdi class="dimension-name" dir="ltr" :title="inspectionValue(row.label)">{{ diagnosticExcerpt(row.label, 56).text }}</bdi>
                  <span class="dimension-counts">
                    <span>{{ formatNumber(row.requests) }} req</span>
                    <span>{{ formatNumber(dimensionNonSuccess(row)) }} non-success</span>
                    <span>{{ formatNumber(dimensionProxyFailures(row)) }} failures</span>
                  </span>
                </button>
              </li>
            </ol>
            <NEmpty v-else size="small" class="panel-empty panel-empty--compact" :description="section.empty" />
          </div>
        </div>
      </section>

      <section class="diagnostics-panel diagnostics-panel--table">
        <div class="panel-heading diagnostics-samples-heading">
          <div>
            <h4>Recent Samples</h4>
            <p>Newest non-success responses, proxy failures, and requests recovered by agent retry.</p>
          </div>
          <div v-if="selectedDimension" class="sample-filter">
            <span>Filtered by {{ selectedDimension.title }}</span>
            <bdi dir="ltr">{{ inspectionValue(selectedDimension.label) }}</bdi>
            <NButton quaternary size="small" attr-type="button" @click="selectedDimension = null">Clear filter</NButton>
          </div>
        </div>
        <div v-if="filteredRecentSamples.length" class="diagnostics-table-shell">
          <NDataTable
            :columns="sampleColumns"
            :data="filteredRecentSamples"
            :row-key="sampleRowKey"
            :pagination="false"
            :bordered="false"
            :single-line="false"
            :scroll-x="1290"
            size="small"
          />
        </div>
        <NEmpty
          v-else
          size="small"
          :description="recentSamples.length ? 'No retained samples match the selected failure dimension.' : 'No recent failure or retry samples in this window.'"
        >
          <template v-if="selectedDimension" #extra>
            <NButton secondary size="small" attr-type="button" @click="selectedDimension = null">Clear sample filter</NButton>
          </template>
        </NEmpty>
      </section>
    </template>

    <NDrawer
      v-model:show="isSampleDetailsOpen"
      placement="right"
      :width="editorDrawerWidth('36rem')"
      aria-label="Diagnostic sample details"
      class="editor-drawer diagnostic-sample-drawer"
    >
      <NDrawerContent title="Diagnostic sample details" closable>
        <div v-if="selectedSample" class="diagnostic-sample-details">
          <p class="diagnostic-sample-details__note">
            Values are complete. Backslashes and invisible control or formatting characters are shown in a reversible escaped form.
          </p>
          <dl>
            <div v-for="field in selectedSampleDetails" :key="field.label">
              <dt>{{ field.label }}</dt>
              <dd><bdi dir="ltr">{{ field.value }}</bdi></dd>
            </div>
          </dl>
        </div>
      </NDrawerContent>
    </NDrawer>
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
  grid-template-columns: auto 11rem auto;
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
  width: 11rem;
}

.breakdown-grid {
  display: grid;
  gap: 0.75rem;
}

.diagnostics-alert {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
}

.diagnostics-alert > div {
  display: grid;
  min-width: 0;
  gap: 0.25rem;
}

.diagnostics-alert bdi,
.diagnostics-alert small {
  direction: ltr;
  color: inherit;
  font-size: 0.75rem;
  unicode-bidi: isolate;
}

.diagnostics-refresh-state {
  margin: 0;
  border-left: 3px solid var(--app-accent);
  background: var(--app-panel-muted);
  padding: 0.625rem 0.75rem;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
}

.incident-summary__eyebrow bdi {
  direction: ltr;
  unicode-bidi: isolate;
}

.diagnostics-skeleton {
  display: grid;
  gap: 1rem;
}

.diagnostics-skeleton__lead,
.diagnostics-skeleton__facts,
.diagnostics-skeleton__grid {
  display: grid;
  gap: 0.75rem;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
  padding: 1rem;
}

.diagnostics-skeleton__facts,
.diagnostics-skeleton__grid {
  grid-template-columns: repeat(5, minmax(0, 1fr));
}

.incident-summary,
.diagnostics-panel {
  min-width: 0;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
}

.incident-summary {
  display: grid;
  gap: 1rem;
  border-left: 3px solid var(--app-accent);
  padding: 1rem;
}

.incident-summary__lead {
  min-width: 0;
}

.incident-summary__eyebrow {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
}

.incident-summary__eyebrow span,
.incident-facts dt {
  color: var(--app-text-muted);
  font-size: 0.72rem;
  font-weight: 700;
  text-transform: uppercase;
}

.incident-summary__eyebrow small,
.incident-summary__lead > p,
.incident-facts small {
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.incident-summary__lead h4 {
  margin: 0.35rem 0 0.25rem;
  color: var(--app-text);
  font-size: 1.125rem;
  font-weight: 700;
}

.incident-facts {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  margin: 0;
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 0.75rem;
}

.incident-facts > div {
  display: grid;
  min-width: 0;
  gap: 0.15rem;
  padding-inline: 0.75rem;
}

.incident-facts > div:first-child {
  padding-left: 0;
}

.incident-facts > div + div {
  border-left: 1px solid var(--app-border-subtle);
}

.incident-facts dd {
  margin: 0;
  color: var(--app-text);
  font-size: 1rem;
  font-weight: 700;
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

.panel-heading h5 {
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 700;
}

.breakdown-section {
  display: grid;
  gap: 0.75rem;
}

.diagnostics-panel--dimension {
  align-content: start;
  gap: 0.5rem;
  padding: 0.75rem;
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
  width: 100%;
  min-width: 0;
  grid-template-columns: 1.25rem minmax(0, 1fr);
  gap: 0.25rem 0.5rem;
  border-top: 1px solid var(--app-border);
  border-right: 0;
  border-bottom: 0;
  border-left: 0;
  background: transparent;
  padding: 0.55rem 0;
  color: var(--app-text);
  cursor: pointer;
  text-align: left;
}

.dimension-row:hover,
.dimension-row--selected {
  color: var(--app-accent);
}

.dimension-row--selected {
  box-shadow: inset 2px 0 0 var(--app-accent);
  padding-left: 0.5rem;
}

.dimension-rank {
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.7rem;
}

.dimension-name {
  min-width: 0;
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.82rem;
  font-weight: 650;
  text-overflow: ellipsis;
  unicode-bidi: isolate;
  white-space: nowrap;
}

.dimension-counts {
  display: grid;
  grid-column: 2;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0.35rem;
  color: var(--app-text-muted);
  font-size: 0.72rem;
}

.dimension-list {
  margin: 0;
  padding: 0;
  list-style: none;
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

.diagnostics-samples-heading {
  align-items: center;
}

.sample-filter {
  display: grid;
  min-width: min(100%, 19rem);
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 0.15rem 0.5rem;
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
  background: var(--app-panel);
  padding: 0.4rem 0.5rem;
}

.sample-filter > span {
  color: var(--app-text-muted);
  font-size: 0.7rem;
}

.sample-filter bdi {
  min-width: 0;
  overflow: hidden;
  direction: ltr;
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 0.75rem;
  text-overflow: ellipsis;
  unicode-bidi: isolate;
  white-space: nowrap;
}

.sample-filter :deep(.n-button) {
  grid-column: 2;
  grid-row: 1 / span 2;
}

.diagnostic-request-stack,
.diagnostic-outcome-cell,
.diagnostic-flow-cell,
.diagnostic-transfer {
  display: grid;
  min-width: 0;
  gap: 0.25rem;
}

.diagnostic-request-path {
  color: var(--app-text-muted);
}

.diagnostic-outcome-cell {
  grid-template-columns: max-content minmax(0, 1fr);
  align-items: center;
  gap: 0.5rem;
}

.diagnostic-retry-outcome {
  grid-column: 1 / -1;
  color: var(--app-accent);
  font-family: var(--font-mono);
  font-size: 0.7rem;
}

.diagnostic-flow-line {
  display: grid;
  min-width: 0;
  grid-template-columns: 3.5rem minmax(0, 1fr);
  gap: 0.4rem;
}

.diagnostic-flow-key {
  color: var(--app-text-muted);
  font-size: 0.68rem;
}

.diagnostic-transfer {
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.75rem;
}

.diagnostic-request-excerpt {
  display: grid;
  min-width: 0;
  grid-template-columns: max-content minmax(0, 1fr);
  align-items: baseline;
  gap: 0.5rem;
}

.diagnostic-request-method {
  color: var(--app-text);
  direction: ltr;
  font-family: var(--font-mono);
  font-size: 0.75rem;
  font-weight: 700;
  unicode-bidi: isolate;
}

.diagnostic-attacker-excerpt {
  display: block;
  min-width: 0;
  overflow: hidden;
  color: var(--app-text);
  direction: ltr;
  font-family: var(--font-mono);
  font-size: 0.75rem;
  text-overflow: ellipsis;
  unicode-bidi: isolate;
  white-space: nowrap;
}

.diagnostic-sample-details {
  display: grid;
  gap: 1rem;
}

.diagnostic-sample-details__note {
  border: 1px solid var(--app-border);
  border-radius: 7px;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  line-height: 1.55;
  padding: 0.75rem;
}

.diagnostic-sample-details dl {
  display: grid;
  margin: 0;
}

.diagnostic-sample-details dl > div {
  display: grid;
  min-width: 0;
  gap: 0.375rem;
  border-top: 1px solid var(--app-border-subtle);
  padding-block: 0.75rem;
}

.diagnostic-sample-details dt {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-weight: 650;
}

.diagnostic-sample-details dd {
  min-width: 0;
  margin: 0;
}

.diagnostic-sample-details bdi {
  display: block;
  max-height: 12rem;
  overflow: auto;
  color: var(--app-text);
  direction: ltr;
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  line-height: 1.55;
  overflow-wrap: anywhere;
  unicode-bidi: isolate;
  white-space: pre-wrap;
}

.panel-empty {
  align-self: start;
}

@media (min-width: 900px) {
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

  .incident-facts,
  .diagnostics-skeleton__facts,
  .diagnostics-skeleton__grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .incident-facts > div {
    border-left: 0 !important;
    border-top: 1px solid var(--app-border-subtle);
    padding: 0.65rem 0;
  }

  .diagnostics-samples-heading {
    display: grid;
  }

  .sample-filter {
    width: 100%;
  }
}

@media (max-width: 520px) {
  .incident-facts,
  .diagnostics-skeleton__facts,
  .diagnostics-skeleton__grid {
    grid-template-columns: 1fr;
  }

  .window-tabs :deep(.n-button) {
    padding-inline: 0.5rem;
  }
}

@media (pointer: coarse) {
  .header-controls :deep(.n-button),
  .header-controls :deep(.n-base-selection),
  .dimension-row,
  .sample-filter :deep(.n-button),
  .diagnostics-table-shell :deep(.n-button),
  .diagnostic-sample-drawer :deep(.n-base-close) {
    min-height: 44px;
  }
}
</style>
