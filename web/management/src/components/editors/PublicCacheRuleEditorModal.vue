<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NButtonGroup, NCheckbox, NEmpty, NInput, NInputNumber, NModal, NSelect } from "naive-ui";
import PublicPolicyMatchEditor from "@/components/editors/PublicPolicyMatchEditor.vue";
import { useManagementClient } from "@/composables/useManagementClient";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { modalCardStyle } from "@/lib/naiveUi";
import {
  defaultPolicyMatchForm,
  policyMatchFormFromProto,
  policyMatchRulePayload,
  policyMatchValidationReason,
  type PolicyMatchForm,
} from "@/lib/publicPolicyMatch";
import { cacheScopeLabel, routeDestinationLabel, routeTargetName, routeTargetTypeLabel } from "@/lib/publicProxyLabels";
import {
  PublicCacheQueryMode,
  PublicCacheScope,
  PublicCacheTtlMode,
  type GetPublicProxyConfigResponse,
  type PublicRoute,
  type PublicRouteTarget,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

type Runner = (action: () => Promise<void>) => Promise<boolean>;

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject<Runner>("runManagementAction");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const isOpen = ref(false);
const rules = computed(() => props.config?.cacheRules ?? []);
const routes = computed(() => props.config?.routes ?? []);
const routeTargets = computed(() => props.config?.routeTargets ?? []);
const proxyTargets = computed(() => routeTargets.value.filter((target) => target.targetType !== 2));
const routeFilterText = ref("");
const targetFilterText = ref("");

const maxCacheListItems = 64;
const defaultVaryHeaders = ["Accept-Encoding"];
const defaultCacheStatusCodes = ["200", "203", "204", "301", "308"];

const form = reactive({
  id: "",
  name: "",
  enabled: true,
  priority: 100,
  match: defaultPolicyMatchForm() as PolicyMatchForm,
  routeIds: [] as string[],
  targetIds: [] as string[],
  scope: PublicCacheScope.SELECTED_BACKEND,
  ttlMode: PublicCacheTtlMode.FIXED,
  ttlMinutes: 60,
  queryMode: PublicCacheQueryMode.FULL,
  queryParams: [] as string[],
  varyHeaders: [...defaultVaryHeaders],
  cacheStatusCodes: [...defaultCacheStatusCodes],
  maxObjectMiB: 100,
  addCacheStatusHeader: true,
  allowCookieRequests: false,
  allowCookieRequestsAcknowledged: false,
});

const ttlModeOptions = [
  { label: "Fixed TTL", value: PublicCacheTtlMode.FIXED },
  { label: "Origin TTL", value: PublicCacheTtlMode.ORIGIN },
];
const scopeOptions = [
  { label: "Selected target", value: PublicCacheScope.SELECTED_BACKEND },
  { label: "Route", value: PublicCacheScope.ROUTE },
];
const queryModeOptions = [
  { label: "Full query", value: PublicCacheQueryMode.FULL },
  { label: "Ignore query", value: PublicCacheQueryMode.IGNORE },
  { label: "Allowlist", value: PublicCacheQueryMode.ALLOWLIST },
  { label: "Denylist", value: PublicCacheQueryMode.DENYLIST },
];

const queryParamsEditorVisible = computed(() => queryModeUsesParams(form.queryMode));
const filteredRoutes = computed(() => {
  const needle = routeFilterText.value.trim().toLowerCase();
  if (!needle) return routes.value;
  return routes.value.filter((route) => routeSearchText(route).includes(needle));
});
const filteredProxyTargets = computed(() => {
  const needle = targetFilterText.value.trim().toLowerCase();
  if (!needle) return proxyTargets.value;
  return proxyTargets.value.filter((target) => targetSearchText(target).includes(needle));
});
const canSelectVisibleRoutes = computed(() =>
  form.routeIds.length < maxCacheListItems &&
  filteredRoutes.value.some((route) => !isRouteSelected(route.id)),
);
const canSelectVisibleTargets = computed(() =>
  form.targetIds.length < maxCacheListItems &&
  filteredProxyTargets.value.some((target) => !isTargetSelected(target.id)),
);
const routeSelectionSummary = computed(() => form.routeIds.length ? `${form.routeIds.length.toString()} selected` : "All routes");
const targetSelectionSummary = computed(() => form.targetIds.length ? `${form.targetIds.length.toString()} selected` : "All proxy targets");
const filterSelectionSummary = computed(() => `${routeSelectionSummary.value} / ${targetSelectionSummary.value}`);
const queryModeSummary = computed(() => queryModeOptions.find((option) => option.value === form.queryMode)?.label ?? "Full query");
const normalizedQueryParams = computed(() => normalizeUniqueStrings(form.queryParams));
const normalizedVaryHeaders = computed(() => normalizeVaryHeaders(form.varyHeaders));
const normalizedCacheStatusCodes = computed(() => normalizeStatusCodes(form.cacheStatusCodes));
const routeSelectionValidationReason = computed(() =>
  form.routeIds.length > maxCacheListItems ? `Cache rules can filter at most ${maxCacheListItems.toString()} routes.` : "",
);
const targetSelectionValidationReason = computed(() =>
  form.targetIds.length > maxCacheListItems ? `Cache rules can filter at most ${maxCacheListItems.toString()} targets.` : "",
);
const queryParamsValidationReason = computed(() => {
  if (!queryParamsEditorVisible.value) return "";
  if (form.queryParams.length > maxCacheListItems) return `Cache rules can list at most ${maxCacheListItems.toString()} query parameters.`;
  const rowError = form.queryParams.map(queryParamError).find(Boolean);
  if (rowError) return rowError;
  if (!normalizedQueryParams.value.length) return "Query allowlist and denylist modes require query parameters.";
  return "";
});
const varyHeadersValidationReason = computed(() => {
  if (form.varyHeaders.length > maxCacheListItems) return `Cache rules can list at most ${maxCacheListItems.toString()} vary headers.`;
  return form.varyHeaders.map(varyHeaderError).find(Boolean) || "";
});
const cacheStatusCodesValidationReason = computed(() => {
  if (form.cacheStatusCodes.length > maxCacheListItems) return `Cache rules can list at most ${maxCacheListItems.toString()} status codes.`;
  const rowError = form.cacheStatusCodes.map(statusCodeError).find(Boolean);
  if (rowError) return rowError;
  if (!normalizedCacheStatusCodes.value.length) return "Add at least one cacheable status code.";
  return "";
});

const submitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!form.name.trim()) return "Enter a rule name.";
  if (form.ttlMinutes < 1) return "TTL must be at least 1 minute.";
  if (form.maxObjectMiB < 1) return "Maximum object size must be at least 1 MiB.";
  if (routeSelectionValidationReason.value) return routeSelectionValidationReason.value;
  if (targetSelectionValidationReason.value) return targetSelectionValidationReason.value;
  if (queryParamsValidationReason.value) return queryParamsValidationReason.value;
  if (varyHeadersValidationReason.value) return varyHeadersValidationReason.value;
  if (cacheStatusCodesValidationReason.value) return cacheStatusCodesValidationReason.value;
  if (policyMatchValidationReason(form.match)) return policyMatchValidationReason(form.match);
  if (form.allowCookieRequests && !form.allowCookieRequestsAcknowledged) return "Acknowledge the Cookie cache key behavior.";
  return "";
});
const submitDisabled = computed(() => Boolean(submitDisabledReason.value));

