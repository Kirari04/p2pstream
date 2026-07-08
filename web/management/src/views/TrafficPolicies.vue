<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Search as SearchIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NCheckbox, NInput, NInputNumber, NModal, NSelect, NTabPane, NTabs, NTag } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
import EmptyState from "@/components/EmptyState.vue";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import TrafficPolicyExecutionOrderStrip from "@/components/traffic-policy/TrafficPolicyExecutionOrderStrip.vue";
import TrafficPolicyRequestPlayground from "@/components/traffic-policy/TrafficPolicyRequestPlayground.vue";
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
import { modalCardStyle, naiveTagType } from "@/lib/naiveUi";
import {
  previewTrafficPolicyStages,
  runtimeOrderedCacheRules,
  runtimeOrderedRateLimitRules,
  runtimeOrderedTrafficShaperRules,
  runtimeOrderedWafRules,
  trafficPolicyAttentionWarnings,
  type TrafficPolicyAttentionWarning,
  type TrafficPolicyKind,
  type TrafficPolicyMatchState,
  type TrafficPolicyStageCandidate,
  type TrafficPolicySyntheticRequest,
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
type TrafficPolicyPreviewForm = {
  method: string;
  protocol: string;
  host: string;
  path: string;
  remoteIp: string;
  headersText: string;
  cookiesText: string;
  queryText: string;
  routeId: string;
  targetId: string;
};
type PlaygroundStageItem = {
  id: bigint;
  name: string;
  priority: bigint;
  state: TrafficPolicyMatchState;
  reason: string;
  selected: boolean;
  skipped: boolean;
};
type PlaygroundStage = {
  key: string;
  label: string;
  mode: "first" | "all";
  items: PlaygroundStageItem[];
};

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
  { label: "All", value: "all" },
  { label: "Enabled", value: "enabled" },
  { label: "Disabled", value: "disabled" },
];
const previewForm = reactive<TrafficPolicyPreviewForm>(defaultTrafficPolicyPreviewForm());
const previewRequest = computed<TrafficPolicySyntheticRequest>(() => ({
  method: previewForm.method,
  protocol: previewForm.protocol,
  host: previewForm.host,
  path: previewForm.path,
  remoteIp: previewForm.remoteIp,
  headers: parseRepeatedMap(previewForm.headersText, true),
  cookies: parseCookieMap(previewForm.cookiesText),
  query: parseRepeatedMap(previewForm.queryText, false),
  routeId: previewForm.routeId || null,
  targetId: previewForm.targetId || null,
}));
const policyStagePreview = computed(() => previewTrafficPolicyStages({
  rateLimitRules: rateLimitRules.value,
  trafficShaperRules: trafficShaperRules.value,
  wafRules: wafRules.value,
  wafCaptchaProviders: wafCaptchaProviders.value,
  cacheSettings: cacheSettings.value ?? undefined,
  cacheRules: cacheRules.value,
}, previewRequest.value));
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
const playgroundStages = computed<PlaygroundStage[]>(() => [
  stageFromFirstCandidate("waf", "WAF", wafRules.value, policyStagePreview.value.waf),
  stageFromRateLimitCandidates(rateLimitRules.value, policyStagePreview.value.rateLimits),
  stageFromFirstCandidate("traffic-shaper", "Traffic shaper", trafficShaperRules.value, policyStagePreview.value.trafficShaper),
  stageFromFirstCandidate("cache", "Cache", cacheRules.value, policyStagePreview.value.cache),
]);
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
    detail: `${rateLimitRules.value.length.toString()} total rules`,
    description: "Throttle traffic based on request rate per client or route.",
  },
  {
    key: "waf",
    label: "WAF",
    value: enabledWafRules.value.toString(),
    detail: `${wafCaptchaProviders.value.length.toString()} captcha providers`,
    description: "Block, challenge, or queue matching application traffic before it reaches routes.",
  },
  {
    key: "cache",
    label: "Cache",
    value: enabledCacheRules.value.toString(),
    detail: cacheSettingsForm.enabled ? "storage enabled" : "storage disabled",
    description: "Cache public static files on the proxy after routing while keeping WAF, rate limits, and shaping active.",
  },
  {
    key: "traffic-shaper",
    label: "Traffic Shaper",
    value: enabledTrafficShapers.value.toString(),
    detail: `${trafficShaperRules.value.length.toString()} total shapers`,
    description: "Limit bandwidth consumption per request or client.",
  },
]);
const activePolicySection = computed<PolicySectionKey>(() => normalizePolicySection(route.params.section));
const activePolicyMeta = computed(() => (
  policySections.value.find((section) => section.key === activePolicySection.value) ?? policySections.value[0]
));

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

