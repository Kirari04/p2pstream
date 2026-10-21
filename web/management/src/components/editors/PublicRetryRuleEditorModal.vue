<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import { TriangleAlert as TriangleAlertIcon } from "@lucide/vue";
import {
  NAlert,
  NButton,
  NCheckbox,
  NDrawer,
  NDrawerContent,
  NDynamicTags,
  NInput,
  NInputNumber,
  NTransfer,
  NTag,
} from "naive-ui";
import type { TransferOption } from "naive-ui";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicPolicyMatchEditor from "@/components/editors/PublicPolicyMatchEditor.vue";
import SegmentedControl from "@/components/ui/SegmentedControl.vue";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { editorDrawerWidth } from "@/lib/naiveUi";
import {
  defaultPolicyMatchForm,
  policyMatchFormFromProto,
  policyMatchRulePayload,
  policyMatchValidationReason,
  type PolicyMatchForm,
} from "@/lib/publicPolicyMatch";
import {
  routeDestinationLabel,
  routeTargetName,
  routeTargetTypeLabel,
} from "@/lib/publicProxyLabels";
import { unavailableSelectionIds } from "@/lib/scopeSelections";
import {
  PublicRetryBodyMode,
  PublicRetryFailureMode,
  PublicRetryResponseBodyMode,
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  type GetPublicProxyConfigResponse,
  type PublicRoute,
} from "@/gen/proto/p2pstream/v1/management_pb";

type MethodPreset = "safe" | "idempotent" | "all" | "custom";
type StatusPreset = "none" | "gateway" | "transient" | "custom";
type RetryTransferOption = TransferOption & { searchText: string };
type DynamicTagValue = string | { label: string; value?: string };

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const managementClient = useManagementClient();
const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));
const isOpen = ref(false);

const rules = computed(() => props.config?.retryRules ?? []);
const routes = computed(() => props.config?.routes ?? []);
const agentTargets = computed(() => (props.config?.routeTargets ?? []).filter((target) =>
  target.targetType === PublicRouteTargetType.PROXY && target.transport === PublicRouteTargetTransport.AGENT,
));

const maxFilterItems = 64;
const safeMethods = ["GET", "HEAD"];
const idempotentMethods = ["GET", "HEAD", "OPTIONS", "PUT", "DELETE"];
const gatewayStatusCodes = [502, 503, 504];
const transientStatusCodes = [408, 425, 429, 500, 502, 503, 504];

const form = reactive({
  id: "",
  name: "",
  priority: 100,
  enabled: true,
  methodPreset: "safe" as MethodPreset,
  customMethods: [...safeMethods],
  maxRetries: 1,
  failureMode: PublicRetryFailureMode.CONNECTION_FAILURES,
  statusPreset: "none" as StatusPreset,
  customStatusCodes: gatewayStatusCodes.map(String),
  bodyMode: PublicRetryBodyMode.NEVER,
  maxReplayBodyKiB: 256,
  responseBodyMode: PublicRetryResponseBodyMode.STREAM,
  maxBufferedResponseBodyKiB: 8192,
  maxBufferedResponseWaitSeconds: 30,
  routeIds: [] as string[],
  targetIds: [] as string[],
  match: defaultPolicyMatchForm() as PolicyMatchForm,
  duplicateRiskAcknowledged: false,
});

const methodPresetOptions: readonly { label: string; value: MethodPreset }[] = [
  { label: "GET + HEAD", value: "safe" },
  { label: "Idempotent", value: "idempotent" },
  { label: "All methods", value: "all" },
  { label: "Custom", value: "custom" },
];
const statusPresetOptions: readonly { label: string; value: StatusPreset }[] = [
  { label: "Transport only", value: "none" },
  { label: "Gateway failures", value: "gateway" },
  { label: "Broad transient", value: "transient" },
  { label: "Custom", value: "custom" },
];
const bodyModeOptions = [
  { label: "Do not buffer", value: PublicRetryBodyMode.NEVER },
  { label: "Buffer body", value: PublicRetryBodyMode.BUFFERED },
];

const failureModeOptions = [
  {
    label: "Connection establishment only",
    value: PublicRetryFailureMode.CONNECTION_FAILURES,
    detail: "Retry only before the upstream request is known to have started.",
  },
  {
    label: "Any failure before response headers",
    value: PublicRetryFailureMode.PRE_RESPONSE_FAILURES,
    detail: "Covers more VPN failures, but the upstream may already have processed the request.",
  },
];

