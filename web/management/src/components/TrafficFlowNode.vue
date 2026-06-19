<script setup lang="ts">
import { Handle, Position } from "@vue-flow/core";
import type { NodeProps } from "@vue-flow/core";
import type { TrafficFlowEditTarget } from "@/types/trafficFlowEdit";

type AgentNodeStatus = {
  state: "connected" | "offline" | "disabled" | "unknown";
  label: string;
};
type CacheNodeStatus = {
  label: string;
  tone: "hit" | "miss" | "bypass" | "stored" | "lookup" | "neutral";
};

type TrafficNodeData = {
  label: string;
  subLabel: string;
  kind: "ingress" | "listener" | "waf" | "rate-limit" | "traffic-shaper" | "cache" | "route" | "target" | "redirect" | "agent" | "upstream" | "response";
  editTargets: TrafficFlowEditTarget[];
  agentStatus?: AgentNodeStatus;
  cacheStatus?: CacheNodeStatus;
};

defineProps<NodeProps<TrafficNodeData>>();
</script>

<template>
  <div
    class="traffic-flow-node"
    :class="[
      `traffic-flow-node-${data.kind}`,
      data.kind === 'cache' && data.cacheStatus ? `traffic-flow-node-cache-${data.cacheStatus.tone}` : '',
      data.editTargets.length ? 'traffic-flow-node-clickable' : '',
    ]"
  >
    <Handle type="target" :position="Position.Left" class="traffic-handle" />
    <div class="node-label" :title="data.label">{{ data.label || "-" }}</div>
    <span
      v-if="data.kind === 'cache' && data.cacheStatus"
      class="node-cache-badge"
      :class="`node-cache-badge-${data.cacheStatus.tone}`"
    >
      {{ data.cacheStatus.label }}
    </span>
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
  position: relative;
  width: 152px;
  height: 58px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
  padding: 9px 12px;
  color: var(--app-text);
  box-shadow: 0 8px 22px rgb(0 0 0 / 26%);
  transition: border-color 140ms ease, background 140ms ease, box-shadow 140ms ease;
}

.traffic-flow-node-clickable {
  cursor: pointer;
}

.traffic-flow-node-clickable:hover {
  border-color: var(--app-border);
  background: var(--app-panel);
  box-shadow: 0 10px 26px rgb(0 0 0 / 34%);
}

.traffic-flow-node-ingress,
.traffic-flow-node-response {
  border-color: var(--app-border);
}

.traffic-flow-node-agent {
  border-color: var(--app-accent);
}

.traffic-flow-node-upstream {
  border-color: var(--app-success);
}

.traffic-flow-node-route {
  border-color: var(--app-border);
}

.traffic-flow-node-rate-limit {
  border-color: var(--app-warning);
}

.traffic-flow-node-waf {
  border-color: var(--app-error);
}

.traffic-flow-node-traffic-shaper {
  border-color: var(--app-accent);
}

.traffic-flow-node-cache {
  border-color: var(--app-accent);
  background:
    linear-gradient(135deg, rgb(45 212 191 / 8%), transparent 42%),
    var(--app-panel-muted);
}

.traffic-flow-node-cache-hit,
.traffic-flow-node-cache-stored {
  border-color: var(--app-success);
  box-shadow: 0 8px 22px rgb(0 0 0 / 26%), 0 0 20px rgb(52 211 153 / 13%);
}

.traffic-flow-node-cache-miss,
.traffic-flow-node-cache-lookup {
  border-color: var(--app-accent);
  box-shadow: 0 8px 22px rgb(0 0 0 / 26%), 0 0 20px rgb(56 189 248 / 12%);
}

.traffic-flow-node-cache-bypass {
  border-color: var(--app-text-muted);
  background: var(--app-panel-muted);
}

.traffic-flow-node-target {
  border-color: var(--app-text-muted);
}

.traffic-flow-node-redirect {
  border-color: var(--app-success);
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

.traffic-flow-node-cache .node-label {
  padding-right: 56px;
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
  color: var(--app-text-muted);
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
  color: var(--app-success);
}

.node-status-offline {
  color: var(--app-warning);
}

.node-status-disabled {
  color: var(--app-text-muted);
}

.node-status-unknown {
  color: var(--app-text-muted);
}

.node-cache-badge {
  position: absolute;
  top: 7px;
  right: 8px;
  max-width: 54px;
  overflow: hidden;
  border: 1px solid currentColor;
  border-radius: 999px;
  padding: 1px 5px;
  background: color-mix(in srgb, var(--app-panel) 88%, transparent);
  font-family: var(--font-mono);
  font-size: 0.54rem;
  font-weight: 750;
  line-height: 1.2;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.node-cache-badge-hit,
.node-cache-badge-stored {
  color: var(--app-success);
}

.node-cache-badge-miss,
.node-cache-badge-lookup {
  color: var(--app-accent);
}

.node-cache-badge-bypass,
.node-cache-badge-neutral {
  color: var(--app-text-muted);
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
