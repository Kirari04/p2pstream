<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import TrashIcon from "@primevue/icons/trash";
import { managementClient } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicPolicyMatchEditor from "@/components/editors/PublicPolicyMatchEditor.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import {
  defaultPolicyMatchForm,
  policyMatchFormFromProto,
  policyMatchRulePayload,
  policyMatchValidationReason,
  type PolicyMatchForm,
} from "@/lib/publicPolicyMatch";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import {
  PublicRateLimitKeySource,
  PublicTrafficShaperBudgetScope,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

type Runner = (action: () => Promise<void>) => Promise<boolean>;
type KeyPartForm = {
  source: PublicRateLimitKeySource;
  name: string;
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
const rules = computed(() => props.config?.trafficShaperRules ?? []);
const matchEditor = ref<InstanceType<typeof PublicPolicyMatchEditor> | null>(null);

const form = reactive({
  id: "",
  name: "",
  enabled: true,
  priority: 100,
  budgetScope: PublicTrafficShaperBudgetScope.PER_KEY,
  uploadKibPerSecond: 0,
  downloadKibPerSecond: 1024,
  burstKib: 0,
  requestFreeKib: 0,
  responseFreeKib: 64,
  match: defaultPolicyMatchForm() as PolicyMatchForm,
  keyParts: [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }] as KeyPartForm[],
});

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

const keyPartsDisabledReason = computed(() =>
  form.budgetScope === PublicTrafficShaperBudgetScope.PER_REQUEST
    ? "Per-request shaping gives every request its own bandwidth budget, so key parts are not used."
    : "",
);
const shaperSubmitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!form.name.trim()) return "Enter a rule name.";
  if (form.uploadKibPerSecond < 0 || form.downloadKibPerSecond < 0) return "Bandwidth values cannot be negative.";
  if (form.uploadKibPerSecond <= 0 && form.downloadKibPerSecond <= 0) return "Set upload or download bandwidth.";
  if (form.burstKib < 0) return "Burst cannot be negative.";
  if (form.requestFreeKib < 0 || form.responseFreeKib < 0) return "Free KiB values cannot be negative.";
  if (policyMatchValidationReason(form.match)) return policyMatchValidationReason(form.match);
  if (form.budgetScope === PublicTrafficShaperBudgetScope.PER_KEY && !form.keyParts.length) return "Add at least one key part.";
  return "";
});
const submitDisabled = computed(() => Boolean(shaperSubmitDisabledReason.value));

