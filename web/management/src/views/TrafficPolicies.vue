<script setup lang="ts">
import { computed, reactive, ref, watch, type HTMLAttributes } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Search as SearchIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NCheckbox, NDrawer, NDrawerContent, NInput, NInputNumber, NTabPane, NTabs, NTag } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
import EmptyState from "@/components/EmptyState.vue";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import TrafficPolicyExecutionOrderStrip from "@/components/traffic-policy/TrafficPolicyExecutionOrderStrip.vue";
import TrafficPolicyRequestPlayground from "@/components/traffic-policy/TrafficPolicyRequestPlayground.vue";
import PublicVisitorIdentitySettings from "@/components/traffic-policy/PublicVisitorIdentitySettings.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
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
  routeTargetName,
  trafficShaperBudgetSummary,
  trafficShaperKeySummary,
  trafficShaperRuleSummary,
  trafficShaperScopeLabel,
  wafActionLabel,
  wafActivationLabel,
  wafProviderLabel,
  wafRuleSummary,
} from "@/lib/publicProxyLabels";
import { editorDrawerWidth, naiveTagType } from "@/lib/naiveUi";
import {
  buildTrafficPolicyPlaygroundStages,
  defaultTrafficPolicyPreviewForm,
  runtimeOrderedCacheRules,
  runtimeOrderedRateLimitRules,
  runtimeOrderedTrafficShaperRules,
  runtimeOrderedWafRules,
  trafficPolicyPreviewFormToRequest,
  trafficPolicyAttentionWarnings,
  type TrafficPolicyAttentionWarning,
  type TrafficPolicyKind,
  type TrafficPolicyPlaygroundStage,
  type TrafficPolicyPreviewForm,
} from "@/lib/trafficPolicyWorkbench";
import type {
  PublicCacheRule,
  PublicRateLimitRule,
  PublicRoute,
  PublicRouteTarget,
  PublicTrafficShaperRule,
  PublicWafCaptchaProvider,
  PublicWafRule,
} from "@/gen/proto/p2pstream/v1/management_pb";

const policySectionKeys = ["rate-limits", "waf", "cache", "traffic-shaper"] as const;
type PolicySectionKey = typeof policySectionKeys[number];
type PolicySectionSummary = {
  key: PolicySectionKey;
  label: string;
  value: string;
  detail: string;
  description: string;
};
type PolicyFilterStatus = "all" | "enabled" | "disabled";

const managementClient = useManagementClient();
const route = useRoute();
const router = useRouter();

const {
  dashboard,
  publicProxyConfig,
  isBusy,
  runManagementAction,
} = useManagementContext();

const config = computed(() => publicProxyConfig.value ?? null);
const rateLimitRules = computed(() => runtimeOrderedRateLimitRules(config.value?.rateLimitRules ?? []));
const cacheRules = computed(() => runtimeOrderedCacheRules(config.value?.cacheRules ?? []));
const cacheSettings = computed(() => config.value?.cacheSettings ?? null);
const wafRules = computed(() => runtimeOrderedWafRules(config.value?.wafRules ?? []));
const wafCaptchaProviders = computed(() => config.value?.wafCaptchaProviders ?? []);
const trafficShaperRules = computed(() => runtimeOrderedTrafficShaperRules(config.value?.trafficShaperRules ?? []));
const enabledRateLimitRules = computed(() => rateLimitRules.value.filter((rule) => rule.enabled).length);
const enabledWafRules = computed(() => wafRules.value.filter((rule) => rule.enabled).length);
const enabledCacheRules = computed(() => cacheRules.value.filter((rule) => rule.enabled).length);
const enabledTrafficShapers = computed(() => trafficShaperRules.value.filter((rule) => rule.enabled).length);
const policyFilter = reactive({
  text: "",
  status: "all" as PolicyFilterStatus,
});
const policyFilterStatusOptions = [
  { label: "All states", value: "all" },
  { label: "Enabled", value: "enabled" },
  { label: "Disabled", value: "disabled" },
];
const previewForm = reactive<TrafficPolicyPreviewForm>(defaultTrafficPolicyPreviewForm());
const previewRequest = computed(() => trafficPolicyPreviewFormToRequest(previewForm));
const policyAttention = computed(() => trafficPolicyAttentionWarnings({
  rateLimitRules: rateLimitRules.value,
  trafficShaperRules: trafficShaperRules.value,
  wafRules: wafRules.value,
  wafCaptchaProviders: wafCaptchaProviders.value,
  cacheSettings: cacheSettings.value ?? undefined,
  cacheRules: cacheRules.value,
}));
const globalPolicyAttention = computed(() => policyAttention.value.filter((item) => item.ruleId === undefined && !item.ruleIds?.length));
const executionStages = computed(() => [
  { key: "precheck", label: "Path Checks", description: "reserved endpoints + route security", icon: "listener" as const },
  { key: "waf", label: "WAF", description: "first matching enforceable rule", icon: "waf" as const, tags: countTags(enabledWafRules.value) },
  { key: "rate-limit", label: "Rate Limits", description: "all matching request budgets", icon: "rate-limit" as const, tags: countTags(enabledRateLimitRules.value) },
  { key: "traffic-shaper", label: "Traffic Shaper", description: "first matching bandwidth budget", icon: "traffic-shaper" as const, tags: countTags(enabledTrafficShapers.value) },
  { key: "route", label: "Route / Target", description: "selected before cache", icon: "route" as const },
  { key: "cache", label: "Cache", description: "first matching cacheable request", icon: "cache" as const, tags: countTags(enabledCacheRules.value) },
  { key: "response", label: "Response", description: "upstream, cached, or terminal", icon: "response" as const },
]);
const playgroundStages = computed<TrafficPolicyPlaygroundStage[]>(() => buildTrafficPolicyPlaygroundStages({
  rateLimitRules: rateLimitRules.value,
  trafficShaperRules: trafficShaperRules.value,
  wafRules: wafRules.value,
  wafCaptchaProviders: wafCaptchaProviders.value,
  cacheSettings: cacheSettings.value ?? undefined,
  cacheRules: cacheRules.value,
}, previewRequest.value));
const filteredRateLimitRules = computed(() => filterPolicyRules(rateLimitRules.value, "rate-limit", rateLimitSearchText));
const filteredWafRules = computed(() => filterPolicyRules(wafRules.value, "waf", wafSearchText));
const filteredCacheRules = computed(() => filterPolicyRules(cacheRules.value, "cache", cacheSearchText));
const filteredTrafficShaperRules = computed(() => filterPolicyRules(trafficShaperRules.value, "traffic-shaper", trafficShaperSearchText));
const isPolicyFilterActive = computed(() => Boolean(policyFilter.text.trim()) || policyFilter.status !== "all");
const previewRouteOptions = computed(() => (config.value?.routes ?? []).map((routeItem) => ({
  label: routeOptionLabel(routeItem),
  value: routeItem.id.toString(),
})));
const previewTargetOptions = computed(() => (config.value?.routeTargets ?? [])
  .filter((target) => !previewForm.routeId || target.routeId.toString() === previewForm.routeId)
  .map((target) => ({
    label: targetOptionLabel(target),
    value: target.id.toString(),
  })));

