<script setup lang="ts">
import { toJsonString } from "@bufbuild/protobuf";
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch, type HTMLAttributes } from "vue";
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from "vue-router";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Search as SearchIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { X as XIcon } from "@lucide/vue";
import { NButton, NCheckbox, NDrawer, NDrawerContent, NInput, NInputNumber, NTabPane, NTabs, NTag } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
import EmptyState from "@/components/EmptyState.vue";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import TrafficPolicyExecutionOrderStrip from "@/components/traffic-policy/TrafficPolicyExecutionOrderStrip.vue";
import TrafficPolicyRequestPlayground from "@/components/traffic-policy/TrafficPolicyRequestPlayground.vue";
import PublicAccessControlSettings from "@/components/traffic-policy/PublicAccessControlSettings.vue";
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
  PublicCacheSettings,
  PublicPolicyMatchRule,
  PublicRateLimitRule,
  PublicRoute,
  PublicRouteTarget,
  PublicTrafficShaperRule,
  PublicWafCaptchaProvider,
  PublicWafRule,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { PublicPolicyMatchRuleSchema } from "@/gen/proto/p2pstream/v1/management_pb";

const policySectionKeys = ["rate-limits", "waf", "access", "cache", "traffic-shaper"] as const;
type PolicySectionKey = typeof policySectionKeys[number];
type PolicySectionMeta = {
  key: PolicySectionKey;
  label: string;
  description: string;
};
type PolicyFilterStatus = "all" | "enabled" | "disabled";
type PolicyFilter = {
  text: string;
  status: PolicyFilterStatus;
};
type CacheSettingsDraft = {
  enabled: boolean;
  maxDiskMiB: number;
  maxMemoryMiB: number;
  hotObjectKiB: number;
  maxEntries: number;
  cleanupIntervalSeconds: number;
};
type CacheSettingsField = Exclude<keyof CacheSettingsDraft, "enabled">;

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
const accessPolicies = computed(() => config.value?.accessPolicies ?? []);
const enabledAccessPolicies = computed(() => accessPolicies.value.filter((policy) => policy.enabled).length);
const enabledCacheRules = computed(() => cacheRules.value.filter((rule) => rule.enabled).length);
const enabledTrafficShapers = computed(() => trafficShaperRules.value.filter((rule) => rule.enabled).length);
const policyFilters = reactive<Record<PolicySectionKey, PolicyFilter>>({
  "rate-limits": { text: "", status: "all" },
  waf: { text: "", status: "all" },
  access: { text: "", status: "all" },
  cache: { text: "", status: "all" },
  "traffic-shaper": { text: "", status: "all" },
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
  { key: "access", label: "Access", description: "route identity and group check", icon: "route" as const, tags: countTags(enabledAccessPolicies.value) },
  { key: "traffic-shaper", label: "Traffic Shaper", description: "first matching bandwidth budget", icon: "traffic-shaper" as const, tags: countTags(enabledTrafficShapers.value) },
  { key: "route", label: "Route / Target", description: "selected before cache", icon: "route" as const },
  { key: "cache", label: "Cache", description: "first matching cacheable request", icon: "cache" as const, tags: countTags(enabledCacheRules.value) },
  { key: "response", label: "Response", description: "upstream, cached, or terminal", icon: "response" as const },
]);
const executionOrderSummary = computed(() => executionStages.value.map((stage) => stage.label).join(" → "));
const playgroundStages = computed<TrafficPolicyPlaygroundStage[]>(() => buildTrafficPolicyPlaygroundStages({
  rateLimitRules: rateLimitRules.value,
  trafficShaperRules: trafficShaperRules.value,
  wafRules: wafRules.value,
  wafCaptchaProviders: wafCaptchaProviders.value,
  cacheSettings: cacheSettings.value ?? undefined,
  cacheRules: cacheRules.value,
}, previewRequest.value));
const filteredRateLimitRules = computed(() => filterPolicyRules(rateLimitRules.value, "rate-limit", "rate-limits", rateLimitSearchText));
const filteredWafRules = computed(() => filterPolicyRules(wafRules.value, "waf", "waf", wafSearchText));
const filteredCacheRules = computed(() => filterPolicyRules(cacheRules.value, "cache", "cache", cacheSearchText));
const filteredTrafficShaperRules = computed(() => filterPolicyRules(trafficShaperRules.value, "traffic-shaper", "traffic-shaper", trafficShaperSearchText));
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

const cacheSettingsForm = reactive<CacheSettingsDraft>({
  enabled: true,
  maxDiskMiB: 1024,
  maxMemoryMiB: 128,
  hotObjectKiB: 256,
  maxEntries: 100000,
  cleanupIntervalSeconds: 60,
});
const cacheSettingsDirty = ref(false);
let cacheSettingsSyncing = false;

const policySections: readonly PolicySectionMeta[] = [
  {
    key: "rate-limits",
    label: "Rate Limits",
    description: "Throttle traffic based on request rate per client or route.",
  },
  {
    key: "waf",
    label: "WAF",
    description: "Block, challenge, or queue matching application traffic before it reaches routes.",
  },
  {
    key: "access",
    label: "Access",
    description: "Authenticate protected routes and enforce identity group membership.",
  },
  {
    key: "cache",
    label: "Cache",
    description: "Cache public static files on the proxy after routing while keeping WAF, rate limits, and shaping active.",
  },
  {
    key: "traffic-shaper",
    label: "Traffic Shaper",
    description: "Limit bandwidth consumption per request or client.",
  },
];
const activePolicySection = computed<PolicySectionKey>(() => normalizePolicySection(route.params.section));
const activePolicyMeta = computed(() => (
  policySections.find((section) => section.key === activePolicySection.value) ?? policySections[0]
));
const cacheSettingsErrors = computed<Record<CacheSettingsField, string>>(() => ({
  maxDiskMiB: positiveWholeNumberError(cacheSettingsForm.maxDiskMiB, "Disk budget", "MiB"),
  maxMemoryMiB: positiveWholeNumberError(cacheSettingsForm.maxMemoryMiB, "Memory budget", "MiB"),
  hotObjectKiB: hotObjectLimitError(),
  maxEntries: maxEntriesError(),
  cleanupIntervalSeconds: cleanupIntervalError(),
}));
const cacheSettingsDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  return Object.values(cacheSettingsErrors.value).find(Boolean) ?? "";
});
const cacheSettingsSaveDisabledReason = computed(() => (
  cacheSettingsDisabledReason.value || (!cacheSettingsDirty.value ? "No unsaved cache settings." : "")
));