function resetForm() {
  form.id = "";
  form.name = nextRuleName();
  form.enabled = true;
  form.priority = 100;
  form.match = defaultPolicyMatchForm();
  form.routeIds = [];
  form.targetIds = [];
  form.scope = PublicCacheScope.SELECTED_BACKEND;
  form.ttlMode = PublicCacheTtlMode.FIXED;
  form.ttlMinutes = 60;
  form.queryMode = PublicCacheQueryMode.FULL;
  form.queryParams = [];
  form.varyHeaders = [...defaultVaryHeaders];
  form.cacheStatusCodes = [...defaultCacheStatusCodes];
  form.maxObjectMiB = 100;
  form.addCacheStatusHeader = true;
  form.allowCookieRequests = false;
  form.allowCookieRequestsAcknowledged = false;
  routeFilterText.value = "";
  targetFilterText.value = "";
}

function openCreate() {
  resetForm();
  isOpen.value = true;
}

function openEdit(ruleId: bigint | string) {
  const id = ruleId.toString();
  const rule = rules.value.find((item) => item.id.toString() === id);
  if (!rule) return;
  form.id = rule.id.toString();
  form.name = rule.name;
  form.enabled = rule.enabled;
  form.priority = Number(rule.priority);
  form.match = policyMatchFormFromProto(rule.matchRule);
  form.routeIds = rule.routeIds.map((value) => value.toString());
  form.targetIds = rule.targetIds.map((value) => value.toString());
  form.scope = rule.scope || PublicCacheScope.SELECTED_BACKEND;
  form.ttlMode = rule.ttlMode || PublicCacheTtlMode.FIXED;
  form.ttlMinutes = Math.max(1, Math.round(Number(rule.ttlMillis || 3600000n) / 60000));
  form.queryMode = rule.queryMode || PublicCacheQueryMode.FULL;
  form.queryParams = [...rule.queryParams];
  form.varyHeaders = rule.varyHeaders.length ? [...rule.varyHeaders] : [...defaultVaryHeaders];
  form.cacheStatusCodes = rule.cacheStatusCodes.length ? rule.cacheStatusCodes.map((value) => value.toString()) : [...defaultCacheStatusCodes];
  form.maxObjectMiB = Math.max(1, Math.round(Number(rule.maxObjectBytes || 104857600n) / 1024 / 1024));
  form.addCacheStatusHeader = rule.addCacheStatusHeader;
  form.allowCookieRequests = rule.allowCookieRequests;
  form.allowCookieRequestsAcknowledged = rule.allowCookieRequests;
  routeFilterText.value = "";
  targetFilterText.value = "";
  isOpen.value = true;
}

