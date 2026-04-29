<script setup lang="ts">
import { inject, computed } from 'vue';
import type { ComputedRef } from 'vue';
import BanIcon from "@primevue/icons/ban";
import PlusIcon from "@primevue/icons/plus";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import Tag from "@/volt/Tag.vue";
import { 
  ProxyState,
  type GetDashboardResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const dashboard = inject<ComputedRef<GetDashboardResponse | null>>('dashboard');
const setProxyRunning = inject<(shouldRun: boolean) => Promise<void>>('setProxyRunning');
const logout = inject<() => Promise<void>>('logout');
const isBusy = inject<ComputedRef<boolean>>('isBusy');

const status = computed(() => dashboard?.value?.status ?? null);
const proxyState = computed(() => status.value?.proxy?.state ?? ProxyState.UNSPECIFIED);
const proxyIsRunning = computed(() => proxyState.value === ProxyState.RUNNING || status.value?.proxyRunning === true);
const proxyError = computed(() => status.value?.proxy?.lastError || status.value?.proxyLastError || "");

const proxySeverity = computed(() => {
  if (proxyState.value === ProxyState.RUNNING) return "success";
  if (proxyState.value === ProxyState.STARTING || proxyState.value === ProxyState.STOPPING) return "warn";
  return "danger";
});

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
</script>

<template>
  <div v-if="dashboard" class="space-y-8">
    <div>
      <h3 class="text-xl font-bold mb-2">Proxy Control</h3>
      <p class="text-[#888] text-sm">Manage the lifecycle of the public proxy listener.</p>
    </div>

    <div class="vercel-card p-8 flex flex-col md:flex-row md:items-center justify-between gap-6">
      <div>
        <div class="flex items-center gap-3 mb-2">
          <span class="text-lg font-semibold">Public Listener</span>
          <Tag :severity="proxySeverity" :value="proxyStateLabel(proxyState)" class="!bg-[#111] !border-[#333] !text-white" />
        </div>
        <p class="text-[#888] text-sm max-w-md">
          When started, the proxy will listen on the configured port and forward traffic to the connected agent.
        </p>
        <p v-if="proxyError" class="mt-4 text-red-500 text-sm font-medium">{{ proxyError }}</p>
      </div>

      <div class="flex items-center gap-3">
        <Button
          v-if="!proxyIsRunning"
          label="Start Proxy"
          class="!bg-white !text-black !border-white px-8"
          :loading="isBusy && !proxyIsRunning"
          :disabled="isBusy"
          @click="setProxyRunning?.(true)"
        >
          <template #icon><PlusIcon class="h-4 w-4" /></template>
        </Button>
        <DangerButton
          v-else
          label="Stop Proxy"
          class="px-8"
          :loading="isBusy && proxyIsRunning"
          :disabled="isBusy"
          @click="setProxyRunning?.(false)"
        >
          <template #icon><BanIcon class="h-4 w-4" /></template>
        </DangerButton>
      </div>
    </div>
    
    <div class="pt-12 border-t border-[#333]">
       <h4 class="text-sm font-semibold text-red-500 uppercase tracking-widest mb-4">Danger Zone</h4>
       <div class="vercel-card border-red-900/50 p-6 flex items-center justify-between">
          <div>
             <p class="font-medium">Reset Session</p>
             <p class="text-sm text-[#888]">This will log you out and clear current dashboard state.</p>
          </div>
          <SecondaryButton label="Log out" class="!border-red-900/50 !text-red-500" @click="logout?.()" />
       </div>
    </div>
  </div>
</template>
