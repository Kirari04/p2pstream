<script setup lang="ts">
import { Plus as PlusIcon } from "@lucide/vue";
import SecondaryButton from "@/components/ui/SecondaryButton.vue";

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
    <p class="text-sm font-medium text-[#888]">{{ title }}</p>
    <p v-if="description" class="max-w-sm text-xs text-[#666]">{{ description }}</p>
    <SecondaryButton
      v-if="actionLabel"
      size="small"
      :label="actionLabel"
      class="mt-1"
      @click="$emit('action')"
    >
      <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
    </SecondaryButton>
  </div>
</template>
