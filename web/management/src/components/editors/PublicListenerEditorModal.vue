<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/components/ui/Button.vue";
import Modal from "@/components/ui/Modal.vue";
import SecondaryButton from "@/components/ui/SecondaryButton.vue";
import {
  PublicListenerProtocol,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

type Runner = (action: () => Promise<void>) => Promise<boolean>;

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject<Runner>("runManagementAction");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const isOpen = ref(false);
const listeners = computed(() => props.config?.listeners ?? []);
const listenerSubmitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  return "";
});

const listenerForm = reactive({
  id: "",
  name: "",
  bindAddress: "",
  port: 80,
  protocol: PublicListenerProtocol.HTTP,
  enabled: true,
});

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
    const payload = {
      name: listenerForm.name,
      bindAddress: listenerForm.bindAddress,
      port: BigInt(listenerForm.port),
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
  <Modal v-model="isOpen" :title="listenerForm.id ? 'Edit Listener' : 'Add Listener'" max-width="36rem">
    <form @submit.prevent="submitListener" class="grid gap-4 sm:grid-cols-2">
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Name
        <input v-model="listenerForm.name" class="app-control text-sm normal-case tracking-normal" required />
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Bind address
        <input v-model="listenerForm.bindAddress" class="app-control text-sm normal-case tracking-normal" placeholder="0.0.0.0" />
        <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Leave empty to bind on all interfaces.</p>
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Port
        <input v-model.number="listenerForm.port" class="app-control text-sm normal-case tracking-normal" type="number" min="1" max="65535" required />
        <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Ports below 1024 may require elevated privileges.</p>
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Protocol
        <select v-model="listenerForm.protocol" class="app-control text-sm normal-case tracking-normal">
          <option :value="PublicListenerProtocol.HTTP">HTTP</option>
          <option :value="PublicListenerProtocol.HTTPS">HTTPS</option>
        </select>
        <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Choose HTTPS to enable TLS termination.</p>
      </label>
      <label class="flex items-center gap-2 text-sm text-[#d4d4d8] sm:col-span-2 mt-2">
        <input v-model="listenerForm.enabled" type="checkbox" />
        Enabled
      </label>
      <div class="sm:col-span-2 mt-4 flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="close" />
        <DisabledHint :disabled="Boolean(listenerSubmitDisabledReason)" :reason="listenerSubmitDisabledReason">
          <Button
                       :label="listenerForm.id ? 'Save Changes' : 'Create Listener'"
            type="submit"
            :disabled="Boolean(listenerSubmitDisabledReason)"
          />
        </DisabledHint>
      </div>
    </form>
  </Modal>
</template>