const editorHost = ref<InstanceType<typeof PublicProxyEditorHost> | null>(null);
const isPreviewOpen = ref(false);
const { confirm } = useConfirmDialog();

const cacheSettingsForm = reactive({
  enabled: true,
  maxDiskMiB: 1024,
  maxMemoryMiB: 128,
  hotObjectKiB: 256,
  maxEntries: 100000,
  cleanupIntervalSeconds: 60,
});

const policySections = computed<PolicySectionSummary[]>(() => [
  {
    key: "rate-limits",
    label: "Rate Limits",
    value: enabledRateLimitRules.value.toString(),
    detail: `enabled · ${countLabel(rateLimitRules.value.length, "rule")} total`,
    description: "Throttle traffic based on request rate per client or route.",
  },
  {
    key: "waf",
    label: "WAF",
    value: enabledWafRules.value.toString(),
    detail: `enabled · ${countLabel(wafRules.value.length, "rule")} · ${countLabel(wafCaptchaProviders.value.length, "captcha provider")}`,
    description: "Block, challenge, or queue matching application traffic before it reaches routes.",
  },
  {
    key: "cache",
    label: "Cache",
    value: enabledCacheRules.value.toString(),
    detail: `enabled · ${countLabel(cacheRules.value.length, "rule")} · storage ${cacheSettingsForm.enabled ? "on" : "off"}`,
    description: "Cache public static files on the proxy after routing while keeping WAF, rate limits, and shaping active.",
  },
  {
    key: "traffic-shaper",
    label: "Traffic Shaper",
    value: enabledTrafficShapers.value.toString(),
    detail: `enabled · ${countLabel(trafficShaperRules.value.length, "shaper")}`,
    description: "Limit bandwidth consumption per request or client.",
  },
]);
const activePolicySection = computed<PolicySectionKey>(() => normalizePolicySection(route.params.section));
const activePolicyMeta = computed(() => (
  policySections.value.find((section) => section.key === activePolicySection.value) ?? policySections.value[0]
));
const activePolicyRuleCount = computed(() => {
  switch (activePolicySection.value) {
    case "waf": return { visible: filteredWafRules.value.length, total: wafRules.value.length };
    case "cache": return { visible: filteredCacheRules.value.length, total: cacheRules.value.length };
    case "traffic-shaper": return { visible: filteredTrafficShaperRules.value.length, total: trafficShaperRules.value.length };
    default: return { visible: filteredRateLimitRules.value.length, total: rateLimitRules.value.length };
  }
});
const policyFilterResultLabel = computed(() => {
  const { visible, total } = activePolicyRuleCount.value;
  if (!isPolicyFilterActive.value) return countLabel(total, "rule");
  return `${visible.toString()} of ${countLabel(total, "rule")} shown`;
});

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

watch(() => previewForm.routeId, () => {
  if (!previewForm.targetId) return;
  if (!previewTargetBelongsToRoute(previewForm.targetId, previewForm.routeId)) {
    previewForm.targetId = "";
  }
});

watch(() => previewForm.targetId, (targetId) => {
  if (!targetId || previewForm.routeId) return;
  const target = findPreviewTarget(targetId);
  if (target) previewForm.routeId = target.routeId.toString();
});

async function run(action: () => Promise<void>) {
  if (!runManagementAction) return;
  await runManagementAction(action);
}

function normalizePolicySection(value: unknown): PolicySectionKey {
  const section = Array.isArray(value) ? value[0] : value;
  return policySectionKeys.includes(section as PolicySectionKey) ? section as PolicySectionKey : "rate-limits";
}

async function selectPolicySection(value: string | number) {
  const section = normalizePolicySection(value);
  if (section === activePolicySection.value) return;
  await router.push(`/policies/${section}`);
}

function policyTabProps(section: PolicySectionKey, label: string): HTMLAttributes {
  return {
    id: `traffic-policy-tab-${section}`,
    role: "tab",
    tabindex: 0,
    "aria-label": label,
    "aria-selected": activePolicySection.value === section,
    onKeydown: (event: KeyboardEvent) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      void selectPolicySection(section);
    },
  };
}

function updateTrafficPolicyPreview(value: TrafficPolicyPreviewForm) {
  Object.assign(previewForm, value);
}

function resetTrafficPolicyPreview() {
  Object.assign(previewForm, defaultTrafficPolicyPreviewForm());
}

function countTags(count: number) {
  return count ? [{ label: count.toString(), tone: "info" as const }] : [];
}

