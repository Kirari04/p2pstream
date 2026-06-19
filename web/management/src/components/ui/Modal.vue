<script setup lang="ts">
import { computed } from "vue";
import { NModal } from "naive-ui";

const props = defineProps<{
  modelValue: boolean;
  title: string;
  maxWidth?: string;
}>();

const emit = defineEmits<{
  (e: "update:modelValue", value: boolean): void;
}>();

const cardStyle = computed(() => ({
  width: "min(calc(100vw - 2rem), " + (props.maxWidth || "42rem") + ")",
  maxHeight: "calc(100vh - 3rem)",
  overflow: "hidden",
}));
</script>

<template>
  <NModal
    :show="modelValue"
    preset="card"
    :title="title"
    :style="cardStyle"
    :bordered="false"
    size="huge"
    @update:show="emit('update:modelValue', $event)"
  >
    <div class="max-h-[calc(100vh-9rem)] overflow-y-auto pr-1">
      <slot />
    </div>
  </NModal>
</template>
