<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch, type HTMLAttributes } from "vue";
import { onBeforeRouteLeave, onBeforeRouteUpdate, RouterLink, useRoute, useRouter } from "vue-router";
import { ChevronRight as ChevronRightIcon } from "@lucide/vue";
import { MoreHorizontal as MoreIcon } from "@lucide/vue";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Search as SearchIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { X as XIcon } from "@lucide/vue";
import { NButton, NCheckbox, NDrawer, NDrawerContent, NDropdown, NInput, NInputNumber, NTabPane, NTabs, NTag, type DropdownOption } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
import EmptyState from "@/components/EmptyState.vue";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import TrafficPolicyExecutionOrderStrip from "@/components/traffic-policy/TrafficPolicyExecutionOrderStrip.vue";
import TrafficPolicyRequestPlayground from "@/components/traffic-policy/TrafficPolicyRequestPlayground.vue";
import PublicAccessControlSettings from "@/components/traffic-policy/PublicAccessControlSettings.vue";
import PublicVisitorIdentitySettings from "@/components/traffic-policy/PublicVisitorIdentitySettings.vue";
import PolicyExactValues, { type PolicyExactValue } from "@/components/traffic-policy/PolicyExactValues.vue";
import CacheStorageUsage from "@/components/traffic-policy/CacheStorageUsage.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import PageHeader from "@/components/ui/PageHeader.vue";
import SectionCard from "@/components/ui/SectionCard.vue";
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
  trafficShaperProtocolScopeLabel,
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
  PublicRetryRule,
  PublicRoute,
  PublicRouteTarget,
  PublicTrafficShaperRule,
  PublicWafCaptchaProvider,
  PublicWafRule,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { PublicRetryBodyMode, PublicRetryFailureMode, PublicRetryResponseBodyMode } from "@/gen/proto/p2pstream/v1/management_pb";

const policySectionKeys = ["rate-limits", "waf", "access", "cache", "retries", "traffic-shaper", "visitor-identity"] as const;
type PolicySectionKey = typeof policySectionKeys[number];
type PolicyFilterKey = Exclude<PolicySectionKey, "visitor-identity">;
type PolicySectionMeta = {
  key: PolicySectionKey;
  label: string;
  description: string;
};
type WafSubpageKey = "rules" | "captcha-providers";
type PolicyExactListKey = "rate-limit" | "waf" | "captcha-provider" | "cache" | "retry" | "traffic-shaper";
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
const retryRules = computed(() => [...(config.value?.retryRules ?? [])].sort((left, right) => {
  if (left.priority === right.priority) return left.id < right.id ? -1 : left.id > right.id ? 1 : 0;
  return left.priority < right.priority ? -1 : 1;
}));
const cacheSettings = computed(() => config.value?.cacheSettings ?? null);
const wafRules = computed(() => runtimeOrderedWafRules(config.value?.wafRules ?? []));
const wafCaptchaProviders = computed(() => config.value?.wafCaptchaProviders ?? []);
const trafficShaperRules = computed(() => runtimeOrderedTrafficShaperRules(config.value?.trafficShaperRules ?? []));
const enabledRateLimitRules = computed(() => rateLimitRules.value.filter((rule) => rule.enabled).length);
const enabledWafRules = computed(() => wafRules.value.filter((rule) => rule.enabled).length);
const accessPolicies = computed(() => config.value?.accessPolicies ?? []);
const enabledAccessPolicies = computed(() => accessPolicies.value.filter((policy) => policy.enabled).length);
const enabledCacheRules = computed(() => cacheRules.value.filter((rule) => rule.enabled).length);
const enabledRetryRules = computed(() => retryRules.value.filter((rule) => rule.enabled).length);
const enabledTrafficShapers = computed(() => trafficShaperRules.value.filter((rule) => rule.enabled).length);
const policyFilters = reactive<Record<PolicyFilterKey, PolicyFilter>>({
  "rate-limits": { text: "", status: "all" },
  waf: { text: "", status: "all" },
  access: { text: "", status: "all" },
  cache: { text: "", status: "all" },
  retries: { text: "", status: "all" },
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
  retryRules: retryRules.value,
  routeTargets: config.value?.routeTargets ?? [],
}));
const globalPolicyAttention = computed(() => policyAttention.value.filter((item) => item.ruleId === undefined && !item.ruleIds?.length));
const executionStages = computed(() => [
  { key: "precheck", label: "Path checks", description: "reserved endpoints + route security", icon: "listener" as const },
  { key: "waf", label: "WAF", description: "first matching enforceable rule", icon: "waf" as const, tags: countTags(enabledWafRules.value) },
  { key: "rate-limit", label: "Rate limits", description: "all matching request budgets", icon: "rate-limit" as const, tags: countTags(enabledRateLimitRules.value) },
  { key: "access", label: "Access", description: "route identity and group check", icon: "route" as const, tags: countTags(enabledAccessPolicies.value) },
  { key: "traffic-shaper", label: "Traffic shaper", description: "first matching bandwidth budget", icon: "traffic-shaper" as const, tags: countTags(enabledTrafficShapers.value) },
  { key: "route", label: "Route / target", description: "selected before cache", icon: "route" as const },
  { key: "cache", label: "Cache", description: "first matching cacheable request", icon: "cache" as const, tags: countTags(enabledCacheRules.value) },
  { key: "retry", label: "Retries", description: "transport failures + selected statuses", icon: "retry" as const, tags: countTags(enabledRetryRules.value) },
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
  retryRules: retryRules.value,
  routeTargets: config.value?.routeTargets ?? [],
}, previewRequest.value));
const filteredRateLimitRules = computed(() => filterPolicyRules(rateLimitRules.value, "rate-limit", "rate-limits", rateLimitSearchText));
const filteredWafRules = computed(() => filterPolicyRules(wafRules.value, "waf", "waf", wafSearchText));
const filteredCacheRules = computed(() => filterPolicyRules(cacheRules.value, "cache", "cache", cacheSearchText));
const filteredRetryRules = computed(() => {
  const filter = policyFilters.retries;
  const needle = filter.text.trim().toLowerCase();
  return retryRules.value.filter((rule) => {
    if (filter.status === "enabled" && !rule.enabled) return false;
    if (filter.status === "disabled" && rule.enabled) return false;
    return !needle || retrySearchText(rule).toLowerCase().includes(needle);
  });
});
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
const visitorIdentitySettings = ref<InstanceType<typeof PublicVisitorIdentitySettings> | null>(null);
const openExactValues = reactive(new Set<string>());
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
    label: "Rate limits",
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
    key: "retries",
    label: "Retries",
    description: "Retry eligible upstream failures through a different agent in the same route target.",
  },
  {
    key: "traffic-shaper",
    label: "Traffic shaper",
    description: "Limit bandwidth consumption per request or client.",
  },
  {
    key: "visitor-identity",
    label: "Visitor identity",
    description: "Resolve the visitor address and country before WAF, rate limits, traffic shaping, captcha verification, and upstream forwarding.",
  },
];
const wafSubpages: readonly { key: WafSubpageKey; label: string; path: string; description: string }[] = [
  {
    key: "rules",
    label: "Rules",
    path: "/policies/waf",
    description: "Block, challenge, or queue matching application traffic before it reaches routes.",
  },
  {
    key: "captcha-providers",
    label: "Captcha providers",
    path: "/policies/waf/captcha-providers",
    description: "Credentials that WAF captcha rules use to challenge visitors.",
  },
];
const activePolicySection = computed<PolicySectionKey>(() => normalizePolicySection(route.params.section));
const activeWafSubpage = computed<WafSubpageKey>(() => (
  routeSectionParam(route.params.section) === "waf/captcha-providers" ? "captcha-providers" : "rules"
));
const policyPrimaryActions: Partial<Record<PolicySectionKey, { label: string; run: () => void }>> = {
  "rate-limits": { label: "Add rate-limit rule", run: openAddRateLimitRuleModal },
  waf: { label: "Add WAF rule", run: openAddWafRuleModal },
  cache: { label: "Add cache rule", run: openAddCacheRuleModal },
  retries: { label: "Add retry rule", run: openAddRetryRuleModal },
  "traffic-shaper": { label: "Add traffic shaper", run: openAddTrafficShaperRuleModal },
  "visitor-identity": { label: "Add custom source", run: openAddTrustedProxySource },
};
// Row menus hold the destructive action; the confirm dialog names what is deleted.
function deleteMenuOptions(label: string): DropdownOption[] {
  return [{ label, key: "delete", props: { role: "menuitem" } }];
}

