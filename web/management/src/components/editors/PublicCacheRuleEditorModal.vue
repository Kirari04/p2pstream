<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import PlusIcon from "@primevue/icons/plus";
import TrashIcon from "@primevue/icons/trash";
import PublicPolicyMatchEditor from "@/components/editors/PublicPolicyMatchEditor.vue";
import { managementClient } from "@/api/managementClient";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { backendSummary, cacheScopeLabel, routeDestinationLabel } from "@/lib/publicProxyLabels";
import Button from "@/volt/Button.vue";
import Modal from "@/volt/Modal.vue";
import {
  PublicBackendType,
  PublicCacheQueryMode,
  PublicCacheScope,
  PublicCacheTtlMode,
  PublicListenerProtocol,
  PublicRateLimitMatchOperator,
  type GetPublicProxyConfigResponse,
  type PublicBackend,
  type PublicRoute,
} from "@/gen/proto/p2pstream/v1/management_pb";

type Runner = (action: () => Promise<void>) => Promise<boolean>;
type MatcherForm = {
  name: string;
  operator: PublicRateLimitMatchOperator;
  value: string;
};

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject<Runner>("runManagementAction");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const isOpen = ref(false);
const matchEditor = ref<InstanceType<typeof PublicPolicyMatchEditor> | null>(null);
const rules = computed(() => props.config?.cacheRules ?? []);
const routes = computed(() => props.config?.routes ?? []);
const backends = computed(() => props.config?.backends ?? []);
const routeBackends = computed(() => props.config?.routeBackends ?? []);
const proxyBackends = computed(() => backends.value.filter((backend) => backend.backendType === PublicBackendType.PROXY_FORWARD));
const routeFilterText = ref("");
const backendFilterText = ref("");

const maxCacheListItems = 64;
const defaultVaryHeaders = ["Accept-Encoding"];
const defaultCacheStatusCodes = ["200", "203", "204", "301", "308"];

const form = reactive({
  id: "",
  name: "",
  enabled: true,
  priority: 100,
  match: {
    methods: ["GET", "HEAD"] as string[],
    protocols: [] as PublicListenerProtocol[],
    hostPatternsText: "",
    pathPrefixesText: "",
    pathSuffixesText: ".css\n.js\n.png\n.jpg\n.jpeg\n.webp\n.svg\n.woff2",
    headers: [] as MatcherForm[],
    cookies: [] as MatcherForm[],
    queryParams: [] as MatcherForm[],
  },
  routeIds: [] as string[],
  backendIds: [] as string[],
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
});

const ttlModeOptions = [
  { label: "Fixed TTL", value: PublicCacheTtlMode.FIXED },
  { label: "Origin TTL", value: PublicCacheTtlMode.ORIGIN },
];
const scopeOptions = [
  { label: "Selected backend", value: PublicCacheScope.SELECTED_BACKEND },
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
const filteredProxyBackends = computed(() => {
  const needle = backendFilterText.value.trim().toLowerCase();
  if (!needle) return proxyBackends.value;
  return proxyBackends.value.filter((backend) => backendSearchText(backend).includes(needle));
});
const canSelectVisibleRoutes = computed(() =>
  form.routeIds.length < maxCacheListItems &&
  filteredRoutes.value.some((route) => !isRouteSelected(route.id)),
);
const canSelectVisibleBackends = computed(() =>
  form.backendIds.length < maxCacheListItems &&
  filteredProxyBackends.value.some((backend) => !isBackendSelected(backend.id)),
);
const routeSelectionSummary = computed(() => form.routeIds.length ? `${form.routeIds.length.toString()} selected` : "All routes");
const backendSelectionSummary = computed(() => form.backendIds.length ? `${form.backendIds.length.toString()} selected` : "All proxy backends");
const targetSelectionSummary = computed(() => `${routeSelectionSummary.value} / ${backendSelectionSummary.value}`);
const queryModeSummary = computed(() => queryModeOptions.find((option) => option.value === form.queryMode)?.label ?? "Full query");
const normalizedQueryParams = computed(() => normalizeUniqueStrings(form.queryParams));
const normalizedVaryHeaders = computed(() => normalizeVaryHeaders(form.varyHeaders));
const normalizedCacheStatusCodes = computed(() => normalizeStatusCodes(form.cacheStatusCodes));
const routeSelectionValidationReason = computed(() =>
  form.routeIds.length > maxCacheListItems ? `Cache rules can filter at most ${maxCacheListItems.toString()} routes.` : "",
);
const backendSelectionValidationReason = computed(() =>
  form.backendIds.length > maxCacheListItems ? `Cache rules can filter at most ${maxCacheListItems.toString()} backends.` : "",
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
  if (backendSelectionValidationReason.value) return backendSelectionValidationReason.value;
  if (queryParamsValidationReason.value) return queryParamsValidationReason.value;
  if (varyHeadersValidationReason.value) return varyHeadersValidationReason.value;
  if (cacheStatusCodesValidationReason.value) return cacheStatusCodesValidationReason.value;
  return "";
});
const submitDisabled = computed(() => Boolean(submitDisabledReason.value));

