<script setup lang="ts">
withDefaults(defineProps<{
  /**
   * "strip": calm row separated by hairlines (configuration and settings pages).
   * "tiles": dense filled tiles with monospace values (Overview, Traffic, Diagnostics).
   */
  variant?: "strip" | "tiles";
  label?: string;
}>(), {
  variant: "strip",
});
</script>

<template>
  <dl class="metric-strip" :class="`metric-strip--${variant}`" :aria-label="label">
    <slot />
  </dl>
</template>

<style scoped>
.metric-strip {
  display: grid;
  min-width: 0;
  grid-template-columns: repeat(auto-fit, minmax(9.5rem, 1fr));
}

.metric-strip--strip {
  border-block: 1px solid var(--app-border-subtle);
}

.metric-strip--strip > :deep(.metric-card) {
  padding: 0.875rem 1rem;
  border-left: 1px solid var(--app-border-subtle);
}

.metric-strip--strip > :deep(.metric-card:first-child) {
  padding-left: 0;
  border-left: 0;
}

.metric-strip--tiles {
  grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr));
  gap: 0.5rem;
}

.metric-strip--tiles > :deep(.metric-card) {
  padding: 0.75rem 0.875rem;
  border-radius: 8px;
  background: var(--app-panel-muted);
}

.metric-strip--tiles > :deep(.metric-card .metric-card__value) {
  font-family: var(--font-mono);
  font-size: 1.125rem;
  letter-spacing: 0;
}

@media (max-width: 639px) {
  .metric-strip--strip {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .metric-strip--strip > :deep(.metric-card) {
    padding: 0.75rem 0;
    border-left: 0;
    border-top: 1px solid var(--app-border-subtle);
  }

  .metric-strip--strip > :deep(.metric-card:nth-child(-n + 2)) {
    border-top: 0;
  }

  .metric-strip--strip > :deep(.metric-card:nth-child(even)) {
    padding-left: 1rem;
    border-left: 1px solid var(--app-border-subtle);
  }
}
</style>