const normalizedMethods = computed(() => {
  if (form.methodPreset === "safe") return safeMethods;
  if (form.methodPreset === "idempotent") return idempotentMethods;
  if (form.methodPreset === "all") return ["*"];
  return normalizeMethods(form.customMethods);
});
const normalizedStatusCodes = computed(() => {
  if (form.statusPreset === "none") return [];
  if (form.statusPreset === "gateway") return gatewayStatusCodes;
  if (form.statusPreset === "transient") return transientStatusCodes;
  return normalizeStatusCodes(form.customStatusCodes);
});
const requiresRiskAcknowledgement = computed(() =>
  form.failureMode === PublicRetryFailureMode.PRE_RESPONSE_FAILURES ||
  normalizedStatusCodes.value.length > 0 ||
  form.responseBodyMode === PublicRetryResponseBodyMode.BUFFERED ||
  normalizedMethods.value.some((method) => !["GET", "HEAD", "OPTIONS"].includes(method)),
);
const methodSummary = computed(() => normalizedMethods.value[0] === "*"
  ? "All supported methods"
  : normalizedMethods.value.join(", "));
const routeSummary = computed(() => form.routeIds.length ? `${form.routeIds.length.toString()} selected` : "All routes");
const targetSummary = computed(() => form.targetIds.length ? `${form.targetIds.length.toString()} selected` : "All agent targets");
const attemptSummary = computed(() => `${(form.maxRetries + 1).toString()} total attempts`);
const statusSummary = computed(() => normalizedStatusCodes.value.length
  ? normalizedStatusCodes.value.join(", ")
  : "Transport failures only");
const responseDeliverySummary = computed(() => form.responseBodyMode === PublicRetryResponseBodyMode.BUFFERED
  ? `Best effort through ${form.maxBufferedResponseBodyKiB.toLocaleString()} KiB / ${form.maxBufferedResponseWaitSeconds.toLocaleString()}s`
  : "Immediate streaming");

const unavailableRouteIds = computed(() => unavailableSelectionIds(form.routeIds, routes.value.map((route) => route.id)));
const unavailableTargetIds = computed(() => unavailableSelectionIds(form.targetIds, agentTargets.value.map((target) => target.id)));
const routeTransferOptions = computed<RetryTransferOption[]>(() => [
  ...routes.value.map((route) => {
    const value = route.id.toString();
    return {
      label: `${routeLabel(route)} — ${routeDestinationLabel(route)}`,
      value,
      searchText: `${routeLabel(route)} ${routeDestinationLabel(route)}`.toLowerCase(),
      disabled: !form.routeIds.includes(value) && form.routeIds.length >= maxFilterItems,
    };
  }),
  ...unavailableRouteIds.value.map((value) => ({
    label: `#${value} unavailable route — remove before saving`,
    value,
    searchText: `#${value} unavailable missing route`,
    disabled: false,
  })),
]);
const targetTransferOptions = computed<RetryTransferOption[]>(() => [
  ...agentTargets.value.map((target) => {
    const value = target.id.toString();
    const detail = `${routeTargetTypeLabel(target.targetType)} / ${target.url || "no upstream URL"}`;
    return {
      label: `#${value} ${routeTargetName(target)} — ${detail}`,
      value,
      searchText: `#${value} ${routeTargetName(target)} ${detail}`.toLowerCase(),
      disabled: !form.targetIds.includes(value) && form.targetIds.length >= maxFilterItems,
    };
  }),
  ...unavailableTargetIds.value.map((value) => ({
    label: `#${value} unavailable target — remove before saving`,
    value,
    searchText: `#${value} unavailable missing target`,
    disabled: false,
  })),
]);

const submitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!/^[A-Za-z0-9._-]{1,64}$/.test(form.name.trim())) return "Use 1–64 letters, numbers, dots, underscores, or hyphens for the name.";
  if (!Number.isInteger(form.priority)) return "Priority must be a whole number.";
  if (!Number.isInteger(form.maxRetries) || form.maxRetries < 1 || form.maxRetries > 3) return "Retries must be between 1 and 3.";
  if (form.routeIds.length > maxFilterItems) return `Select at most ${maxFilterItems.toString()} routes.`;
  if (form.targetIds.length > maxFilterItems) return `Select at most ${maxFilterItems.toString()} targets.`;
  if (unavailableRouteIds.value.length) return `Remove unavailable route #${unavailableRouteIds.value[0]} from the saved scope.`;
  if (unavailableTargetIds.value.length) return `Remove unavailable target #${unavailableTargetIds.value[0]} from the saved scope.`;
  const methodError = methodsValidationReason(normalizedMethods.value);
  if (methodError) return methodError;
  const statusError = statusCodesValidationReason();
  if (statusError) return statusError;
  if (form.bodyMode === PublicRetryBodyMode.BUFFERED && (!Number.isInteger(form.maxReplayBodyKiB) || form.maxReplayBodyKiB < 1 || form.maxReplayBodyKiB > 4096)) {
    return "Replay body limit must be between 1 KiB and 4,096 KiB.";
  }
  if (form.responseBodyMode === PublicRetryResponseBodyMode.BUFFERED && (!Number.isInteger(form.maxBufferedResponseBodyKiB) || form.maxBufferedResponseBodyKiB < 1 || form.maxBufferedResponseBodyKiB > 65536)) {
    return "Protected response limit must be between 1 KiB and 65,536 KiB.";
  }
  if (form.responseBodyMode === PublicRetryResponseBodyMode.BUFFERED && (!Number.isInteger(form.maxBufferedResponseWaitSeconds) || form.maxBufferedResponseWaitSeconds < 1 || form.maxBufferedResponseWaitSeconds > 300)) {
    return "Protected response wait must be between 1 and 300 seconds.";
  }
  const matchError = policyMatchValidationReason(form.match);
  if (matchError) return matchError;
  if (requiresRiskAcknowledgement.value && !form.duplicateRiskAcknowledged) return "Acknowledge the duplicate-request risk.";
  return "";
});
const submitDisabled = computed(() => Boolean(submitDisabledReason.value));

function resetForm() {
  form.id = "";
  form.name = nextRuleName();
  form.priority = 100;
  form.enabled = true;
  form.methodPreset = "safe";
  form.customMethods = [...safeMethods];
  form.maxRetries = 1;
  form.failureMode = PublicRetryFailureMode.CONNECTION_FAILURES;
  form.statusPreset = "none";
  form.customStatusCodes = gatewayStatusCodes.map(String);
  form.bodyMode = PublicRetryBodyMode.NEVER;
  form.maxReplayBodyKiB = 256;
  form.responseBodyMode = PublicRetryResponseBodyMode.STREAM;
  form.maxBufferedResponseBodyKiB = 8192;
  form.maxBufferedResponseWaitSeconds = 30;
  form.routeIds = [];
  form.targetIds = [];
  form.match = defaultPolicyMatchForm();
  form.duplicateRiskAcknowledged = false;
}

function openCreate() {
  resetForm();
  isOpen.value = true;
}

function openEdit(ruleId: bigint | string) {
  const rule = rules.value.find((item) => item.id.toString() === ruleId.toString());
  if (!rule) return;
  form.id = rule.id.toString();
  form.name = rule.name;
  form.priority = Number(rule.priority);
  form.enabled = rule.enabled;
  form.methodPreset = presetForMethods(rule.methods);
  form.customMethods = rule.methods[0] === "*" ? [...safeMethods] : [...rule.methods];
  form.maxRetries = Number(rule.maxRetries || 1n);
  form.failureMode = rule.failureMode || PublicRetryFailureMode.CONNECTION_FAILURES;
  const retryStatusCodes = rule.retryStatusCodes.map(Number);
  form.statusPreset = presetForStatusCodes(retryStatusCodes);
  form.customStatusCodes = retryStatusCodes.length ? retryStatusCodes.map(String) : gatewayStatusCodes.map(String);
  form.bodyMode = rule.bodyMode || PublicRetryBodyMode.NEVER;
  form.maxReplayBodyKiB = Math.max(1, Math.round(Number(rule.maxReplayBodyBytes || 262144n) / 1024));
  form.responseBodyMode = rule.responseBodyMode || PublicRetryResponseBodyMode.STREAM;
  form.maxBufferedResponseBodyKiB = Math.max(1, Math.round(Number(rule.maxBufferedResponseBodyBytes || 8388608n) / 1024));
  form.maxBufferedResponseWaitSeconds = Math.max(1, Math.round(Number(rule.maxBufferedResponseWaitMillis || 30000n) / 1000));
  form.routeIds = rule.routeIds.map(String);
  form.targetIds = rule.targetIds.map(String);
  form.match = policyMatchFormFromProto(rule.matchRule);
  form.duplicateRiskAcknowledged = requiresRiskFor(rule.methods, form.failureMode, retryStatusCodes, form.responseBodyMode);
  isOpen.value = true;
}

