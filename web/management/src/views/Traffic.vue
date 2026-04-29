<script setup lang="ts">
import { inject, computed } from 'vue';
import type { ComputedRef } from 'vue';
import type { 
  GetDashboardResponse,
  DashboardWindowSummary,
} from "@/gen/proto/p2pstream/v1/management_pb";

const dashboard = inject<ComputedRef<GetDashboardResponse | null>>('dashboard');
const trafficWindows = computed(() => dashboard?.value?.windows ?? []);

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
</script>

<template>
  <div v-if="dashboard" class="space-y-8">
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
  </div>
</template>