const activePolicyPrimaryAction = computed(() => {
  if (activePolicySection.value === "waf" && activeWafSubpage.value === "captcha-providers") {
    return { label: "Add captcha provider", run: openAddWafCaptchaProviderModal };
  }
  return policyPrimaryActions[activePolicySection.value];
});
const activePolicyMeta = computed(() => (
  policySections.find((section) => section.key === activePolicySection.value) ?? policySections[0]
));
const activePolicyDescription = computed(() => (
  activePolicySection.value === "waf"
    ? wafSubpages.find((page) => page.key === activeWafSubpage.value)?.description ?? activePolicyMeta.value.description
    : activePolicyMeta.value.description
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

function routeSectionParam(value: unknown): string {
  const section = Array.isArray(value) ? value[0] : value;
  return typeof section === "string" ? section : "";
}

function normalizePolicySection(value: unknown): PolicySectionKey {
  const section = routeSectionParam(value);
  if (section === "waf/captcha-providers") return "waf";
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
  return count ? [{ label: count.toString(), tone: "neutral" as const }] : [];
}

function countLabel(count: number, singular: string, plural = `${singular}s`): string {
  return `${count.toString()} ${count === 1 ? singular : plural}`;
}

function enabledCountLabel(enabled: number, total: number): string {
  return `${enabled.toString()} of ${total.toString()} enabled`;
}

function isPolicyFilterActive(section: PolicyFilterKey): boolean {
  const filter = policyFilters[section];
  return Boolean(filter.text.trim()) || filter.status !== "all";
}

function policyFilterResultLabel(section: PolicyFilterKey, visible: number, total: number): string {
  if (!isPolicyFilterActive(section)) return countLabel(total, "rule");
  return `${visible.toString()} of ${countLabel(total, "rule")} shown`;
}

function clearPolicyFilter(section: PolicyFilterKey) {
  policyFilters[section].text = "";
  policyFilters[section].status = "all";
}

function filterPolicyRules<T extends { enabled: boolean; id: bigint }>(
  rules: readonly T[],
  kind: TrafficPolicyKind,
  section: PolicyFilterKey,
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

function exactValuesId(list: PolicyExactListKey, id: bigint): string {
  return `policy-exact-${list}-${id.toString()}`;
}

function isExactValuesOpen(list: PolicyExactListKey, id: bigint): boolean {
  return openExactValues.has(exactValuesId(list, id));
}

function toggleExactValues(list: PolicyExactListKey, id: bigint) {
  const key = exactValuesId(list, id);
  if (openExactValues.has(key)) openExactValues.delete(key);
  else openExactValues.add(key);
}

function exactMatchValue(summary: string, matchRule?: PublicPolicyMatchRule): PolicyExactValue {
  const expression = matchRule?.celExpression?.trim() ?? "";
  return {
    label: "Match",
    value: summary,
    code: expression && expression !== "true" ? expression : undefined,
    wide: true,
  };
}

function exactOrderValues(rule: { priority: bigint; enabled: boolean }): PolicyExactValue[] {
  return [
    { label: "Priority", value: rule.priority.toString() },
    { label: "State", value: rule.enabled ? "Enabled" : "Disabled" },
  ];
}

function rateLimitExactValues(rule: PublicRateLimitRule): PolicyExactValue[] {
  return [
    { label: "Name", value: rule.name, wide: true },
    exactMatchValue(publicPolicyMatchSummary(rule), rule.matchRule),
    { label: "Key", value: rateLimitKeySummary(rule) },
    { label: "Budget", value: rateLimitRuleSummary(rule) },
    { label: "Algorithm", value: rateLimitAlgorithmLabel(rule.algorithm) },
    { label: "Response status", value: rule.responseStatusCode.toString() },
    ...exactOrderValues(rule),
  ];
}

function wafExactValues(rule: PublicWafRule): PolicyExactValue[] {
  return [
    { label: "Name", value: rule.name, wide: true },
    exactMatchValue(publicPolicyMatchSummary(rule), rule.matchRule),
    { label: "Key", value: rateLimitKeySummary(rule) },
    { label: "Action", value: wafRuleSummary(rule, wafCaptchaProviders.value) },
    { label: "Activation", value: wafActivationLabel(rule.activationMode) },
    ...exactOrderValues(rule),
  ];
}

function captchaProviderExactValues(provider: PublicWafCaptchaProvider): PolicyExactValue[] {
  return [
    { label: "Name", value: provider.name, wide: true },
    { label: "Site key", code: provider.siteKey || undefined, value: provider.siteKey ? undefined : "Not set", wide: true },
    { label: "Type", value: wafProviderLabel(provider.providerType) },
    { label: "Credential", value: provider.secretKeySet ? "Secret saved" : "Secret missing" },
    { label: "State", value: provider.enabled ? "Enabled" : "Disabled" },
  ];
}

function cacheExactValues(rule: PublicCacheRule): PolicyExactValue[] {
  return [
    { label: "Name", value: rule.name, wide: true },
    exactMatchValue(cacheRuleMatchSummary(rule), rule.matchRule),
    { label: "Key", value: cacheQueryModeLabel(rule.queryMode) },
    { label: "TTL & storage", value: cacheRuleSummary(rule) },
    { label: "TTL mode", value: cacheTtlModeLabel(rule.ttlMode) },
    { label: "Scope", value: cacheScopeLabel(rule.scope) },
    ...exactOrderValues(rule),
  ];
}

function retryExactValues(rule: PublicRetryRule): PolicyExactValue[] {
  return [
    { label: "Name", value: rule.name, wide: true },
    exactMatchValue(publicPolicyMatchSummary(rule), rule.matchRule),
    { label: "Methods", code: rule.methods.join(", ") || undefined, value: rule.methods.length ? undefined : "None" },
    { label: "Scope", value: retryScopeSummary(rule) },
    { label: "Retries", value: rule.maxRetries.toString() },
    { label: "Failure mode", value: retryFailureModeLabel(rule.failureMode) },
    { label: "Retry statuses", value: retryStatusSummary(rule) },
    { label: "Body replay", value: retryBodyModeLabel(rule) },
    { label: "Response completion", value: retryResponseBodyModeLabel(rule) },
    ...exactOrderValues(rule),
  ];
}

function trafficShaperExactValues(rule: PublicTrafficShaperRule): PolicyExactValue[] {
  return [
    { label: "Name", value: rule.name, wide: true },
    exactMatchValue(publicPolicyMatchSummary(rule), rule.matchRule),
    { label: "Key", value: trafficShaperKeySummary(rule) },
    { label: "Applies to", value: trafficShaperProtocolScopeLabel(rule.protocolScope) },
    { label: "Budget", value: `${trafficShaperRuleSummary(rule)} / ${trafficShaperBudgetSummary(rule)}` },
    { label: "Budget scope", value: trafficShaperScopeLabel(rule.budgetScope) },
    ...exactOrderValues(rule),
  ];
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
    warning.code !== "disabled-rule"
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
    case "retry-duplicate-risk": return "Duplicate risk";
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
    case "retry-duplicate-risk":
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

function retryFailureModeLabel(mode: PublicRetryFailureMode): string {
  return mode === PublicRetryFailureMode.PRE_RESPONSE_FAILURES
    ? "Before response headers"
    : "Connection establishment";
}

function retryBodyModeLabel(rule: PublicRetryRule): string {
  if (rule.bodyMode !== PublicRetryBodyMode.BUFFERED) return "No body buffer";
  return `Replay ≤ ${bytesToKiB(rule.maxReplayBodyBytes).toLocaleString()} KiB`;
}

function retryResponseBodyModeLabel(rule: PublicRetryRule): string {
  if (rule.responseBodyMode !== PublicRetryResponseBodyMode.BUFFERED) return "Response streaming";
  const waitSeconds = Math.max(1, Math.round(Number(rule.maxBufferedResponseWaitMillis || 30000n) / 1000));
  return `Protect response ≤ ${bytesToKiB(rule.maxBufferedResponseBodyBytes).toLocaleString()} KiB / ${waitSeconds.toLocaleString()} s`;
}

function retryStatusSummary(rule: PublicRetryRule): string {
  return rule.retryStatusCodes.length
    ? `HTTP ${rule.retryStatusCodes.map(String).join(", ")}`
    : "No status retries";
}

function retryMethodSummary(rule: PublicRetryRule): string {
  return rule.methods.length === 1 && rule.methods[0] === "*"
    ? "All supported methods"
    : rule.methods.join(", ");
}

function retryScopeSummary(rule: PublicRetryRule): string {
  const routeScope = rule.routeIds.length ? `${rule.routeIds.length.toString()} routes` : "all routes";
  const targetScope = rule.targetIds.length ? `${rule.targetIds.length.toString()} agent targets` : "all agent targets";
  return `${routeScope} / ${targetScope}`;
}

function retryHasDuplicateRisk(rule: PublicRetryRule): boolean {
  return rule.failureMode === PublicRetryFailureMode.PRE_RESPONSE_FAILURES ||
    rule.retryStatusCodes.length > 0 ||
    rule.responseBodyMode === PublicRetryResponseBodyMode.BUFFERED ||
    rule.methods.some((method) => !["GET", "HEAD", "OPTIONS"].includes(method));
}

function retrySearchText(rule: PublicRetryRule): string {
  return [
    rule.name,
    rule.priority.toString(),
    rule.enabled ? "enabled" : "disabled",
    retryMethodSummary(rule),
    retryFailureModeLabel(rule.failureMode),
    retryStatusSummary(rule),
    retryBodyModeLabel(rule),
    retryResponseBodyModeLabel(rule),
    retryScopeSummary(rule),
    publicPolicyMatchSummary(rule),
    retryHasDuplicateRisk(rule) ? "duplicate risk" : "safe methods",
  ].join(" ");
}

function trafficShaperSearchText(rule: PublicTrafficShaperRule): string {
  return [
    rule.name,
    rule.priority.toString(),
    rule.enabled ? "enabled" : "disabled",
    trafficShaperScopeLabel(rule.budgetScope),
    trafficShaperProtocolScopeLabel(rule.protocolScope),
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

function openAddTrustedProxySource() {
  visitorIdentitySettings.value?.openCreateProxy();
}

function openAddCacheRuleModal() {
  editorHost.value?.openCreateCacheRule();
}

function editCacheRule(id: bigint) {
  editorHost.value?.openCacheRule(id);
}

function openAddRetryRuleModal() {
  editorHost.value?.openCreateRetryRule();
}

function editRetryRule(id: bigint) {
  editorHost.value?.openRetryRule(id);
}

function openAddTrafficShaperRuleModal() {
  editorHost.value?.openCreateTrafficShaperRule();
}

function editTrafficShaperRule(id: bigint) {
  editorHost.value?.openTrafficShaperRule(id);
}

async function deleteRateLimitRule(id: bigint) {
  if (!await confirm("Delete rate limit rule", "This rate-limit rule will be permanently removed.")) return;
  await run(async () => {
    await managementClient.deletePublicRateLimitRule({ id });
  });
}

async function deleteWafRule(id: bigint) {
  if (!await confirm("Delete WAF rule", "This WAF rule will be permanently removed.")) return;
  await run(async () => {
    await managementClient.deletePublicWafRule({ id });
  });
}

async function deleteWafCaptchaProvider(id: bigint) {
  if (!await confirm("Delete captcha provider", "This captcha provider will be permanently removed. Captcha rules using it must be updated first.")) return;
  await run(async () => {
    await managementClient.deletePublicWafCaptchaProvider({ id });
  });
}

async function deleteCacheRule(id: bigint) {
  if (!await confirm("Delete cache rule", "This cache rule will be permanently removed. Existing cached objects for the rule may be purged separately.")) return;
  await run(async () => {
    await managementClient.deletePublicCacheRule({ id });
  });
}

async function deleteRetryRule(id: bigint) {
  if (!await confirm("Delete request retry rule", "This request retry rule will be permanently removed.")) return;
  await run(async () => {
    await managementClient.deletePublicRetryRule({ id });
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
  if (!await confirm("Delete traffic shaper rule", "This traffic-shaper rule will be permanently removed.")) return;
  await run(async () => {
    await managementClient.deletePublicTrafficShaperRule({ id });
  });
}
</script>

<template>
  <div v-if="dashboard" class="stack-lg">
    <PageHeader title="Traffic policy">
      <template #description>
        <span id="traffic-policy-page-description">{{ activePolicyDescription }}</span>
      </template>
      <template #actions>
        <NButton secondary size="small" @click="isPreviewOpen = true">
          <template #icon><SearchIcon class="icon-sm" /></template>
          Request tester
        </NButton>
        <NButton v-if="activePolicyPrimaryAction" type="primary" size="small" @click="activePolicyPrimaryAction.run">
          <template #icon><PlusIcon class="icon-sm" /></template>
          {{ activePolicyPrimaryAction.label }}
        </NButton>
      </template>
    </PageHeader>

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
        :tab="`Rate limits · ${enabledRateLimitRules}/${rateLimitRules.length}`"
        :tab-props="policyTabProps('rate-limits', `Rate limits, ${enabledCountLabel(enabledRateLimitRules, rateLimitRules.length)}`)"
      >
        <SectionCard flush>
          <div class="policy-list-toolbar">
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
          </div>
          <div v-if="filteredRateLimitRules.length" class="policy-data-table" role="table" aria-label="Rate-limit rules">
            <div role="rowgroup">
              <div class="policy-data-header policy-rule-grid" role="row">
                <span class="policy-grid-identity" role="columnheader">Rule</span>
                <span class="policy-grid-match" role="columnheader">Match &amp; key</span>
                <span class="policy-grid-action" role="columnheader">Budget &amp; algorithm</span>
                <span class="policy-grid-state" role="columnheader">Order &amp; state</span>
                <span class="policy-grid-actions" role="columnheader"><span class="visually-hidden">Actions</span></span>
              </div>
            </div>
            <div class="policy-data-rows" role="rowgroup">
              <template v-for="rule in filteredRateLimitRules" :key="rule.id.toString()">
                <div
                  class="policy-data-row policy-rule-grid"
                  role="row"
                >
                  <div class="policy-data-cell policy-grid-identity" data-label="Rule" role="cell">
                    <span class="policy-data-label">Rule</span>
                    <p class="policy-data-primary" dir="auto" :title="rule.name">{{ rule.name }}</p>
                    <p class="policy-data-secondary mono-text">Rule #{{ rule.id.toString() }}</p>
                    <button
                      type="button"
                      class="policy-exact-toggle"
                      :aria-expanded="isExactValuesOpen('rate-limit', rule.id)"
                      :aria-controls="isExactValuesOpen('rate-limit', rule.id) ? exactValuesId('rate-limit', rule.id) : undefined"
                      :aria-label="`Exact values for ${rule.name}`"
                      @click="toggleExactValues('rate-limit', rule.id)"
                    >
                      <ChevronRightIcon class="policy-exact-toggle__icon" aria-hidden="true" />
                      Exact values
                    </button>
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
                      <NTag size="small" :bordered="false">P{{ rule.priority.toString() }}</NTag>
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
                    <span class="visually-hidden">Actions</span>
                    <NButton secondary size="small" aria-label="Edit rate-limit rule" @click="editRateLimitRule(rule.id)">
                      <template #icon><PencilIcon class="icon-sm" /></template>
                      Edit
                    </NButton>
                    <NDropdown trigger="click" :options="deleteMenuOptions('Delete rate-limit rule')" @select="() => deleteRateLimitRule(rule.id)">
                      <NButton secondary size="small" :aria-label="`More actions for rate-limit rule ${rule.name}`">
                        <template #icon><MoreIcon class="icon-sm" /></template>
                        More
                      </NButton>
                    </NDropdown>
                  </div>
                </div>
                <PolicyExactValues
                  v-if="isExactValuesOpen('rate-limit', rule.id)"
                  :id="exactValuesId('rate-limit', rule.id)"
                  :label="`Exact values for ${rule.name}`"
                  :entries="rateLimitExactValues(rule)"
                />
              </template>
            </div>
          </div>
          <EmptyState
            v-if="rateLimitRules.length && !filteredRateLimitRules.length && isPolicyFilterActive('rate-limits')"
            title="No matching rate-limit rules"
            description="Clear or adjust the filters to show more rules."
          />
          <EmptyState
            v-if="!rateLimitRules.length"
            title="No rate-limit rules configured"
            description="Rate limits protect your route targets from excessive traffic by throttling requests per client or route."
            action-label="Add rate-limit rule"
            @action="openAddRateLimitRuleModal"
          />
        </SectionCard>
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
          <nav class="policy-subnav" aria-label="WAF pages">
            <RouterLink
              v-for="page in wafSubpages"
              :key="page.key"
              :to="page.path"
              class="policy-subnav__link"
              :class="{ 'policy-subnav__link--active': activeWafSubpage === page.key }"
              :aria-current="activeWafSubpage === page.key ? 'page' : undefined"
            >
              {{ page.label }}
            </RouterLink>
          </nav>

          <SectionCard v-if="activeWafSubpage === 'rules'" flush>
            <div class="policy-list-toolbar">
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
            </div>
            <div v-if="filteredWafRules.length" class="policy-data-table" role="table" aria-label="WAF rules">
              <div role="rowgroup">
                <div class="policy-data-header policy-rule-grid" role="row">
                  <span class="policy-grid-identity" role="columnheader">Rule</span>
                  <span class="policy-grid-match" role="columnheader">Match</span>
                  <span class="policy-grid-action" role="columnheader">Action</span>
                  <span class="policy-grid-state" role="columnheader">Order &amp; state</span>
                  <span class="policy-grid-actions" role="columnheader"><span class="visually-hidden">Actions</span></span>
                </div>
              </div>
              <div class="policy-data-rows" role="rowgroup">
                <template v-for="rule in filteredWafRules" :key="`rule-${rule.id.toString()}`">
                  <div
                    class="policy-data-row policy-rule-grid"
                    role="row"
                  >
                    <div class="policy-data-cell policy-grid-identity" data-label="Rule" role="cell">
                      <span class="policy-data-label">Rule</span>
                      <p class="policy-data-primary" dir="auto" :title="rule.name">{{ rule.name }}</p>
                      <p class="policy-data-secondary mono-text">Rule #{{ rule.id.toString() }}</p>
                      <button
                        type="button"
                        class="policy-exact-toggle"
                        :aria-expanded="isExactValuesOpen('waf', rule.id)"
                        :aria-controls="isExactValuesOpen('waf', rule.id) ? exactValuesId('waf', rule.id) : undefined"
                        :aria-label="`Exact values for ${rule.name}`"
                        @click="toggleExactValues('waf', rule.id)"
                      >
                        <ChevronRightIcon class="policy-exact-toggle__icon" aria-hidden="true" />
                        Exact values
                      </button>
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
                        <NTag size="small" :bordered="false">P{{ rule.priority.toString() }}</NTag>
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
                      <span class="visually-hidden">Actions</span>
                      <NButton secondary size="small" aria-label="Edit WAF rule" @click="editWafRule(rule.id)">
                        <template #icon><PencilIcon class="icon-sm" /></template>
                        Edit
                      </NButton>
                      <NDropdown trigger="click" :options="deleteMenuOptions('Delete WAF rule')" @select="() => deleteWafRule(rule.id)">
                        <NButton secondary size="small" :aria-label="`More actions for WAF rule ${rule.name}`">
                          <template #icon><MoreIcon class="icon-sm" /></template>
                          More
                        </NButton>
                      </NDropdown>
                    </div>
                  </div>
                  <PolicyExactValues
                    v-if="isExactValuesOpen('waf', rule.id)"
                    :id="exactValuesId('waf', rule.id)"
                    :label="`Exact values for ${rule.name}`"
                    :entries="wafExactValues(rule)"
                  />
                </template>
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
              action-label="Add WAF rule"
              @action="openAddWafRuleModal"
            />
          </SectionCard>

          <SectionCard v-if="activeWafSubpage === 'captcha-providers'" flush>
            <div v-if="wafCaptchaProviders.length" class="policy-data-table" role="table" aria-label="Captcha providers">
              <div role="rowgroup">
                <div class="policy-data-header policy-provider-grid" role="row">
                  <span class="policy-grid-identity" role="columnheader">Provider</span>
                  <span class="policy-grid-type" role="columnheader">Type</span>
                  <span class="policy-grid-secret" role="columnheader">Credential</span>
                  <span class="policy-grid-state" role="columnheader">State</span>
                  <span class="policy-grid-actions" role="columnheader"><span class="visually-hidden">Actions</span></span>
                </div>
              </div>
              <div class="policy-data-rows" role="rowgroup">
                <template v-for="provider in wafCaptchaProviders" :key="`provider-${provider.id.toString()}`">
                  <div
                    class="policy-data-row policy-provider-grid"
                    role="row"
                  >
                    <div class="policy-data-cell policy-grid-identity" data-label="Provider" role="cell">
                      <span class="policy-data-label">Provider</span>
                      <p class="policy-data-primary" dir="auto" :title="provider.name">{{ provider.name }}</p>
                      <p class="policy-data-secondary mono-text" dir="auto" :title="provider.siteKey">{{ provider.siteKey }}</p>
                      <button
                        type="button"
                        class="policy-exact-toggle"
                        :aria-expanded="isExactValuesOpen('captcha-provider', provider.id)"
                        :aria-controls="isExactValuesOpen('captcha-provider', provider.id) ? exactValuesId('captcha-provider', provider.id) : undefined"
                        :aria-label="`Exact values for ${provider.name}`"
                        @click="toggleExactValues('captcha-provider', provider.id)"
                      >
                        <ChevronRightIcon class="policy-exact-toggle__icon" aria-hidden="true" />
                        Exact values
                      </button>
                    </div>
                    <div class="policy-data-cell policy-grid-type" data-label="Type" role="cell">
                      <span class="policy-data-label">Type</span>
                      <NTag size="small" :bordered="false">{{ wafProviderLabel(provider.providerType) }}</NTag>
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
                      <span class="visually-hidden">Actions</span>
                      <NButton secondary size="small" aria-label="Edit captcha provider" @click="editWafCaptchaProvider(provider)">
                        <template #icon><PencilIcon class="icon-sm" /></template>
                        Edit
                      </NButton>
                      <NDropdown trigger="click" :options="deleteMenuOptions('Delete captcha provider')" @select="() => deleteWafCaptchaProvider(provider.id)">
                        <NButton secondary size="small" :aria-label="`More actions for captcha provider ${provider.name}`">
                          <template #icon><MoreIcon class="icon-sm" /></template>
                          More
                        </NButton>
                      </NDropdown>
                    </div>
                  </div>
                  <PolicyExactValues
                    v-if="isExactValuesOpen('captcha-provider', provider.id)"
                    :id="exactValuesId('captcha-provider', provider.id)"
                    :label="`Exact values for ${provider.name}`"
                    :entries="captchaProviderExactValues(provider)"
                  />
                </template>
              </div>
            </div>
            <EmptyState
              v-else
              title="No captcha providers configured"
              description="Captcha rules challenge visitors through Turnstile, hCaptcha, or reCAPTCHA. Add a provider before creating a captcha rule."
              action-label="Add captcha provider"
              @action="openAddWafCaptchaProviderModal"
            />
          </SectionCard>
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
        <PublicAccessControlSettings :config="config" />
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
          <SectionCard flush>
            <div class="policy-list-toolbar">
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
            </div>
            <div v-if="filteredCacheRules.length" class="policy-data-table" role="table" aria-label="Cache rules">
              <div role="rowgroup">
                <div class="policy-data-header policy-rule-grid" role="row">
                  <span class="policy-grid-identity" role="columnheader">Rule</span>
                  <span class="policy-grid-match" role="columnheader">Match &amp; key</span>
                  <span class="policy-grid-action" role="columnheader">TTL &amp; storage</span>
                  <span class="policy-grid-state" role="columnheader">Order &amp; state</span>
                  <span class="policy-grid-actions" role="columnheader"><span class="visually-hidden">Actions</span></span>
                </div>
              </div>
              <div class="policy-data-rows" role="rowgroup">
                <template v-for="rule in filteredCacheRules" :key="rule.id.toString()">
                  <div
                    class="policy-data-row policy-rule-grid"
                    role="row"
                  >
                    <div class="policy-data-cell policy-grid-identity" data-label="Rule" role="cell">
                      <span class="policy-data-label">Rule</span>
                      <p class="policy-data-primary" dir="auto" :title="rule.name">{{ rule.name }}</p>
                      <p class="policy-data-secondary mono-text">Rule #{{ rule.id.toString() }}</p>
                      <button
                        type="button"
                        class="policy-exact-toggle"
                        :aria-expanded="isExactValuesOpen('cache', rule.id)"
                        :aria-controls="isExactValuesOpen('cache', rule.id) ? exactValuesId('cache', rule.id) : undefined"
                        :aria-label="`Exact values for ${rule.name}`"
                        @click="toggleExactValues('cache', rule.id)"
                      >
                        <ChevronRightIcon class="policy-exact-toggle__icon" aria-hidden="true" />
                        Exact values
                      </button>
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
                        <NTag size="small" :bordered="false">P{{ rule.priority.toString() }}</NTag>
                        <NTag size="small" :bordered="false" :type="naiveTagType(rule.enabled ? 'success' : 'warning')">
                          {{ rule.enabled ? 'Enabled' : 'Disabled' }}
                        </NTag>
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
                      <span class="visually-hidden">Actions</span>
                      <NButton secondary size="small" aria-label="Edit cache rule" @click="editCacheRule(rule.id)">
                        <template #icon><PencilIcon class="icon-sm" /></template>
                        Edit
                      </NButton>
                      <NDropdown trigger="click" :options="deleteMenuOptions('Delete cache rule')" @select="() => deleteCacheRule(rule.id)">
                        <NButton secondary size="small" :aria-label="`More actions for cache rule ${rule.name}`">
                          <template #icon><MoreIcon class="icon-sm" /></template>
                          More
                        </NButton>
                      </NDropdown>
                    </div>
                  </div>
                  <PolicyExactValues
                    v-if="isExactValuesOpen('cache', rule.id)"
                    :id="exactValuesId('cache', rule.id)"
                    :label="`Exact values for ${rule.name}`"
                    :entries="cacheExactValues(rule)"
                  />
                </template>
              </div>
            </div>
            <EmptyState
              v-if="cacheRules.length && !filteredCacheRules.length && isPolicyFilterActive('cache')"
              title="No matching cache rules"
              description="Clear or adjust the filters to show more rules."
            />
            <EmptyState
              v-if="!cacheRules.length"
              title="No cache rules configured"
              description="Cache rules store public GET assets such as CSS, JavaScript, images, and fonts on the proxy. Authorization and Cookie requests always bypass shared cache."
              action-label="Add cache rule"
              @action="openAddCacheRuleModal"
            />
          </SectionCard>

          <SectionCard
            title="Cache storage"
            description="Shared storage budgets, set independently from the rules that decide cache eligibility. Bodies use the public cache directory; metadata stays in SQLite."
          >
            <template #badge>
              <NTag v-if="cacheSettingsDirty" size="small" :bordered="false" type="warning">Unsaved changes</NTag>
              <NTag v-else size="small" :bordered="false" :type="cacheSettingsForm.enabled ? 'success' : 'default'">
                {{ cacheSettingsForm.enabled ? 'Storage enabled' : 'Storage disabled' }}
              </NTag>
            </template>
            <form class="policy-cache-settings" @submit.prevent="saveCacheSettings">
              <CacheStorageUsage :settings="cacheSettings" :stats="config?.cacheStorageStats" />
              <NCheckbox v-model:checked="cacheSettingsForm.enabled">Cache storage enabled</NCheckbox>
              <div class="policy-cache-fields">
                <label class="layout-grid space-xs copy-xs weight-medium muted-text">
                  Disk MiB
                  <NInputNumber
                    v-model:value="cacheSettingsForm.maxDiskMiB"
                    :show-button="false"
                    size="small"
                    :min="1"
                    :step="1"
                    :precision="0"
                    :status="cacheSettingsErrors.maxDiskMiB ? 'error' : undefined"
                  />
                  <small v-if="cacheSettingsErrors.maxDiskMiB" class="policy-field-error" role="alert">{{ cacheSettingsErrors.maxDiskMiB }}</small>
                </label>
                <label class="layout-grid space-xs copy-xs weight-medium muted-text">
                  Memory MiB
                  <NInputNumber
                    v-model:value="cacheSettingsForm.maxMemoryMiB"
                    :show-button="false"
                    size="small"
                    :min="1"
                    :step="1"
                    :precision="0"
                    :status="cacheSettingsErrors.maxMemoryMiB ? 'error' : undefined"
                  />
                  <small v-if="cacheSettingsErrors.maxMemoryMiB" class="policy-field-error" role="alert">{{ cacheSettingsErrors.maxMemoryMiB }}</small>
                </label>
                <label class="layout-grid space-xs copy-xs weight-medium muted-text">
                  Hot object KiB
                  <NInputNumber
                    v-model:value="cacheSettingsForm.hotObjectKiB"
                    :show-button="false"
                    size="small"
                    :min="1"
                    :step="1"
                    :precision="0"
                    :status="cacheSettingsErrors.hotObjectKiB ? 'error' : undefined"
                  />
                  <small v-if="cacheSettingsErrors.hotObjectKiB" class="policy-field-error" role="alert">{{ cacheSettingsErrors.hotObjectKiB }}</small>
                </label>
                <label class="layout-grid space-xs copy-xs weight-medium muted-text">
                  Max entries
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
                  <small v-if="cacheSettingsErrors.maxEntries" class="policy-field-error" role="alert">{{ cacheSettingsErrors.maxEntries }}</small>
                </label>
                <label class="layout-grid space-xs copy-xs weight-medium muted-text">
                  Cleanup seconds
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
                  <small v-if="cacheSettingsErrors.cleanupIntervalSeconds" class="policy-field-error" role="alert">{{ cacheSettingsErrors.cleanupIntervalSeconds }}</small>
                </label>
              </div>
              <div class="policy-inline-row">
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
            <template #footer>
              <div class="policy-inline-row">
                <div class="layout-grid space-2xs">
                  <h3 class="copy-sm weight-semibold base-text">Purge cached objects</h3>
                  <p class="copy-xs muted-text">Delete every cached public-proxy object. Eligible requests will miss until their responses are stored again.</p>
                </div>
                <NButton type="error" secondary size="small" :disabled="isBusy" @click="purgeAllCache">
                  <template #icon><TrashIcon class="icon-sm" /></template>
                  Purge all cached objects
                </NButton>
              </div>
            </template>
          </SectionCard>
        </div>
      </NTabPane>

      <NTabPane
        id="traffic-policy-panel-retries"
        name="retries"
        role="tabpanel"
        aria-labelledby="traffic-policy-tab-retries"
        :tab="`Retries · ${enabledRetryRules}/${retryRules.length}`"
        :tab-props="policyTabProps('retries', `Retries, ${enabledCountLabel(enabledRetryRules, retryRules.length)}`)"
      >
        <div class="stack-lg">
          <section class="retry-principles" aria-labelledby="retry-principles-title">
            <div class="layout-grid space-2xs min-width-zero">
              <h2 id="retry-principles-title" class="copy-sm weight-semibold base-text">One client request, bounded upstream attempts</h2>
              <p class="copy-xs line-normal muted-text">
                Retries use a different eligible agent chosen by the target's load balancer. They stay inside the selected target and can recover transport failures, selected HTTP error statuses, and protected mid-response failures before any response reaches the client.
              </p>
            </div>
            <div class="retry-principles__facts" aria-label="Retry invariants">
              <NTag size="small" :bordered="false">Different agent</NTag>
              <NTag size="small" :bordered="false">Same target</NTag>
              <NTag size="small" :bordered="false">1–3 retries</NTag>
            </div>
          </section>

          <SectionCard flush>
            <div class="policy-list-toolbar">
              <div class="policy-local-filter" aria-label="Filter request retry rules">
                <NInput
                  v-model:value="policyFilters.retries.text"
                  size="small"
                  clearable
                  placeholder="Filter retry rules"
                  :input-props="{ 'aria-label': 'Filter retry rules by name, methods, match, scope, or failure mode' }"
                >
                  <template #prefix><SearchIcon class="icon-sm" /></template>
                </NInput>
                <AccessibleSelect
                  v-model:value="policyFilters.retries.status"
                  accessible-label="Filter retry rules by state"
                  size="small"
                  :options="policyFilterStatusOptions"
                />
                <NButton v-if="isPolicyFilterActive('retries')" quaternary size="small" @click="clearPolicyFilter('retries')">
                  <template #icon><XIcon class="icon-sm" /></template>
                  Clear filters
                </NButton>
                <p class="policy-filter-result" aria-live="polite">
                  {{ policyFilterResultLabel('retries', filteredRetryRules.length, retryRules.length) }}
                </p>
              </div>
            </div>

            <div v-if="filteredRetryRules.length" class="policy-data-table" role="table" aria-label="Request retry rules">
              <div role="rowgroup">
                <div class="policy-data-header policy-rule-grid" role="row">
                  <span class="policy-grid-identity" role="columnheader">Rule</span>
                  <span class="policy-grid-match" role="columnheader">Match &amp; scope</span>
                  <span class="policy-grid-action" role="columnheader">Attempt policy</span>
                  <span class="policy-grid-state" role="columnheader">Order &amp; state</span>
                  <span class="policy-grid-actions" role="columnheader"><span class="visually-hidden">Actions</span></span>
                </div>
              </div>
              <div class="policy-data-rows" role="rowgroup">
                <template v-for="rule in filteredRetryRules" :key="rule.id.toString()">
                  <div
                    class="policy-data-row policy-rule-grid"
                    role="row"
                  >
                    <div class="policy-data-cell policy-grid-identity" data-label="Rule" role="cell">
                      <span class="policy-data-label">Rule</span>
                      <p class="policy-data-primary" dir="auto" :title="rule.name">{{ rule.name }}</p>
                      <p class="policy-data-secondary mono-text">Rule #{{ rule.id.toString() }}</p>
                      <button
                        type="button"
                        class="policy-exact-toggle"
                        :aria-expanded="isExactValuesOpen('retry', rule.id)"
                        :aria-controls="isExactValuesOpen('retry', rule.id) ? exactValuesId('retry', rule.id) : undefined"
                        :aria-label="`Exact values for ${rule.name}`"
                        @click="toggleExactValues('retry', rule.id)"
                      >
                        <ChevronRightIcon class="policy-exact-toggle__icon" aria-hidden="true" />
                        Exact values
                      </button>
                    </div>
                    <div class="policy-data-cell policy-grid-match" data-label="Match & scope" role="cell">
                      <span class="policy-data-label">Match &amp; scope</span>
                      <p class="policy-data-value" dir="auto" :title="publicPolicyMatchSummary(rule)">{{ publicPolicyMatchSummary(rule) }}</p>
                      <p class="policy-data-secondary mono-text" :title="retryScopeSummary(rule)">{{ retryScopeSummary(rule) }}</p>
                    </div>
                    <div class="policy-data-cell policy-grid-action" data-label="Attempt policy" role="cell">
                      <span class="policy-data-label">Attempt policy</span>
                      <div class="policy-data-tags">
                        <NTag size="small" :bordered="false" type="info">{{ rule.maxRetries.toString() }} {{ rule.maxRetries === 1n ? 'retry' : 'retries' }}</NTag>
                        <NTag size="small" :bordered="false">{{ retryFailureModeLabel(rule.failureMode) }}</NTag>
                      </div>
                      <p class="policy-data-secondary mono-text" :title="retryMethodSummary(rule)">{{ retryMethodSummary(rule) }}</p>
                      <p class="policy-data-secondary">{{ retryStatusSummary(rule) }}</p>
                      <p class="policy-data-secondary">{{ retryBodyModeLabel(rule) }}</p>
                      <p class="policy-data-secondary">{{ retryResponseBodyModeLabel(rule) }}</p>
                    </div>
                    <div class="policy-data-cell policy-grid-state" data-label="Order & state" role="cell">
                      <span class="policy-data-label">Order &amp; state</span>
                      <div class="policy-data-tags">
                        <NTag size="small" :bordered="false">P{{ rule.priority.toString() }}</NTag>
                        <NTag size="small" :bordered="false" :type="naiveTagType(rule.enabled ? 'success' : 'warning')">
                          {{ rule.enabled ? 'Enabled' : 'Disabled' }}
                        </NTag>
                      </div>
                      <div v-if="visiblePolicyWarningsForRule('retry', rule.id).length" class="policy-data-tags">
                        <NTag
                          v-for="warning in visiblePolicyWarningsForRule('retry', rule.id)"
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
                      <span class="visually-hidden">Actions</span>
                      <NButton secondary size="small" aria-label="Edit retry rule" @click="editRetryRule(rule.id)">
                        <template #icon><PencilIcon class="icon-sm" /></template>
                        Edit
                      </NButton>
                      <NDropdown trigger="click" :options="deleteMenuOptions('Delete retry rule')" @select="() => deleteRetryRule(rule.id)">
                        <NButton secondary size="small" :aria-label="`More actions for retry rule ${rule.name}`">
                          <template #icon><MoreIcon class="icon-sm" /></template>
                          More
                        </NButton>
                      </NDropdown>
                    </div>
                  </div>
                  <PolicyExactValues
                    v-if="isExactValuesOpen('retry', rule.id)"
                    :id="exactValuesId('retry', rule.id)"
                    :label="`Exact values for ${rule.name}`"
                    :entries="retryExactValues(rule)"
                  />
                </template>
              </div>
            </div>
            <EmptyState
              v-if="retryRules.length && !filteredRetryRules.length && isPolicyFilterActive('retries')"
              title="No matching retry rules"
              description="Clear or adjust the filters to show more rules."
            />
            <EmptyState
              v-if="!retryRules.length"
              title="No request retries configured"
              description="Add an opt-in rule to retry selected requests through a different agent when an agent tunnel or VPN connection fails. GET and HEAD with one connection-only retry is the safest starting point."
              action-label="Add retry rule"
              @action="openAddRetryRuleModal"
            />
          </SectionCard>
        </div>
      </NTabPane>

      <NTabPane
        id="traffic-policy-panel-traffic-shaper"
        name="traffic-shaper"
        role="tabpanel"
        aria-labelledby="traffic-policy-tab-traffic-shaper"
        :tab="`Traffic shaper · ${enabledTrafficShapers}/${trafficShaperRules.length}`"
        :tab-props="policyTabProps('traffic-shaper', `Traffic shaper, ${enabledCountLabel(enabledTrafficShapers, trafficShaperRules.length)}`)"
      >
        <SectionCard flush>
          <div class="policy-list-toolbar">
            <div class="policy-local-filter" aria-label="Filter traffic-shaper rules">
              <NInput
                v-model:value="policyFilters['traffic-shaper'].text"
                size="small"
                clearable
                placeholder="Filter traffic-shaper rules"
                :input-props="{ 'aria-label': 'Filter traffic-shaper rules by name, match, key, priority, budget, protocol scope, or warning' }"
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
          </div>
          <div v-if="filteredTrafficShaperRules.length" class="policy-data-table" role="table" aria-label="Traffic-shaper rules">
            <div role="rowgroup">
              <div class="policy-data-header policy-rule-grid" role="row">
                <span class="policy-grid-identity" role="columnheader">Rule</span>
                <span class="policy-grid-match" role="columnheader">Match &amp; key</span>
                <span class="policy-grid-action" role="columnheader">Budget &amp; scope</span>
                <span class="policy-grid-state" role="columnheader">Order &amp; state</span>
                <span class="policy-grid-actions" role="columnheader"><span class="visually-hidden">Actions</span></span>
              </div>
            </div>
            <div class="policy-data-rows" role="rowgroup">
              <template v-for="rule in filteredTrafficShaperRules" :key="rule.id.toString()">
                <div
                  class="policy-data-row policy-rule-grid"
                  role="row"
                >
                  <div class="policy-data-cell policy-grid-identity" data-label="Rule" role="cell">
                    <span class="policy-data-label">Rule</span>
                    <p class="policy-data-primary" dir="auto" :title="rule.name">{{ rule.name }}</p>
                    <p class="policy-data-secondary mono-text">Rule #{{ rule.id.toString() }}</p>
                    <button
                      type="button"
                      class="policy-exact-toggle"
                      :aria-expanded="isExactValuesOpen('traffic-shaper', rule.id)"
                      :aria-controls="isExactValuesOpen('traffic-shaper', rule.id) ? exactValuesId('traffic-shaper', rule.id) : undefined"
                      :aria-label="`Exact values for ${rule.name}`"
                      @click="toggleExactValues('traffic-shaper', rule.id)"
                    >
                      <ChevronRightIcon class="policy-exact-toggle__icon" aria-hidden="true" />
                      Exact values
                    </button>
                  </div>
                  <div class="policy-data-cell policy-grid-match" data-label="Match & key" role="cell">
                    <span class="policy-data-label">Match &amp; key</span>
                    <p class="policy-data-value" dir="auto" :title="publicPolicyMatchSummary(rule)">{{ publicPolicyMatchSummary(rule) }}</p>
                    <p class="policy-data-secondary mono-text" dir="auto" :title="trafficShaperKeySummary(rule)">Key {{ trafficShaperKeySummary(rule) }}</p>
                  </div>
                  <div class="policy-data-cell policy-grid-action" data-label="Budget & scope" role="cell">
                    <span class="policy-data-label">Budget &amp; scope</span>
                    <div class="policy-data-tags">
                      <NTag size="small" :bordered="false" type="info">{{ trafficShaperScopeLabel(rule.budgetScope) }}</NTag>
                      <NTag size="small" :bordered="false">{{ trafficShaperProtocolScopeLabel(rule.protocolScope) }}</NTag>
                    </div>
                    <p class="policy-data-secondary mono-text" :title="trafficShaperRuleSummary(rule)">{{ trafficShaperRuleSummary(rule) }}</p>
                    <p class="policy-data-secondary" :title="trafficShaperBudgetSummary(rule)">{{ trafficShaperBudgetSummary(rule) }}</p>
                  </div>
                  <div class="policy-data-cell policy-grid-state" data-label="Order & state" role="cell">
                    <span class="policy-data-label">Order &amp; state</span>
                    <div class="policy-data-tags">
                      <NTag size="small" :bordered="false">P{{ rule.priority.toString() }}</NTag>
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
                    <span class="visually-hidden">Actions</span>
                    <NButton secondary size="small" aria-label="Edit traffic-shaper rule" @click="editTrafficShaperRule(rule.id)">
                      <template #icon><PencilIcon class="icon-sm" /></template>
                      Edit
                    </NButton>
                    <NDropdown trigger="click" :options="deleteMenuOptions('Delete traffic-shaper rule')" @select="() => deleteTrafficShaperRule(rule.id)">
                      <NButton secondary size="small" :aria-label="`More actions for traffic-shaper rule ${rule.name}`">
                        <template #icon><MoreIcon class="icon-sm" /></template>
                        More
                      </NButton>
                    </NDropdown>
                  </div>
                </div>
                <PolicyExactValues
                  v-if="isExactValuesOpen('traffic-shaper', rule.id)"
                  :id="exactValuesId('traffic-shaper', rule.id)"
                  :label="`Exact values for ${rule.name}`"
                  :entries="trafficShaperExactValues(rule)"
                />
              </template>
            </div>
          </div>
          <EmptyState
            v-if="trafficShaperRules.length && !filteredTrafficShaperRules.length && isPolicyFilterActive('traffic-shaper')"
            title="No matching traffic-shaper rules"
            description="Clear or adjust the filters to show more rules."
          />
          <EmptyState
            v-if="!trafficShaperRules.length"
            title="No traffic-shaper rules configured"
            description="Traffic shapers limit bandwidth consumption per request or client to prevent saturation."
            action-label="Add traffic shaper"
            @action="openAddTrafficShaperRuleModal"
          />
        </SectionCard>
      </NTabPane>

      <NTabPane
        id="traffic-policy-panel-visitor-identity"
        name="visitor-identity"
        role="tabpanel"
        aria-labelledby="traffic-policy-tab-visitor-identity"
        tab="Visitor identity"
        :tab-props="policyTabProps('visitor-identity', 'Visitor identity')"
      >
        <PublicVisitorIdentitySettings ref="visitorIdentitySettings" :config="config" />
      </NTabPane>
    </NTabs>

    <PublicProxyEditorHost ref="editorHost" :config="config" />

    <NDrawer
      v-model:show="isPreviewOpen"
      placement="right"
      :width="editorDrawerWidth('76rem')"
      aria-label="Request tester"
      class="editor-drawer"
    >
      <NDrawerContent title="Request tester" closable>
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
/* Execution order: a quiet disclosure between hairlines. */
.policy-execution-disclosure {
  min-width: 0;
  border-block: 1px solid var(--app-border-subtle);
}

.policy-execution-disclosure > summary {
  display: flex;
  min-height: 2.75rem;
  align-items: center;
  gap: 0.75rem;
  padding: 0.5rem 0;
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

.policy-execution-disclosure > summary:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: 2px;
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

.policy-tabs {
  min-width: 0;
}

.policy-tabs :deep(.n-tabs-nav) {
  margin-bottom: 1rem;
}

/* List toolbar: the tab's filters. Its primary action lives in the page header. */
.policy-list-toolbar {
  display: grid;
  gap: 0.75rem;
  border-bottom: 1px solid var(--app-border-subtle);
  padding: 0.75rem 1.25rem;
}

.policy-local-filter {
  display: grid;
  min-width: 0;
  gap: 0.625rem;
}

.policy-filter-result {
  align-self: center;
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
  line-height: 1.4;
  white-space: nowrap;
}

/* WAF sub-pages: route-backed links styled like SegmentedControl. */
.policy-subnav {
  display: flex;
  width: fit-content;
  max-width: 100%;
  flex-wrap: wrap;
  gap: 0.125rem;
  border-radius: 8px;
  padding: 0.1875rem;
  background: var(--app-panel-muted);
}

.stack-lg > .policy-subnav + * {
  margin-top: 1rem;
}

.policy-subnav__link {
  display: inline-flex;
  min-height: 1.75rem;
  align-items: center;
  border-radius: 6px;
  padding: 0.25rem 0.625rem;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  font-weight: 500;
  text-decoration: none;
  transition: background-color 140ms ease-out, color 140ms ease-out;
}

.policy-subnav__link:hover {
  color: var(--app-text);
}

.policy-subnav__link--active {
  background: var(--app-panel);
  box-shadow: 0 0 0 1px var(--app-border-subtle), 0 1px 2px rgba(0, 0, 0, 0.12);
  color: var(--app-text);
}

.policy-subnav__link:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: 1px;
}

/* Rule tables: role="table" grids that stack into labelled cells on narrow containers. */
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
  padding: 0.875rem 1rem;
}

.policy-data-row + .policy-data-row,
.policy-exact-row + .policy-data-row {
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

.policy-grid-identity { grid-area: identity; }
.policy-grid-type { grid-area: type; }
.policy-grid-secret { grid-area: secret; }
.policy-grid-match { grid-area: match; }
.policy-grid-action { grid-area: action; }
.policy-grid-state { grid-area: state; }
.policy-grid-actions { grid-area: actions; }

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
  font-variant-numeric: tabular-nums;
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
  gap: 0.375rem;
}

.policy-data-actions {
  justify-content: flex-end;
  justify-self: end;
  gap: 0.5rem;
}

.policy-data-cell > :deep(.n-tag) {
  max-width: 100%;
}

/* Exact values: a quiet toggle in the identity cell opens a full-width row below. */
.policy-exact-toggle {
  display: inline-flex;
  min-height: 1.5rem;
  align-items: center;
  gap: 0.25rem;
  margin: 0 0 0 -0.3125rem;
  border: 0;
  border-radius: 6px;
  padding: 0.125rem 0.375rem 0.125rem 0.25rem;
  background: transparent;
  color: var(--app-text-muted);
  cursor: pointer;
  font: inherit;
  font-size: 0.75rem;
  line-height: 1.4;
  white-space: nowrap;
}

.policy-exact-toggle:hover {
  background: var(--app-hover);
  color: var(--app-text);
}

.policy-exact-toggle:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: 1px;
}

.policy-exact-toggle[aria-expanded="true"] {
  color: var(--app-text);
}

.policy-exact-toggle__icon {
  width: 0.875rem;
  height: 0.875rem;
  flex: none;
  transition: transform 140ms ease-out;
}

.policy-exact-toggle[aria-expanded="true"] .policy-exact-toggle__icon {
  transform: rotate(90deg);
}

@media (prefers-reduced-motion: reduce) {
  .policy-exact-toggle__icon {
    transition: none;
  }
}

/* Cache storage form. */
.policy-cache-settings {
  display: grid;
  gap: 1.25rem;
}

.policy-cache-fields {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr));
  gap: 1rem;
}

.policy-field-error {
  color: var(--app-error);
  font-size: 0.75rem;
  font-weight: 400;
  line-height: 1.35;
}

.policy-inline-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
}

.policy-cache-settings > .policy-inline-row {
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 1rem;
}

.policy-inline-row > p {
  max-width: 64ch;
  margin: 0;
}

/* Retries: a quiet explainer above the rule list. */
.retry-principles {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem 1.5rem;
  border-radius: 10px;
  padding: 1rem 1.25rem;
  background: var(--app-panel-muted);
}

.retry-principles p {
  max-width: 76ch;
}

.retry-principles__facts {
  display: flex;
  flex: none;
  flex-wrap: wrap;
  gap: 0.375rem;
}

@container (min-width: 48rem) {
  .policy-data-header {
    display: grid;
    align-items: center;
    gap: 0.75rem;
    border-bottom: 1px solid var(--app-border-subtle);
    padding: 0.5rem 1.25rem;
    color: var(--app-text-muted);
    font-size: 0.75rem;
    font-weight: 500;
    line-height: 1.35;
  }

  .policy-data-row {
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 1.25rem;
  }

  .policy-provider-grid {
    grid-template-areas: "identity type secret state actions";
    grid-template-columns:
      minmax(12rem, 1.3fr)
      minmax(7rem, 0.55fr)
      minmax(8rem, 0.7fr)
      minmax(7rem, 0.55fr)
      9.5rem;
  }

  .policy-rule-grid {
    grid-template-areas: "identity match action state actions";
    grid-template-columns:
      minmax(8.5rem, 0.8fr)
      minmax(10rem, 1.25fr)
      minmax(10rem, 1.05fr)
      minmax(7.5rem, 0.75fr)
      9.5rem;
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
    min-height: 2.75rem;
  }

  .policy-subnav__link {
    min-height: 2.5rem;
  }
}

@media (min-width: 640px) {
  .policy-local-filter {
    grid-template-columns: minmax(12rem, 22rem) minmax(9rem, 11rem) auto auto;
    align-items: center;
    justify-content: start;
  }
}

@media (max-width: 639px) {
  .policy-execution-disclosure__path {
    display: none;
  }

  .policy-list-toolbar {
    padding-inline: 1rem;
  }

  .policy-inline-row,
  .retry-principles {
    align-items: stretch;
    flex-direction: column;
  }

  .policy-inline-row > div,
  .policy-inline-row > :deep(.n-button) {
    width: 100%;
  }

  .policy-inline-row :deep(.n-button) {
    flex: 1 1 auto;
  }

  .retry-principles {
    padding: 1rem;
  }
}
</style>