watch(cacheSettings, (settings) => {
  if (!cacheSettingsDirty.value) syncCacheSettingsForm(cacheSettingsDraftFromProto(settings));
}, { immediate: true });
watch(cacheSettingsForm, () => {
  if (!cacheSettingsSyncing) cacheSettingsDirty.value = true;
}, { deep: true, flush: "sync" });

onBeforeRouteUpdate(async (to, from) => {
  if (from.params.section !== "cache" || to.params.section === "cache") return true;
  return confirmDiscardCacheSettings();
});
onBeforeRouteLeave(() => confirmDiscardCacheSettings());
onMounted(() => window.addEventListener("beforeunload", protectCacheSettingsBeforeUnload));
onBeforeUnmount(() => window.removeEventListener("beforeunload", protectCacheSettingsBeforeUnload));

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

async function run(action: () => Promise<void>, successMessage?: string): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action, successMessage);
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

function enabledCountLabel(enabled: number, total: number): string {
  return `${enabled.toString()} of ${total.toString()} enabled`;
}

function isPolicyFilterActive(section: PolicySectionKey): boolean {
  const filter = policyFilters[section];
  return Boolean(filter.text.trim()) || filter.status !== "all";
}

function policyFilterResultLabel(section: PolicySectionKey, visible: number, total: number): string {
  if (!isPolicyFilterActive(section)) return countLabel(total, "rule");
  return `${visible.toString()} of ${countLabel(total, "rule")} shown`;
}

function clearPolicyFilter(section: PolicySectionKey) {
  policyFilters[section].text = "";
  policyFilters[section].status = "all";
}

function filterPolicyRules<T extends { enabled: boolean; id: bigint }>(
  rules: readonly T[],
  kind: TrafficPolicyKind,
  section: PolicySectionKey,
  searchText: (rule: T) => string,
): T[] {
  const filter = policyFilters[section];
  const needle = filter.text.trim().toLowerCase();
  return rules.filter((rule) => {
    if (filter.status === "enabled" && !rule.enabled) return false;
    if (filter.status === "disabled" && rule.enabled) return false;
    if (!needle) return true;
    return searchText(rule).toLowerCase().includes(needle) ||
      policyWarningsForRule(kind, rule.id).some((warning) => warningLabel(warning).toLowerCase().includes(needle));
  });
}

function exactPolicyMatch(rule?: PublicPolicyMatchRule): string {
  if (!rule) return "Any request";
  return toJsonString(PublicPolicyMatchRuleSchema, rule, {
    alwaysEmitImplicit: true,
    prettySpaces: 0,
  });
}

function cacheSettingsDraftFromProto(settings?: PublicCacheSettings | null): CacheSettingsDraft {
  return {
    enabled: settings?.enabled ?? true,
    maxDiskMiB: bytesToMiB(settings?.maxDiskBytes ?? 1073741824n),
    maxMemoryMiB: bytesToMiB(settings?.maxMemoryBytes ?? 134217728n),
    hotObjectKiB: bytesToKiB(settings?.memoryHotObjectMaxBytes ?? 262144n),
    maxEntries: Number(settings?.maxEntries ?? 100000n),
    cleanupIntervalSeconds: Math.max(1, Math.round(Number(settings?.cleanupIntervalMillis ?? 60000n) / 1000)),
  };
}

function syncCacheSettingsForm(draft: CacheSettingsDraft) {
  cacheSettingsSyncing = true;
  Object.assign(cacheSettingsForm, draft);
  cacheSettingsDirty.value = false;
  cacheSettingsSyncing = false;
}

function resetCacheSettings() {
  syncCacheSettingsForm(cacheSettingsDraftFromProto(cacheSettings.value));
}

function positiveWholeNumberError(value: number, label: string, unit = ""): string {
  const suffix = unit ? ` ${unit}` : "";
  if (!Number.isFinite(value) || value < 1) return `${label} must be at least 1${suffix}.`;
  if (!Number.isSafeInteger(value)) return unit
    ? `${label} must be a whole number of ${unit}.`
    : `${label} must be a whole number.`;
  return "";
}

function hotObjectLimitError(): string {
  const basicError = positiveWholeNumberError(cacheSettingsForm.hotObjectKiB, "Hot-object limit", "KiB");
  if (basicError) return basicError;
  if (cacheSettingsForm.hotObjectKiB > cacheSettingsForm.maxMemoryMiB * 1024) {
    return "Hot-object limit cannot exceed the memory budget.";
  }
  return "";
}

function cleanupIntervalError(): string {
  const basicError = positiveWholeNumberError(cacheSettingsForm.cleanupIntervalSeconds, "Cleanup interval", "seconds");
  if (basicError) return basicError;
  if (cacheSettingsForm.cleanupIntervalSeconds > 3600) {
    return "Cleanup interval must be 3,600 seconds or less.";
  }
  return "";
}

function maxEntriesError(): string {
  const basicError = positiveWholeNumberError(cacheSettingsForm.maxEntries, "Max entries");
  if (basicError) return basicError;
  if (cacheSettingsForm.maxEntries > 10_000_000) return "Max entries must be 10,000,000 or less.";
  return "";
}

async function confirmDiscardCacheSettings(): Promise<boolean> {
  if (!cacheSettingsDirty.value) return true;
  const shouldDiscard = await confirm(
    "Discard unsaved cache settings?",
    "The cache storage values in this form have not been saved and will be lost.",
    "Discard changes",
  );
  if (shouldDiscard) resetCacheSettings();
  return shouldDiscard;
}

