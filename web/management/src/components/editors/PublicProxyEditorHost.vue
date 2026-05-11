<script setup lang="ts">
import { ref } from "vue";
import AgentEditorModal from "@/components/editors/AgentEditorModal.vue";
import PublicBackendEditorModal from "@/components/editors/PublicBackendEditorModal.vue";
import PublicListenerEditorModal from "@/components/editors/PublicListenerEditorModal.vue";
import PublicRouteEditorModal from "@/components/editors/PublicRouteEditorModal.vue";
import type { TrafficFlowEditTarget } from "@/types/trafficFlowEdit";
import type { Agent, GetPublicProxyConfigResponse } from "@/gen/proto/p2pstream/v1/management_pb";

defineProps<{
  config: GetPublicProxyConfigResponse | null;
  allowAgentCreate?: boolean;
}>();

const emit = defineEmits<{
  (event: "created-agent", payload: { agent: Agent | null; token: string }): void;
  (event: "saved"): void;
}>();

const listenerEditor = ref<InstanceType<typeof PublicListenerEditorModal> | null>(null);
const backendEditor = ref<InstanceType<typeof PublicBackendEditorModal> | null>(null);
const routeEditor = ref<InstanceType<typeof PublicRouteEditorModal> | null>(null);
const agentEditor = ref<InstanceType<typeof AgentEditorModal> | null>(null);

function openTarget(target: TrafficFlowEditTarget) {
  switch (target.kind) {
    case "listener":
      openListener(target.id);
      break;
    case "route":
      openRoute(target.id);
      break;
    case "backend":
      openBackend(target.id);
      break;
    case "agent":
      openAgent(target.id);
      break;
  }
}

function openListener(listenerId: bigint | string) {
  listenerEditor.value?.openEdit(listenerId);
}

function openRoute(routeId: bigint | string) {
  routeEditor.value?.openEdit(routeId);
}

function openBackend(backendId: bigint | string) {
  backendEditor.value?.openEdit(backendId);
}

function openAgent(agentId: bigint | string) {
  agentEditor.value?.openEdit(agentId);
}

function openCreateListener() {
  listenerEditor.value?.openCreate();
}

function openCreateRoute() {
  routeEditor.value?.openCreate();
}

function openCreateBackend() {
  backendEditor.value?.openCreate();
}

function openCreateAgent() {
  agentEditor.value?.openCreate();
}

defineExpose({
  openTarget,
  openListener,
  openRoute,
  openBackend,
  openAgent,
  openCreateListener,
  openCreateRoute,
  openCreateBackend,
  openCreateAgent,
});
</script>

<template>
  <PublicListenerEditorModal ref="listenerEditor" :config="config" @saved="emit('saved')" />
  <PublicBackendEditorModal ref="backendEditor" :config="config" @saved="emit('saved')" />
  <PublicRouteEditorModal ref="routeEditor" :config="config" @saved="emit('saved')" />
  <AgentEditorModal
    ref="agentEditor"
    :config="config"
    :allow-create="allowAgentCreate"
    @saved="emit('saved')"
    @created-agent="emit('created-agent', $event)"
  />
</template>
