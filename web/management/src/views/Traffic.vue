<script setup lang="ts">
import { computed, h, inject, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import { NAlert, NButton, NCheckbox, NDataTable, NEmpty, NInput, NRadioButton, NRadioGroup, NSkeleton, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import MetricCard from "@/components/ui/MetricCard.vue";
import MetricStrip from "@/components/ui/MetricStrip.vue";
import PageHeader from "@/components/ui/PageHeader.vue";
import SectionCard from "@/components/ui/SectionCard.vue";
import { dashboardKey, publicProxyConfigKey, selectedEnvironmentBlockedKey, selectedEnvironmentIdKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import TrafficFlowEditTargetChooser from "@/components/editors/TrafficFlowEditTargetChooser.vue";
import TrafficFlowDiagram from "@/components/TrafficFlowDiagram.vue";
import TrafficTraceDetailsModal from "@/components/TrafficTraceDetailsModal.vue";
import { NO_TRACES_REASON, TRACE_BUSY_REASON } from "@/lib/disabledReasons";
import { TrafficTraceStore, isTerminalStage, traceStreamRequestForSequence } from "@/lib/trafficTraceStore";
import type { TrafficFlowEditRequest, TrafficFlowEditTarget } from "@/types/trafficFlowEdit";
import type { TraceRenderStats, TraceRequest, TraceRequestView } from "@/types/trafficTrace";
import { emptyTraceRenderStats } from "@/types/trafficTrace";
import type { TrafficTraceEvent, TrafficTraceSettings } from "@/gen/proto/p2pstream/v1/management_pb";
import { TrafficTraceLevel, TrafficTraceStage } from "@/gen/proto/p2pstream/v1/management_pb";
import { messageFromError } from "@/lib/errors";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";

const managementClient = useManagementClient();

const dashboard = inject(dashboardKey, computed(() => null));
const publicProxyConfig = inject(publicProxyConfigKey, computed(() => null));
const selectedEnvironmentId = inject(selectedEnvironmentIdKey, computed(() => "0"));
const selectedEnvironmentBlocked = inject(selectedEnvironmentBlockedKey, computed(() => ""));

const config = computed(() => publicProxyConfig?.value ?? null);

const traceSettings = ref<TrafficTraceSettings | null>(null);
const selectedTraceLevel = ref<TrafficTraceLevel>(TrafficTraceLevel.UNSPECIFIED);
const traceSettingsState = ref<"loading" | "ready" | "error" | "blocked">("loading");
const isTraceBusy = ref(false);
const isPaused = ref(false);
const streamState = ref<"idle" | "connecting" | "live" | "retrying" | "error">("idle");
const streamError = ref("");
const tableRequests = shallowRef<TraceRequestView[]>([]);
const diagramRequests = shallowRef<TraceRequest[]>([]);
const renderStats = shallowRef<TraceRenderStats>(emptyTraceRenderStats());
const traceSnapshotVersion = ref(0);
const selectedRequestId = ref<string | null>(null);
const selectedRequest = computed(() => {
  traceSnapshotVersion.value;
  return selectedRequestId.value ? traceStore.get(selectedRequestId.value) : null;
});
const isDetailsOpen = ref(false);
const renderedTokenCount = ref(0);
const editorHost = ref<InstanceType<typeof PublicProxyEditorHost> | null>(null);
const pendingEditRequest = ref<TrafficFlowEditRequest | null>(null);
const isEditChooserOpen = ref(false);
const showDebugStats = ref(false);
const traceSearch = ref("");
const traceFilter = ref<"all" | "problems" | "in-flight" | "completed">("all");
const traceStore = new TrafficTraceStore(applyTraceStoreSnapshot);

let streamController: AbortController | null = null;
let retryTimer: number | null = null;
let retryDelayMs = 1000;
let traceSettingsLoadVersion = 0;

const traceLevelOptions = [
  { label: "Basic", value: TrafficTraceLevel.BASIC },
  { label: "Detailed", value: TrafficTraceLevel.DETAILED },
  { label: "Headers", value: TrafficTraceLevel.HEADERS },
  { label: "Debug", value: TrafficTraceLevel.DEBUG },
];
const traceFilterOptions = [
  { label: "All traces", value: "all" },
  { label: "Problems", value: "problems" },
  { label: "In flight", value: "in-flight" },
  { label: "Completed", value: "completed" },
];
const traceColumns = computed<DataTableColumns<TraceRequestView>>(() => [
  {
    title: "Request",
    key: "request",
    minWidth: 260,
    render: (request) => h("div", [
      h("div", { class: "layout-row align-center space-sm" }, [
        h("bdi", { class: "round-sm framed frame-standard pad-x-xs pad-y-2xs mono-text copy-2xs base-text trace-hostile-value", dir: "ltr" }, diagnosticExcerpt(request.methodLabel, 18).text),
        h("bdi", { class: "max-token-width clip-text mono-text copy-xs base-text trace-hostile-value", dir: "ltr", title: diagnosticInspectionText(request.pathLabel) }, diagnosticExcerpt(request.pathLabel).text),
      ]),
      h("bdi", { class: "margin-top-xs max-trace-width clip-text mono-text copy-2xs muted-text trace-hostile-value", dir: "ltr", title: diagnosticInspectionText(request.requestIdLabel) }, diagnosticExcerpt(request.requestIdLabel).text),
    ]),
  },
  {
    title: "Flow",
    key: "flow",
    minWidth: 260,
    render: (request) => h("div", [
      h("bdi", { class: "copy-xs base-text trace-hostile-value trace-flow-label", dir: "ltr" }, diagnosticExcerpt(request.flowLabel, 112).text),
      h("p", { class: "margin-top-xs copy-2xs muted-text" }, `${request.stageLabel}${request.sampledEventCount ? ` / sampled ${numberLabel(request.sampledEventCount)}` : ""}`),
    ]),
  },
  {
    title: "Status",
    key: "status",
    width: 100,
    align: "right",
    render: (request) => h("span", { class: ["mono-text copy-xs", request.statusClass] }, request.statusLabel),
  },
  {
    title: "Duration",
    key: "duration",
    width: 110,
    align: "right",
    render: (request) => h("span", { class: "mono-text copy-xs muted-text" }, request.durationLabel),
  },
]);

const tracingEnabled = computed(() => traceSettings.value?.enabled === true);
const traceControlsDisabledReason = computed(() => {
  if (selectedEnvironmentBlocked.value) return selectedEnvironmentBlocked.value;
  if (traceSettingsState.value === "loading") return "Loading authoritative trace settings from the server.";
  if (traceSettingsState.value === "error") return "Reload trace settings before changing server tracing.";
  if (isTraceBusy.value) return TRACE_BUSY_REASON;
  return "";
});
const pauseDisabledReason = computed(() => {
  if (traceControlsDisabledReason.value) return traceControlsDisabledReason.value;
  return tracingEnabled.value ? "" : "Enable tracing before pausing live updates.";
});
const clearTracesDisabledReason = computed(() => renderStats.value.retainedRequests ? "" : NO_TRACES_REASON);
const filteredTableRequests = computed(() => {
  const query = traceSearch.value.trim().toLocaleLowerCase();
  return tableRequests.value.filter((view) => {
    const request = traceStore.get(view.requestId);
    if (traceFilter.value === "problems" && !isProblemTrace(request)) return false;
    if (traceFilter.value === "in-flight" && (!request || isTerminalStage(request.stage))) return false;
    if (traceFilter.value === "completed" && (!request || !isTerminalStage(request.stage))) return false;
    if (!query) return true;
    return [view.methodLabel, view.pathLabel, view.requestIdLabel, view.flowLabel, view.stageLabel, view.statusLabel]
      .some((value) => value.toLocaleLowerCase().includes(query));
  });
});
const traceTableSummary = computed(() => {
  const stats = renderStats.value;
  if (stats.sampledEvents || stats.sampledRequests) {
    return `Sampled under load: ${numberLabel(stats.sampledEvents)} events / ${numberLabel(stats.sampledRequests)} requests omitted from rendering.`;
  }
  return `Latest ${numberLabel(stats.renderedTableRows)} rendered from ${numberLabel(stats.retainedRequests)} retained requests.`;
});
const streamStateTagType = computed(() => {
  if (traceSettingsState.value === "loading") return "info";
  if (traceSettingsState.value === "error") return "error";
  if (isPaused.value && tracingEnabled.value) return "warning";
  if (!tracingEnabled.value) return "default";
  if (streamState.value === "live") return "success";
  if (streamState.value === "error") return "error";
  if (streamState.value === "connecting" || streamState.value === "retrying") return "warning";
  return "info";
});

const traceStateTone = computed<"success" | "warning" | "error" | "info" | "muted">(() => {
  const type = streamStateTagType.value;
  return type === "default" ? "muted" : type;
});

async function loadTraceSettings(loadVersion: number) {
  if (selectedEnvironmentBlocked.value) {
    traceSettingsState.value = "blocked";
    return;
  }
  traceSettingsState.value = "loading";
  traceSettings.value = null;
  selectedTraceLevel.value = TrafficTraceLevel.UNSPECIFIED;
  try {
    const resp = await managementClient.getTrafficTraceSettings({});
    if (loadVersion !== traceSettingsLoadVersion) return;
    if (!resp.settings) throw new Error("The server returned no trace settings.");
    applyTraceSettings(resp.settings);
    traceSettingsState.value = "ready";
    if (resp.settings.enabled && !isPaused.value) {
      startTraceStream();
    }
  } catch (err) {
    if (loadVersion !== traceSettingsLoadVersion) return;
    traceSettings.value = null;
    traceSettingsState.value = "error";
    streamError.value = messageFromError(err);
    streamState.value = "error";
  }
}

function retryTraceSettings() {
  traceSettingsLoadVersion += 1;
  streamError.value = "";
  void loadTraceSettings(traceSettingsLoadVersion);
}

async function setTracingEnabled(enabled: boolean) {
  await updateTraceSettings(enabled, selectedTraceLevel.value);
}

async function setTraceLevel(level: TrafficTraceLevel) {
  await updateTraceSettings(tracingEnabled.value, level);
}

async function updateTraceSettings(enabled: boolean, level: TrafficTraceLevel) {
  if (traceControlsDisabledReason.value) return;
  isTraceBusy.value = true;
  streamError.value = "";
  try {
    const resp = await managementClient.setTrafficTraceSettings({ enabled, level });
    if (!resp.settings) throw new Error("The server did not confirm the trace settings change.");
    applyTraceSettings(resp.settings);
    traceSettingsState.value = "ready";
    if (resp.settings?.enabled) {
      startTraceStream();
    } else {
      stopTraceStream("idle");
    }
  } catch (err) {
    streamError.value = messageFromError(err);
  } finally {
    isTraceBusy.value = false;
  }
}

function applyTraceSettings(settings: TrafficTraceSettings | null) {
  if (!settings) return;
  traceSettings.value = settings;
  selectedTraceLevel.value = settings.level;
  if (!settings.enabled) {
    stopTraceStream("idle");
  }
}

function startTraceStream() {
  if (streamController || isPaused.value || traceSettingsState.value !== "ready" || !traceSettings.value?.enabled || selectedEnvironmentBlocked.value) return;
  clearRetryTimer();
  streamController = new AbortController();
  streamState.value = "connecting";
  streamError.value = "";
  void consumeTraceStream(streamController);
}

async function consumeTraceStream(controller: AbortController) {
  try {
    const streamRequest = traceStreamRequestForSequence(traceStore.lastSequence);
    const stream = managementClient.streamTrafficTraceEvents(
      streamRequest,
      { signal: controller.signal },
    );
    streamState.value = "live";
    retryDelayMs = 1000;
    for await (const message of stream) {
      if (message.settings) {
        applyTraceSettings(message.settings);
        if (!message.settings.enabled) return;
      }
      if (message.event) {
        mergeTraceEvent(message.event);
      }
    }
    if (!controller.signal.aborted && traceSettings.value?.enabled) {
      scheduleTraceReconnect("Trace stream closed");
    }
  } catch (err) {
    if (controller.signal.aborted) return;
    scheduleTraceReconnect(messageFromError(err));
  } finally {
    if (streamController === controller) {
      streamController = null;
    }
  }
}

function scheduleTraceReconnect(message: string) {
  streamError.value = message;
  if (!traceSettings.value?.enabled || isPaused.value) {
    streamState.value = "idle";
    return;
  }
  streamState.value = "retrying";
  clearRetryTimer();
  const delay = retryDelayMs;
  retryDelayMs = Math.min(retryDelayMs * 2, 8000);
  retryTimer = window.setTimeout(() => {
    retryTimer = null;
    startTraceStream();
  }, delay);
}

function stopTraceStream(nextState: "idle" | "error" = "idle") {
  clearRetryTimer();
  if (streamController) {
    streamController.abort();
    streamController = null;
  }
  streamState.value = nextState;
}

function clearRetryTimer() {
  if (retryTimer !== null) {
    window.clearTimeout(retryTimer);
    retryTimer = null;
  }
}

function mergeTraceEvent(event: TrafficTraceEvent) {
  traceStore.enqueue(event);
}

function applyTraceStoreSnapshot(snapshot: ReturnType<TrafficTraceStore["snapshot"]>) {
  tableRequests.value = snapshot.tableRows;
  diagramRequests.value = snapshot.diagramRequests;
  renderStats.value = snapshot.stats;
  traceSnapshotVersion.value += 1;
  if (selectedRequestId.value && !traceStore.get(selectedRequestId.value)) {
    selectedRequestId.value = null;
  }
}

function clearTraceRequests() {
  traceStore.clear();
  selectedRequestId.value = null;
}

function togglePause() {
  if (pauseDisabledReason.value) return;
  isPaused.value = !isPaused.value;
  if (isPaused.value) {
    stopTraceStream("idle");
    return;
  }
  startTraceStream();
}

function isProblemTrace(request: TraceRequest | null): boolean {
  if (!request) return false;
  if (request.stage === TrafficTraceStage.FAILED || request.stage === TrafficTraceStage.WAF_BLOCKED || request.stage === TrafficTraceStage.RATE_LIMITED || request.stage === TrafficTraceStage.ACCESS_DENIED) return true;
  return request.statusCode >= 400n;
}

function openTraceDetails(request: TraceRequest | TraceRequestView | string) {
  selectedRequestId.value = typeof request === "string" ? request : request.requestId;
  isDetailsOpen.value = true;
}

function traceRowKey(request: TraceRequestView): string {
  return request.requestId;
}

function traceRowProps(request: TraceRequestView) {
  return {
    class: "interactive-cursor",
    role: "button",
    tabindex: 0,
    "aria-label": `Open trace details for ${diagnosticInspectionText(request.requestIdLabel)}`,
    onClick: () => openTraceDetails(request),
    onKeydown: (event: KeyboardEvent) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      openTraceDetails(request);
    },
  };
}

function handleFlowEditRequest(request: TrafficFlowEditRequest) {
  if (request.targets.length === 1) {
    openEditTarget(request.targets[0]);
    return;
  }
  pendingEditRequest.value = request;
  isEditChooserOpen.value = true;
}

function openEditTarget(target: TrafficFlowEditTarget) {
  isEditChooserOpen.value = false;
  pendingEditRequest.value = null;
  editorHost.value?.openTarget(target);
}

function handlePageHide() {
  stopTraceStream("idle");
}

function bigIntLabel(value: bigint | undefined): string {
  if (value === undefined) return "0";
  return new Intl.NumberFormat().format(value);
}

function numberLabel(value: number): string {
  return new Intl.NumberFormat().format(value);
}

function streamStateLabel(): string {
  if (selectedEnvironmentBlocked.value) return "Unavailable";
  if (traceSettingsState.value === "loading") return "Loading settings";
  if (traceSettingsState.value === "error") return "Settings unavailable";
  if (isPaused.value && tracingEnabled.value) return "Paused";
  if (!tracingEnabled.value) return "Disabled";
  if (streamState.value === "live") return "Live";
  if (streamState.value === "connecting") return "Connecting";
  if (streamState.value === "retrying") return "Reconnecting";
  if (streamState.value === "error") return "Error";
  return "Idle";
}


onMounted(() => {
  window.addEventListener("pagehide", handlePageHide);
  traceSettingsLoadVersion += 1;
  if (!selectedEnvironmentBlocked.value) {
    void loadTraceSettings(traceSettingsLoadVersion);
  }
});

watch([selectedEnvironmentId, selectedEnvironmentBlocked], () => {
  traceSettingsLoadVersion += 1;
  stopTraceStream("idle");
  traceStore.clear();
  selectedRequestId.value = null;
  traceSettings.value = null;
  selectedTraceLevel.value = TrafficTraceLevel.UNSPECIFIED;
  traceSettingsState.value = selectedEnvironmentBlocked.value ? "blocked" : "loading";
  isPaused.value = false;
  streamError.value = "";
  if (selectedEnvironmentBlocked.value) return;
  void loadTraceSettings(traceSettingsLoadVersion);
});

onBeforeUnmount(() => {
  window.removeEventListener("pagehide", handlePageHide);
  stopTraceStream();
  traceStore.clear();
});
</script>

<template>
  <div v-if="dashboard" class="traffic-page" :aria-busy="traceSettingsState === 'loading'">
    <PageHeader title="Traffic Flow" description="Live request routing across listeners, routes, targets, agents, and upstreams.">
      <template #badge>
        <NTag size="small" :bordered="false" :type="streamStateTagType">{{ streamStateLabel() }}</NTag>
      </template>
      <template #actions>
        <div class="traffic-controls">
          <DisabledHint :disabled="Boolean(traceControlsDisabledReason)" :reason="traceControlsDisabledReason">
            <NCheckbox
              class="traffic-tracing-toggle"
              :checked="tracingEnabled"
              :indeterminate="traceSettingsState === 'loading'"
              :disabled="Boolean(traceControlsDisabledReason)"
              @update:checked="setTracingEnabled"
            >
              Tracing
            </NCheckbox>
          </DisabledHint>

          <DisabledHint :disabled="Boolean(traceControlsDisabledReason)" :reason="traceControlsDisabledReason">
            <NRadioGroup
              class="traffic-level-group"
              :value="selectedTraceLevel"
              :disabled="Boolean(traceControlsDisabledReason)"
              size="small"
              @update:value="(value) => setTraceLevel(value as TrafficTraceLevel)"
            >
              <NRadioButton v-for="option in traceLevelOptions" :key="option.value" :value="option.value">
                {{ option.label }}
              </NRadioButton>
            </NRadioGroup>
          </DisabledHint>

          <DisabledHint :disabled="Boolean(pauseDisabledReason)" :reason="pauseDisabledReason">
            <NButton
              secondary
              size="small"
              attr-type="button"
              :disabled="Boolean(pauseDisabledReason)"
              :aria-pressed="isPaused"
              @click="togglePause"
            >
              {{ isPaused ? "Resume live updates" : "Pause live updates" }}
            </NButton>
          </DisabledHint>
        </div>
      </template>
    </PageHeader>

    <div class="traffic-stats">
      <MetricStrip variant="tiles" label="Trace status">
        <MetricCard label="Trace state" :value="streamStateLabel()" :tone="traceStateTone" dot text-value />
        <MetricCard label="Events" :value="traceSettings ? bigIntLabel(traceSettings.emittedEvents) : '—'">
          <template v-if="traceSettingsState === 'loading'" #value>
            <NSkeleton text width="4.5rem" />
          </template>
        </MetricCard>
        <MetricCard
          label="Dropped"
          :value="traceSettings ? bigIntLabel(traceSettings.droppedEvents) : '—'"
          :tone="traceSettings?.droppedEvents ? 'warning' : 'default'"
        >
          <template v-if="traceSettingsState === 'loading'" #value>
            <NSkeleton text width="4.5rem" />
          </template>
        </MetricCard>
        <MetricCard label="Rendered" :value="`${numberLabel(renderStats.renderedTableRows)}/${numberLabel(renderStats.retainedRequests)}`" detail="table rows / retained" />
      </MetricStrip>

      <MetricStrip v-if="showDebugStats" variant="tiles" label="Debug stats">
        <MetricCard label="Subscribers" :value="traceSettings?.subscriberCount?.toString() ?? '0'" />
        <MetricCard
          label="Sampled"
          :value="`${numberLabel(renderStats.sampledEvents)}/${numberLabel(renderStats.sampledRequests)}`"
          :tone="renderStats.sampledEvents || renderStats.sampledRequests ? 'warning' : 'default'"
          detail="events / requests"
        />
        <MetricCard label="Live tokens" :value="String(renderedTokenCount)" />
      </MetricStrip>

      <NButton quaternary size="small" attr-type="button" class="traffic-debug-toggle" @click="showDebugStats = !showDebugStats">
        {{ showDebugStats ? 'Hide' : 'Show' }} debug stats
      </NButton>
    </div>

    <NAlert v-if="streamError" :type="traceSettingsState === 'error' ? 'error' : 'warning'" :show-icon="false" :bordered="false">
      <div class="traffic-alert-content">
        <bdi class="traffic-alert-message" dir="ltr">{{ diagnosticInspectionText(streamError) }}</bdi>
        <NButton v-if="traceSettingsState === 'error'" secondary size="small" attr-type="button" @click="retryTraceSettings">
          Retry settings
        </NButton>
      </div>
    </NAlert>

    <TrafficFlowDiagram
      :config="config"
      :requests="diagramRequests"
      :tracing-enabled="tracingEnabled && !isPaused"
      @select="openTraceDetails"
      @active-change="renderedTokenCount = $event"
      @edit-node="handleFlowEditRequest"
    />

    <SectionCard class="traffic-traces" title="Recent traces" :description="traceTableSummary" flush>
      <template #actions>
        <div class="traffic-table-tools">
          <NInput
            v-model:value="traceSearch"
            clearable
            size="small"
            :input-props="{ 'aria-label': 'Search recent traces' }"
            placeholder="Search request, flow, or status"
          />
          <AccessibleSelect
            v-model:value="traceFilter"
            accessible-label="Filter recent traces"
            class="traffic-filter-select"
            size="small"
            :options="traceFilterOptions"
          />
          <DisabledHint :disabled="Boolean(clearTracesDisabledReason)" :reason="clearTracesDisabledReason">
            <NButton
              secondary
              size="small"
              attr-type="button"
              :disabled="Boolean(clearTracesDisabledReason)"
              @click="clearTraceRequests"
            >
              Clear
            </NButton>
          </DisabledHint>
        </div>
      </template>

      <NDataTable
        v-if="filteredTableRequests.length"
        :columns="traceColumns"
        :data="filteredTableRequests"
        :row-key="traceRowKey"
        :row-props="traceRowProps"
        :pagination="false"
        :bordered="false"
        :single-line="false"
        :scroll-x="730"
        size="small"
      />
      <div v-else class="traffic-empty-state">
        <NEmpty
          size="small"
          :description="tableRequests.length ? 'No retained traces match this search and filter.' : tracingEnabled ? (isPaused ? 'Live updates are paused. Resume to receive new traces.' : 'Waiting for the next traced request.') : traceSettingsState === 'ready' ? 'Tracing is disabled. Enable it to inspect live requests.' : 'Trace data will appear after server settings load.'"
        />
      </div>
    </SectionCard>

    <TrafficTraceDetailsModal
      v-model="isDetailsOpen"
      :request="selectedRequest"
      :level="selectedTraceLevel"
    />
    <PublicProxyEditorHost ref="editorHost" :config="config" />
    <TrafficFlowEditTargetChooser
      v-model="isEditChooserOpen"
      :request="pendingEditRequest"
      @select="openEditTarget"
    />
  </div>
</template>

<style scoped>
.traffic-page {
  display: grid;
  min-width: 0;
  gap: 1.25rem;
}

.traffic-controls {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 0.5rem 0.75rem;
}

.traffic-tracing-toggle {
  display: inline-flex;
  min-height: 2.125rem;
  align-items: center;
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 0 0.75rem;
}

/* Radio semantics stay (specs use role=radio); the look matches SegmentedControl. */
.traffic-level-group {
  display: inline-flex;
  max-width: 100%;
  flex-wrap: wrap;
  gap: 0.125rem;
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 0.1875rem;
}

.traffic-level-group :deep(.n-radio-group__splitor) {
  display: none;
}

.traffic-level-group :deep(.n-radio-button) {
  height: 1.75rem;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  font-weight: 500;
  line-height: 1.75rem;
  padding: 0 0.625rem;
  transition: background-color 140ms ease-out, color 140ms ease-out;
}

.traffic-level-group :deep(.n-radio-button .n-radio-button__state-border) {
  display: none;
}

.traffic-level-group :deep(.n-radio-button:hover:not(.n-radio-button--disabled)) {
  color: var(--app-text);
}

.traffic-level-group :deep(.n-radio-button.n-radio-button--checked) {
  background: var(--app-panel);
  box-shadow: 0 0 0 1px var(--app-border-subtle), 0 1px 2px rgba(0, 0, 0, 0.12);
  color: var(--app-text);
}

.traffic-level-group :deep(.n-radio-button--disabled) {
  cursor: not-allowed;
  opacity: 0.5;
}

.traffic-stats {
  display: grid;
  min-width: 0;
  gap: 0.5rem;
}

.traffic-debug-toggle {
  justify-self: start;
}

.traffic-alert-content {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
}

.traffic-alert-message {
  min-width: 0;
  overflow-wrap: anywhere;
  direction: ltr;
  unicode-bidi: isolate;
}

.traffic-table-tools {
  display: grid;
  min-width: 0;
  grid-template-columns: minmax(12rem, 18rem) 9.5rem auto;
  align-items: center;
  gap: 0.5rem;
}

.traffic-filter-select {
  width: 100%;
}

.traffic-empty-state {
  padding: 2rem 1rem;
}

/* Cells rendered by NDataTable via h() do not carry this component's scope id; reach them with :deep(). */
.traffic-traces :deep(.trace-hostile-value) {
  direction: ltr;
  unicode-bidi: isolate;
}

.traffic-traces :deep(.trace-flow-label) {
  display: block;
  overflow-wrap: anywhere;
}

@media (max-width: 1023px) {
  .traffic-controls {
    justify-content: flex-start;
  }
}

@media (max-width: 639px) {
  .traffic-traces :deep(.section-card__actions) {
    align-self: stretch;
  }

  .traffic-table-tools {
    width: 100%;
    grid-template-columns: minmax(0, 1fr) auto;
  }

  .traffic-table-tools > :first-child {
    grid-column: 1 / -1;
  }
}

@media (pointer: coarse) {
  .traffic-controls :deep(.n-button),
  .traffic-table-tools :deep(.n-button),
  .traffic-table-tools :deep(.n-input),
  .traffic-table-tools :deep(.n-base-selection) {
    min-height: 44px;
  }

  .traffic-level-group :deep(.n-radio-button) {
    height: 2.5rem;
    line-height: 2.5rem;
  }
}
</style>