function protectCacheSettingsBeforeUnload(event: BeforeUnloadEvent) {
  if (!cacheSettingsDirty.value) return;
  event.preventDefault();
  event.returnValue = "";
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
  if (!await confirm(
    "Purge all cached objects?",
    "Every cached public-proxy object will be deleted. Requests will miss the cache until eligible responses are stored again. This cannot be undone.",
    "Purge all cached objects",
  )) return;
  await run(async () => {
    await managementClient.purgePublicCache({ all: true });
  }, "Cache purged");
}

async function saveCacheSettings() {
  if (cacheSettingsSaveDisabledReason.value) return;
  const ok = await run(async () => {
    await managementClient.updatePublicCacheSettings({
      enabled: cacheSettingsForm.enabled,
      maxDiskBytes: miBToBytes(cacheSettingsForm.maxDiskMiB),
      maxMemoryBytes: miBToBytes(cacheSettingsForm.maxMemoryMiB),
      memoryHotObjectMaxBytes: kiBToBytes(cacheSettingsForm.hotObjectKiB),
      maxEntries: BigInt(cacheSettingsForm.maxEntries),
      cleanupIntervalMillis: BigInt(cacheSettingsForm.cleanupIntervalSeconds * 1000),
    });
  }, "Cache settings saved");
  if (ok) resetCacheSettings();
}

async function deleteTrafficShaperRule(id: bigint) {
  if (!await confirm("Delete Traffic Shaper Rule", "This traffic-shaper rule will be permanently removed.")) return;
  await run(async () => {
    await managementClient.deletePublicTrafficShaperRule({ id });
  });
}
</script>

