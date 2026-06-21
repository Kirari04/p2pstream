<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import { NButton, NCheckbox, NInput, NInputNumber, NModal, NSelect } from "naive-ui";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { modalCardStyle } from "@/lib/naiveUi";
import {
  PublicListenerProtocol,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();


const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));

const isOpen = ref(false);
const listeners = computed(() => props.config?.listeners ?? []);
const listenerSubmitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  const port = listenerForm.port;
  if (port === null || !Number.isInteger(port)) return "Enter a listener port.";
  if (port < 1 || port > 65535) return "Listener port must be between 1 and 65535.";
  return "";
});

const listenerForm = reactive({
  id: "",
  name: "",
  bindAddress: "",
  port: 80 as number | null,
  protocol: PublicListenerProtocol.HTTP,
  enabled: true,
});
const protocolOptions = [
  { label: "HTTP", value: PublicListenerProtocol.HTTP },
  { label: "HTTPS", value: PublicListenerProtocol.HTTPS },
];

function resetForm() {
  listenerForm.id = "";
  listenerForm.name = "";
  listenerForm.bindAddress = "";
  listenerForm.port = 80;
  listenerForm.protocol = PublicListenerProtocol.HTTP;
  listenerForm.enabled = true;
}

function openCreate() {
  resetForm();
  isOpen.value = true;
}

function openEdit(listenerId: bigint | string) {
  const id = listenerId.toString();
  const listener = listeners.value.find((item) => item.id.toString() === id);
  if (!listener) return;
  listenerForm.id = listener.id.toString();
  listenerForm.name = listener.name;
  listenerForm.bindAddress = listener.bindAddress;
  listenerForm.port = Number(listener.port);
  listenerForm.protocol = listener.protocol;
  listenerForm.enabled = listener.enabled;
  isOpen.value = true;
}

function close() {
  isOpen.value = false;
}

async function run(action: () => Promise<void>): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action);
}

async function submitListener() {
  const ok = await run(async () => {
    const port = listenerForm.port;
    if (port === null || !Number.isInteger(port) || port < 1 || port > 65535) {
      throw new Error("Listener port must be between 1 and 65535.");
    }
    const payload = {
      name: listenerForm.name,
      bindAddress: listenerForm.bindAddress,
      port: BigInt(port),
      protocol: listenerForm.protocol,
      enabled: listenerForm.enabled,
    };
    if (listenerForm.id) {
      await managementClient.updatePublicListener({ id: BigInt(listenerForm.id), ...payload });
    } else {
      await managementClient.createPublicListener(payload);
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
    :title="listenerForm.id ? 'Edit Listener' : 'Add Listener'"
    :style="modalCardStyle('36rem')"
    :bordered="false"
    size="huge"
  >
    <form @submit.prevent="submitListener" class="grid max-h-[calc(100vh-9rem)] gap-4 overflow-y-auto pr-1 sm:grid-cols-2">
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
        Name
        <NInput v-model:value="listenerForm.name" size="small" required />
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
        Bind address
        <NInput v-model:value="listenerForm.bindAddress" size="small" placeholder="0.0.0.0" />
        <p class="text-xs font-normal normal-case tracking-normal text-[var(--app-text-muted)]">Leave empty to bind on all interfaces.</p>
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
        Port
        <NInputNumber v-model:value="listenerForm.port" size="small" :min="1" :max="65535" required />
        <p class="text-xs font-normal normal-case tracking-normal text-[var(--app-text-muted)]">Ports below 1024 may require elevated privileges.</p>
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
        Protocol
        <NSelect v-model:value="listenerForm.protocol" size="small" :options="protocolOptions" />
        <p class="text-xs font-normal normal-case tracking-normal text-[var(--app-text-muted)]">Choose HTTPS to enable TLS termination.</p>
      </label>
      <NCheckbox v-model:checked="listenerForm.enabled" class="mt-2 sm:col-span-2">
        Enabled
      </NCheckbox>
      <div class="sm:col-span-2 mt-4 flex justify-end gap-3">
        <NButton secondary @click="close">Cancel</NButton>
        <DisabledHint :disabled="Boolean(listenerSubmitDisabledReason)" :reason="listenerSubmitDisabledReason">
          <NButton
            type="primary"
            attr-type="submit"
            :disabled="Boolean(listenerSubmitDisabledReason)"
          >
            {{ listenerForm.id ? 'Save Changes' : 'Create Listener' }}
          </NButton>
        </DisabledHint>
      </div>
    </form>
  </NModal>
</template>