function close() {
  isOpen.value = false;
}

function nextRuleName(): string {
  let index = rules.value.length + 1;
  const used = new Set(rules.value.map((rule) => rule.name));
  while (used.has(`cache-${index.toString()}`)) index += 1;
  return `cache-${index.toString()}`;
}

function queryModeUsesParams(mode: PublicCacheQueryMode): boolean {
  return mode === PublicCacheQueryMode.ALLOWLIST || mode === PublicCacheQueryMode.DENYLIST;
}

function queryModeDescription(mode: PublicCacheQueryMode): string {
  switch (mode) {
    case PublicCacheQueryMode.IGNORE:
      return "Path-only cache key";
    case PublicCacheQueryMode.ALLOWLIST:
      return "Only listed params";
    case PublicCacheQueryMode.DENYLIST:
      return "Exclude listed params";
    default:
      return "Complete query string";
  }
}

function routeLabel(route: PublicRoute): string {
  return `#${route.id.toString()} ${route.hostPattern || "*"}${route.pathPrefix || "/"}`;
}

function routeDetail(route: PublicRoute): string {
  return routeDestinationLabel(route);
}

function routeSearchText(route: PublicRoute): string {
  return `${routeLabel(route)} ${routeDetail(route)}`.toLowerCase();
}

function targetDetail(target: PublicRouteTarget): string {
  return `${routeTargetTypeLabel(target.targetType)} / ${target.url || "static response"}`;
}

function targetSearchText(target: PublicRouteTarget): string {
  return `#${target.id.toString()} ${routeTargetName(target)} ${targetDetail(target)}`.toLowerCase();
}

function isRouteSelected(id: bigint | string): boolean {
  return form.routeIds.includes(id.toString());
}

function isTargetSelected(id: bigint | string): boolean {
  return form.targetIds.includes(id.toString());
}

function toggleSelection(values: string[], id: bigint | string) {
  const idString = id.toString();
  const index = values.indexOf(idString);
  if (index >= 0) {
    values.splice(index, 1);
    return;
  }
  if (values.length < maxCacheListItems) values.push(idString);
}

function toggleRoute(id: bigint | string) {
  toggleSelection(form.routeIds, id);
}

function toggleTarget(id: bigint | string) {
  toggleSelection(form.targetIds, id);
}

function selectVisibleRoutes() {
  const selected = new Set(form.routeIds);
  for (const route of filteredRoutes.value) {
    if (selected.size >= maxCacheListItems) break;
    selected.add(route.id.toString());
  }
  form.routeIds = [...selected];
}

function selectVisibleTargets() {
  const selected = new Set(form.targetIds);
  for (const target of filteredProxyTargets.value) {
    if (selected.size >= maxCacheListItems) break;
    selected.add(target.id.toString());
  }
  form.targetIds = [...selected];
}

function clearRoutes() {
  form.routeIds = [];
}

function clearTargets() {
  form.targetIds = [];
}

function addQueryParam() {
  if (form.queryParams.length < maxCacheListItems) form.queryParams.push("");
}

function removeQueryParam(index: number) {
  form.queryParams.splice(index, 1);
}

function addVaryHeader() {
  if (form.varyHeaders.length < maxCacheListItems) form.varyHeaders.push("");
}

function removeVaryHeader(index: number) {
  form.varyHeaders.splice(index, 1);
}

function addCacheStatusCode() {
  if (form.cacheStatusCodes.length < maxCacheListItems) form.cacheStatusCodes.push("");
}

function removeCacheStatusCode(index: number) {
  form.cacheStatusCodes.splice(index, 1);
}

function normalizeUniqueStrings(values: readonly string[]): string[] {
  const seen = new Set<string>();
  const normalized: string[] = [];
  for (const raw of values) {
    const value = raw.trim();
    if (!value || seen.has(value)) continue;
    seen.add(value);
    normalized.push(value);
  }
  return normalized;
}

function validHttpToken(value: string): boolean {
  return /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(value);
}

function canonicalHeaderName(value: string): string {
  return value
    .trim()
    .toLowerCase()
    .split("-")
    .map((part) => part ? `${part[0].toUpperCase()}${part.slice(1)}` : part)
    .join("-");
}

