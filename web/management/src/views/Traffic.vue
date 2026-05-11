<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onMounted, ref } from "vue";
import type { ComputedRef } from "vue";
import { managementClient } from "@/api/managementClient";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import TrafficFlowEditTargetChooser from "@/components/editors/TrafficFlowEditTargetChooser.vue";
import TrafficFlowDiagram from "@/components/TrafficFlowDiagram.vue";
import TrafficTraceDetailsModal from "@/components/TrafficTraceDetailsModal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import type { TrafficFlowEditRequest, TrafficFlowEditTarget } from "@/types/trafficFlowEdit";
import type {
  DashboardWindowSummary,
  GetDashboardResponse,
  GetPublicProxyConfigResponse,
  TrafficTraceEvent,
  TrafficTraceSettings,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  PublicBackendForwardMode,
  PublicBackendType,
  PublicRateLimitAlgorithm,
  TrafficTraceLevel,
  TrafficTraceStage,
} from "@/gen/proto/p2pstream/v1/management_pb";

type TraceRequest = {
  requestId: string;
  method: string;
  host: string;
  path: string;
  query: string;
  stage: TrafficTraceStage;
  statusCode: bigint;
  durationMs: bigint;
  errorKind: string;
  listenerId: bigint;
  listenerName: string;
  routeId: bigint;
  routeLabel: string;
  defaultRoute: boolean;
  backendId: bigint;
  backendName: string;
  backendType: PublicBackendType;
  forwardMode: PublicBackendForwardMode;
  targetOrigin: string;
  agentId: bigint;
  agentName: string;
  agentPublicId: string;
  requestBytes: bigint;
  responseBytes: bigint;
  rateLimitRuleId: bigint;
  rateLimitRuleName: string;
  rateLimitAlgorithm: PublicRateLimitAlgorithm;
  visible: boolean;
  completedAt: number | null;
  latestEvent: TrafficTraceEvent | null;
  events: TrafficTraceEvent[];
};

const dashboard = inject<ComputedRef<GetDashboardResponse | null>>("dashboard");
const publicProxyConfig = inject<ComputedRef<GetPublicProxyConfigResponse | null>>("publicProxyConfig");

const trafficWindows = computed(() => dashboard?.value?.windows ?? []);
const config = computed(() => publicProxyConfig?.value ?? null);

const traceSettings = ref<TrafficTraceSettings | null>(null);
const selectedTraceLevel = ref<TrafficTraceLevel>(TrafficTraceLevel.BASIC);
const isTraceBusy = ref(false);
const streamState = ref<"idle" | "connecting" | "live" | "retrying" | "error">("idle");
const streamError = ref("");
const traceRequests = ref<TraceRequest[]>([]);
const selectedRequest = ref<TraceRequest | null>(null);
const isDetailsOpen = ref(false);
const lastSequence = ref<bigint>(0n);
const renderedTokenCount = ref(0);
const editorHost = ref<InstanceType<typeof PublicProxyEditorHost> | null>(null);
const pendingEditRequest = ref<TrafficFlowEditRequest | null>(null);
const isEditChooserOpen = ref(false);

let streamController: AbortController | null = null;
let retryTimer: number | null = null;
let retryDelayMs = 1000;
const hideTimers = new Map<string, number>();

const traceLevelOptions = [
  { label: "Basic", value: TrafficTraceLevel.BASIC },
  { label: "Detailed", value: TrafficTraceLevel.DETAILED },
  { label: "Headers", value: TrafficTraceLevel.HEADERS },
  { label: "Debug", value: TrafficTraceLevel.DEBUG },
];

const tracingEnabled = computed(() => traceSettings.value?.enabled === true);

