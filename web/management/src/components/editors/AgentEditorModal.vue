<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NCheckbox, NEmpty, NInput, NModal, NTag } from "naive-ui";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
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
import { modalCardStyle } from "@/lib/naiveUi";
import type { Agent, GetPublicProxyConfigResponse } from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();


const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
  allowCreate?: boolean;
}>();

const emit = defineEmits<{
  (event: "created-agent", payload: { agent: Agent | null; token: string }): void;
  (event: "saved"): void;
}>();

const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));

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
  <NModal
    v-model:show="isOpen"
    preset="card"
    :title="agentForm.id ? 'Edit Agent' : 'Add Agent'"
    :style="modalCardStyle('44rem')"
    :bordered="false"
    size="huge"
  >
    <form data-testid="agent-editor-form" @submit.prevent="submitAgent" class="layout-grid max-modal-height space-xl scroll-y pad-right-xs">
      <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
        Name
        <NInput v-model:value="agentForm.name" size="small" required />
      </label>
      <NCheckbox v-model:checked="agentForm.enabled">
        Enabled
      </NCheckbox>
      <section data-testid="agent-user-labels" class="layout-grid space-md round-lg framed frame-standard muted-bg pad-lg">
        <div class="layout-row align-center spread-items space-md">
          <div>
            <h4 class="copy-sm weight-semibold base-text">User Labels</h4>
            <p class="margin-top-xs copy-xs line-normal muted-text">Use labels such as site=home-lab or role=app to select agents from route targets.</p>
          </div>
          <NButton secondary size="small" @click="addLabel">
            <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
            Add Label
          </NButton>
        </div>
        <div v-if="agentForm.labels.length" class="layout-grid space-sm">
          <div v-for="(label, index) in agentForm.labels" :key="label.id" data-testid="agent-label-row" class="layout-grid space-md round-md framed frame-subtle panel-bg pad-md mq-sm-two-auto mq-sm-align-end">
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Key
              <NInput v-model:value="label.key" data-testid="agent-label-key" size="small" placeholder="site" required />
            </label>
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Value
              <NInput v-model:value="label.value" data-testid="agent-label-value" size="small" placeholder="home-lab" />
            </label>
            <NButton type="error" secondary size="small" aria-label="Remove label" title="Remove label" class="self-align-end" @click="removeLabel(index)">
              <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
            </NButton>
          </div>
        </div>
        <NEmpty v-else size="small" description="No user labels configured" class="round-md framed dashed-border frame-standard panel-bg pad-x-md pad-y-md" />
      </section>
      <section v-if="agentForm.systemLabels.length" data-testid="agent-system-labels" class="layout-grid space-md round-lg framed frame-standard muted-bg pad-lg">
        <div>
          <h4 class="copy-sm weight-semibold base-text">System Labels</h4>
          <p class="margin-top-xs copy-xs line-normal muted-text">System labels are read-only and can be used for exact-agent target selectors.</p>
        </div>
        <div class="layout-row wrap-items space-sm">
          <NTag v-for="label in agentForm.systemLabels" :key="label.id" data-testid="agent-system-label" size="small" :bordered="true" class="mono-text">
            {{ label.key }}={{ label.value }}
          </NTag>
        </div>
      </section>
      <div class="margin-top-lg layout-row align-end-row space-md">
        <NButton secondary @click="close">Cancel</NButton>
        <DisabledHint :disabled="Boolean(agentSubmitDisabledReason)" :reason="agentSubmitDisabledReason">
          <NButton type="primary" attr-type="submit" :disabled="Boolean(agentSubmitDisabledReason)">
            {{ agentForm.id ? 'Save Changes' : 'Create Agent' }}
          </NButton>
        </DisabledHint>
      </div>
    </form>
  </NModal>
</template>
