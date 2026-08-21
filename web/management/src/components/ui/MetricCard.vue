<script setup lang="ts">
const props = withDefaults(defineProps<{
  label: string;
  value: string;
  tone?: "default" | "success" | "warning" | "error" | "info";
}>(), {
  tone: "default",
});

const toneClass = {
  default: "metric-card__value--default",
  success: "metric-card__value--success",
  warning: "metric-card__value--warning",
  error: "metric-card__value--error",
  info: "metric-card__value--info",
}[props.tone];
</script>

<template>
  <div class="metric-card">
    <span class="metric-card__label">{{ label }}</span>
    <strong class="metric-card__value" :class="toneClass">{{ value }}</strong>
  </div>
</template>

<style scoped>
.metric-card__label {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-weight: 600;
  letter-spacing: 0;
}

.metric-card {
  display: grid;
  min-width: 0;
  gap: 0.25rem;
  padding: 0.75rem 1rem;
}

.metric-card__value {
  overflow: hidden;
  font-size: 1.125rem;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
  letter-spacing: 0;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