function close() {
  isOpen.value = false;
}

function nextRuleName(): string {
  const used = new Set(rules.value.map((rule) => rule.name));
  if (!used.has("vpn-failover")) return "vpn-failover";
  let suffix = 2;
  while (used.has(`vpn-failover-${suffix.toString()}`)) suffix += 1;
  return `vpn-failover-${suffix.toString()}`;
}

function normalizeMethods(methods: readonly string[]): string[] {
  return [...new Set(methods.map((method) => method.trim().toUpperCase()).filter(Boolean))].sort();
}

function presetForMethods(methods: readonly string[]): MethodPreset {
  const normalized = normalizeMethods(methods);
  if (normalized.length === 1 && normalized[0] === "*") return "all";
  if (sameMethods(normalized, safeMethods)) return "safe";
  if (sameMethods(normalized, idempotentMethods)) return "idempotent";
  return "custom";
}

function sameMethods(left: readonly string[], right: readonly string[]): boolean {
  const leftSet = new Set(left);
  return leftSet.size === right.length && right.every((method) => leftSet.has(method));
}

function requiresRiskFor(methods: readonly string[], failureMode: PublicRetryFailureMode, retryStatusCodes: readonly number[], responseBodyMode: PublicRetryResponseBodyMode): boolean {
  return failureMode === PublicRetryFailureMode.PRE_RESPONSE_FAILURES ||
    retryStatusCodes.length > 0 ||
    responseBodyMode === PublicRetryResponseBodyMode.BUFFERED ||
    methods.some((method) => !["GET", "HEAD", "OPTIONS"].includes(method));
}

function normalizeStatusCodes(values: readonly string[]): number[] {
  return [...new Set(values.map((value) => Number(value.trim())).filter(Number.isInteger))].sort((left, right) => left - right);
}

function presetForStatusCodes(statuses: readonly number[]): StatusPreset {
  if (!statuses.length) return "none";
  if (sameNumbers(statuses, gatewayStatusCodes)) return "gateway";
  if (sameNumbers(statuses, transientStatusCodes)) return "transient";
  return "custom";
}

function sameNumbers(left: readonly number[], right: readonly number[]): boolean {
  const leftSet = new Set(left);
  return leftSet.size === right.length && right.every((value) => leftSet.has(value));
}

function statusCodesValidationReason(): string {
  if (form.statusPreset !== "custom") return "";
  if (!form.customStatusCodes.length) return "Add at least one HTTP status code or choose Transport only.";
  for (const raw of form.customStatusCodes) {
    const value = Number(raw.trim());
    if (!Number.isInteger(value) || value < 400 || value > 599) {
      return `Status ${raw || "(empty)"} must be a whole number between 400 and 599.`;
    }
  }
  if (normalizeStatusCodes(form.customStatusCodes).length > 64) return "Add at most 64 unique HTTP status codes.";
  return "";
}

function methodsValidationReason(methods: readonly string[]): string {
  if (!methods.length) return "Add at least one request method.";
  if (methods.length > 32) return "Add at most 32 request methods.";
  if (methods.includes("*") && methods.length > 1) return "The all-methods wildcard must be used by itself.";
  for (const method of methods) {
    if (!/^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(method)) return `Method ${method || "(empty)"} is not a valid HTTP token.`;
    if (method === "CONNECT" || method === "TRACE") return `${method} requests cannot be retried.`;
  }
  return "";
}

function routeLabel(route: PublicRoute): string {
  return `#${route.id.toString()} ${route.hostPattern || "*"}${route.pathPrefix || "/"}`;
}

function transferFilter(pattern: string, option: TransferOption): boolean {
  const candidate = option as RetryTransferOption;
  const needle = pattern.trim().toLowerCase();
  return !needle || candidate.searchText.includes(needle) || String(candidate.label).toLowerCase().includes(needle);
}

function updateRouteIds(value: Array<string | number>) {
  form.routeIds = value.map(String);
}

function updateTargetIds(value: Array<string | number>) {
  form.targetIds = value.map(String);
}