function countLabel(count: number, singular: string, plural = `${singular}s`): string {
  return `${count.toString()} ${count === 1 ? singular : plural}`;
}

function filterPolicyRules<T extends { enabled: boolean; id: bigint }>(
  rules: readonly T[],
  kind: TrafficPolicyKind,
  searchText: (rule: T) => string,
): T[] {
  const needle = policyFilter.text.trim().toLowerCase();
  return rules.filter((rule) => {
    if (policyFilter.status === "enabled" && !rule.enabled) return false;
    if (policyFilter.status === "disabled" && rule.enabled) return false;
    if (!needle) return true;
    return searchText(rule).toLowerCase().includes(needle) ||
      policyWarningsForRule(kind, rule.id).some((warning) => warningLabel(warning).toLowerCase().includes(needle));
  });
}

function policyWarningsForRule(kind: TrafficPolicyKind, id: bigint): TrafficPolicyAttentionWarning[] {
  return policyAttention.value.filter((warning) => warning.policyKind === kind && (
    warning.ruleId === id || warning.ruleIds?.some((ruleId) => ruleId === id)
  ));
}

function visiblePolicyWarningsForRule(kind: TrafficPolicyKind, id: bigint): TrafficPolicyAttentionWarning[] {
  return policyWarningsForRule(kind, id).filter((warning) => (
    warning.code !== "disabled-rule" &&
    warning.code !== "cache-allows-cookie-requests"
  ));
}

function warningLabel(warning: TrafficPolicyAttentionWarning): string {
  switch (warning.code) {
    case "duplicate-priority": return "Priority tie";
    case "disabled-rule": return "Disabled";
    case "any-request-rule": return "Any request";
    case "captcha-provider-missing": return "Provider missing";
    case "captcha-provider-disabled": return "Provider disabled";
    case "captcha-provider-secret-missing": return "Provider secret missing";
    case "cache-settings-disabled": return "Cache disabled";
    case "cache-allows-cookie-requests": return "Legacy Cookie flag";
    default: return warning.message;
  }
}

function warningSeverity(warning: TrafficPolicyAttentionWarning): string {
  switch (warning.code) {
    case "captcha-provider-missing":
    case "captcha-provider-disabled":
    case "captcha-provider-secret-missing":
      return "danger";
    case "duplicate-priority":
    case "any-request-rule":
    case "cache-settings-disabled":
    case "cache-allows-cookie-requests":
      return "warning";
    default:
      return "info";
  }
}

function rateLimitSearchText(rule: PublicRateLimitRule): string {
  return [
    rule.name,
    rule.priority.toString(),
    rule.enabled ? "enabled" : "disabled",
    rateLimitAlgorithmLabel(rule.algorithm),
    rateLimitRuleSummary(rule),
    rateLimitKeySummary(rule),
    publicPolicyMatchSummary(rule),
    rule.responseStatusCode.toString(),
  ].join(" ");
}

function wafSearchText(rule: PublicWafRule): string {
  return [
    rule.name,
    rule.priority.toString(),
    rule.enabled ? "enabled" : "disabled",
    wafActionLabel(rule.action),
    wafActivationLabel(rule.activationMode),
    wafRuleSummary(rule, wafCaptchaProviders.value),
    rateLimitKeySummary(rule),
    publicPolicyMatchSummary(rule),
  ].join(" ");
}

function cacheSearchText(rule: PublicCacheRule): string {
  return [
    rule.name,
    rule.priority.toString(),
    rule.enabled ? "enabled" : "disabled",
    cacheTtlModeLabel(rule.ttlMode),
    cacheScopeLabel(rule.scope),
    cacheRuleSummary(rule),
    cacheQueryModeLabel(rule.queryMode),
    cacheRuleMatchSummary(rule),
  ].join(" ");
}

function trafficShaperSearchText(rule: PublicTrafficShaperRule): string {
  return [
    rule.name,
    rule.priority.toString(),
    rule.enabled ? "enabled" : "disabled",
    trafficShaperScopeLabel(rule.budgetScope),
    trafficShaperRuleSummary(rule),
    trafficShaperBudgetSummary(rule),
    trafficShaperKeySummary(rule),
    publicPolicyMatchSummary(rule),
  ].join(" ");
}

function routeOptionLabel(routeItem: PublicRoute): string {
  return `${routeItem.hostPattern || "*"}${routeItem.pathPrefix || "/"} / P${routeItem.priority.toString()}`;
}

function targetOptionLabel(target: PublicRouteTarget): string {
  return `${routeTargetName(target)} / route ${target.routeId.toString()}`;
}

function findPreviewTarget(targetId: string): PublicRouteTarget | undefined {
  return (config.value?.routeTargets ?? []).find((target) => target.id.toString() === targetId);
}

