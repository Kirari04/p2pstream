<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import PlusIcon from "@primevue/icons/plus";
import TrashIcon from "@primevue/icons/trash";
import { managementClient } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicRateLimitPreview from "@/components/editors/PublicRateLimitPreview.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import {
  PublicListenerProtocol,
  PublicRateLimitAlgorithm,
  PublicRateLimitKeySource,
  PublicRateLimitMatchOperator,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

type Runner = (action: () => Promise<void>) => Promise<boolean>;
type MatcherForm = {
  name: string;
  operator: PublicRateLimitMatchOperator;
  value: string;
};
type MatcherGroupKey = "headers" | "cookies" | "queryParams";
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
const rules = computed(() => props.config?.rateLimitRules ?? []);
const activeMatcherGroup = ref<MatcherGroupKey>("headers");

const form = reactive({
  id: "",
  name: "",
  enabled: true,
  priority: 100,
  algorithm: PublicRateLimitAlgorithm.FIXED_WINDOW,
  limit: 60,
  windowSeconds: 60,
  burst: 0,
  methods: [] as string[],
  protocols: [] as PublicListenerProtocol[],
  hostPatternsText: "",
  pathPrefixesText: "",
  headers: [] as MatcherForm[],
  cookies: [] as MatcherForm[],
  queryParams: [] as MatcherForm[],
  keyParts: [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }] as KeyPartForm[],
  responseStatusCode: 429,
  responseContentType: "text/plain; charset=utf-8",
  responseBody: "Rate limit exceeded\n",
  responseHeaders: [] as HeaderForm[],
});

const methodOptions = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];
const algorithmOptions = [
  { label: "Fixed window", value: PublicRateLimitAlgorithm.FIXED_WINDOW },
  { label: "Sliding window", value: PublicRateLimitAlgorithm.SLIDING_WINDOW },
  { label: "Token bucket", value: PublicRateLimitAlgorithm.TOKEN_BUCKET },
  { label: "Leaky bucket", value: PublicRateLimitAlgorithm.LEAKY_BUCKET },
];
const matcherOperatorOptions = [
  { label: "Present", value: PublicRateLimitMatchOperator.PRESENT },
  { label: "Equals", value: PublicRateLimitMatchOperator.EQUALS },
  { label: "Prefix", value: PublicRateLimitMatchOperator.PREFIX },
  { label: "Suffix", value: PublicRateLimitMatchOperator.SUFFIX },
  { label: "Contains", value: PublicRateLimitMatchOperator.CONTAINS },
];
const matcherGroups = [
  {
    key: "headers",
    label: "Headers",
    singular: "header",
    namePlaceholder: "Header",
  },
  {
    key: "cookies",
    label: "Cookies",
    singular: "cookie",
    namePlaceholder: "Cookie",
  },
  {
    key: "queryParams",
    label: "Query params",
    singular: "query param",
    namePlaceholder: "Param",
  },
] as const;
const keySourceOptions = [
  { label: "Remote IP", value: PublicRateLimitKeySource.REMOTE_IP },
  { label: "Host", value: PublicRateLimitKeySource.HOST },
  { label: "Method", value: PublicRateLimitKeySource.METHOD },
  { label: "Path", value: PublicRateLimitKeySource.PATH },
  { label: "Protocol", value: PublicRateLimitKeySource.PROTOCOL },
  { label: "Header", value: PublicRateLimitKeySource.HEADER },
  { label: "Cookie", value: PublicRateLimitKeySource.COOKIE },
  { label: "Query param", value: PublicRateLimitKeySource.QUERY_PARAM },
];

const usesBurst = computed(() =>
  form.algorithm === PublicRateLimitAlgorithm.TOKEN_BUCKET ||
  form.algorithm === PublicRateLimitAlgorithm.LEAKY_BUCKET,
);
const burstDisabledReason = computed(() => usesBurst.value ? "" : "Burst only applies to token bucket and leaky bucket algorithms.");
const rateLimitSubmitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!form.name.trim()) return "Enter a rule name.";
  if (form.limit < 1) return "Limit must be at least 1.";
  if (form.windowSeconds < 1) return "Window must be at least 1 second.";
  if (!form.keyParts.length) return "Add at least one key part.";
  return "";
});
const submitDisabled = computed(() => Boolean(rateLimitSubmitDisabledReason.value));

