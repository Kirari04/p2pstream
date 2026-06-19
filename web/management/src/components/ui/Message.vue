<script setup lang="ts">
import { computed } from "vue";
import { NAlert } from "naive-ui";

const props = defineProps<{
  severity?: "success" | "info" | "warn" | "error" | "secondary" | "contrast";
}>();

const alertType = computed(() => {
  switch (props.severity) {
    case "success":
      return "success";
    case "warn":
      return "warning";
    case "error":
      return "error";
    default:
      return "info";
  }
});
</script>

<template>
  <NAlert :type="alertType" :show-icon="true" v-bind="$attrs">
    <slot />
  </NAlert>
</template>