function defaultTrafficPolicyPreviewForm(): TrafficPolicyPreviewForm {
  return {
    method: "GET",
    protocol: "https",
    host: "app.example.com",
    path: "/",
    remoteIp: "198.51.100.10",
    headersText: "X-Plan: pro",
    cookiesText: "",
    queryText: "",
    routeId: "",
    targetId: "",
  };
}

function updateTrafficPolicyPreview(value: TrafficPolicyPreviewForm) {
  Object.assign(previewForm, value);
}

function resetTrafficPolicyPreview() {
  Object.assign(previewForm, defaultTrafficPolicyPreviewForm());
}

function parseRepeatedMap(text: string, lowerCaseKeys: boolean): Record<string, string[]> {
  const parsed: Record<string, string[]> = {};
  for (const line of text.split(/\r?\n/)) {
    const item = parseKeyValueLine(line);
    if (!item) continue;
    const key = lowerCaseKeys ? item.key.toLowerCase() : item.key;
    const values = parsed[key] ?? [];
    values.push(item.value);
    parsed[key] = values;
  }
  return parsed;
}

function parseCookieMap(text: string): Record<string, string> {
  const parsed: Record<string, string> = {};
  for (const part of text.split(/\r?\n|;/)) {
    const item = parseKeyValueLine(part);
    if (!item) continue;
    if (parsed[item.key] !== undefined) continue;
    parsed[item.key] = item.value;
  }
  return parsed;
}

function parseKeyValueLine(line: string): { key: string; value: string } | null {
  const trimmed = line.trim();
  if (!trimmed) return null;
  const colon = trimmed.indexOf(":");
  const equals = trimmed.indexOf("=");
  const separator = colon >= 0 && (equals < 0 || colon < equals) ? colon : equals;
  if (separator < 0) return { key: trimmed, value: "" };
  const key = trimmed.slice(0, separator).trim();
  if (!key) return null;
  return { key, value: trimmed.slice(separator + 1).trim() };
}

function countTags(count: number) {
  return count ? [{ label: count.toString(), tone: "info" as const }] : [];
}

function stageFromFirstCandidate<T extends { id: bigint; name: string; priority: bigint; enabled: boolean }>(
  key: TrafficPolicyKind,
  label: string,
  rules: readonly T[],
  candidate: TrafficPolicyStageCandidate<T> | null,
): PlaygroundStage {
  const selectedId = candidate?.rule.id;
  let passedSelected = false;
  return {
    key,
    label,
    mode: "first",
    items: rules.map((rule) => {
      const selected = selectedId !== undefined && rule.id === selectedId;
      const skipped = Boolean(rule.enabled && passedSelected && !selected);
      const selectedResult = selected ? candidate?.result : null;
      if (selected) passedSelected = true;
      return {
        id: rule.id,
        name: rule.name,
        priority: rule.priority,
        state: selectedResult?.state ?? "miss",
        reason: selectedResult ? (selectedResult.reason || "Selected by preview") : (rule.enabled ? skipped ? "Skipped after earlier candidate" : "No match in preview" : "Rule is disabled"),
        selected,
        skipped,
      };
    }),
  };
}

