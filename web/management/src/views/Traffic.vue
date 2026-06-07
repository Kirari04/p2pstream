<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import type { ComputedRef } from "vue";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import TrafficFlowEditTargetChooser from "@/components/editors/TrafficFlowEditTargetChooser.vue";
import TrafficFlowDiagram from "@/components/TrafficFlowDiagram.vue";
import TrafficTraceDetailsModal from "@/components/TrafficTraceDetailsModal.vue";
import { NO_TRACES_REASON, TRACE_BUSY_REASON } from "@/lib/disabledReasons";
import { TrafficTraceStore, formatDuration, traceStreamRequestForSequence } from "@/lib/trafficTraceStore";
import SecondaryButton from "@/volt/SecondaryButton.vue";
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
          <p class="text-[#888] text-sm">Live request routing across listeners, routes, targets, agents, and upstreams.</p>
        </div>

        <div class="flex flex-col gap-3 sm:flex-row sm:items-center">
          <DisabledHint :disabled="Boolean(traceBusyDisabledReason)" :reason="traceBusyDisabledReason">
            <label class="flex h-10 items-center gap-3 rounded-md border border-[#333] bg-black px-3 text-sm text-[#ededed]">
              <input
                type="checkbox"
                               :checked="tracingEnabled"
                :disabled="Boolean(traceBusyDisabledReason)"
                @change="setTracingEnabled(($event.target as HTMLInputElement).checked)"
              />
              <span>Tracing</span>
            </label>
          </DisabledHint>

          <div class="grid grid-cols-4 overflow-hidden rounded-md border border-[#333]">
            <DisabledHint
              v-for="(option, index) in traceLevelOptions"
              :key="option.value"
              full-width
              :disabled="Boolean(traceBusyDisabledReason)"
              :reason="traceBusyDisabledReason"
            >
              <button
                type="button"
                class="h-10 w-full px-3 text-xs font-medium text-[#888] transition hover:bg-[#111] hover:text-white disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-45 disabled:saturate-0"
                :class="[
                  selectedTraceLevel === option.value ? 'bg-white text-black hover:bg-white hover:text-black' : 'bg-black',
                  index === traceLevelOptions.length - 1 ? '' : 'border-r border-[#333]',
                ]"
                :disabled="Boolean(traceBusyDisabledReason)"
                @click="setTraceLevel(option.value)"
              >
                {{ option.label }}
              </button>
            </DisabledHint>
          </div>
        </div>
      </div>

      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <div class="vercel-card p-4">
          <p class="vercel-card-title">Trace State</p>
          <span class="text-lg font-semibold" :class="tracingEnabled ? 'text-green-400' : 'text-[#888]'">{{ streamStateLabel() }}</span>
        </div>
        <div class="vercel-card p-4">
          <p class="vercel-card-title">Events</p>
          <span class="text-lg font-semibold">{{ bigIntLabel(traceSettings?.emittedEvents) }}</span>
        </div>
        <div class="vercel-card p-4">
          <p class="vercel-card-title">Dropped</p>
          <span class="text-lg font-semibold" :class="traceSettings?.droppedEvents ? 'text-amber-400' : 'text-[#ededed]'">
            {{ bigIntLabel(traceSettings?.droppedEvents) }}
          </span>
        </div>
        <div class="vercel-card p-4">
          <p class="vercel-card-title">Rendered</p>
          <span class="text-lg font-semibold">{{ numberLabel(renderStats.renderedTableRows) }}/{{ numberLabel(renderStats.retainedRequests) }}</span>
        </div>
      </div>
      <div class="grid gap-3">
        <button
          type="button"
          class="text-xs font-medium text-[#666] transition hover:text-[#888]"
          @click="showDebugStats = !showDebugStats"
        >
          {{ showDebugStats ? 'Hide' : 'Show' }} debug stats
        </button>
        <div v-if="showDebugStats" class="grid gap-3 sm:grid-cols-3">
          <div class="vercel-card p-4">
            <p class="vercel-card-title">Subscribers</p>
            <span class="text-lg font-semibold">{{ traceSettings?.subscriberCount?.toString() ?? "0" }}</span>
          </div>
          <div class="vercel-card p-4">
            <p class="vercel-card-title">Sampled</p>
            <span class="text-lg font-semibold" :class="renderStats.sampledEvents || renderStats.sampledRequests ? 'text-amber-400' : 'text-[#ededed]'">
              {{ numberLabel(renderStats.sampledEvents) }}/{{ numberLabel(renderStats.sampledRequests) }}
            </span>
          </div>
          <div class="vercel-card p-4">
            <p class="vercel-card-title">Live Tokens</p>
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

      <div class="vercel-card overflow-hidden">
        <div class="flex items-center justify-between border-b border-[#333] px-5 py-4">
          <div>
            <h4 class="font-semibold">Recent traces</h4>
            <p class="text-xs text-[#888]">{{ traceTableSummary }}</p>
          </div>
          <DisabledHint :disabled="Boolean(clearTracesDisabledReason)" :reason="clearTracesDisabledReason">
            <SecondaryButton
              label="Clear"
              size="small"
              class="border-[#333]! bg-transparent! text-[#888]! hover:border-[#666]!"
              :disabled="Boolean(clearTracesDisabledReason)"
              @click="clearTraceRequests"
            />
          </DisabledHint>
        </div>

        <div class="overflow-x-auto">
          <table class="w-full min-w-[760px] text-left text-sm">
            <thead>
              <tr class="border-b border-[#333] bg-[#0a0a0a]">
                <th class="px-5 py-3 font-medium text-[#888]">Request</th>
                <th class="px-5 py-3 font-medium text-[#888]">Flow</th>
                <th class="px-5 py-3 font-medium text-[#888] text-right">Status</th>
                <th class="px-5 py-3 font-medium text-[#888] text-right">Duration</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[#222]">
              <tr
                v-for="request in tableRequests"
                :key="request.requestId"
                v-memo="[request.version]"
                class="cursor-pointer transition hover:bg-[#0a0a0a]"
                @click="openTraceDetails(request)"
              >
                <td class="px-5 py-3">
                  <div class="flex items-center gap-2">
                    <span class="rounded border border-[#333] px-1.5 py-0.5 font-mono text-[0.7rem] text-[#d4d4d8]">{{ request.methodLabel }}</span>
                    <span class="max-w-[18rem] truncate font-mono text-xs text-white">{{ request.pathLabel }}</span>
                  </div>
                  <p class="mt-1 max-w-[22rem] truncate font-mono text-[0.7rem] text-[#666]">{{ request.requestIdLabel }}</p>
                </td>
                <td class="px-5 py-3">
                  <p class="text-xs text-[#d4d4d8]">
                    {{ request.flowLabel }}
                  </p>
                  <p class="mt-1 text-[0.7rem] text-[#888]">
                    {{ request.stageLabel }}<span v-if="request.sampledEventCount"> / sampled {{ numberLabel(request.sampledEventCount) }}</span>
                  </p>
                </td>
                <td class="px-5 py-3 text-right font-mono text-xs" :class="request.statusClass">
                  {{ request.statusLabel }}
                </td>
                <td class="px-5 py-3 text-right font-mono text-xs text-[#888]">{{ request.durationLabel }}</td>
              </tr>
              <tr v-if="!tableRequests.length">
                <td colspan="4" class="px-5 py-8 text-center text-sm text-[#666]">No traces captured. Enable tracing above to see live request flow.</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>

    <section class="space-y-4">
      <div>
        <h3 class="text-xl font-bold mb-2">Traffic History</h3>
        <p class="text-[#888] text-sm">Aggregated request counts across different time windows.</p>
      </div>

      <div class="vercel-card overflow-hidden">
        <div class="overflow-x-auto">
          <table class="w-full text-left text-sm min-w-[800px]">
            <thead>
              <tr class="border-b border-[#333] bg-[#0a0a0a]">
                <th class="px-6 py-4 font-medium text-[#888]">Window</th>
                <th class="px-6 py-4 font-medium text-[#888] text-right">Requests</th>
                <th class="px-6 py-4 font-medium text-[#888] text-right">Success</th>
                <th class="px-6 py-4 font-medium text-[#888] text-right">Errors</th>
                <th class="px-6 py-4 font-medium text-[#888] text-right">Avg Duration</th>
                <th class="px-6 py-4 font-medium text-[#888] text-right">Traffic In</th>
                <th class="px-6 py-4 font-medium text-[#888] text-right">Traffic Out</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[#333]">
              <tr v-for="item in trafficWindows" :key="item.label" class="hover:bg-[#0a0a0a] transition-colors">
                <td class="px-6 py-4 font-medium">{{ item.label }}</td>
                <td class="px-6 py-4 text-right tabular-nums">{{ bigIntLabel(item.proxyRequests) }}</td>
                <td class="px-6 py-4 text-right tabular-nums text-green-500">{{ bigIntLabel(item.proxySuccess) }}</td>
                <td class="px-6 py-4 text-right tabular-nums text-red-500">{{ bigIntLabel(proxyErrors(item)) }}</td>
                <td class="px-6 py-4 text-right tabular-nums text-[#888]">{{ formatDuration(item.proxyAvgDurationMs) }}</td>
                <td class="px-6 py-4 text-right tabular-nums">{{ formatBytes(item.agentBytesReceived) }}</td>
                <td class="px-6 py-4 text-right tabular-nums">{{ formatBytes(item.agentBytesSent) }}</td>
              </tr>
            </tbody>
          </table>
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
