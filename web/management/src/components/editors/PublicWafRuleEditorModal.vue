<script setup lang="ts">
import { computed, inject, reactive, ref, watch } from "vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NButtonGroup, NCheckbox, NInput, NInputNumber, NModal, NSelect } from "naive-ui";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicPolicyKeyPartsEditor from "@/components/editors/PublicPolicyKeyPartsEditor.vue";
import PublicPolicyMatchEditor from "@/components/editors/PublicPolicyMatchEditor.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { modalCardStyle } from "@/lib/naiveUi";
import {
  defaultPolicyMatchForm,
  policyMatchFormFromProto,
  policyMatchRulePayload,
  policyMatchValidationReason,
  type PolicyMatchForm,
} from "@/lib/publicPolicyMatch";
import {
  defaultWafTriggerForm,
  setWafTriggerMetricEnabled,
  wafTriggerFormFromProto,
  wafTriggerPayloadFromForm,
  type WafTriggerMetric,
} from "@/lib/publicWafTriggerForm";
import {
  PublicRateLimitKeySource,
  PublicResponseBodyMode,
  PublicResponseTemplateKind,
  PublicWafActivationMode,
  PublicWafRuleAction,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

type KeyPartForm = {
  source: PublicRateLimitKeySource;
  name: string;
};
type HeaderForm = {
  name: string;
  value: string;
};
type AutomaticTriggerMetricControl = {
  key: WafTriggerMetric;
  label: string;
  unit: string;
  min: number;
  max?: number;
  step?: number;
  body: string;
};
type AutomaticTriggerGroup = {
  title: string;
  body: string;
  metrics: AutomaticTriggerMetricControl[];
};

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));

const isOpen = ref(false);
const rules = computed(() => props.config?.wafRules ?? []);
const providers = computed(() => props.config?.wafCaptchaProviders ?? []);
const genericTemplates = computed(() => (props.config?.responseTemplates ?? []).filter((template) => template.kind === PublicResponseTemplateKind.GENERIC_BODY));
const captchaTemplates = computed(() => (props.config?.responseTemplates ?? []).filter((template) => template.kind === PublicResponseTemplateKind.WAF_CAPTCHA_PAGE));
const waitingRoomTemplates = computed(() => (props.config?.responseTemplates ?? []).filter((template) => template.kind === PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE));
const genericTemplateOptions = computed(() =>
  genericTemplates.value.map((template) => ({
    label: template.name,
    value: template.id.toString(),
  })),
);
const captchaTemplateOptions = computed(() => [
  { label: "Built-in captcha page", value: "" },
  ...captchaTemplates.value.map((template) => ({
    label: template.name,
    value: template.id.toString(),
  })),
]);
const waitingRoomTemplateOptions = computed(() => [
  { label: "Built-in waiting-room page", value: "" },
  ...waitingRoomTemplates.value.map((template) => ({
    label: template.name,
    value: template.id.toString(),
  })),
]);

const form = reactive({
  id: "",
  name: "",
  enabled: true,
  priority: 100,
  action: PublicWafRuleAction.BLOCK,
  activationMode: PublicWafActivationMode.ALWAYS,
  match: defaultPolicyMatchForm() as PolicyMatchForm,
  keyParts: [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }] as KeyPartForm[],
  captchaProviderId: "",
  captchaPassMinutes: 30,
  captchaPageTemplateId: "",
  waitingRoomMaxAdmitted: 50,
  waitingRoomAdmissionRate: 10,
  waitingRoomAdmissionTtlMinutes: 10,
  waitingRoomPollSeconds: 5,
  waitingRoomTimeoutMinutes: 30,
  waitingRoomPageTitle: "Waiting room",
  waitingRoomPageBody: "Traffic is high. You will be admitted automatically.",
  waitingRoomPageTemplateId: "",
  triggers: defaultWafTriggerForm(),
  blockResponseStatusCode: 403,
  blockResponseContentType: "text/plain; charset=utf-8",
  blockResponseBody: "Request blocked\n",
  blockResponseBodyMode: PublicResponseBodyMode.INLINE,
  blockResponseTemplateId: "",
  blockResponseHeaders: [] as HeaderForm[],
});