function resetForm() {
  form.id = "";
  form.name = nextRuleName();
  form.enabled = true;
  form.priority = 100;
  form.match.methods = ["GET", "HEAD"];
  form.match.protocols = [];
  form.match.hostPatternsText = "";
  form.match.pathPrefixesText = "";
  form.match.pathSuffixesText = ".css\n.js\n.png\n.jpg\n.jpeg\n.webp\n.svg\n.woff2";
  form.match.headers = [];
  form.match.cookies = [];
  form.match.queryParams = [];
  form.routeIds = [];
  form.backendIds = [];
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
  routeFilterText.value = "";
  backendFilterText.value = "";
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
  form.match.methods = [...(rule.match?.methods ?? [])];
  form.match.protocols = [...(rule.match?.protocols ?? [])];
  form.match.hostPatternsText = (rule.match?.hostPatterns ?? []).join("\n");
  form.match.pathPrefixesText = (rule.match?.pathPrefixes ?? []).join("\n");
  form.match.pathSuffixesText = (rule.match?.pathSuffixes ?? []).join("\n");
  form.match.headers = cloneMatchers(rule.match?.headers ?? []);
  form.match.cookies = cloneMatchers(rule.match?.cookies ?? []);
  form.match.queryParams = cloneMatchers(rule.match?.queryParams ?? []);
  form.routeIds = rule.routeIds.map((value) => value.toString());
  form.backendIds = rule.backendIds.map((value) => value.toString());
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
  routeFilterText.value = "";
  backendFilterText.value = "";
  isOpen.value = true;
  requestAnimationFrame(() => matchEditor.value?.setInitialTab());
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

function lines(value: string): string[] {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}

function cloneMatchers(matchers: readonly MatcherForm[]): MatcherForm[] {
  return matchers.map((matcher) => ({ name: matcher.name, operator: matcher.operator, value: matcher.value }));
}

function matcherPayload(matchers: MatcherForm[]) {
  return matchers
    .map((matcher) => ({
      name: matcher.name.trim(),
      operator: matcher.operator,
      value: matcher.value,
    }))
    .filter((matcher) => matcher.name);
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
  return routeDestinationLabel(route, backends.value, routeBackends.value);
}

function routeSearchText(route: PublicRoute): string {
  return `${routeLabel(route)} ${routeDetail(route)}`.toLowerCase();
}

function backendDetail(backend: PublicBackend): string {
  return backendSummary(backend) || "Proxy forward";
}

function backendSearchText(backend: PublicBackend): string {
  return `#${backend.id.toString()} ${backend.name} ${backendDetail(backend)}`.toLowerCase();
}

function isRouteSelected(id: bigint | string): boolean {
  return form.routeIds.includes(id.toString());
}

function isBackendSelected(id: bigint | string): boolean {
  return form.backendIds.includes(id.toString());
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

function toggleBackend(id: bigint | string) {
  toggleSelection(form.backendIds, id);
}

function selectVisibleRoutes() {
  const selected = new Set(form.routeIds);
  for (const route of filteredRoutes.value) {
    if (selected.size >= maxCacheListItems) break;
    selected.add(route.id.toString());
  }
  form.routeIds = [...selected];
}

function selectVisibleBackends() {
  const selected = new Set(form.backendIds);
  for (const backend of filteredProxyBackends.value) {
    if (selected.size >= maxCacheListItems) break;
    selected.add(backend.id.toString());
  }
  form.backendIds = [...selected];
}

function clearRoutes() {
  form.routeIds = [];
}

function clearBackends() {
  form.backendIds = [];
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
      match: {
        methods: [...form.match.methods],
        protocols: [...form.match.protocols],
        hostPatterns: lines(form.match.hostPatternsText),
        pathPrefixes: lines(form.match.pathPrefixesText),
        pathSuffixes: lines(form.match.pathSuffixesText),
        headers: matcherPayload(form.match.headers),
        cookies: matcherPayload(form.match.cookies),
        queryParams: matcherPayload(form.match.queryParams),
      },
      routeIds: normalizeIdStrings(form.routeIds).map((id) => BigInt(id)),
      backendIds: normalizeIdStrings(form.backendIds).map((id) => BigInt(id)),
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
  <Modal v-model="isOpen" :title="form.id ? 'Edit Cache Rule' : 'Add Cache Rule'" max-width="64rem">
    <form class="grid gap-5" @submit.prevent="submitRule">
      <section class="grid gap-4 sm:grid-cols-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
          Name
          <input v-model="form.name" class="vercel-input text-sm normal-case tracking-normal" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Priority
          <input v-model.number="form.priority" type="number" class="vercel-input text-sm normal-case tracking-normal" required />
        </label>
        <label class="flex items-center gap-2 self-end text-sm text-[#d4d4d8]">
          <input v-model="form.enabled" type="checkbox" />
          Enabled
        </label>
        <label class="sm:col-span-4 flex items-start gap-3 rounded-md border border-[#2f3f46] bg-[#071316] p-3 text-sm text-[#d4d4d8]">
          <input v-model="form.allowCookieRequests" type="checkbox" class="mt-0.5" />
          <span class="grid gap-1">
            <span class="font-medium text-white">Cache requests with Cookie headers</span>
            <span class="text-xs leading-5 text-[#8ba6ad]">
              Enable this only for public static asset rules. Cookie values are ignored and are never part of the cache key.
            </span>
          </span>
        </label>
      </section>

      <PublicPolicyMatchEditor ref="matchEditor" :form="form.match" />

      <section class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <h4 class="text-sm font-semibold text-white">Cache behavior</h4>
        <p class="text-xs leading-5 text-[#777]">
          Authorization requests are always bypassed. Cookie requests are cached only when this rule allows them. Responses with Set-Cookie, no-store, private, or no-cache are never cached.
        </p>
        <div class="grid gap-4 sm:grid-cols-4">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            TTL mode
            <select v-model="form.ttlMode" class="vercel-input text-sm normal-case tracking-normal">
              <option v-for="option in ttlModeOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Default TTL minutes
            <input v-model.number="form.ttlMinutes" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Scope
            <select v-model="form.scope" class="vercel-input text-sm normal-case tracking-normal">
              <option v-for="option in scopeOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Max object MiB
            <input v-model.number="form.maxObjectMiB" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
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
            <p class="mt-1 text-xs leading-5 text-[#777]">Empty route or backend filters match every available target.</p>
          </div>
          <div class="cache-summary-chips" aria-label="Cache key summary">
            <span class="cache-summary-chip">{{ targetSelectionSummary }}</span>
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
              <button type="button" class="panel-link-button" :disabled="!form.routeIds.length" @click="clearRoutes">
                Clear
              </button>
            </div>
            <div class="target-toolbar">
              <input v-model="routeFilterText" class="vercel-input target-search" placeholder="Filter routes" />
              <button type="button" class="panel-action-button" :disabled="!canSelectVisibleRoutes" @click="selectVisibleRoutes">
                Select visible
              </button>
            </div>
            <p v-if="routeSelectionValidationReason" class="field-error">{{ routeSelectionValidationReason }}</p>
            <div class="target-list" role="listbox" aria-label="Route filters">
              <label
                v-for="route in filteredRoutes"
                :key="route.id.toString()"
                class="target-row"
                :class="{ 'target-row-selected': isRouteSelected(route.id) }"
              >
                <input
                  type="checkbox"
                  :checked="isRouteSelected(route.id)"
                  :disabled="!isRouteSelected(route.id) && form.routeIds.length >= maxCacheListItems"
                  :aria-label="`Filter cache rule to route ${routeLabel(route)}`"
                  @change="toggleRoute(route.id)"
                />
                <span class="target-row-body">
                  <span class="target-row-title">{{ routeLabel(route) }}</span>
                  <span class="target-row-detail">{{ routeDetail(route) }}</span>
                </span>
              </label>
              <div v-if="!filteredRoutes.length" class="target-empty">
                {{ routes.length ? "No routes match the filter." : "No routes configured." }}
              </div>
            </div>
          </div>

          <div class="target-panel">
            <div class="target-panel-head">
              <div class="min-w-0">
                <p class="panel-eyebrow">Backends</p>
                <h5 class="panel-heading">{{ backendSelectionSummary }}</h5>
              </div>
              <button type="button" class="panel-link-button" :disabled="!form.backendIds.length" @click="clearBackends">
                Clear
              </button>
            </div>
            <div class="target-toolbar">
              <input v-model="backendFilterText" class="vercel-input target-search" placeholder="Filter backends" />
              <button type="button" class="panel-action-button" :disabled="!canSelectVisibleBackends" @click="selectVisibleBackends">
                Select visible
              </button>
            </div>
            <p v-if="backendSelectionValidationReason" class="field-error">{{ backendSelectionValidationReason }}</p>
            <div class="target-list" role="listbox" aria-label="Backend filters">
              <label
                v-for="backend in filteredProxyBackends"
                :key="backend.id.toString()"
                class="target-row"
                :class="{ 'target-row-selected': isBackendSelected(backend.id) }"
              >
                <input
                  type="checkbox"
                  :checked="isBackendSelected(backend.id)"
                  :disabled="!isBackendSelected(backend.id) && form.backendIds.length >= maxCacheListItems"
                  :aria-label="`Filter cache rule to backend ${backend.name}`"
                  @change="toggleBackend(backend.id)"
                />
                <span class="target-row-body">
                  <span class="target-row-title">{{ backend.name }}</span>
                  <span class="target-row-detail">{{ backendDetail(backend) }}</span>
                </span>
              </label>
              <div v-if="!filteredProxyBackends.length" class="target-empty">
                {{ proxyBackends.length ? "No backends match the filter." : "No proxy backends configured." }}
              </div>
            </div>
          </div>
        </div>

        <div class="query-mode-panel">
          <div class="query-mode-head">
            <p class="panel-eyebrow">Query mode</p>
            <h5 class="panel-heading">{{ queryModeSummary }}</h5>
          </div>
          <div class="query-mode-grid" role="radiogroup" aria-label="Query cache key mode">
            <button
              v-for="option in queryModeOptions"
              :key="option.value"
              type="button"
              class="query-mode-button"
              :class="{ 'query-mode-button-active': form.queryMode === option.value }"
              :aria-checked="form.queryMode === option.value"
              role="radio"
              @click="form.queryMode = option.value"
            >
              <span>{{ option.label }}</span>
              <small>{{ queryModeDescription(option.value) }}</small>
            </button>
          </div>
        </div>

        <div v-if="queryParamsEditorVisible" class="value-editor">
          <div class="value-editor-head">
            <div class="min-w-0">
              <p class="panel-eyebrow">Query params</p>
              <h5 class="panel-heading">{{ normalizedQueryParams.length.toString() }} active</h5>
            </div>
            <button type="button" class="add-row-button" :disabled="form.queryParams.length >= maxCacheListItems" @click="addQueryParam">
              <PlusIcon class="h-3.5 w-3.5" />
              <span>Add param</span>
            </button>
          </div>
          <p v-if="queryParamsValidationReason" class="field-error">{{ queryParamsValidationReason }}</p>
          <div v-if="!form.queryParams.length" class="value-empty">
            <p>No query parameters listed.</p>
            <button type="button" @click="addQueryParam">
              <PlusIcon class="h-3.5 w-3.5" />
              <span>Add param</span>
            </button>
          </div>
          <div v-else class="value-list">
            <div v-for="(param, index) in form.queryParams" :key="`query-${index}`" class="value-row">
              <div class="value-field">
                <input v-model="form.queryParams[index]" class="vercel-input value-input" placeholder="version" />
                <p v-if="queryParamError(param)" class="field-error">{{ queryParamError(param) }}</p>
              </div>
              <button type="button" class="remove-row-button" aria-label="Remove query parameter" title="Remove query parameter" @click="removeQueryParam(index)">
                <TrashIcon class="h-3.5 w-3.5" />
              </button>
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
              <button type="button" class="add-row-button" :disabled="form.varyHeaders.length >= maxCacheListItems" @click="addVaryHeader">
                <PlusIcon class="h-3.5 w-3.5" />
                <span>Add header</span>
              </button>
            </div>
            <p v-if="varyHeadersValidationReason" class="field-error">{{ varyHeadersValidationReason }}</p>
            <div v-if="!form.varyHeaders.length" class="value-empty">
              <p>No vary headers configured.</p>
              <button type="button" @click="addVaryHeader">
                <PlusIcon class="h-3.5 w-3.5" />
                <span>Add header</span>
              </button>
            </div>
            <div v-else class="value-list">
              <div v-for="(header, index) in form.varyHeaders" :key="`vary-${index}`" class="value-row">
                <div class="value-field">
                  <input v-model="form.varyHeaders[index]" class="vercel-input value-input" placeholder="Accept-Encoding" />
                  <p v-if="varyHeaderError(header)" class="field-error">{{ varyHeaderError(header) }}</p>
                </div>
                <button type="button" class="remove-row-button" aria-label="Remove vary header" title="Remove vary header" @click="removeVaryHeader(index)">
                  <TrashIcon class="h-3.5 w-3.5" />
                </button>
              </div>
            </div>
          </div>

          <div class="value-editor">
            <div class="value-editor-head">
              <div class="min-w-0">
                <p class="panel-eyebrow">Cache status codes</p>
                <h5 class="panel-heading">{{ normalizedCacheStatusCodes.length.toString() }} active</h5>
              </div>
              <button type="button" class="add-row-button" :disabled="form.cacheStatusCodes.length >= maxCacheListItems" @click="addCacheStatusCode">
                <PlusIcon class="h-3.5 w-3.5" />
                <span>Add code</span>
              </button>
            </div>
            <p v-if="cacheStatusCodesValidationReason" class="field-error">{{ cacheStatusCodesValidationReason }}</p>
            <div v-if="!form.cacheStatusCodes.length" class="value-empty">
              <p>No status codes configured.</p>
              <button type="button" @click="addCacheStatusCode">
                <PlusIcon class="h-3.5 w-3.5" />
                <span>Add code</span>
              </button>
            </div>
            <div v-else class="value-list">
              <div v-for="(status, index) in form.cacheStatusCodes" :key="`status-${index}`" class="value-row">
                <div class="value-field">
                  <input v-model="form.cacheStatusCodes[index]" inputmode="numeric" class="vercel-input value-input" placeholder="200" />
                  <p v-if="statusCodeError(status)" class="field-error">{{ statusCodeError(status) }}</p>
                </div>
                <button type="button" class="remove-row-button" aria-label="Remove status code" title="Remove status code" @click="removeCacheStatusCode(index)">
                  <TrashIcon class="h-3.5 w-3.5" />
                </button>
              </div>
            </div>
          </div>
        </div>

        <label class="cache-status-toggle">
          <span class="min-w-0">
            <span class="toggle-title">Expose cache status</span>
            <span class="toggle-detail">X-p2pstream-Cache response header</span>
          </span>
          <input v-model="form.addCacheStatusHeader" type="checkbox" />
        </label>
      </section>

      <div class="flex justify-end gap-3 border-t border-[#222] pt-4">
        <Button type="button" severity="secondary" label="Cancel" @click="close" />
        <Button type="submit" :label="form.id ? 'Save Rule' : 'Create Rule'" :disabled="submitDisabled" :title="submitDisabledReason" />
      </div>
    </form>
  </Modal>
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
