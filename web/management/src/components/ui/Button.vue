<script setup lang="ts">
import { computed } from "vue";
import { NButton } from "naive-ui";

defineOptions({ inheritAttrs: false });

const props = withDefaults(defineProps<{
  label?: string;
  loading?: boolean;
  disabled?: boolean;
  size?: "small" | "medium" | "large";
  type?: "button" | "submit" | "reset";
  severity?: "primary" | "secondary" | "success" | "info" | "warn" | "danger" | "error" | "contrast";
}>(), {
  type: "button",
  size: "medium",
});

const naiveType = computed(() => {
  switch (props.severity) {
    case "success":
      return "success";
    case "warn":
      return "warning";
    case "danger":
    case "error":
      return "error";
    case "info":
      return "info";
    case "secondary":
      return "default";
    default:
      return "primary";
  }
});
</script>

<template>
  <NButton
    v-bind="$attrs"
    :type="naiveType"
    :attr-type="type"
    :size="size"
    :loading="loading"
    :disabled="disabled"
  >
    <template v-if="$slots.icon" #icon>
      <slot name="icon" />
    </template>
    <slot>{{ label }}</slot>
  </NButton>
</template>
