<script setup lang="ts">
import { computed, h, inject, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import { NAlert, NButton, NCheckbox, NDataTable, NRadioButton, NRadioGroup, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { dashboardKey, publicProxyConfigKey, selectedEnvironmentBlockedKey, selectedEnvironmentIdKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import TrafficFlowEditTargetChooser from "@/components/editors/TrafficFlowEditTargetChooser.vue";
import TrafficFlowDiagram from "@/components/TrafficFlowDiagram.vue";
import TrafficTraceDetailsModal from "@/components/TrafficTraceDetailsModal.vue";
import { NO_TRACES_REASON, TRACE_BUSY_REASON } from "@/lib/disabledReasons";
import { TrafficTraceStore, traceStreamRequestForSequence } from "@/lib/trafficTraceStore";
import type { TrafficFlowEditRequest, TrafficFlowEditTarget } from "@/types/trafficFlowEdit";
import type { TraceRenderStats, TraceRequest, TraceRequestView } from "@/types/trafficTrace";
import { emptyTraceRenderStats } from "@/types/trafficTrace";
import type { TrafficTraceEvent, TrafficTraceSettings } from "@/gen/proto/p2pstream/v1/management_pb";
import { TrafficTraceLevel } from "@/gen/proto/p2pstream/v1/management_pb";
import { messageFromError } from "@/lib/errors";

const managementClient = useManagementClient();

const dashboard = inject(dashboardKey, computed(() => null));
const publicProxyConfig = inject(publicProxyConfigKey, computed(() => null));
const selectedEnvironmentId = inject(selectedEnvironmentIdKey, computed(() => "0"));
const selectedEnvironmentBlocked = inject(selectedEnvironmentBlockedKey, computed(() => ""));

const config = computed(() => publicProxyConfig?.value ?? null);

const traceSettings = ref<TrafficTraceSettings | null>(null);
const selectedTraceLevel = ref<TrafficTraceLevel>(TrafficTraceLevel.BASIC);
const isTraceBusy = ref(false);
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
const traceColumns = computed<DataTableColumns<TraceRequestView>>(() => [
  {
    title: "Request",
    key: "request",
    minWidth: 260,
    render: (request) => h("div", [
      h("div", { class: "layout-row align-center space-sm" }, [
        h("span", { class: "round-sm framed frame-standard pad-x-xs pad-y-2xs mono-text copy-2xs base-text" }, request.methodLabel),
        h("span", { class: "max-token-width clip-text mono-text copy-xs base-text" }, request.pathLabel),
      ]),
      h("p", { class: "margin-top-xs max-trace-width clip-text mono-text copy-2xs muted-text" }, request.requestIdLabel),
    ]),
  },
  {
    title: "Flow",
    key: "flow",
    minWidth: 260,
    render: (request) => h("div", [
      h("p", { class: "copy-xs base-text" }, request.flowLabel),
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
const traceBusyDisabledReason = computed(() => isTraceBusy.value ? TRACE_BUSY_REASON : "");
const clearTracesDisabledReason = computed(() => renderStats.value.retainedRequests ? "" : NO_TRACES_REASON);
const traceTableSummary = computed(() => {
  const stats = renderStats.value;
  if (stats.sampledEvents || stats.sampledRequests) {
    return `Sampled under load: ${numberLabel(stats.sampledEvents)} events / ${numberLabel(stats.sampledRequests)} requests omitted from rendering.`;
  }
  return `Latest ${numberLabel(stats.renderedTableRows)} rendered from ${numberLabel(stats.retainedRequests)} retained requests.`;
});
const streamStateTagType = computed(() => {
  if (!tracingEnabled.value) return "default";
  if (streamState.value === "live") return "success";
  if (streamState.value === "error") return "error";
  if (streamState.value === "connecting" || streamState.value === "retrying") return "warning";
  return "info";
});

async function loadTraceSettings(loadVersion: number) {
  if (selectedEnvironmentBlocked.value) return;
  try {
    const resp = await managementClient.getTrafficTraceSettings({});
    if (loadVersion !== traceSettingsLoadVersion) return;
    applyTraceSettings(resp.settings ?? null);
    if (resp.settings?.enabled) {
      startTraceStream();
    }
  } catch (err) {
    if (loadVersion !== traceSettingsLoadVersion) return;
    streamError.value = messageFromError(err);
    streamState.value = "error";
  }
}

async function setTracingEnabled(enabled: boolean) {
  await updateTraceSettings(enabled, selectedTraceLevel.value);
}

async function setTraceLevel(level: TrafficTraceLevel) {
  selectedTraceLevel.value = level;
  await updateTraceSettings(tracingEnabled.value, level);
}

async function updateTraceSettings(enabled: boolean, level: TrafficTraceLevel) {
  if (isTraceBusy.value || selectedEnvironmentBlocked.value) return;
  isTraceBusy.value = true;
  streamError.value = "";
  try {
    const resp = await managementClient.setTrafficTraceSettings({ enabled, level });
    applyTraceSettings(resp.settings ?? null);
    if (resp.settings?.enabled) {
      startTraceStream();
    } else {
      stopTraceStream("idle");
    }
  } catch (err) {
    streamError.value = messageFromError(err);
    streamState.value = "error";
  } finally {
    isTraceBusy.value = false;
  }
}

function applyTraceSettings(settings: TrafficTraceSettings | null) {
  if (!settings) return;
  traceSettings.value = settings;
  selectedTraceLevel.value = settings.level || TrafficTraceLevel.BASIC;
  if (!settings.enabled) {
    stopTraceStream("idle");
  }
}

function startTraceStream() {
  if (streamController || !traceSettings.value?.enabled || selectedEnvironmentBlocked.value) return;
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
  if (!traceSettings.value?.enabled) {
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
    "aria-label": `Open trace details for ${request.requestIdLabel}`,
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
  return new Intl.NumberFormat().format(Number(value));
}

function numberLabel(value: number): string {
  return new Intl.NumberFormat().format(value);
}

function streamStateLabel(): string {
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
      <div class="surface-card traffic-console">
        <div class="traffic-console__header">
          <div class="traffic-console__intro">
            <div class="traffic-title-row">
              <h3 class="copy-xl weight-bold">Traffic Flow</h3>
              <NTag size="small" :bordered="false" :type="streamStateTagType">{{ streamStateLabel() }}</NTag>
            </div>
            <p class="muted-text copy-sm">Live request routing across listeners, routes, targets, agents, and upstreams.</p>
          </div>

          <div class="traffic-console__controls">
            <DisabledHint :disabled="Boolean(traceBusyDisabledReason)" :reason="traceBusyDisabledReason">
              <NCheckbox
                class="traffic-tracing-toggle"
                :checked="tracingEnabled"
                :disabled="Boolean(traceBusyDisabledReason)"
                @update:checked="setTracingEnabled"
              >
                Tracing
              </NCheckbox>
            </DisabledHint>

            <DisabledHint full-width :disabled="Boolean(traceBusyDisabledReason)" :reason="traceBusyDisabledReason">
              <NRadioGroup
                class="traffic-level-group"
                :value="selectedTraceLevel"
                :disabled="Boolean(traceBusyDisabledReason)"
                button-style="solid"
                size="small"
                @update:value="(value) => setTraceLevel(value as TrafficTraceLevel)"
              >
                <NRadioButton v-for="option in traceLevelOptions" :key="option.value" :value="option.value">
                  {{ option.label }}
                </NRadioButton>
              </NRadioGroup>
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
            <span class="copy-lg weight-semibold">{{ bigIntLabel(traceSettings?.emittedEvents) }}</span>
          </div>
          <div class="traffic-status-item">
            <p class="stat-label">Dropped</p>
            <span class="copy-lg weight-semibold" :class="traceSettings?.droppedEvents ? 'warning-text' : 'base-text'">
              {{ bigIntLabel(traceSettings?.droppedEvents) }}
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

        <NAlert v-if="streamError" type="warning" :show-icon="false">
          {{ streamError }}
        </NAlert>
      </div>

      <TrafficFlowDiagram
        :config="config"
        :requests="diagramRequests"
        :tracing-enabled="tracingEnabled"
        @select="openTraceDetails"
        @active-change="renderedTokenCount = $event"
        @edit-node="handleFlowEditRequest"
      />

      <div class="surface-card hide-overflow">
        <div class="layout-row align-center spread-items divider-bottom frame-standard pad-x-xl pad-y-lg">
          <div>
            <h4 class="weight-semibold">Recent traces</h4>
            <p class="copy-xs muted-text">{{ traceTableSummary }}</p>
          </div>
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

        <NDataTable
          :columns="traceColumns"
          :data="tableRequests"
          :row-key="traceRowKey"
          :row-props="traceRowProps"
          :pagination="false"
          :bordered="false"
          :single-line="false"
          :scroll-x="760"
          size="small"
        />
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

@media (min-width: 760px) {
  .traffic-console {
    padding: 1.25rem;
  }

  .traffic-console__header {
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: end;
  }

  .traffic-console__controls {
    width: min(100%, 32rem);
    grid-template-columns: auto minmax(22rem, 1fr);
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
}
</style>
