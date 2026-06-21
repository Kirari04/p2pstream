<script setup lang="ts">
import { NCard } from "naive-ui";

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
  <NCard :bordered="true" size="small" class="metric-card">
    <p class="metric-card__label">{{ label }}</p>
    <p class="metric-card__value" :class="toneClass">{{ value }}</p>
  </NCard>
</template>

<style scoped>
.metric-card__label {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-weight: 600;
  letter-spacing: 0;
  text-transform: uppercase;
}

.metric-card__value {
  margin: 0.5rem 0 0;
  font-size: 1.5rem;
  font-weight: 600;
  letter-spacing: 0;
}
</style>
