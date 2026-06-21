<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import { NButton, NCheckbox, NDynamicTags, NInput, NInputNumber, NModal, NRadioButton, NRadioGroup, NSelect, NTransfer } from "naive-ui";
import type { TransferOption } from "naive-ui";
import PublicPolicyMatchEditor from "@/components/editors/PublicPolicyMatchEditor.vue";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
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

type CacheTransferOption = TransferOption & {
  searchText: string;
};
type DynamicTagValue = string | { label: string; value?: string };

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));

const isOpen = ref(false);
const rules = computed(() => props.config?.cacheRules ?? []);
const routes = computed(() => props.config?.routes ?? []);
const routeTargets = computed(() => props.config?.routeTargets ?? []);
const proxyTargets = computed(() => routeTargets.value.filter((target) => target.targetType !== 2));

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
const routeTransferOptions = computed<CacheTransferOption[]>(() =>
  routes.value.map((route) => {
    const value = route.id.toString();
    return {
      label: `${routeLabel(route)} - ${routeDetail(route)}`,
      value,
      searchText: routeSearchText(route),
      disabled: !form.routeIds.includes(value) && form.routeIds.length >= maxCacheListItems,
    };
  }),
);
const targetTransferOptions = computed<CacheTransferOption[]>(() =>
  proxyTargets.value.map((target) => {
    const value = target.id.toString();
    return {
      label: `#${target.id.toString()} ${routeTargetName(target)} - ${targetDetail(target)}`,
      value,
      searchText: targetSearchText(target),
      disabled: !form.targetIds.includes(value) && form.targetIds.length >= maxCacheListItems,
    };
  }),
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

function transferFilter(pattern: string, option: TransferOption): boolean {
  const candidate = option as CacheTransferOption;
  const needle = pattern.trim().toLowerCase();
  if (!needle) return true;
  return candidate.searchText.includes(needle) || String(candidate.label).toLowerCase().includes(needle);
}

function updateRouteIds(value: Array<string | number>) {
  form.routeIds = value.map(String);
}

function updateTargetIds(value: Array<string | number>) {
  form.targetIds = value.map(String);
}

function dynamicTagToString(tag: DynamicTagValue): string {
  return typeof tag === "string" ? tag : tag.value ?? tag.label;
}

function createTrimmedTag(label: string): string {
  return label.trim();
}

function updateQueryParams(value: DynamicTagValue[]) {
  form.queryParams = value.map(dynamicTagToString);
}

function updateVaryHeaders(value: DynamicTagValue[]) {
  form.varyHeaders = value.map(dynamicTagToString);
}

function updateCacheStatusCodes(value: DynamicTagValue[]) {
  form.cacheStatusCodes = value.map(dynamicTagToString);
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
    <form class="layout-grid max-modal-height space-xl scroll-y pad-right-xs" @submit.prevent="submitRule">
      <section class="layout-grid space-lg mq-sm-cols-four">
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text mq-sm-span-two">
          Name
          <NInput v-model:value="form.name" size="small" required />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Priority
          <NInputNumber v-model:value="form.priority" size="small" required />
        </label>
        <NCheckbox v-model:checked="form.enabled" class="self-align-end">
          Enabled
        </NCheckbox>
      </section>

      <PublicPolicyMatchEditor :form="form.match" />

      <section class="layout-grid space-lg round-md framed frame-standard muted-bg pad-lg">
        <h4 class="copy-sm weight-semibold base-text">Cache behavior</h4>
        <p class="copy-xs line-normal muted-text">
          Authorization requests are always bypassed. Cookie requests are cached only when this rule allows them. Responses with Set-Cookie, no-store, private, or no-cache are never cached.
        </p>
        <NCheckbox v-model:checked="form.allowCookieRequests" class="round-md framed frame-standard muted-bg pad-md">
          <span class="layout-grid space-2xs">
            <span class="weight-medium base-text">Cache requests with Cookie headers</span>
            <span class="copy-xs line-normal muted-text">
              Enable this only for public static asset rules. Cookie values are ignored and are never part of the cache key.
            </span>
          </span>
        </NCheckbox>
        <NCheckbox
          v-if="form.allowCookieRequests"
          v-model:checked="form.allowCookieRequestsAcknowledged"
          class="round-md framed frame-standard panel-bg pad-md"
        >
          <span class="layout-grid space-2xs">
            <span class="weight-medium base-text">I understand Cookie is ignored in this cache key</span>
            <span class="copy-xs line-normal muted-text">
              Only use this for responses that are identical for every visitor, even when the request includes cookies.
            </span>
          </span>
        </NCheckbox>
        <div class="layout-grid space-lg mq-sm-cols-four">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            TTL mode
            <NSelect v-model:value="form.ttlMode" size="small" :options="ttlModeOptions" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Default TTL minutes
            <NInputNumber v-model:value="form.ttlMinutes" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Scope
            <NSelect v-model:value="form.scope" size="small" :options="scopeOptions" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Max object MiB
            <NInputNumber v-model:value="form.maxObjectMiB" size="small" :min="1" />
          </label>
        </div>
        <p class="copy-xs line-normal muted-text">
          Responses with Set-Cookie, private/no-store/no-cache, Vary: Cookie, or Vary: Authorization are never stored, even when cookie requests are enabled above.
        </p>
      </section>

      <section class="cache-filter-section">
        <div class="cache-filter-header">
          <div>
            <h4 class="copy-sm weight-semibold base-text">Keys and filters</h4>
            <p class="margin-top-xs copy-xs line-normal muted-text">Empty route or target filters match every available target.</p>
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
              <div class="min-width-zero">
                <p class="panel-eyebrow">Routes</p>
                <h5 class="panel-heading">{{ routeSelectionSummary }}</h5>
              </div>
            </div>
            <p v-if="routeSelectionValidationReason" class="field-error">{{ routeSelectionValidationReason }}</p>
            <NTransfer
              :value="form.routeIds"
              :options="routeTransferOptions"
              source-title="Available routes"
              target-title="Filtered routes"
              select-all-text="Select available"
              clear-text="Clear selected"
              source-filterable
              target-filterable
              virtual-scroll
              size="small"
              :filter="transferFilter"
              @update:value="updateRouteIds"
            />
          </div>

          <div class="target-panel">
            <div class="target-panel-head">
              <div class="min-width-zero">
                <p class="panel-eyebrow">Targets</p>
                <h5 class="panel-heading">{{ targetSelectionSummary }}</h5>
              </div>
            </div>
            <p v-if="targetSelectionValidationReason" class="field-error">{{ targetSelectionValidationReason }}</p>
            <NTransfer
              :value="form.targetIds"
              :options="targetTransferOptions"
              source-title="Available targets"
              target-title="Filtered targets"
              select-all-text="Select available"
              clear-text="Clear selected"
              source-filterable
              target-filterable
              virtual-scroll
              size="small"
              :filter="transferFilter"
              @update:value="updateTargetIds"
            />
          </div>
        </div>

        <div class="query-mode-panel">
          <div class="query-mode-head">
            <p class="panel-eyebrow">Query mode</p>
            <h5 class="panel-heading">{{ queryModeSummary }}</h5>
          </div>
          <NRadioGroup v-model:value="form.queryMode" class="fill-width" name="cache-query-mode" size="small">
            <NRadioButton v-for="option in queryModeOptions" :key="option.value" :value="option.value" :label="option.label" />
          </NRadioGroup>
        </div>

        <div v-if="queryParamsEditorVisible" class="value-editor">
          <div class="value-editor-head">
            <div class="min-width-zero">
              <p class="panel-eyebrow">Query params</p>
              <h5 class="panel-heading">{{ normalizedQueryParams.length.toString() }} active</h5>
            </div>
          </div>
          <p v-if="queryParamsValidationReason" class="field-error">{{ queryParamsValidationReason }}</p>
          <NDynamicTags
            :value="form.queryParams"
            size="small"
            :max="maxCacheListItems"
            :on-create="createTrimmedTag"
            :input-props="{ placeholder: 'version' }"
            @update:value="updateQueryParams"
          />
        </div>

        <div class="value-editor-grid">
          <div class="value-editor">
            <div class="value-editor-head">
              <div class="min-width-zero">
                <p class="panel-eyebrow">Vary headers</p>
                <h5 class="panel-heading">{{ normalizedVaryHeaders.length.toString() }} active</h5>
              </div>
            </div>
            <p v-if="varyHeadersValidationReason" class="field-error">{{ varyHeadersValidationReason }}</p>
            <NDynamicTags
              :value="form.varyHeaders"
              size="small"
              :max="maxCacheListItems"
              :on-create="createTrimmedTag"
              :input-props="{ placeholder: 'Accept-Encoding' }"
              @update:value="updateVaryHeaders"
            />
          </div>

          <div class="value-editor">
            <div class="value-editor-head">
              <div class="min-width-zero">
                <p class="panel-eyebrow">Cache status codes</p>
                <h5 class="panel-heading">{{ normalizedCacheStatusCodes.length.toString() }} active</h5>
              </div>
            </div>
            <p v-if="cacheStatusCodesValidationReason" class="field-error">{{ cacheStatusCodesValidationReason }}</p>
            <NDynamicTags
              :value="form.cacheStatusCodes"
              size="small"
              :max="maxCacheListItems"
              :on-create="createTrimmedTag"
              :input-props="{ placeholder: '200' }"
              @update:value="updateCacheStatusCodes"
            />
          </div>
        </div>

        <NCheckbox v-model:checked="form.addCacheStatusHeader" class="cache-status-toggle">
          <span class="min-width-zero">
            <span class="toggle-title">Expose cache status</span>
            <span class="toggle-detail">X-p2pstream-Cache response header</span>
          </span>
        </NCheckbox>
      </section>

      <div class="layout-row align-end-row space-md divider-top frame-standard pad-top-lg">
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
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
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
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-panel-muted);
  color: var(--app-text);
  padding: 0.25rem 0.55rem;
  font-size: 0.68rem;
  font-weight: 650;
  line-height: 1.1;
  white-space: nowrap;
}

.target-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 1rem;
}

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
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
  padding: 0.85rem;
}

.panel-eyebrow {
  color: var(--app-text-muted);
  font-size: 0.68rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  line-height: 1.1;
  text-transform: uppercase;
}

.panel-heading {
  margin-top: 0.18rem;
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.92rem;
  font-weight: 650;
  line-height: 1.2;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.query-mode-head {
  min-width: 0;
}

.field-error {
  color: var(--app-error);
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
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
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
  color: var(--app-text);
  font-size: 0.86rem;
  font-weight: 650;
}

.toggle-detail {
  margin-top: 0.15rem;
  color: var(--app-text-muted);
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

  .value-editor-grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 560px) {
  .cache-status-toggle {
    align-items: flex-start;
  }
}
</style>