function resetForm() {
  form.id = "";
  form.name = nextRuleName();
  form.enabled = true;
  form.priority = 100;
  form.budgetScope = PublicTrafficShaperBudgetScope.PER_KEY;
  form.uploadKibPerSecond = 0;
  form.downloadKibPerSecond = 1024;
  form.burstKib = 0;
  form.requestFreeKib = 0;
  form.responseFreeKib = 64;
  form.match = defaultPolicyMatchForm();
  form.keyParts = [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
}

function nextRuleName(): string {
  const existing = new Set(rules.value.map((rule) => rule.name));
  if (!existing.has("traffic-shaper")) return "traffic-shaper";
  let index = 2;
  while (existing.has(`traffic-shaper-${index}`)) index += 1;
  return `traffic-shaper-${index}`;
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
  form.budgetScope = rule.budgetScope || PublicTrafficShaperBudgetScope.PER_KEY;
  form.uploadKibPerSecond = bytesToKib(rule.uploadBytesPerSecond);
  form.downloadKibPerSecond = bytesToKib(rule.downloadBytesPerSecond);
  form.burstKib = bytesToKib(rule.burstBytes);
  form.requestFreeKib = bytesToKib(rule.requestExemptBytes);
  form.responseFreeKib = bytesToKib(rule.responseExemptBytes);
  form.match = policyMatchFormFromProto(rule.matchRule);
  form.keyParts = rule.keyParts.length
    ? rule.keyParts.map((part) => ({ source: part.source, name: part.name }))
    : [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  isOpen.value = true;
  requestAnimationFrame(() => matchEditor.value?.validationReason());
}

function close() {
  isOpen.value = false;
}

function bytesToKib(value: bigint): number {
  return Math.round(Number(value || 0n) / 1024);
}

function kibToBytes(value: number): bigint {
  return BigInt(Math.round((value || 0) * 1024));
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
  if (keyPartsDisabledReason.value) return keyPartsDisabledReason.value;
  return keyPartNeedsName(source) ? "" : "This key source does not need a name.";
}

function removeKeyPartDisabledReason(): string {
  if (keyPartsDisabledReason.value) return keyPartsDisabledReason.value;
  return form.keyParts.length <= 1 ? "At least one key part is required." : "";
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
      budgetScope: form.budgetScope,
      uploadBytesPerSecond: kibToBytes(form.uploadKibPerSecond),
      downloadBytesPerSecond: kibToBytes(form.downloadKibPerSecond),
      burstBytes: kibToBytes(form.burstKib),
      requestExemptBytes: kibToBytes(form.requestFreeKib),
      responseExemptBytes: kibToBytes(form.responseFreeKib),
      matchRule: policyMatchRulePayload(form.match),
      keyParts: form.budgetScope === PublicTrafficShaperBudgetScope.PER_KEY
        ? form.keyParts.map((part) => ({
          source: part.source,
          name: keyPartNeedsName(part.source) ? part.name.trim() : "",
        }))
        : [],
    };
    if (form.id) {
      await managementClient.updatePublicTrafficShaperRule({ id: BigInt(form.id), ...payload });
    } else {
      await managementClient.createPublicTrafficShaperRule(payload);
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
  <Modal v-model="isOpen" :title="form.id ? 'Edit Traffic Shaper' : 'Add Traffic Shaper'" max-width="60rem">
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
        <div class="grid grid-cols-2 overflow-hidden rounded-md border border-[#333] bg-[#0b0b0b] p-1">
          <button
            type="button"
            class="rounded px-3 py-2 text-sm font-medium transition"
            :class="form.budgetScope === PublicTrafficShaperBudgetScope.PER_KEY ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
            @click="form.budgetScope = PublicTrafficShaperBudgetScope.PER_KEY"
          >
            Per key
          </button>
          <button
            type="button"
            class="rounded px-3 py-2 text-sm font-medium transition"
            :class="form.budgetScope === PublicTrafficShaperBudgetScope.PER_REQUEST ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
            @click="form.budgetScope = PublicTrafficShaperBudgetScope.PER_REQUEST"
          >
            Per request
          </button>
        </div>

        <div class="grid gap-4 sm:grid-cols-5">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Upload KiB/s
            <input v-model.number="form.uploadKibPerSecond" type="number" min="0" step="1" class="vercel-input text-sm normal-case tracking-normal" />
            <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Client-to-server bandwidth cap.</p>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Download KiB/s
            <input v-model.number="form.downloadKibPerSecond" type="number" min="0" step="1" class="vercel-input text-sm normal-case tracking-normal" />
            <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Server-to-client bandwidth cap.</p>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Burst KiB
            <input v-model.number="form.burstKib" type="number" min="0" step="1" class="vercel-input text-sm normal-case tracking-normal" />
            <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Extra data allowed in a burst before throttling.</p>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Request free KiB
            <input v-model.number="form.requestFreeKib" type="number" min="0" step="1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Response free KiB
            <input v-model.number="form.responseFreeKib" type="number" min="0" step="1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
        </div>
        <p class="rounded-md border border-[#222] bg-[#050505] px-3 py-2 text-xs text-[#888]">
          A value of 0 leaves that direction unlimited. Free KiB are sent without delay and do not consume the shaper budget.
        </p>
      </section>

      <PublicPolicyMatchEditor ref="matchEditor" :form="form.match" />

      <section class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <div class="flex items-center justify-between gap-3">
          <h4 class="text-sm font-semibold text-white">Key parts</h4>
          <DisabledHint :disabled="Boolean(keyPartsDisabledReason)" :reason="keyPartsDisabledReason">
            <SecondaryButton type="button" size="small" label="Add Key" :disabled="Boolean(keyPartsDisabledReason)" @click="addKeyPart" />
          </DisabledHint>
        </div>
        <div class="grid gap-2">
          <div v-for="(part, index) in form.keyParts" :key="index" class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
            <DisabledHint full-width :disabled="Boolean(keyPartsDisabledReason)" :reason="keyPartsDisabledReason">
              <select v-model="part.source" class="vercel-input text-sm" :disabled="Boolean(keyPartsDisabledReason)">
                <option v-for="option in keySourceOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
              </select>
            </DisabledHint>
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

      <div class="mt-2 flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="close" />
        <DisabledHint :disabled="submitDisabled" :reason="shaperSubmitDisabledReason">
          <Button type="submit" :label="form.id ? 'Save Changes' : 'Create Shaper'" :disabled="submitDisabled" />
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
  letter-spacing: normal;
}

.matcher-add-button,
.matcher-empty button {
  display: inline-flex;
  height: 2rem;
  align-items: center;
  gap: 0.4rem;
  border: 1px solid #333;
  border-radius: 5px;
  background: #050505;
  color: #d4d4d8;
  padding: 0 0.65rem;
  font-size: 0.72rem;
  font-weight: 650;
  transition: border-color 140ms ease, color 140ms ease, background 140ms ease;
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

.row-remove-button {
  width: 2.25rem;
  height: 2.25rem;
  padding: 0 !important;
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
