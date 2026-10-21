<script setup lang="ts">
withDefaults(defineProps<{
  label: string;
  value: string;
  detail?: string;
  tone?: "default" | "success" | "warning" | "error" | "info" | "muted";
  /** Show a leading status dot in the tone color. */
  dot?: boolean;
}>(), {
  tone: "default",
  dot: false,
});
</script>

<template>
  <div class="metric-card">
    <dt class="metric-card__label">{{ label }}</dt>
    <dd class="metric-card__value" :class="`metric-card__value--${tone}`">
      <span v-if="dot" class="metric-card__dot" aria-hidden="true" />
      <slot name="value">{{ value }}</slot>
    </dd>
    <dd v-if="detail || $slots.detail" class="metric-card__detail">
      <slot name="detail">{{ detail }}</slot>
    </dd>
    <slot />
  </div>
</template>

<style scoped>
.metric-card {
  display: grid;
  min-width: 0;
  align-content: start;
  gap: 0.25rem;
}

.metric-card__label {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.metric-card__value {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.5rem;
  overflow: hidden;
  color: var(--app-text);
  font-size: 1.375rem;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
  letter-spacing: -0.01em;
  line-height: 1.2;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.metric-card__dot {
  width: 0.5rem;
  height: 0.5rem;
  flex: 0 0 auto;
  border-radius: 50%;
  background: currentColor;
}

.metric-card__value--success .metric-card__dot { color: var(--app-success); }
.metric-card__value--warning { color: var(--app-warning); }
.metric-card__value--error { color: var(--app-error); }
.metric-card__value--info { color: var(--app-info); }
.metric-card__value--muted { color: var(--app-text-muted); }
.metric-card__value--success:not(:has(.metric-card__dot)) { color: var(--app-success); }

.metric-card__detail {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.4;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
