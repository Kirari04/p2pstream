<script setup lang="ts">
import { ref } from "vue";
import AgentEditorModal from "@/components/editors/AgentEditorModal.vue";
import PublicBackendEditorModal from "@/components/editors/PublicBackendEditorModal.vue";
import PublicListenerEditorModal from "@/components/editors/PublicListenerEditorModal.vue";
import PublicCacheRuleEditorModal from "@/components/editors/PublicCacheRuleEditorModal.vue";
import PublicRateLimitRuleEditorModal from "@/components/editors/PublicRateLimitRuleEditorModal.vue";
import PublicRouteEditorModal from "@/components/editors/PublicRouteEditorModal.vue";
import PublicTrafficShaperRuleEditorModal from "@/components/editors/PublicTrafficShaperRuleEditorModal.vue";
import PublicWafCaptchaProviderEditorModal from "@/components/editors/PublicWafCaptchaProviderEditorModal.vue";
import PublicWafRuleEditorModal from "@/components/editors/PublicWafRuleEditorModal.vue";
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
const rateLimitEditor = ref<InstanceType<typeof PublicRateLimitRuleEditorModal> | null>(null);
const cacheRuleEditor = ref<InstanceType<typeof PublicCacheRuleEditorModal> | null>(null);
const trafficShaperEditor = ref<InstanceType<typeof PublicTrafficShaperRuleEditorModal> | null>(null);
const wafRuleEditor = ref<InstanceType<typeof PublicWafRuleEditorModal> | null>(null);
const wafCaptchaProviderEditor = ref<InstanceType<typeof PublicWafCaptchaProviderEditorModal> | null>(null);

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
    case "rate-limit":
      openRateLimitRule(target.id);
      break;
    case "traffic-shaper":
      openTrafficShaperRule(target.id);
      break;
    case "waf":
      openWafRule(target.id);
      break;
    case "cache":
      openCacheRule(target.id);
      break;
  }
}

function openListener(listenerId: bigint | string) {
  listenerEditor.value?.openEdit(listenerId);
}

function openRoute(routeId: bigint | string) {
  routeEditor.value?.openEdit(routeId);
}

function openCloneRoute(routeId: bigint | string) {
  routeEditor.value?.openClone(routeId);
}

function openBackend(backendId: bigint | string) {
  backendEditor.value?.openEdit(backendId);
}

function openCloneBackend(backendId: bigint | string) {
  backendEditor.value?.openClone(backendId);
}

function openAgent(agentId: bigint | string) {
  agentEditor.value?.openEdit(agentId);
}

function openRateLimitRule(ruleId: bigint | string) {
  rateLimitEditor.value?.openEdit(ruleId);
}

function openTrafficShaperRule(ruleId: bigint | string) {
  trafficShaperEditor.value?.openEdit(ruleId);
}

function openWafRule(ruleId: bigint | string) {
  wafRuleEditor.value?.openEdit(ruleId);
}

function openCacheRule(ruleId: bigint | string) {
  cacheRuleEditor.value?.openEdit(ruleId);
}

function openWafCaptchaProvider(providerId: bigint | string) {
  wafCaptchaProviderEditor.value?.openEdit(providerId);
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

function openCreateRateLimitRule() {
  rateLimitEditor.value?.openCreate();
}

function openCreateTrafficShaperRule() {
  trafficShaperEditor.value?.openCreate();
}

function openCreateWafRule() {
  wafRuleEditor.value?.openCreate();
}

function openCreateCacheRule() {
  cacheRuleEditor.value?.openCreate();
}

function openCreateWafCaptchaProvider() {
  wafCaptchaProviderEditor.value?.openCreate();
}

defineExpose({
  openTarget,
  openListener,
  openRoute,
  openCloneRoute,
  openBackend,
  openCloneBackend,
  openAgent,
  openRateLimitRule,
  openTrafficShaperRule,
  openWafRule,
  openCacheRule,
  openWafCaptchaProvider,
  openCreateListener,
  openCreateRoute,
  openCreateBackend,
  openCreateAgent,
  openCreateRateLimitRule,
  openCreateTrafficShaperRule,
  openCreateWafRule,
  openCreateCacheRule,
  openCreateWafCaptchaProvider,
});
</script>

<template>
  <PublicListenerEditorModal ref="listenerEditor" :config="config" @saved="emit('saved')" />
  <PublicBackendEditorModal ref="backendEditor" :config="config" @saved="emit('saved')" />
  <PublicRouteEditorModal ref="routeEditor" :config="config" @saved="emit('saved')" />
  <PublicRateLimitRuleEditorModal ref="rateLimitEditor" :config="config" @saved="emit('saved')" />
  <PublicTrafficShaperRuleEditorModal ref="trafficShaperEditor" :config="config" @saved="emit('saved')" />
  <PublicCacheRuleEditorModal ref="cacheRuleEditor" :config="config" @saved="emit('saved')" />
  <PublicWafRuleEditorModal ref="wafRuleEditor" :config="config" @saved="emit('saved')" />
  <PublicWafCaptchaProviderEditorModal ref="wafCaptchaProviderEditor" :config="config" @saved="emit('saved')" />
  <AgentEditorModal
    ref="agentEditor"
    :config="config"
    :allow-create="allowAgentCreate"
    @saved="emit('saved')"
    @created-agent="emit('created-agent', $event)"
  />
</template>
