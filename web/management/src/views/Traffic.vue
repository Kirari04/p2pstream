<script setup lang="ts">
import { computed, h, inject, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import { NAlert, NButton, NCheckbox, NDataTable, NEmpty, NInput, NRadioButton, NRadioGroup, NSkeleton, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
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
        h("bdi", { class: "max-token-width clip-text mono-text copy-xs base-text trace-hostile-value", dir: "ltr" }, diagnosticExcerpt(request.pathLabel).text),
      ]),
      h("bdi", { class: "margin-top-xs max-trace-width clip-text mono-text copy-2xs muted-text trace-hostile-value", dir: "ltr" }, diagnosticExcerpt(request.requestIdLabel).text),
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
    width: 110,
    align: "right",
    render: (request) => h("span", { class: ["mono-text copy-xs", request.statusClass] }, request.statusLabel),
  },
  {
    title: "Duration",
    key: "duration",
    width: 120,
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
  if (request.stage === TrafficTraceStage.FAILED || request.stage === TrafficTraceStage.WAF_BLOCKED || request.stage === TrafficTraceStage.RATE_LIMITED) return true;
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
  <div v-if="dashboard" class="stack-xl traffic-page">
    <section class="stack-md">
      <div class="surface-card traffic-console" :aria-busy="traceSettingsState === 'loading'">
        <div class="traffic-console__header">
          <div class="traffic-console__intro">
            <div class="traffic-title-row">
              <h3 class="copy-xl weight-bold">Traffic Flow</h3>
              <NTag size="small" :bordered="false" :type="streamStateTagType">{{ streamStateLabel() }}</NTag>
            </div>
            <p class="muted-text copy-sm">Live request routing across listeners, routes, targets, agents, and upstreams.</p>
          </div>

          <div class="traffic-console__controls">
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

            <DisabledHint full-width :disabled="Boolean(traceControlsDisabledReason)" :reason="traceControlsDisabledReason">
              <NRadioGroup
                class="traffic-level-group"
                :value="selectedTraceLevel"
                :disabled="Boolean(traceControlsDisabledReason)"
                button-style="solid"
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
        </div>

        <div class="traffic-status-grid">
          <div class="traffic-status-item">
            <p class="stat-label">Trace State</p>
            <span class="copy-lg weight-semibold" :class="tracingEnabled ? 'success-text' : 'muted-text'">{{ streamStateLabel() }}</span>
          </div>
          <div class="traffic-status-item">
            <p class="stat-label">Events</p>
            <NSkeleton v-if="traceSettingsState === 'loading'" text width="4.5rem" />
            <span v-else class="copy-lg weight-semibold">{{ traceSettings ? bigIntLabel(traceSettings.emittedEvents) : "—" }}</span>
          </div>
          <div class="traffic-status-item">
            <p class="stat-label">Dropped</p>
            <NSkeleton v-if="traceSettingsState === 'loading'" text width="4.5rem" />
            <span v-else class="copy-lg weight-semibold" :class="traceSettings?.droppedEvents ? 'warning-text' : 'base-text'">
              {{ traceSettings ? bigIntLabel(traceSettings.droppedEvents) : "—" }}
            </span>
          </div>
          <div class="traffic-status-item">
            <p class="stat-label">Rendered</p>
            <span class="copy-lg weight-semibold">{{ numberLabel(renderStats.renderedTableRows) }}/{{ numberLabel(renderStats.retainedRequests) }}</span>
          </div>
        </div>

        <div class="traffic-debug-panel">
          <NButton quaternary size="small" attr-type="button" class="traffic-debug-toggle" @click="showDebugStats = !showDebugStats">
            {{ showDebugStats ? 'Hide' : 'Show' }} debug stats
          </NButton>
          <div v-if="showDebugStats" class="traffic-debug-grid">
            <div class="traffic-debug-item">
              <p class="stat-label">Subscribers</p>
              <span class="copy-lg weight-semibold">{{ traceSettings?.subscriberCount?.toString() ?? "0" }}</span>
            </div>
            <div class="traffic-debug-item">
              <p class="stat-label">Sampled</p>
              <span class="copy-lg weight-semibold" :class="renderStats.sampledEvents || renderStats.sampledRequests ? 'warning-text' : 'base-text'">
                {{ numberLabel(renderStats.sampledEvents) }}/{{ numberLabel(renderStats.sampledRequests) }}
              </span>
            </div>
            <div class="traffic-debug-item">
              <p class="stat-label">Live Tokens</p>
              <span class="copy-lg weight-semibold">{{ renderedTokenCount }}</span>
            </div>
          </div>
        </div>

        <NAlert v-if="streamError" :type="traceSettingsState === 'error' ? 'error' : 'warning'" :show-icon="false">
          <div class="traffic-alert-content">
            <bdi class="traffic-alert-message" dir="ltr">{{ diagnosticInspectionText(streamError) }}</bdi>
            <NButton v-if="traceSettingsState === 'error'" secondary size="small" attr-type="button" @click="retryTraceSettings">
              Retry settings
            </NButton>
          </div>
        </NAlert>
      </div>

      <TrafficFlowDiagram
        :config="config"
        :requests="diagramRequests"
        :tracing-enabled="tracingEnabled && !isPaused"
        @select="openTraceDetails"
        @active-change="renderedTokenCount = $event"
        @edit-node="handleFlowEditRequest"
      />

      <div class="surface-card hide-overflow">
        <div class="traffic-table-heading divider-bottom frame-standard pad-x-xl pad-y-lg">
          <div>
            <h4 class="weight-semibold">Recent traces</h4>
            <p class="copy-xs muted-text">{{ traceTableSummary }}</p>
          </div>
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
                class="important-muted-frame important-transparent-bg important-muted-text important-muted-button"
                :disabled="Boolean(clearTracesDisabledReason)"
                @click="clearTraceRequests"
              >
                Clear
              </NButton>
            </DisabledHint>
          </div>
        </div>

        <NDataTable
          v-if="filteredTableRequests.length"
          :columns="traceColumns"
          :data="filteredTableRequests"
          :row-key="traceRowKey"
          :row-props="traceRowProps"
          :pagination="false"
          :bordered="false"
          :single-line="false"
          :scroll-x="760"
          size="small"
        />
        <div v-else class="traffic-empty-state">
          <NEmpty
            size="small"
            :description="tableRequests.length ? 'No retained traces match this search and filter.' : tracingEnabled ? (isPaused ? 'Live updates are paused. Resume to receive new traces.' : 'Waiting for the next traced request.') : traceSettingsState === 'ready' ? 'Tracing is disabled. Enable it to inspect live requests.' : 'Trace data will appear after server settings load.'"
          />
        </div>
      </div>
    </section>

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
.traffic-console {
  display: grid;
  gap: 1rem;
  padding: 1rem;
}

.traffic-console__header {
  display: grid;
  gap: 1rem;
}

.traffic-console__intro {
  min-width: 0;
}

.traffic-title-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 0.5rem;
}

