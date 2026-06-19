<script setup lang="ts">
import { Plus as PlusIcon } from "@lucide/vue";
import { NButton } from "naive-ui";

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
  <div class="flex flex-col items-center justify-center gap-3 px-5 py-10 text-center">
    <p class="text-sm font-medium text-[var(--app-text-muted)]">{{ title }}</p>
    <p v-if="description" class="max-w-sm text-xs text-[var(--app-text-muted)]">{{ description }}</p>
    <NButton
      v-if="actionLabel"
      secondary
      size="small"
      class="mt-1"
      @click="$emit('action')"
    >
      <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
      {{ actionLabel }}
    </NButton>
  </div>
</template>
