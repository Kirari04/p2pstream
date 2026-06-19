<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicRateLimitPreview from "@/components/editors/PublicRateLimitPreview.vue";
import PublicPolicyMatchEditor from "@/components/editors/PublicPolicyMatchEditor.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import {
  defaultPolicyMatchForm,
  policyMatchFormFromProto,
  policyMatchRulePayload,
  policyMatchValidationReason,
  type PolicyMatchForm,
} from "@/lib/publicPolicyMatch";
import Button from "@/components/ui/Button.vue";
import DangerButton from "@/components/ui/DangerButton.vue";
import Modal from "@/components/ui/Modal.vue";
import SecondaryButton from "@/components/ui/SecondaryButton.vue";
import {
  PublicRateLimitAlgorithm,
  PublicRateLimitKeySource,
  PublicResponseBodyMode,
  PublicResponseTemplateKind,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

type Runner = (action: () => Promise<void>) => Promise<boolean>;
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
const genericTemplates = computed(() => (props.config?.responseTemplates ?? []).filter((template) => template.kind === PublicResponseTemplateKind.GENERIC_BODY));

const form = reactive({
  id: "",
  name: "",
  enabled: true,
  priority: 100,
  algorithm: PublicRateLimitAlgorithm.FIXED_WINDOW,
  limit: 60,
  windowSeconds: 60,
  burst: 0,
  match: defaultPolicyMatchForm() as PolicyMatchForm,
  keyParts: [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }] as KeyPartForm[],
  responseStatusCode: 429,
  responseContentType: "text/plain; charset=utf-8",
  responseBody: "Rate limit exceeded\n",
  responseBodyMode: PublicResponseBodyMode.INLINE,
  responseBodyTemplateId: "",
  responseHeaders: [] as HeaderForm[],
});

