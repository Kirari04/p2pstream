<script setup lang="ts">
const props = withDefaults(defineProps<{
  label: string;
  value: string;
  tone?: "default" | "success" | "warning" | "error" | "info";
}>(), {
  tone: "default",
});

const toneClass = {
  default: "text-[var(--app-text)]",
  success: "text-emerald-600 dark:text-emerald-300",
  warning: "text-amber-600 dark:text-amber-300",
  error: "text-red-600 dark:text-red-300",
  info: "text-blue-600 dark:text-sky-300",
}[props.tone];
</script>

<template>
  <div class="app-card p-5">
    <p class="text-xs font-semibold uppercase tracking-normal text-[var(--app-text-muted)]">{{ label }}</p>
    <p class="mt-2 text-2xl font-semibold tracking-normal" :class="toneClass">{{ value }}</p>
  </div>
</template>