function sensitiveVaryHeader(value: string): boolean {
  switch (value.trim().toLowerCase()) {
    case "cookie":
    case "authorization":
    case "set-cookie":
      return true;
    default:
      return false;
  }
}

function normalizeVaryHeaders(values: readonly string[]): string[] {
  const seen = new Set<string>();
  const normalized: string[] = [];
  for (const raw of values) {
    const value = raw.trim();
    if (!value || !validHttpToken(value) || sensitiveVaryHeader(value)) continue;
    const header = canonicalHeaderName(value);
    const key = header.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    normalized.push(header);
  }
  return normalized;
}

function normalizeStatusCodes(values: readonly string[]): bigint[] {
  const seen = new Set<number>();
  const normalized: bigint[] = [];
  for (const raw of values) {
    const value = raw.trim();
    if (!/^\d+$/.test(value)) continue;
    const status = Number.parseInt(value, 10);
    if (status < 100 || status > 599 || seen.has(status)) continue;
    seen.add(status);
    normalized.push(BigInt(status));
  }
  return normalized;
}

function queryParamError(value: string): string {
  return value.trim() ? "" : "Enter a query parameter or remove this row.";
}

function varyHeaderError(value: string): string {
  const header = value.trim();
  if (!header) return "Enter a header name or remove this row.";
  if (!validHttpToken(header)) return "Use a valid HTTP header name.";
  if (sensitiveVaryHeader(header)) return "Cookie, Authorization, and Set-Cookie cannot vary cached responses.";
  return "";
}

function statusCodeError(value: string): string {
  const status = value.trim();
  if (!status) return "Enter a status code or remove this row.";
  if (!/^\d+$/.test(status)) return "Status code must be a number.";
  const parsed = Number.parseInt(status, 10);
  if (parsed < 100 || parsed > 599) return "Use a status code between 100 and 599.";
  return "";
}

function normalizeIdStrings(values: readonly string[]): string[] {
  const seen = new Set<string>();
  const normalized: string[] = [];
  for (const raw of values) {
    const value = raw.trim();
    if (!/^\d+$/.test(value) || value === "0" || seen.has(value)) continue;
    seen.add(value);
    normalized.push(value);
  }
  return normalized;
}

async function run(action: () => Promise<void>): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action);
}

async function submitRule() {
  const ok = await run(async () => {
    const payload = {
      name: form.name.trim(),
      priority: BigInt(form.priority || 0),
      enabled: form.enabled,
      matchRule: policyMatchRulePayload(form.match),
      routeIds: normalizeIdStrings(form.routeIds).map((id) => BigInt(id)),
      targetIds: normalizeIdStrings(form.targetIds).map((id) => BigInt(id)),
      scope: form.scope,
      ttlMode: form.ttlMode,
      ttlMillis: BigInt(Math.max(1, form.ttlMinutes) * 60000),
      queryMode: form.queryMode,
      queryParams: queryParamsEditorVisible.value ? normalizedQueryParams.value : [],
      varyHeaders: normalizedVaryHeaders.value,
      cacheStatusCodes: normalizedCacheStatusCodes.value,
      maxObjectBytes: BigInt(Math.max(1, form.maxObjectMiB) * 1024 * 1024),
      addCacheStatusHeader: form.addCacheStatusHeader,
      allowCookieRequests: form.allowCookieRequests,
      allowCookieRequestsAcknowledged: form.allowCookieRequests && form.allowCookieRequestsAcknowledged,
    };
    if (form.id) {
      await managementClient.updatePublicCacheRule({ id: BigInt(form.id), ...payload });
    } else {
      await managementClient.createPublicCacheRule(payload);
    }
  });
  if (ok) {
    isOpen.value = false;
    emit("saved");
  }
}

defineExpose({ openCreate, openEdit, close });
</script>

