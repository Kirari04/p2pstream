<script setup lang="ts">
import { computed, h, inject, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import type { ComputedRef } from "vue";
import { NButton, NButtonGroup, NCheckbox, NDataTable } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import TrafficFlowEditTargetChooser from "@/components/editors/TrafficFlowEditTargetChooser.vue";
import TrafficFlowDiagram from "@/components/TrafficFlowDiagram.vue";
import TrafficTraceDetailsModal from "@/components/TrafficTraceDetailsModal.vue";
import { NO_TRACES_REASON, TRACE_BUSY_REASON } from "@/lib/disabledReasons";
import { TrafficTraceStore, formatDuration, traceStreamRequestForSequence } from "@/lib/trafficTraceStore";
import type { TrafficFlowEditRequest, TrafficFlowEditTarget } from "@/types/trafficFlowEdit";
import type { TraceRenderStats, TraceRequest, TraceRequestView } from "@/types/trafficTrace";
import { emptyTraceRenderStats } from "@/types/trafficTrace";
import type {
  DashboardWindowSummary,
  GetDashboardResponse,
  GetPublicProxyConfigResponse,
  TrafficTraceEvent,
  TrafficTraceSettings,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { TrafficTraceLevel } from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

const dashboard = inject<ComputedRef<GetDashboardResponse | null>>("dashboard");
const publicProxyConfig = inject<ComputedRef<GetPublicProxyConfigResponse | null>>("publicProxyConfig");
const selectedEnvironmentId = inject<ComputedRef<string>>("selectedEnvironmentId", computed(() => "0"));

const trafficWindows = computed(() => dashboard?.value?.windows ?? []);
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
      h("div", { class: "flex items-center gap-2" }, [
        h("span", { class: "rounded border border-[var(--app-border)] px-1.5 py-0.5 font-mono text-[0.7rem] text-[var(--app-text)]" }, request.methodLabel),
        h("span", { class: "max-w-[18rem] truncate font-mono text-xs text-[var(--app-text)]" }, request.pathLabel),
      ]),
      h("p", { class: "mt-1 max-w-[22rem] truncate font-mono text-[0.7rem] text-[var(--app-text-muted)]" }, request.requestIdLabel),
    ]),
  },
  {
    title: "Flow",
    key: "flow",
    minWidth: 260,
    render: (request) => h("div", [
      h("p", { class: "text-xs text-[var(--app-text)]" }, request.flowLabel),
      h("p", { class: "mt-1 text-[0.7rem] text-[var(--app-text-muted)]" }, `${request.stageLabel}${request.sampledEventCount ? ` / sampled ${numberLabel(request.sampledEventCount)}` : ""}`),
    ]),
  },
  {
    title: "Status",
    key: "status",
    width: 110,
    align: "right",
    render: (request) => h("span", { class: ["font-mono text-xs", request.statusClass] }, request.statusLabel),
  },
  {
    title: "Duration",
    key: "duration",
    width: 120,
    align: "right",
    render: (request) => h("span", { class: "font-mono text-xs text-[var(--app-text-muted)]" }, request.durationLabel),
  },
]);
const trafficWindowColumns = computed<DataTableColumns<DashboardWindowSummary>>(() => [
  { title: "Window", key: "window", minWidth: 140, render: (item) => item.label },
  { title: "Requests", key: "requests", width: 130, align: "right", render: (item) => bigIntLabel(item.proxyRequests) },
  { title: "Success", key: "success", width: 130, align: "right", render: (item) => h("span", { class: "text-green-500" }, bigIntLabel(item.proxySuccess)) },
  { title: "Errors", key: "errors", width: 130, align: "right", render: (item) => h("span", { class: "text-red-500" }, bigIntLabel(proxyErrors(item))) },
  { title: "Avg Duration", key: "duration", width: 140, align: "right", render: (item) => h("span", { class: "text-[var(--app-text-muted)]" }, formatDuration(item.proxyAvgDurationMs)) },
  { title: "Traffic In", key: "in", width: 130, align: "right", render: (item) => formatBytes(item.agentBytesReceived) },
  { title: "Traffic Out", key: "out", width: 130, align: "right", render: (item) => formatBytes(item.agentBytesSent) },
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

async function loadTraceSettings(loadVersion: number) {
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
  if (isTraceBusy.value) return;
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
  if (streamController || !traceSettings.value?.enabled) return;
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
    class: "cursor-pointer",
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

function trafficWindowRowKey(item: DashboardWindowSummary): string {
  return item.label;
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

function proxyErrors(window: DashboardWindowSummary): bigint {
  return window.proxyClientError + window.proxyServerError + window.proxyInternalError;
}

function bigIntLabel(value: bigint | undefined): string {
  if (value === undefined) return "0";
  return new Intl.NumberFormat().format(Number(value));
}

function numberLabel(value: number): string {
  return new Intl.NumberFormat().format(value);
}

function formatBytes(value: bigint | undefined): string {
  if (value === undefined) return "0 B";
  const bytes = Number(value);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function streamStateLabel(): string {
  if (!tracingEnabled.value) return "Disabled";
  if (streamState.value === "live") return "Live";
  if (streamState.value === "connecting") return "Connecting";
  if (streamState.value === "retrying") return "Reconnecting";
  if (streamState.value === "error") return "Error";
  return "Idle";
}

function messageFromError(err: unknown): string {
  return err instanceof Error ? err.message : "Request failed";
}

onMounted(() => {
  window.addEventListener("pagehide", handlePageHide);
  traceSettingsLoadVersion += 1;
  void loadTraceSettings(traceSettingsLoadVersion);
});

watch(selectedEnvironmentId, () => {
  traceSettingsLoadVersion += 1;
  stopTraceStream("idle");
  traceStore.clear();
  selectedRequestId.value = null;
  traceSettings.value = null;
  streamError.value = "";
  void loadTraceSettings(traceSettingsLoadVersion);
});

onBeforeUnmount(() => {
  window.removeEventListener("pagehide", handlePageHide);
  stopTraceStream();
  traceStore.clear();
});
</script>

<template>
  <div v-if="dashboard" class="space-y-8">
    <section class="space-y-4">
      <div class="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
        <div>
          <h3 class="text-xl font-bold mb-2">Traffic Flow</h3>
          <p class="text-[var(--app-text-muted)] text-sm">Live request routing across listeners, routes, targets, agents, and upstreams.</p>
        </div>

        <div class="flex flex-col gap-3 sm:flex-row sm:items-center">
          <DisabledHint :disabled="Boolean(traceBusyDisabledReason)" :reason="traceBusyDisabledReason">
            <NCheckbox
              class="flex h-10 items-center rounded-md border border-[var(--app-border)] bg-[var(--app-panel)] px-3"
              :checked="tracingEnabled"
              :disabled="Boolean(traceBusyDisabledReason)"
              @update:checked="setTracingEnabled"
            >
              Tracing
            </NCheckbox>
          </DisabledHint>

          <NButtonGroup class="grid grid-cols-4 overflow-hidden rounded-md border border-[var(--app-border)]" size="small">
            <DisabledHint
              v-for="option in traceLevelOptions"
              :key="option.value"
              full-width
              :disabled="Boolean(traceBusyDisabledReason)"
              :reason="traceBusyDisabledReason"
            >
              <NButton
                attr-type="button"
                class="h-10 w-full"
                :type="selectedTraceLevel === option.value ? 'primary' : 'default'"
                :disabled="Boolean(traceBusyDisabledReason)"
                @click="setTraceLevel(option.value)"
              >
                {{ option.label }}
              </NButton>
            </DisabledHint>
          </NButtonGroup>
        </div>
      </div>

      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <div class="app-card p-4">
          <p class="app-card-title">Trace State</p>
          <span class="text-lg font-semibold" :class="tracingEnabled ? 'text-green-400' : 'text-[var(--app-text-muted)]'">{{ streamStateLabel() }}</span>
        </div>
        <div class="app-card p-4">
          <p class="app-card-title">Events</p>
          <span class="text-lg font-semibold">{{ bigIntLabel(traceSettings?.emittedEvents) }}</span>
        </div>
        <div class="app-card p-4">
          <p class="app-card-title">Dropped</p>
          <span class="text-lg font-semibold" :class="traceSettings?.droppedEvents ? 'text-amber-400' : 'text-[var(--app-text)]'">
            {{ bigIntLabel(traceSettings?.droppedEvents) }}
          </span>
        </div>
        <div class="app-card p-4">
          <p class="app-card-title">Rendered</p>
          <span class="text-lg font-semibold">{{ numberLabel(renderStats.renderedTableRows) }}/{{ numberLabel(renderStats.retainedRequests) }}</span>
        </div>
      </div>
      <div class="grid gap-3">
        <NButton quaternary size="small" attr-type="button" class="w-fit" @click="showDebugStats = !showDebugStats">
          {{ showDebugStats ? 'Hide' : 'Show' }} debug stats
        </NButton>
        <div v-if="showDebugStats" class="grid gap-3 sm:grid-cols-3">
          <div class="app-card p-4">
            <p class="app-card-title">Subscribers</p>
            <span class="text-lg font-semibold">{{ traceSettings?.subscriberCount?.toString() ?? "0" }}</span>
          </div>
          <div class="app-card p-4">
            <p class="app-card-title">Sampled</p>
            <span class="text-lg font-semibold" :class="renderStats.sampledEvents || renderStats.sampledRequests ? 'text-amber-400' : 'text-[var(--app-text)]'">
              {{ numberLabel(renderStats.sampledEvents) }}/{{ numberLabel(renderStats.sampledRequests) }}
            </span>
          </div>
          <div class="app-card p-4">
            <p class="app-card-title">Live Tokens</p>
            <span class="text-lg font-semibold">{{ renderedTokenCount }}</span>
          </div>
        </div>
      </div>

      <div v-if="streamError" class="rounded-md border border-amber-900/60 bg-amber-950/20 px-4 py-3 text-sm text-amber-300">
        {{ streamError }}
      </div>

      <TrafficFlowDiagram
        :config="config"
        :requests="diagramRequests"
        :tracing-enabled="tracingEnabled"
        @select="openTraceDetails"
        @active-change="renderedTokenCount = $event"
        @edit-node="handleFlowEditRequest"
      />

      <div class="app-card overflow-hidden">
        <div class="flex items-center justify-between border-b border-[var(--app-border)] px-5 py-4">
          <div>
            <h4 class="font-semibold">Recent traces</h4>
            <p class="text-xs text-[var(--app-text-muted)]">{{ traceTableSummary }}</p>
          </div>
          <DisabledHint :disabled="Boolean(clearTracesDisabledReason)" :reason="clearTracesDisabledReason">
            <NButton
              secondary
              size="small"
              class="border-[var(--app-border)]! bg-transparent! text-[var(--app-text-muted)]! hover:border-[var(--app-text-muted)]!"
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

    <section class="space-y-4">
      <div>
        <h3 class="text-xl font-bold mb-2">Traffic History</h3>
        <p class="text-[var(--app-text-muted)] text-sm">Aggregated request counts across different time windows.</p>
      </div>

      <div class="app-card overflow-hidden">
        <NDataTable
          :columns="trafficWindowColumns"
          :data="trafficWindows"
          :row-key="trafficWindowRowKey"
          :pagination="false"
          :bordered="false"
          :single-line="false"
          :scroll-x="910"
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
