<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import RefreshIcon from "@primevue/icons/refresh";
import TimesIcon from "@primevue/icons/times";
import { useManagementClient } from "@/composables/useManagementClient";
import {
  assignmentsForBackend,
  backendAgentAvailabilitySummary,
  backendHealthLabel,
  backendHealthSeverity,
  durationMillisLabel,
  healthTraceOutcomeLabel,
  healthTraceOutcomeSeverity,
  healthTraceReasonSummary,
  healthTraceSourceLabel,
  healthTraceTargetLabel,
  healthTraceTransitionSummary,
} from "@/lib/publicProxyLabels";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import Tag from "@/volt/Tag.vue";
import {
  PublicBackendForwardMode,
  PublicBackendType,
  type Agent,
  type PublicBackend,
  type PublicBackendAgent,
  type PublicBackendHealthTrace,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

const props = defineProps<{
  modelValue: boolean;
  backend: PublicBackend | null;
  backendAgents: readonly PublicBackendAgent[];
  agents: readonly Agent[];
}>();

const emit = defineEmits<{
  (event: "update:modelValue", value: boolean): void;
}>();

const isOpen = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit("update:modelValue", value),
});
const agentAssignments = computed(() => props.backend ? assignmentsForBackend(props.backend, props.backendAgents).filter((item) => item.enabled) : []);
const selectedAgentId = ref<bigint>(0n);
const failuresOnly = ref(false);
const traces = ref<PublicBackendHealthTrace[]>([]);
const selectedSequence = ref<bigint>(0n);
const isTraceDetailOpen = ref(false);
const retainedCount = ref(0n);
const maxRetainedPerTarget = ref(100n);
const isLoading = ref(false);
const errorMessage = ref("");
let refreshTimer: number | null = null;

const selectedTrace = computed(() => traces.value.find((trace) => trace.sequence === selectedSequence.value) ?? null);
const modalTitle = computed(() => props.backend ? `Health checks: ${props.backend.name}` : "Health checks");
const healthSummary = computed(() => props.backend ? backendHealthLabel(props.backend) : "Health unknown");
const healthSeverity = computed(() => props.backend ? backendHealthSeverity(props.backend) : "info");
const isAgentPool = computed(() => props.backend?.backendType === PublicBackendType.PROXY_FORWARD && props.backend.forwardMode === PublicBackendForwardMode.AGENT_POOL);

watch(() => props.modelValue, (open) => {
  if (open) {
    selectedAgentId.value = 0n;
    selectedSequence.value = 0n;
    void loadTraces();
    startPolling();
  } else {
    stopPolling();
    isTraceDetailOpen.value = false;
  }
});

watch(() => props.backend?.id, () => {
  traces.value = [];
  selectedSequence.value = 0n;
  isTraceDetailOpen.value = false;
  selectedAgentId.value = 0n;
  if (props.modelValue) void loadTraces();
});

watch([selectedAgentId, failuresOnly], () => {
  selectedSequence.value = 0n;
  isTraceDetailOpen.value = false;
  if (props.modelValue) void loadTraces();
});

onBeforeUnmount(() => {
  stopPolling();
});

function startPolling() {
  stopPolling();
  refreshTimer = window.setInterval(() => {
    void loadTraces(false);
  }, 5000);
}

function stopPolling() {
  if (refreshTimer === null) return;
  window.clearInterval(refreshTimer);
  refreshTimer = null;
}

async function loadTraces(showBusy = true) {
  if (!props.backend) return;
  if (showBusy) isLoading.value = true;
  errorMessage.value = "";
  try {
    const resp = await managementClient.listPublicBackendHealthTraces({
      backendId: props.backend.id,
      agentId: selectedAgentId.value,
      limit: 100n,
      failuresOnly: failuresOnly.value,
    });
    traces.value = resp.traces;
    retainedCount.value = resp.retainedCount;
    maxRetainedPerTarget.value = resp.maxRetainedPerTarget;
    if (selectedSequence.value && !traces.value.some((trace) => trace.sequence === selectedSequence.value)) {
      selectedSequence.value = 0n;
      isTraceDetailOpen.value = false;
    }
  } catch (err) {
    errorMessage.value = err instanceof Error ? err.message : "Failed to load health traces";
  } finally {
    if (showBusy) isLoading.value = false;
  }
}

