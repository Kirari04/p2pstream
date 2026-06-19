<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NButtonGroup, NCheckbox, NInput, NInputNumber, NModal, NSelect } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicRateLimitPreview from "@/components/editors/PublicRateLimitPreview.vue";
import PublicPolicyMatchEditor from "@/components/editors/PublicPolicyMatchEditor.vue";
import PublicPolicyKeyPartsEditor from "@/components/editors/PublicPolicyKeyPartsEditor.vue";
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
const genericTemplateOptions = computed(() =>
  genericTemplates.value.map((template) => ({
    label: template.name,
    value: template.id.toString(),
  })),
);

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

function keyPartNeedsName(source: PublicRateLimitKeySource): boolean {
  return source === PublicRateLimitKeySource.HEADER ||
    source === PublicRateLimitKeySource.COOKIE ||
    source === PublicRateLimitKeySource.QUERY_PARAM;
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
  <NModal
    v-model:show="isOpen"
    preset="card"
    :title="form.id ? 'Edit Rate Limit' : 'Add Rate Limit'"
    :style="modalCardStyle('60rem')"
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

      <section class="grid gap-4">
        <NButtonGroup class="grid grid-cols-2 sm:grid-cols-4" size="small">
          <NButton
            v-for="option in algorithmOptions"
            :key="option.value"
            :type="form.algorithm === option.value ? 'primary' : 'default'"
            @click="form.algorithm = option.value"
          >
            {{ option.label }}
          </NButton>
        </NButtonGroup>
        <div class="grid gap-4 sm:grid-cols-3">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Limit
            <NInputNumber v-model:value="form.limit" size="small" :min="1" required />
            <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Max requests allowed per window.</p>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Window seconds
            <NInputNumber v-model:value="form.windowSeconds" size="small" :min="1" :step="1" required />
            <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Duration of each rate limit window.</p>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Burst
            <DisabledHint full-width :disabled="Boolean(burstDisabledReason)" :reason="burstDisabledReason">
              <NInputNumber
                v-model:value="form.burst"
                size="small"
                :min="0"
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

      <PublicPolicyKeyPartsEditor :key-parts="form.keyParts" />

      <section class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <h4 class="text-sm font-semibold text-white">Denied response</h4>
        <div class="grid gap-4 sm:grid-cols-3">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Status
            <NInputNumber v-model:value="form.responseStatusCode" size="small" :min="400" :max="599" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
            Content type
            <NInput v-model:value="form.responseContentType" size="small" />
          </label>
        </div>
        <div class="grid gap-3 rounded-md border border-[#222] bg-[#050505] p-3">
          <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Body source
            <NButtonGroup class="grid grid-cols-2" size="small">
              <NButton
                :type="form.responseBodyMode === PublicResponseBodyMode.INLINE ? 'primary' : 'default'"
                @click="form.responseBodyMode = PublicResponseBodyMode.INLINE"
              >
                Inline
              </NButton>
              <NButton
                :type="form.responseBodyMode === PublicResponseBodyMode.TEMPLATE ? 'primary' : 'default'"
                @click="form.responseBodyMode = PublicResponseBodyMode.TEMPLATE"
              >
                Template
              </NButton>
            </NButtonGroup>
          </div>
          <label v-if="form.responseBodyMode === PublicResponseBodyMode.TEMPLATE" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Template
            <NSelect
              v-model:value="form.responseBodyTemplateId"
              size="small"
              :options="genericTemplateOptions"
              :placeholder="genericTemplates.length ? 'Select template' : 'No generic templates'"
              :disabled="!genericTemplates.length"
            />
          </label>
          <label v-else class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Body
            <NInput v-model:value="form.responseBody" type="textarea" class="font-mono" :autosize="{ minRows: 4, maxRows: 8 }" />
          </label>
        </div>
        <div class="grid gap-2">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Headers</span>
            <NButton secondary size="small" @click="addResponseHeader">Add Header</NButton>
          </div>
          <div v-for="(header, index) in form.responseHeaders" :key="index" class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
            <NInput v-model:value="header.name" size="small" placeholder="Name" />
            <NInput v-model:value="header.value" size="small" placeholder="Value" />
            <NButton
              type="error"
              size="small"
              class="row-remove-button"
              aria-label="Remove response header"
              title="Remove response header"
              @click="removeResponseHeader(index)"
            >
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </NButton>
          </div>
        </div>
      </section>

      <div class="flex justify-end gap-3">
        <NButton secondary @click="close">Cancel</NButton>
        <DisabledHint :disabled="submitDisabled" :reason="rateLimitSubmitDisabledReason">
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

@media (max-width: 720px) {
  .row-remove-button {
    width: 100%;
  }
}
</style>
