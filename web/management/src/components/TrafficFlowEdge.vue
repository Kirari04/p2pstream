<script setup lang="ts">
import { computed } from "vue";
import { BaseEdge } from "@vue-flow/core";
import type { EdgeProps } from "@vue-flow/core";

type TrafficEdgeData = {
  path?: string;
};

const props = defineProps<EdgeProps<TrafficEdgeData>>();

const path = computed(() => {
  if (props.data?.path) return props.data.path;
  return `M${props.sourceX},${props.sourceY} C${props.sourceX + 80},${props.sourceY} ${props.targetX - 80},${props.targetY} ${props.targetX},${props.targetY}`;
});
</script>

<template>
  <BaseEdge
    :id="id"
    :path="path"
    :marker-end="markerEnd"
    :interaction-width="interactionWidth"
  />
</template>
