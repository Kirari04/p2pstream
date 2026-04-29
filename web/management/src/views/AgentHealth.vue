<script setup lang="ts">
import { inject, computed } from 'vue';
import type { ComputedRef } from 'vue';
import type { GetDashboardResponse } from "@/gen/proto/p2pstream/v1/management_pb";

const dashboard = inject<ComputedRef<GetDashboardResponse | null>>('dashboard');
const status = computed(() => dashboard?.value?.status ?? null);
const oneHourWindow = computed(() => dashboard?.value?.windows.find(w => w.label === '1h'));
const dayWindow = computed(() => dashboard?.value?.windows.find(w => w.label === '24h'));

function bigIntLabel(value: bigint | undefined): string {
  if (value === undefined) return "0";
  return new Intl.NumberFormat().format(Number(value));
}

function formatDate(value: bigint | undefined): string {
  if (value === undefined || value === 0n) return "-";
  return new Date(Number(value)).toLocaleString();
}
</script>

<template>
  <div v-if="dashboard && status" class="space-y-8">
    <div>
      <h3 class="text-xl font-bold mb-2">System Metrics</h3>
      <p class="text-[#888] text-sm">Real-time and historical health data from the remote agent.</p>
    </div>

    <div class="grid gap-6 sm:grid-cols-2 lg:grid-cols-3">
      <div class="vercel-card p-6">
        <p class="vercel-card-title">Memory Usage (Sys)</p>
        <span class="vercel-card-value">{{ bigIntLabel(status.latestAgentStats?.memorySysMb) }} MB</span>
      </div>
      <div class="vercel-card p-6">
        <p class="vercel-card-title">Goroutines</p>
        <span class="vercel-card-value">{{ bigIntLabel(status.latestAgentStats?.numGoroutine) }}</span>
      </div>
      <div class="vercel-card p-6">
        <p class="vercel-card-title">Active Requests</p>
        <span class="vercel-card-value">{{ status.latestAgentStats?.activeRequests ?? 0 }}</span>
      </div>
      <div class="vercel-card p-6">
        <p class="vercel-card-title">Avg Memory (1h)</p>
        <span class="vercel-card-value">{{ bigIntLabel(oneHourWindow?.agentAvgMemoryMb) }} MB</span>
      </div>
      <div class="vercel-card p-6">
        <p class="vercel-card-title">Max Memory (24h)</p>
        <span class="vercel-card-value">{{ bigIntLabel(dayWindow?.agentMaxMemoryMb) }} MB</span>
      </div>
      <div class="vercel-card p-6">
        <p class="vercel-card-title">Max Goroutines (24h)</p>
        <span class="vercel-card-value">{{ bigIntLabel(dayWindow?.agentMaxGoroutines) }}</span>
      </div>
    </div>

    <div class="vercel-card p-8">
      <h4 class="text-sm font-semibold text-[#888] uppercase tracking-widest mb-6">Connection History</h4>
      <div class="grid gap-6 sm:grid-cols-2">
        <div class="flex flex-col">
          <span class="text-xs text-[#666] mb-1">Last Connected</span>
          <span class="text-sm font-mono">{{ formatDate(dashboard.agentConnections?.lastConnectedAtUnixMillis) }}</span>
        </div>
        <div class="flex flex-col">
          <span class="text-xs text-[#666] mb-1">Last Disconnected</span>
          <span class="text-sm font-mono">{{ formatDate(dashboard.agentConnections?.lastDisconnectedAtUnixMillis) }}</span>
        </div>
        <div class="flex flex-col">
          <span class="text-xs text-[#666] mb-1">Total Connections</span>
          <span class="text-sm font-mono">{{ bigIntLabel(dashboard.agentConnections?.totalConnections) }}</span>
        </div>
        <div class="flex flex-col">
          <span class="text-xs text-[#666] mb-1">Active Since</span>
          <span class="text-sm font-mono">{{ formatDate(dashboard.agentConnections?.activeConnectedAtUnixMillis) }}</span>
        </div>
      </div>
    </div>
  </div>
</template>