async function loadTraceSettings() {
  try {
    const resp = await managementClient.getTrafficTraceSettings({});
    applyTraceSettings(resp.settings ?? null);
    if (resp.settings?.enabled) {
      startTraceStream();
    }
  } catch (err) {
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
    const stream = managementClient.streamTrafficTraceEvents(
      { replayRecent: true, afterSequence: lastSequence.value },
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
  if (event.sequence > lastSequence.value) {
    lastSequence.value = event.sequence;
  }
  const requestId = event.requestId || `trace-${event.sequence.toString()}`;
  let request = traceRequests.value.find((item) => item.requestId === requestId);
  if (!request) {
    request = newTraceRequest(requestId);
  }

  request.events.push(event);
  request.latestEvent = event;
  request.visible = true;
  request.stage = event.stage;
  request.method = event.method || request.method;
  request.host = event.host || request.host;
  request.path = event.path || request.path;
  request.query = event.query || request.query;
  request.statusCode = event.statusCode || request.statusCode;
  request.durationMs = event.durationMs || request.durationMs;
  request.errorKind = event.errorKind || request.errorKind;
  request.listenerId = event.listenerId || request.listenerId;
  request.listenerName = event.listenerName || request.listenerName;
  request.routeId = event.routeId || request.routeId;
  request.routeLabel = event.routeLabel || request.routeLabel;
  request.defaultRoute = event.defaultRoute || request.defaultRoute;
  request.backendId = event.backendId || request.backendId;
  request.backendName = event.backendName || request.backendName;
  request.backendType = event.backendType || request.backendType;
  request.forwardMode = event.forwardMode || request.forwardMode;
  request.targetOrigin = event.targetOrigin || request.targetOrigin;
  request.agentId = event.agentId || request.agentId;
  request.agentName = event.agentName || request.agentName;
  request.agentPublicId = event.agentPublicId || request.agentPublicId;
  request.requestBytes = event.requestBytes || request.requestBytes;
  request.responseBytes = event.responseBytes || request.responseBytes;
  request.rateLimitRuleId = event.rateLimitRuleId || request.rateLimitRuleId;
  request.rateLimitRuleName = event.rateLimitRuleName || request.rateLimitRuleName;
  request.rateLimitAlgorithm = event.rateLimitAlgorithm || request.rateLimitAlgorithm;

  if (event.stage === TrafficTraceStage.RESPONSE_SENT || event.stage === TrafficTraceStage.FAILED || event.stage === TrafficTraceStage.RATE_LIMITED) {
    request.completedAt = Date.now();
    queueHideRequest(request.requestId);
  }

  traceRequests.value = [
    request,
    ...traceRequests.value.filter((item) => item.requestId !== requestId),
  ].slice(0, 200);
}

function newTraceRequest(requestId: string): TraceRequest {
  return {
    requestId,
    method: "",
    host: "",
    path: "",
    query: "",
    stage: TrafficTraceStage.UNSPECIFIED,
    statusCode: 0n,
    durationMs: 0n,
    errorKind: "",
    listenerId: 0n,
    listenerName: "",
    routeId: 0n,
    routeLabel: "",
    defaultRoute: false,
    backendId: 0n,
    backendName: "",
    backendType: PublicBackendType.UNSPECIFIED,
    forwardMode: PublicBackendForwardMode.UNSPECIFIED,
    targetOrigin: "",
    agentId: 0n,
    agentName: "",
    agentPublicId: "",
    requestBytes: 0n,
    responseBytes: 0n,
    rateLimitRuleId: 0n,
    rateLimitRuleName: "",
    rateLimitAlgorithm: PublicRateLimitAlgorithm.UNSPECIFIED,
    visible: true,
    completedAt: null,
    latestEvent: null,
    events: [],
  };
}

function queueHideRequest(requestId: string) {
  const existing = hideTimers.get(requestId);
  if (existing !== undefined) {
    window.clearTimeout(existing);
  }
  const timer = window.setTimeout(() => {
    hideTimers.delete(requestId);
    const request = traceRequests.value.find((item) => item.requestId === requestId);
    if (!request) return;
    request.visible = false;
    traceRequests.value = [...traceRequests.value];
  }, 2000);
  hideTimers.set(requestId, timer);
}

function openTraceDetails(request: TraceRequest) {
  selectedRequest.value = request;
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

function proxyErrors(window: DashboardWindowSummary): bigint {
  return window.proxyClientError + window.proxyServerError + window.proxyInternalError;
}

function bigIntLabel(value: bigint | undefined): string {
  if (value === undefined) return "0";
  return new Intl.NumberFormat().format(Number(value));
}

function formatBytes(value: bigint | undefined): string {
  if (value === undefined) return "0 B";
  const bytes = Number(value);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function formatDuration(value: bigint | undefined): string {
  if (value === undefined || value === 0n) return "-";
  const millis = Number(value);
  if (millis < 1000) return `${millis} ms`;
  return `${(millis / 1000).toFixed(1)} s`;
}

function traceStageLabel(stage: TrafficTraceStage): string {
  switch (stage) {
    case TrafficTraceStage.RECEIVED: return "Received";
    case TrafficTraceStage.ROUTE_RESOLVED: return "Route";
    case TrafficTraceStage.BACKEND_SELECTED: return "Backend";
    case TrafficTraceStage.AGENT_SELECTED: return "Agent";
    case TrafficTraceStage.UPSTREAM_STARTED: return "Upstream";
    case TrafficTraceStage.UPSTREAM_RESPONDED: return "Responded";
    case TrafficTraceStage.RESPONSE_SENT: return "Done";
    case TrafficTraceStage.FAILED: return "Failed";
    case TrafficTraceStage.RATE_LIMITED: return "Rate limited";
    default: return "Waiting";
  }
}

function requestStatusClass(request: TraceRequest): string {
  if (request.stage === TrafficTraceStage.FAILED) return "text-red-400";
  if (request.stage === TrafficTraceStage.RATE_LIMITED) return "text-amber-400";
  const status = Number(request.statusCode);
  if (status >= 500) return "text-red-400";
  if (status >= 400) return "text-amber-400";
  if (status >= 200) return "text-green-400";
  return "text-[#888]";
}

function traceFlowLabel(request: TraceRequest): string {
  const parts = [request.listenerName || "Listener"];
  if (request.rateLimitRuleName || request.stage === TrafficTraceStage.RATE_LIMITED) {
    parts.push(request.rateLimitRuleName ? `Rate limit: ${request.rateLimitRuleName}` : "Rate limit");
  }
  if (request.routeLabel || request.defaultRoute) {
    parts.push(request.routeLabel || "Default route");
  }
  if (request.backendName) {
    parts.push(request.backendName);
  }
  if (request.agentName || request.agentPublicId) {
    parts.push(request.agentName || request.agentPublicId);
  }
  return parts.join(" -> ");
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
  void loadTraceSettings();
});

onBeforeUnmount(() => {
  stopTraceStream();
  for (const timer of hideTimers.values()) {
    window.clearTimeout(timer);
  }
  hideTimers.clear();
});
</script>

<template>
  <div v-if="dashboard" class="space-y-8">
    <section class="space-y-4">
      <div class="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
        <div>
          <h3 class="text-xl font-bold mb-2">Traffic Flow</h3>
          <p class="text-[#888] text-sm">Live request routing across listeners, routes, backends, agents, and upstreams.</p>
        </div>

        <div class="flex flex-col gap-3 sm:flex-row sm:items-center">
          <label class="flex h-10 items-center gap-3 rounded-md border border-[#333] bg-black px-3 text-sm text-[#ededed]">
            <input
              type="checkbox"
              class="h-4 w-4 accent-white"
              :checked="tracingEnabled"
              :disabled="isTraceBusy"
              @change="setTracingEnabled(($event.target as HTMLInputElement).checked)"
            />
            <span>Tracing</span>
          </label>

          <div class="grid grid-cols-4 overflow-hidden rounded-md border border-[#333]">
            <button
              v-for="option in traceLevelOptions"
              :key="option.value"
              type="button"
              class="h-10 border-r border-[#333] px-3 text-xs font-medium text-[#888] transition last:border-r-0 hover:bg-[#111] hover:text-white disabled:opacity-50"
              :class="selectedTraceLevel === option.value ? 'bg-white text-black hover:bg-white hover:text-black' : 'bg-black'"
              :disabled="isTraceBusy"
              @click="setTraceLevel(option.value)"
            >
              {{ option.label }}
            </button>
          </div>
        </div>
      </div>

      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <div class="vercel-card p-4">
          <p class="vercel-card-title">Trace State</p>
          <span class="text-lg font-semibold" :class="tracingEnabled ? 'text-green-400' : 'text-[#888]'">{{ streamStateLabel() }}</span>
        </div>
        <div class="vercel-card p-4">
          <p class="vercel-card-title">Subscribers</p>
          <span class="text-lg font-semibold">{{ traceSettings?.subscriberCount?.toString() ?? "0" }}</span>
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
          <p class="vercel-card-title">Live Tokens</p>
          <span class="text-lg font-semibold">{{ renderedTokenCount }}</span>
        </div>
      </div>

      <div v-if="streamError" class="rounded-md border border-amber-900/60 bg-amber-950/20 px-4 py-3 text-sm text-amber-300">
        {{ streamError }}
      </div>

      <TrafficFlowDiagram
        :config="config"
        :requests="traceRequests"
        :tracing-enabled="tracingEnabled"
        @select="openTraceDetails"
        @active-change="renderedTokenCount = $event"
        @edit-node="handleFlowEditRequest"
      />

      <div class="vercel-card overflow-hidden">
        <div class="flex items-center justify-between border-b border-[#333] px-5 py-4">
          <div>
            <h4 class="font-semibold">Recent traces</h4>
            <p class="text-xs text-[#888]">Last 200 requests captured while tracing is enabled.</p>
          </div>
          <SecondaryButton
            label="Clear"
            size="small"
            class="!border-[#333] !bg-transparent !text-[#888] hover:!border-[#666]"
            :disabled="!traceRequests.length"
            @click="traceRequests = []"
          />
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
                v-for="request in traceRequests"
                :key="request.requestId"
                class="cursor-pointer transition hover:bg-[#0a0a0a]"
                @click="openTraceDetails(request)"
              >
                <td class="px-5 py-3">
                  <div class="flex items-center gap-2">
                    <span class="rounded border border-[#333] px-1.5 py-0.5 font-mono text-[0.7rem] text-[#d4d4d8]">{{ request.method || "-" }}</span>
                    <span class="max-w-[18rem] truncate font-mono text-xs text-white">{{ request.path || "/" }}</span>
                  </div>
                  <p class="mt-1 max-w-[22rem] truncate font-mono text-[0.7rem] text-[#666]">{{ request.requestId }}</p>
                </td>
                <td class="px-5 py-3">
                  <p class="text-xs text-[#d4d4d8]">
                    {{ traceFlowLabel(request) }}
                  </p>
                  <p class="mt-1 text-[0.7rem] text-[#888]">{{ traceStageLabel(request.stage) }}</p>
                </td>
                <td class="px-5 py-3 text-right font-mono text-xs" :class="requestStatusClass(request)">
                  {{ request.statusCode ? request.statusCode.toString() : traceStageLabel(request.stage) }}
                </td>
                <td class="px-5 py-3 text-right font-mono text-xs text-[#888]">{{ formatDuration(request.durationMs) }}</td>
              </tr>
              <tr v-if="!traceRequests.length">
                <td colspan="4" class="px-5 py-8 text-center text-sm text-[#888]">No traces captured.</td>
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
