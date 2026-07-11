<script setup lang="ts">
defineProps<{
  title: string;
  description?: string;
}>();
</script>

<template>
  <header class="page-header">
    <div class="page-header__copy">
      <h1>{{ title }}</h1>
      <p v-if="description">
        {{ description }}
      </p>
      <slot />
    </div>
    <div v-if="$slots.actions" class="page-header__actions">
      <slot name="actions" />
    </div>
  </header>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1.5rem;
  margin-bottom: 1.5rem;
}

.page-header__copy {
  min-width: 0;
}

.page-header h1 {
  margin: 0;
  color: var(--app-text);
  font-size: 1.5rem;
  font-weight: 600;
  letter-spacing: -0.02em;
  line-height: 1.25;
  text-wrap: balance;
}

.page-header p {
  max-width: 70ch;
  margin: 0.375rem 0 0;
  color: var(--app-text-muted);
  font-size: 0.875rem;
  line-height: 1.55;
  text-wrap: pretty;
}

.page-header__actions {
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 0.5rem;
}

@media (max-width: 639px) {
  .page-header {
    align-items: stretch;
    flex-direction: column;
    gap: 1rem;
  }

  .page-header__actions {
    justify-content: stretch;
  }
}
</style>
