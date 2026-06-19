<script setup lang="ts">
import { NButton } from "naive-ui";
import Modal from "@/components/ui/Modal.vue";
import SecondaryButton from "@/components/ui/SecondaryButton.vue";
import type { TrafficFlowEditRequest, TrafficFlowEditTarget } from "@/types/trafficFlowEdit";

defineProps<{
  modelValue: boolean;
  request: TrafficFlowEditRequest | null;
}>();

const emit = defineEmits<{
  (event: "update:modelValue", value: boolean): void;
  (event: "select", target: TrafficFlowEditTarget): void;
}>();

function close() {
  emit("update:modelValue", false);
}

function selectTarget(target: TrafficFlowEditTarget) {
  emit("select", target);
  close();
}

function kindLabel(kind: TrafficFlowEditTarget["kind"]): string {
  switch (kind) {
    case "listener":
      return "Listener";
    case "route":
      return "Route";
    case "target":
      return "Target";
    case "agent":
      return "Agent";
    case "rate-limit":
      return "Rate limit";
    case "waf":
      return "WAF";
    case "cache":
      return "Cache";
    case "traffic-shaper":
      return "Traffic shaper";
    default:
      return "Settings";
  }
}
</script>

<template>
  <Modal :model-value="modelValue" :title="request ? `Edit ${request.nodeLabel}` : 'Edit Settings'" max-width="34rem" @update:model-value="emit('update:modelValue', $event)">
    <div class="grid gap-4">
      <div class="grid gap-2">
        <NButton
          v-for="target in request?.targets ?? []"
          :key="`${target.kind}:${target.id}`"
          secondary
          class="target-choice"
          @click="selectTarget(target)"
        >
          <span class="text-sm font-medium text-white">{{ target.label }}</span>
          <span class="font-mono text-xs text-[#888]">{{ kindLabel(target.kind) }} #{{ target.id }}{{ target.subLabel ? ` / ${target.subLabel}` : "" }}</span>
        </NButton>
      </div>
      <div class="flex justify-end">
        <SecondaryButton type="button" label="Cancel" @click="close" />
      </div>
    </div>
  </Modal>
</template>

<style scoped>
.target-choice {
  --n-height: auto !important;
  justify-content: flex-start;
  padding: 0.75rem;
  text-align: left;
}

.target-choice :deep(.n-button__content) {
  display: grid;
  gap: 0.25rem;
  justify-items: start;
}
</style>
