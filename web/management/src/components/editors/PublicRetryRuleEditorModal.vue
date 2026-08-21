<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import {
  NButton,
  NButtonGroup,
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
import {
  PublicRetryBodyMode,
  PublicRetryFailureMode,
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  type GetPublicProxyConfigResponse,
  type PublicRoute,
} from "@/gen/proto/p2pstream/v1/management_pb";

type MethodPreset = "safe" | "idempotent" | "all" | "custom";
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

const form = reactive({
  id: "",
  name: "",
  priority: 100,
  enabled: true,
  methodPreset: "safe" as MethodPreset,
  customMethods: [...safeMethods],
  maxRetries: 1,
  failureMode: PublicRetryFailureMode.CONNECTION_FAILURES,
  bodyMode: PublicRetryBodyMode.NEVER,
  maxReplayBodyKiB: 256,
  routeIds: [] as string[],
  targetIds: [] as string[],
  match: defaultPolicyMatchForm() as PolicyMatchForm,
  duplicateRiskAcknowledged: false,
});

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
const requiresRiskAcknowledgement = computed(() =>
  form.failureMode === PublicRetryFailureMode.PRE_RESPONSE_FAILURES ||
  normalizedMethods.value.some((method) => !["GET", "HEAD", "OPTIONS"].includes(method)),
);
const methodSummary = computed(() => normalizedMethods.value[0] === "*"
  ? "All supported methods"
  : normalizedMethods.value.join(", "));
const routeSummary = computed(() => form.routeIds.length ? `${form.routeIds.length.toString()} selected` : "All routes");
const targetSummary = computed(() => form.targetIds.length ? `${form.targetIds.length.toString()} selected` : "All agent targets");
const attemptSummary = computed(() => `${(form.maxRetries + 1).toString()} total attempts`);

const routeTransferOptions = computed<RetryTransferOption[]>(() => routes.value.map((route) => {
  const value = route.id.toString();
  return {
    label: `${routeLabel(route)} — ${routeDestinationLabel(route)}`,
    value,
    searchText: `${routeLabel(route)} ${routeDestinationLabel(route)}`.toLowerCase(),
    disabled: !form.routeIds.includes(value) && form.routeIds.length >= maxFilterItems,
  };
}));
const targetTransferOptions = computed<RetryTransferOption[]>(() => agentTargets.value.map((target) => {
  const value = target.id.toString();
  const detail = `${routeTargetTypeLabel(target.targetType)} / ${target.url || "no upstream URL"}`;
  return {
    label: `#${value} ${routeTargetName(target)} — ${detail}`,
    value,
    searchText: `#${value} ${routeTargetName(target)} ${detail}`.toLowerCase(),
    disabled: !form.targetIds.includes(value) && form.targetIds.length >= maxFilterItems,
  };
}));

const submitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!/^[A-Za-z0-9._-]{1,64}$/.test(form.name.trim())) return "Use 1–64 letters, numbers, dots, underscores, or hyphens for the name.";
  if (!Number.isInteger(form.priority)) return "Priority must be a whole number.";
  if (!Number.isInteger(form.maxRetries) || form.maxRetries < 1 || form.maxRetries > 3) return "Retries must be between 1 and 3.";
  if (form.routeIds.length > maxFilterItems) return `Select at most ${maxFilterItems.toString()} routes.`;
  if (form.targetIds.length > maxFilterItems) return `Select at most ${maxFilterItems.toString()} targets.`;
  const methodError = methodsValidationReason(normalizedMethods.value);
  if (methodError) return methodError;
  if (form.bodyMode === PublicRetryBodyMode.BUFFERED && (!Number.isInteger(form.maxReplayBodyKiB) || form.maxReplayBodyKiB < 1 || form.maxReplayBodyKiB > 4096)) {
    return "Replay body limit must be between 1 KiB and 4,096 KiB.";
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
  form.bodyMode = PublicRetryBodyMode.NEVER;
  form.maxReplayBodyKiB = 256;
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
  form.bodyMode = rule.bodyMode || PublicRetryBodyMode.NEVER;
  form.maxReplayBodyKiB = Math.max(1, Math.round(Number(rule.maxReplayBodyBytes || 262144n) / 1024));
  form.routeIds = rule.routeIds.map(String);
  form.targetIds = rule.targetIds.map(String);
  form.match = policyMatchFormFromProto(rule.matchRule);
  form.duplicateRiskAcknowledged = requiresRiskFor(rule.methods, form.failureMode);
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

function requiresRiskFor(methods: readonly string[], failureMode: PublicRetryFailureMode): boolean {
  return failureMode === PublicRetryFailureMode.PRE_RESPONSE_FAILURES ||
    methods.some((method) => !["GET", "HEAD", "OPTIONS"].includes(method));
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
      bodyMode: form.bodyMode,
      maxReplayBodyBytes: form.bodyMode === PublicRetryBodyMode.BUFFERED
        ? BigInt(form.maxReplayBodyKiB * 1024)
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
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text mq-sm-span-two">
            Name
            <NInput v-model:value="form.name" size="small" required />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Priority
            <NInputNumber v-model:value="form.priority" :show-button="false" size="small" required />
          </label>
          <NCheckbox v-model:checked="form.enabled" class="self-align-end">Enabled</NCheckbox>
        </section>

        <section class="retry-envelope">
          <div class="retry-envelope-head">
            <div>
              <p class="panel-eyebrow">Attempt envelope</p>
              <h4 class="copy-base weight-semibold base-text">Fail over inside the selected target</h4>
            </div>
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

        <section class="layout-grid space-lg">
          <div>
            <h4 class="copy-sm weight-semibold base-text">Request eligibility</h4>
            <p class="margin-top-xs copy-xs line-normal muted-text">Choose which HTTP methods can use this rule. CONNECT, TRACE, and protocol upgrades are always excluded.</p>
          </div>
          <NButtonGroup class="method-presets" size="small" role="group" aria-label="Retry method preset">
            <NButton attr-type="button" :type="form.methodPreset === 'safe' ? 'primary' : 'default'" @click="form.methodPreset = 'safe'">GET + HEAD</NButton>
            <NButton attr-type="button" :type="form.methodPreset === 'idempotent' ? 'primary' : 'default'" @click="form.methodPreset = 'idempotent'">Idempotent</NButton>
            <NButton attr-type="button" :type="form.methodPreset === 'all' ? 'primary' : 'default'" @click="form.methodPreset = 'all'">All methods</NButton>
            <NButton attr-type="button" :type="form.methodPreset === 'custom' ? 'primary' : 'default'" @click="form.methodPreset = 'custom'">Custom</NButton>
          </NButtonGroup>
          <div v-if="form.methodPreset === 'custom'" class="layout-grid space-xs">
            <NDynamicTags :value="form.customMethods" @update:value="updateCustomMethods" />
            <p class="copy-xs muted-text">Enter HTTP method names; they are normalized to uppercase.</p>
          </div>
          <div class="retry-summary-line">
            <span class="panel-eyebrow">Effective methods</span>
            <code>{{ methodSummary }}</code>
          </div>
        </section>

        <section class="layout-grid space-lg round-md framed frame-standard muted-bg pad-lg">
          <div>
            <h4 class="copy-sm weight-semibold base-text">Failure boundary</h4>
            <p class="margin-top-xs copy-xs line-normal muted-text">Retries stop permanently once response headers arrive. HTTP status codes are never retried.</p>
          </div>
          <div class="failure-mode-grid" role="radiogroup" aria-label="Retry failure boundary">
            <button
              v-for="option in failureModeOptions"
              :key="option.value"
              type="button"
              role="radio"
              class="failure-mode-option"
              :class="{ 'failure-mode-option--active': form.failureMode === option.value }"
              :aria-checked="form.failureMode === option.value"
              @click="form.failureMode = option.value"
            >
              <span class="layout-grid space-2xs text-left">
                <span class="weight-medium base-text">{{ option.label }}</span>
                <span class="copy-xs line-normal muted-text">{{ option.detail }}</span>
              </span>
            </button>
          </div>
          <div class="layout-grid space-lg mq-sm-cols-two">
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Retry attempts
              <NInputNumber v-model:value="form.maxRetries" :show-button="false" size="small" :min="1" :max="3" />
              <span class="copy-xs weight-normal normal-text letter-normal muted-text">One retry means at most two total upstream attempts.</span>
            </label>
            <div class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Request body replay
              <NButtonGroup class="cols-two" size="small">
                <NButton attr-type="button" :type="form.bodyMode === PublicRetryBodyMode.NEVER ? 'primary' : 'default'" @click="form.bodyMode = PublicRetryBodyMode.NEVER">Do not buffer</NButton>
                <NButton attr-type="button" :type="form.bodyMode === PublicRetryBodyMode.BUFFERED ? 'primary' : 'default'" @click="form.bodyMode = PublicRetryBodyMode.BUFFERED">Buffer body</NButton>
              </NButtonGroup>
              <span class="copy-xs weight-normal normal-text letter-normal muted-text">Unbuffered bodies retry only if the failed attempt consumed zero body bytes.</span>
            </div>
          </div>
          <label v-if="form.bodyMode === PublicRetryBodyMode.BUFFERED" class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text replay-limit-field">
            Maximum replay body KiB
            <NInputNumber v-model:value="form.maxReplayBodyKiB" :show-button="false" size="small" :min="1" :max="4096" />
            <span class="copy-xs weight-normal normal-text letter-normal muted-text">Bodies above this limit stream normally and are not retried after consumption. The server also enforces a 64 MiB global replay-memory budget.</span>
          </label>
        </section>

        <section v-if="requiresRiskAcknowledgement" class="risk-panel">
          <div class="risk-marker" aria-hidden="true">!</div>
          <div class="layout-grid space-sm min-width-zero">
            <div>
              <h4 class="copy-sm weight-semibold base-text">Duplicate request risk</h4>
              <p class="margin-top-xs copy-xs line-normal muted-text">
                The first agent can fail after the upstream has accepted the request. Retrying may repeat writes, payments, uploads, or other side effects even when the client sees only one response.
              </p>
            </div>
            <NCheckbox v-model:checked="form.duplicateRiskAcknowledged">
              I understand this rule can cause duplicate upstream requests
            </NCheckbox>
          </div>
        </section>

        <section class="layout-grid space-lg">
          <div>
            <h4 class="copy-sm weight-semibold base-text">Route and target scope</h4>
            <p class="margin-top-xs copy-xs line-normal muted-text">Empty filters match every agent-backed proxy target. Direct and static targets are not eligible.</p>
          </div>
          <div class="target-grid">
            <div class="target-panel">
              <div class="target-panel-head"><div><p class="panel-eyebrow">Routes</p><h5 class="panel-heading">{{ routeSummary }}</h5></div></div>
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
            <div class="target-panel">
              <div class="target-panel-head"><div><p class="panel-eyebrow">Agent targets</p><h5 class="panel-heading">{{ targetSummary }}</h5></div></div>
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
          <NButton secondary @click="close">Cancel</NButton>
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
.retry-envelope {
  display: grid;
  gap: 0.75rem;
  padding: 1rem;
  border: 1px solid color-mix(in srgb, var(--app-accent) 28%, var(--app-border));
  border-left: 3px solid var(--app-accent);
  border-radius: 0.5rem;
  background: color-mix(in srgb, var(--app-accent) 6%, var(--app-panel));
}

.retry-envelope-head,
.retry-chip-row,
.retry-summary-line,
.target-panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.method-presets {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.retry-summary-line {
  justify-content: flex-start;
  padding: 0.65rem 0.8rem;
  border: 1px solid var(--app-border);
  border-radius: 0.5rem;
  background: var(--app-panel-muted);
}

.failure-mode-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.75rem;
}

.failure-mode-option {
  min-height: 5rem;
  padding: 0.8rem;
  border: 1px solid var(--app-border);
  border-radius: 0.45rem;
  color: inherit;
  background: var(--app-panel);
  cursor: pointer;
  font: inherit;
  text-align: left;
}

.failure-mode-option:hover {
  border-color: color-mix(in srgb, var(--app-accent) 55%, var(--app-border));
  background: color-mix(in srgb, var(--app-accent) 4%, var(--app-panel));
}

.failure-mode-option--active {
  border-color: var(--app-accent);
  box-shadow: inset 3px 0 0 var(--app-accent);
  background: color-mix(in srgb, var(--app-accent) 8%, var(--app-panel));
}

.failure-mode-option:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: 2px;
}

.replay-limit-field {
  max-width: 28rem;
}

.risk-panel {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  gap: 0.9rem;
  padding: 1rem;
  border: 1px solid color-mix(in srgb, var(--app-warning) 45%, var(--app-border));
  border-radius: 0.5rem;
  background: color-mix(in srgb, var(--app-warning) 8%, var(--app-panel));
}

.risk-marker {
  display: grid;
  place-items: center;
  width: 1.7rem;
  height: 1.7rem;
  border-radius: 50%;
  color: var(--app-panel);
  background: var(--app-warning);
  font-weight: 800;
}

.target-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem;
}

.target-panel {
  min-width: 0;
  padding: 0.9rem;
  border: 1px solid var(--app-border);
  border-radius: 0.5rem;
  background: var(--app-panel);
}

.panel-eyebrow {
  color: var(--text-color-3);
  font-size: 0.68rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.panel-heading {
  margin-top: 0.15rem;
  font-size: 0.83rem;
  font-weight: 600;
}

@media (max-width: 760px) {
  .method-presets,
  .failure-mode-grid,
  .target-grid {
    grid-template-columns: 1fr;
  }
}
</style>
