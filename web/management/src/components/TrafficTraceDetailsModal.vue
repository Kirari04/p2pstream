<script setup lang="ts">
import { computed } from "vue";
import Modal from "@/volt/Modal.vue";
import type { TraceRequest } from "@/types/trafficTrace";
import {
  PublicBackendForwardMode,
  PublicBackendType,
  PublicRateLimitAlgorithm,
  PublicTrafficShaperBudgetScope,
  TrafficTraceLevel,
  TrafficTraceStage,
} from "@/gen/proto/p2pstream/v1/management_pb";

const props = defineProps<{
  modelValue: boolean;
  request: TraceRequest | null;
  level: TrafficTraceLevel;
}>();

const emit = defineEmits<{
  (event: "update:modelValue", value: boolean): void;
}>();

const isOpen = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit("update:modelValue", value),
});

const latestEvent = computed(() => props.request?.latestEvent ?? null);
const showDetailed = computed(() => props.level >= TrafficTraceLevel.DETAILED);
const showHeaders = computed(() => props.level >= TrafficTraceLevel.HEADERS);
const showDebug = computed(() => props.level >= TrafficTraceLevel.DEBUG);

function statusClass(status: bigint, stage: TrafficTraceStage): string {
  if (stage === TrafficTraceStage.FAILED) return "text-red-400";
  if (stage === TrafficTraceStage.RATE_LIMITED) return "text-amber-400";
  const code = Number(status);
  if (code >= 500) return "text-red-400";
  if (code >= 400) return "text-amber-400";
  if (code >= 200) return "text-green-400";
  return "text-[#888]";
}

function stageLabel(stage: TrafficTraceStage): string {
  switch (stage) {
    case TrafficTraceStage.RECEIVED: return "Received";
    case TrafficTraceStage.ROUTE_RESOLVED: return "Route resolved";
    case TrafficTraceStage.BACKEND_SELECTED: return "Backend selected";
    case TrafficTraceStage.AGENT_SELECTED: return "Agent selected";
    case TrafficTraceStage.TRAFFIC_SHAPER_SELECTED: return "Traffic shaper selected";
    case TrafficTraceStage.UPSTREAM_STARTED: return "Upstream started";
    case TrafficTraceStage.UPSTREAM_RESPONDED: return "Upstream responded";
    case TrafficTraceStage.RESPONSE_SENT: return "Response sent";
    case TrafficTraceStage.FAILED: return "Failed";
    case TrafficTraceStage.RATE_LIMITED: return "Rate limited";
    default: return "Unknown";
  }
}

function rateLimitAlgorithmLabel(algorithm: PublicRateLimitAlgorithm): string {
  switch (algorithm) {
    case PublicRateLimitAlgorithm.SLIDING_WINDOW: return "Sliding window";
    case PublicRateLimitAlgorithm.TOKEN_BUCKET: return "Token bucket";
    case PublicRateLimitAlgorithm.LEAKY_BUCKET: return "Leaky bucket";
    case PublicRateLimitAlgorithm.FIXED_WINDOW: return "Fixed window";
    default: return "-";
  }
}

function trafficShaperScopeLabel(scope: PublicTrafficShaperBudgetScope): string {
  return scope === PublicTrafficShaperBudgetScope.PER_REQUEST ? "Per request" : "Per key";
}

function formatRate(value: bigint): string {
  const bytes = Number(value || 0n);
  if (bytes <= 0) return "unlimited";
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024).toString()} KiB/s`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB/s`;
}

function backendTypeLabel(type: PublicBackendType): string {
  if (type === PublicBackendType.STATIC) return "Static";
  if (type === PublicBackendType.PROXY_FORWARD) return "Proxy forward";
  return "-";
}

function forwardModeLabel(mode: PublicBackendForwardMode): string {
  if (mode === PublicBackendForwardMode.AGENT_POOL) return "Agent pool";
  if (mode === PublicBackendForwardMode.DIRECT) return "Direct";
  return "-";
}

function formatDate(value: bigint): string {
  if (!value) return "-";
  return new Date(Number(value)).toLocaleTimeString();
}

function formatDuration(value: bigint): string {
  if (!value) return "-";
  const millis = Number(value);
  return millis < 1000 ? `${millis} ms` : `${(millis / 1000).toFixed(2)} s`;
}