.traffic-console__controls {
  display: grid;
  gap: 0.75rem;
  min-width: 0;
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

.traffic-tracing-toggle {
  display: inline-flex;
  min-height: 2.25rem;
  align-items: center;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel);
  padding: 0 0.8rem;
}

.traffic-level-group {
  min-width: 0;
  width: 100%;
}

.traffic-level-group :deep(.n-radio-button) {
  flex: 1 1 0;
  min-width: 0;
  text-align: center;
}

.traffic-level-group :deep(.n-radio-button__label) {
  width: 100%;
  text-align: center;
}

.traffic-status-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  overflow: hidden;
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
}

.traffic-status-item {
  min-width: 0;
  background: var(--app-panel-muted);
  padding: 0.85rem;
}

.traffic-status-item:nth-child(even) {
  border-left: 1px solid var(--app-border-subtle);
}

.traffic-status-item:nth-child(n + 3) {
  border-top: 1px solid var(--app-border-subtle);
}

.traffic-debug-panel {
  display: grid;
  gap: 0.75rem;
}

.traffic-debug-toggle {
  justify-self: start;
}

.traffic-debug-grid {
  display: grid;
  gap: 0.5rem;
}

.traffic-debug-item {
  min-width: 0;
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
  background: var(--app-panel-muted);
  padding: 0.85rem;
}

.traffic-table-heading {
  display: grid;
  gap: 0.75rem;
}

.traffic-table-tools {
  display: grid;
  min-width: 0;
  gap: 0.5rem;
}

.traffic-filter-select {
  width: 100%;
}

.traffic-empty-state {
  padding: 2rem 1rem;
}

.trace-hostile-value {
  direction: ltr;
  unicode-bidi: isolate;
}

.trace-flow-label {
  display: block;
  overflow-wrap: anywhere;
}

@media (min-width: 760px) {
  .traffic-console {
    padding: 1.25rem;
  }

  .traffic-console__header {
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: end;
  }

  .traffic-console__controls {
    width: min(100%, 44rem);
    grid-template-columns: auto minmax(22rem, 1fr) auto;
    align-items: center;
    justify-content: end;
  }

  .traffic-status-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }

  .traffic-status-item {
    border-left: 1px solid var(--app-border-subtle);
    border-top: 0;
  }

  .traffic-status-item:first-child {
    border-left: 0;
  }

  .traffic-debug-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .traffic-table-heading {
    grid-template-columns: minmax(13rem, 1fr) minmax(25rem, auto);
    align-items: end;
  }

  .traffic-table-tools {
    grid-template-columns: minmax(14rem, 20rem) 10rem auto;
    align-items: center;
  }

  .traffic-filter-select {
    width: 10rem;
  }
}

@media (pointer: coarse) {
  .traffic-console__controls :deep(.n-button),
  .traffic-table-tools :deep(.n-button),
  .traffic-table-tools :deep(.n-input),
  .traffic-table-tools :deep(.n-base-selection) {
    min-height: 44px;
  }
}
</style>
