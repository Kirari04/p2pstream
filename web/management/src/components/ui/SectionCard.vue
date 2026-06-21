<script setup lang="ts">
import { NCard } from "naive-ui";

defineProps<{
  title?: string;
  description?: string;
  segmented?: boolean;
}>();
</script>

<template>
  <NCard :bordered="true" class="section-card" :class="{ 'section-card--segmented': segmented }">
    <template v-if="title || description || $slots.actions" #header>
      <div class="section-card__heading">
        <h2 v-if="title" class="copy-base weight-semibold base-text">{{ title }}</h2>
        <p v-if="description" class="margin-top-xs copy-sm muted-text">{{ description }}</p>
      </div>
    </template>
    <template v-if="$slots.actions" #header-extra>
      <div class="section-card__actions">
        <slot name="actions" />
      </div>
    </template>
    <slot />
  </NCard>
</template>

<style scoped>
.section-card {
  overflow: hidden;
}

.section-card__heading {
  min-width: 0;
}

.section-card__actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 0.5rem;
}

.section-card--segmented :deep(.n-card__content) {
  background: var(--app-panel-muted);
}
</style>
