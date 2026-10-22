<script setup lang="ts">
import { computed, type Component } from "vue";
import { Handle, Position } from "@vue-flow/core";
import type { NodeProps } from "@vue-flow/core";
import {
  Box,
  CornerUpRight,
  Crosshair,
  Database,
  Gauge,
  Globe,
  Plug,
  Reply,
  Server,
  Shield,
  Signpost,
  Waves,
} from "@lucide/vue";
import type { TrafficNodeData, TrafficNodeKind } from "@/lib/trafficFlowModel";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";

const props = defineProps<NodeProps<TrafficNodeData>>();

// Neutral kind markers: the icon says what a node is, color stays reserved for live status.
const kindIcons: Record<TrafficNodeKind, Component> = {
  ingress: Globe,
  listener: Plug,
  waf: Shield,
  "rate-limit": Gauge,
  "traffic-shaper": Waves,
  cache: Database,
  route: Signpost,
  target: Crosshair,
  redirect: CornerUpRight,
  agent: Server,
  upstream: Box,
  response: Reply,
};
const kindIcon = computed(() => kindIcons[props.data.kind]);

const labelText = computed(() => diagnosticExcerpt(props.data.label || "—", 34).text);
const labelExact = computed(() => diagnosticInspectionText(props.data.label || "—"));
const subLabelText = computed(() => diagnosticExcerpt(props.data.subLabel || "—", 38).text);
const subLabelExact = computed(() => diagnosticInspectionText(props.data.subLabel || "—"));
const cacheStatusText = computed(() => diagnosticExcerpt(props.data.cacheStatus?.label || "—", 14).text);
const agentStatusText = computed(() => diagnosticExcerpt(props.data.agentStatus?.label || "—", 18).text);
const cacheStatusExact = computed(() => diagnosticInspectionText(props.data.cacheStatus?.label || "—"));
const agentStatusExact = computed(() => diagnosticInspectionText(props.data.agentStatus?.label || "—"));
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
    <div class="node-title">
      <component :is="kindIcon" class="node-kind-icon" :size="12" aria-hidden="true" />
      <bdi class="node-label" dir="ltr" :title="labelExact">{{ labelText }}</bdi>
    </div>
    <span
      v-if="data.kind === 'cache' && data.cacheStatus"
      class="node-cache-badge"
      :class="`node-cache-badge-${data.cacheStatus.tone}`"
      :title="cacheStatusExact"
    >
      <bdi dir="ltr">{{ cacheStatusText }}</bdi>
    </span>
    <div class="node-meta">
      <bdi class="node-sub-label" dir="ltr" :title="subLabelExact">{{ subLabelText }}</bdi>
      <span
        v-if="data.kind === 'agent' && data.agentStatus"
        class="node-status"
        :class="`node-status-${data.agentStatus.state}`"
        :title="agentStatusExact"
      >
        <span class="node-status-dot" />
        <bdi class="node-status-label" dir="ltr">{{ agentStatusText }}</bdi>
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
  border-radius: 8px;
  background: var(--app-panel);
  padding: 9px 12px;
  color: var(--app-text);
  box-shadow: 0 1px 2px rgb(0 0 0 / 8%);
  transition: border-color 140ms ease, background 140ms ease, box-shadow 140ms ease;
}

.traffic-flow-node-clickable {
  cursor: pointer;
}

.traffic-flow-node-clickable:hover {
  border-color: var(--app-accent);
  background: var(--app-panel);
  box-shadow: 0 0 0 1px color-mix(in srgb, var(--app-accent) 20%, transparent);
}

/* Entry and exit points are quiet; status colors are reserved for live cache and agent state. */
.traffic-flow-node-ingress,
.traffic-flow-node-response {
  background: var(--app-panel-muted);
}

.traffic-flow-node-cache-hit,
.traffic-flow-node-cache-stored {
  border-color: var(--app-success);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--app-success) 14%, transparent);
}

.traffic-flow-node-cache-miss,
.traffic-flow-node-cache-lookup {
  border-color: var(--app-accent);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--app-accent) 14%, transparent);
}

.traffic-flow-node-cache-bypass {
  border-color: var(--app-text-muted);
}

.node-label,
.node-sub-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  direction: ltr;
  unicode-bidi: isolate;
}

.node-title {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 5px;
}

.node-kind-icon {
  flex: 0 0 auto;
  color: var(--app-text-muted);
}

.node-label {
  font-size: 0.75rem;
  font-weight: 600;
  line-height: 1.25;
}

.traffic-flow-node-cache .node-title {
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
  font-size: 0.6875rem;
  line-height: 1.2;
}

.node-status {
  display: inline-flex;
  flex: 0 0 auto;
  max-width: 76px;
  align-items: center;
  gap: 4px;
  overflow: hidden;
  border-radius: 6px;
  padding: 1px 5px;
  background: color-mix(in srgb, currentColor 12%, transparent);
  font-size: 0.625rem;
  font-weight: 500;
  line-height: 1.2;
}

.node-status-dot {
  width: 5px;
  height: 5px;
  flex: 0 0 auto;
  border-radius: 999px;
  background: currentColor;
}

.node-status-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  direction: ltr;
  unicode-bidi: isolate;
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
  border-radius: 6px;
  padding: 1px 5px;
  background: color-mix(in srgb, currentColor 12%, transparent);
  font-family: var(--font-mono);
  font-size: 0.5625rem;
  font-weight: 600;
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
