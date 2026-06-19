<script setup lang="ts">
import { computed } from "vue";
import { NTooltip } from "naive-ui";

type DisabledHintPlacement = "top" | "bottom" | "left" | "right";

const props = withDefaults(defineProps<{
  disabled?: boolean;
  reason?: string;
  placement?: DisabledHintPlacement;
  inline?: boolean;
  fullWidth?: boolean;
}>(), {
  placement: "top",
  inline: true,
  fullWidth: false,
});

const normalizedReason = computed(() => props.reason?.trim() ?? "");
const isActive = computed(() => Boolean(props.disabled && normalizedReason.value));
const wrapperClass = computed(() => ({
  "disabled-hint": true,
  "disabled-hint-inline": props.inline && !props.fullWidth,
  "disabled-hint-full": props.fullWidth,
}));
</script>

<template>
  <NTooltip :disabled="!isActive" :placement="placement" trigger="hover">
    <template #trigger>
      <span
        :class="wrapperClass"
        :aria-disabled="isActive ? 'true' : undefined"
      >
        <slot />
      </span>
    </template>
    {{ normalizedReason }}
  </NTooltip>
</template>

<style scoped>
.disabled-hint {
  min-width: 0;
}

.disabled-hint-inline {
  display: inline-flex;
}

.disabled-hint-full {
  display: flex;
  width: 100%;
}
</style>
