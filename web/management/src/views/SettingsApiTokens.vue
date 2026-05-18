<script setup lang="ts">
import RefreshIcon from "@primevue/icons/refresh";
import TrashIcon from "@primevue/icons/trash";
import { computed, inject, onMounted, reactive, ref, watch } from "vue";
import type { ComputedRef } from "vue";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import Tag from "@/volt/Tag.vue";
import type { ManagementAccessToken } from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();
const isBusy = inject<ComputedRef<boolean>>("isBusy", computed(() => false));
const selectedEnvironmentId = inject<ComputedRef<string>>("selectedEnvironmentId", computed(() => "0"));
const selectedEnvironmentLabel = inject<ComputedRef<string>>("selectedEnvironmentLabel", computed(() => "Local"));
const selectedEnvironmentBlocked = inject<ComputedRef<string>>("selectedEnvironmentBlocked", computed(() => ""));

const tokens = ref<ManagementAccessToken[]>([]);
const isLoading = ref(false);
const issuedToken = ref("");
const operationError = ref("");

const tokenForm = reactive({
  name: "",
  expiresAt: "",
  enabled: true,
});

const actionDisabledReason = computed(() => {
  if (selectedEnvironmentBlocked.value) return selectedEnvironmentBlocked.value;
  if (isBusy.value || isLoading.value) return BUSY_REASON;
  return "";
});

onMounted(() => {
  void refreshTokens();
});

watch([selectedEnvironmentId, selectedEnvironmentBlocked], () => {
  issuedToken.value = "";
  operationError.value = "";
  tokens.value = [];
  void refreshTokens();
});

async function refreshTokens() {
  if (isLoading.value) return;
  isLoading.value = true;
  operationError.value = "";
  try {
    await loadTokens();
  } catch (err) {
    operationError.value = messageFromError(err);
  } finally {
    isLoading.value = false;
  }
}

async function loadTokens() {
  if (selectedEnvironmentBlocked.value) {
    tokens.value = [];
    return;
  }
  const resp = await managementClient.listManagementAccessTokens({});
  tokens.value = resp.accessTokens;
}

async function createToken() {
  await runTokenAction(async () => {
    const resp = await managementClient.createManagementAccessToken({
      name: tokenForm.name,
      enabled: tokenForm.enabled,
      expiresAtUnixMillis: tokenExpiryMillis(),
    });
    issuedToken.value = resp.token;
    tokenForm.name = "";
    tokenForm.expiresAt = "";
    tokenForm.enabled = true;
    await loadTokens();
  });
}

async function deleteToken(token: ManagementAccessToken) {
  if (!window.confirm(`Revoke API token "${token.name}"?`)) return;
  await runTokenAction(async () => {
    await managementClient.deleteManagementAccessToken({ id: token.id });
    await loadTokens();
  });
}

async function runTokenAction(action: () => Promise<void>) {
  if (isLoading.value || selectedEnvironmentBlocked.value) return;
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

function tokenExpiryMillis(): bigint {
  if (!tokenForm.expiresAt) return 0n;
  const millis = new Date(tokenForm.expiresAt).getTime();
  if (!Number.isFinite(millis)) {
    throw new Error("Expiry date is invalid.");
  }
  return BigInt(millis);
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
        <h4 class="mb-2 text-lg font-semibold text-white">API Tokens</h4>
        <p class="text-sm text-[#888]">Admin API credentials for {{ selectedEnvironmentLabel }}.</p>
      </div>
      <DisabledHint :disabled="Boolean(actionDisabledReason)" :reason="actionDisabledReason">
        <SecondaryButton
          size="small"
          aria-label="Refresh API tokens"
          title="Refresh API tokens"
          :disabled="Boolean(actionDisabledReason)"
          :loading="isLoading"
          @click="refreshTokens"
        >
          <template #icon><RefreshIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </DisabledHint>
    </div>

    <div v-if="selectedEnvironmentBlocked" class="rounded-md border border-amber-900/60 bg-black p-4 text-sm text-amber-300">
      {{ selectedEnvironmentBlocked }}
    </div>
    <div v-if="operationError" class="rounded-md border border-red-900/60 bg-black p-4 text-sm text-red-400">
      {{ operationError }}
    </div>

    <section class="grid gap-6 lg:grid-cols-[1fr_1.25fr]">
      <div class="vercel-card p-5">
        <h5 class="mb-4 text-sm font-semibold uppercase tracking-widest text-[#888]">Create API Token</h5>
        <form class="grid gap-4" @submit.prevent="createToken">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Name
            <input v-model="tokenForm.name" class="vercel-input text-sm normal-case tracking-normal" required :disabled="Boolean(actionDisabledReason)" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Expires
            <input v-model="tokenForm.expiresAt" type="datetime-local" class="vercel-input text-sm normal-case tracking-normal" :disabled="Boolean(actionDisabledReason)" />
          </label>
          <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
            <input v-model="tokenForm.enabled" type="checkbox" :disabled="Boolean(actionDisabledReason)" />
            Enabled
          </label>
          <DisabledHint :disabled="Boolean(actionDisabledReason)" :reason="actionDisabledReason">
            <Button label="Create Token" type="submit" :disabled="Boolean(actionDisabledReason)" />
          </DisabledHint>
        </form>
        <div v-if="issuedToken" class="mt-5 rounded-md border border-[#333] bg-[#050505] p-3">
          <p class="mb-2 text-xs uppercase tracking-wider text-[#888]">Issued Token</p>
          <code class="block break-all font-mono text-xs text-white">{{ issuedToken }}</code>
        </div>
      </div>

      <div class="vercel-card overflow-hidden">
        <div class="border-b border-[#333] px-5 py-4">
          <h5 class="text-sm font-semibold uppercase tracking-widest text-[#888]">API Tokens</h5>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full min-w-[760px] text-sm">
            <thead class="border-b border-[#333] text-left text-xs uppercase tracking-wider text-[#888]">
              <tr>
                <th class="px-5 py-3">Name</th>
                <th class="px-5 py-3">Expires</th>
                <th class="px-5 py-3">Last Used</th>
                <th class="px-5 py-3">Status</th>
                <th class="px-5 py-3 text-right">Actions</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="token in tokens" :key="token.id.toString()" class="border-b border-[#1f1f1f] last:border-0">
                <td class="px-5 py-4">
                  <p class="font-medium text-white">{{ token.name }}</p>
                </td>
                <td class="px-5 py-4 font-mono text-xs text-[#d4d4d8]">{{ formatDate(token.expiresAtUnixMillis) }}</td>
                <td class="px-5 py-4 font-mono text-xs text-[#d4d4d8]">{{ formatDate(token.lastUsedAtUnixMillis) }}</td>
                <td class="px-5 py-4">
                  <Tag :value="token.enabled ? 'Enabled' : 'Disabled'" :severity="token.enabled ? 'success' : 'warn'" />
                </td>
                <td class="px-5 py-4 text-right">
                  <DisabledHint :disabled="Boolean(actionDisabledReason)" :reason="actionDisabledReason">
                    <DangerButton size="small" aria-label="Revoke token" title="Revoke token" :disabled="Boolean(actionDisabledReason)" @click="deleteToken(token)">
                      <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
                    </DangerButton>
                  </DisabledHint>
                </td>
              </tr>
              <tr v-if="!tokens.length">
                <td colspan="5" class="px-5 py-10 text-center text-sm text-[#888]">No API tokens issued.</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>
  </div>
</template>