<template>
  <NModal
    v-model:show="isOpen"
    preset="card"
    :title="form.id ? 'Edit Cache Rule' : 'Add Cache Rule'"
    :style="modalCardStyle('64rem')"
    :bordered="false"
    size="huge"
  >
    <form class="grid max-h-[calc(100vh-9rem)] gap-5 overflow-y-auto pr-1" @submit.prevent="submitRule">
      <section class="grid gap-4 sm:grid-cols-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
          Name
          <NInput v-model:value="form.name" size="small" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Priority
          <NInputNumber v-model:value="form.priority" size="small" required />
        </label>
        <NCheckbox v-model:checked="form.enabled" class="self-end">
          Enabled
        </NCheckbox>
      </section>

      <PublicPolicyMatchEditor :form="form.match" />

      <section class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <h4 class="text-sm font-semibold text-white">Cache behavior</h4>
        <p class="text-xs leading-5 text-[#777]">
          Authorization requests are always bypassed. Cookie requests are cached only when this rule allows them. Responses with Set-Cookie, no-store, private, or no-cache are never cached.
        </p>
        <NCheckbox v-model:checked="form.allowCookieRequests" class="rounded-md border border-[#222] bg-[#050505] p-3">
          <span class="grid gap-1">
            <span class="font-medium text-white">Cache requests with Cookie headers</span>
            <span class="text-xs leading-5 text-[#777]">
              Enable this only for public static asset rules. Cookie values are ignored and are never part of the cache key.
            </span>
          </span>
        </NCheckbox>
        <NCheckbox
          v-if="form.allowCookieRequests"
          v-model:checked="form.allowCookieRequestsAcknowledged"
          class="rounded-md border border-[#333] bg-black p-3"
        >
          <span class="grid gap-1">
            <span class="font-medium text-white">I understand Cookie is ignored in this cache key</span>
            <span class="text-xs leading-5 text-[#777]">
              Only use this for responses that are identical for every visitor, even when the request includes cookies.
            </span>
          </span>
        </NCheckbox>
        <div class="grid gap-4 sm:grid-cols-4">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            TTL mode
            <NSelect v-model:value="form.ttlMode" size="small" :options="ttlModeOptions" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Default TTL minutes
            <NInputNumber v-model:value="form.ttlMinutes" size="small" :min="1" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Scope
            <NSelect v-model:value="form.scope" size="small" :options="scopeOptions" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Max object MiB
            <NInputNumber v-model:value="form.maxObjectMiB" size="small" :min="1" />
          </label>
        </div>
        <p class="text-xs leading-5 text-[#777]">
          Responses with Set-Cookie, private/no-store/no-cache, Vary: Cookie, or Vary: Authorization are never stored, even when cookie requests are enabled above.
        </p>
      </section>

      <section class="cache-filter-section">
        <div class="cache-filter-header">
          <div>
            <h4 class="text-sm font-semibold text-white">Keys and filters</h4>
            <p class="mt-1 text-xs leading-5 text-[#777]">Empty route or target filters match every available target.</p>
          </div>
          <div class="cache-summary-chips" aria-label="Cache key summary">
            <span class="cache-summary-chip">{{ filterSelectionSummary }}</span>
            <span class="cache-summary-chip">{{ queryModeSummary }}</span>
            <span class="cache-summary-chip">{{ normalizedVaryHeaders.length.toString() }} vary</span>
            <span class="cache-summary-chip">{{ normalizedCacheStatusCodes.length.toString() }} status</span>
            <span class="cache-summary-chip">{{ cacheScopeLabel(form.scope) }}</span>
          </div>
        </div>

        <div class="target-grid">
          <div class="target-panel">
            <div class="target-panel-head">
              <div class="min-w-0">
                <p class="panel-eyebrow">Routes</p>
                <h5 class="panel-heading">{{ routeSelectionSummary }}</h5>
              </div>
              <NButton secondary size="tiny" attr-type="button" :disabled="!form.routeIds.length" @click="clearRoutes">
                Clear
              </NButton>
            </div>
            <div class="target-toolbar">
              <NInput v-model:value="routeFilterText" size="small" class="target-search" placeholder="Filter routes" />
              <NButton secondary size="small" attr-type="button" :disabled="!canSelectVisibleRoutes" @click="selectVisibleRoutes">
                Select visible
              </NButton>
            </div>
            <p v-if="routeSelectionValidationReason" class="field-error">{{ routeSelectionValidationReason }}</p>
            <div class="target-list" role="listbox" aria-label="Route filters">
              <div
                v-for="route in filteredRoutes"
                :key="route.id.toString()"
                class="target-row"
                :class="{ 'target-row-selected': isRouteSelected(route.id) }"
              >
                <NCheckbox
                  :checked="isRouteSelected(route.id)"
                  :disabled="!isRouteSelected(route.id) && form.routeIds.length >= maxCacheListItems"
                  :aria-label="`Filter cache rule to route ${routeLabel(route)}`"
                  @update:checked="toggleRoute(route.id)"
                />
                <span class="target-row-body">
                  <span class="target-row-title">{{ routeLabel(route) }}</span>
                  <span class="target-row-detail">{{ routeDetail(route) }}</span>
                </span>
              </div>
              <NEmpty v-if="!filteredRoutes.length" class="target-empty" size="small" :description="routes.length ? 'No routes match the filter.' : 'No routes configured.'" />
            </div>
          </div>

          <div class="target-panel">
            <div class="target-panel-head">
              <div class="min-w-0">
                <p class="panel-eyebrow">Targets</p>
                <h5 class="panel-heading">{{ targetSelectionSummary }}</h5>
              </div>
              <NButton secondary size="tiny" attr-type="button" :disabled="!form.targetIds.length" @click="clearTargets">
                Clear
              </NButton>
            </div>
            <div class="target-toolbar">
              <NInput v-model:value="targetFilterText" size="small" class="target-search" placeholder="Filter targets" />
              <NButton secondary size="small" attr-type="button" :disabled="!canSelectVisibleTargets" @click="selectVisibleTargets">
                Select visible
              </NButton>
            </div>
            <p v-if="targetSelectionValidationReason" class="field-error">{{ targetSelectionValidationReason }}</p>
            <div class="target-list" role="listbox" aria-label="Target filters">
              <div
                v-for="target in filteredProxyTargets"
                :key="target.id.toString()"
                class="target-row"
                :class="{ 'target-row-selected': isTargetSelected(target.id) }"
              >
                <NCheckbox
                  :checked="isTargetSelected(target.id)"
                  :disabled="!isTargetSelected(target.id) && form.targetIds.length >= maxCacheListItems"
                  :aria-label="`Filter cache rule to target ${routeTargetName(target)}`"
                  @update:checked="toggleTarget(target.id)"
                />
                <span class="target-row-body">
                  <span class="target-row-title">{{ routeTargetName(target) }}</span>
                  <span class="target-row-detail">{{ targetDetail(target) }}</span>
                </span>
              </div>
              <NEmpty v-if="!filteredProxyTargets.length" class="target-empty" size="small" :description="proxyTargets.length ? 'No targets match the filter.' : 'No proxy targets configured.'" />
            </div>
          </div>
        </div>

        <div class="query-mode-panel">
          <div class="query-mode-head">
            <p class="panel-eyebrow">Query mode</p>
            <h5 class="panel-heading">{{ queryModeSummary }}</h5>
          </div>
          <NButtonGroup class="query-mode-grid" role="radiogroup" aria-label="Query cache key mode" size="small">
            <NButton
              v-for="option in queryModeOptions"
              :key="option.value"
              class="query-mode-button"
              :class="{ 'query-mode-button-active': form.queryMode === option.value }"
              :aria-checked="form.queryMode === option.value"
              attr-type="button"
              role="radio"
              :type="form.queryMode === option.value ? 'primary' : 'default'"
              @click="form.queryMode = option.value"
            >
              <span>{{ option.label }}</span>
              <small>{{ queryModeDescription(option.value) }}</small>
            </NButton>
          </NButtonGroup>
        </div>

        <div v-if="queryParamsEditorVisible" class="value-editor">
          <div class="value-editor-head">
            <div class="min-w-0">
              <p class="panel-eyebrow">Query params</p>
              <h5 class="panel-heading">{{ normalizedQueryParams.length.toString() }} active</h5>
            </div>
            <NButton secondary size="small" attr-type="button" :disabled="form.queryParams.length >= maxCacheListItems" @click="addQueryParam">
              <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
              Add param
            </NButton>
          </div>
          <p v-if="queryParamsValidationReason" class="field-error">{{ queryParamsValidationReason }}</p>
          <div v-if="!form.queryParams.length" class="value-empty">
            <p>No query parameters listed.</p>
            <NButton secondary size="small" attr-type="button" @click="addQueryParam">
              <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
              Add param
            </NButton>
          </div>
          <div v-else class="value-list">
            <div v-for="(param, index) in form.queryParams" :key="`query-${index}`" class="value-row">
              <div class="value-field">
                <NInput v-model:value="form.queryParams[index]" size="small" class="value-input" placeholder="version" />
                <p v-if="queryParamError(param)" class="field-error">{{ queryParamError(param) }}</p>
              </div>
              <NButton type="error" size="small" class="remove-row-button" aria-label="Remove query parameter" title="Remove query parameter" attr-type="button" @click="removeQueryParam(index)">
                <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
              </NButton>
            </div>
          </div>
        </div>

        <div class="value-editor-grid">
          <div class="value-editor">
            <div class="value-editor-head">
              <div class="min-w-0">
                <p class="panel-eyebrow">Vary headers</p>
                <h5 class="panel-heading">{{ normalizedVaryHeaders.length.toString() }} active</h5>
              </div>
              <NButton secondary size="small" attr-type="button" :disabled="form.varyHeaders.length >= maxCacheListItems" @click="addVaryHeader">
                <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
                Add header
              </NButton>
            </div>
            <p v-if="varyHeadersValidationReason" class="field-error">{{ varyHeadersValidationReason }}</p>
            <div v-if="!form.varyHeaders.length" class="value-empty">
              <p>No vary headers configured.</p>
              <NButton secondary size="small" attr-type="button" @click="addVaryHeader">
                <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
                Add header
              </NButton>
            </div>
            <div v-else class="value-list">
              <div v-for="(header, index) in form.varyHeaders" :key="`vary-${index}`" class="value-row">
                <div class="value-field">
                  <NInput v-model:value="form.varyHeaders[index]" size="small" class="value-input" placeholder="Accept-Encoding" />
                  <p v-if="varyHeaderError(header)" class="field-error">{{ varyHeaderError(header) }}</p>
                </div>
                <NButton type="error" size="small" class="remove-row-button" aria-label="Remove vary header" title="Remove vary header" attr-type="button" @click="removeVaryHeader(index)">
                  <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
                </NButton>
              </div>
            </div>
          </div>

          <div class="value-editor">
            <div class="value-editor-head">
              <div class="min-w-0">
                <p class="panel-eyebrow">Cache status codes</p>
                <h5 class="panel-heading">{{ normalizedCacheStatusCodes.length.toString() }} active</h5>
              </div>
              <NButton secondary size="small" attr-type="button" :disabled="form.cacheStatusCodes.length >= maxCacheListItems" @click="addCacheStatusCode">
                <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
                Add code
              </NButton>
            </div>
            <p v-if="cacheStatusCodesValidationReason" class="field-error">{{ cacheStatusCodesValidationReason }}</p>
            <div v-if="!form.cacheStatusCodes.length" class="value-empty">
              <p>No status codes configured.</p>
              <NButton secondary size="small" attr-type="button" @click="addCacheStatusCode">
                <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
                Add code
              </NButton>
            </div>
            <div v-else class="value-list">
              <div v-for="(status, index) in form.cacheStatusCodes" :key="`status-${index}`" class="value-row">
                <div class="value-field">
                  <NInput v-model:value="form.cacheStatusCodes[index]" size="small" class="value-input" placeholder="200" />
                  <p v-if="statusCodeError(status)" class="field-error">{{ statusCodeError(status) }}</p>
                </div>
                <NButton type="error" size="small" class="remove-row-button" aria-label="Remove status code" title="Remove status code" attr-type="button" @click="removeCacheStatusCode(index)">
                  <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
                </NButton>
              </div>
            </div>
          </div>
        </div>

        <NCheckbox v-model:checked="form.addCacheStatusHeader" class="cache-status-toggle">
          <span class="min-w-0">
            <span class="toggle-title">Expose cache status</span>
            <span class="toggle-detail">X-p2pstream-Cache response header</span>
          </span>
        </NCheckbox>
      </section>

      <div class="flex justify-end gap-3 border-t border-[#222] pt-4">
        <NButton secondary attr-type="button" @click="close">Cancel</NButton>
        <NButton type="primary" attr-type="submit" :disabled="submitDisabled" :title="submitDisabledReason">
          {{ form.id ? 'Save Rule' : 'Create Rule' }}
        </NButton>
      </div>
    </form>
  </NModal>
