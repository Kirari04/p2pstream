<script setup lang="ts">
import { Handle, Position } from "@vue-flow/core";
import type { NodeProps } from "@vue-flow/core";

type TrafficNodeData = {
  label: string;
  subLabel: string;
  kind: "ingress" | "listener" | "route" | "backend" | "agent" | "upstream" | "response";
};

defineProps<NodeProps<TrafficNodeData>>();
</script>

<template>
  <div class="traffic-flow-node" :class="`traffic-flow-node-${data.kind}`">
    <Handle type="target" :position="Position.Left" class="traffic-handle" />
    <div class="node-label" :title="data.label">{{ data.label || "-" }}</div>
    <div class="node-sub-label" :title="data.subLabel">{{ data.subLabel || "-" }}</div>
    <Handle type="source" :position="Position.Right" class="traffic-handle" />
  </div>
</template>

<style scoped>
.traffic-flow-node {
  width: 152px;
  height: 58px;
  border: 1px solid #333;
  border-radius: 6px;
  background: #050505;
  padding: 9px 12px;
  color: #ededed;
  box-shadow: 0 8px 22px rgb(0 0 0 / 26%);
}

.traffic-flow-node-ingress,
.traffic-flow-node-response {
  border-color: #d4d4d8;
}

.traffic-flow-node-agent {
  border-color: #2563eb;
}

.traffic-flow-node-upstream {
  border-color: #0f766e;
}

.traffic-flow-node-route {
  border-color: #52525b;
}

.traffic-flow-node-backend {
  border-color: #71717a;
}

.node-label,
.node-sub-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.node-label {
  font-size: 0.76rem;
  font-weight: 650;
  line-height: 1.25;
}

.node-sub-label {
  margin-top: 4px;
  color: #888;
  font-family: var(--font-mono);
  font-size: 0.66rem;
  line-height: 1.2;
}

:deep(.traffic-handle) {
  width: 2px;
  height: 18px;
  border: 0;
  border-radius: 999px;
  background: transparent;
  opacity: 0;
  pointer-events: none;
}
</style>
