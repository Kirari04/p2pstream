<script setup lang="ts">
defineProps<{
  title: string;
  description?: string;
}>();
</script>

<template>
  <header class="page-header">
    <div class="page-header__copy">
      <div class="page-header__title-row">
        <h1 class="page-header__title">{{ title }}</h1>
        <slot name="badge" />
      </div>
      <p v-if="description || $slots.description" class="page-header__description">
        <slot name="description">{{ description }}</slot>
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
  align-items: center;
  justify-content: space-between;
  gap: 1rem 1.5rem;
}

.page-header__copy {
  display: grid;
  min-width: 0;
  gap: 0.25rem;
}

.page-header__title-row {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem 0.75rem;
}

.page-header__title {
  color: var(--app-text);
  font-size: 1.375rem;
  font-weight: 600;
  letter-spacing: -0.015em;
  line-height: 1.25;
  text-wrap: balance;
}

.page-header__description {
  max-width: 72ch;
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
  }

  .page-header__actions {
    justify-content: flex-start;
  }
}
</style>
