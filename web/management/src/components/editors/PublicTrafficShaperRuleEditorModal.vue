<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import { NButton, NButtonGroup, NCheckbox, NInput, NInputNumber, NModal } from "naive-ui";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
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
  PublicRateLimitKeySource,
  PublicTrafficShaperBudgetScope,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

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

const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));

const isOpen = ref(false);
const rules = computed(() => props.config?.trafficShaperRules ?? []);

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

function keyPartNeedsName(source: PublicRateLimitKeySource): boolean {
  return source === PublicRateLimitKeySource.HEADER ||
    source === PublicRateLimitKeySource.COOKIE ||
    source === PublicRateLimitKeySource.QUERY_PARAM;
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
  <NModal
    v-model:show="isOpen"
    preset="card"
    :title="form.id ? 'Edit Traffic Shaper' : 'Add Traffic Shaper'"
    :style="modalCardStyle('60rem')"
    :bordered="false"
    size="huge"
  >
    <form class="grid max-h-[calc(100vh-9rem)] gap-5 overflow-y-auto pr-1" @submit.prevent="submitRule">
      <section class="grid gap-4 sm:grid-cols-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)] sm:col-span-2">
          Name
          <NInput v-model:value="form.name" size="small" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Priority
          <NInputNumber v-model:value="form.priority" size="small" required />
        </label>
        <NCheckbox v-model:checked="form.enabled" class="self-end">
          Enabled
        </NCheckbox>
      </section>

      <section class="grid gap-4">
        <NButtonGroup class="grid grid-cols-2" size="small">
          <NButton
            :type="form.budgetScope === PublicTrafficShaperBudgetScope.PER_KEY ? 'primary' : 'default'"
            @click="form.budgetScope = PublicTrafficShaperBudgetScope.PER_KEY"
          >
            Per key
          </NButton>
          <NButton
            :type="form.budgetScope === PublicTrafficShaperBudgetScope.PER_REQUEST ? 'primary' : 'default'"
            @click="form.budgetScope = PublicTrafficShaperBudgetScope.PER_REQUEST"
          >
            Per request
          </NButton>
        </NButtonGroup>

        <div class="grid gap-4 sm:grid-cols-5">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
            Upload KiB/s
            <NInputNumber v-model:value="form.uploadKibPerSecond" size="small" :min="0" :step="1" />
            <p class="text-xs font-normal normal-case tracking-normal text-[var(--app-text-muted)]">Client-to-server bandwidth cap.</p>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
            Download KiB/s
            <NInputNumber v-model:value="form.downloadKibPerSecond" size="small" :min="0" :step="1" />
            <p class="text-xs font-normal normal-case tracking-normal text-[var(--app-text-muted)]">Server-to-client bandwidth cap.</p>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
            Burst KiB
            <NInputNumber v-model:value="form.burstKib" size="small" :min="0" :step="1" />
            <p class="text-xs font-normal normal-case tracking-normal text-[var(--app-text-muted)]">Extra data allowed in a burst before throttling.</p>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
            Request free KiB
            <NInputNumber v-model:value="form.requestFreeKib" size="small" :min="0" :step="1" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
            Response free KiB
            <NInputNumber v-model:value="form.responseFreeKib" size="small" :min="0" :step="1" />
          </label>
        </div>
        <p class="rounded-md border border-[var(--app-border)] bg-[var(--app-panel-muted)] px-3 py-2 text-xs text-[var(--app-text-muted)]">
          A value of 0 leaves that direction unlimited. Free KiB are sent without delay and do not consume the shaper budget.
        </p>
      </section>

      <PublicPolicyMatchEditor :form="form.match" />

      <PublicPolicyKeyPartsEditor :key-parts="form.keyParts" :disabled-reason="keyPartsDisabledReason" />

      <div class="mt-2 flex justify-end gap-3">
        <NButton secondary @click="close">Cancel</NButton>
        <DisabledHint :disabled="submitDisabled" :reason="shaperSubmitDisabledReason">
          <NButton type="primary" attr-type="submit" :disabled="submitDisabled">
            {{ form.id ? 'Save Changes' : 'Create Shaper' }}
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