</template>

<style scoped>
.cache-filter-section {
  display: grid;
  gap: 1rem;
  border: 1px solid #222;
  border-radius: 6px;
  background: #050505;
  padding: 1rem;
}

.cache-filter-header,
.target-panel-head,
.value-editor-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 0.85rem;
}

.cache-summary-chips {
  display: flex;
  max-width: 100%;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 0.35rem;
}

.cache-summary-chip {
  min-height: 1.6rem;
  border: 1px solid #2a2a2a;
  border-radius: 999px;
  background: #0b0b0b;
  color: #c9c9cf;
  padding: 0.25rem 0.55rem;
  font-size: 0.68rem;
  font-weight: 650;
  line-height: 1.1;
  white-space: nowrap;
}

.target-grid,
.value-editor-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem;
}

.target-panel,
.query-mode-panel,
.value-editor {
  display: grid;
  min-width: 0;
  gap: 0.75rem;
  border: 1px solid #222;
  border-radius: 6px;
  background: #080808;
  padding: 0.85rem;
}

.panel-eyebrow {
  color: #777;
  font-size: 0.68rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  line-height: 1.1;
  text-transform: uppercase;
}

.panel-heading {
  margin-top: 0.18rem;
  overflow: hidden;
  color: #fff;
  font-size: 0.92rem;
  font-weight: 650;
  line-height: 1.2;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.target-toolbar {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 0.5rem;
}

.target-search,
.value-input {
  min-width: 0;
  height: 2.25rem;
  font-size: 0.8rem;
  letter-spacing: 0;
  text-align: left;
  text-transform: none;
}

.target-list {
  min-height: 11rem;
  max-height: 15rem;
  overflow-y: auto;
  overscroll-behavior: contain;
  border: 1px solid #202020;
  border-radius: 6px;
  background: #030303;
}

.target-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  min-height: 3.15rem;
  align-items: center;
  gap: 0.65rem;
  border-bottom: 1px solid #151515;
  padding: 0.55rem 0.65rem;
  cursor: pointer;
  transition: background 140ms ease, border-color 140ms ease;
}