function formatBytes(value: bigint): string {
  const bytes = Number(value || 0n);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function numberLabel(value: number): string {
  return new Intl.NumberFormat().format(value);
}

function entries(mapValue: Record<string, string> | undefined): Array<[string, string]> {
  if (!mapValue) return [];
  return Object.entries(mapValue).sort(([a], [b]) => a.localeCompare(b));
}
</script>

<template>
  <Modal v-model="isOpen" title="Trace details" max-width="52rem">
    <div v-if="request" class="space-y-6">
      <section class="grid gap-3 sm:grid-cols-2">
        <div class="trace-field sm:col-span-2">
          <span>Request</span>
          <strong class="break-all font-mono">{{ request.requestId }}</strong>
        </div>
        <p v-if="request.sampledEventCount > 0" class="rounded-md border border-amber-900/60 bg-amber-950/20 px-3 py-2 text-xs text-amber-300 sm:col-span-2">
          Some intermediate trace events were omitted while the UI was under load. {{ numberLabel(request.sampledEventCount) }} events were sampled for this request.
        </p>
        <div class="trace-field">
          <span>Method</span>
          <strong>{{ request.method || "-" }}</strong>
        </div>
        <div class="trace-field">
          <span>Status</span>
          <strong :class="statusClass(request.statusCode, request.stage)">
            {{ request.statusCode ? request.statusCode.toString() : stageLabel(request.stage) }}
          </strong>
        </div>
        <div class="trace-field sm:col-span-2">
          <span>Path</span>
          <strong class="break-all font-mono">{{ request.path || "/" }}</strong>
        </div>
        <div class="trace-field">
          <span>Duration</span>
          <strong>{{ formatDuration(request.durationMs) }}</strong>
        </div>
        <div class="trace-field">
          <span>Current stage</span>
          <strong>{{ stageLabel(request.stage) }}</strong>
        </div>
      </section>

      <section class="grid gap-3 sm:grid-cols-2">
        <div class="trace-field">
          <span>Listener</span>
          <strong>{{ request.listenerName || (request.listenerId ? `#${request.listenerId.toString()}` : "-") }}</strong>
        </div>
        <div class="trace-field">
          <span>Route</span>
          <strong>{{ request.routeLabel || (request.defaultRoute ? "Default route" : "-") }}</strong>
        </div>
        <div class="trace-field">
          <span>Backend</span>
          <strong>{{ request.backendName || (request.backendId ? `#${request.backendId.toString()}` : "-") }}</strong>
        </div>
        <div class="trace-field">
          <span>Agent</span>
          <strong>{{ request.agentName || request.agentPublicId || "-" }}</strong>
        </div>
        <div v-if="request.rateLimitRuleId" class="trace-field sm:col-span-2">
          <span>Rate limit</span>
          <strong>
            {{ request.rateLimitRuleName || `#${request.rateLimitRuleId.toString()}` }}
            <span class="text-[#888]">/ {{ rateLimitAlgorithmLabel(request.rateLimitAlgorithm) }}</span>
          </strong>
        </div>
        <div v-if="request.trafficShaperRuleId" class="trace-field sm:col-span-2">
          <span>Traffic shaper</span>
          <strong>
            {{ request.trafficShaperRuleName || `#${request.trafficShaperRuleId.toString()}` }}
            <span class="text-[#888]">
              / {{ trafficShaperScopeLabel(request.trafficShaperBudgetScope) }}
              / up {{ formatRate(request.trafficShaperUploadBytesPerSecond) }}
              / down {{ formatRate(request.trafficShaperDownloadBytesPerSecond) }}
              / free {{ formatBytes(request.trafficShaperRequestExemptBytes) }} req, {{ formatBytes(request.trafficShaperResponseExemptBytes) }} res
            </span>
          </strong>
        </div>
      </section>

      <section v-if="showDetailed" class="grid gap-3 sm:grid-cols-2">
        <div class="trace-field">
          <span>Host</span>
          <strong class="break-all font-mono">{{ request.host || "-" }}</strong>
        </div>
        <div class="trace-field">
          <span>Query</span>
          <strong class="break-all font-mono">{{ request.query || "-" }}</strong>
        </div>
        <div class="trace-field">
          <span>Backend type</span>
          <strong>{{ backendTypeLabel(request.backendType) }}</strong>
        </div>
        <div class="trace-field">
          <span>Forward mode</span>
          <strong>{{ forwardModeLabel(request.forwardMode) }}</strong>
        </div>
        <div class="trace-field sm:col-span-2">
          <span>Target origin</span>
          <strong class="break-all font-mono">{{ request.targetOrigin || "-" }}</strong>
        </div>
        <div class="trace-field sm:col-span-2">
          <span>Error kind</span>
          <strong>{{ request.errorKind || "-" }}</strong>
        </div>
      </section>

      <section v-if="showHeaders" class="grid gap-4 lg:grid-cols-2">
        <div class="trace-panel">
          <h4>Request headers</h4>
          <dl v-if="entries(latestEvent?.requestHeaders).length">
            <template v-for="[name, value] in entries(latestEvent?.requestHeaders)" :key="name">
              <dt>{{ name }}</dt>
              <dd>{{ value }}</dd>
            </template>
          </dl>
          <p v-else>No request headers captured.</p>
        </div>
        <div class="trace-panel">
          <h4>Response headers</h4>
          <dl v-if="entries(latestEvent?.responseHeaders).length">
            <template v-for="[name, value] in entries(latestEvent?.responseHeaders)" :key="name">
              <dt>{{ name }}</dt>
              <dd>{{ value }}</dd>
            </template>
          </dl>
          <p v-else>No response headers captured.</p>
        </div>
      </section>

      <section v-if="showDebug" class="grid gap-4 lg:grid-cols-2">
        <div class="trace-field">
          <span>Request bytes</span>
          <strong>{{ formatBytes(request.requestBytes) }}</strong>
        </div>
        <div class="trace-field">
          <span>Response bytes</span>
          <strong>{{ formatBytes(request.responseBytes) }}</strong>
        </div>
        <div class="trace-panel lg:col-span-2">
          <h4>Debug attributes</h4>
          <dl v-if="entries(latestEvent?.debugAttributes).length">
            <template v-for="[name, value] in entries(latestEvent?.debugAttributes)" :key="name">
              <dt>{{ name }}</dt>
              <dd>{{ value }}</dd>
            </template>
          </dl>
          <p v-else>No debug attributes captured.</p>
        </div>
      </section>

      <section class="trace-panel">
        <h4>Lifecycle</h4>
        <div class="divide-y divide-[#222]">
          <div v-for="event in request.events" :key="event.sequence.toString()" class="grid gap-2 py-2 text-xs sm:grid-cols-[10rem_1fr_6rem]">
            <span class="text-[#888]">{{ formatDate(event.occurredAtUnixMillis) }}</span>
            <span class="font-medium text-white">{{ stageLabel(event.stage) }}</span>
            <span class="text-right font-mono text-[#888]">{{ formatDuration(event.durationMs) }}</span>
          </div>
        </div>
      </section>
    </div>
  </Modal>
</template>

<style scoped>
.trace-field {
  border: 1px solid #222;
  border-radius: 6px;
  background: #050505;
  padding: 0.75rem;
}

.trace-field span {
  display: block;
  margin-bottom: 0.35rem;
  color: #888;
  font-size: 0.7rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
}

.trace-field strong {
  color: #ededed;
  font-size: 0.875rem;
}

.trace-panel {
  border: 1px solid #222;
  border-radius: 6px;
  background: #050505;
  padding: 0.85rem;
}

.trace-panel h4 {
  margin-bottom: 0.75rem;
  color: #ededed;
  font-size: 0.8rem;
  font-weight: 700;
}

.trace-panel dl {
  display: grid;
  gap: 0.5rem;
}

.trace-panel dt {
  color: #888;
  font-family: var(--font-mono);
  font-size: 0.72rem;
}

.trace-panel dd {
  margin: -0.35rem 0 0;
  overflow-wrap: anywhere;
  color: #d4d4d8;
  font-family: var(--font-mono);
  font-size: 0.75rem;
}

.trace-panel p {
  color: #888;
  font-size: 0.8rem;
}
</style>