const algorithmOptions = [
  { label: "Fixed window", value: PublicRateLimitAlgorithm.FIXED_WINDOW },
  { label: "Sliding window", value: PublicRateLimitAlgorithm.SLIDING_WINDOW },
  { label: "Token bucket", value: PublicRateLimitAlgorithm.TOKEN_BUCKET },
  { label: "Leaky bucket", value: PublicRateLimitAlgorithm.LEAKY_BUCKET },
];
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
  if (form.responseBodyMode === PublicResponseBodyMode.TEMPLATE && !form.responseBodyTemplateId) return "Select a response template.";
  if (policyMatchValidationReason(form.match)) return policyMatchValidationReason(form.match);
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
  form.match = defaultPolicyMatchForm();
  form.keyParts = [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  form.responseStatusCode = 429;
  form.responseContentType = "text/plain; charset=utf-8";
  form.responseBody = "Rate limit exceeded\n";
  form.responseBodyMode = PublicResponseBodyMode.INLINE;
  form.responseBodyTemplateId = "";
  form.responseHeaders = [];
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
  form.match = policyMatchFormFromProto(rule.matchRule);
  form.keyParts = rule.keyParts.length
    ? rule.keyParts.map((part) => ({ source: part.source, name: part.name }))
    : [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  form.responseStatusCode = Number(rule.responseStatusCode || 429n);
  form.responseContentType = rule.responseContentType || "text/plain; charset=utf-8";
  form.responseBody = rule.responseBody || "Rate limit exceeded\n";
  form.responseBodyMode = rule.responseBodyMode || PublicResponseBodyMode.INLINE;
  form.responseBodyTemplateId = rule.responseBodyTemplateId ? rule.responseBodyTemplateId.toString() : "";
  form.responseHeaders = rule.responseHeaders.map((header) => ({ name: header.name, value: header.value }));
  isOpen.value = true;
}

function close() {
  isOpen.value = false;
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
      matchRule: policyMatchRulePayload(form.match),
      keyParts: form.keyParts.map((part) => ({
        source: part.source,
        name: keyPartNeedsName(part.source) ? part.name.trim() : "",
      })),
      responseStatusCode: BigInt(form.responseStatusCode || 429),
      responseBody: form.responseBody,
      responseBodyMode: form.responseBodyMode,
      responseBodyTemplateId: form.responseBodyMode === PublicResponseBodyMode.TEMPLATE ? BigInt(form.responseBodyTemplateId || "0") : 0n,
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
          <input v-model="form.name" class="app-control text-sm normal-case tracking-normal" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Priority
          <input v-model.number="form.priority" type="number" class="app-control text-sm normal-case tracking-normal" required />
        </label>
        <label class="flex items-center gap-2 self-end text-sm text-[#d4d4d8]">
          <input v-model="form.enabled" type="checkbox" />
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
            <input v-model.number="form.limit" type="number" min="1" class="app-control text-sm normal-case tracking-normal" required />
            <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Max requests allowed per window.</p>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Window seconds
            <input v-model.number="form.windowSeconds" type="number" min="1" step="1" class="app-control text-sm normal-case tracking-normal" required />
            <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Duration of each rate limit window.</p>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Burst
            <DisabledHint full-width :disabled="Boolean(burstDisabledReason)" :reason="burstDisabledReason">
              <input
                v-model.number="form.burst"
                type="number"
                min="0"
                class="app-control text-sm normal-case tracking-normal"
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

      <PublicPolicyMatchEditor :form="form.match" />

      <section class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <div class="flex items-center justify-between gap-3">
          <h4 class="text-sm font-semibold text-white">Key parts</h4>
          <SecondaryButton type="button" size="small" label="Add Key" @click="addKeyPart" />
        </div>
        <div class="grid gap-2">
          <div v-for="(part, index) in form.keyParts" :key="index" class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
            <select v-model="part.source" class="app-control text-sm">
              <option v-for="option in keySourceOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
            <DisabledHint full-width :disabled="Boolean(keyPartNameDisabledReason(part.source))" :reason="keyPartNameDisabledReason(part.source)">
              <input v-model="part.name" class="app-control text-sm" placeholder="Name" :disabled="Boolean(keyPartNameDisabledReason(part.source))" />
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
            <input v-model.number="form.responseStatusCode" type="number" min="400" max="599" class="app-control text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
            Content type
            <input v-model="form.responseContentType" class="app-control text-sm normal-case tracking-normal" />
          </label>
        </div>
        <div class="grid gap-3 rounded-md border border-[#222] bg-[#050505] p-3">
          <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Body source
            <div class="grid grid-cols-2 rounded-md border border-[#333] bg-[#0b0b0b] p-1">
              <button
                type="button"
                class="rounded px-3 py-2 text-sm font-medium normal-case tracking-normal transition"
                :class="form.responseBodyMode === PublicResponseBodyMode.INLINE ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
                @click="form.responseBodyMode = PublicResponseBodyMode.INLINE"
              >
                Inline
              </button>
              <button
                type="button"
                class="rounded px-3 py-2 text-sm font-medium normal-case tracking-normal transition"
                :class="form.responseBodyMode === PublicResponseBodyMode.TEMPLATE ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
                @click="form.responseBodyMode = PublicResponseBodyMode.TEMPLATE"
              >
                Template
              </button>
            </div>
          </div>
          <label v-if="form.responseBodyMode === PublicResponseBodyMode.TEMPLATE" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Template
            <select v-model="form.responseBodyTemplateId" class="app-control text-sm normal-case tracking-normal">
              <option value="">{{ genericTemplates.length ? 'Select template' : 'No generic templates' }}</option>
              <option v-for="template in genericTemplates" :key="template.id.toString()" :value="template.id.toString()">
                {{ template.name }}
              </option>
            </select>
          </label>
          <label v-else class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Body
            <textarea v-model="form.responseBody" class="app-control min-h-24 text-sm normal-case tracking-normal font-mono" />
          </label>
        </div>
        <div class="grid gap-2">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Headers</span>
            <SecondaryButton type="button" size="small" label="Add Header" @click="addResponseHeader" />
          </div>
          <div v-for="(header, index) in form.responseHeaders" :key="index" class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
            <input v-model="header.name" class="app-control text-sm" placeholder="Name" />
            <input v-model="header.value" class="app-control text-sm" placeholder="Value" />
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

@media (max-width: 720px) {
  .row-remove-button {
    width: 100%;
  }
}
</style>
