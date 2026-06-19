<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { useManagementClient } from "@/composables/useManagementClient";
import ConfirmDialog from "@/components/ConfirmDialog.vue";
import EmptyState from "@/components/EmptyState.vue";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementContext } from "@/composables/useManagementContext";
import { BUSY_REASON } from "@/lib/disabledReasons";
import {
  bytesToKiB,
  bytesToMiB,
  cacheQueryModeLabel,
  cacheRuleMatchSummary,
  cacheRuleSummary,
  cacheScopeLabel,
  cacheTtlModeLabel,
  kiBToBytes,
  miBToBytes,
  publicPolicyMatchSummary,
  rateLimitAlgorithmLabel,
  rateLimitKeySummary,
  rateLimitRuleSummary,
  trafficShaperBudgetSummary,
  trafficShaperKeySummary,
  trafficShaperRuleSummary,
  trafficShaperScopeLabel,
  wafActionLabel,
  wafActivationLabel,
  wafProviderLabel,
  wafRuleSummary,
} from "@/lib/publicProxyLabels";
import Button from "@/components/ui/Button.vue";
import DangerButton from "@/components/ui/DangerButton.vue";
import SecondaryButton from "@/components/ui/SecondaryButton.vue";
import Tag from "@/components/ui/Tag.vue";
import type { PublicWafCaptchaProvider } from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

const {
  dashboard,
  publicProxyConfig,
  isBusy,
  runManagementAction,
} = useManagementContext();

const config = computed(() => publicProxyConfig.value ?? null);
const rateLimitRules = computed(() => config.value?.rateLimitRules ?? []);
const cacheRules = computed(() => config.value?.cacheRules ?? []);
const cacheSettings = computed(() => config.value?.cacheSettings ?? null);
const wafRules = computed(() => config.value?.wafRules ?? []);
const wafCaptchaProviders = computed(() => config.value?.wafCaptchaProviders ?? []);
const trafficShaperRules = computed(() => config.value?.trafficShaperRules ?? []);
const enabledRateLimitRules = computed(() => rateLimitRules.value.filter((rule) => rule.enabled).length);
const enabledWafRules = computed(() => wafRules.value.filter((rule) => rule.enabled).length);
const enabledCacheRules = computed(() => cacheRules.value.filter((rule) => rule.enabled).length);
const enabledTrafficShapers = computed(() => trafficShaperRules.value.filter((rule) => rule.enabled).length);

const editorHost = ref<InstanceType<typeof PublicProxyEditorHost> | null>(null);
const { state: confirmState, confirm, handleConfirm: onConfirm, handleCancel: onCancel } = useConfirmDialog();

const cacheSettingsForm = reactive({
  enabled: true,
  maxDiskMiB: 1024,
  maxMemoryMiB: 128,
  hotObjectKiB: 256,
  maxEntries: 100000,
  cleanupIntervalSeconds: 60,
});

const summaryCards = computed(() => [
  { label: "Rate Limits", value: enabledRateLimitRules.value.toString(), detail: `${rateLimitRules.value.length.toString()} total rules` },
  { label: "WAF", value: enabledWafRules.value.toString(), detail: `${wafCaptchaProviders.value.length.toString()} captcha providers` },
  { label: "Cache", value: enabledCacheRules.value.toString(), detail: cacheSettingsForm.enabled ? "storage enabled" : "storage disabled" },
  { label: "Traffic Shapers", value: enabledTrafficShapers.value.toString(), detail: `${trafficShaperRules.value.length.toString()} total shapers` },
]);

const cacheSettingsDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  if (cacheSettingsForm.maxDiskMiB < 1) return "Disk budget must be at least 1 MiB.";
  if (cacheSettingsForm.maxMemoryMiB < 1) return "Memory budget must be at least 1 MiB.";
  if (cacheSettingsForm.hotObjectKiB < 1) return "Hot-object limit must be at least 1 KiB.";
  if (cacheSettingsForm.hotObjectKiB > cacheSettingsForm.maxMemoryMiB * 1024) return "Hot-object limit cannot exceed memory budget.";
  if (cacheSettingsForm.maxEntries < 1) return "Max entries must be at least 1.";
  if (cacheSettingsForm.cleanupIntervalSeconds < 1 || cacheSettingsForm.cleanupIntervalSeconds > 3600) return "Cleanup interval must be between 1 and 3600 seconds.";
  return "";
});

