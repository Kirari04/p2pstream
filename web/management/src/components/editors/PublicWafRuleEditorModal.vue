<script setup lang="ts">
import { computed, inject, reactive, ref, watch } from "vue";
import type { ComputedRef } from "vue";
import TrashIcon from "@primevue/icons/trash";
import { managementClient } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicPolicyKeyPartsEditor from "@/components/editors/PublicPolicyKeyPartsEditor.vue";
import PublicPolicyMatchEditor from "@/components/editors/PublicPolicyMatchEditor.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
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
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import {
  PublicRateLimitKeySource,
  PublicResponseBodyMode,
  PublicResponseTemplateKind,
  PublicWafActivationMode,
  PublicWafRuleAction,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

type Runner = (action: () => Promise<void>) => Promise<boolean>;
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

const runManagementAction = inject<Runner>("runManagementAction");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const isOpen = ref(false);
const rules = computed(() => props.config?.wafRules ?? []);
const providers = computed(() => props.config?.wafCaptchaProviders ?? []);
const genericTemplates = computed(() => (props.config?.responseTemplates ?? []).filter((template) => template.kind === PublicResponseTemplateKind.GENERIC_BODY));
const captchaTemplates = computed(() => (props.config?.responseTemplates ?? []).filter((template) => template.kind === PublicResponseTemplateKind.WAF_CAPTCHA_PAGE));
const waitingRoomTemplates = computed(() => (props.config?.responseTemplates ?? []).filter((template) => template.kind === PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE));

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
        key: "backendActiveRequests",
        label: "Backend active",
        unit: "requests",
        min: 1,
        body: "Activates when any backend reaches this active request count.",
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

function updateTriggerMetricEnabled(metric: WafTriggerMetric, event: Event) {
  form.triggers = setWafTriggerMetricEnabled(form.triggers, metric, (event.target as HTMLInputElement).checked);
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
  <Modal v-model="isOpen" :title="form.id ? 'Edit WAF Rule' : 'Add WAF Rule'" max-width="64rem">
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
      </section>

      <section class="grid gap-4">
        <div class="grid grid-cols-1 overflow-hidden rounded-md border border-[#333] bg-[#0b0b0b] p-1 sm:grid-cols-3">
          <button
            v-for="option in actionOptions"
            :key="option.value"
            type="button"
            class="rounded px-3 py-2 text-sm font-medium transition"
            :class="form.action === option.value ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
            @click="form.action = option.value"
          >
            {{ option.label }}
          </button>
        </div>
        <p class="text-xs leading-5 text-[#888]">{{ selectedActionDescription }}</p>
        <div class="grid grid-cols-2 overflow-hidden rounded-md border border-[#333] bg-[#0b0b0b] p-1">
          <button
            v-for="option in activationOptions"
            :key="option.value"
            type="button"
            class="rounded px-3 py-2 text-sm font-medium transition"
            :class="form.activationMode === option.value ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
            @click="form.activationMode = option.value"
          >
            {{ option.label }}
          </button>
        </div>
        <div class="grid gap-1 border-l border-[#333] pl-3">
          <p class="text-xs font-semibold text-[#d4d4d8]">{{ selectedActivationTitle }}</p>
          <p class="text-xs leading-5 text-[#888]">{{ selectedActivationDescription }}</p>
          <p class="text-xs leading-5 text-[#666]">
            WAF runs before rate limits, traffic shaping, routing, cache, and backend forwarding. Lower priority numbers are evaluated first.
          </p>
        </div>
      </section>

      <PublicPolicyMatchEditor :form="form.match" />
      <PublicPolicyKeyPartsEditor :key-parts="form.keyParts" />

      <section v-if="form.action === PublicWafRuleAction.CAPTCHA" class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <h4 class="text-sm font-semibold text-white">Captcha</h4>
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Provider
            <select v-model="form.captchaProviderId" class="vercel-input text-sm normal-case tracking-normal" :disabled="!providers.length">
              <option value="">{{ providers.length ? 'Select provider' : 'No captcha providers configured' }}</option>
              <option
                v-if="form.captchaProviderId && !selectedCaptchaProvider"
                :value="form.captchaProviderId"
                disabled
              >
                Missing provider {{ form.captchaProviderId }}
              </option>
              <option
                v-for="provider in providers"
                :key="provider.id.toString()"
                :value="provider.id.toString()"
                :disabled="!provider.enabled"
              >
                {{ provider.name }}{{ provider.enabled ? '' : ' (disabled)' }}
              </option>
            </select>
            <span v-if="!providers.length" class="text-xs normal-case tracking-normal text-[#666]">
              Add a captcha provider in the WAF section before creating a captcha rule.
            </span>
            <span v-else-if="!enabledProviders.length" class="text-xs normal-case tracking-normal text-amber-400">
              All configured captcha providers are disabled.
            </span>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Pass TTL minutes
            <input v-model.number="form.captchaPassMinutes" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
            Page template
            <select v-model="form.captchaPageTemplateId" class="vercel-input text-sm normal-case tracking-normal">
              <option value="">Built-in captcha page</option>
              <option v-for="template in captchaTemplates" :key="template.id.toString()" :value="template.id.toString()">
                {{ template.name }}
              </option>
            </select>
          </label>
        </div>
      </section>

      <section v-if="form.action === PublicWafRuleAction.WAITING_ROOM" class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <h4 class="text-sm font-semibold text-white">Waiting room</h4>
        <div class="grid gap-4 sm:grid-cols-5">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Capacity
            <input v-model.number="form.waitingRoomMaxAdmitted" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Admit/sec
            <input v-model.number="form.waitingRoomAdmissionRate" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            TTL minutes
            <input v-model.number="form.waitingRoomAdmissionTtlMinutes" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Poll seconds
            <input v-model.number="form.waitingRoomPollSeconds" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Timeout minutes
            <input v-model.number="form.waitingRoomTimeoutMinutes" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Page title
            <input v-model="form.waitingRoomPageTitle" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Page body
            <input v-model="form.waitingRoomPageBody" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
        </div>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Page template
          <select v-model="form.waitingRoomPageTemplateId" class="vercel-input text-sm normal-case tracking-normal">
            <option value="">Built-in waiting-room page</option>
            <option v-for="template in waitingRoomTemplates" :key="template.id.toString()" :value="template.id.toString()">
              {{ template.name }}
            </option>
          </select>
        </label>
      </section>

      <section v-if="form.activationMode === PublicWafActivationMode.AUTOMATIC" class="grid gap-5 rounded-md border border-[#222] bg-[#050505] p-4">
        <div class="grid gap-1">
          <h4 class="text-sm font-semibold text-white">Automatic trigger behavior</h4>
          <p class="text-xs leading-5 text-[#888]">
            These settings decide when the selected WAF action temporarily turns on for matching traffic.
            Enabled metrics are combined as OR conditions; disabled metrics are saved as 0 and ignored.
          </p>
        </div>
        <div class="grid gap-3 sm:grid-cols-4">
          <div v-for="step in automaticFlowSteps" :key="step.label" class="border-l border-[#333] pl-3">
            <p class="text-xs font-semibold text-[#d4d4d8]">{{ step.label }}</p>
            <p class="mt-1 text-xs leading-5 text-[#888]">{{ step.body }}</p>
          </div>
        </div>
        <div class="grid gap-4 sm:grid-cols-3">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Window seconds
            <input v-model.number="form.triggers.requestWindowSeconds" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
            <p class="text-xs font-normal normal-case leading-5 tracking-normal text-[#666]">Rolling window used by request-volume metrics.</p>
          </label>
        </div>
        <div v-for="group in automaticTriggerGroups" :key="group.title" class="grid gap-3 rounded-md border border-[#222] bg-[#070707] p-3">
          <div class="grid gap-1">
            <h5 class="text-xs font-semibold uppercase tracking-wider text-[#888]">{{ group.title }}</h5>
            <p class="text-xs leading-5 text-[#666]">{{ group.body }}</p>
          </div>
          <div class="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            <div
              v-for="metric in group.metrics"
              :key="metric.key"
              class="grid gap-3 rounded border border-[#222] bg-[#0b0b0b] p-3"
              :class="form.triggers.metrics[metric.key].enabled ? 'border-[#333]' : 'opacity-70'"
            >
              <div class="flex items-center justify-between gap-3">
                <label class="flex min-w-0 items-center gap-2 text-sm font-medium text-[#d4d4d8]">
                  <input
                    type="checkbox"
                    :checked="form.triggers.metrics[metric.key].enabled"
                    @change="updateTriggerMetricEnabled(metric.key, $event)"
                  />
                  <span class="truncate">{{ metric.label }}</span>
                </label>
                <span
                  class="shrink-0 rounded border px-2 py-0.5 text-[0.68rem] font-semibold uppercase tracking-wider"
                  :class="form.triggers.metrics[metric.key].enabled ? 'border-emerald-500/30 text-emerald-300' : 'border-[#333] text-[#777]'"
                >
                  {{ form.triggers.metrics[metric.key].enabled ? 'Enabled' : 'Disabled' }}
                </span>
              </div>
              <div class="grid gap-1.5">
                <div class="flex items-center gap-2">
                  <input
                    v-model.number="form.triggers.metrics[metric.key].value"
                    type="number"
                    :min="metric.min"
                    :max="metric.max"
                    :step="metric.step ?? 1"
                    :disabled="!form.triggers.metrics[metric.key].enabled"
                    class="vercel-input min-w-0 flex-1 text-sm normal-case tracking-normal disabled:cursor-not-allowed disabled:border-[#222] disabled:bg-[#111] disabled:text-[#555]"
                  />
                  <span class="w-16 shrink-0 text-xs text-[#777]">{{ metric.unit }}</span>
                </div>
                <p class="text-xs leading-5 text-[#666]">{{ metric.body }}</p>
              </div>
            </div>
          </div>
        </div>
        <div class="grid gap-3 rounded-md border border-[#222] bg-[#070707] p-3">
          <div class="grid gap-1">
            <h5 class="text-xs font-semibold uppercase tracking-wider text-[#888]">Activation timing</h5>
            <p class="text-xs leading-5 text-[#666]">Timing controls prevent brief spikes from rapidly turning the rule on and off. They are not detection metrics.</p>
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              Minimum active seconds
              <input v-model.number="form.triggers.minimumActiveSeconds" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
              <p class="text-xs font-normal normal-case leading-5 tracking-normal text-[#666]">Pressure must persist this long before the action begins.</p>
            </label>
            <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              Quiet seconds
              <input v-model.number="form.triggers.quietSeconds" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
              <p class="text-xs font-normal normal-case leading-5 tracking-normal text-[#666]">The action stays active this long after all pressure clears.</p>
            </label>
          </div>
        </div>
      </section>

      <section v-if="form.action === PublicWafRuleAction.BLOCK" class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <h4 class="text-sm font-semibold text-white">Block response</h4>
        <div class="grid gap-4 sm:grid-cols-3">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Status
            <input v-model.number="form.blockResponseStatusCode" type="number" min="400" max="599" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
            Content type
            <input v-model="form.blockResponseContentType" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
        </div>
        <div class="grid gap-3 rounded-md border border-[#222] bg-[#050505] p-3">
          <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Body source
            <div class="grid grid-cols-2 rounded-md border border-[#333] bg-[#0b0b0b] p-1">
              <button
                type="button"
                class="rounded px-3 py-2 text-sm font-medium normal-case tracking-normal transition"
                :class="form.blockResponseBodyMode === PublicResponseBodyMode.INLINE ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
                @click="form.blockResponseBodyMode = PublicResponseBodyMode.INLINE"
              >
                Inline
              </button>
              <button
                type="button"
                class="rounded px-3 py-2 text-sm font-medium normal-case tracking-normal transition"
                :class="form.blockResponseBodyMode === PublicResponseBodyMode.TEMPLATE ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
                @click="form.blockResponseBodyMode = PublicResponseBodyMode.TEMPLATE"
              >
                Template
              </button>
            </div>
          </div>
          <label v-if="form.blockResponseBodyMode === PublicResponseBodyMode.TEMPLATE" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Template
            <select v-model="form.blockResponseTemplateId" class="vercel-input text-sm normal-case tracking-normal">
              <option value="">{{ genericTemplates.length ? 'Select template' : 'No generic templates' }}</option>
              <option v-for="template in genericTemplates" :key="template.id.toString()" :value="template.id.toString()">
                {{ template.name }}
              </option>
            </select>
          </label>
          <label v-else class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Body
            <textarea v-model="form.blockResponseBody" class="vercel-input min-h-24 text-sm normal-case tracking-normal font-mono" />
          </label>
        </div>
        <div class="grid gap-2">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Headers</span>
            <SecondaryButton type="button" size="small" label="Add Header" @click="addBlockHeader" />
          </div>
          <div v-for="(header, index) in form.blockResponseHeaders" :key="index" class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
            <input v-model="header.name" class="vercel-input text-sm" placeholder="Name" />
            <input v-model="header.value" class="vercel-input text-sm" placeholder="Value" />
            <DangerButton size="small" class="row-remove-button" aria-label="Remove response header" title="Remove response header" type="button" @click="removeBlockHeader(index)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
      </section>

      <div class="flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="close" />
        <DisabledHint :disabled="Boolean(submitDisabledReason)" :reason="submitDisabledReason">
          <Button :label="form.id ? 'Save Changes' : 'Create Rule'" type="submit" :disabled="submitDisabled" />
        </DisabledHint>
      </div>
    </form>
  </Modal>
</template>

<style scoped>
.row-remove-button {
  width: 2.25rem;
  height: 2.25rem;
  padding: 0 !important;
}
</style>
