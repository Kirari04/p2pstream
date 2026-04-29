<script setup lang="ts">
import { inject, computed } from 'vue';
import type { ComputedRef } from 'vue';
import { 
  ProxyState,
  type GetDashboardResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const dashboard = inject<ComputedRef<GetDashboardResponse | null>>('dashboard');
const status = computed(() => dashboard?.value?.status ?? null);
const oneHourWindow = computed(() => dashboard?.value?.windows.find(w => w.label === '1h'));

const proxyState = computed(() => status.value?.proxy?.state ?? ProxyState.UNSPECIFIED);
const proxyIsRunning = computed(() => proxyState.value === ProxyState.RUNNING || status.value?.proxyRunning === true);

function proxyStateLabel(state: ProxyState): string {
  switch (state) {
    case ProxyState.STOPPED: return "Stopped";
    case ProxyState.STARTING: return "Starting";
    case ProxyState.RUNNING: return "Running";
    case ProxyState.STOPPING: return "Stopping";
    case ProxyState.ERROR: return "Error";
    default: return status.value?.proxyRunning ? "Running" : "Unknown";
  }
}

function bigIntLabel(value: bigint | undefined): string {
  if (value === undefined) return "0";
  return new Intl.NumberFormat().format(Number(value));
}

function formatDuration(value: bigint | undefined): string {
  if (value === undefined || value === 0n) return "-";
  const millis = Number(value);
  if (millis < 1000) return `${millis} ms`;
  return `${(millis / 1000).toFixed(1)} s`;
}
</script>

<template>
  <div v-if="dashboard && status" class="space-y-12">
    <section>
      <div class="grid gap-6 sm:grid-cols-2 lg:grid-cols-4">
        <div class="vercel-card p-6">
          <p class="vercel-card-title">Proxy Status</p>
          <div class="flex items-center gap-2">
            <div class="h-2 w-2 rounded-full" :class="proxyIsRunning ? 'bg-green-500' : 'bg-red-500'"></div>
            <span class="vercel-card-value">{{ proxyStateLabel(proxyState) }}</span>
          </div>
        </div>

        <div class="vercel-card p-6">
          <p class="vercel-card-title">Agent Connectivity</p>
          <div class="flex items-center gap-2">
            <div class="h-2 w-2 rounded-full" :class="status.agentConnected ? 'bg-green-500' : 'bg-red-500'"></div>
            <span class="vercel-card-value">{{ status.agentConnected ? 'Connected' : 'Disconnected' }}</span>
          </div>
        </div>

        <div class="vercel-card p-6 lg:col-span-2">
          <p class="vercel-card-title">Target Origin</p>
          <span class="vercel-card-value truncate block">{{ status.targetOrigin || "Not configured" }}</span>
        </div>
      </div>
    </section>

    <section>
      <h3 class="text-lg font-semibold mb-6">Quick Stats (1h)</h3>
      <div class="grid gap-6 sm:grid-cols-2 lg:grid-cols-3">
        <div class="vercel-card p-6">
          <p class="vercel-card-title">Requests</p>
          <span class="vercel-card-value">{{ bigIntLabel(oneHourWindow?.proxyRequests) }}</span>
        </div>
        <div class="vercel-card p-6">
          <p class="vercel-card-title">Success Rate</p>
          <span class="vercel-card-value text-green-500">
            {{ oneHourWindow?.proxyRequests ? Math.round(Number(oneHourWindow.proxySuccess) / Number(oneHourWindow.proxyRequests) * 100) : 0 }}%
          </span>
        </div>
        <div class="vercel-card p-6">
          <p class="vercel-card-title">Avg Latency</p>
          <span class="vercel-card-value">{{ formatDuration(oneHourWindow?.proxyAvgDurationMs) }}</span>
        </div>
      </div>
    </section>
  </div>
</template>