const actionOptions = [
  { label: "Block", value: PublicWafRuleAction.BLOCK },
  { label: "Captcha", value: PublicWafRuleAction.CAPTCHA },
  { label: "Waiting room", value: PublicWafRuleAction.WAITING_ROOM },
];
const activationOptions = [
  { label: "Always", value: PublicWafActivationMode.ALWAYS },
  { label: "Automatic", value: PublicWafActivationMode.AUTOMATIC },
];
const automaticFlowSteps = [
  {
    label: "1. Match",
    body: "Only requests that match this rule's method, host, path, header, cookie, and query filters are counted or challenged.",
  },
  {
    label: "2. Measure",
    body: "Only enabled metrics are allowed to create pressure. Disabled metrics are saved as 0 and ignored by the server.",
  },
  {
    label: "3. Activate",
    body: "Any enabled metric can activate the rule after pressure stays above its threshold for the minimum active time.",
  },
  {
    label: "4. Cool down",
    body: "After all pressure clears, the rule remains active for the quiet period, then matching requests pass normally again.",
  },
];
const automaticTriggerGroups: AutomaticTriggerGroup[] = [
  {
    title: "Request volume",
    body: "Metrics derived from requests that match this rule.",
    metrics: [
      {
        key: "minimumRequestRate",
        label: "Minimum RPS",
        unit: "rps",
        min: 1,
        body: "Activates when matching request rate reaches this threshold inside the request window.",
      },
      {
        key: "trafficSpikeMultiplier",
        label: "Traffic spike",
        unit: "x",
        min: 0.1,
        max: 100,
        step: 0.1,
        body: "Activates when current matching RPS exceeds the learned inactive baseline by this multiplier.",
      },
    ],
  },
  {
    title: "Active requests",
    body: "Metrics derived from concurrent work already in progress.",
    metrics: [
      {
        key: "proxyActiveRequests",
        label: "Proxy active",
        unit: "requests",
        min: 1,
        body: "Activates when total active public proxy requests reaches this threshold.",
      },
      {
        key: "routeTargetActiveRequests",
        label: "Target active",
        unit: "requests",
        min: 1,
        body: "Activates when any route target reaches this active request count.",
      },
      {
        key: "agentActiveRequests",
        label: "Agent active",
        unit: "requests",
        min: 1,
        body: "Activates when a connected agent reports this active request count.",
      },
    ],
  },
  {
    title: "CPU pressure",
    body: "Metrics for proxy or agent CPU load. Use these for CPU-only waiting-room activation.",
    metrics: [
      {
        key: "serverCpuPercent",
        label: "Server CPU",
        unit: "%",
        min: 1,
        max: 100,
        body: "Activates when proxy server CPU stays at or above this percentage.",
      },
      {
        key: "agentCpuPercent",
        label: "Agent CPU",
        unit: "%",
        min: 1,
        max: 100,
        body: "Activates when any agent reports CPU at or above this percentage.",
      },
    ],
  },
];