.target-row:last-child {
  border-bottom: 0;
}

.target-row:hover {
  background: #0f0f0f;
}

.target-row-selected {
  background: #111;
}

.target-row-body {
  display: grid;
  min-width: 0;
  gap: 0.16rem;
}

.target-row-title,
.target-row-detail {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.target-row-title {
  color: #ededed;
  font-size: 0.82rem;
  font-weight: 650;
}

.target-row-detail {
  color: #777;
  font-size: 0.72rem;
  line-height: 1.2;
}

.target-empty,
.value-empty {
  display: grid;
  min-height: 8rem;
  place-items: center;
  align-content: center;
  gap: 0.7rem;
  color: #777;
  font-size: 0.82rem;
  text-align: center;
}

.panel-link-button,
.panel-action-button,
.add-row-button,
.value-empty button {
  display: inline-flex;
  height: 2rem;
  align-items: center;
  justify-content: center;
  gap: 0.4rem;
  border: 1px solid #333;
  border-radius: 5px;
  background: #050505;
  color: #d4d4d8;
  padding: 0 0.65rem;
  font-size: 0.72rem;
  font-weight: 650;
  line-height: 1;
  transition: border-color 140ms ease, color 140ms ease, background 140ms ease;
  white-space: nowrap;
}

.panel-link-button {
  border-color: transparent;
  background: transparent;
  color: #888;
  padding-inline: 0.35rem;
}

.panel-link-button:not(:disabled):hover,
.panel-action-button:not(:disabled):hover,
.add-row-button:not(:disabled):hover,
.value-empty button:hover {
  border-color: #666;
  background: #0f0f0f;
  color: #fff;
}

.panel-link-button:not(:disabled):hover {
  border-color: #333;
}

.panel-link-button:disabled,
.panel-action-button:disabled,
.add-row-button:disabled {
  border-color: #202020;
  color: #555;
  cursor: not-allowed;
}

.query-mode-head {
  min-width: 0;
}

.query-mode-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 0.35rem;
  border: 1px solid #333;
  border-radius: 6px;
  background: #050505;
  padding: 0.25rem;
}