watch(cacheSettings, (settings) => {
  cacheSettingsForm.enabled = settings?.enabled ?? true;
  cacheSettingsForm.maxDiskMiB = bytesToMiB(settings?.maxDiskBytes ?? 1073741824n);
  cacheSettingsForm.maxMemoryMiB = bytesToMiB(settings?.maxMemoryBytes ?? 134217728n);
  cacheSettingsForm.hotObjectKiB = bytesToKiB(settings?.memoryHotObjectMaxBytes ?? 262144n);
  cacheSettingsForm.maxEntries = Number(settings?.maxEntries ?? 100000n);
  cacheSettingsForm.cleanupIntervalSeconds = Math.max(1, Math.round(Number(settings?.cleanupIntervalMillis ?? 60000n) / 1000));
}, { immediate: true });

async function run(action: () => Promise<void>) {
  if (!runManagementAction) return;
  await runManagementAction(action);
}

function openAddRateLimitRuleModal() {
  editorHost.value?.openCreateRateLimitRule();
}

function editRateLimitRule(id: bigint) {
  editorHost.value?.openRateLimitRule(id);
}

function openAddWafRuleModal() {
  editorHost.value?.openCreateWafRule();
}

function editWafRule(id: bigint) {
  editorHost.value?.openWafRule(id);
}

function openAddWafCaptchaProviderModal() {
  editorHost.value?.openCreateWafCaptchaProvider();
}

function editWafCaptchaProvider(provider: PublicWafCaptchaProvider) {
  editorHost.value?.openWafCaptchaProvider(provider.id);
}

function openAddCacheRuleModal() {
  editorHost.value?.openCreateCacheRule();
}

function editCacheRule(id: bigint) {
  editorHost.value?.openCacheRule(id);
}

function openAddTrafficShaperRuleModal() {
  editorHost.value?.openCreateTrafficShaperRule();
}

function editTrafficShaperRule(id: bigint) {
  editorHost.value?.openTrafficShaperRule(id);
}

async function deleteRateLimitRule(id: bigint) {
  if (!await confirm("Delete Rate Limit Rule", "This rate-limit rule will be permanently removed.")) return;
  await run(async () => {
    await managementClient.deletePublicRateLimitRule({ id });
  });
}

async function deleteWafRule(id: bigint) {
  if (!await confirm("Delete WAF Rule", "This WAF rule will be permanently removed.")) return;
  await run(async () => {
    await managementClient.deletePublicWafRule({ id });
  });
}

async function deleteWafCaptchaProvider(id: bigint) {
  if (!await confirm("Delete Captcha Provider", "This captcha provider will be permanently removed. Captcha rules using it must be updated first.")) return;
  await run(async () => {
    await managementClient.deletePublicWafCaptchaProvider({ id });
  });
}

async function deleteCacheRule(id: bigint) {
  if (!await confirm("Delete Cache Rule", "This cache rule will be permanently removed. Existing cached objects for the rule may be purged separately.")) return;
  await run(async () => {
    await managementClient.deletePublicCacheRule({ id });
  });
}

async function purgeAllCache() {
  if (!await confirm("Purge Cache", "All cached public proxy objects will be removed from the proxy cache.")) return;
  await run(async () => {
    await managementClient.purgePublicCache({ all: true });
  });
}

async function saveCacheSettings() {
  if (cacheSettingsDisabledReason.value) return;
  await run(async () => {
    await managementClient.updatePublicCacheSettings({
      enabled: cacheSettingsForm.enabled,
      maxDiskBytes: miBToBytes(cacheSettingsForm.maxDiskMiB),
      maxMemoryBytes: miBToBytes(cacheSettingsForm.maxMemoryMiB),
      memoryHotObjectMaxBytes: kiBToBytes(cacheSettingsForm.hotObjectKiB),
      maxEntries: BigInt(Math.max(1, Math.round(cacheSettingsForm.maxEntries || 0))),
      cleanupIntervalMillis: BigInt(Math.max(1, Math.round(cacheSettingsForm.cleanupIntervalSeconds || 0)) * 1000),
    });
  });
}

async function deleteTrafficShaperRule(id: bigint) {
  if (!await confirm("Delete Traffic Shaper Rule", "This traffic-shaper rule will be permanently removed.")) return;
  await run(async () => {
    await managementClient.deletePublicTrafficShaperRule({ id });
  });
}
</script>

