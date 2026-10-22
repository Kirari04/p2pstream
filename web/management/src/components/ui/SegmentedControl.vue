<script setup lang="ts" generic="T extends string | number">
defineProps<{
  modelValue: T;
  options: readonly { value: T; label: string; disabled?: boolean }[];
  /** Accessible name for the group, e.g. "Dashboard window". */
  label: string;
  size?: "small" | "medium";
}>();

const emit = defineEmits<{ "update:modelValue": [value: T] }>();
</script>

<template>
  <div class="segmented" :class="`segmented--${size ?? 'small'}`" role="group" :aria-label="label">
    <button
      v-for="option in options"
      :key="String(option.value)"
      type="button"
      class="segmented__option"
      :class="{ 'segmented__option--active': option.value === modelValue }"
      :aria-pressed="option.value === modelValue"
      :disabled="option.disabled"
      @click="emit('update:modelValue', option.value)"
    >
      {{ option.label }}
    </button>
  </div>
</template>

<style scoped>
.segmented {
  display: inline-flex;
  max-width: 100%;
  flex-wrap: wrap;
  gap: 0.125rem;
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 0.1875rem;
}

.segmented__option {
  min-height: 1.75rem;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--app-text-muted);
  cursor: pointer;
  font: inherit;
  font-size: 0.8125rem;
  font-weight: 500;
  padding: 0.25rem 0.625rem;
  transition: background-color 140ms ease-out, color 140ms ease-out;
}

.segmented--medium .segmented__option {
  min-height: 2rem;
  padding-inline: 0.75rem;
  font-size: 0.875rem;
}

.segmented__option:hover:not(:disabled) {
  color: var(--app-text);
}

.segmented__option--active {
  background: var(--app-panel);
  box-shadow: 0 0 0 1px var(--app-border-subtle), 0 1px 2px rgba(0, 0, 0, 0.12);
  color: var(--app-text);
}

.segmented__option:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: 1px;
}

.segmented__option:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

@media (pointer: coarse) {
  .segmented__option {
    min-height: 2.5rem;
  }
}
</style>
