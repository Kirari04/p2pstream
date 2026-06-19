<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NCheckbox, NInputNumber, NTag } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
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
import { naiveTagType } from "@/lib/naiveUi";
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
const { confirm } = useConfirmDialog();

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
        <p class="text-sm text-[var(--app-text-muted)]">Rate limits, WAF rules, cache behavior, and bandwidth shaping.</p>
      </div>
    </div>

    <section class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div v-for="card in summaryCards" :key="card.label" class="app-card p-4">
        <p class="text-xs font-semibold uppercase tracking-widest text-[var(--app-text-muted)]">{{ card.label }}</p>
        <p class="mt-2 text-2xl font-semibold text-[var(--app-text)]">{{ card.value }}</p>
        <p class="mt-1 text-xs text-[var(--app-text-muted)]">{{ card.detail }}</p>
      </div>
    </section>

    <section class="app-card overflow-hidden">
      <div class="border-b border-[var(--app-border)] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[var(--app-text-muted)]">Rate Limits</h4>
          <p class="mt-0.5 text-xs text-[var(--app-text-muted)] normal-case tracking-normal">Throttle traffic based on request rate per client or route.</p>
        </div>
        <NButton secondary size="small" @click="openAddRateLimitRuleModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          Add Rule
        </NButton>
      </div>
      <div class="divide-y divide-[var(--app-border-subtle)]">
        <div v-for="rule in rateLimitRules" :key="rule.id.toString()" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-[var(--app-text)]">{{ rule.name }}</p>
              <NTag size="small" :bordered="false" type="info">{{ rateLimitAlgorithmLabel(rule.algorithm) }}</NTag>
              <NTag v-if="!rule.enabled" size="small" :bordered="false" type="warning">Disabled</NTag>
              <NTag size="small" :bordered="false" type="info">P{{ rule.priority.toString() }}</NTag>
            </div>
            <p class="mt-1 truncate font-mono text-xs text-[var(--app-text-muted)]">{{ rateLimitRuleSummary(rule) }} / key {{ rateLimitKeySummary(rule) }}</p>
            <p class="mt-1 truncate text-xs text-[var(--app-text-muted)]">{{ publicPolicyMatchSummary(rule) }} / response {{ rule.responseStatusCode.toString() }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <NButton secondary size="small" aria-label="Edit rate-limit rule" title="Edit rate-limit rule" @click="editRateLimitRule(rule.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete rate-limit rule" title="Delete rate-limit rule" @click="deleteRateLimitRule(rule.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </NButton>
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
      <div class="border-b border-[var(--app-border)] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[var(--app-text-muted)]">WAF</h4>
          <p class="mt-0.5 text-xs text-[var(--app-text-muted)] normal-case tracking-normal">Block, challenge, or queue matching application traffic before it reaches routes.</p>
        </div>
        <div class="flex flex-wrap gap-2">
          <NButton secondary size="small" @click="openAddWafCaptchaProviderModal">
            <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
            Add Provider
          </NButton>
          <NButton secondary size="small" @click="openAddWafRuleModal">
            <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
            Add Rule
          </NButton>
        </div>
      </div>
      <div class="divide-y divide-[var(--app-border-subtle)]">
        <div v-for="provider in wafCaptchaProviders" :key="`provider-${provider.id.toString()}`" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-[var(--app-text)]">{{ provider.name }}</p>
              <NTag size="small" :bordered="false" type="info">{{ wafProviderLabel(provider.providerType) }}</NTag>
              <NTag size="small" :bordered="false" :type="naiveTagType(provider.secretKeySet ? 'success' : 'danger')">{{ provider.secretKeySet ? 'Secret saved' : 'Secret missing' }}</NTag>
              <NTag v-if="!provider.enabled" size="small" :bordered="false" type="warning">Disabled</NTag>
            </div>
            <p class="mt-1 truncate font-mono text-xs text-[var(--app-text-muted)]">{{ provider.siteKey }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <NButton secondary size="small" aria-label="Edit captcha provider" title="Edit captcha provider" @click="editWafCaptchaProvider(provider)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete captcha provider" title="Delete captcha provider" @click="deleteWafCaptchaProvider(provider.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </NButton>
          </div>
        </div>
        <div v-for="rule in wafRules" :key="`rule-${rule.id.toString()}`" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-[var(--app-text)]">{{ rule.name }}</p>
              <NTag size="small" :bordered="false" type="info">{{ wafActionLabel(rule.action) }}</NTag>
              <NTag size="small" :bordered="false" type="info">{{ wafActivationLabel(rule.activationMode) }}</NTag>
              <NTag v-if="!rule.enabled" size="small" :bordered="false" type="warning">Disabled</NTag>
              <NTag size="small" :bordered="false" type="info">P{{ rule.priority.toString() }}</NTag>
            </div>
            <p class="mt-1 truncate font-mono text-xs text-[var(--app-text-muted)]">{{ wafRuleSummary(rule, wafCaptchaProviders) }} / key {{ rateLimitKeySummary(rule) }}</p>
            <p class="mt-1 truncate text-xs text-[var(--app-text-muted)]">{{ publicPolicyMatchSummary(rule) }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <NButton secondary size="small" aria-label="Edit WAF rule" title="Edit WAF rule" @click="editWafRule(rule.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete WAF rule" title="Delete WAF rule" @click="deleteWafRule(rule.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </NButton>
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
      <div class="border-b border-[var(--app-border)] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[var(--app-text-muted)]">Cache</h4>
          <p class="mt-0.5 text-xs text-[var(--app-text-muted)] normal-case tracking-normal">Cache public static files on the proxy after routing while keeping WAF, rate limits, and shaping active.</p>
        </div>
        <div class="flex flex-wrap gap-2">
          <NButton secondary size="small" @click="purgeAllCache">
            <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            Purge All
          </NButton>
          <NButton secondary size="small" @click="openAddCacheRuleModal">
            <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
            Add Rule
          </NButton>
        </div>
      </div>
      <div class="grid gap-4 border-b border-[var(--app-border)] bg-[var(--app-panel-muted)] px-5 py-4">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h5 class="text-xs font-semibold uppercase tracking-widest text-[var(--app-text-muted)]">Cache Settings</h5>
            <p class="mt-1 text-xs text-[var(--app-text-muted)]">Bodies are stored under the configured public cache directory; metadata stays in SQLite.</p>
          </div>
          <NCheckbox v-model:checked="cacheSettingsForm.enabled">
            Enabled
          </NCheckbox>
        </div>
        <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
            Disk MiB
            <NInputNumber v-model:value="cacheSettingsForm.maxDiskMiB" size="small" :min="1" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
            Memory MiB
            <NInputNumber v-model:value="cacheSettingsForm.maxMemoryMiB" size="small" :min="1" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
            Hot object KiB
            <NInputNumber v-model:value="cacheSettingsForm.hotObjectKiB" size="small" :min="1" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
            Max entries
            <NInputNumber v-model:value="cacheSettingsForm.maxEntries" size="small" :min="1" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
            Cleanup seconds
            <NInputNumber v-model:value="cacheSettingsForm.cleanupIntervalSeconds" size="small" :min="1" :max="3600" />
          </label>
        </div>
        <div class="flex justify-end">
          <NButton type="primary" size="small" :disabled="Boolean(cacheSettingsDisabledReason)" :title="cacheSettingsDisabledReason" @click="saveCacheSettings">
            Save Settings
          </NButton>
        </div>
      </div>
      <div class="divide-y divide-[var(--app-border-subtle)]">
        <div v-for="rule in cacheRules" :key="rule.id.toString()" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-[var(--app-text)]">{{ rule.name }}</p>
              <NTag size="small" :bordered="false" type="info">{{ cacheTtlModeLabel(rule.ttlMode) }}</NTag>
              <NTag size="small" :bordered="false" type="info">{{ cacheScopeLabel(rule.scope) }}</NTag>
              <NTag size="small" :bordered="false" :type="naiveTagType(rule.allowCookieRequests ? 'warn' : 'info')">{{ rule.allowCookieRequests ? 'Cookies allowed' : 'Cookies blocked' }}</NTag>
              <NTag v-if="!rule.enabled" size="small" :bordered="false" type="warning">Disabled</NTag>
              <NTag size="small" :bordered="false" type="info">P{{ rule.priority.toString() }}</NTag>
            </div>
            <p class="mt-1 truncate font-mono text-xs text-[var(--app-text-muted)]">{{ cacheRuleSummary(rule) }} / {{ cacheQueryModeLabel(rule.queryMode) }}</p>
            <p class="mt-1 truncate text-xs text-[var(--app-text-muted)]">{{ cacheRuleMatchSummary(rule) }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <NButton secondary size="small" aria-label="Edit cache rule" title="Edit cache rule" @click="editCacheRule(rule.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete cache rule" title="Delete cache rule" @click="deleteCacheRule(rule.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </NButton>
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
      <div class="border-b border-[var(--app-border)] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[var(--app-text-muted)]">Traffic Shaper</h4>
          <p class="mt-0.5 text-xs text-[var(--app-text-muted)] normal-case tracking-normal">Limit bandwidth consumption per request or client.</p>
        </div>
        <NButton secondary size="small" @click="openAddTrafficShaperRuleModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          Add Shaper
        </NButton>
      </div>
      <div class="divide-y divide-[var(--app-border-subtle)]">
        <div v-for="rule in trafficShaperRules" :key="rule.id.toString()" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-[var(--app-text)]">{{ rule.name }}</p>
              <NTag size="small" :bordered="false" type="info">{{ trafficShaperScopeLabel(rule.budgetScope) }}</NTag>
              <NTag v-if="!rule.enabled" size="small" :bordered="false" type="warning">Disabled</NTag>
              <NTag size="small" :bordered="false" type="info">P{{ rule.priority.toString() }}</NTag>
            </div>
            <p class="mt-1 truncate font-mono text-xs text-[var(--app-text-muted)]">{{ trafficShaperRuleSummary(rule) }} / {{ trafficShaperBudgetSummary(rule) }}</p>
            <p class="mt-1 truncate text-xs text-[var(--app-text-muted)]">{{ publicPolicyMatchSummary(rule) }} / key {{ trafficShaperKeySummary(rule) }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <NButton secondary size="small" aria-label="Edit traffic-shaper rule" title="Edit traffic-shaper rule" @click="editTrafficShaperRule(rule.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete traffic-shaper rule" title="Delete traffic-shaper rule" @click="deleteTrafficShaperRule(rule.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </NButton>
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
  </div>
</template>
