<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import { managementClient } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import type { Agent, GetPublicProxyConfigResponse } from "@/gen/proto/p2pstream/v1/management_pb";

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
const agentSubmitDisabledReason = computed(() => isBusy?.value ? BUSY_REASON : "");

const agentForm = reactive({
  id: "",
  name: "",
  enabled: true,
});

function resetForm() {
  agentForm.id = "";
  agentForm.name = "";
  agentForm.enabled = true;
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
    if (agentForm.id) {
      await managementClient.updateAgent({
        id: BigInt(agentForm.id),
        name: agentForm.name,
        enabled: agentForm.enabled,
      });
    } else {
      const resp = await managementClient.createAgent({
        name: agentForm.name,
        enabled: agentForm.enabled,
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

defineExpose({ openCreate, openEdit, close });
</script>

<template>
  <Modal v-model="isOpen" :title="agentForm.id ? 'Edit Agent' : 'Add Agent'" max-width="32rem">
    <form @submit.prevent="submitAgent" class="grid gap-4">
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Name
        <input v-model="agentForm.name" class="vercel-input text-sm normal-case tracking-normal" required />
      </label>
      <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
        <input v-model="agentForm.enabled" type="checkbox" />
        Enabled
      </label>
      <div class="mt-4 flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="close" />
        <DisabledHint :disabled="Boolean(agentSubmitDisabledReason)" :reason="agentSubmitDisabledReason">
          <Button :label="agentForm.id ? 'Save Changes' : 'Create Agent'" type="submit" :disabled="Boolean(agentSubmitDisabledReason)" />
        </DisabledHint>
      </div>
    </form>
  </Modal>
</template>
