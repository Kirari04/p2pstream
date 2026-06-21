<script setup lang="ts">
defineProps<{
  title: string;
  description?: string;
}>();
</script>

<template>
  <header class="margin-bottom-xl layout-row layout-column space-lg mq-md-row mq-md-align-end mq-md-spread">
    <div class="min-width-zero">
      <h1 class="copy-2xl weight-semibold letter-normal base-text">{{ title }}</h1>
      <p v-if="description" class="margin-top-xs max-content-lg copy-sm line-relaxed muted-text">
        {{ description }}
      </p>
      <slot />
    </div>
    <div v-if="$slots.actions" class="layout-row wrap-items align-center space-sm">
      <slot name="actions" />
    </div>
  </header>
</template>