function resetForm() {
  form.id = "";
  form.name = nextRuleName();
  form.enabled = true;
  form.priority = 100;
  form.algorithm = PublicRateLimitAlgorithm.FIXED_WINDOW;
  form.limit = 60;
  form.windowSeconds = 60;
  form.burst = 0;
  form.methods = [];
  form.protocols = [];
  form.hostPatternsText = "";
  form.pathPrefixesText = "";
  form.headers = [];
  form.cookies = [];
  form.queryParams = [];
  form.keyParts = [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  form.responseStatusCode = 429;
  form.responseContentType = "text/plain; charset=utf-8";
  form.responseBody = "Rate limit exceeded\n";
  form.responseHeaders = [];
  activeMatcherGroup.value = "headers";
}

function nextRuleName(): string {
  const existing = new Set(rules.value.map((rule) => rule.name));
  if (!existing.has("rate-limit")) return "rate-limit";
  let index = 2;
  while (existing.has(`rate-limit-${index}`)) index += 1;
  return `rate-limit-${index}`;
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
  form.algorithm = rule.algorithm || PublicRateLimitAlgorithm.FIXED_WINDOW;
  form.limit = Number(rule.limit || 60n);
  form.windowSeconds = Math.max(1, Number(rule.windowMillis || 60000n) / 1000);
  form.burst = Number(rule.burst || 0n);
  form.methods = [...(rule.match?.methods ?? [])];
  form.protocols = [...(rule.match?.protocols ?? [])];
  form.hostPatternsText = (rule.match?.hostPatterns ?? []).join("\n");
  form.pathPrefixesText = (rule.match?.pathPrefixes ?? []).join("\n");
  form.headers = cloneMatchers(rule.match?.headers ?? []);
  form.cookies = cloneMatchers(rule.match?.cookies ?? []);
  form.queryParams = cloneMatchers(rule.match?.queryParams ?? []);
  form.keyParts = rule.keyParts.length
    ? rule.keyParts.map((part) => ({ source: part.source, name: part.name }))
    : [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  form.responseStatusCode = Number(rule.responseStatusCode || 429n);
  form.responseContentType = rule.responseContentType || "text/plain; charset=utf-8";
  form.responseBody = rule.responseBody || "Rate limit exceeded\n";
  form.responseHeaders = rule.responseHeaders.map((header) => ({ name: header.name, value: header.value }));
  setInitialMatcherTab();
  isOpen.value = true;
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

function toggleMethod(method: string) {
  if (form.methods.includes(method)) {
    form.methods = form.methods.filter((item) => item !== method);
    return;
  }
  form.methods = [...form.methods, method];
}

function toggleProtocol(protocol: PublicListenerProtocol) {
  if (form.protocols.includes(protocol)) {
    form.protocols = form.protocols.filter((item) => item !== protocol);
    return;
  }
  form.protocols = [...form.protocols, protocol];
}

function addMatcher(target: MatcherForm[]) {
  target.push({ name: "", operator: PublicRateLimitMatchOperator.PRESENT, value: "" });
}

function removeMatcher(target: MatcherForm[], index: number) {
  target.splice(index, 1);
}

function matchersForGroup(group: MatcherGroupKey): MatcherForm[] {
  switch (group) {
    case "cookies":
      return form.cookies;
    case "queryParams":
      return form.queryParams;
    default:
      return form.headers;
  }
}

function activeMatcherGroupConfig() {
  return matcherGroups.find((group) => group.key === activeMatcherGroup.value) ?? matcherGroups[0];
}

function activeMatchers(): MatcherForm[] {
  return matchersForGroup(activeMatcherGroup.value);
}

function matcherCount(group: MatcherGroupKey): number {
  return matchersForGroup(group).length;
}

function addActiveMatcher() {
  addMatcher(activeMatchers());
}

function removeActiveMatcher(index: number) {
  removeMatcher(activeMatchers(), index);
}

function setInitialMatcherTab() {
  activeMatcherGroup.value =
    form.headers.length ? "headers" :
      form.cookies.length ? "cookies" :
        form.queryParams.length ? "queryParams" :
          "headers";
}

function addKeyPart() {
  form.keyParts.push({ source: PublicRateLimitKeySource.REMOTE_IP, name: "" });
}

function removeKeyPart(index: number) {
  form.keyParts.splice(index, 1);
  if (!form.keyParts.length) addKeyPart();
}

function keyPartNeedsName(source: PublicRateLimitKeySource): boolean {
  return source === PublicRateLimitKeySource.HEADER ||
    source === PublicRateLimitKeySource.COOKIE ||
    source === PublicRateLimitKeySource.QUERY_PARAM;
}

function matcherValueDisabledReason(matcher: MatcherForm): string {
  return matcher.operator === PublicRateLimitMatchOperator.PRESENT
    ? "Present only checks that the value exists, so no comparison value is used."
    : "";
}

function keyPartNameDisabledReason(source: PublicRateLimitKeySource): string {
  return keyPartNeedsName(source) ? "" : "This key source does not need a name.";
}

function removeKeyPartDisabledReason(): string {
  return form.keyParts.length <= 1 ? "At least one key part is required." : "";
}

function addResponseHeader() {
  form.responseHeaders.push({ name: "", value: "" });
}

function removeResponseHeader(index: number) {
  form.responseHeaders.splice(index, 1);
}

function lines(value: string): string[] {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
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
      algorithm: form.algorithm,
      limit: BigInt(form.limit || 0),
      windowMillis: BigInt(Math.round((form.windowSeconds || 0) * 1000)),
      burst: BigInt(form.burst || 0),
      match: {
        methods: [...form.methods],
        protocols: [...form.protocols],
        hostPatterns: lines(form.hostPatternsText),
        pathPrefixes: lines(form.pathPrefixesText),
        headers: matcherPayload(form.headers),
        cookies: matcherPayload(form.cookies),
        queryParams: matcherPayload(form.queryParams),
      },
      keyParts: form.keyParts.map((part) => ({
        source: part.source,
        name: keyPartNeedsName(part.source) ? part.name.trim() : "",
      })),
      responseStatusCode: BigInt(form.responseStatusCode || 429),
      responseBody: form.responseBody,
      responseContentType: form.responseContentType,
      responseHeaders: form.responseHeaders
        .map((header) => ({ name: header.name.trim(), value: header.value }))
        .filter((header) => header.name),
    };
    if (form.id) {
      await managementClient.updatePublicRateLimitRule({ id: BigInt(form.id), ...payload });
    } else {
      await managementClient.createPublicRateLimitRule(payload);
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
  <Modal v-model="isOpen" :title="form.id ? 'Edit Rate Limit' : 'Add Rate Limit'" max-width="60rem">
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
          <input v-model="form.enabled" type="checkbox" class="h-4 w-4 accent-white" />
          Enabled
        </label>
      </section>

      <section class="grid gap-4">
        <div class="grid grid-cols-2 overflow-hidden rounded-md border border-[#333] bg-[#0b0b0b] p-1 sm:grid-cols-4">
          <button
            v-for="option in algorithmOptions"
            :key="option.value"
            type="button"
            class="rounded px-3 py-2 text-sm font-medium transition"
            :class="form.algorithm === option.value ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
            @click="form.algorithm = option.value"
          >
            {{ option.label }}
          </button>
        </div>
        <div class="grid gap-4 sm:grid-cols-3">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Limit
            <input v-model.number="form.limit" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" required />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Window seconds
            <input v-model.number="form.windowSeconds" type="number" min="1" step="1" class="vercel-input text-sm normal-case tracking-normal" required />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Burst
            <DisabledHint full-width :disabled="Boolean(burstDisabledReason)" :reason="burstDisabledReason">
              <input
                v-model.number="form.burst"
                type="number"
                min="0"
                class="vercel-input text-sm normal-case tracking-normal"
                :disabled="Boolean(burstDisabledReason)"
              />
            </DisabledHint>
          </label>
        </div>
      </section>

      <PublicRateLimitPreview
        :algorithm="form.algorithm"
        :limit="form.limit"
        :window-seconds="form.windowSeconds"
        :burst="form.burst"
        :enabled="form.enabled"
      />

      <section class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <div>
          <h4 class="text-sm font-semibold text-white">Match</h4>
        </div>
        <div class="grid gap-4 lg:grid-cols-2">
          <div class="grid gap-2">
            <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Methods</span>
            <div class="flex flex-wrap gap-2">
              <button
                v-for="method in methodOptions"
                :key="method"
                type="button"
                class="rounded border px-2.5 py-1 text-xs font-medium transition"
                :class="form.methods.includes(method) ? 'border-white bg-white text-black' : 'border-[#333] bg-black text-[#d4d4d8] hover:border-[#666]'"
                @click="toggleMethod(method)"
              >
                {{ method }}
              </button>
            </div>
          </div>
          <div class="grid gap-2">
            <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Protocols</span>
            <div class="flex flex-wrap gap-2">
              <button
                type="button"
                class="rounded border px-2.5 py-1 text-xs font-medium transition"
                :class="form.protocols.includes(PublicListenerProtocol.HTTP) ? 'border-white bg-white text-black' : 'border-[#333] bg-black text-[#d4d4d8] hover:border-[#666]'"
                @click="toggleProtocol(PublicListenerProtocol.HTTP)"
              >
                HTTP
              </button>
              <button
                type="button"
                class="rounded border px-2.5 py-1 text-xs font-medium transition"
                :class="form.protocols.includes(PublicListenerProtocol.HTTPS) ? 'border-white bg-white text-black' : 'border-[#333] bg-black text-[#d4d4d8] hover:border-[#666]'"
                @click="toggleProtocol(PublicListenerProtocol.HTTPS)"
              >
                HTTPS
              </button>
            </div>
          </div>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Host patterns
            <textarea v-model="form.hostPatternsText" class="vercel-input min-h-20 text-sm normal-case tracking-normal" placeholder="api.example.com&#10;*.example.com" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Path prefixes
            <textarea v-model="form.pathPrefixesText" class="vercel-input min-h-20 text-sm normal-case tracking-normal" placeholder="/api&#10;/login" />
          </label>
        </div>

        <div class="matcher-editor">
          <div class="matcher-editor-header">
            <div>
              <p class="matcher-eyebrow">Request attributes</p>
              <h5 class="matcher-heading">{{ activeMatcherGroupConfig().label }}</h5>
            </div>

            <button type="button" class="matcher-add-button" @click="addActiveMatcher">
              <PlusIcon class="h-3.5 w-3.5" />
              <span>Add {{ activeMatcherGroupConfig().singular }}</span>
            </button>
          </div>

          <div class="matcher-tabs" role="tablist" aria-label="Matcher type">
            <button
              v-for="group in matcherGroups"
              :key="group.key"
              type="button"
              role="tab"
              class="matcher-tab"
              :class="{ 'matcher-tab-active': activeMatcherGroup === group.key }"
              :aria-selected="activeMatcherGroup === group.key"
              @click="activeMatcherGroup = group.key"
            >
              <span>{{ group.label }}</span>
              <span class="matcher-tab-count">{{ matcherCount(group.key) }}</span>
            </button>
          </div>

          <div class="matcher-list-shell">
            <div v-if="!activeMatchers().length" class="matcher-empty">
              <p>No {{ activeMatcherGroupConfig().singular }} matchers configured.</p>
              <button type="button" @click="addActiveMatcher">
                <PlusIcon class="h-3.5 w-3.5" />
                <span>Add {{ activeMatcherGroupConfig().singular }}</span>
              </button>
            </div>

            <div v-else class="matcher-list">
              <div class="matcher-row matcher-row-head" aria-hidden="true">
                <span>Name</span>
                <span>Operator</span>
                <span>Value</span>
                <span />
              </div>

              <div
                v-for="(matcher, index) in activeMatchers()"
                :key="`${activeMatcherGroup}-${index}`"
                class="matcher-row"
              >
                <input
                  v-model="matcher.name"
                  class="vercel-input matcher-input"
                  :placeholder="activeMatcherGroupConfig().namePlaceholder"
                />
                <select v-model="matcher.operator" class="vercel-input matcher-input">
                  <option v-for="option in matcherOperatorOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
                </select>
                <DisabledHint full-width :disabled="Boolean(matcherValueDisabledReason(matcher))" :reason="matcherValueDisabledReason(matcher)">
                  <input
                    v-model="matcher.value"
                    class="vercel-input matcher-input"
                    :placeholder="matcher.operator === PublicRateLimitMatchOperator.PRESENT ? 'Ignored for Present' : 'Value'"
                    :disabled="Boolean(matcherValueDisabledReason(matcher))"
                  />
                </DisabledHint>
                <DangerButton
                  size="small"
                  class="row-remove-button"
                  type="button"
                  :aria-label="`Remove ${activeMatcherGroupConfig().singular} matcher`"
                  :title="`Remove ${activeMatcherGroupConfig().singular} matcher`"
                  @click="removeActiveMatcher(index)"
                >
                  <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
                </DangerButton>
              </div>
            </div>
          </div>
        </div>
      </section>

      <section class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <div class="flex items-center justify-between gap-3">
          <h4 class="text-sm font-semibold text-white">Key parts</h4>
          <SecondaryButton type="button" size="small" label="Add Key" @click="addKeyPart" />
        </div>
        <div class="grid gap-2">
          <div v-for="(part, index) in form.keyParts" :key="index" class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
            <select v-model="part.source" class="vercel-input text-sm">
              <option v-for="option in keySourceOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
            <DisabledHint full-width :disabled="Boolean(keyPartNameDisabledReason(part.source))" :reason="keyPartNameDisabledReason(part.source)">
              <input v-model="part.name" class="vercel-input text-sm" placeholder="Name" :disabled="Boolean(keyPartNameDisabledReason(part.source))" />
            </DisabledHint>
            <DisabledHint :disabled="Boolean(removeKeyPartDisabledReason())" :reason="removeKeyPartDisabledReason()">
              <DangerButton
                size="small"
                class="row-remove-button"
                aria-label="Remove key part"
                title="Remove key part"
                type="button"
                :disabled="Boolean(removeKeyPartDisabledReason())"
                @click="removeKeyPart(index)"
              >
                <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
              </DangerButton>
            </DisabledHint>
          </div>
        </div>
      </section>

      <section class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <h4 class="text-sm font-semibold text-white">Denied response</h4>
        <div class="grid gap-4 sm:grid-cols-3">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Status
            <input v-model.number="form.responseStatusCode" type="number" min="400" max="599" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
            Content type
            <input v-model="form.responseContentType" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
        </div>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Body
          <textarea v-model="form.responseBody" class="vercel-input min-h-24 text-sm normal-case tracking-normal font-mono" />
        </label>
        <div class="grid gap-2">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Headers</span>
            <SecondaryButton type="button" size="small" label="Add Header" @click="addResponseHeader" />
          </div>
          <div v-for="(header, index) in form.responseHeaders" :key="index" class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
            <input v-model="header.name" class="vercel-input text-sm" placeholder="Name" />
            <input v-model="header.value" class="vercel-input text-sm" placeholder="Value" />
            <DangerButton
              size="small"
              class="row-remove-button"
              aria-label="Remove response header"
              title="Remove response header"
              type="button"
              @click="removeResponseHeader(index)"
            >
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
      </section>

      <div class="flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="close" />
        <DisabledHint :disabled="Boolean(rateLimitSubmitDisabledReason)" :reason="rateLimitSubmitDisabledReason">
          <Button class="!bg-white !text-black !border-white" :label="form.id ? 'Save Changes' : 'Create Rule'" type="submit" :disabled="submitDisabled" />
        </DisabledHint>
      </div>
    </form>
  </Modal>
</template>

<style scoped>
.matcher-editor {
  display: grid;
  gap: 0.85rem;
  min-width: 0;
  border: 1px solid #222;
  border-radius: 6px;
  background: #080808;
  padding: 0.85rem;
}

.matcher-editor-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
}

.matcher-eyebrow {
  color: #777;
  font-size: 0.68rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.matcher-heading {
  margin-top: 0.15rem;
  color: #fff;
  font-size: 0.92rem;
  font-weight: 650;
}

.matcher-tabs {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  overflow: hidden;
  border: 1px solid #333;
  border-radius: 6px;
  background: #050505;
  padding: 0.2rem;
}

.matcher-tab {
  display: flex;
  min-width: 0;
  height: 2.25rem;
  align-items: center;
  justify-content: center;
  gap: 0.45rem;
  border-radius: 4px;
  color: #a1a1aa;
  font-size: 0.78rem;
  font-weight: 650;
  transition: background 140ms ease, color 140ms ease;
}

.matcher-tab:hover {
  background: #141414;
  color: #fff;
}

.matcher-tab-active {
  background: #fff;
  color: #000;
}

.matcher-tab-count {
  min-width: 1.25rem;
  border-radius: 999px;
  background: rgb(255 255 255 / 10%);
  padding: 0.1rem 0.35rem;
  font-size: 0.68rem;
  line-height: 1.1;
  text-align: center;
}

.matcher-tab-active .matcher-tab-count {
  background: rgb(0 0 0 / 12%);
}

.matcher-list-shell {
  min-height: 13.5rem;
  max-height: 18rem;
  overflow-y: auto;
  overscroll-behavior: contain;
  border: 1px solid #222;
  border-radius: 6px;
  background: #030303;
}

.matcher-list {
  display: grid;
  gap: 0.45rem;
  padding: 0.6rem;
}

.matcher-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 9rem minmax(0, 1.15fr) 2.25rem;
  gap: 0.5rem;
  align-items: center;
  min-height: 2.5rem;
}

.matcher-row-head {
  min-height: 1.4rem;
  color: #666;
  font-size: 0.68rem;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}

.matcher-input {
  min-width: 0;
  height: 2.25rem;
  font-size: 0.8rem;
  text-transform: none;
  letter-spacing: 0;
}

.matcher-add-button,
.matcher-empty button {
  border: 1px solid #333;
  border-radius: 5px;
  background: #050505;
  color: #d4d4d8;
  font-size: 0.72rem;
  font-weight: 650;
  transition: border-color 140ms ease, color 140ms ease, background 140ms ease;
}

.matcher-add-button,
.matcher-empty button {
  display: inline-flex;
  height: 2rem;
  align-items: center;
  gap: 0.4rem;
  padding: 0 0.65rem;
}

.row-remove-button {
  width: 2.25rem;
  height: 2.25rem;
  padding: 0 !important;
}

.matcher-add-button:hover,
.matcher-empty button:hover {
  border-color: #666;
  background: #0f0f0f;
  color: #fff;
}

.matcher-empty {
  display: grid;
  min-height: 13.5rem;
  place-items: center;
  align-content: center;
  gap: 0.75rem;
  color: #777;
  font-size: 0.82rem;
  text-align: center;
}

@media (max-width: 720px) {
  .matcher-editor-header {
    align-items: stretch;
    flex-direction: column;
  }

  .matcher-add-button {
    justify-content: center;
    width: 100%;
  }

  .matcher-tabs {
    grid-template-columns: 1fr;
  }

  .matcher-row,
  .matcher-row-head {
    grid-template-columns: 1fr;
  }

  .matcher-row-head {
    display: none;
  }

  .row-remove-button {
    width: 100%;
  }
}
</style>