function updateCustomMethods(value: DynamicTagValue[]) {
  form.customMethods = value.map((tag) => typeof tag === "string" ? tag : tag.value ?? tag.label);
}

function updateCustomStatusCodes(value: DynamicTagValue[]) {
  form.customStatusCodes = value.map((tag) => typeof tag === "string" ? tag : tag.value ?? tag.label);
}

async function run(action: () => Promise<void>): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action);
}

async function submitRule() {
  const ok = await run(async () => {
    const payload = {
      name: form.name.trim(),
      priority: BigInt(form.priority),
      enabled: form.enabled,
      methods: normalizedMethods.value,
      maxRetries: BigInt(form.maxRetries),
      failureMode: form.failureMode,
      retryStatusCodes: normalizedStatusCodes.value.map(BigInt),
      bodyMode: form.bodyMode,
      maxReplayBodyBytes: form.bodyMode === PublicRetryBodyMode.BUFFERED
        ? BigInt(form.maxReplayBodyKiB * 1024)
        : 0n,
      responseBodyMode: form.responseBodyMode,
      maxBufferedResponseBodyBytes: form.responseBodyMode === PublicRetryResponseBodyMode.BUFFERED
        ? BigInt(form.maxBufferedResponseBodyKiB * 1024)
        : 0n,
      maxBufferedResponseWaitMillis: form.responseBodyMode === PublicRetryResponseBodyMode.BUFFERED
        ? BigInt(form.maxBufferedResponseWaitSeconds * 1000)
        : 0n,
      routeIds: form.routeIds.map(BigInt),
      targetIds: form.targetIds.map(BigInt),
      matchRule: policyMatchRulePayload(form.match),
      duplicateRiskAcknowledged: requiresRiskAcknowledgement.value && form.duplicateRiskAcknowledged,
    };
    if (form.id) {
      await managementClient.updatePublicRetryRule({ id: BigInt(form.id), ...payload });
    } else {
      await managementClient.createPublicRetryRule(payload);
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
  <NDrawer
    v-model:show="isOpen"
    placement="right"
    :width="editorDrawerWidth('64rem')"
    :aria-label="form.id ? 'Edit Request Retry Rule' : 'Add Request Retry Rule'"
    class="editor-drawer"
  >
    <NDrawerContent :title="form.id ? 'Edit Request Retry Rule' : 'Add Request Retry Rule'" closable>
      <form class="editor-drawer-form layout-grid space-xl" @submit.prevent="submitRule">
        <section class="layout-grid space-lg mq-sm-cols-four">
          <label class="layout-grid space-xs copy-xs weight-medium muted-text mq-sm-span-two">
            Name
            <NInput v-model:value="form.name" size="small" required />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Priority
            <NInputNumber v-model:value="form.priority" :show-button="false" size="small" required />
          </label>
          <NCheckbox v-model:checked="form.enabled" class="self-align-end">Enabled</NCheckbox>
        </section>

        <section class="retry-envelope layout-grid space-sm">
          <div class="retry-envelope-head">
            <h3 class="copy-sm weight-semibold base-text">Fail over inside the selected target</h3>
            <div class="retry-chip-row">
              <NTag size="small" type="info" :bordered="false">{{ attemptSummary }}</NTag>
              <NTag size="small" :bordered="false">Different agent</NTag>
              <NTag size="small" :bordered="false">Same target</NTag>
            </div>
          </div>
          <p class="copy-xs line-normal muted-text">
            A failed agent is excluded for the rest of this request. The target's load-balancing rule chooses among the remaining healthy agents; retries never jump to another route target.
          </p>
        </section>

        <section class="editor-group layout-grid space-lg">
          <div class="layout-grid space-2xs">
            <h3 class="copy-sm weight-semibold base-text">Request eligibility</h3>
            <p class="copy-xs line-normal muted-text">Choose which HTTP methods can use this rule. CONNECT, TRACE, and protocol upgrades are always excluded.</p>
          </div>
          <SegmentedControl v-model="form.methodPreset" :options="methodPresetOptions" label="Retry method preset" />
          <div v-if="form.methodPreset === 'custom'" class="layout-grid space-xs">
            <NDynamicTags :value="form.customMethods" @update:value="updateCustomMethods" />
            <p class="copy-xs muted-text">Enter HTTP method names; they are normalized to uppercase.</p>
          </div>
          <p class="retry-summary-line copy-xs">
            <span class="muted-text">Effective methods</span>
            <code>{{ methodSummary }}</code>
          </p>
        </section>

        <section class="editor-group layout-grid space-xl">
          <div class="layout-grid space-2xs">
            <h3 class="copy-sm weight-semibold base-text">Failure boundary</h3>
            <p class="copy-xs line-normal muted-text">Transport retries stop once response headers arrive. Selected HTTP error responses can still fail over before any response is sent to the client.</p>
          </div>
          <div class="option-grid" role="radiogroup" aria-label="Retry failure boundary">
            <button
              v-for="option in failureModeOptions"
              :key="option.value"
              type="button"
              role="radio"
              class="option-card"
              :class="{ 'option-card--active': form.failureMode === option.value }"
              :aria-checked="form.failureMode === option.value"
              @click="form.failureMode = option.value"
            >
              <span class="weight-medium base-text">{{ option.label }}</span>
              <span class="copy-xs line-normal muted-text">{{ option.detail }}</span>
            </button>
          </div>

          <div class="retry-subgroup layout-grid space-md">
            <div class="retry-subgroup-head">
              <div class="layout-grid space-2xs min-width-zero">
                <h4 class="copy-sm weight-semibold base-text">Retry selected error statuses on another agent</h4>
                <p class="copy-xs line-normal muted-text">The first response body is discarded and the same request is replayed through a different healthy agent. A final matching status is returned unchanged after attempts are exhausted.</p>
              </div>
              <NTag size="small" :type="normalizedStatusCodes.length ? 'warning' : 'default'" :bordered="false">{{ statusSummary }}</NTag>
            </div>
            <SegmentedControl v-model="form.statusPreset" :options="statusPresetOptions" label="Retryable HTTP status preset" />
            <div v-if="form.statusPreset === 'custom'" class="layout-grid space-xs">
              <NDynamicTags :value="form.customStatusCodes" @update:value="updateCustomStatusCodes" />
              <p class="copy-xs muted-text">Add exact HTTP error statuses from 400 through 599.</p>
            </div>
            <div class="status-preset-notes copy-xs muted-text">
              <span><span class="base-text weight-medium">Gateway:</span> 502, 503, 504</span>
              <span><span class="base-text weight-medium">Broad:</span> 408, 425, 429, 500, 502, 503, 504</span>
            </div>
          </div>

          <div class="retry-subgroup layout-grid space-lg">
            <div class="layout-grid space-lg mq-sm-cols-two">
              <label class="layout-grid space-xs copy-xs weight-medium muted-text">
                Retry attempts
                <NInputNumber v-model:value="form.maxRetries" :show-button="false" size="small" :min="1" :max="3" />
                <span class="weight-normal muted-text">One retry means at most two total upstream attempts.</span>
              </label>
              <div class="layout-grid space-xs copy-xs weight-medium muted-text">
                Request body replay
                <SegmentedControl v-model="form.bodyMode" :options="bodyModeOptions" label="Request body replay" />
                <span class="weight-normal muted-text">Unbuffered bodies retry only if the failed attempt consumed zero body bytes.</span>
              </div>
            </div>
            <label v-if="form.bodyMode === PublicRetryBodyMode.BUFFERED" class="layout-grid space-xs copy-xs weight-medium muted-text replay-limit-field">
              Maximum replay body KiB
              <NInputNumber v-model:value="form.maxReplayBodyKiB" :show-button="false" size="small" :min="1" :max="4096" />
              <span class="weight-normal muted-text">Bodies above this limit stream normally and are not retried after consumption. The server also enforces a 64 MiB global replay-memory budget.</span>
            </label>
          </div>

          <div class="retry-subgroup layout-grid space-md">
            <div class="retry-subgroup-head">
              <div class="layout-grid space-2xs min-width-zero">
                <h4 class="copy-sm weight-semibold base-text">Protect bounded responses from mid-transfer agent loss</h4>
                <p class="copy-xs line-normal muted-text">
                  Protected responses are read completely before headers are sent to the client. If the agent disappears mid-body, the request can fail over without exposing partial content. Encoded bytes are preserved exactly—there is no decompression or recompression.
                </p>
              </div>
              <NTag size="small" :type="form.responseBodyMode === PublicRetryResponseBodyMode.BUFFERED ? 'info' : 'default'" :bordered="false">
                {{ responseDeliverySummary }}
              </NTag>
            </div>
            <div class="option-grid" role="radiogroup" aria-label="Response completion mode">
              <button
                type="button"
                role="radio"
                class="option-card"
                :class="{ 'option-card--active': form.responseBodyMode === PublicRetryResponseBodyMode.STREAM }"
                :aria-checked="form.responseBodyMode === PublicRetryResponseBodyMode.STREAM"
                @click="form.responseBodyMode = PublicRetryResponseBodyMode.STREAM"
              >
                <span class="weight-medium base-text">Stream immediately</span>
                <span class="copy-xs line-normal muted-text">Lowest latency and memory use; a failure after headers cannot be retried.</span>
              </button>
              <button
                type="button"
                role="radio"
                class="option-card"
                :class="{ 'option-card--active': form.responseBodyMode === PublicRetryResponseBodyMode.BUFFERED }"
                :aria-checked="form.responseBodyMode === PublicRetryResponseBodyMode.BUFFERED"
                @click="form.responseBodyMode = PublicRetryResponseBodyMode.BUFFERED"
              >
                <span class="weight-medium base-text">Buffer bounded responses</span>
                <span class="copy-xs line-normal muted-text">Adds upstream completion latency and uses replay memory, but enables clean failover while protection remains available.</span>
              </button>
            </div>
            <div v-if="form.responseBodyMode === PublicRetryResponseBodyMode.BUFFERED" class="layout-grid space-lg mq-sm-cols-two">
              <label class="layout-grid space-xs copy-xs weight-medium muted-text replay-limit-field">
                Maximum protected response KiB
                <NInputNumber v-model:value="form.maxBufferedResponseBodyKiB" :show-button="false" size="small" :min="1" :max="65536" />
                <span class="weight-normal muted-text">
                  Larger responses fall back to streaming without dropping the request.
                </span>
              </label>
              <label class="layout-grid space-xs copy-xs weight-medium muted-text replay-limit-field">
                Maximum completion wait seconds
                <NInputNumber v-model:value="form.maxBufferedResponseWaitSeconds" :show-button="false" size="small" :min="1" :max="300" />
                <span class="weight-normal muted-text">
                  Slow or long-lived responses switch cleanly to streaming after this wait.
                </span>
              </label>
            </div>
            <p v-if="form.responseBodyMode === PublicRetryResponseBodyMode.BUFFERED" class="copy-xs line-normal muted-text">
              Protection is best effort. Request and response buffers share the server's 64 MiB replay-memory budget; budget-constrained responses stream normally and the skip reason is recorded in traffic traces.
            </p>
            <p class="copy-xs line-normal muted-text">
              <span class="base-text weight-medium">Always streamed:</span>
              WebSocket/upgrades, HEAD and bodyless responses, ranges and 206 responses, server-sent events, gRPC, multipart streams, and responses with <code>X-Accel-Buffering: no</code>.
            </p>
          </div>
        </section>

        <section v-if="requiresRiskAcknowledgement" class="risk-panel layout-grid space-sm">
          <div class="layout-row align-start space-sm">
            <TriangleAlertIcon class="icon-sm warning-text no-shrink risk-icon" aria-hidden="true" />
            <div class="layout-grid space-2xs min-width-zero">
              <h3 class="copy-sm weight-semibold base-text">Duplicate request risk</h3>
              <p class="copy-xs line-normal muted-text">
                The first agent can fail after the upstream has accepted the request, return a configured retryable status, or lose its response body mid-transfer. Retrying may repeat writes, payments, uploads, or other side effects even when the client sees only one response.
              </p>
            </div>
          </div>
          <NCheckbox v-model:checked="form.duplicateRiskAcknowledged">
            I understand this rule can cause duplicate upstream requests
          </NCheckbox>
        </section>

        <section class="editor-group layout-grid space-lg">
          <div class="layout-grid space-2xs">
            <h3 class="copy-sm weight-semibold base-text">Route and target scope</h3>
            <p class="copy-xs line-normal muted-text">Empty filters match every agent-backed proxy target. Direct and static targets are not eligible.</p>
          </div>
          <NAlert
            v-if="unavailableRouteIds.length || unavailableTargetIds.length"
            type="warning"
            :bordered="false"
            title="Saved scope contains unavailable entries"
          >
            One or more routes or targets were removed after this rule was saved. Remove the entries marked unavailable below before saving.
          </NAlert>
          <div class="target-grid">
            <div class="target-field">
              <div class="target-field-head">
                <span class="copy-xs weight-medium muted-text">Routes</span>
                <span class="copy-xs muted-text">{{ routeSummary }}</span>
              </div>
              <NTransfer
                :value="form.routeIds"
                :options="routeTransferOptions"
                source-title="Available routes"
                target-title="Filtered routes"
                source-filterable
                target-filterable
                virtual-scroll
                size="small"
                :filter="transferFilter"
                @update:value="updateRouteIds"
              />
            </div>
            <div class="target-field">
              <div class="target-field-head">
                <span class="copy-xs weight-medium muted-text">Agent targets</span>
                <span class="copy-xs muted-text">{{ targetSummary }}</span>
              </div>
              <NTransfer
                :value="form.targetIds"
                :options="targetTransferOptions"
                source-title="Available targets"
                target-title="Filtered targets"
                source-filterable
                target-filterable
                virtual-scroll
                size="small"
                :filter="transferFilter"
                @update:value="updateTargetIds"
              />
            </div>
          </div>
        </section>

        <PublicPolicyMatchEditor :form="form.match" />

        <div class="editor-drawer-actions margin-top-sm layout-row align-end-row space-md">
          <NButton secondary attr-type="button" @click="close">Cancel</NButton>
          <DisabledHint :disabled="submitDisabled" :reason="submitDisabledReason">
            <NButton type="primary" attr-type="submit" :disabled="submitDisabled">
              {{ form.id ? 'Save Changes' : 'Create Retry Rule' }}
            </NButton>
          </DisabledHint>
        </div>
      </form>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.editor-group {
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 1.25rem;
}

/* Field labels stack from the top so helper text in a sibling column never stretches the input row. */
label.layout-grid {
  align-content: start;
}

/* Segmented controls keep their natural width inside stacked field groups. */
.segmented {
  justify-self: start;
}

:deep(.disabled-hint-full > .n-input-number) {
  flex: 1 1 auto;
}

.retry-envelope {
  border-radius: 8px;
  padding: 0.875rem 1rem;
  background: var(--app-panel-muted);
}

.retry-envelope-head,
.retry-chip-row,
.retry-subgroup-head,
.target-field-head,
.status-preset-notes {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem 0.75rem;
  flex-wrap: wrap;
}

.retry-subgroup-head {
  align-items: flex-start;
  flex-wrap: nowrap;
}

.target-field-head {
  align-items: baseline;
}

.retry-chip-row {
  gap: 0.375rem;
}

.retry-summary-line {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 0.5rem;
  margin: 0;
}

.retry-summary-line code {
  border-radius: 6px;
  padding: 0.125rem 0.375rem;
  background: var(--app-panel-muted);
  color: var(--app-text);
}

.retry-subgroup + .retry-subgroup {
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 1.25rem;
}

.option-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.75rem;
}

/* Option cards are radio controls, so they keep an input-like hairline; selection uses the accent. */
.option-card {
  display: grid;
  align-content: start;
  gap: 0.25rem;
  min-height: 4.5rem;
  border: 1px solid var(--app-border-subtle);
  border-radius: 8px;
  padding: 0.75rem 0.875rem;
  background: transparent;
  color: inherit;
  cursor: pointer;
  font: inherit;
  text-align: left;
  transition: border-color 140ms ease, background-color 140ms ease;
}

.option-card:hover {
  border-color: var(--app-border);
  background: var(--app-hover);
}

.option-card--active,
.option-card--active:hover {
  border-color: var(--app-accent);
  background: var(--app-accent-soft);
}

.option-card:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: 2px;
}

.status-preset-notes {
  justify-content: flex-start;
  column-gap: 1.25rem;
}

.replay-limit-field {
  max-width: 28rem;
}

.risk-panel {
  border-radius: 8px;
  padding: 0.875rem 1rem;
  background: color-mix(in srgb, var(--app-warning) 9%, var(--app-panel));
}

.risk-icon {
  margin-top: 0.125rem;
}

.target-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1.25rem;
}

.target-field {
  display: grid;
  min-width: 0;
  gap: 0.5rem;
}

@media (max-width: 760px) {
  .option-grid,
  .target-grid {
    grid-template-columns: 1fr;
  }

  .retry-subgroup-head {
    flex-wrap: wrap;
  }
}
</style>
