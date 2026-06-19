<script setup lang="ts">
import { NButton } from "naive-ui";

defineOptions({ inheritAttrs: false });

withDefaults(defineProps<{
  label?: string;
  loading?: boolean;
  disabled?: boolean;
  size?: "small" | "medium" | "large";
  type?: "button" | "submit" | "reset";
}>(), {
  type: "button",
  size: "medium",
});
</script>

<template>
  <NButton
    v-bind="$attrs"
    secondary
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
