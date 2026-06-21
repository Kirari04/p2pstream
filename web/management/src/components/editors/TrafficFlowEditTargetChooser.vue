<script setup lang="ts">
import { NButton, NModal } from "naive-ui";
import { modalCardStyle } from "@/lib/naiveUi";
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
  <NModal
    :show="modelValue"
    preset="card"
    :title="request ? `Edit ${request.nodeLabel}` : 'Edit Settings'"
    :style="modalCardStyle('34rem')"
    :bordered="false"
    @update:show="emit('update:modelValue', $event)"
  >
    <div class="layout-grid space-lg">
      <div class="layout-grid space-sm">
        <NButton
          v-for="target in request?.targets ?? []"
          :key="`${target.kind}:${target.id}`"
          secondary
          class="target-choice"
          @click="selectTarget(target)"
        >
          <span class="copy-sm weight-medium base-text">{{ target.label }}</span>
          <span class="mono-text copy-xs muted-text">{{ kindLabel(target.kind) }} #{{ target.id }}{{ target.subLabel ? ` / ${target.subLabel}` : "" }}</span>
        </NButton>
      </div>
      <div class="layout-row align-end-row">
        <NButton secondary attr-type="button" @click="close">Cancel</NButton>
      </div>
    </div>
  </NModal>
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