function formatDate(value: bigint): string {
  if (!value) return "-";
  return new Date(Number(value)).toLocaleString();
}

function formatDuration(value: bigint): string {
  const millis = Number(value || 0n);
  if (millis <= 0) return "-";
  return millis < 1000 ? `${millis.toString()} ms` : `${(millis / 1000).toFixed(2)} s`;
}

function formatStatus(trace: PublicBackendHealthTrace): string {
  return trace.statusCode ? trace.statusCode.toString() : "-";
}

function formatBool(value: boolean): string {
  return value ? "Yes" : "No";
}

function traceKey(trace: PublicBackendHealthTrace): string {
  return trace.sequence.toString();
}

function assignmentTargetLabel(assignment: PublicBackendAgent): string {
  const agent = props.agents.find((item) => item.id === assignment.agentId);
  if (agent) return `${agent.name} (${agent.publicId})`;
  return `#${assignment.agentId.toString()}`;
}

function openTraceDetail(trace: PublicBackendHealthTrace) {
  selectedSequence.value = trace.sequence;
  isTraceDetailOpen.value = true;
}

function debugEntries(trace: PublicBackendHealthTrace | null): Array<[string, string]> {
  if (!trace?.debugAttributes) return [];
  return Object.entries(trace.debugAttributes).sort(([left], [right]) => left.localeCompare(right));
}
</script>

