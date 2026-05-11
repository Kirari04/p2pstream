<script setup lang="ts">
import { computed, inject, reactive, ref, watch } from "vue";
import type { ComputedRef } from "vue";
import { managementClient } from "@/api/managementClient";
import Button from "@/volt/Button.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import {
  PublicListenerProtocol,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

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
const backends = computed(() => props.config?.backends ?? []);

const listenerForm = reactive({
  id: "",
  name: "",
  bindAddress: "",
  port: 80,
  protocol: PublicListenerProtocol.HTTP,
  enabled: true,
  defaultBackendId: "",
});

function resetForm() {
  listenerForm.id = "";
  listenerForm.name = "";
  listenerForm.bindAddress = "";
  listenerForm.port = 80;
  listenerForm.protocol = PublicListenerProtocol.HTTP;
  listenerForm.enabled = true;
  listenerForm.defaultBackendId = backends.value[0]?.id.toString() ?? "";
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
  listenerForm.defaultBackendId = listener.defaultBackendId.toString();
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
      defaultBackendId: BigInt(listenerForm.defaultBackendId || "0"),
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

watch(backends, () => {
  if (!listenerForm.defaultBackendId && backends.value[0]) {
    listenerForm.defaultBackendId = backends.value[0].id.toString();
  }
}, { immediate: true });

defineExpose({ openCreate, openEdit, close });
</script>

<template>
  <Modal v-model="isOpen" :title="listenerForm.id ? 'Edit Listener' : 'Add Listener'" max-width="36rem">
    <form @submit.prevent="submitListener" class="grid gap-4 sm:grid-cols-2">
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Name
        <input v-model="listenerForm.name" class="vercel-input text-sm normal-case tracking-normal" required />
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Bind address
        <input v-model="listenerForm.bindAddress" class="vercel-input text-sm normal-case tracking-normal" placeholder="0.0.0.0" />
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Port
        <input v-model.number="listenerForm.port" class="vercel-input text-sm normal-case tracking-normal" type="number" min="1" max="65535" required />
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Protocol
        <select v-model="listenerForm.protocol" class="vercel-input text-sm normal-case tracking-normal">
          <option :value="PublicListenerProtocol.HTTP">HTTP</option>
          <option :value="PublicListenerProtocol.HTTPS">HTTPS</option>
        </select>
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
        Default backend
        <select v-model="listenerForm.defaultBackendId" class="vercel-input text-sm normal-case tracking-normal" required>
          <option v-for="backend in backends" :key="backend.id.toString()" :value="backend.id.toString()">{{ backend.name }}</option>
        </select>
      </label>
      <label class="flex items-center gap-2 text-sm text-[#d4d4d8] sm:col-span-2 mt-2">
        <input v-model="listenerForm.enabled" type="checkbox" class="h-4 w-4 accent-white" />
        Enabled
      </label>
      <div class="sm:col-span-2 mt-4 flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="close" />
        <Button class="!bg-white !text-black !border-white" :label="listenerForm.id ? 'Save Changes' : 'Create Listener'" type="submit" :disabled="isBusy || !backends.length" />
      </div>
    </form>
  </Modal>
</template>
