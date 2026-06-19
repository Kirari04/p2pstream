<script setup lang="ts">
defineProps<{
  title?: string;
  description?: string;
  segmented?: boolean;
}>();
</script>

<template>
  <section class="app-card overflow-hidden" :class="{ 'app-card-segmented': segmented }">
    <div v-if="title || description || $slots.actions" class="app-card-header">
      <div>
        <h2 v-if="title" class="text-base font-semibold text-[var(--app-text)]">{{ title }}</h2>
        <p v-if="description" class="mt-1 text-sm text-[var(--app-text-muted)]">{{ description }}</p>
      </div>
      <div v-if="$slots.actions" class="flex flex-wrap items-center gap-2">
        <slot name="actions" />
      </div>
    </div>
    <div class="app-card-body">
      <slot />
    </div>
  </section>
</template>