function stageFromRateLimitCandidates(
  rules: readonly PublicRateLimitRule[],
  candidates: readonly TrafficPolicyStageCandidate<PublicRateLimitRule>[],
): PlaygroundStage {
  const candidateById = new Map(candidates.map((candidate) => [candidate.rule.id.toString(), candidate]));
  return {
    key: "rate-limit",
    label: "Rate limits",
    mode: "all",
    items: rules.map((rule) => {
      const candidate = candidateById.get(rule.id.toString());
      return {
        id: rule.id,
        name: rule.name,
        priority: rule.priority,
        state: candidate?.result.state ?? "miss",
        reason: candidate?.result.reason || (rule.enabled ? "No match in preview" : "Rule is disabled"),
        selected: Boolean(candidate),
        skipped: false,
      };
    }),
  };
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
        <h3 class="margin-bottom-sm copy-xl weight-bold">Traffic Policy</h3>
        <p class="copy-sm muted-text">{{ activePolicyMeta.description }}</p>
      </div>
      <div class="page-toolbar__actions">
        <NButton secondary size="small" @click="isPreviewOpen = true">
          <template #icon><SearchIcon class="icon-sm" /></template>
          Request Tester
        </NButton>
      </div>
    </div>

    <section class="summary-grid summary-grid--four policy-summary-grid" aria-label="Policy type summary">
      <div
        v-for="section in policySections"
        :key="section.key"
        class="surface-card pad-lg policy-summary-card"
        :class="{ 'policy-summary-card--active': section.key === activePolicySection }"
      >
        <p class="copy-xs weight-semibold label-case letter-widest muted-text">{{ section.label }}</p>
        <p class="margin-top-sm copy-2xl weight-semibold base-text">{{ section.value }}</p>
        <p class="margin-top-xs copy-xs muted-text">{{ section.detail }}</p>
      </div>
    </section>

    <TrafficPolicyExecutionOrderStrip
      :stages="executionStages"
      description="Rules are shown in runtime order. Lower priorities run first, ties fall back to rule ID."
    />

    <section class="surface-card policy-filter-bar" aria-label="Traffic policy filters">
      <label class="policy-filter-search">
        <span class="copy-xs weight-semibold label-case letter-wide muted-text">Filter</span>
        <NInput v-model:value="policyFilter.text" size="small" clearable placeholder="Rule name, match, priority, warning">
          <template #prefix><SearchIcon class="icon-sm" /></template>
        </NInput>
      </label>
      <label class="policy-filter-status">
        <span class="copy-xs weight-semibold label-case letter-wide muted-text">State</span>
        <NSelect v-model:value="policyFilter.status" size="small" :options="policyFilterStatusOptions" />
      </label>
    </section>

    <NTabs class="policy-tabs" type="line" animated :value="activePolicySection" @update:value="selectPolicySection">
      <NTabPane name="rate-limits" :tab="`Rate Limits (${enabledRateLimitRules})`">
    <section class="surface-card hide-overflow">
      <div class="divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
        <div>
          <h4 class="copy-sm weight-semibold label-case letter-widest muted-text">Rate Limits</h4>
          <p class="margin-top-2xs copy-xs muted-text normal-text letter-normal">Throttle traffic based on request rate per client or route.</p>
        </div>
        <NButton secondary size="small" @click="openAddRateLimitRuleModal">
          <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
          Add Rule
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
              <template #icon><PencilIcon class="icon-sm icon-sm" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete rate-limit rule" title="Delete rate-limit rule" @click="deleteRateLimitRule(rule.id)">
              <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
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
          action-label="Add Rule"
          @action="openAddRateLimitRuleModal"
        />
      </div>
    </section>
      </NTabPane>

      <NTabPane name="waf" :tab="`WAF (${enabledWafRules})`">
    <section class="surface-card hide-overflow">
      <div class="divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
        <div>
          <h4 class="copy-sm weight-semibold label-case letter-widest muted-text">WAF</h4>
          <p class="margin-top-2xs copy-xs muted-text normal-text letter-normal">Block, challenge, or queue matching application traffic before it reaches routes.</p>
        </div>
        <div class="layout-row wrap-items space-sm">
          <NButton secondary size="small" @click="openAddWafCaptchaProviderModal">
            <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
            Add Provider
          </NButton>
          <NButton secondary size="small" @click="openAddWafRuleModal">
            <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
            Add Rule
          </NButton>
        </div>
      </div>
      <div class="divided-list">
        <div v-for="provider in wafCaptchaProviders" :key="`provider-${provider.id.toString()}`" class="layout-grid space-md pad-x-xl pad-y-lg mq-lg-one-auto">
          <div class="min-width-zero">
            <div class="layout-row min-width-zero wrap-items align-center space-sm">
              <p class="clip-text copy-sm weight-medium base-text">{{ provider.name }}</p>
              <NTag size="small" :bordered="false" type="info">{{ wafProviderLabel(provider.providerType) }}</NTag>
              <NTag size="small" :bordered="false" :type="naiveTagType(provider.secretKeySet ? 'success' : 'danger')">{{ provider.secretKeySet ? 'Secret saved' : 'Secret missing' }}</NTag>
              <NTag v-if="!provider.enabled" size="small" :bordered="false" type="warning">Disabled</NTag>
            </div>
            <p class="margin-top-xs clip-text mono-text copy-xs muted-text">{{ provider.siteKey }}</p>
          </div>
          <div class="layout-row space-sm mq-lg-end">
            <NButton secondary size="small" aria-label="Edit captcha provider" title="Edit captcha provider" @click="editWafCaptchaProvider(provider)">
              <template #icon><PencilIcon class="icon-sm icon-sm" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete captcha provider" title="Delete captcha provider" @click="deleteWafCaptchaProvider(provider.id)">
              <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
            </NButton>
          </div>
        </div>
        <div v-for="rule in filteredWafRules" :key="`rule-${rule.id.toString()}`" class="layout-grid space-md pad-x-xl pad-y-lg mq-lg-one-auto">
          <div class="min-width-zero">
            <div class="layout-row min-width-zero wrap-items align-center space-sm">
              <p class="clip-text copy-sm weight-medium base-text">{{ rule.name }}</p>
              <NTag size="small" :bordered="false" type="info">{{ wafActionLabel(rule.action) }}</NTag>
              <NTag size="small" :bordered="false" type="info">{{ wafActivationLabel(rule.activationMode) }}</NTag>
              <NTag v-if="!rule.enabled" size="small" :bordered="false" type="warning">Disabled</NTag>
              <NTag size="small" :bordered="false" type="info">P{{ rule.priority.toString() }}</NTag>
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
            <p class="margin-top-xs clip-text mono-text copy-xs muted-text">{{ wafRuleSummary(rule, wafCaptchaProviders) }} / key {{ rateLimitKeySummary(rule) }}</p>
            <p class="margin-top-xs clip-text copy-xs muted-text">{{ publicPolicyMatchSummary(rule) }}</p>
          </div>
          <div class="layout-row space-sm mq-lg-end">
            <NButton secondary size="small" aria-label="Edit WAF rule" title="Edit WAF rule" @click="editWafRule(rule.id)">
              <template #icon><PencilIcon class="icon-sm icon-sm" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete WAF rule" title="Delete WAF rule" @click="deleteWafRule(rule.id)">
              <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
            </NButton>
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
          action-label="Add Rule"
          @action="openAddWafRuleModal"
        />
      </div>
    </section>
      </NTabPane>

      <NTabPane name="cache" :tab="`Cache (${enabledCacheRules})`">
    <section class="surface-card hide-overflow">
      <div class="divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
        <div>
          <h4 class="copy-sm weight-semibold label-case letter-widest muted-text">Cache</h4>
          <p class="margin-top-2xs copy-xs muted-text normal-text letter-normal">Cache public static files on the proxy after routing while keeping WAF, rate limits, and shaping active.</p>
        </div>
        <div class="layout-row wrap-items space-sm">
          <NButton secondary size="small" @click="purgeAllCache">
            <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
            Purge All
          </NButton>
          <NButton secondary size="small" @click="openAddCacheRuleModal">
            <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
            Add Rule
          </NButton>
        </div>
      </div>
      <div class="layout-grid space-lg divider-bottom frame-standard muted-bg pad-x-xl pad-y-lg">
        <div class="layout-row wrap-items align-center spread-items space-md">
          <div>
            <h5 class="copy-xs weight-semibold label-case letter-widest muted-text">Cache Settings</h5>
            <p class="margin-top-xs copy-xs muted-text">Bodies are stored under the configured public cache directory; metadata stays in SQLite.</p>
          </div>
          <NCheckbox v-model:checked="cacheSettingsForm.enabled">
            Enabled
          </NCheckbox>
        </div>
        <div class="layout-grid space-md mq-sm-cols-two mq-xl-cols-five">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Disk MiB
            <NInputNumber v-model:value="cacheSettingsForm.maxDiskMiB" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Memory MiB
            <NInputNumber v-model:value="cacheSettingsForm.maxMemoryMiB" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Hot object KiB
            <NInputNumber v-model:value="cacheSettingsForm.hotObjectKiB" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Max entries
            <NInputNumber v-model:value="cacheSettingsForm.maxEntries" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Cleanup seconds
            <NInputNumber v-model:value="cacheSettingsForm.cleanupIntervalSeconds" size="small" :min="1" :max="3600" />
          </label>
        </div>
        <div class="layout-row align-end-row">
          <NButton type="primary" size="small" :disabled="Boolean(cacheSettingsDisabledReason)" :title="cacheSettingsDisabledReason" @click="saveCacheSettings">
            Save Settings
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
              <template #icon><PencilIcon class="icon-sm icon-sm" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete cache rule" title="Delete cache rule" @click="deleteCacheRule(rule.id)">
              <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
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
          action-label="Add Rule"
          @action="openAddCacheRuleModal"
        />
      </div>
    </section>
      </NTabPane>

      <NTabPane name="traffic-shaper" :tab="`Traffic Shaper (${enabledTrafficShapers})`">
    <section class="surface-card hide-overflow">
      <div class="divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
        <div>
          <h4 class="copy-sm weight-semibold label-case letter-widest muted-text">Traffic Shaper</h4>
          <p class="margin-top-2xs copy-xs muted-text normal-text letter-normal">Limit bandwidth consumption per request or client.</p>
        </div>
        <NButton secondary size="small" @click="openAddTrafficShaperRuleModal">
          <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
          Add Shaper
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
              <template #icon><PencilIcon class="icon-sm icon-sm" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete traffic-shaper rule" title="Delete traffic-shaper rule" @click="deleteTrafficShaperRule(rule.id)">
              <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
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
          action-label="Add Shaper"
          @action="openAddTrafficShaperRuleModal"
        />
      </div>
    </section>
      </NTabPane>
    </NTabs>

    <PublicProxyEditorHost ref="editorHost" :config="config" />

    <NModal
      v-model:show="isPreviewOpen"
      preset="card"
      title="Request Tester"
      :style="modalCardStyle('76rem')"
      :bordered="false"
      size="huge"
    >
      <div class="policy-preview-modal-body">
        <TrafficPolicyRequestPlayground
          :model-value="previewForm"
          :stages="playgroundStages"
          :route-options="previewRouteOptions"
          :target-options="previewTargetOptions"
          :global-attention="globalPolicyAttention"
          @update:model-value="updateTrafficPolicyPreview"
          @reset="resetTrafficPolicyPreview"
        />
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.policy-summary-card {
  min-height: 7rem;
  padding: 0.875rem;
  transition: border-color 160ms ease, box-shadow 160ms ease;
}

.policy-summary-grid {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.policy-summary-card--active {
  border-color: var(--app-accent);
  box-shadow: inset 0 0 0 1px var(--app-accent-soft);
}

.policy-summary-card--active .base-text {
  color: var(--app-accent);
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
  padding: 0.875rem 1rem;
}

.policy-filter-search,
.policy-filter-status {
  display: grid;
  gap: 0.375rem;
  min-width: 0;
}

.policy-preview-modal-body {
  max-height: calc(100vh - 9rem);
  overflow-y: auto;
  padding-right: 0.25rem;
}

@media (min-width: 900px) {
  .policy-summary-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }

  .policy-summary-card {
    min-height: 0;
    padding: 1rem;
  }

  .policy-filter-bar {
    grid-template-columns: minmax(0, 1fr) 12rem;
    align-items: end;
  }
}
</style>
