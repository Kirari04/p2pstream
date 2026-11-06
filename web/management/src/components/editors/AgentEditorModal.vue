<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NCheckbox, NDrawer, NDrawerContent, NInput, NTag } from "naive-ui";
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
import { diagnosticInspectionText } from "@/lib/diagnosticText";
import { editorDrawerWidth } from "@/lib/naiveUi";
import type { GetPublicProxyConfigResponse } from "@/gen/proto/p2pstream/v1/management_pb";
import type { CreatedAgentSetup } from "@/types/agentSetup";

const managementClient = useManagementClient();


const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
  allowCreate?: boolean;
}>();

const emit = defineEmits<{
  (event: "created-agent", payload: CreatedAgentSetup): void;
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
  let createdPayload: CreatedAgentSetup | null = null;
  const ok = await run(async () => {
    const labels = agentLabelRowsToRecord(agentForm.labels);
    if (agentForm.id) {
      await managementClient.updateAgent({
        id: BigInt(agentForm.id),
        name: agentForm.name,
        enabled: agentForm.enabled,
        labels,
        replaceLabels: true,
      });
    } else {
      const resp = await managementClient.createAgent({
        name: agentForm.name,
        enabled: agentForm.enabled,
        labels,
      });
      createdPayload = {
        agent: resp.agent ?? null,
        token: resp.token,
        updaterEnrollmentToken: resp.updaterEnrollmentToken,
        updaterEnrollmentExpiresAtUnixMillis: resp.updaterEnrollmentExpiresAtUnixMillis,
        updaterPinnedRepository: resp.updaterPinnedRepository,
        updaterManagementAuthorityPublicKeyBase64: bytesToBase64(resp.updaterManagementAuthority?.publicKey ?? new Uint8Array()),
        updaterManagementAuthorityKeyId: resp.updaterManagementAuthority?.keyId ?? "",
        updaterManagementAuthorityEpoch: resp.updaterManagementAuthority?.epoch ?? 0n,
      };
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

function bytesToBase64(value: Uint8Array): string {
  let binary = "";
  for (const byte of value) binary += String.fromCharCode(byte);
  return btoa(binary);
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
  <NDrawer
    v-model:show="isOpen"
    placement="right"
    :width="editorDrawerWidth('44rem')"
    :aria-label="agentForm.id ? 'Edit agent' : 'Add agent'"
    class="editor-drawer"
  >
    <NDrawerContent :title="agentForm.id ? 'Edit agent' : 'Add agent'" closable>
    <form data-testid="agent-editor-form" @submit.prevent="submitAgent" class="editor-drawer-form agent-editor">
      <label class="agent-editor__field">
        <span class="agent-editor__label">Name</span>
        <NInput v-model:value="agentForm.name" size="small" required />
      </label>
      <NCheckbox v-model:checked="agentForm.enabled">
        Enabled
      </NCheckbox>
      <section data-testid="agent-user-labels" class="agent-editor__section">
        <div class="agent-editor__section-header">
          <div>
            <h4 class="agent-editor__heading">User labels</h4>
            <p class="agent-editor__description">Use labels such as site=home-lab or role=app to select agents from route targets.</p>
          </div>
          <NButton secondary size="small" @click="addLabel">
            <template #icon><PlusIcon class="icon-sm" /></template>
            Add label
          </NButton>
        </div>
        <div v-if="agentForm.labels.length" class="agent-editor__label-rows">
          <div v-for="(label, index) in agentForm.labels" :key="label.id" data-testid="agent-label-row" class="agent-editor__label-row">
            <label class="agent-editor__field">
              <span class="agent-editor__label">Key</span>
              <NInput v-model:value="label.key" data-testid="agent-label-key" size="small" placeholder="site" required />
            </label>
            <label class="agent-editor__field">
              <span class="agent-editor__label">Value</span>
              <NInput v-model:value="label.value" data-testid="agent-label-value" size="small" placeholder="home-lab" />
            </label>
            <NButton type="error" secondary size="small" aria-label="Remove label" title="Remove label" class="agent-editor__remove" @click="removeLabel(index)">
              <template #icon><TrashIcon class="icon-sm" /></template>
              Remove
            </NButton>
          </div>
        </div>
        <p v-else class="agent-editor__empty">No user labels configured</p>
      </section>
      <section v-if="agentForm.systemLabels.length" data-testid="agent-system-labels" class="agent-editor__section">
        <div>
          <h4 class="agent-editor__heading">System labels</h4>
          <p class="agent-editor__description">System labels are read-only and can be used for exact-agent target selectors.</p>
        </div>
        <div class="agent-editor__tags">
          <NTag v-for="label in agentForm.systemLabels" :key="label.id" data-testid="agent-system-label" size="small" :bordered="false" class="agent-editor__tag mono-text">
            <bdi dir="ltr">{{ diagnosticInspectionText(label.key) }}</bdi>=<bdi dir="ltr">{{ diagnosticInspectionText(label.value) }}</bdi>
          </NTag>
        </div>
      </section>
      <div class="editor-drawer-actions margin-top-lg layout-row align-end-row space-md">
        <NButton secondary @click="close">Cancel</NButton>
        <DisabledHint :disabled="Boolean(agentSubmitDisabledReason)" :reason="agentSubmitDisabledReason">
          <NButton type="primary" attr-type="submit" :disabled="Boolean(agentSubmitDisabledReason)">
            {{ agentForm.id ? 'Save changes' : 'Create agent' }}
          </NButton>
        </DisabledHint>
      </div>
    </form>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.agent-editor {
  display: grid;
  gap: 1.25rem;
}

.agent-editor__field {
  display: grid;
  min-width: 0;
  gap: 0.375rem;
}

.agent-editor__label {
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  font-weight: 500;
}

.agent-editor__section {
  display: grid;
  gap: 0.875rem;
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 1.25rem;
}

.agent-editor__section-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
}

.agent-editor__heading {
  color: var(--app-text);
  font-size: 0.9375rem;
  font-weight: 600;
}

.agent-editor__description {
  margin-top: 0.25rem;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  line-height: 1.5;
}

.agent-editor__label-rows {
  display: grid;
  gap: 0.75rem;
}

.agent-editor__label-row {
  display: grid;
  gap: 0.75rem;
}

.agent-editor__remove {
  justify-self: end;
}

.agent-editor__empty {
  border-radius: 8px;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  padding: 0.875rem 1rem;
  text-align: center;
}

.agent-editor__tags {
  display: flex;
  flex-wrap: wrap;
  gap: 0.375rem;
}

.agent-editor__tag {
  max-width: 100%;
  height: auto;
  white-space: normal;
  overflow-wrap: anywhere;
}

@media (min-width: 640px) {
  .agent-editor__label-row {
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto;
    align-items: end;
  }

  .agent-editor__label-row + .agent-editor__label-row .agent-editor__label {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    clip-path: inset(50%);
    white-space: nowrap;
  }
}

@media (max-width: 639px) {
  .agent-editor__section-header {
    flex-direction: column;
  }

  .agent-editor__label-row + .agent-editor__label-row {
    border-top: 1px solid var(--app-border-subtle);
    padding-top: 0.75rem;
  }
}
</style>
