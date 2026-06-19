<script setup lang="ts">
import { NCard } from "naive-ui";
</script>

<template>
  <NCard v-bind="$attrs">
    <slot />
  </NCard>
</template>