const enabledProviders = computed(() => providers.value.filter((provider) => provider.enabled));
const selectedCaptchaProvider = computed(() => providers.value.find((provider) => provider.id.toString() === form.captchaProviderId) ?? null);
const captchaProviderOptions = computed(() => {
  const options = providers.value.map((provider) => ({
    label: `${provider.name}${provider.enabled ? "" : " (disabled)"}`,
    value: provider.id.toString(),
    disabled: !provider.enabled,
  }));
  if (form.captchaProviderId && !selectedCaptchaProvider.value) {
    return [
      { label: `Missing provider ${form.captchaProviderId}`, value: form.captchaProviderId, disabled: true },
      ...options,
    ];
  }
  return options;
});
const selectedActionDescription = computed(() => {
  switch (form.action) {
    case PublicWafRuleAction.CAPTCHA:
      return "Challenges matching requests; successful clients receive a temporary pass cookie.";
    case PublicWafRuleAction.WAITING_ROOM:
      return "Queues matching clients and admits them gradually according to the waiting-room limits.";
    default:
      return "Returns the configured block response immediately.";
  }
});
const selectedActivationTitle = computed(() => (
  form.activationMode === PublicWafActivationMode.AUTOMATIC ? "Automatic mode" : "Always mode"
));
const selectedActivationDescription = computed(() => (
  form.activationMode === PublicWafActivationMode.AUTOMATIC
    ? "After match filters pass, requests are normally allowed while this rule is inactive. The rule starts applying the selected action only when pressure triggers stay active long enough."
    : "After match filters pass, the selected action runs immediately on every matching request. Automatic trigger thresholds are ignored."
));
const triggerValidationMessage = computed(() => {
  if (form.activationMode !== PublicWafActivationMode.AUTOMATIC) return "";
  if (form.triggers.requestWindowSeconds < 1) return "Request window must be at least 1 second.";
  if (form.triggers.minimumActiveSeconds < 1) return "Minimum active time must be at least 1 second.";
  if (form.triggers.quietSeconds < 1) return "Quiet period must be at least 1 second.";
  for (const group of automaticTriggerGroups) {
    for (const metric of group.metrics) {
      const state = form.triggers.metrics[metric.key];
      if (!state.enabled) continue;
      if (state.value < metric.min) return `${metric.label} must be at least ${metric.min.toString()} when enabled.`;
      if (metric.max !== undefined && state.value > metric.max) return `${metric.label} must be at most ${metric.max.toString()}.`;
    }
  }
  return "";
});
const submitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!form.name.trim()) return "Enter a rule name.";
  if (!form.keyParts.length) return "Add at least one key part.";
  if (form.action === PublicWafRuleAction.CAPTCHA) {
    if (!providers.value.length) return "Create a captcha provider first.";
    if (!enabledProviders.value.length) return "Enable a captcha provider first.";
    if (!form.captchaProviderId) return "Select a captcha provider.";
    if (!selectedCaptchaProvider.value) return "Selected captcha provider is missing.";
    if (!selectedCaptchaProvider.value.enabled) return "Selected captcha provider is disabled.";
  }
  if (form.action === PublicWafRuleAction.WAITING_ROOM && form.waitingRoomAdmissionRate < 1) return "Admission rate must be at least 1.";
  if (form.action === PublicWafRuleAction.BLOCK && form.blockResponseBodyMode === PublicResponseBodyMode.TEMPLATE && !form.blockResponseTemplateId) {
    return "Select a block response template.";
  }
  if (triggerValidationMessage.value) return triggerValidationMessage.value;
  if (policyMatchValidationReason(form.match)) return policyMatchValidationReason(form.match);
  return "";
});
const submitDisabled = computed(() => Boolean(submitDisabledReason.value));

function firstEnabledProviderId(): string {
  return enabledProviders.value[0]?.id.toString() ?? "";
}

watch(
  () => [form.action, enabledProviders.value.map((provider) => provider.id.toString()).join(",")],
  () => {
    if (form.action === PublicWafRuleAction.CAPTCHA && !form.captchaProviderId) {
      form.captchaProviderId = firstEnabledProviderId();
    }
  },
);

function nextRuleName(): string {
  const existing = new Set(rules.value.map((rule) => rule.name));
  if (!existing.has("waf-rule")) return "waf-rule";
  let index = 2;
  while (existing.has(`waf-rule-${index}`)) index += 1;
  return `waf-rule-${index}`;
}

