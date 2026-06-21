<script setup lang="ts">
import { Plus as PlusIcon } from "@lucide/vue";
import { NButton, NEmpty } from "naive-ui";

defineProps<{
  title: string;
  description?: string;
  actionLabel?: string;
}>();

defineEmits<{
  (event: "action"): void;
}>();
</script>

<template>
  <NEmpty :description="title" size="small" class="empty-state">
    <template v-if="description || actionLabel" #extra>
      <div class="empty-state__extra">
        <p v-if="description" class="empty-state__description">{{ description }}</p>
        <NButton
          v-if="actionLabel"
          secondary
          size="small"
          @click="$emit('action')"
        >
          <template #icon><PlusIcon class="icon-sm" /></template>
          {{ actionLabel }}
        </NButton>
      </div>
    </template>
  </NEmpty>
</template>

<style scoped>
.empty-state {
  padding: 2.5rem 1.25rem;
}

.empty-state__extra {
  display: grid;
  justify-items: center;
  gap: 0.75rem;
  margin-top: 0.25rem;
}

.empty-state__description {
  max-width: 24rem;
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  text-align: center;
}
</style>
