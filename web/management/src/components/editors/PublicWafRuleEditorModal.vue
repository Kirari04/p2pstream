<script setup lang="ts">
import { computed, inject, reactive, ref, watch } from "vue";
import type { ComputedRef } from "vue";
import TrashIcon from "@primevue/icons/trash";
import { managementClient } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicPolicyKeyPartsEditor from "@/components/editors/PublicPolicyKeyPartsEditor.vue";
import PublicPolicyMatchEditor from "@/components/editors/PublicPolicyMatchEditor.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import {
  PublicListenerProtocol,
  PublicRateLimitKeySource,
  PublicRateLimitMatchOperator,
  PublicWafActivationMode,
  PublicWafRuleAction,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

type Runner = (action: () => Promise<void>) => Promise<boolean>;
type MatcherForm = {
  name: string;
  operator: PublicRateLimitMatchOperator;
  value: string;
};
type KeyPartForm = {
  source: PublicRateLimitKeySource;
  name: string;
};
type HeaderForm = {
  name: string;
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
const rules = computed(() => props.config?.wafRules ?? []);
const providers = computed(() => props.config?.wafCaptchaProviders ?? []);
const matchEditor = ref<InstanceType<typeof PublicPolicyMatchEditor> | null>(null);

const form = reactive({
  id: "",
  name: "",
  enabled: true,
  priority: 100,
  action: PublicWafRuleAction.BLOCK,
  activationMode: PublicWafActivationMode.ALWAYS,
  match: {
    methods: [] as string[],
    protocols: [] as PublicListenerProtocol[],
    hostPatternsText: "",
    pathPrefixesText: "",
    headers: [] as MatcherForm[],
    cookies: [] as MatcherForm[],
    queryParams: [] as MatcherForm[],
  },
  keyParts: [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }] as KeyPartForm[],
  captchaProviderId: "",
  captchaPassMinutes: 30,
  waitingRoomMaxAdmitted: 50,
  waitingRoomAdmissionRate: 10,
  waitingRoomAdmissionTtlMinutes: 10,
  waitingRoomPollSeconds: 5,
  waitingRoomTimeoutMinutes: 30,
  waitingRoomPageTitle: "Waiting room",
  waitingRoomPageBody: "Traffic is high. You will be admitted automatically.",
  triggerWindowSeconds: 10,
  triggerMinimumRps: 50,
  triggerSpikeMultiplier: 4,
  triggerProxyActive: 100,
  triggerBackendActive: 100,
  triggerAgentActive: 50,
  triggerServerCpu: 85,
  triggerAgentCpu: 85,
  triggerMinimumActiveSeconds: 30,
  triggerQuietSeconds: 60,
  blockResponseStatusCode: 403,
  blockResponseContentType: "text/plain; charset=utf-8",
  blockResponseBody: "Request blocked\n",
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

const enabledProviders = computed(() => providers.value.filter((provider) => provider.enabled));
const selectedCaptchaProvider = computed(() => providers.value.find((provider) => provider.id.toString() === form.captchaProviderId) ?? null);
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
  if (form.activationMode === PublicWafActivationMode.AUTOMATIC && form.triggerWindowSeconds < 1) return "Request window must be at least 1 second.";
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
  form.match.methods = [];
  form.match.protocols = [];
  form.match.hostPatternsText = "";
  form.match.pathPrefixesText = "";
  form.match.headers = [];
  form.match.cookies = [];
  form.match.queryParams = [];
  form.keyParts = [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  form.captchaProviderId = firstEnabledProviderId();
  form.captchaPassMinutes = 30;
  form.waitingRoomMaxAdmitted = 50;
  form.waitingRoomAdmissionRate = 10;
  form.waitingRoomAdmissionTtlMinutes = 10;
  form.waitingRoomPollSeconds = 5;
  form.waitingRoomTimeoutMinutes = 30;
  form.waitingRoomPageTitle = "Waiting room";
  form.waitingRoomPageBody = "Traffic is high. You will be admitted automatically.";
  form.triggerWindowSeconds = 10;
  form.triggerMinimumRps = 50;
  form.triggerSpikeMultiplier = 4;
  form.triggerProxyActive = 100;
  form.triggerBackendActive = 100;
  form.triggerAgentActive = 50;
  form.triggerServerCpu = 85;
  form.triggerAgentCpu = 85;
  form.triggerMinimumActiveSeconds = 30;
  form.triggerQuietSeconds = 60;
  form.blockResponseStatusCode = 403;
  form.blockResponseContentType = "text/plain; charset=utf-8";
  form.blockResponseBody = "Request blocked\n";
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
  form.match.methods = [...(rule.match?.methods ?? [])];
  form.match.protocols = [...(rule.match?.protocols ?? [])];
  form.match.hostPatternsText = (rule.match?.hostPatterns ?? []).join("\n");
  form.match.pathPrefixesText = (rule.match?.pathPrefixes ?? []).join("\n");
  form.match.headers = cloneMatchers(rule.match?.headers ?? []);
  form.match.cookies = cloneMatchers(rule.match?.cookies ?? []);
  form.match.queryParams = cloneMatchers(rule.match?.queryParams ?? []);
  form.keyParts = rule.keyParts.length
    ? rule.keyParts.map((part) => ({ source: part.source, name: part.name }))
    : [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  form.captchaProviderId = rule.captchaProviderId ? rule.captchaProviderId.toString() : firstEnabledProviderId();
  form.captchaPassMinutes = millisToMinutes(rule.captchaPassTtlMillis || 1800000n);
  form.waitingRoomMaxAdmitted = Number(rule.waitingRoom?.maxAdmittedSessions || 50n);
  form.waitingRoomAdmissionRate = Number(rule.waitingRoom?.admissionRatePerSecond || 10n);
  form.waitingRoomAdmissionTtlMinutes = millisToMinutes(rule.waitingRoom?.admissionSessionTtlMillis || 600000n);
  form.waitingRoomPollSeconds = millisToSeconds(rule.waitingRoom?.queuePollIntervalMillis || 5000n);
  form.waitingRoomTimeoutMinutes = millisToMinutes(rule.waitingRoom?.queueTimeoutMillis || 1800000n);
  form.waitingRoomPageTitle = rule.waitingRoom?.pageTitle || "Waiting room";
  form.waitingRoomPageBody = rule.waitingRoom?.pageBody || "Traffic is high. You will be admitted automatically.";
  form.triggerWindowSeconds = millisToSeconds(rule.triggers?.requestWindowMillis || 10000n);
  form.triggerMinimumRps = Number(rule.triggers?.minimumRequestRate || 50n);
  form.triggerSpikeMultiplier = rule.triggers?.trafficSpikeMultiplier || 4;
  form.triggerProxyActive = Number(rule.triggers?.proxyActiveRequests || 100n);
  form.triggerBackendActive = Number(rule.triggers?.backendActiveRequests || 100n);
  form.triggerAgentActive = Number(rule.triggers?.agentActiveRequests || 50n);
  form.triggerServerCpu = rule.triggers?.serverCpuPercent || 85;
  form.triggerAgentCpu = rule.triggers?.agentCpuPercent || 85;
  form.triggerMinimumActiveSeconds = millisToSeconds(rule.triggers?.minimumActiveMillis || 30000n);
  form.triggerQuietSeconds = millisToSeconds(rule.triggers?.quietPeriodMillis || 60000n);
  form.blockResponseStatusCode = Number(rule.blockResponseStatusCode || 403n);
  form.blockResponseContentType = rule.blockResponseContentType || "text/plain; charset=utf-8";
  form.blockResponseBody = rule.blockResponseBody || "Request blocked\n";
  form.blockResponseHeaders = rule.blockResponseHeaders.map((header) => ({ name: header.name, value: header.value }));
  isOpen.value = true;
  requestAnimationFrame(() => matchEditor.value?.setInitialTab());
}

function close() {
  isOpen.value = false;
}

function cloneMatchers(matchers: readonly MatcherForm[]): MatcherForm[] {
  return matchers.map((matcher) => ({
    name: matcher.name,
    operator: matcher.operator || PublicRateLimitMatchOperator.EQUALS,
    value: matcher.value,
  }));
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

function lines(value: string): string[] {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}

function keyPartNeedsName(source: PublicRateLimitKeySource): boolean {
  return source === PublicRateLimitKeySource.HEADER ||
    source === PublicRateLimitKeySource.COOKIE ||
    source === PublicRateLimitKeySource.QUERY_PARAM;
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

function addBlockHeader() {
  form.blockResponseHeaders.push({ name: "", value: "" });
}

function removeBlockHeader(index: number) {
  form.blockResponseHeaders.splice(index, 1);
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
      match: {
        methods: [...form.match.methods],
        protocols: [...form.match.protocols],
        hostPatterns: lines(form.match.hostPatternsText),
        pathPrefixes: lines(form.match.pathPrefixesText),
        headers: matcherPayload(form.match.headers),
        cookies: matcherPayload(form.match.cookies),
        queryParams: matcherPayload(form.match.queryParams),
      },
      keyParts: form.keyParts.map((part) => ({
        source: part.source,
        name: keyPartNeedsName(part.source) ? part.name.trim() : "",
      })),
      captchaProviderId: form.captchaProviderId ? BigInt(form.captchaProviderId) : 0n,
      captchaPassTtlMillis: minutesToMillis(form.captchaPassMinutes),
      waitingRoom: {
        maxAdmittedSessions: BigInt(form.waitingRoomMaxAdmitted || 0),
        admissionRatePerSecond: BigInt(form.waitingRoomAdmissionRate || 0),
        admissionSessionTtlMillis: minutesToMillis(form.waitingRoomAdmissionTtlMinutes),
        queuePollIntervalMillis: secondsToMillis(form.waitingRoomPollSeconds),
        queueTimeoutMillis: minutesToMillis(form.waitingRoomTimeoutMinutes),
        pageTitle: form.waitingRoomPageTitle,
        pageBody: form.waitingRoomPageBody,
      },
      triggers: {
        requestWindowMillis: secondsToMillis(form.triggerWindowSeconds),
        minimumRequestRate: BigInt(form.triggerMinimumRps || 0),
        trafficSpikeMultiplier: form.triggerSpikeMultiplier || 0,
        proxyActiveRequests: BigInt(form.triggerProxyActive || 0),
        backendActiveRequests: BigInt(form.triggerBackendActive || 0),
        agentActiveRequests: BigInt(form.triggerAgentActive || 0),
        serverCpuPercent: form.triggerServerCpu || 0,
        agentCpuPercent: form.triggerAgentCpu || 0,
        minimumActiveMillis: secondsToMillis(form.triggerMinimumActiveSeconds),
        quietPeriodMillis: secondsToMillis(form.triggerQuietSeconds),
      },
      blockResponseStatusCode: BigInt(form.blockResponseStatusCode || 403),
      blockResponseBody: form.blockResponseBody,
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
      </section>

      <PublicPolicyMatchEditor ref="matchEditor" :form="form.match" />
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
      </section>

      <section v-if="form.activationMode === PublicWafActivationMode.AUTOMATIC" class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <h4 class="text-sm font-semibold text-white">Automatic triggers</h4>
        <div class="grid gap-4 sm:grid-cols-5">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Window seconds
            <input v-model.number="form.triggerWindowSeconds" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Min RPS
            <input v-model.number="form.triggerMinimumRps" type="number" min="0" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Spike x
            <input v-model.number="form.triggerSpikeMultiplier" type="number" min="0" step="0.1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Server CPU %
            <input v-model.number="form.triggerServerCpu" type="number" min="0" max="100" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Agent CPU %
            <input v-model.number="form.triggerAgentCpu" type="number" min="0" max="100" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
        </div>
        <div class="grid gap-4 sm:grid-cols-5">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Proxy active
            <input v-model.number="form.triggerProxyActive" type="number" min="0" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Backend active
            <input v-model.number="form.triggerBackendActive" type="number" min="0" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Agent active
            <input v-model.number="form.triggerAgentActive" type="number" min="0" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Min active sec
            <input v-model.number="form.triggerMinimumActiveSeconds" type="number" min="0" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Quiet sec
            <input v-model.number="form.triggerQuietSeconds" type="number" min="0" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
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
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Body
          <textarea v-model="form.blockResponseBody" class="vercel-input min-h-24 text-sm normal-case tracking-normal font-mono" />
        </label>
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
