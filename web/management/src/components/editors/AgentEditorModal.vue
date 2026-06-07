<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import PlusIcon from "@primevue/icons/plus";
import TrashIcon from "@primevue/icons/trash";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import {
  agentLabelRowsToRecord,
  systemAgentLabelPairs,
  userAgentLabelPairs,
  validateUserAgentLabelRows,
  type AgentLabelPair,
} from "@/lib/agentLabels";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import type { Agent, GetPublicProxyConfigResponse } from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

type Runner = (action: () => Promise<void>) => Promise<boolean>;

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
  allowCreate?: boolean;
}>();

const emit = defineEmits<{
  (event: "created-agent", payload: { agent: Agent | null; token: string }): void;
  (event: "saved"): void;
}>();

const runManagementAction = inject<Runner>("runManagementAction");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const isOpen = ref(false);
const agents = computed(() => props.config?.agents ?? []);
const agentSubmitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  return validateUserAgentLabelRows(agentForm.labels);
});

const agentForm = reactive({
  id: "",
  name: "",
  enabled: true,
  labels: [] as AgentLabelPair[],
  systemLabels: [] as AgentLabelPair[],
});
let nextLabelRowID = 1;

function resetForm() {
  agentForm.id = "";
  agentForm.name = "";
  agentForm.enabled = true;
  agentForm.labels = [];
  agentForm.systemLabels = [];
}

function openCreate() {
  if (!props.allowCreate) return;
  resetForm();
  isOpen.value = true;
}

function openEdit(agentId: bigint | string) {
  const id = agentId.toString();
  const agent = agents.value.find((item) => item.id.toString() === id);
  if (!agent) return;
  agentForm.id = agent.id.toString();
  agentForm.name = agent.name;
  agentForm.enabled = agent.enabled;
  agentForm.labels = userAgentLabelPairs(agent.labels).map(cloneEditableLabelPair);
  agentForm.systemLabels = systemAgentLabelPairs(agent.labels);
  isOpen.value = true;
}

function close() {
  isOpen.value = false;
}

async function run(action: () => Promise<void>): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action);
}

async function submitAgent() {
  let createdPayload: { agent: Agent | null; token: string } | null = null;
  const ok = await run(async () => {
    const labels = agentLabelRowsToRecord(agentForm.labels);
    if (agentForm.id) {
      await managementClient.updateAgent({
        id: BigInt(agentForm.id),
        name: agentForm.name,
        enabled: agentForm.enabled,
        labels,
      });
    } else {
      const resp = await managementClient.createAgent({
        name: agentForm.name,
        enabled: agentForm.enabled,
        labels,
      });
      createdPayload = { agent: resp.agent ?? null, token: resp.token };
    }
  });
  if (ok) {
    isOpen.value = false;
    if (createdPayload) {
      emit("created-agent", createdPayload);
    }
    emit("saved");
  }
}

function addLabel() {
  agentForm.labels.push({
    id: `new:${nextLabelRowID++}`,
    key: "",
    value: "",
    system: false,
  });
}

function removeLabel(index: number) {
  agentForm.labels.splice(index, 1);
}

function cloneEditableLabelPair(label: AgentLabelPair): AgentLabelPair {
  return {
    id: `edit:${nextLabelRowID++}:${label.key}`,
    key: label.key,
    value: label.value,
    system: false,
  };
}

defineExpose({ openCreate, openEdit, close });
</script>

<template>
  <Modal v-model="isOpen" :title="agentForm.id ? 'Edit Agent' : 'Add Agent'" max-width="44rem">
    <form data-testid="agent-editor-form" @submit.prevent="submitAgent" class="grid gap-5">
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Name
        <input v-model="agentForm.name" class="vercel-input text-sm normal-case tracking-normal" required />
      </label>
      <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
        <input v-model="agentForm.enabled" type="checkbox" />
        Enabled
      </label>
      <section data-testid="agent-user-labels" class="grid gap-3 rounded-md border border-[#222] bg-[#050505] p-4">
        <div class="flex items-center justify-between gap-3">
          <div>
            <h4 class="text-sm font-semibold text-white">User Labels</h4>
            <p class="mt-1 text-xs leading-5 text-[#888]">Use labels such as site=home-lab or role=app to select agents from route targets.</p>
          </div>
          <SecondaryButton type="button" size="small" label="Add Label" @click="addLabel">
            <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          </SecondaryButton>
        </div>
        <div v-if="agentForm.labels.length" class="grid gap-2">
          <div v-for="(label, index) in agentForm.labels" :key="label.id" data-testid="agent-label-row" class="grid gap-2 rounded-md border border-[#1f1f1f] bg-[#090909] p-3 sm:grid-cols-[1fr_1fr_auto]">
            <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              Key
              <input v-model="label.key" data-testid="agent-label-key" class="vercel-input text-sm normal-case tracking-normal" placeholder="site" required />
            </label>
            <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              Value
              <input v-model="label.value" data-testid="agent-label-value" class="vercel-input text-sm normal-case tracking-normal" placeholder="home-lab" />
            </label>
            <DangerButton type="button" size="small" aria-label="Remove label" title="Remove label" class="self-end" @click="removeLabel(index)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
        <p v-else class="rounded-md border border-dashed border-[#333] px-3 py-2 text-xs text-[#777]">No user labels configured.</p>
      </section>
      <section v-if="agentForm.systemLabels.length" data-testid="agent-system-labels" class="grid gap-3 rounded-md border border-[#222] bg-[#050505] p-4">
        <div>
          <h4 class="text-sm font-semibold text-white">System Labels</h4>
          <p class="mt-1 text-xs leading-5 text-[#888]">System labels are read-only and can be used for exact-agent target selectors.</p>
        </div>
        <div class="flex flex-wrap gap-2">
          <code v-for="label in agentForm.systemLabels" :key="label.id" data-testid="agent-system-label" class="rounded border border-[#333] bg-[#0b0b0b] px-2 py-1 font-mono text-[11px] text-[#d4d4d8]">
            {{ label.key }}={{ label.value }}
          </code>
        </div>
      </section>
      <div class="mt-4 flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="close" />
        <DisabledHint :disabled="Boolean(agentSubmitDisabledReason)" :reason="agentSubmitDisabledReason">
          <Button :label="agentForm.id ? 'Save Changes' : 'Create Agent'" type="submit" :disabled="Boolean(agentSubmitDisabledReason)" />
        </DisabledHint>
      </div>
    </form>
  </Modal>
</template>
