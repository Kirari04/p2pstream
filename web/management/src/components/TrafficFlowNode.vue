<script setup lang="ts">
import { Handle, Position } from "@vue-flow/core";
import type { NodeProps } from "@vue-flow/core";
import type { TrafficFlowEditTarget } from "@/types/trafficFlowEdit";

type AgentNodeStatus = {
  state: "connected" | "offline" | "disabled" | "unknown";
  label: string;
};

type TrafficNodeData = {
  label: string;
  subLabel: string;
  kind: "ingress" | "listener" | "rate-limit" | "route" | "backend" | "redirect" | "agent" | "upstream" | "response";
  editTargets: TrafficFlowEditTarget[];
  agentStatus?: AgentNodeStatus;
};

defineProps<NodeProps<TrafficNodeData>>();
</script>

<template>
  <div
    class="traffic-flow-node"
    :class="[`traffic-flow-node-${data.kind}`, data.editTargets.length ? 'traffic-flow-node-clickable' : '']"
  >
    <Handle type="target" :position="Position.Left" class="traffic-handle" />
    <div class="node-label" :title="data.label">{{ data.label || "-" }}</div>
    <div class="node-meta">
      <div class="node-sub-label" :title="data.subLabel">{{ data.subLabel || "-" }}</div>
      <span
        v-if="data.kind === 'agent' && data.agentStatus"
        class="node-status"
        :class="`node-status-${data.agentStatus.state}`"
      >
        <span class="node-status-dot" />
        <span class="node-status-label">{{ data.agentStatus.label }}</span>
      </span>
    </div>
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
  transition: border-color 140ms ease, background 140ms ease, box-shadow 140ms ease;
}

.traffic-flow-node-clickable {
  cursor: pointer;
}

.traffic-flow-node-clickable:hover {
  border-color: #e4e4e7;
  background: #0a0a0a;
  box-shadow: 0 10px 26px rgb(0 0 0 / 34%);
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

.traffic-flow-node-rate-limit {
  border-color: #f59e0b;
}

.traffic-flow-node-backend {
  border-color: #71717a;
}

.traffic-flow-node-redirect {
  border-color: #0f766e;
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

.node-meta {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
  margin-top: 4px;
}

.node-sub-label {
  flex: 1 1 auto;
  color: #888;
  font-family: var(--font-mono);
  font-size: 0.66rem;
  line-height: 1.2;
}

.node-status {
  display: inline-flex;
  flex: 0 0 auto;
  max-width: 76px;
  align-items: center;
  gap: 4px;
  overflow: hidden;
  border: 1px solid currentColor;
  border-radius: 999px;
  padding: 1px 5px;
  background: rgb(255 255 255 / 3%);
  font-size: 0.58rem;
  font-weight: 650;
  line-height: 1.2;
}

.node-status-dot {
  width: 5px;
  height: 5px;
  flex: 0 0 auto;
  border-radius: 999px;
  background: currentColor;
  box-shadow: 0 0 8px currentColor;
}

.node-status-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.node-status-connected {
  color: #22c55e;
}

.node-status-offline {
  color: #f59e0b;
}

.node-status-disabled {
  color: #a1a1aa;
}

.node-status-unknown {
  color: #71717a;
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