function resetForm() {
  form.id = "";
  form.name = nextRuleName();
  form.enabled = true;
  form.priority = 100;
  form.action = PublicWafRuleAction.BLOCK;
  form.activationMode = PublicWafActivationMode.ALWAYS;
  form.match = defaultPolicyMatchForm();
  form.keyParts = [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  form.captchaProviderId = firstEnabledProviderId();
  form.captchaPassMinutes = 30;
  form.captchaPageTemplateId = "";
  form.waitingRoomMaxAdmitted = 50;
  form.waitingRoomAdmissionRate = 10;
  form.waitingRoomAdmissionTtlMinutes = 10;
  form.waitingRoomPollSeconds = 5;
  form.waitingRoomTimeoutMinutes = 30;
  form.waitingRoomPageTitle = "Waiting room";
  form.waitingRoomPageBody = "Traffic is high. You will be admitted automatically.";
  form.waitingRoomPageTemplateId = "";
  form.triggers = defaultWafTriggerForm();
  form.blockResponseStatusCode = 403;
  form.blockResponseContentType = "text/plain; charset=utf-8";
  form.blockResponseBody = "Request blocked\n";
  form.blockResponseBodyMode = PublicResponseBodyMode.INLINE;
  form.blockResponseTemplateId = "";
  form.blockResponseHeaders = [];
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
  form.action = rule.action || PublicWafRuleAction.BLOCK;
  form.activationMode = rule.activationMode || PublicWafActivationMode.ALWAYS;
  form.match = policyMatchFormFromProto(rule.matchRule);
  form.keyParts = rule.keyParts.length
    ? rule.keyParts.map((part) => ({ source: part.source, name: part.name }))
    : [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  form.captchaProviderId = rule.captchaProviderId ? rule.captchaProviderId.toString() : firstEnabledProviderId();
  form.captchaPassMinutes = millisToMinutes(rule.captchaPassTtlMillis || 1800000n);
  form.captchaPageTemplateId = rule.captchaPageTemplateId ? rule.captchaPageTemplateId.toString() : "";
  form.waitingRoomMaxAdmitted = Number(rule.waitingRoom?.maxAdmittedSessions || 50n);
  form.waitingRoomAdmissionRate = Number(rule.waitingRoom?.admissionRatePerSecond || 10n);
  form.waitingRoomAdmissionTtlMinutes = millisToMinutes(rule.waitingRoom?.admissionSessionTtlMillis || 600000n);
  form.waitingRoomPollSeconds = millisToSeconds(rule.waitingRoom?.queuePollIntervalMillis || 5000n);
  form.waitingRoomTimeoutMinutes = millisToMinutes(rule.waitingRoom?.queueTimeoutMillis || 1800000n);
  form.waitingRoomPageTitle = rule.waitingRoom?.pageTitle || "Waiting room";
  form.waitingRoomPageBody = rule.waitingRoom?.pageBody || "Traffic is high. You will be admitted automatically.";
  form.waitingRoomPageTemplateId = rule.waitingRoomPageTemplateId ? rule.waitingRoomPageTemplateId.toString() : "";
  form.triggers = wafTriggerFormFromProto(rule.triggers);
  form.blockResponseStatusCode = Number(rule.blockResponseStatusCode || 403n);
  form.blockResponseContentType = rule.blockResponseContentType || "text/plain; charset=utf-8";
  form.blockResponseBody = rule.blockResponseBody || "Request blocked\n";
  form.blockResponseBodyMode = rule.blockResponseBodyMode || PublicResponseBodyMode.INLINE;
  form.blockResponseTemplateId = rule.blockResponseTemplateId ? rule.blockResponseTemplateId.toString() : "";
  form.blockResponseHeaders = rule.blockResponseHeaders.map((header) => ({ name: header.name, value: header.value }));
  isOpen.value = true;
}

function close() {
  isOpen.value = false;
}

function millisToSeconds(value: bigint): number {
  return Math.max(1, Math.round(Number(value || 0n) / 1000));
}

function millisToMinutes(value: bigint): number {
  return Math.max(1, Math.round(Number(value || 0n) / 60000));
}

function minutesToMillis(value: number): bigint {
  return BigInt(Math.round((value || 0) * 60000));
}

function secondsToMillis(value: number): bigint {
  return BigInt(Math.round((value || 0) * 1000));
}

function keyPartNeedsName(source: PublicRateLimitKeySource): boolean {
  return source === PublicRateLimitKeySource.HEADER ||
    source === PublicRateLimitKeySource.COOKIE ||
    source === PublicRateLimitKeySource.QUERY_PARAM;
}

function addBlockHeader() {
  form.blockResponseHeaders.push({ name: "", value: "" });
}

function removeBlockHeader(index: number) {
  form.blockResponseHeaders.splice(index, 1);
}

function updateTriggerMetricEnabled(metric: WafTriggerMetric, checked: boolean) {
  form.triggers = setWafTriggerMetricEnabled(form.triggers, metric, checked);
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
      action: form.action,
      activationMode: form.activationMode,
      matchRule: policyMatchRulePayload(form.match),
      keyParts: form.keyParts.map((part) => ({
        source: part.source,
        name: keyPartNeedsName(part.source) ? part.name.trim() : "",
      })),
      captchaProviderId: form.captchaProviderId ? BigInt(form.captchaProviderId) : 0n,
      captchaPassTtlMillis: minutesToMillis(form.captchaPassMinutes),
      captchaPageTemplateId: form.action === PublicWafRuleAction.CAPTCHA ? BigInt(form.captchaPageTemplateId || "0") : 0n,
      waitingRoom: {
        maxAdmittedSessions: BigInt(form.waitingRoomMaxAdmitted || 0),
        admissionRatePerSecond: BigInt(form.waitingRoomAdmissionRate || 0),
        admissionSessionTtlMillis: minutesToMillis(form.waitingRoomAdmissionTtlMinutes),
        queuePollIntervalMillis: secondsToMillis(form.waitingRoomPollSeconds),
        queueTimeoutMillis: minutesToMillis(form.waitingRoomTimeoutMinutes),
        pageTitle: form.waitingRoomPageTitle,
        pageBody: form.waitingRoomPageBody,
      },
      waitingRoomPageTemplateId: form.action === PublicWafRuleAction.WAITING_ROOM ? BigInt(form.waitingRoomPageTemplateId || "0") : 0n,
      triggers: wafTriggerPayloadFromForm(form.triggers),
      blockResponseStatusCode: BigInt(form.blockResponseStatusCode || 403),
      blockResponseBody: form.blockResponseBody,
      blockResponseBodyMode: form.action === PublicWafRuleAction.BLOCK ? form.blockResponseBodyMode : PublicResponseBodyMode.INLINE,
      blockResponseTemplateId: form.action === PublicWafRuleAction.BLOCK && form.blockResponseBodyMode === PublicResponseBodyMode.TEMPLATE
        ? BigInt(form.blockResponseTemplateId || "0")
        : 0n,
      blockResponseContentType: form.blockResponseContentType,
      blockResponseHeaders: form.blockResponseHeaders
        .map((header) => ({ name: header.name.trim(), value: header.value }))
        .filter((header) => header.name),
    };
    if (form.id) {
      await managementClient.updatePublicWafRule({ id: BigInt(form.id), ...payload });
    } else {
      await managementClient.createPublicWafRule(payload);
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
    :title="form.id ? 'Edit WAF Rule' : 'Add WAF Rule'"
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

      <section class="layout-grid space-lg">
        <NButtonGroup class="layout-grid cols-one mq-sm-cols-three" size="small">
          <NButton
            v-for="option in actionOptions"
            :key="option.value"
            attr-type="button"
            :type="form.action === option.value ? 'primary' : 'default'"
            @click="form.action = option.value"
          >
            {{ option.label }}
          </NButton>
        </NButtonGroup>
        <p class="copy-xs line-normal muted-text">{{ selectedActionDescription }}</p>
        <NButtonGroup class="layout-grid cols-two" size="small">
          <NButton
            v-for="option in activationOptions"
            :key="option.value"
            attr-type="button"
            :type="form.activationMode === option.value ? 'primary' : 'default'"
            @click="form.activationMode = option.value"
          >
            {{ option.label }}
          </NButton>
        </NButtonGroup>
        <div class="layout-grid space-2xs divider-left frame-standard pad-left-md">
          <p class="copy-xs weight-semibold base-text">{{ selectedActivationTitle }}</p>
          <p class="copy-xs line-normal muted-text">{{ selectedActivationDescription }}</p>
          <p class="copy-xs line-normal muted-text">
            WAF runs before rate limits, traffic shaping, routing, cache, and target forwarding. Lower priority numbers are evaluated first.
          </p>
        </div>
      </section>

      <PublicPolicyMatchEditor :form="form.match" />
      <PublicPolicyKeyPartsEditor :key-parts="form.keyParts" />

      <section v-if="form.action === PublicWafRuleAction.CAPTCHA" class="layout-grid space-lg round-md framed frame-standard muted-bg pad-lg">
        <h4 class="copy-sm weight-semibold base-text">Captcha</h4>
        <div class="layout-grid space-lg mq-sm-cols-two">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Provider
            <NSelect
              v-model:value="form.captchaProviderId"
              size="small"
              :options="captchaProviderOptions"
              :placeholder="providers.length ? 'Select provider' : 'No captcha providers configured'"
              :disabled="!providers.length"
            />
            <span v-if="!providers.length" class="copy-xs normal-text letter-normal muted-text">
              Add a captcha provider in the WAF section before creating a captcha rule.
            </span>
            <span v-else-if="!enabledProviders.length" class="copy-xs normal-text letter-normal warning-text">
              All configured captcha providers are disabled.
            </span>
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Pass TTL minutes
            <NInputNumber v-model:value="form.captchaPassMinutes" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text mq-sm-span-two">
            Page template
            <NSelect v-model:value="form.captchaPageTemplateId" size="small" :options="captchaTemplateOptions" />
          </label>
        </div>
      </section>

      <section v-if="form.action === PublicWafRuleAction.WAITING_ROOM" class="layout-grid space-lg round-md framed frame-standard muted-bg pad-lg">
        <h4 class="copy-sm weight-semibold base-text">Waiting room</h4>
        <div class="layout-grid space-lg mq-sm-cols-five">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Capacity
            <NInputNumber v-model:value="form.waitingRoomMaxAdmitted" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Admit/sec
            <NInputNumber v-model:value="form.waitingRoomAdmissionRate" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            TTL minutes
            <NInputNumber v-model:value="form.waitingRoomAdmissionTtlMinutes" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Poll seconds
            <NInputNumber v-model:value="form.waitingRoomPollSeconds" size="small" :min="1" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Timeout minutes
            <NInputNumber v-model:value="form.waitingRoomTimeoutMinutes" size="small" :min="1" />
          </label>
        </div>
        <div class="layout-grid space-lg mq-sm-cols-two">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Page title
            <NInput v-model:value="form.waitingRoomPageTitle" size="small" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Page body
            <NInput v-model:value="form.waitingRoomPageBody" size="small" />
          </label>
        </div>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Page template
          <NSelect v-model:value="form.waitingRoomPageTemplateId" size="small" :options="waitingRoomTemplateOptions" />
        </label>
      </section>

      <section v-if="form.activationMode === PublicWafActivationMode.AUTOMATIC" class="layout-grid space-xl round-md framed frame-standard muted-bg pad-lg">
        <div class="layout-grid space-2xs">
          <h4 class="copy-sm weight-semibold base-text">Automatic trigger behavior</h4>
          <p class="copy-xs line-normal muted-text">
            These settings decide when the selected WAF action temporarily turns on for matching traffic.
            Enabled metrics are combined as OR conditions; disabled metrics are saved as 0 and ignored.
          </p>
        </div>
        <div class="layout-grid space-md mq-sm-cols-four">
          <div v-for="step in automaticFlowSteps" :key="step.label" class="divider-left frame-standard pad-left-md">
            <p class="copy-xs weight-semibold base-text">{{ step.label }}</p>
            <p class="margin-top-xs copy-xs line-normal muted-text">{{ step.body }}</p>
          </div>
        </div>
        <div class="layout-grid space-lg mq-sm-cols-three">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Window seconds
            <NInputNumber v-model:value="form.triggers.requestWindowSeconds" size="small" :min="1" />
            <p class="copy-xs weight-normal normal-text line-normal letter-normal muted-text">Rolling window used by request-volume metrics.</p>
          </label>
        </div>
        <div v-for="group in automaticTriggerGroups" :key="group.title" class="layout-grid space-md round-md framed frame-standard muted-bg pad-md">
          <div class="layout-grid space-2xs">
            <h5 class="copy-xs weight-semibold label-case letter-wide muted-text">{{ group.title }}</h5>
            <p class="copy-xs line-normal muted-text">{{ group.body }}</p>
          </div>
          <div class="layout-grid space-md mq-md-cols-two mq-xl-cols-three">
            <div
              v-for="metric in group.metrics"
              :key="metric.key"
              class="layout-grid space-md round-sm framed frame-standard muted-bg pad-md"
              :class="form.triggers.metrics[metric.key].enabled ? 'frame-standard' : 'disabled-fade'"
            >
              <div class="layout-row align-center spread-items space-md">
                <NCheckbox
                  class="min-width-zero"
                  :checked="form.triggers.metrics[metric.key].enabled"
                  @update:checked="updateTriggerMetricEnabled(metric.key, $event)"
                >
                  <span class="clip-text copy-sm weight-medium">{{ metric.label }}</span>
                </NCheckbox>
                <span
                  class="no-shrink round-sm framed pad-x-sm pad-y-2xs copy-2xs weight-semibold label-case letter-wide"
                  :class="form.triggers.metrics[metric.key].enabled ? 'success-border success-text' : 'frame-standard muted-text'"
                >
                  {{ form.triggers.metrics[metric.key].enabled ? 'Enabled' : 'Disabled' }}
                </span>
              </div>
              <div class="layout-grid space-xs">
                <div class="layout-row align-center space-sm">
                  <NInputNumber
                    v-model:value="form.triggers.metrics[metric.key].value"
                    size="small"
                    class="min-width-zero grow-fill"
                    :min="metric.min"
                    :max="metric.max"
                    :step="metric.step ?? 1"
                    :disabled="!form.triggers.metrics[metric.key].enabled"
                  />
                  <span class="width-compact no-shrink copy-xs muted-text">{{ metric.unit }}</span>
                </div>
                <p class="copy-xs line-normal muted-text">{{ metric.body }}</p>
              </div>
            </div>
          </div>
        </div>
        <div class="layout-grid space-md round-md framed frame-standard muted-bg pad-md">
          <div class="layout-grid space-2xs">
            <h5 class="copy-xs weight-semibold label-case letter-wide muted-text">Activation timing</h5>
            <p class="copy-xs line-normal muted-text">Timing controls prevent brief spikes from rapidly turning the rule on and off. They are not detection metrics.</p>
          </div>
          <div class="layout-grid space-lg mq-sm-cols-two">
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Minimum active seconds
              <NInputNumber v-model:value="form.triggers.minimumActiveSeconds" size="small" :min="1" />
              <p class="copy-xs weight-normal normal-text line-normal letter-normal muted-text">Pressure must persist this long before the action begins.</p>
            </label>
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Quiet seconds
              <NInputNumber v-model:value="form.triggers.quietSeconds" size="small" :min="1" />
              <p class="copy-xs weight-normal normal-text line-normal letter-normal muted-text">The action stays active this long after all pressure clears.</p>
            </label>
          </div>
        </div>
      </section>

      <section v-if="form.action === PublicWafRuleAction.BLOCK" class="layout-grid space-lg round-md framed frame-standard muted-bg pad-lg">
        <h4 class="copy-sm weight-semibold base-text">Block response</h4>
        <div class="layout-grid space-lg mq-sm-cols-three">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Status
            <NInputNumber v-model:value="form.blockResponseStatusCode" size="small" :min="400" :max="599" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text mq-sm-span-two">
            Content type
            <NInput v-model:value="form.blockResponseContentType" size="small" />
          </label>
        </div>
        <div class="layout-grid space-md round-md framed frame-standard muted-bg pad-md">
          <div class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Body source
            <NButtonGroup class="layout-grid cols-two" size="small">
              <NButton
                attr-type="button"
                :type="form.blockResponseBodyMode === PublicResponseBodyMode.INLINE ? 'primary' : 'default'"
                @click="form.blockResponseBodyMode = PublicResponseBodyMode.INLINE"
              >
                Inline
              </NButton>
              <NButton
                attr-type="button"
                :type="form.blockResponseBodyMode === PublicResponseBodyMode.TEMPLATE ? 'primary' : 'default'"
                @click="form.blockResponseBodyMode = PublicResponseBodyMode.TEMPLATE"
              >
                Template
              </NButton>
            </NButtonGroup>
          </div>
          <label v-if="form.blockResponseBodyMode === PublicResponseBodyMode.TEMPLATE" class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Template
            <NSelect
              v-model:value="form.blockResponseTemplateId"
              size="small"
              :options="genericTemplateOptions"
              :placeholder="genericTemplates.length ? 'Select template' : 'No generic templates'"
              :disabled="!genericTemplates.length"
            />
          </label>
          <label v-else class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Body
            <NInput v-model:value="form.blockResponseBody" type="textarea" class="mono-text" :autosize="{ minRows: 4, maxRows: 8 }" />
          </label>
        </div>
        <div class="layout-grid space-sm">
          <div class="layout-row align-center spread-items space-md">
            <span class="copy-xs weight-medium label-case letter-wide muted-text">Headers</span>
            <NButton secondary size="small" attr-type="button" @click="addBlockHeader">Add Header</NButton>
          </div>
          <div v-for="(header, index) in form.blockResponseHeaders" :key="index" class="layout-grid space-sm mq-sm-two-auto">
            <NInput v-model:value="header.name" size="small" placeholder="Name" />
            <NInput v-model:value="header.value" size="small" placeholder="Value" />
            <NButton
              type="error"
              size="small"
              class="row-remove-button"
              aria-label="Remove response header"
              title="Remove response header"
              attr-type="button"
              @click="removeBlockHeader(index)"
            >
              <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
            </NButton>
          </div>
        </div>
      </section>

      <div class="layout-row align-end-row space-md">
        <NButton secondary attr-type="button" @click="close">Cancel</NButton>
        <DisabledHint :disabled="submitDisabled" :reason="submitDisabledReason">
          <NButton type="primary" attr-type="submit" :disabled="submitDisabled">
            {{ form.id ? 'Save Changes' : 'Create Rule' }}
          </NButton>
        </DisabledHint>
      </div>
    </form>
  </NModal>
</template>

<style scoped>
.row-remove-button {
  width: 2.25rem;
  height: 2.25rem;
  padding: 0 !important;
}
</style>