<template>
  <div v-if="dashboard" class="stack-lg">
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

    <details class="policy-execution-disclosure">
      <summary>
        <span class="policy-execution-disclosure__label">Execution order</span>
        <span class="policy-execution-disclosure__path">{{ executionOrderSummary }}</span>
      </summary>
      <TrafficPolicyExecutionOrderStrip
        :stages="executionStages"
        title=""
        description="Rules run in priority order. Lower priorities run first; ties fall back to rule ID."
      />
    </details>

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
        :tab="`Rate Limits · ${enabledRateLimitRules}/${rateLimitRules.length}`"
        :tab-props="policyTabProps('rate-limits', `Rate Limits, ${enabledCountLabel(enabledRateLimitRules, rateLimitRules.length)}`)"
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
      <div class="policy-local-filter" aria-label="Filter rate-limit rules">
        <NInput
          v-model:value="policyFilters['rate-limits'].text"
          size="small"
          clearable
          placeholder="Filter rate-limit rules"
          :input-props="{ 'aria-label': 'Filter rate-limit rules by name, match, key, priority, or warning' }"
        >
          <template #prefix><SearchIcon class="icon-sm" /></template>
        </NInput>
        <AccessibleSelect
          v-model:value="policyFilters['rate-limits'].status"
          accessible-label="Filter rate-limit rules by state"
          size="small"
          :options="policyFilterStatusOptions"
        />
        <NButton
          v-if="isPolicyFilterActive('rate-limits')"
          quaternary
          size="small"
          @click="clearPolicyFilter('rate-limits')"
        >
          <template #icon><XIcon class="icon-sm" /></template>
          Clear filters
        </NButton>
        <p class="policy-filter-result" aria-live="polite">
          {{ policyFilterResultLabel('rate-limits', filteredRateLimitRules.length, rateLimitRules.length) }}
        </p>
      </div>
      <div v-if="filteredRateLimitRules.length" class="policy-data-table" role="table" aria-label="Rate-limit rules">
        <div role="rowgroup">
          <div class="policy-data-header policy-rule-grid" role="row">
            <span class="policy-grid-identity" role="columnheader">Rule</span>
            <span class="policy-grid-match" role="columnheader">Match &amp; key</span>
            <span class="policy-grid-action" role="columnheader">Budget &amp; algorithm</span>
            <span class="policy-grid-state" role="columnheader">Order &amp; state</span>
            <span class="policy-grid-actions" role="columnheader">Actions</span>
          </div>
        </div>
        <div class="policy-data-rows" role="rowgroup">
          <div
            v-for="rule in filteredRateLimitRules"
            :key="rule.id.toString()"
            class="policy-data-row policy-rule-grid"
            role="row"
          >
            <div class="policy-data-cell policy-grid-identity" data-label="Rule" role="cell">
              <span class="policy-data-label">Rule</span>
              <p class="policy-data-primary" dir="auto" :title="rule.name">{{ rule.name }}</p>
              <p class="policy-data-secondary mono-text">Rule #{{ rule.id.toString() }}</p>
              <details class="policy-exact-disclosure">
                <summary>Exact values</summary>
                <dl>
                  <div><dt>Name</dt><dd dir="auto">{{ rule.name }}</dd></div>
                  <div><dt>Match rule</dt><dd><code dir="ltr">{{ exactPolicyMatch(rule.matchRule) }}</code></dd></div>
                  <div><dt>Key</dt><dd dir="auto">{{ rateLimitKeySummary(rule) }}</dd></div>
                  <div><dt>Budget</dt><dd dir="auto">{{ rateLimitRuleSummary(rule) }}</dd></div>
                </dl>
              </details>
            </div>
            <div class="policy-data-cell policy-grid-match" data-label="Match & key" role="cell">
              <span class="policy-data-label">Match &amp; key</span>
              <p class="policy-data-value" dir="auto" :title="publicPolicyMatchSummary(rule)">{{ publicPolicyMatchSummary(rule) }}</p>
              <p class="policy-data-secondary mono-text" dir="auto" :title="rateLimitKeySummary(rule)">Key {{ rateLimitKeySummary(rule) }}</p>
            </div>
            <div class="policy-data-cell policy-grid-action" data-label="Budget & algorithm" role="cell">
              <span class="policy-data-label">Budget &amp; algorithm</span>
              <NTag size="small" :bordered="false" type="info">{{ rateLimitAlgorithmLabel(rule.algorithm) }}</NTag>
              <p class="policy-data-secondary mono-text" :title="rateLimitRuleSummary(rule)">{{ rateLimitRuleSummary(rule) }}</p>
              <p class="policy-data-secondary">Response {{ rule.responseStatusCode.toString() }}</p>
            </div>
            <div class="policy-data-cell policy-grid-state" data-label="Order & state" role="cell">
              <span class="policy-data-label">Order &amp; state</span>
              <div class="policy-data-tags">
                <NTag size="small" :bordered="false" type="info">P{{ rule.priority.toString() }}</NTag>
                <NTag size="small" :bordered="false" :type="naiveTagType(rule.enabled ? 'success' : 'warning')">
                  {{ rule.enabled ? 'Enabled' : 'Disabled' }}
                </NTag>
              </div>
              <div v-if="visiblePolicyWarningsForRule('rate-limit', rule.id).length" class="policy-data-tags">
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
            </div>
            <div class="policy-data-cell policy-data-actions policy-grid-actions" data-label="Actions" role="cell">
              <span class="policy-data-label">Actions</span>
              <NButton secondary size="small" aria-label="Edit rate-limit rule" title="Edit rate-limit rule" @click="editRateLimitRule(rule.id)">
                <template #icon><PencilIcon class="icon-sm" /></template>
              </NButton>
              <NButton type="error" size="small" aria-label="Delete rate-limit rule" title="Delete rate-limit rule" @click="deleteRateLimitRule(rule.id)">
                <template #icon><TrashIcon class="icon-sm" /></template>
              </NButton>
            </div>
          </div>
        </div>
      </div>
      <div class="divided-list">
        <EmptyState
          v-if="rateLimitRules.length && !filteredRateLimitRules.length && isPolicyFilterActive('rate-limits')"
          title="No matching rate-limit rules"
          description="Clear or adjust the filters to show more rules."
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
        :tab="`WAF · ${enabledWafRules}/${wafRules.length}`"
        :tab-props="policyTabProps('waf', `WAF, ${enabledCountLabel(enabledWafRules, wafRules.length)}`)"
      >
    <div class="stack-lg">
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
      <div class="policy-local-filter" aria-label="Filter WAF rules">
        <NInput
          v-model:value="policyFilters.waf.text"
          size="small"
          clearable
          placeholder="Filter WAF rules"
          :input-props="{ 'aria-label': 'Filter WAF rules by name, match, key, priority, action, or warning' }"
        >
          <template #prefix><SearchIcon class="icon-sm" /></template>
        </NInput>
        <AccessibleSelect
          v-model:value="policyFilters.waf.status"
          accessible-label="Filter WAF rules by state"
          size="small"
          :options="policyFilterStatusOptions"
        />
        <NButton v-if="isPolicyFilterActive('waf')" quaternary size="small" @click="clearPolicyFilter('waf')">
          <template #icon><XIcon class="icon-sm" /></template>
          Clear filters
        </NButton>
        <p class="policy-filter-result" aria-live="polite">
          {{ policyFilterResultLabel('waf', filteredWafRules.length, wafRules.length) }}
        </p>
      </div>
      <div class="divided-list">
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
                <p class="policy-data-primary" dir="auto" :title="rule.name">{{ rule.name }}</p>
                <p class="policy-data-secondary mono-text">Rule #{{ rule.id.toString() }}</p>
                <details class="policy-exact-disclosure">
                  <summary>Exact values</summary>
                  <dl>
                    <div><dt>Name</dt><dd dir="auto">{{ rule.name }}</dd></div>
                    <div><dt>Match rule</dt><dd><code dir="ltr">{{ exactPolicyMatch(rule.matchRule) }}</code></dd></div>
                    <div><dt>Key</dt><dd dir="auto">{{ rateLimitKeySummary(rule) }}</dd></div>
                    <div><dt>Action</dt><dd dir="auto">{{ wafRuleSummary(rule, wafCaptchaProviders) }}</dd></div>
                  </dl>
                </details>
              </div>
              <div class="policy-data-cell policy-grid-match" data-label="Match" role="cell">
                <span class="policy-data-label">Match</span>
                <p class="policy-data-value" dir="auto" :title="publicPolicyMatchSummary(rule)">{{ publicPolicyMatchSummary(rule) }}</p>
                <p class="policy-data-secondary mono-text" dir="auto" :title="rateLimitKeySummary(rule)">Key {{ rateLimitKeySummary(rule) }}</p>
              </div>
              <div class="policy-data-cell policy-grid-action" data-label="Action" role="cell">
                <span class="policy-data-label">Action</span>
                <div class="policy-data-tags">
                  <NTag size="small" :bordered="false" type="info">{{ wafActionLabel(rule.action) }}</NTag>
                  <NTag size="small" :bordered="false">{{ wafActivationLabel(rule.activationMode) }}</NTag>
                </div>
                <p class="policy-data-secondary mono-text" dir="auto" :title="wafRuleSummary(rule, wafCaptchaProviders)">{{ wafRuleSummary(rule, wafCaptchaProviders) }}</p>
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
        <div v-if="wafCaptchaProviders.length" class="policy-list-heading policy-list-heading--secondary">
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
                <p class="policy-data-primary" dir="auto" :title="provider.name">{{ provider.name }}</p>
                <p class="policy-data-secondary mono-text" dir="auto" :title="provider.siteKey">{{ provider.siteKey }}</p>
                <details class="policy-exact-disclosure">
                  <summary>Exact values</summary>
                  <dl>
                    <div><dt>Name</dt><dd dir="auto">{{ provider.name }}</dd></div>
                    <div><dt>Site key</dt><dd dir="auto">{{ provider.siteKey }}</dd></div>
                  </dl>
                </details>
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
        <EmptyState
          v-if="wafRules.length && !filteredWafRules.length && isPolicyFilterActive('waf')"
          title="No matching WAF rules"
          description="Clear or adjust the filters to show more rules."
        />
        <EmptyState
          v-if="!wafRules.length"
          title="No WAF rules configured"
          description="WAF rules can block, challenge, or queue selected traffic before rate limits, shapers, routes, and targets."
          action-label="Add WAF Rule"
          @action="openAddWafRuleModal"
        />
      </div>
    </section>
    <PublicVisitorIdentitySettings :config="config" />
    </div>
      </NTabPane>

      <NTabPane
        id="traffic-policy-panel-access"
        name="access"
        role="tabpanel"
        aria-labelledby="traffic-policy-tab-access"
        :tab="`Access · ${enabledAccessPolicies}/${accessPolicies.length}`"
        :tab-props="policyTabProps('access', `Access, ${enabledCountLabel(enabledAccessPolicies, accessPolicies.length)}`)"
      >
        <div class="stack-lg">
          <PublicAccessControlSettings :config="config" />
        </div>
      </NTabPane>

      <NTabPane
        id="traffic-policy-panel-cache"
        name="cache"
        role="tabpanel"
        aria-labelledby="traffic-policy-tab-cache"
        :tab="`Cache · ${enabledCacheRules}/${cacheRules.length}`"
        :tab-props="policyTabProps('cache', `Cache, ${enabledCountLabel(enabledCacheRules, cacheRules.length)}`)"
      >
    <div class="stack-lg">
    <section class="surface-card hide-overflow">
      <div class="workbench-section-header divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
        <div>
          <h2 class="copy-base weight-semibold">Cache rules</h2>
          <p class="margin-top-xs copy-sm muted-text">Choose which eligible public responses are stored and how their cache keys behave.</p>
        </div>
        <NButton type="primary" size="small" @click="openAddCacheRuleModal">
          <template #icon><PlusIcon class="icon-sm" /></template>
          Add Cache Rule
        </NButton>
      </div>
      <div class="policy-local-filter" aria-label="Filter cache rules">
        <NInput
          v-model:value="policyFilters.cache.text"
          size="small"
          clearable
          placeholder="Filter cache rules"
          :input-props="{ 'aria-label': 'Filter cache rules by name, match, query key, priority, or warning' }"
        >
          <template #prefix><SearchIcon class="icon-sm" /></template>
        </NInput>
        <AccessibleSelect
          v-model:value="policyFilters.cache.status"
          accessible-label="Filter cache rules by state"
          size="small"
          :options="policyFilterStatusOptions"
        />
        <NButton v-if="isPolicyFilterActive('cache')" quaternary size="small" @click="clearPolicyFilter('cache')">
          <template #icon><XIcon class="icon-sm" /></template>
          Clear filters
        </NButton>
        <p class="policy-filter-result" aria-live="polite">
          {{ policyFilterResultLabel('cache', filteredCacheRules.length, cacheRules.length) }}
        </p>
      </div>
      <div v-if="filteredCacheRules.length" class="policy-data-table" role="table" aria-label="Cache rules">
        <div role="rowgroup">
          <div class="policy-data-header policy-rule-grid" role="row">
            <span class="policy-grid-identity" role="columnheader">Rule</span>
            <span class="policy-grid-match" role="columnheader">Match &amp; key</span>
            <span class="policy-grid-action" role="columnheader">TTL &amp; storage</span>
            <span class="policy-grid-state" role="columnheader">Order &amp; state</span>
            <span class="policy-grid-actions" role="columnheader">Actions</span>
          </div>
        </div>
        <div class="policy-data-rows" role="rowgroup">
          <div
            v-for="rule in filteredCacheRules"
            :key="rule.id.toString()"
            class="policy-data-row policy-rule-grid"
            role="row"
          >
            <div class="policy-data-cell policy-grid-identity" data-label="Rule" role="cell">
              <span class="policy-data-label">Rule</span>
              <p class="policy-data-primary" dir="auto" :title="rule.name">{{ rule.name }}</p>
              <p class="policy-data-secondary mono-text">Rule #{{ rule.id.toString() }}</p>
              <details class="policy-exact-disclosure">
                <summary>Exact values</summary>
                <dl>
                  <div><dt>Name</dt><dd dir="auto">{{ rule.name }}</dd></div>
                  <div><dt>Match rule</dt><dd><code dir="ltr">{{ exactPolicyMatch(rule.matchRule) }}</code></dd></div>
                  <div><dt>Key</dt><dd dir="auto">{{ cacheQueryModeLabel(rule.queryMode) }}</dd></div>
                  <div><dt>TTL &amp; storage</dt><dd dir="auto">{{ cacheRuleSummary(rule) }}</dd></div>
                </dl>
              </details>
            </div>
            <div class="policy-data-cell policy-grid-match" data-label="Match & key" role="cell">
              <span class="policy-data-label">Match &amp; key</span>
              <p class="policy-data-value" dir="auto" :title="cacheRuleMatchSummary(rule)">{{ cacheRuleMatchSummary(rule) }}</p>
              <p class="policy-data-secondary mono-text" dir="auto" :title="cacheQueryModeLabel(rule.queryMode)">{{ cacheQueryModeLabel(rule.queryMode) }}</p>
            </div>
            <div class="policy-data-cell policy-grid-action" data-label="TTL & storage" role="cell">
              <span class="policy-data-label">TTL &amp; storage</span>
              <div class="policy-data-tags">
                <NTag size="small" :bordered="false" type="info">{{ cacheTtlModeLabel(rule.ttlMode) }}</NTag>
                <NTag size="small" :bordered="false">{{ cacheScopeLabel(rule.scope) }}</NTag>
              </div>
              <p class="policy-data-secondary mono-text" dir="auto" :title="cacheRuleSummary(rule)">{{ cacheRuleSummary(rule) }}</p>
            </div>
            <div class="policy-data-cell policy-grid-state" data-label="Order & state" role="cell">
              <span class="policy-data-label">Order &amp; state</span>
              <div class="policy-data-tags">
                <NTag size="small" :bordered="false" type="info">P{{ rule.priority.toString() }}</NTag>
                <NTag size="small" :bordered="false" :type="naiveTagType(rule.enabled ? 'success' : 'warning')">
                  {{ rule.enabled ? 'Enabled' : 'Disabled' }}
                </NTag>
                <NTag v-if="rule.allowCookieRequests" size="small" :bordered="false" type="warning">Legacy cookie flag</NTag>
              </div>
              <div v-if="visiblePolicyWarningsForRule('cache', rule.id).length" class="policy-data-tags">
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
            </div>
            <div class="policy-data-cell policy-data-actions policy-grid-actions" data-label="Actions" role="cell">
              <span class="policy-data-label">Actions</span>
              <NButton secondary size="small" aria-label="Edit cache rule" title="Edit cache rule" @click="editCacheRule(rule.id)">
                <template #icon><PencilIcon class="icon-sm" /></template>
              </NButton>
              <NButton type="error" size="small" aria-label="Delete cache rule" title="Delete cache rule" @click="deleteCacheRule(rule.id)">
                <template #icon><TrashIcon class="icon-sm" /></template>
              </NButton>
            </div>
          </div>
        </div>
      </div>
      <div class="divided-list">
        <EmptyState
          v-if="cacheRules.length && !filteredCacheRules.length && isPolicyFilterActive('cache')"
          title="No matching cache rules"
          description="Clear or adjust the filters to show more rules."
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
    <section class="surface-card" aria-labelledby="cache-storage-heading">
      <div class="surface-card__header policy-cache-header">
        <div>
          <div class="layout-row wrap-items align-center space-sm">
            <h2 id="cache-storage-heading" class="copy-base weight-semibold">Cache storage &amp; operations</h2>
            <NTag v-if="cacheSettingsDirty" size="small" :bordered="false" type="warning">Unsaved changes</NTag>
            <NTag v-else size="small" :bordered="false" :type="cacheSettingsForm.enabled ? 'success' : 'default'">
              {{ cacheSettingsForm.enabled ? 'Storage enabled' : 'Storage disabled' }}
            </NTag>
          </div>
          <p class="margin-top-xs copy-sm muted-text">Set shared storage budgets independently from the rules that decide cache eligibility.</p>
        </div>
      </div>
      <form class="policy-cache-settings" @submit.prevent="saveCacheSettings">
        <div class="policy-cache-settings__enable">
          <div>
            <h3 class="copy-sm weight-semibold">Storage limits</h3>
            <p class="margin-top-xs copy-xs muted-text">Bodies use the public cache directory; metadata stays in SQLite.</p>
          </div>
          <NCheckbox v-model:checked="cacheSettingsForm.enabled">Cache storage enabled</NCheckbox>
        </div>
        <div class="policy-cache-fields">
          <label class="policy-cache-field">
            <span>Disk MiB</span>
            <NInputNumber
              v-model:value="cacheSettingsForm.maxDiskMiB"
              :show-button="false"
              size="small"
              :min="1"
              :step="1"
              :precision="0"
              :status="cacheSettingsErrors.maxDiskMiB ? 'error' : undefined"
            />
            <small v-if="cacheSettingsErrors.maxDiskMiB" role="alert">{{ cacheSettingsErrors.maxDiskMiB }}</small>
          </label>
          <label class="policy-cache-field">
            <span>Memory MiB</span>
            <NInputNumber
              v-model:value="cacheSettingsForm.maxMemoryMiB"
              :show-button="false"
              size="small"
              :min="1"
              :step="1"
              :precision="0"
              :status="cacheSettingsErrors.maxMemoryMiB ? 'error' : undefined"
            />
            <small v-if="cacheSettingsErrors.maxMemoryMiB" role="alert">{{ cacheSettingsErrors.maxMemoryMiB }}</small>
          </label>
          <label class="policy-cache-field">
            <span>Hot object KiB</span>
            <NInputNumber
              v-model:value="cacheSettingsForm.hotObjectKiB"
              :show-button="false"
              size="small"
              :min="1"
              :step="1"
              :precision="0"
              :status="cacheSettingsErrors.hotObjectKiB ? 'error' : undefined"
            />
            <small v-if="cacheSettingsErrors.hotObjectKiB" role="alert">{{ cacheSettingsErrors.hotObjectKiB }}</small>
          </label>
          <label class="policy-cache-field">
            <span>Max entries</span>
            <NInputNumber
              v-model:value="cacheSettingsForm.maxEntries"
              :show-button="false"
              size="small"
              :min="1"
              :max="10000000"
              :step="1"
              :precision="0"
              :status="cacheSettingsErrors.maxEntries ? 'error' : undefined"
            />
            <small v-if="cacheSettingsErrors.maxEntries" role="alert">{{ cacheSettingsErrors.maxEntries }}</small>
          </label>
          <label class="policy-cache-field">
            <span>Cleanup seconds</span>
            <NInputNumber
              v-model:value="cacheSettingsForm.cleanupIntervalSeconds"
              :show-button="false"
              size="small"
              :min="1"
              :max="3600"
              :step="1"
              :precision="0"
              :status="cacheSettingsErrors.cleanupIntervalSeconds ? 'error' : undefined"
            />
            <small v-if="cacheSettingsErrors.cleanupIntervalSeconds" role="alert">{{ cacheSettingsErrors.cleanupIntervalSeconds }}</small>
          </label>
        </div>
        <div class="policy-cache-settings__footer">
          <p class="copy-xs muted-text" role="status">
            {{ cacheSettingsDirty ? 'Automatic refresh is paused for this draft until you save or discard it.' : 'Settings match the latest server response.' }}
          </p>
          <div class="layout-row wrap-items space-sm">
            <NButton v-if="cacheSettingsDirty" secondary size="small" attr-type="button" :disabled="isBusy" @click="resetCacheSettings">
              Discard changes
            </NButton>
            <NButton
              type="primary"
              size="small"
              attr-type="submit"
              :disabled="Boolean(cacheSettingsSaveDisabledReason)"
              :title="cacheSettingsSaveDisabledReason"
            >
              Save cache settings
            </NButton>
          </div>
        </div>
      </form>
      <div class="policy-cache-danger">
        <div>
          <h3>Purge cached objects</h3>
          <p>Delete every cached public-proxy object. Eligible requests will miss until their responses are stored again.</p>
        </div>
        <NButton type="error" size="small" :disabled="isBusy" @click="purgeAllCache">
          <template #icon><TrashIcon class="icon-sm" /></template>
          Purge all cached objects
        </NButton>
      </div>
    </section>
    </div>
      </NTabPane>

      <NTabPane
        id="traffic-policy-panel-traffic-shaper"
        name="traffic-shaper"
        role="tabpanel"
        aria-labelledby="traffic-policy-tab-traffic-shaper"
        :tab="`Traffic Shaper · ${enabledTrafficShapers}/${trafficShaperRules.length}`"
        :tab-props="policyTabProps('traffic-shaper', `Traffic Shaper, ${enabledCountLabel(enabledTrafficShapers, trafficShaperRules.length)}`)"
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
      <div class="policy-local-filter" aria-label="Filter traffic-shaper rules">
        <NInput
          v-model:value="policyFilters['traffic-shaper'].text"
          size="small"
          clearable
          placeholder="Filter traffic-shaper rules"
          :input-props="{ 'aria-label': 'Filter traffic-shaper rules by name, match, key, priority, budget, or warning' }"
        >
          <template #prefix><SearchIcon class="icon-sm" /></template>
        </NInput>
        <AccessibleSelect
          v-model:value="policyFilters['traffic-shaper'].status"
          accessible-label="Filter traffic-shaper rules by state"
          size="small"
          :options="policyFilterStatusOptions"
        />
        <NButton
          v-if="isPolicyFilterActive('traffic-shaper')"
          quaternary
          size="small"
          @click="clearPolicyFilter('traffic-shaper')"
        >
          <template #icon><XIcon class="icon-sm" /></template>
          Clear filters
        </NButton>
        <p class="policy-filter-result" aria-live="polite">
          {{ policyFilterResultLabel('traffic-shaper', filteredTrafficShaperRules.length, trafficShaperRules.length) }}
        </p>
      </div>
      <div v-if="filteredTrafficShaperRules.length" class="policy-data-table" role="table" aria-label="Traffic-shaper rules">
        <div role="rowgroup">
          <div class="policy-data-header policy-rule-grid" role="row">
            <span class="policy-grid-identity" role="columnheader">Rule</span>
            <span class="policy-grid-match" role="columnheader">Match &amp; key</span>
            <span class="policy-grid-action" role="columnheader">Budget &amp; scope</span>
            <span class="policy-grid-state" role="columnheader">Order &amp; state</span>
            <span class="policy-grid-actions" role="columnheader">Actions</span>
          </div>
        </div>
        <div class="policy-data-rows" role="rowgroup">
          <div
            v-for="rule in filteredTrafficShaperRules"
            :key="rule.id.toString()"
            class="policy-data-row policy-rule-grid"
            role="row"
          >
            <div class="policy-data-cell policy-grid-identity" data-label="Rule" role="cell">
              <span class="policy-data-label">Rule</span>
              <p class="policy-data-primary" dir="auto" :title="rule.name">{{ rule.name }}</p>
              <p class="policy-data-secondary mono-text">Rule #{{ rule.id.toString() }}</p>
              <details class="policy-exact-disclosure">
                <summary>Exact values</summary>
                <dl>
                  <div><dt>Name</dt><dd dir="auto">{{ rule.name }}</dd></div>
                  <div><dt>Match rule</dt><dd><code dir="ltr">{{ exactPolicyMatch(rule.matchRule) }}</code></dd></div>
                  <div><dt>Key</dt><dd dir="auto">{{ trafficShaperKeySummary(rule) }}</dd></div>
                  <div><dt>Budget</dt><dd dir="auto">{{ trafficShaperRuleSummary(rule) }} / {{ trafficShaperBudgetSummary(rule) }}</dd></div>
                </dl>
              </details>
            </div>
            <div class="policy-data-cell policy-grid-match" data-label="Match & key" role="cell">
              <span class="policy-data-label">Match &amp; key</span>
              <p class="policy-data-value" dir="auto" :title="publicPolicyMatchSummary(rule)">{{ publicPolicyMatchSummary(rule) }}</p>
              <p class="policy-data-secondary mono-text" dir="auto" :title="trafficShaperKeySummary(rule)">Key {{ trafficShaperKeySummary(rule) }}</p>
            </div>
            <div class="policy-data-cell policy-grid-action" data-label="Budget & scope" role="cell">
              <span class="policy-data-label">Budget &amp; scope</span>
              <NTag size="small" :bordered="false" type="info">{{ trafficShaperScopeLabel(rule.budgetScope) }}</NTag>
              <p class="policy-data-secondary mono-text" :title="trafficShaperRuleSummary(rule)">{{ trafficShaperRuleSummary(rule) }}</p>
              <p class="policy-data-secondary" :title="trafficShaperBudgetSummary(rule)">{{ trafficShaperBudgetSummary(rule) }}</p>
            </div>
            <div class="policy-data-cell policy-grid-state" data-label="Order & state" role="cell">
              <span class="policy-data-label">Order &amp; state</span>
              <div class="policy-data-tags">
                <NTag size="small" :bordered="false" type="info">P{{ rule.priority.toString() }}</NTag>
                <NTag size="small" :bordered="false" :type="naiveTagType(rule.enabled ? 'success' : 'warning')">
                  {{ rule.enabled ? 'Enabled' : 'Disabled' }}
                </NTag>
              </div>
              <div v-if="visiblePolicyWarningsForRule('traffic-shaper', rule.id).length" class="policy-data-tags">
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
            </div>
            <div class="policy-data-cell policy-data-actions policy-grid-actions" data-label="Actions" role="cell">
              <span class="policy-data-label">Actions</span>
              <NButton secondary size="small" aria-label="Edit traffic-shaper rule" title="Edit traffic-shaper rule" @click="editTrafficShaperRule(rule.id)">
                <template #icon><PencilIcon class="icon-sm" /></template>
              </NButton>
              <NButton type="error" size="small" aria-label="Delete traffic-shaper rule" title="Delete traffic-shaper rule" @click="deleteTrafficShaperRule(rule.id)">
                <template #icon><TrashIcon class="icon-sm" /></template>
              </NButton>
            </div>
          </div>
        </div>
      </div>
      <div class="divided-list">
        <EmptyState
          v-if="trafficShaperRules.length && !filteredTrafficShaperRules.length && isPolicyFilterActive('traffic-shaper')"
          title="No matching traffic-shaper rules"
          description="Clear or adjust the filters to show more rules."
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
.policy-execution-disclosure {
  min-width: 0;
  border-block: 1px solid var(--app-border-subtle);
}