function previewTargetBelongsToRoute(targetId: string, routeId: string): boolean {
  if (!routeId) return true;
  const target = findPreviewTarget(targetId);
  return Boolean(target && target.routeId.toString() === routeId);
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
  <div v-if="dashboard" class="stack-xl">
    <div class="page-toolbar policy-toolbar">
      <div class="page-toolbar__body">
        <h1 class="margin-bottom-sm copy-2xl weight-bold">Traffic Policy</h1>
        <p id="traffic-policy-page-description" class="copy-sm muted-text">{{ activePolicyMeta.description }}</p>
      </div>
      <div class="page-toolbar__actions">
        <NButton secondary size="small" @click="isPreviewOpen = true">
          <template #icon><SearchIcon class="icon-sm" /></template>
          Request Tester
        </NButton>
      </div>
    </div>

    <dl class="summary-grid summary-grid--four policy-summary-grid" aria-label="Traffic policy summary">
      <div
        v-for="section in policySections"
        :key="section.key"
        class="policy-summary-card"
        :class="{ 'policy-summary-card--active': section.key === activePolicySection }"
      >
        <dt>{{ section.label }}</dt>
        <dd>
          <strong class="base-text">{{ section.value }}</strong>
          <small>{{ section.detail }}</small>
        </dd>
      </div>
    </dl>

    <TrafficPolicyExecutionOrderStrip
      :stages="executionStages"
      description="Rules are shown in runtime order. Lower priorities run first, ties fall back to rule ID."
    />

    <section class="policy-filter-bar" aria-label="Traffic policy filters">
      <div class="policy-filter-search">
        <label id="traffic-policy-search-label" class="copy-sm weight-medium muted-text">Search rules</label>
        <NInput
          v-model:value="policyFilter.text"
          size="small"
          clearable
          placeholder="Name, match, priority, or warning"
          :input-props="{ 'aria-labelledby': 'traffic-policy-search-label' }"
        >
          <template #prefix><SearchIcon class="icon-sm" /></template>
        </NInput>
      </div>
      <div class="policy-filter-status">
        <label id="traffic-policy-state-label" class="copy-sm weight-medium muted-text">Rule state</label>
        <AccessibleSelect
          v-model:value="policyFilter.status"
          accessible-label="Rule state"
          size="small"
          :options="policyFilterStatusOptions"
          :input-props="{ 'aria-labelledby': 'traffic-policy-state-label' }"
        />
      </div>
      <p class="policy-filter-result copy-xs muted-text" aria-live="polite">{{ policyFilterResultLabel }}</p>
    </section>

    <NTabs
      class="policy-tabs"
      type="line"
      animated
      role="group"
      aria-label="Traffic policy sections"
      aria-describedby="traffic-policy-page-description"
      :value="activePolicySection"
      @update:value="selectPolicySection"
    >
      <NTabPane
        id="traffic-policy-panel-rate-limits"
        name="rate-limits"
        role="tabpanel"
        aria-labelledby="traffic-policy-tab-rate-limits"
        :tab="`Rate Limits · ${enabledRateLimitRules} enabled`"
        :tab-props="policyTabProps('rate-limits', `Rate Limits, ${enabledRateLimitRules.toString()} enabled`)"
      >
    <section class="surface-card hide-overflow">
      <div class="workbench-section-header divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
        <div>
          <h2 class="copy-base weight-semibold">Rate Limits</h2>
          <p class="margin-top-xs copy-sm muted-text">Throttle traffic based on request rate per client or route.</p>
        </div>
        <NButton type="primary" size="small" @click="openAddRateLimitRuleModal">
          <template #icon><PlusIcon class="icon-sm" /></template>
          Add Rate-Limit Rule
        </NButton>
      </div>
      <div class="divided-list">
        <div v-for="rule in filteredRateLimitRules" :key="rule.id.toString()" class="layout-grid space-md pad-x-xl pad-y-lg mq-lg-one-auto">
          <div class="min-width-zero">
            <div class="layout-row min-width-zero wrap-items align-center space-sm">
              <p class="clip-text copy-sm weight-medium base-text">{{ rule.name }}</p>
              <NTag size="small" :bordered="false" type="info">{{ rateLimitAlgorithmLabel(rule.algorithm) }}</NTag>
              <NTag v-if="!rule.enabled" size="small" :bordered="false" type="warning">Disabled</NTag>
              <NTag size="small" :bordered="false" type="info">P{{ rule.priority.toString() }}</NTag>
              <NTag
                v-for="warning in visiblePolicyWarningsForRule('rate-limit', rule.id)"
                :key="warning.code"
                size="small"
                :bordered="false"
                :type="naiveTagType(warningSeverity(warning))"
              >
                {{ warningLabel(warning) }}
              </NTag>
            </div>
            <p class="margin-top-xs clip-text mono-text copy-xs muted-text">{{ rateLimitRuleSummary(rule) }} / key {{ rateLimitKeySummary(rule) }}</p>
            <p class="margin-top-xs clip-text copy-xs muted-text">{{ publicPolicyMatchSummary(rule) }} / response {{ rule.responseStatusCode.toString() }}</p>
          </div>
          <div class="layout-row space-sm mq-lg-end">
            <NButton secondary size="small" aria-label="Edit rate-limit rule" title="Edit rate-limit rule" @click="editRateLimitRule(rule.id)">
              <template #icon><PencilIcon class="icon-sm" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete rate-limit rule" title="Delete rate-limit rule" @click="deleteRateLimitRule(rule.id)">
              <template #icon><TrashIcon class="icon-sm" /></template>
            </NButton>
          </div>
        </div>
        <EmptyState
          v-if="rateLimitRules.length && !filteredRateLimitRules.length && isPolicyFilterActive"
          title="No matching rate-limit rules"
          description="Adjust the filter text or state selector."
        />
        <EmptyState
          v-if="!rateLimitRules.length"
          title="No rate-limit rules configured"
          description="Rate limits protect your route targets from excessive traffic by throttling requests per client or route."
          action-label="Add Rate-Limit Rule"
          @action="openAddRateLimitRuleModal"
        />
      </div>
    </section>
      </NTabPane>

      <NTabPane
        id="traffic-policy-panel-waf"
        name="waf"
        role="tabpanel"
        aria-labelledby="traffic-policy-tab-waf"
        :tab="`WAF · ${enabledWafRules} enabled`"
        :tab-props="policyTabProps('waf', `WAF, ${enabledWafRules.toString()} enabled`)"
      >
    <div class="stack-xl">
    <PublicVisitorIdentitySettings :config="config" />
    <section class="surface-card hide-overflow">
      <div class="workbench-section-header divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
        <div>
          <h2 class="copy-base weight-semibold">WAF</h2>
          <p class="margin-top-xs copy-sm muted-text">Block, challenge, or queue matching application traffic before it reaches routes.</p>
        </div>
        <div class="workbench-section-actions layout-row wrap-items space-sm">
          <NButton secondary size="small" @click="openAddWafCaptchaProviderModal">
            <template #icon><PlusIcon class="icon-sm" /></template>
            Add Captcha Provider
          </NButton>
          <NButton type="primary" size="small" @click="openAddWafRuleModal">
            <template #icon><PlusIcon class="icon-sm" /></template>
            Add WAF Rule
          </NButton>
        </div>
      </div>
      <div class="divided-list">
        <div v-if="wafCaptchaProviders.length" class="policy-list-heading">
          <div>
            <h3>Captcha providers</h3>
            <p>Credentials used by WAF challenge rules.</p>
          </div>
          <span>{{ countLabel(wafCaptchaProviders.length, "provider") }}</span>
        </div>
        <div v-if="wafCaptchaProviders.length" class="policy-data-table" role="table" aria-label="Captcha providers">
          <div role="rowgroup">
            <div class="policy-data-header policy-provider-grid" role="row">
              <span class="policy-grid-identity" role="columnheader">Provider</span>
              <span class="policy-grid-type" role="columnheader">Type</span>
              <span class="policy-grid-secret" role="columnheader">Credential</span>
              <span class="policy-grid-state" role="columnheader">State</span>
              <span class="policy-grid-actions" role="columnheader">Actions</span>
            </div>
          </div>
          <div class="policy-data-rows" role="rowgroup">
            <div
              v-for="provider in wafCaptchaProviders"
              :key="`provider-${provider.id.toString()}`"
              class="policy-data-row policy-provider-grid"
              role="row"
            >
              <div class="policy-data-cell policy-grid-identity" data-label="Provider" role="cell">
                <span class="policy-data-label">Provider</span>
                <p class="policy-data-primary" :title="provider.name">{{ provider.name }}</p>
                <p class="policy-data-secondary mono-text" :title="provider.siteKey">{{ provider.siteKey }}</p>
              </div>
              <div class="policy-data-cell policy-grid-type" data-label="Type" role="cell">
                <span class="policy-data-label">Type</span>
                <NTag size="small" :bordered="false" type="info">{{ wafProviderLabel(provider.providerType) }}</NTag>
              </div>
              <div class="policy-data-cell policy-grid-secret" data-label="Credential" role="cell">
                <span class="policy-data-label">Credential</span>
                <NTag size="small" :bordered="false" :type="naiveTagType(provider.secretKeySet ? 'success' : 'danger')">
                  {{ provider.secretKeySet ? 'Secret saved' : 'Secret missing' }}
                </NTag>
              </div>
              <div class="policy-data-cell policy-grid-state" data-label="State" role="cell">
                <span class="policy-data-label">State</span>
                <NTag size="small" :bordered="false" :type="naiveTagType(provider.enabled ? 'success' : 'warning')">
                  {{ provider.enabled ? 'Enabled' : 'Disabled' }}
                </NTag>
              </div>
              <div class="policy-data-cell policy-data-actions policy-grid-actions" data-label="Actions" role="cell">
                <span class="policy-data-label">Actions</span>
                <NButton secondary size="small" aria-label="Edit captcha provider" title="Edit captcha provider" @click="editWafCaptchaProvider(provider)">
                  <template #icon><PencilIcon class="icon-sm" /></template>
                </NButton>
                <NButton type="error" size="small" aria-label="Delete captcha provider" title="Delete captcha provider" @click="deleteWafCaptchaProvider(provider.id)">
                  <template #icon><TrashIcon class="icon-sm" /></template>
                </NButton>
              </div>
            </div>
          </div>
        </div>
        <div v-if="wafRules.length" class="policy-list-heading">
          <div>
            <h3>WAF rules</h3>
            <p>Evaluated in priority order before rate limits and routing.</p>
          </div>
          <span>{{ countLabel(wafRules.length, "rule") }}</span>
        </div>
        <div v-if="filteredWafRules.length" class="policy-data-table" role="table" aria-label="WAF rules">
          <div role="rowgroup">
            <div class="policy-data-header policy-rule-grid" role="row">
              <span class="policy-grid-identity" role="columnheader">Rule</span>
              <span class="policy-grid-match" role="columnheader">Match</span>
              <span class="policy-grid-action" role="columnheader">Action</span>
              <span class="policy-grid-state" role="columnheader">Order &amp; state</span>
              <span class="policy-grid-actions" role="columnheader">Actions</span>
            </div>
          </div>
          <div class="policy-data-rows" role="rowgroup">
            <div
              v-for="rule in filteredWafRules"
              :key="`rule-${rule.id.toString()}`"
              class="policy-data-row policy-rule-grid"
              role="row"
            >
              <div class="policy-data-cell policy-grid-identity" data-label="Rule" role="cell">
                <span class="policy-data-label">Rule</span>
                <p class="policy-data-primary" :title="rule.name">{{ rule.name }}</p>
                <p class="policy-data-secondary mono-text">Rule #{{ rule.id.toString() }}</p>
              </div>
              <div class="policy-data-cell policy-grid-match" data-label="Match" role="cell">
                <span class="policy-data-label">Match</span>
                <p class="policy-data-value">{{ publicPolicyMatchSummary(rule) }}</p>
                <p class="policy-data-secondary mono-text">Key {{ rateLimitKeySummary(rule) }}</p>
              </div>
              <div class="policy-data-cell policy-grid-action" data-label="Action" role="cell">
                <span class="policy-data-label">Action</span>
                <div class="policy-data-tags">
                  <NTag size="small" :bordered="false" type="info">{{ wafActionLabel(rule.action) }}</NTag>
                  <NTag size="small" :bordered="false">{{ wafActivationLabel(rule.activationMode) }}</NTag>
                </div>
                <p class="policy-data-secondary mono-text">{{ wafRuleSummary(rule, wafCaptchaProviders) }}</p>
              </div>
              <div class="policy-data-cell policy-grid-state" data-label="Order &amp; state" role="cell">
                <span class="policy-data-label">Order &amp; state</span>
                <div class="policy-data-tags">
                  <NTag size="small" :bordered="false" type="info">P{{ rule.priority.toString() }}</NTag>
                  <NTag size="small" :bordered="false" :type="naiveTagType(rule.enabled ? 'success' : 'warning')">
                    {{ rule.enabled ? 'Enabled' : 'Disabled' }}
                  </NTag>
                </div>
                <div v-if="visiblePolicyWarningsForRule('waf', rule.id).length" class="policy-data-tags">
                  <NTag
                    v-for="warning in visiblePolicyWarningsForRule('waf', rule.id)"
                    :key="warning.code"
                    size="small"
                    :bordered="false"
                    :type="naiveTagType(warningSeverity(warning))"
                  >
                    {{ warningLabel(warning) }}
                  </NTag>
                </div>
              </div>
              <div class="policy-data-cell policy-data-actions policy-grid-actions" data-label="Actions" role="cell">
                <span class="policy-data-label">Actions</span>
                <NButton secondary size="small" aria-label="Edit WAF rule" title="Edit WAF rule" @click="editWafRule(rule.id)">
                  <template #icon><PencilIcon class="icon-sm" /></template>
                </NButton>
                <NButton type="error" size="small" aria-label="Delete WAF rule" title="Delete WAF rule" @click="deleteWafRule(rule.id)">
                  <template #icon><TrashIcon class="icon-sm" /></template>
                </NButton>
              </div>
            </div>
          </div>
        </div>
        <EmptyState
          v-if="wafRules.length && !filteredWafRules.length && isPolicyFilterActive"
          title="No matching WAF rules"
          description="Adjust the filter text or state selector."
        />
        <EmptyState
          v-if="!wafRules.length && !wafCaptchaProviders.length"
          title="No WAF policy configured"
          description="WAF rules can block, challenge, or queue selected traffic before rate limits, shapers, routes, and targets."
          action-label="Add WAF Rule"
          @action="openAddWafRuleModal"
        />
      </div>
    </section>
    </div>
      </NTabPane>

      <NTabPane
        id="traffic-policy-panel-cache"
        name="cache"
        role="tabpanel"
        aria-labelledby="traffic-policy-tab-cache"
        :tab="`Cache · ${enabledCacheRules} enabled`"
        :tab-props="policyTabProps('cache', `Cache, ${enabledCacheRules.toString()} enabled`)"
      >
    <section class="surface-card hide-overflow">
      <div class="workbench-section-header divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
        <div>
          <h2 class="copy-base weight-semibold">Cache</h2>
          <p class="margin-top-xs copy-sm muted-text">Cache public static files on the proxy after routing while keeping WAF, rate limits, and shaping active.</p>
        </div>
        <div class="workbench-section-actions layout-row wrap-items space-sm">
          <NButton secondary size="small" @click="purgeAllCache">
            <template #icon><TrashIcon class="icon-sm" /></template>
            Purge Cache
          </NButton>
          <NButton type="primary" size="small" @click="openAddCacheRuleModal">
            <template #icon><PlusIcon class="icon-sm" /></template>
            Add Cache Rule
          </NButton>
        </div>
      </div>
      <div class="policy-cache-settings layout-grid space-lg divider-bottom frame-standard muted-bg pad-x-xl pad-y-lg">
        <div class="layout-row wrap-items align-center spread-items space-md">
          <div>
            <h3 class="copy-sm weight-semibold">Cache Settings</h3>
            <p class="margin-top-xs copy-xs muted-text">Bodies are stored under the configured public cache directory; metadata stays in SQLite.</p>
          </div>
          <NCheckbox v-model:checked="cacheSettingsForm.enabled">
            Cache enabled
          </NCheckbox>
        </div>
        <div class="layout-grid space-md mq-sm-cols-two mq-xl-cols-five">
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Disk MiB
            <NInputNumber :show-button="false" v-model:value="cacheSettingsForm.maxDiskMiB" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Memory MiB
            <NInputNumber :show-button="false" v-model:value="cacheSettingsForm.maxMemoryMiB" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Hot object KiB
            <NInputNumber :show-button="false" v-model:value="cacheSettingsForm.hotObjectKiB" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Max entries
            <NInputNumber :show-button="false" v-model:value="cacheSettingsForm.maxEntries" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Cleanup seconds
            <NInputNumber :show-button="false" v-model:value="cacheSettingsForm.cleanupIntervalSeconds" size="small" :min="1" :max="3600" />
          </label>
        </div>
        <div class="layout-row align-end-row">
          <NButton type="primary" size="small" :disabled="Boolean(cacheSettingsDisabledReason)" :title="cacheSettingsDisabledReason" @click="saveCacheSettings">
            Save Cache Settings
          </NButton>
        </div>
      </div>
      <div class="divided-list">
        <div v-for="rule in filteredCacheRules" :key="rule.id.toString()" class="layout-grid space-md pad-x-xl pad-y-lg mq-lg-one-auto">
          <div class="min-width-zero">
            <div class="layout-row min-width-zero wrap-items align-center space-sm">
              <p class="clip-text copy-sm weight-medium base-text">{{ rule.name }}</p>
              <NTag size="small" :bordered="false" type="info">{{ cacheTtlModeLabel(rule.ttlMode) }}</NTag>
              <NTag size="small" :bordered="false" type="info">{{ cacheScopeLabel(rule.scope) }}</NTag>
              <NTag v-if="rule.allowCookieRequests" size="small" :bordered="false" type="warning">Legacy cookie flag</NTag>
              <NTag v-if="!rule.enabled" size="small" :bordered="false" type="warning">Disabled</NTag>
              <NTag size="small" :bordered="false" type="info">P{{ rule.priority.toString() }}</NTag>
              <NTag
                v-for="warning in visiblePolicyWarningsForRule('cache', rule.id)"
                :key="warning.code"
                size="small"
                :bordered="false"
                :type="naiveTagType(warningSeverity(warning))"
              >
                {{ warningLabel(warning) }}
              </NTag>
            </div>
            <p class="margin-top-xs clip-text mono-text copy-xs muted-text">{{ cacheRuleSummary(rule) }} / {{ cacheQueryModeLabel(rule.queryMode) }}</p>
            <p class="margin-top-xs clip-text copy-xs muted-text">{{ cacheRuleMatchSummary(rule) }}</p>
          </div>
          <div class="layout-row space-sm mq-lg-end">
            <NButton secondary size="small" aria-label="Edit cache rule" title="Edit cache rule" @click="editCacheRule(rule.id)">
              <template #icon><PencilIcon class="icon-sm" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete cache rule" title="Delete cache rule" @click="deleteCacheRule(rule.id)">
              <template #icon><TrashIcon class="icon-sm" /></template>
            </NButton>
          </div>
        </div>
        <EmptyState
          v-if="cacheRules.length && !filteredCacheRules.length && isPolicyFilterActive"
          title="No matching cache rules"
          description="Adjust the filter text or state selector."
        />
        <EmptyState
          v-if="!cacheRules.length"
          title="No cache rules configured"
          description="Cache rules store public GET assets such as CSS, JavaScript, images, and fonts on the proxy. Authorization and Cookie requests always bypass shared cache."
          action-label="Add Cache Rule"
          @action="openAddCacheRuleModal"
        />
      </div>
    </section>
      </NTabPane>

      <NTabPane
        id="traffic-policy-panel-traffic-shaper"
        name="traffic-shaper"
        role="tabpanel"
        aria-labelledby="traffic-policy-tab-traffic-shaper"
        :tab="`Traffic Shaper · ${enabledTrafficShapers} enabled`"
        :tab-props="policyTabProps('traffic-shaper', `Traffic Shaper, ${enabledTrafficShapers.toString()} enabled`)"
      >
    <section class="surface-card hide-overflow">
      <div class="workbench-section-header divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
        <div>
          <h2 class="copy-base weight-semibold">Traffic Shaper</h2>
          <p class="margin-top-xs copy-sm muted-text">Limit bandwidth consumption per request or client.</p>
        </div>
        <NButton type="primary" size="small" @click="openAddTrafficShaperRuleModal">
          <template #icon><PlusIcon class="icon-sm" /></template>
          Add Traffic Shaper
        </NButton>
      </div>
      <div class="divided-list">
        <div v-for="rule in filteredTrafficShaperRules" :key="rule.id.toString()" class="layout-grid space-md pad-x-xl pad-y-lg mq-lg-one-auto">
          <div class="min-width-zero">
            <div class="layout-row min-width-zero wrap-items align-center space-sm">
              <p class="clip-text copy-sm weight-medium base-text">{{ rule.name }}</p>
              <NTag size="small" :bordered="false" type="info">{{ trafficShaperScopeLabel(rule.budgetScope) }}</NTag>
              <NTag v-if="!rule.enabled" size="small" :bordered="false" type="warning">Disabled</NTag>
              <NTag size="small" :bordered="false" type="info">P{{ rule.priority.toString() }}</NTag>
              <NTag
                v-for="warning in visiblePolicyWarningsForRule('traffic-shaper', rule.id)"
                :key="warning.code"
                size="small"
                :bordered="false"
                :type="naiveTagType(warningSeverity(warning))"
              >
                {{ warningLabel(warning) }}
              </NTag>
            </div>
            <p class="margin-top-xs clip-text mono-text copy-xs muted-text">{{ trafficShaperRuleSummary(rule) }} / {{ trafficShaperBudgetSummary(rule) }}</p>
            <p class="margin-top-xs clip-text copy-xs muted-text">{{ publicPolicyMatchSummary(rule) }} / key {{ trafficShaperKeySummary(rule) }}</p>
          </div>
          <div class="layout-row space-sm mq-lg-end">
            <NButton secondary size="small" aria-label="Edit traffic-shaper rule" title="Edit traffic-shaper rule" @click="editTrafficShaperRule(rule.id)">
              <template #icon><PencilIcon class="icon-sm" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete traffic-shaper rule" title="Delete traffic-shaper rule" @click="deleteTrafficShaperRule(rule.id)">
              <template #icon><TrashIcon class="icon-sm" /></template>
            </NButton>
          </div>
        </div>
        <EmptyState
          v-if="trafficShaperRules.length && !filteredTrafficShaperRules.length && isPolicyFilterActive"
          title="No matching traffic-shaper rules"
          description="Adjust the filter text or state selector."
        />
        <EmptyState
          v-if="!trafficShaperRules.length"
          title="No traffic-shaper rules configured"
          description="Traffic shapers limit bandwidth consumption per request or client to prevent saturation."
          action-label="Add Traffic Shaper"
          @action="openAddTrafficShaperRuleModal"
        />
      </div>
    </section>
      </NTabPane>
    </NTabs>

    <PublicProxyEditorHost ref="editorHost" :config="config" />

    <NDrawer
      v-model:show="isPreviewOpen"
      placement="right"
      :width="editorDrawerWidth('76rem')"
      aria-label="Request Tester"
      class="editor-drawer"
    >
      <NDrawerContent title="Request Tester" closable>
        <TrafficPolicyRequestPlayground
          :model-value="previewForm"
          :stages="playgroundStages"
          :route-options="previewRouteOptions"
          :target-options="previewTargetOptions"
          :global-attention="globalPolicyAttention"
          @update:model-value="updateTrafficPolicyPreview"
          @reset="resetTrafficPolicyPreview"
        />
      </NDrawerContent>
    </NDrawer>
  </div>
</template>

<style scoped>
.policy-summary-grid {
  margin: 0;
}

.policy-summary-card {
  display: grid;
  align-content: center;
  gap: 0.25rem;
}

.policy-summary-card dt,
.policy-summary-card small {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.45;
}

.policy-summary-card dt {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 600;
}

.policy-summary-card small {
  overflow-wrap: anywhere;
}

.policy-summary-card dd {
  display: grid;
  gap: 0.125rem;
  min-width: 0;
  margin: 0;
}

.policy-summary-card strong {
  overflow: hidden;
  font-size: 1rem;
  font-variant-numeric: tabular-nums;
  font-weight: 650;
  line-height: 1.4;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.policy-tabs {
  min-width: 0;
}

.policy-tabs :deep(.n-tabs-nav) {
  margin-bottom: 1rem;
}

.policy-tabs :deep(.n-tab-pane) {
  padding-top: 0.25rem;
}

.policy-filter-bar {
  display: grid;
  gap: 0.75rem;
  border-block: 1px solid var(--app-border-subtle);
  padding-block: 0.875rem;
}

.policy-filter-search,
.policy-filter-status {
  display: grid;
  gap: 0.375rem;
  min-width: 0;
}

.policy-filter-result {
  align-self: end;
  margin: 0;
  padding-bottom: 0.375rem;
  white-space: nowrap;
}

.policy-list-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.75rem 1.25rem;
  background: var(--app-panel-muted);
}

.policy-list-heading h3,
.policy-list-heading p {
  margin: 0;
}

.policy-list-heading h3 {
  font-size: 0.875rem;
  font-weight: 600;
}

.policy-list-heading p,
.policy-list-heading > span {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.45;
}

.policy-list-heading > span {
  flex: none;
}

.policy-data-table {
  min-width: 0;
  container-type: inline-size;
}

.policy-data-header {
  display: none;
}

.policy-data-rows {
  display: grid;
}

.policy-data-row {
  display: grid;
  min-width: 0;
  align-items: start;
  gap: 0.75rem 1rem;
  padding: 0.75rem 1rem;
}

.policy-data-row + .policy-data-row {
  border-top: 1px solid var(--app-border-subtle);
}

.policy-provider-grid {
  grid-template-areas:
    "identity identity actions"
    "type secret state";
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.policy-rule-grid {
  grid-template-areas:
    "identity actions"
    "match match"
    "action state";
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.policy-grid-identity {
  grid-area: identity;
}

.policy-grid-type {
  grid-area: type;
}

.policy-grid-secret {
  grid-area: secret;
}

.policy-grid-match {
  grid-area: match;
}

.policy-grid-action {
  grid-area: action;
}

.policy-grid-state {
  grid-area: state;
}

.policy-grid-actions {
  grid-area: actions;
}

.policy-data-cell {
  display: grid;
  min-width: 0;
  align-content: start;
  justify-items: start;
  gap: 0.25rem;
}

.policy-data-label {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-weight: 600;
  line-height: 1.35;
}

.policy-data-primary,
.policy-data-secondary,
.policy-data-value {
  max-width: 100%;
  margin: 0;
  unicode-bidi: plaintext;
}

.policy-data-primary {
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
  line-height: 1.4;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.policy-data-value {
  display: -webkit-box;
  max-height: 2.8em;
  overflow: hidden;
  overflow-wrap: anywhere;
  color: var(--app-text);
  font-size: 0.8125rem;
  line-height: 1.4;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.policy-data-secondary {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.4;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.policy-data-tags,
.policy-data-actions {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
}

.policy-data-actions {
  justify-content: flex-end;
  justify-self: end;
}

.policy-data-actions .policy-data-label {
  flex-basis: 100%;
  text-align: right;
}

.policy-data-cell > :deep(.n-tag) {
  max-width: 100%;
}

@container (min-width: 48rem) {
  .policy-data-header {
    display: grid;
    align-items: center;
    gap: 0.75rem;
    border-bottom: 1px solid var(--app-border-subtle);
    padding: 0.5rem 1rem;
    background: color-mix(in srgb, var(--app-panel-muted) 62%, var(--app-panel));
    color: var(--app-text-muted);
    font-size: 0.75rem;
    font-weight: 600;
    line-height: 1.35;
  }

  .policy-data-row {
    align-items: center;
    gap: 0.75rem;
    padding: 0.625rem 1rem;
  }

  .policy-provider-grid {
    grid-template-areas: "identity type secret state actions";
    grid-template-columns:
      minmax(12rem, 1.3fr)
      minmax(7rem, 0.55fr)
      minmax(8rem, 0.7fr)
      minmax(7rem, 0.55fr)
      4.5rem;
  }

  .policy-rule-grid {
    grid-template-areas: "identity match action state actions";
    grid-template-columns:
      minmax(8.5rem, 0.8fr)
      minmax(10rem, 1.25fr)
      minmax(10rem, 1.05fr)
      minmax(7.5rem, 0.75fr)
      4.5rem;
  }

  .policy-data-label {
    display: none;
  }

  .policy-data-actions {
    flex-wrap: nowrap;
  }
}

@media (pointer: coarse) {
  .policy-data-actions :deep(.n-button) {
    min-width: 2.75rem;
    min-height: 2.75rem;
  }
}

.workbench-section-header {
  flex-direction: column;
  align-items: stretch;
}

.workbench-section-header > :deep(.n-button),
.workbench-section-actions {
  width: 100%;
}

.workbench-section-actions > :deep(.n-button) {
  flex: 1 1 auto;
}

@media (min-width: 640px) {
  .workbench-section-header {
    flex-direction: row;
    align-items: center;
  }

  .workbench-section-header > :deep(.n-button),
  .workbench-section-actions {
    width: auto;
  }

  .workbench-section-actions > :deep(.n-button) {
    flex: none;
  }
}

@media (min-width: 900px) {
  .policy-filter-bar {
    grid-template-columns: minmax(0, 1fr) 12rem auto;
    align-items: end;
  }
}
</style>
