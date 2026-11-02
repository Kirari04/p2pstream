<script setup lang="ts">
import { computed, h, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { NAlert, NButton, NDataTable, NDrawer, NDrawerContent, NEmpty, NSkeleton, NTab, NTabs, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import RetryHealthPanel from "@/components/RetryHealthPanel.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import MetricCard from "@/components/ui/MetricCard.vue";
import MetricStrip from "@/components/ui/MetricStrip.vue";
import PageHeader from "@/components/ui/PageHeader.vue";
import SectionCard from "@/components/ui/SectionCard.vue";
import SegmentedControl from "@/components/ui/SegmentedControl.vue";
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
import { admissionCapacitySignal } from "@/lib/admissionCapacity";
import {
  DIAGNOSTICS_SECTIONS,
  filterDiagnosticSamples,
  normalizeDiagnosticsSection,
  type DiagnosticDimensionFilter,
  type DiagnosticDimensionKey,
  type DiagnosticsSectionKey,
} from "@/lib/diagnosticsWorkbench";
import { editorDrawerWidth } from "@/lib/naiveUi";

type WindowLabel = "5m" | "1h" | "24h" | "30d";

const managementClient = useManagementClient();
const route = useRoute();
const router = useRouter();
const windowLabels: WindowLabel[] = ["5m", "1h", "24h", "30d"];
const windowOptions = windowLabels.map((label) => ({ value: label, label }));
const diagnosticsSections = DIAGNOSTICS_SECTIONS;
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
const selectedDimension = ref<DiagnosticDimensionFilter | null>(null);
let requestSequence = 0;

const activeDiagnosticsSection = computed<DiagnosticsSectionKey>(() => normalizeDiagnosticsSection(route.params.section));
const activeDiagnosticsSectionMeta = computed(() =>
  diagnosticsSections.find((section) => section.key === activeDiagnosticsSection.value) ?? diagnosticsSections[0],
);
const outcome = computed(() => diagnostics.value?.outcome);
const streamCapacity = computed(() => diagnostics.value?.agentStreamCapacity);
const capacityConstraintRows = computed(() => Object.entries(streamCapacity.value?.admissionMissesByConstraint ?? {})
  .filter(([, count]) => count > 0n)
  .sort((left, right) => left[1] === right[1] ? left[0].localeCompare(right[0]) : left[1] > right[1] ? -1 : 1)
  .map(([constraint, count]) => ({
    constraint,
    label: capacityConstraintLabel(constraint),
    count,
    waiting: streamCapacity.value?.waitersByConstraint[constraint] ?? 0n,
  })));
const capacitySignal = computed(() => streamCapacity.value ? admissionCapacitySignal(streamCapacity.value) : null);
const statusCodes = computed(() => diagnostics.value?.statusCodes ?? []);
const recentSamples = computed(() => diagnostics.value?.recentSamples ?? []);
const filteredRecentSamples = computed(() => filterDiagnosticSamples(recentSamples.value, selectedDimension.value));
const sampleFilterSummary = computed(() => {
  if (!selectedDimension.value) return `${recentSamples.value.length.toString()} loaded samples`;
  return `${filteredRecentSamples.value.length.toString()} of ${recentSamples.value.length.toString()} loaded samples`;
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
    { label: "First failed agent", value: inspectionValue(sample.retryFailedAgentLabel) },
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
    width: 112,
    render: (sample) => h("span", { class: "diagnostic-time" }, formatSampleTime(sample.occurredAtUnixMillis)),
  },
  {
    title: "Request",
    key: "request",
    width: 216,
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
    width: 156,
    render: (sample) => h("span", { class: "diagnostic-outcome-cell" }, [
      h("span", { class: ["status-pill", `tone-${statusTone(sample.statusCode)}`] }, sampleStatusLabel(sample)),
      attackerCell(sample.errorKind, 48),
      sample.retryRuleId > 0n && (sample.retryCount > 0n || Boolean(sample.retryOutcome))
        ? h("span", { class: "diagnostic-retry-outcome" }, retrySampleLabel(sample))
        : null,
    ]),
  },
  {
    title: "Resolved flow",
    key: "flow",
    width: 208,
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
    width: 84,
    render: (sample) => h("span", { class: "diagnostic-number" }, formatDuration(sample.durationMs)),
  },
  {
    title: "Transfer",
    key: "transfer",
    width: 96,
    render: (sample) => h("span", { class: "diagnostic-transfer" }, [
      h("span", `↓ ${formatBytes(sample.responseBytes)}`),
      h("span", `↑ ${formatBytes(sample.requestBytes)}`),
    ]),
  },
  {
    title: "Details",
    key: "details",
    width: 72,
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

watch(selectedWindowLabel, () => {
  selectedDimension.value = null;
  void loadDiagnostics(true);
});

watch(sampleLimit, () => {
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
  return sample.statusCode > 0n ? sample.statusCode.toString() : "—";
}

function retrySampleLabel(sample: DashboardDiagnosticsSample): string {
  const outcomeLabel = sample.retryOutcome || "attempted";
  if (sample.retryCount <= 0n) return `retry ${outcomeLabel}`;
  return `${sample.retryCount.toString()} ${sample.retryCount === 1n ? "retry" : "retries"} · ${outcomeLabel}`;
}

function openSampleDetails(sample: DashboardDiagnosticsSample) {
  selectedSample.value = sample;
  isSampleDetailsOpen.value = true;
}

async function selectDiagnosticsSection(value: string | number) {
  const section = diagnosticsSections.find((item) => item.key === value);
  if (!section || section.key === activeDiagnosticsSection.value) return;
  await router.push(section.path);
}

async function inspectDimension(key: DiagnosticDimensionKey, row: DashboardProxyDimensionSummary, title: string) {
  await inspectDimensionLabel(key, row.label, title);
}

async function inspectDimensionLabel(key: DiagnosticDimensionKey, label: string, title: string) {
  selectedDimension.value = { key, label, title };
  if (activeDiagnosticsSection.value !== "samples") {
    await router.push("/monitor/diagnostics/samples");
  }
}

function dimensionSelected(key: DiagnosticDimensionKey, label: string): boolean {
  return selectedDimension.value?.key === key && selectedDimension.value.label === label;
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
  if (millis <= 0) return "—";
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
    sample.retryFailedAgentLabel,
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

function capacityConstraintLabel(constraint: string): string {
  switch (constraint) {
    case "total_budget": return "Server total full";
    case "public_budget": return "Public stream lane full";
    case "pooled_budget": return "Reusable stream lane full";
    case "control_budget": return "Health-check lane full";
    case "session_budget": return "Selected agent session full";
    case "session_opening_limit": return "Agent stream-opening backlog full";
    case "fair_turn": return "Waiting for fair admission turn";
    case "queue_full": return "Global admission queue full";
    case "key_queue_full": return "Route admission queue full";
    case "class_disabled": return "Admission lane disabled";
    default: return constraint || "Unknown constraint";
  }
}
</script>

<template>
  <div class="diagnostics-page">
    <PageHeader title="Diagnostics" :description="activeDiagnosticsSectionMeta.description">
      <template #actions>
        <SegmentedControl v-model="selectedWindowLabel" :options="windowOptions" label="Diagnostics window" />
        <NButton secondary size="small" attr-type="button" :loading="isLoading && Boolean(diagnostics)" @click="loadDiagnostics(false)">
          Refresh
        </NButton>
      </template>
    </PageHeader>

    <NTabs
      class="diagnostics-section-tabs"
      type="line"
      :value="activeDiagnosticsSection"
      aria-label="Diagnostics sections"
      @update:value="selectDiagnosticsSection"
    >
      <NTab
        v-for="section in diagnosticsSections"
        :key="section.key"
        :name="section.key"
        :tab="section.label"
      />
    </NTabs>

    <NAlert v-if="error" :type="diagnostics ? 'warning' : 'error'" :show-icon="false" :bordered="false">
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
        <NSkeleton v-for="index in 5" :key="index" height="4.25rem" :sharp="false" />
      </div>
      <NSkeleton height="12rem" :sharp="false" />
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

      <SectionCard
        v-if="activeDiagnosticsSection === 'overview'"
        class="incident-summary"
        :title="incidentTitle"
        :description="incidentSummary"
        :aria-busy="isLoading"
      >
        <template #actions>
          <span class="diagnostics-meta">Incident snapshot · <bdi dir="ltr">{{ diagnostics.label ? diagnosticInspectionText(diagnostics.label) : selectedWindowLabel }}</bdi> · generated {{ generatedAtLabel }}</span>
        </template>
        <MetricStrip variant="tiles" label="Incident snapshot">
          <MetricCard
            label="Requests"
            :value="formatNumber(outcome?.requests)"
            :detail="`${formatPercent(outcome && outcome.requests > 0n ? toNumber(outcome.success) / Math.max(1, toNumber(outcome.requests)) : 0)} successful`"
          />
          <MetricCard label="Successful" :value="formatNumber(outcome?.success)" detail="2xx + 3xx" />
          <MetricCard label="Non-success" :value="formatNumber(outcome?.nonSuccess)" :tone="outcome && outcome.nonSuccess > 0n ? 'warning' : 'default'" detail="4xx + 5xx" />
          <MetricCard label="Proxy failures" :value="formatNumber(outcome?.proxyFailure)" :tone="outcome && outcome.proxyFailure > 0n ? 'error' : 'default'" detail="error kind set" />
          <MetricCard label="Latency" :value="formatDuration(outcome?.avgDurationMs)" :detail="`max ${formatDuration(outcome?.maxDurationMs)}`" />
        </MetricStrip>
      </SectionCard>

      <SectionCard
        v-if="activeDiagnosticsSection === 'overview' && streamCapacity && capacitySignal"
        class="capacity-observatory"
        title="Tunnel capacity"
        description="Live admission state: current stream allowance and application reservations. These values are live at refresh time and are not scoped to the selected incident window."
      >
        <template #badge>
          <NTag size="small" :bordered="false" :type="capacitySignal.tone">
            {{ capacitySignal.label }}
          </NTag>
        </template>
        <MetricStrip variant="tiles" label="Tunnel capacity" class="capacity-meters">
          <MetricCard
            :label="streamCapacity.adaptive ? 'Adaptive allowance' : 'Total streams'"
            :value="`${formatNumber(streamCapacity.totalInUse)} / ${formatNumber(streamCapacity.adaptive ? streamCapacity.adaptiveAdmissionLimit : streamCapacity.totalCapacity)}`"
          >
            <template #detail>{{ formatNumber(streamCapacity.opening) }} opening · {{ formatNumber(streamCapacity.live) }} live · {{ formatNumber(streamCapacity.closing) }} closing<span v-if="streamCapacity.adaptive"> · guard {{ formatNumber(streamCapacity.totalCapacity) }}</span></template>
          </MetricCard>
          <MetricCard
            v-if="streamCapacity.adaptive"
            label="Measured memory"
            :value="streamCapacity.memoryLimitBytes > 0n ? `${((Number(streamCapacity.memoryUsedBytes) / Number(streamCapacity.memoryLimitBytes)) * 100).toFixed(1)}%` : 'Unknown'"
            :text-value="streamCapacity.memoryLimitBytes <= 0n"
            :detail="streamCapacity.memoryLimitBytes > 0n ? `${formatBytes(streamCapacity.memoryUsedBytes)} / ${formatBytes(streamCapacity.memoryLimitBytes)} · ${streamCapacity.memorySource}` : 'No valid memory sample'"
          />
          <MetricCard
            v-if="streamCapacity.adaptive"
            label="File descriptors"
            :value="streamCapacity.fileDescriptorsLimit > 0n ? `${formatNumber(streamCapacity.fileDescriptorsUsed)} / ${formatNumber(streamCapacity.fileDescriptorsLimit)}` : 'Unknown'"
            :text-value="streamCapacity.fileDescriptorsLimit <= 0n"
          >
            <template #detail>
              <template v-if="streamCapacity.fileDescriptorsLimit > 0n">{{ ((Number(streamCapacity.fileDescriptorsUsed) / Number(streamCapacity.fileDescriptorsLimit)) * 100).toFixed(1) }}% used<span v-if="streamCapacity.resourcePressureReason"> · limiting signal {{ streamCapacity.resourcePressureReason }}</span></template>
              <template v-else>Descriptor pressure signal unavailable</template>
            </template>
          </MetricCard>
          <MetricCard
            v-if="streamCapacity.adaptive"
            label="Pre-stream reservations"
            :value="formatBytes(streamCapacity.adaptiveExternalBytes)"
            :detail="`${formatNumber(streamCapacity.adaptiveExternalFileDescriptors)} descriptors · raw allowance ${formatNumber(streamCapacity.adaptiveRawAdmissionLimit)}`"
          />
          <MetricCard
            label="Public connections"
            :value="formatNumber(streamCapacity.publicConnections)"
            :detail="`${formatNumber(streamCapacity.publicConnectionLimitRejected)} peer/hard socket rejects · ${formatNumber(streamCapacity.publicConnectionResourceRejected)} resource rejects · ${formatNumber(streamCapacity.publicClientRequestRejected)} client request rejects`"
          />
          <MetricCard
            v-if="streamCapacity.resourceSampleError"
            label="Resource sensor"
            value="Degraded"
            tone="warning"
            text-value
            :detail="streamCapacity.resourceLastGoodUnixMillis > 0n ? `Last good sample ${formatSampleTime(streamCapacity.resourceLastGoodUnixMillis)}` : 'New stream admission is paused until sensing recovers'"
          />
          <MetricCard
            label="Public lane"
            :value="`${formatNumber(streamCapacity.publicInUse)} / ${formatNumber(streamCapacity.adaptive ? streamCapacity.adaptivePublicAdmissionLimit : streamCapacity.publicCapacity)}`"
          >
            <template #detail>{{ formatNumber(streamCapacity.publicWaiters) }} waiting<span v-if="streamCapacity.adaptive"> · structural guard {{ formatNumber(streamCapacity.publicCapacity) }}</span></template>
          </MetricCard>
          <MetricCard
            label="Reusable lane"
            :value="`${formatNumber(streamCapacity.pooledInUse)} / ${formatNumber(streamCapacity.pooledCapacity)}`"
            :detail="`${formatNumber(streamCapacity.pooledTransportShards)} transport shards`"
          />
          <MetricCard
            label="Busiest agent session"
            :value="formatNumber(streamCapacity.maxSessionPublicInUse)"
            :detail="streamCapacity.contendedSessionPublicLimit > 0n ? `shared-pressure limit ${formatNumber(streamCapacity.contendedSessionPublicLimit)}` : `${formatNumber(streamCapacity.registeredSessions)} registered sessions`"
          />
          <MetricCard
            label="Terminal rejections"
            :value="formatNumber(streamCapacity.terminalCapacityFailures)"
            :tone="streamCapacity.terminalCapacityFailures > 0n ? 'error' : 'default'"
            detail="customer-visible capacity failures since server start"
          />
        </MetricStrip>
        <div class="capacity-constraints">
          <div>
            <h3>Exact admission constraints</h3>
            <p>Admission misses are cumulative since server start and include pooled misses recovered by fallback.</p>
          </div>
          <ol v-if="capacityConstraintRows.length">
            <li v-for="row in capacityConstraintRows" :key="row.constraint">
              <span><bdi dir="ltr">{{ row.label }}</bdi><code>{{ row.constraint }}</code></span>
              <strong>{{ formatNumber(row.count) }}</strong>
              <small v-if="row.waiting > 0n">{{ formatNumber(row.waiting) }} waiting now</small>
            </li>
          </ol>
          <p v-else class="capacity-constraints__empty">No capacity admission misses since server start.</p>
        </div>
      </SectionCard>

      <RetryHealthPanel
        v-if="activeDiagnosticsSection === 'retries'"
        :retry-health="diagnostics.retryHealth"
        :retry-trend="diagnostics.retryTrend"
        :retry-rules="diagnostics.retryRules"
        :retry-failed-agents="diagnostics.retryFailedAgents"
        :retry-error-kinds="diagnostics.retryErrorKinds"
        :generated-at-unix-millis="diagnostics.generatedAtUnixMillis"
        :window-label="selectedWindowLabel"
        :selected-filter="selectedDimension"
        @select-dimension="inspectDimensionLabel"
      />

      <SectionCard v-if="activeDiagnosticsSection === 'overview'" title="Status codes" description="Exact response distribution for the selected window." :flush="statusCodes.length > 0">
        <ul v-if="statusCodes.length" class="status-list">
          <li v-for="row in statusCodes" :key="row.statusCode.toString()" class="status-row">
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
          </li>
        </ul>
        <p v-else class="diagnostics-empty">No status codes in this window.</p>
      </SectionCard>

      <SectionCard
        v-if="activeDiagnosticsSection === 'overview'"
        title="Choose an investigation lane"
        description="Each workspace has one job and keeps its own controls close to the evidence they affect."
        aria-label="Choose an investigation lane"
      >
        <div class="investigation-grid">
          <RouterLink class="investigation-lane" to="/monitor/diagnostics/retries">
            <div>
              <strong>Retry health</strong>
              <p>Decide whether retries recovered transport failures or merely delayed exhaustion.</p>
            </div>
            <dl>
              <div><dt>Matched</dt><dd>{{ formatNumber(diagnostics.retryHealth?.matchedRequests) }}</dd></div>
              <div><dt>Exhausted</dt><dd :class="{ 'lane-value--error': (diagnostics.retryHealth?.exhaustedRequests ?? 0n) > 0n }">{{ formatNumber(diagnostics.retryHealth?.exhaustedRequests) }}</dd></div>
            </dl>
            <span class="investigation-lane__action">Open retry health →</span>
          </RouterLink>
          <RouterLink class="investigation-lane" to="/monitor/diagnostics/failures">
            <div>
              <strong>Failure map</strong>
              <p>Rank the exact errors, listeners, routes, targets, and agents involved.</p>
            </div>
            <dl>
              <div><dt>Proxy failures</dt><dd :class="{ 'lane-value--error': (outcome?.proxyFailure ?? 0n) > 0n }">{{ formatNumber(outcome?.proxyFailure) }}</dd></div>
              <div><dt>Error kinds</dt><dd>{{ diagnostics.errorKinds.length.toString() }}</dd></div>
            </dl>
            <span class="investigation-lane__action">Explore dimensions →</span>
          </RouterLink>
          <RouterLink class="investigation-lane" to="/monitor/diagnostics/samples">
            <div>
              <strong>Request samples</strong>
              <p>Inspect concrete request evidence after the aggregate signal points somewhere.</p>
            </div>
            <dl>
              <div><dt>Loaded</dt><dd>{{ recentSamples.length.toString() }}</dd></div>
              <div><dt>Window</dt><dd>{{ selectedWindowLabel }}</dd></div>
            </dl>
            <span class="investigation-lane__action">Inspect requests →</span>
          </RouterLink>
        </div>
      </SectionCard>

      <SectionCard
        v-if="activeDiagnosticsSection === 'failures'"
        variant="plain"
        title="Ranked failure dimensions"
        description="Select a row to open request samples filtered by that exact value."
        aria-label="Ranked failure dimensions"
      >
        <div class="breakdown-grid">
          <SectionCard v-for="section in dimensionSections" :key="section.title" :title="section.title" :heading-level="3" flush>
            <ol v-if="section.rows.length" class="dimension-list">
              <li v-for="(row, index) in section.rows" :key="`${section.title}-${row.id.toString()}-${row.label}`">
                <button
                  type="button"
                  class="dimension-row"
                  :class="{ 'dimension-row--selected': dimensionSelected(section.key, row.label) }"
                  :aria-pressed="dimensionSelected(section.key, row.label)"
                  @click="inspectDimension(section.key, row, section.title)"
                >
                  <span class="dimension-rank">{{ index + 1 }}</span>
                  <bdi class="dimension-name" dir="ltr" :title="inspectionValue(row.label)">{{ diagnosticExcerpt(row.label, 56).text }}</bdi>
                  <span class="dimension-counts">
                    <span>{{ formatNumber(row.requests) }} req</span>
                    <span>{{ formatNumber(dimensionNonSuccess(row)) }} non-success</span>
                    <span>{{ formatNumber(dimensionProxyFailures(row)) }} failures</span>
                  </span>
                  <span class="dimension-action">Inspect samples →</span>
                </button>
              </li>
            </ol>
            <p v-else class="diagnostics-empty diagnostics-empty--compact">{{ section.empty }}</p>
          </SectionCard>
        </div>
      </SectionCard>

      <SectionCard
        v-if="activeDiagnosticsSection === 'samples'"
        class="diagnostics-samples"
        title="Request samples"
        description="Newest retained non-success, proxy-failure, and retry requests. Filters are exact matches against this loaded sample set; they do not change the server-wide aggregates."
        flush
      >
        <template #actions>
          <div class="sample-controls">
            <NTag size="small" :bordered="false" :type="selectedDimension ? 'info' : 'default'">{{ sampleFilterSummary }}</NTag>
            <AccessibleSelect
              v-model:value="sampleLimit"
              accessible-label="Loaded diagnostic sample limit"
              class="sample-select"
              size="small"
              :options="sampleSelectOptions"
            />
          </div>
        </template>
        <div class="sample-filter-bar">
          <div v-if="selectedDimension" class="sample-filter" role="status">
            <span>Exact filter · {{ selectedDimension.title }}</span>
            <bdi dir="ltr">{{ inspectionValue(selectedDimension.label) }}</bdi>
            <NButton quaternary size="small" attr-type="button" @click="selectedDimension = null">Show all loaded samples</NButton>
          </div>
          <p v-else class="sample-filter-guide">
            No exact-match filter is active. Open <RouterLink to="/monitor/diagnostics/failures">Failure map</RouterLink> or <RouterLink to="/monitor/diagnostics/retries">Retry health</RouterLink>, then choose a dimension to inspect its matching samples here.
          </p>
        </div>
        <div v-if="filteredRecentSamples.length" class="diagnostics-table-shell">
          <NDataTable
            :columns="sampleColumns"
            :data="filteredRecentSamples"
            :row-key="sampleRowKey"
            :pagination="false"
            :bordered="false"
           
            :scroll-x="944"
            size="small"
          />
        </div>
        <NEmpty
          v-else
          size="small"
          class="diagnostics-table-empty"
          :description="recentSamples.length ? 'No retained samples match the selected failure dimension.' : 'No recent failure or retry samples in this window.'"
        >
          <template v-if="selectedDimension" #extra>
            <NButton secondary size="small" attr-type="button" @click="selectedDimension = null">Clear sample filter</NButton>
          </template>
        </NEmpty>
      </SectionCard>
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
  min-width: 0;
  gap: 1.25rem;
}

.diagnostics-section-tabs {
  min-width: 0;
  margin-top: -0.5rem;
}

.diagnostics-section-tabs :deep(.n-tabs-nav) {
  margin-bottom: 0;
}

.diagnostics-section-tabs :deep(.n-tabs-tab) {
  padding: 0.625rem 0.125rem 0.75rem;
  font-size: 0.875rem;
}

.diagnostics-section-tabs :deep(.n-tabs-tab + .n-tabs-tab) {
  margin-left: 1.25rem;
}

.diagnostics-meta {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
}

.diagnostics-meta bdi,
.diagnostics-alert bdi {
  direction: ltr;
  unicode-bidi: isolate;
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
  color: inherit;
  font-size: 0.75rem;
}

.diagnostics-refresh-state {
  margin: 0;
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 0.625rem 0.875rem;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
}

.diagnostics-empty {
  color: var(--app-text-muted);
  font-size: 0.8125rem;
}

.diagnostics-empty--compact {
  padding: 0.75rem 1.25rem 1rem;
}

/* Loading */
.diagnostics-skeleton {
  display: grid;
  gap: 1rem;
}

.diagnostics-skeleton__lead {
  display: grid;
  gap: 0.75rem;
}

.diagnostics-skeleton__facts {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: 0.5rem;
}

/* Overview: incident and capacity tiles */
.capacity-observatory .capacity-meters {
  grid-template-columns: repeat(auto-fill, minmax(11rem, 1fr));
}

.capacity-meters :deep(.metric-card__detail) {
  white-space: normal;
}

.capacity-constraints {
  display: grid;
  grid-template-columns: minmax(12rem, 0.75fr) minmax(0, 1.25fr);
  gap: 1rem 1.5rem;
  align-items: start;
  margin-top: 1rem;
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 1rem;
}

.capacity-constraints h3 {
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
}

.capacity-constraints p {
  margin-top: 0.125rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.5;
}

.capacity-constraints ol {
  display: grid;
  margin: 0;
  padding: 0;
  list-style: none;
}

.capacity-constraints li {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 0.125rem 0.75rem;
  padding: 0.5rem 0;
}

.capacity-constraints li + li {
  border-top: 1px solid var(--app-border-subtle);
}

.capacity-constraints li:first-child {
  padding-top: 0;
}

.capacity-constraints li > span {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  gap: 0.25rem 0.5rem;
  color: var(--app-text);
  font-size: 0.8125rem;
}

.capacity-constraints code {
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.75rem;
}

.capacity-constraints li > strong {
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.capacity-constraints li > small {
  grid-column: 1 / -1;
  color: var(--app-warning);
  font-size: 0.75rem;
}

.capacity-constraints__empty {
  margin-top: 0;
}

/* Status code distribution */
.status-list {
  display: grid;
  margin: 0;
  padding: 0;
  list-style: none;
}

.status-row {
  display: grid;
  grid-template-columns: 8rem minmax(10rem, 1fr);
  gap: 0.375rem 1rem;
  align-items: center;
  padding: 0.625rem 1.25rem;
}

.status-row + .status-row {
  border-top: 1px solid var(--app-border-subtle);
}

.status-label {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.625rem;
}

.status-label strong {
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 0.875rem;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.status-bar-track {
  overflow: hidden;
  height: 0.375rem;
  border-radius: 999px;
  background: var(--app-panel-muted);
}

.status-bar {
  height: 100%;
  min-width: 0.375rem;
  border-radius: 999px;
  background: var(--tone-color);
}

.status-meta {
  display: grid;
  grid-column: 2;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 0.5rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
}

.status-meta span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Status pills are shared by the status list and the samples table (rendered via h()). */
.status-pill,
.diagnostics-samples :deep(.status-pill) {
  display: inline-flex;
  min-width: 2.75rem;
  height: 1.375rem;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  background: color-mix(in srgb, var(--tone-color) 14%, transparent);
  color: var(--tone-color);
  font-family: var(--font-mono);
  font-size: 0.75rem;
  font-weight: 600;
}

.tone-success,
.diagnostics-samples :deep(.tone-success) { --tone-color: var(--app-success); }
.tone-redirect,
.diagnostics-samples :deep(.tone-redirect) { --tone-color: var(--app-accent); }
.tone-client-error,
.diagnostics-samples :deep(.tone-client-error) { --tone-color: var(--app-warning); }
.tone-server-error,
.diagnostics-samples :deep(.tone-server-error) { --tone-color: var(--app-error); }
.tone-neutral,
.diagnostics-samples :deep(.tone-neutral) { --tone-color: var(--app-text-muted); }

/* Investigation lanes: muted tiles inside one card */
.investigation-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0.5rem;
}

.investigation-lane {
  display: grid;
  min-width: 0;
  gap: 0.75rem;
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 0.875rem 1rem;
  color: inherit;
  text-decoration: none;
  transition: background-color 140ms ease;
}

.investigation-lane:hover {
  background: var(--app-hover);
}

.investigation-lane:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: 2px;
}

.investigation-lane strong {
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
}

.investigation-lane p {
  margin-top: 0.25rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.5;
}

.investigation-lane dl {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  margin: 0;
}

.investigation-lane dl > div {
  min-width: 0;
}

.investigation-lane dt {
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.investigation-lane dd {
  margin: 0.125rem 0 0;
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 1rem;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.investigation-lane dd.lane-value--error {
  color: var(--app-error);
}

.investigation-lane__action {
  color: var(--app-accent);
  font-size: 0.8125rem;
  font-weight: 500;
}

/* Failure map */
.breakdown-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(17rem, 1fr));
  gap: 1rem;
  align-items: start;
}

.dimension-list {
  display: grid;
  margin: 0;
  padding: 0;
  list-style: none;
}

.dimension-list li + li {
  border-top: 1px solid var(--app-border-subtle);
}

.dimension-row {
  display: grid;
  width: 100%;
  min-width: 0;
  grid-template-columns: 1.25rem minmax(0, 1fr);
  gap: 0.25rem 0.5rem;
  border: 0;
  background: transparent;
  padding: 0.625rem 1.25rem;
  color: var(--app-text);
  cursor: pointer;
  font: inherit;
  text-align: left;
}

.dimension-row:hover {
  background: var(--app-hover);
}

.dimension-row:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: -2px;
}

.dimension-row--selected {
  background: var(--app-accent-soft);
  box-shadow: inset 2px 0 0 var(--app-accent);
}

.dimension-rank {
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
}

.dimension-name {
  min-width: 0;
  overflow: hidden;
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  font-weight: 500;
  text-overflow: ellipsis;
  unicode-bidi: isolate;
  white-space: nowrap;
}

.dimension-row--selected .dimension-name {
  color: var(--app-accent);
}

.dimension-counts {
  display: flex;
  grid-column: 2;
  flex-wrap: wrap;
  gap: 0.125rem 0.75rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
}

.dimension-action {
  grid-column: 2;
  color: var(--app-accent);
  font-size: 0.75rem;
  font-weight: 500;
}

/* Request samples */
.sample-controls {
  display: flex;
  flex: none;
  align-items: center;
  gap: 0.5rem;
}

.sample-select {
  width: 11rem;
}

.sample-filter-bar {
  border-bottom: 1px solid var(--app-border-subtle);
  padding: 0.75rem 1.25rem;
}

.sample-filter {
  display: grid;
  min-width: 0;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 0.125rem 0.75rem;
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 0.5rem 0.5rem 0.5rem 0.75rem;
}

.sample-filter > span {
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.sample-filter bdi {
  min-width: 0;
  overflow: hidden;
  direction: ltr;
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  text-overflow: ellipsis;
  unicode-bidi: isolate;
  white-space: nowrap;
}

.sample-filter :deep(.n-button) {
  grid-column: 2;
  grid-row: 1 / span 2;
  align-self: center;
}

.sample-filter-guide {
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  line-height: 1.5;
}

.sample-filter-guide a {
  color: var(--app-accent);
  font-weight: 500;
}

.sample-filter-guide a:hover {
  text-decoration: underline;
}

.diagnostics-table-shell {
  min-width: 0;
}

.diagnostics-table-empty {
  padding: 2rem 1rem;
}

/* Cells rendered by NDataTable via h() do not carry this component's scope id; reach them with :deep(). */
.diagnostics-samples :deep(.diagnostic-time),
.diagnostics-samples :deep(.diagnostic-number) {
  font-family: var(--font-mono);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
}

.diagnostics-samples :deep(.diagnostic-request-stack),
.diagnostics-samples :deep(.diagnostic-outcome-cell),
.diagnostics-samples :deep(.diagnostic-flow-cell),
.diagnostics-samples :deep(.diagnostic-transfer) {
  display: grid;
  min-width: 0;
  gap: 0.25rem;
}

.diagnostics-samples :deep(.diagnostic-request-excerpt) {
  display: grid;
  min-width: 0;
  grid-template-columns: max-content minmax(0, 1fr);
  align-items: baseline;
  gap: 0.5rem;
}

.diagnostics-samples :deep(.diagnostic-request-method) {
  color: var(--app-text);
  direction: ltr;
  font-family: var(--font-mono);
  font-size: 0.75rem;
  font-weight: 600;
  unicode-bidi: isolate;
}

.diagnostics-samples :deep(.diagnostic-attacker-excerpt) {
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

.diagnostics-samples :deep(.diagnostic-request-path) {
  color: var(--app-text-muted);
}

.diagnostics-samples :deep(.diagnostic-outcome-cell) {
  grid-template-columns: max-content minmax(0, 1fr);
  align-items: center;
  gap: 0.5rem;
}

.diagnostics-samples :deep(.diagnostic-retry-outcome) {
  grid-column: 1 / -1;
  color: var(--app-accent);
  font-family: var(--font-mono);
  font-size: 0.75rem;
}

.diagnostics-samples :deep(.diagnostic-flow-cell) {
  gap: 0.125rem;
}

.diagnostics-samples :deep(.diagnostic-flow-line) {
  display: grid;
  min-width: 0;
  grid-template-columns: 3.25rem minmax(0, 1fr);
  gap: 0.375rem;
}

.diagnostics-samples :deep(.diagnostic-flow-key) {
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.diagnostics-samples :deep(.diagnostic-transfer) {
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
}

/* Sample details drawer */
.diagnostic-sample-details {
  display: grid;
  gap: 1rem;
}

.diagnostic-sample-details__note {
  border-radius: 8px;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  line-height: 1.55;
  padding: 0.75rem 0.875rem;
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
  font-weight: 500;
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

@media (max-width: 860px) {
  .investigation-grid {
    grid-template-columns: 1fr;
  }

  .status-row {
    grid-template-columns: 1fr;
  }

  .status-meta {
    grid-column: 1;
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .diagnostics-skeleton__facts {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .capacity-constraints {
    grid-template-columns: 1fr;
  }

  .sample-controls {
    display: grid;
    width: 100%;
    grid-template-columns: 1fr;
  }

  .sample-select {
    width: 100%;
  }
}

@media (max-width: 639px) {
  .diagnostics-samples :deep(.section-card__actions) {
    align-self: stretch;
  }

  .status-row,
  .dimension-row,
  .sample-filter-bar {
    padding-inline: 1rem;
  }

  .diagnostics-empty--compact {
    padding-inline: 1rem;
  }
}

@media (pointer: coarse) {
  .diagnostics-page :deep(.page-header__actions .n-button),
  .sample-controls :deep(.n-base-selection),
  .dimension-row,
  .sample-filter :deep(.n-button),
  .diagnostics-table-shell :deep(.n-button),
  .diagnostic-sample-drawer :deep(.n-base-close),
  .diagnostics-section-tabs :deep(.n-tabs-tab),
  .investigation-lane {
    min-height: 44px;
  }
}
</style>
