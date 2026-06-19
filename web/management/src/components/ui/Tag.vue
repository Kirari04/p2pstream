<script setup lang="ts">
import { computed } from "vue";
import { NTag } from "naive-ui";

const props = defineProps<{
  value?: string;
  severity?: string;
}>();

const tagType = computed(() => {
  switch (props.severity) {
    case "success":
      return "success";
    case "warn":
      return "warning";
    case "danger":
    case "error":
      return "error";
    case "info":
      return "info";
    default:
      return "default";
  }
});
</script>

<template>
  <NTag size="small" round :type="tagType" v-bind="$attrs">
    <slot>{{ value }}</slot>
  </NTag>
</template>