<template>
  <div v-if="dashboard" class="space-y-8">
    <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
      <div>
        <h3 class="mb-2 text-xl font-bold">Traffic Policy</h3>
        <p class="text-sm text-[#888]">Rate limits, WAF rules, cache behavior, and bandwidth shaping.</p>
      </div>
    </div>

    <section class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div v-for="card in summaryCards" :key="card.label" class="app-card p-4">
        <p class="text-xs font-semibold uppercase tracking-widest text-[#666]">{{ card.label }}</p>
        <p class="mt-2 text-2xl font-semibold text-white">{{ card.value }}</p>
        <p class="mt-1 text-xs text-[#777]">{{ card.detail }}</p>
      </div>
    </section>

    <section class="app-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Rate Limits</h4>
          <p class="mt-0.5 text-xs text-[#666] normal-case tracking-normal">Throttle traffic based on request rate per client or route.</p>
        </div>
        <SecondaryButton size="small" label="Add Rule" @click="openAddRateLimitRuleModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div v-for="rule in rateLimitRules" :key="rule.id.toString()" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ rule.name }}</p>
              <Tag :value="rateLimitAlgorithmLabel(rule.algorithm)" severity="info" />
              <Tag v-if="!rule.enabled" value="Disabled" severity="warn" />
              <Tag :value="`P${rule.priority.toString()}`" severity="info" />
            </div>
            <p class="mt-1 truncate font-mono text-xs text-[#888]">{{ rateLimitRuleSummary(rule) }} / key {{ rateLimitKeySummary(rule) }}</p>
            <p class="mt-1 truncate text-xs text-[#666]">{{ publicPolicyMatchSummary(rule) }} / response {{ rule.responseStatusCode.toString() }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <SecondaryButton size="small" aria-label="Edit rate-limit rule" title="Edit rate-limit rule" @click="editRateLimitRule(rule.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <DangerButton size="small" aria-label="Delete rate-limit rule" title="Delete rate-limit rule" @click="deleteRateLimitRule(rule.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
        <EmptyState
          v-if="!rateLimitRules.length"
          title="No rate-limit rules configured"
          description="Rate limits protect your route targets from excessive traffic by throttling requests per client or route."
          action-label="Add Rule"
          @action="openAddRateLimitRuleModal"
        />
      </div>
    </section>

    <section class="app-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">WAF</h4>
          <p class="mt-0.5 text-xs text-[#666] normal-case tracking-normal">Block, challenge, or queue matching application traffic before it reaches routes.</p>
        </div>
        <div class="flex flex-wrap gap-2">
          <SecondaryButton size="small" label="Add Provider" @click="openAddWafCaptchaProviderModal">
            <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          </SecondaryButton>
          <SecondaryButton size="small" label="Add Rule" @click="openAddWafRuleModal">
            <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          </SecondaryButton>
        </div>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div v-for="provider in wafCaptchaProviders" :key="`provider-${provider.id.toString()}`" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ provider.name }}</p>
              <Tag :value="wafProviderLabel(provider.providerType)" severity="info" />
              <Tag :value="provider.secretKeySet ? 'Secret saved' : 'Secret missing'" :severity="provider.secretKeySet ? 'success' : 'danger'" />
              <Tag v-if="!provider.enabled" value="Disabled" severity="warn" />
            </div>
            <p class="mt-1 truncate font-mono text-xs text-[#888]">{{ provider.siteKey }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <SecondaryButton size="small" aria-label="Edit captcha provider" title="Edit captcha provider" @click="editWafCaptchaProvider(provider)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <DangerButton size="small" aria-label="Delete captcha provider" title="Delete captcha provider" @click="deleteWafCaptchaProvider(provider.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
        <div v-for="rule in wafRules" :key="`rule-${rule.id.toString()}`" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ rule.name }}</p>
              <Tag :value="wafActionLabel(rule.action)" severity="info" />
              <Tag :value="wafActivationLabel(rule.activationMode)" severity="info" />
              <Tag v-if="!rule.enabled" value="Disabled" severity="warn" />
              <Tag :value="`P${rule.priority.toString()}`" severity="info" />
            </div>
            <p class="mt-1 truncate font-mono text-xs text-[#888]">{{ wafRuleSummary(rule, wafCaptchaProviders) }} / key {{ rateLimitKeySummary(rule) }}</p>
            <p class="mt-1 truncate text-xs text-[#666]">{{ publicPolicyMatchSummary(rule) }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <SecondaryButton size="small" aria-label="Edit WAF rule" title="Edit WAF rule" @click="editWafRule(rule.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <DangerButton size="small" aria-label="Delete WAF rule" title="Delete WAF rule" @click="deleteWafRule(rule.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
        <EmptyState
          v-if="!wafRules.length && !wafCaptchaProviders.length"
          title="No WAF policy configured"
          description="WAF rules can block, challenge, or queue selected traffic before rate limits, shapers, routes, and targets."
          action-label="Add Rule"
          @action="openAddWafRuleModal"
        />
      </div>
    </section>

    <section class="app-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Cache</h4>
          <p class="mt-0.5 text-xs text-[#666] normal-case tracking-normal">Cache public static files on the proxy after routing while keeping WAF, rate limits, and shaping active.</p>
        </div>
        <div class="flex flex-wrap gap-2">
          <SecondaryButton size="small" label="Purge All" @click="purgeAllCache">
            <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
          </SecondaryButton>
          <SecondaryButton size="small" label="Add Rule" @click="openAddCacheRuleModal">
            <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          </SecondaryButton>
        </div>
      </div>
      <div class="grid gap-4 border-b border-[#222] bg-[#050505] px-5 py-4">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h5 class="text-xs font-semibold uppercase tracking-widest text-[#888]">Cache Settings</h5>
            <p class="mt-1 text-xs text-[#666]">Bodies are stored under the configured public cache directory; metadata stays in SQLite.</p>
          </div>
          <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
            <input v-model="cacheSettingsForm.enabled" type="checkbox" />
            Enabled
          </label>
        </div>
        <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Disk MiB
            <input v-model.number="cacheSettingsForm.maxDiskMiB" type="number" min="1" class="app-control text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Memory MiB
            <input v-model.number="cacheSettingsForm.maxMemoryMiB" type="number" min="1" class="app-control text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Hot object KiB
            <input v-model.number="cacheSettingsForm.hotObjectKiB" type="number" min="1" class="app-control text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Max entries
            <input v-model.number="cacheSettingsForm.maxEntries" type="number" min="1" class="app-control text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Cleanup seconds
            <input v-model.number="cacheSettingsForm.cleanupIntervalSeconds" type="number" min="1" max="3600" class="app-control text-sm normal-case tracking-normal" />
          </label>
        </div>
        <div class="flex justify-end">
          <Button size="small" label="Save Settings" :disabled="Boolean(cacheSettingsDisabledReason)" :title="cacheSettingsDisabledReason" @click="saveCacheSettings" />
        </div>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div v-for="rule in cacheRules" :key="rule.id.toString()" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ rule.name }}</p>
              <Tag :value="cacheTtlModeLabel(rule.ttlMode)" severity="info" />
              <Tag :value="cacheScopeLabel(rule.scope)" severity="info" />
              <Tag :value="rule.allowCookieRequests ? 'Cookies allowed' : 'Cookies blocked'" :severity="rule.allowCookieRequests ? 'warn' : 'info'" />
              <Tag v-if="!rule.enabled" value="Disabled" severity="warn" />
              <Tag :value="`P${rule.priority.toString()}`" severity="info" />
            </div>
            <p class="mt-1 truncate font-mono text-xs text-[#888]">{{ cacheRuleSummary(rule) }} / {{ cacheQueryModeLabel(rule.queryMode) }}</p>
            <p class="mt-1 truncate text-xs text-[#666]">{{ cacheRuleMatchSummary(rule) }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <SecondaryButton size="small" aria-label="Edit cache rule" title="Edit cache rule" @click="editCacheRule(rule.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <DangerButton size="small" aria-label="Delete cache rule" title="Delete cache rule" @click="deleteCacheRule(rule.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
        <EmptyState
          v-if="!cacheRules.length"
          title="No cache rules configured"
          description="Cache rules store public GET assets such as CSS, JavaScript, images, and fonts on the proxy. Authorization requests are always bypassed; cookie requests require an explicit rule opt-in."
          action-label="Add Rule"
          @action="openAddCacheRuleModal"
        />
      </div>
    </section>

    <section class="app-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Traffic Shaper</h4>
          <p class="mt-0.5 text-xs text-[#666] normal-case tracking-normal">Limit bandwidth consumption per request or client.</p>
        </div>
        <SecondaryButton size="small" label="Add Shaper" @click="openAddTrafficShaperRuleModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div v-for="rule in trafficShaperRules" :key="rule.id.toString()" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ rule.name }}</p>
              <Tag :value="trafficShaperScopeLabel(rule.budgetScope)" severity="info" />
              <Tag v-if="!rule.enabled" value="Disabled" severity="warn" />
              <Tag :value="`P${rule.priority.toString()}`" severity="info" />
            </div>
            <p class="mt-1 truncate font-mono text-xs text-[#888]">{{ trafficShaperRuleSummary(rule) }} / {{ trafficShaperBudgetSummary(rule) }}</p>
            <p class="mt-1 truncate text-xs text-[#666]">{{ publicPolicyMatchSummary(rule) }} / key {{ trafficShaperKeySummary(rule) }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <SecondaryButton size="small" aria-label="Edit traffic-shaper rule" title="Edit traffic-shaper rule" @click="editTrafficShaperRule(rule.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <DangerButton size="small" aria-label="Delete traffic-shaper rule" title="Delete traffic-shaper rule" @click="deleteTrafficShaperRule(rule.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
        <EmptyState
          v-if="!trafficShaperRules.length"
          title="No traffic-shaper rules configured"
          description="Traffic shapers limit bandwidth consumption per request or client to prevent saturation."
          action-label="Add Shaper"
          @action="openAddTrafficShaperRuleModal"
        />
      </div>
    </section>

    <PublicProxyEditorHost ref="editorHost" :config="config" />
    <ConfirmDialog :state="confirmState" @confirm="onConfirm" @cancel="onCancel" />
  </div>
</template>
