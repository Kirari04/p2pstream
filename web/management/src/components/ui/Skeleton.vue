<script setup lang="ts">
import { NSkeleton } from "naive-ui";

defineProps<{
  width?: string;
  height?: string;
}>();
</script>

<template>
  <NSkeleton :width="width" :height="height" v-bind="$attrs" />
</template>