.query-mode-button {
  display: grid;
  min-width: 0;
  min-height: 3.25rem;
  align-content: center;
  gap: 0.22rem;
  border-radius: 4px;
  color: #a1a1aa;
  padding: 0.45rem 0.55rem;
  text-align: left;
  transition: background 140ms ease, color 140ms ease;
}

.query-mode-button span,
.query-mode-button small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.query-mode-button span {
  font-size: 0.8rem;
  font-weight: 700;
}

.query-mode-button small {
  color: inherit;
  font-size: 0.68rem;
  opacity: 0.68;
}

.query-mode-button:hover {
  background: #141414;
  color: #fff;
}

.query-mode-button-active {
  background: #fff;
  color: #000;
}

.value-list {
  display: grid;
  gap: 0.55rem;
}

.value-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 2.25rem;
  gap: 0.5rem;
  align-items: start;
}

.value-field {
  display: grid;
  min-width: 0;
  gap: 0.3rem;
}

.remove-row-button {
  display: inline-flex;
  width: 2.25rem;
  height: 2.25rem;
  align-items: center;
  justify-content: center;
  border: 1px solid #3f1d1d;
  border-radius: 5px;
  background: #100707;
  color: #fca5a5;
  transition: border-color 140ms ease, color 140ms ease, background 140ms ease;
}

.remove-row-button:hover {
  border-color: #7f1d1d;
  background: #1a0a0a;
  color: #fecaca;
}

.field-error {
  color: #fca5a5;
  font-size: 0.72rem;
  font-weight: 500;
  line-height: 1.35;
}

.cache-status-toggle {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 0.85rem;
  border: 1px solid #222;
  border-radius: 6px;
  background: #080808;
  padding: 0.75rem 0.85rem;
  cursor: pointer;
}

.toggle-title,
.toggle-detail {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.toggle-title {
  color: #ededed;
  font-size: 0.86rem;
  font-weight: 650;
}

.toggle-detail {
  margin-top: 0.15rem;
  color: #777;
  font-size: 0.72rem;
}

@media (max-width: 860px) {
  .cache-filter-header {
    align-items: stretch;
    flex-direction: column;
  }

  .cache-summary-chips {
    justify-content: flex-start;
  }

  .target-grid,
  .value-editor-grid {
    grid-template-columns: 1fr;
  }

  .query-mode-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 560px) {
  .target-toolbar,
  .value-row {
    grid-template-columns: 1fr;
  }

  .panel-action-button,
  .remove-row-button {
    width: 100%;
  }

  .query-mode-grid {
    grid-template-columns: 1fr;
  }

  .cache-status-toggle {
    align-items: flex-start;
  }
}
</style>