<template>
  <Modal v-model="isOpen" :title="modalTitle" max-width="72rem">
    <div v-if="backend" class="space-y-5">
      <section class="grid gap-3 lg:grid-cols-4">
        <div class="health-field">
          <span>Current status</span>
          <Tag :value="healthSummary" :severity="healthSeverity" />
        </div>
        <div class="health-field">
          <span>Health check</span>
          <strong>{{ backend.healthCheck?.method || "-" }} {{ backend.healthCheck?.path || "/" }}</strong>
        </div>
        <div class="health-field">
          <span>Timing</span>
          <strong>{{ durationMillisLabel(backend.healthCheck?.intervalMillis ?? 0n) }} / timeout {{ durationMillisLabel(backend.healthCheck?.timeoutMillis ?? 0n) }}</strong>
        </div>
        <div class="health-field">
          <span>Retained</span>
          <strong>{{ retainedCount.toString() }} / {{ maxRetainedPerTarget.toString() }}</strong>
        </div>
        <div v-if="isAgentPool" class="health-field lg:col-span-4">
          <span>Agent pool</span>
          <strong>{{ backendAgentAvailabilitySummary(backend, backendAgents) }}</strong>
        </div>
      </section>

      <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <div class="flex min-w-0 flex-wrap items-center gap-2">
          <button
            type="button"
            class="health-segment"
            :class="selectedAgentId === 0n ? 'health-segment-active' : ''"
            @click="selectedAgentId = 0n"
          >
            All
          </button>
          <button
            v-for="assignment in agentAssignments"
            :key="assignment.agentId.toString()"
            type="button"
            class="health-segment"
            :class="selectedAgentId === assignment.agentId ? 'health-segment-active' : ''"
            @click="selectedAgentId = assignment.agentId"
          >
            {{ assignmentTargetLabel(assignment) }}
          </button>
        </div>
        <div class="flex items-center gap-3">
          <label class="flex h-9 items-center gap-2 rounded-md border border-[#333] bg-black px-3 text-xs font-medium text-[#d4d4d8]">
            <input v-model="failuresOnly" type="checkbox" />
            <span>Failures only</span>
          </label>
          <SecondaryButton size="small" label="Refresh" :loading="isLoading" @click="loadTraces()">
            <template #icon><RefreshIcon class="h-3.5 w-3.5" /></template>
          </SecondaryButton>
        </div>
      </div>

      <p v-if="errorMessage" class="rounded-md border border-red-900/50 bg-red-950/20 px-4 py-3 text-sm text-red-400">{{ errorMessage }}</p>

      <section class="overflow-hidden rounded-md border border-[#222]">
        <div class="overflow-x-auto">
          <table class="w-full min-w-[980px] text-left text-sm">
            <thead>
              <tr class="border-b border-[#333] bg-[#0a0a0a]">
                <th class="px-4 py-3 text-xs font-semibold uppercase tracking-widest text-[#777]">Time</th>
                <th class="px-4 py-3 text-xs font-semibold uppercase tracking-widest text-[#777]">Target</th>
                <th class="px-4 py-3 text-xs font-semibold uppercase tracking-widest text-[#777]">Source</th>
                <th class="px-4 py-3 text-xs font-semibold uppercase tracking-widest text-[#777]">Outcome</th>
                <th class="px-4 py-3 text-right text-xs font-semibold uppercase tracking-widest text-[#777]">HTTP</th>
                <th class="px-4 py-3 text-right text-xs font-semibold uppercase tracking-widest text-[#777]">Duration</th>
                <th class="px-4 py-3 text-xs font-semibold uppercase tracking-widest text-[#777]">Reason</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[#1f1f1f]">
              <tr
                v-for="trace in traces"
                :key="traceKey(trace)"
                class="cursor-pointer transition hover:bg-[#0f0f0f]"
                :class="selectedTrace?.sequence === trace.sequence ? 'bg-[#111]' : ''"
                @click="openTraceDetail(trace)"
              >
                <td class="whitespace-nowrap px-4 py-3 font-mono text-xs text-[#888]">{{ formatDate(trace.finishedAtUnixMillis || trace.startedAtUnixMillis) }}</td>
                <td class="max-w-[14rem] truncate px-4 py-3 text-xs text-[#ededed]">{{ healthTraceTargetLabel(trace, agents) }}</td>
                <td class="px-4 py-3 text-xs text-[#d4d4d8]">{{ healthTraceSourceLabel(trace.source) }}</td>
                <td class="px-4 py-3"><Tag :value="healthTraceOutcomeLabel(trace.outcome)" :severity="healthTraceOutcomeSeverity(trace.outcome)" /></td>
                <td class="px-4 py-3 text-right font-mono text-xs text-[#d4d4d8]">{{ formatStatus(trace) }}</td>
                <td class="px-4 py-3 text-right font-mono text-xs text-[#888]">{{ formatDuration(trace.durationMillis) }}</td>
                <td class="max-w-[24rem] truncate px-4 py-3 text-xs text-[#d4d4d8]">{{ healthTraceReasonSummary(trace) }}</td>
              </tr>
              <tr v-if="!traces.length">
                <td colspan="7" class="px-5 py-8 text-center text-sm text-[#666]">No health-check traces captured for this target.</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>
  </Modal>

  <Teleport to="body">
    <Transition name="drawer">
      <div v-if="isOpen && isTraceDetailOpen && selectedTrace" class="fixed inset-0 z-[70] flex justify-end" @click="isTraceDetailOpen = false">
        <div class="absolute inset-0 bg-black/50 backdrop-blur-[2px]"></div>
        <aside class="health-drawer relative h-full w-full max-w-[34rem] overflow-y-auto border-l border-[#333] bg-[#070707] shadow-2xl" @click.stop>
          <div class="sticky top-0 z-10 flex items-start justify-between gap-4 border-b border-[#222] bg-black px-5 py-4">
            <div class="min-w-0">
              <p class="text-xs font-semibold uppercase tracking-widest text-[#777]">Health trace detail</p>
              <h3 class="mt-1 truncate text-base font-semibold text-white">{{ healthTraceReasonSummary(selectedTrace) }}</h3>
              <p class="mt-1 text-xs text-[#888]">{{ formatDate(selectedTrace.finishedAtUnixMillis || selectedTrace.startedAtUnixMillis) }}</p>
            </div>
            <button
              type="button"
              class="rounded-md p-1.5 text-[#888] transition hover:bg-[#1f1f1f] hover:text-white"
              aria-label="Close health trace detail"
              @click="isTraceDetailOpen = false"
            >
              <TimesIcon class="h-4 w-4" />
            </button>
          </div>

          <div class="space-y-4 p-5">
            <div class="flex flex-wrap items-center gap-2">
              <Tag :value="healthTraceOutcomeLabel(selectedTrace.outcome)" :severity="healthTraceOutcomeSeverity(selectedTrace.outcome)" />
              <Tag :value="healthTraceSourceLabel(selectedTrace.source)" severity="info" />
              <Tag :value="healthTraceTargetLabel(selectedTrace, agents)" severity="secondary" />
            </div>

            <div class="grid gap-3 sm:grid-cols-2">
              <div class="health-field sm:col-span-2">
                <span>Request</span>
                <strong class="break-all font-mono">{{ selectedTrace.method || "-" }} {{ selectedTrace.url || "-" }}</strong>
              </div>
              <div class="health-field">
                <span>Expected</span>
                <strong>{{ selectedTrace.expectedStatusMin.toString() }}-{{ selectedTrace.expectedStatusMax.toString() }}</strong>
              </div>
              <div class="health-field">
                <span>Timeout</span>
                <strong>{{ formatDuration(selectedTrace.timeoutMillis) }}</strong>
              </div>
              <div class="health-field">
                <span>TLS skip verify</span>
                <strong>{{ formatBool(selectedTrace.tlsSkipVerify) }}</strong>
              </div>
              <div class="health-field">
                <span>HTTP status</span>
                <strong>{{ formatStatus(selectedTrace) }}</strong>
              </div>
              <div class="health-field sm:col-span-2">
                <span>Transition</span>
                <strong>{{ healthTraceTransitionSummary(selectedTrace) }}</strong>
              </div>
              <div class="health-field">
                <span>Availability</span>
                <strong>{{ formatBool(selectedTrace.availableBefore) }} -> {{ formatBool(selectedTrace.availableAfter) }}</strong>
              </div>
              <div class="health-field">
                <span>Duration</span>
                <strong>{{ formatDuration(selectedTrace.durationMillis) }}</strong>
              </div>
              <div class="health-field">
                <span>Healthy streak</span>
                <strong>{{ selectedTrace.healthyStreakBefore.toString() }} -> {{ selectedTrace.healthyStreakAfter.toString() }}</strong>
              </div>
              <div class="health-field">
                <span>Unhealthy streak</span>
                <strong>{{ selectedTrace.unhealthyStreakBefore.toString() }} -> {{ selectedTrace.unhealthyStreakAfter.toString() }}</strong>
              </div>
              <div v-if="selectedTrace.passiveUnhealthyUntilUnixMillis" class="health-field sm:col-span-2">
                <span>Passive cooldown until</span>
                <strong>{{ formatDate(selectedTrace.passiveUnhealthyUntilUnixMillis) }}</strong>
              </div>
              <div class="health-field sm:col-span-2">
                <span>Error</span>
                <strong class="break-all">{{ selectedTrace.error || "-" }}</strong>
              </div>
            </div>

            <div class="rounded-md border border-[#222] bg-black p-4">
              <h4 class="mb-3 text-xs font-semibold uppercase tracking-widest text-[#777]">Debug attributes</h4>
              <dl v-if="debugEntries(selectedTrace).length" class="grid gap-2 sm:grid-cols-[10rem_1fr]">
                <template v-for="[key, value] in debugEntries(selectedTrace)" :key="key">
                  <dt class="font-mono text-xs text-[#777]">{{ key }}</dt>
                  <dd class="break-all font-mono text-xs text-[#d4d4d8]">{{ value }}</dd>
                </template>
              </dl>
              <p v-else class="text-sm text-[#666]">No debug attributes captured.</p>
            </div>
          </div>
        </aside>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.health-field {
  border: 1px solid #222;
  border-radius: 6px;
  background: #050505;
  padding: 0.75rem;
}

.health-field span {
  display: block;
  margin-bottom: 0.35rem;
  color: #777;
  font-size: 0.68rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.health-field strong {
  color: #ededed;
  font-size: 0.85rem;
  font-weight: 600;
}

.health-segment {
  min-height: 2.25rem;
  border: 1px solid #333;
  border-radius: 6px;
  background: #050505;
  padding: 0.45rem 0.7rem;
  color: #888;
  font-size: 0.75rem;
  font-weight: 700;
  transition: border-color 0.15s ease, color 0.15s ease, background 0.15s ease;
}

.health-segment:hover {
  border-color: #555;
  color: #ededed;
}

.health-segment-active {
  border-color: #ededed;
  background: #ededed;
  color: #050505;
}

.health-drawer {
  animation: drawer-in 0.18s ease-out;
}

.drawer-enter-active,
.drawer-leave-active {
  transition: opacity 0.18s ease;
}

.drawer-enter-from,
.drawer-leave-to {
  opacity: 0;
}

.drawer-enter-from .health-drawer,
.drawer-leave-to .health-drawer {
  transform: translateX(1.5rem);
}

@keyframes drawer-in {
  from {
    transform: translateX(1.5rem);
  }
  to {
    transform: translateX(0);
  }
}
</style>
