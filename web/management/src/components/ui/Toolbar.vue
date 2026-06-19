<script setup lang="ts">
import { NFlex } from "naive-ui";
</script>

<template>
  <NFlex align="center" justify="space-between" wrap v-bind="$attrs">
    <slot name="start" />
    <slot name="center" />
    <slot name="end" />
    <slot />
  </NFlex>
</template>
