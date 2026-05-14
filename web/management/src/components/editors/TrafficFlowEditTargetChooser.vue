<script setup lang="ts">
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
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
    case "backend":
      return "Backend";
    case "agent":
      return "Agent";
    case "rate-limit":
      return "Rate limit";
    case "waf":
      return "WAF";
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
        <button
          v-for="target in request?.targets ?? []"
          :key="`${target.kind}:${target.id}`"
          type="button"
          class="grid gap-1 rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-3 text-left transition hover:border-[#666] hover:bg-[#111]"
          @click="selectTarget(target)"
        >
          <span class="text-sm font-medium text-white">{{ target.label }}</span>
          <span class="font-mono text-xs text-[#888]">{{ kindLabel(target.kind) }} #{{ target.id }}{{ target.subLabel ? ` / ${target.subLabel}` : "" }}</span>
        </button>
      </div>
      <div class="flex justify-end">
        <SecondaryButton type="button" label="Cancel" @click="close" />
      </div>
    </div>
  </Modal>
</template>