.policy-execution-disclosure > summary {
  display: flex;
  min-height: 2.75rem;
  align-items: center;
  gap: 0.75rem;
  padding: 0.5rem 0.25rem;
  color: var(--app-text);
  cursor: pointer;
  list-style: none;
}

.policy-execution-disclosure > summary::-webkit-details-marker {
  display: none;
}

.policy-execution-disclosure > summary::after {
  margin-left: auto;
  color: var(--app-text-muted);
  content: "+";
  font-size: 1rem;
  line-height: 1;
}

.policy-execution-disclosure[open] > summary::after {
  content: "−";
}

.policy-execution-disclosure__label {
  flex: none;
  font-size: 0.8125rem;
  font-weight: 600;
}

.policy-execution-disclosure__path {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.policy-execution-disclosure :deep(.tp-order-strip) {
  border: 0;
  border-top: 1px solid var(--app-border-subtle);
  border-radius: 0;
}

.policy-tabs {
  min-width: 0;
}

.policy-tabs :deep(.n-tabs-nav) {
  margin-bottom: 0.75rem;
}

.policy-tabs :deep(.n-tab-pane) {
  padding-top: 0.25rem;
}

.policy-local-filter {
  display: grid;
  gap: 0.625rem;
  border-bottom: 1px solid var(--app-border-subtle);
  padding: 0.75rem 1rem;
  background: color-mix(in srgb, var(--app-panel-muted) 55%, var(--app-panel));
}

.policy-filter-result {
  align-self: center;
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.4;
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

.policy-list-heading--secondary {
  border-top: 1px solid var(--app-border-subtle);
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
  unicode-bidi: isolate;
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

.policy-exact-disclosure {
  max-width: 100%;
  margin-top: 0.125rem;
  color: var(--app-text-muted);
  font-size: 0.7rem;
}

.policy-exact-disclosure > summary {
  width: max-content;
  max-width: 100%;
  cursor: pointer;
  font-weight: 600;
  line-height: 1.4;
}

.policy-exact-disclosure dl {
  display: grid;
  gap: 0.375rem;
  width: min(30rem, 100%);
  margin: 0.5rem 0 0;
  padding: 0.5rem;
  border: 1px solid var(--app-border-subtle);
  background: var(--app-panel-muted);
}

.policy-exact-disclosure dl > div {
  display: grid;
  gap: 0.125rem;
  min-width: 0;
}

.policy-exact-disclosure dt {
  font-weight: 600;
}

.policy-exact-disclosure dd {
  min-width: 0;
  margin: 0;
  overflow-wrap: anywhere;
  color: var(--app-text);
  line-height: 1.45;
  unicode-bidi: isolate;
}

.policy-exact-disclosure code {
  white-space: pre-wrap;
  word-break: break-word;
}

.policy-cache-header {
  align-items: flex-start;
}

.policy-cache-settings {
  display: grid;
  gap: 1rem;
  padding: 1rem 1.25rem;
}

.policy-cache-settings__enable,
.policy-cache-settings__footer,
.policy-cache-danger {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
}

.policy-cache-fields {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr));
  gap: 0.75rem;
}

.policy-cache-field {
  display: grid;
  align-content: start;
  gap: 0.375rem;
  min-width: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-weight: 600;
}

.policy-cache-field small {
  color: var(--app-error);
  font-size: 0.7rem;
  font-weight: 400;
  line-height: 1.35;
}

.policy-cache-settings__footer {
  padding-top: 0.875rem;
  border-top: 1px solid var(--app-border-subtle);
}

.policy-cache-settings__footer > p {
  max-width: 64ch;
  margin: 0;
}

.policy-cache-danger {
  border-top: 1px solid color-mix(in srgb, var(--app-error) 28%, var(--app-border-subtle));
  padding: 1rem 1.25rem;
  background: color-mix(in srgb, var(--app-error) 5%, var(--app-panel));
}

.policy-cache-danger h3,
.policy-cache-danger p {
  margin: 0;
}

.policy-cache-danger h3 {
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
}

.policy-cache-danger p {
  max-width: 70ch;
  margin-top: 0.25rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.45;
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
  .policy-local-filter {
    grid-template-columns: minmax(12rem, 1fr) minmax(9rem, 11rem) auto auto;
    align-items: center;
  }

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

@media (max-width: 639px) {
  .policy-execution-disclosure__path {
    display: none;
  }

  .policy-cache-settings__enable,
  .policy-cache-settings__footer,
  .policy-cache-danger {
    align-items: stretch;
    flex-direction: column;
  }

  .policy-cache-settings__footer > div,
  .policy-cache-danger > :deep(.n-button) {
    width: 100%;
  }

  .policy-cache-settings__footer :deep(.n-button) {
    flex: 1 1 auto;
  }
}
</style>
