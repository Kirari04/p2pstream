<script setup lang="ts">
import CheckIcon from "@primevue/icons/check";
import PencilIcon from "@primevue/icons/pencil";
import PlusIcon from "@primevue/icons/plus";
import RefreshIcon from "@primevue/icons/refresh";
import TrashIcon from "@primevue/icons/trash";
import { computed, inject, onMounted, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import { localManagementClient } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import Tag from "@/volt/Tag.vue";
import type {
  Agent,
  Environment,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  EnvironmentTransport,
  EnvironmentTrustState,
} from "@/gen/proto/p2pstream/v1/management_pb";

const environments = inject<ComputedRef<Environment[]>>("environments", computed(() => []));
const reloadEnvironments = inject<(() => Promise<void>) | undefined>("reloadEnvironments", undefined);
const isBusy = inject<ComputedRef<boolean>>("isBusy", computed(() => false));

const localAgents = ref<Agent[]>([]);
const isLoading = ref(false);
const isEnvironmentModalOpen = ref(false);
const operationError = ref("");

const environmentForm = reactive({
  id: "",
  name: "",
  managementUrl: "",
  transport: EnvironmentTransport.DIRECT,
  agentId: "0",
  accessToken: "",
  responseHeaderTimeoutMillis: 10000,
  enabled: true,
});

const busyDisabledReason = computed(() => isBusy.value || isLoading.value ? BUSY_REASON : "");
const enabledLocalAgents = computed(() => localAgents.value.filter((agent) => agent.enabled));

onMounted(() => {
  void refreshLocalData();
});

async function refreshLocalData() {
  isLoading.value = true;
  operationError.value = "";
  try {
    await Promise.all([reloadEnvironments?.(), loadLocalAgents()]);
  } catch (err) {
    operationError.value = messageFromError(err);
  } finally {
    isLoading.value = false;
  }
}

async function loadLocalAgents() {
  const resp = await localManagementClient.getPublicProxyConfig({});
  localAgents.value = resp.agents;
}

function openCreateEnvironment() {
  environmentForm.id = "";
  environmentForm.name = "";
  environmentForm.managementUrl = "";
  environmentForm.transport = EnvironmentTransport.DIRECT;
  environmentForm.agentId = "0";
  environmentForm.accessToken = "";
  environmentForm.responseHeaderTimeoutMillis = 10000;
  environmentForm.enabled = true;
  operationError.value = "";
  isEnvironmentModalOpen.value = true;
}

function openEditEnvironment(environment: Environment) {
  environmentForm.id = environment.id.toString();
  environmentForm.name = environment.name;
  environmentForm.managementUrl = environment.managementUrl;
  environmentForm.transport = environment.transport || EnvironmentTransport.DIRECT;
  environmentForm.agentId = environment.agentId.toString();
  environmentForm.accessToken = "";
  environmentForm.responseHeaderTimeoutMillis = Number(environment.responseHeaderTimeoutMillis || 10000n);
  environmentForm.enabled = environment.enabled;
  operationError.value = "";
  isEnvironmentModalOpen.value = true;
}

async function submitEnvironment() {
  await runLocalAction(async () => {
    const payload = {
      name: environmentForm.name,
      managementUrl: environmentForm.managementUrl,
      transport: environmentForm.transport,
      agentId: BigInt(environmentForm.transport === EnvironmentTransport.AGENT ? environmentForm.agentId || "0" : "0"),
      accessToken: environmentForm.accessToken,
      responseHeaderTimeoutMillis: BigInt(environmentForm.responseHeaderTimeoutMillis),
      enabled: environmentForm.enabled,
    };
    if (environmentForm.id) {
      await localManagementClient.updateEnvironment({ id: BigInt(environmentForm.id), ...payload });
    } else {
      await localManagementClient.createEnvironment(payload);
    }
    isEnvironmentModalOpen.value = false;
    await reloadEnvironments?.();
  });
}

async function deleteEnvironment(environment: Environment) {
  if (!window.confirm(`Delete environment "${environment.name}"?`)) return;
  await runLocalAction(async () => {
    await localManagementClient.deleteEnvironment({ id: environment.id });
    await reloadEnvironments?.();
  });
}

async function discoverCertificate(environment: Environment) {
  await runLocalAction(async () => {
    await localManagementClient.discoverEnvironmentCertificate({ id: environment.id });
    await reloadEnvironments?.();
  });
}

async function trustCertificate(environment: Environment) {
  const fingerprint = environment.observedCertificate?.sha256Fingerprint ?? "";
  if (!fingerprint) return;
  if (!window.confirm(`Trust certificate ${fingerprint} for "${environment.name}"?`)) return;
  await runLocalAction(async () => {
    await localManagementClient.trustEnvironmentCertificate({ id: environment.id, sha256Fingerprint: fingerprint });
    await reloadEnvironments?.();
  });
}

async function testEnvironment(environment: Environment) {
  await runLocalAction(async () => {
    await localManagementClient.testEnvironment({ id: environment.id });
    await reloadEnvironments?.();
  });
}

async function runLocalAction(action: () => Promise<void>) {
  if (isLoading.value) return;
  isLoading.value = true;
  operationError.value = "";
  try {
    await action();
  } catch (err) {
    operationError.value = messageFromError(err);
  } finally {
    isLoading.value = false;
  }
}

function trustLabel(state: EnvironmentTrustState): string {
  switch (state) {
    case EnvironmentTrustState.TRUSTED:
      return "Trusted";
    case EnvironmentTrustState.CHANGED:
      return "Changed";
    case EnvironmentTrustState.EXPIRED:
      return "Expired";
    default:
      return "Untrusted";
  }
}

function trustSeverity(state: EnvironmentTrustState): "success" | "warn" | "danger" {
  if (state === EnvironmentTrustState.TRUSTED) return "success";
  if (state === EnvironmentTrustState.CHANGED || state === EnvironmentTrustState.EXPIRED) return "danger";
  return "warn";
}

function transportLabel(transport: EnvironmentTransport): string {
  return transport === EnvironmentTransport.AGENT ? "Agent" : "Direct";
}

function formatDate(value: bigint | undefined): string {
  if (!value || value === 0n) return "Never";
  return new Date(Number(value)).toLocaleString();
}

function messageFromError(err: unknown): string {
  return err instanceof Error ? err.message : "Request failed";
}
</script>

<template>
  <div class="space-y-8">
    <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
      <div>
        <h4 class="mb-2 text-lg font-semibold text-white">Environments</h4>
        <p class="text-sm text-[#888]">Remote management endpoints, routing, and certificate trust.</p>
      </div>
      <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
        <SecondaryButton size="small" label="Add Environment" :disabled="Boolean(busyDisabledReason)" @click="openCreateEnvironment">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </DisabledHint>
    </div>

    <div v-if="operationError" class="rounded-md border border-red-900/60 bg-black p-4 text-sm text-red-400">
      {{ operationError }}
    </div>

    <section class="vercel-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4">
        <h5 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Registered Environments</h5>
      </div>
      <div class="overflow-x-auto">
        <table class="w-full min-w-[1120px] text-sm">
          <thead class="border-b border-[#333] text-left text-xs uppercase tracking-wider text-[#888]">
            <tr>
              <th class="px-5 py-3">Environment</th>
              <th class="px-5 py-3">Transport</th>
              <th class="px-5 py-3">Trust</th>
              <th class="px-5 py-3">Certificate</th>
              <th class="px-5 py-3">Last Check</th>
              <th class="px-5 py-3 text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="environment in environments" :key="environment.id.toString()" class="border-b border-[#1f1f1f] last:border-0">
              <td class="px-5 py-4">
                <p class="font-medium text-white">{{ environment.name }}</p>
                <p class="max-w-md truncate font-mono text-xs text-[#888]">{{ environment.managementUrl }}</p>
                <div class="mt-2 flex gap-2">
                  <Tag v-if="!environment.enabled" value="Disabled" severity="warn" />
                  <Tag v-if="environment.accessTokenConfigured" value="Token" severity="secondary" />
                </div>
              </td>
              <td class="px-5 py-4">
                <p class="text-[#d4d4d8]">{{ transportLabel(environment.transport) }}</p>
                <p v-if="environment.transport === EnvironmentTransport.AGENT" class="font-mono text-xs text-[#888]">
                  {{ environment.agentName || `agent #${environment.agentId.toString()}` }}
                  <span :class="environment.agentConnected ? 'text-green-400' : 'text-amber-400'">
                    {{ environment.agentConnected ? 'connected' : 'offline' }}
                  </span>
                </p>
              </td>
              <td class="px-5 py-4">
                <Tag :value="trustLabel(environment.trustState)" :severity="trustSeverity(environment.trustState)" />
              </td>
              <td class="px-5 py-4">
                <p class="font-mono text-xs text-[#d4d4d8]">
                  {{ environment.observedCertificate?.sha256Fingerprint || environment.trustedCertificate?.sha256Fingerprint || '-' }}
                </p>
                <p class="mt-1 text-xs text-[#888]">
                  Expires {{ formatDate(environment.trustedCertificate?.notAfterUnixMillis || environment.observedCertificate?.notAfterUnixMillis) }}
                </p>
              </td>
              <td class="px-5 py-4">
                <p class="font-mono text-xs text-[#d4d4d8]">{{ formatDate(environment.lastCheckedAtUnixMillis) }}</p>
                <p v-if="environment.lastError" class="mt-1 max-w-xs truncate text-xs text-red-400">{{ environment.lastError }}</p>
              </td>
              <td class="px-5 py-4">
                <div class="flex justify-end gap-2">
                  <SecondaryButton size="small" aria-label="Discover certificate" title="Discover certificate" :disabled="Boolean(busyDisabledReason)" @click="discoverCertificate(environment)">
                    <template #icon><RefreshIcon class="h-3.5 w-3.5" /></template>
                  </SecondaryButton>
                  <SecondaryButton size="small" aria-label="Trust certificate" title="Trust certificate" :disabled="Boolean(busyDisabledReason) || !environment.observedCertificate?.sha256Fingerprint" @click="trustCertificate(environment)">
                    <template #icon><CheckIcon class="h-3.5 w-3.5" /></template>
                  </SecondaryButton>
                  <SecondaryButton size="small" label="Test" :disabled="Boolean(busyDisabledReason) || environment.trustState !== EnvironmentTrustState.TRUSTED" @click="testEnvironment(environment)" />
                  <SecondaryButton size="small" aria-label="Edit environment" title="Edit environment" :disabled="Boolean(busyDisabledReason)" @click="openEditEnvironment(environment)">
                    <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
                  </SecondaryButton>
                  <DangerButton size="small" aria-label="Delete environment" title="Delete environment" :disabled="Boolean(busyDisabledReason)" @click="deleteEnvironment(environment)">
                    <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
                  </DangerButton>
                </div>
              </td>
            </tr>
            <tr v-if="!environments.length">
              <td colspan="6" class="px-5 py-10 text-center text-sm text-[#888]">No remote environments registered.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <Modal v-model="isEnvironmentModalOpen" :title="environmentForm.id ? 'Edit Environment' : 'Add Environment'" max-width="42rem">
      <form class="grid gap-4" @submit.prevent="submitEnvironment">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Name
          <input v-model="environmentForm.name" class="vercel-input text-sm normal-case tracking-normal" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Management URL
          <input v-model="environmentForm.managementUrl" class="vercel-input text-sm normal-case tracking-normal" placeholder="https://proxy.example.com:8081" required />
        </label>
        <div class="grid gap-2 text-xs font-medium uppercase tracking-wider text-[#888]">
          Transport
          <div class="inline-flex w-fit rounded-md border border-[#333] bg-[#050505] p-1">
            <button
              type="button"
              class="rounded px-3 py-1.5 text-sm normal-case tracking-normal"
              :class="environmentForm.transport === EnvironmentTransport.DIRECT ? 'bg-white text-black' : 'text-[#888]'"
              @click="environmentForm.transport = EnvironmentTransport.DIRECT"
            >
              Direct
            </button>
            <button
              type="button"
              class="rounded px-3 py-1.5 text-sm normal-case tracking-normal"
              :class="environmentForm.transport === EnvironmentTransport.AGENT ? 'bg-white text-black' : 'text-[#888]'"
              @click="environmentForm.transport = EnvironmentTransport.AGENT"
            >
              Agent
            </button>
          </div>
        </div>
        <label v-if="environmentForm.transport === EnvironmentTransport.AGENT" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Local Agent
          <select v-model="environmentForm.agentId" class="vercel-input text-sm normal-case tracking-normal" required>
            <option value="0" disabled>Select agent</option>
            <option v-for="agent in enabledLocalAgents" :key="agent.id.toString()" :value="agent.id.toString()">
              {{ agent.name }}{{ agent.connected ? '' : ' (offline)' }}
            </option>
          </select>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Access Token
          <input
            v-model="environmentForm.accessToken"
            class="vercel-input text-sm normal-case tracking-normal"
            :placeholder="environmentForm.id ? 'Leave blank to keep existing token' : 'p2pat_...'"
            :required="!environmentForm.id"
          />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Response Header Timeout
          <input v-model.number="environmentForm.responseHeaderTimeoutMillis" type="number" min="1000" max="300000" class="vercel-input text-sm normal-case tracking-normal" required />
        </label>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
          <input v-model="environmentForm.enabled" type="checkbox" />
          Enabled
        </label>
        <div class="mt-4 flex justify-end gap-3">
          <SecondaryButton type="button" label="Cancel" @click="isEnvironmentModalOpen = false" />
          <Button :label="environmentForm.id ? 'Save Changes' : 'Create Environment'" type="submit" :disabled="Boolean(busyDisabledReason)" />
        </div>
      </form>
    </Modal>
  </div>
</template>
