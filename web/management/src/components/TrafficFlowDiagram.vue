<script setup lang="ts">
import { computed, markRaw, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  MarkerType,
  Position,
  VueFlow,
  useVueFlow,
  type Edge,
  type EdgeTypesObject,
  type Node,
  type NodeMouseEvent,
  type NodeTypesObject,
} from "@vue-flow/core";
import { NButton } from "naive-ui";
import TrafficFlowEdge from "@/components/TrafficFlowEdge.vue";
import TrafficFlowNode from "@/components/TrafficFlowNode.vue";
import {
  CACHE_KEY,
  TrafficRequestPathCache,
  createTrafficFlowConfigIndex,
  type TrafficRequestPathCacheEntry,
} from "@/lib/trafficFlowLayout";
import {
  buildTrafficFlowGraph,
  buildTrafficFlowEdgeRoutes,
  cacheStatusForTone,
  cacheTonePriority,
  edgeKey,
  nodeBounds,
  pointInsideBounds,
  routeToMotionPoints,
} from "@/lib/trafficFlowGraph";
import {
  activeVisualTokens,
  advanceVisualToken,
  cacheStorePulseActive,
  createVisualToken,
  nextFrameStressState,
  renderedTokenCap,
  resetFrameStressState,
  shouldEnqueueCacheStorePulse,
  tokenColorClass,
  updateVisualToken,
  visualTokenOpacity,
  visualTokenPoint,
} from "@/lib/trafficFlowAnimation";
import {
  CACHE_PROXIMITY_PX,
  CACHE_STORE_PULSE_MS,
  FLOW_ID,
  NODE_HEIGHT,
  NODE_WIDTH,
  type CacheNodeStatus,
  type CacheStorePulse,
  type Point,
  type TrafficNodeData,
  type VisualToken,
} from "@/lib/trafficFlowModel";
import {
  buildMotionNodeBox,
  buildMotionPlan,
  type MotionNodeBox,
  type MotionPlan,
  type MotionPoint,
} from "@/lib/trafficMotion";
import type { GetPublicProxyConfigResponse } from "@/gen/proto/p2pstream/v1/management_pb";
import type { TrafficFlowEditRequest } from "@/types/trafficFlowEdit";
import type { TraceRequest } from "@/types/trafficTrace";
import "@vue-flow/core/dist/style.css";
import "@vue-flow/core/dist/theme-default.css";

const props = withDefaults(
  defineProps<{
    config: GetPublicProxyConfigResponse | null;
    requests: TraceRequest[];
    tracingEnabled?: boolean;
  }>(),
  { tracingEnabled: false },
);

const emit = defineEmits<{
  (event: "select", request: TraceRequest): void;
  (event: "active-change", count: number): void;
  (event: "skipped-change", count: number): void;
  (event: "edit-node", request: TrafficFlowEditRequest): void;
}>();

const nodeTypes: NodeTypesObject = {
  traffic: markRaw(TrafficFlowNode),
};
const edgeTypes: EdgeTypesObject = {
  trafficBypass: markRaw(TrafficFlowEdge),
};

const { fitView, viewport } = useVueFlow(FLOW_ID);
const configIndex = computed(() => createTrafficFlowConfigIndex(props.config));
const requestPathCache = new TrafficRequestPathCache();
const tokenMotionPlanCache = new Map<string, MotionPlan>();
const flowShellRef = ref<HTMLElement | null>(null);
const visualTokens = ref<VisualToken[]>([]);
const cacheStorePulses = ref<CacheStorePulse[]>([]);
const rafNow = ref(typeof performance === "undefined" ? Date.now() : performance.now());
const skippedVisualizations = ref(0);
const isAnimationStressed = ref(false);
const seenRequestIds = new Set<string>();
const seenCacheStorePulseRequestIds = new Set<string>();
let rafId: number | null = null;
let didInitialFit = false;
let frameStressState = resetFrameStressState();
let resizeObserver: ResizeObserver | null = null;
let resizeFitTimer: number | null = null;

const layout = computed(() => buildTrafficFlowGraph({
  config: props.config,
  requests: props.requests,
  configIndex: configIndex.value,
  requestPath: cachedRequestPath,
}));

const activeCacheStatus = computed<CacheNodeStatus | undefined>(() => {
  const cacheNode = layout.value.nodeByKey.get(CACHE_KEY);
  if (!cacheNode) return undefined;
  rafNow.value;
  const candidates: CacheNodeStatus[] = [];
  const bounds = nodeBounds(cacheNode);
  const expandedBounds = {
    left: bounds.left - CACHE_PROXIMITY_PX,
    right: bounds.right + CACHE_PROXIMITY_PX,
    top: bounds.top - CACHE_PROXIMITY_PX,
    bottom: bounds.bottom + CACHE_PROXIMITY_PX,
  };
  for (const token of visualTokens.value) {
    if (!token.cacheTone || !token.path.includes(CACHE_KEY)) continue;
    const point = tokenPoint(token);
    if (!point || !pointInsideBounds(point, expandedBounds)) continue;
    candidates.push(cacheStatusForTone(token.cacheTone));
  }
  return candidates.sort((a, b) => cacheTonePriority(b.tone) - cacheTonePriority(a.tone))[0];
});

const flowNodes = computed<Node<TrafficNodeData>[]>(() =>
  layout.value.nodes.map((node) => ({
    id: node.key,
    type: "traffic",
    position: { x: node.x, y: node.y },
    width: NODE_WIDTH,
    height: NODE_HEIGHT,
    sourcePosition: Position.Right,
    targetPosition: Position.Left,
    selectable: false,
    draggable: false,
    connectable: false,
    focusable: false,
    data: {
      label: node.label,
      subLabel: node.subLabel,
      kind: node.kind,
      editTargets: node.editTargets,
      agentStatus: node.agentStatus,
      cacheStatus: node.kind === "cache" ? activeCacheStatus.value ?? node.cacheStatus : node.cacheStatus,
    },
  })),
);

const flowEdges = computed<Edge[]>(() =>
  layout.value.edges.map((edge) => {
    const key = edgeKey(edge.from, edge.to);
    const route = edgeRoutes.value.get(key);
    return {
      id: key,
      source: edge.from,
      target: edge.to,
      type: edge.route === "default" ? "default" : "trafficBypass",
      animated: props.tracingEnabled,
      selectable: false,
      focusable: false,
      markerEnd: {
        type: MarkerType.ArrowClosed,
        color: "var(--app-border)",
        width: 16,
        height: 16,
      },
      style: {
        stroke: "var(--app-border)",
        strokeWidth: 1.25,
      },
      data: {
        path: route?.path,
      },
    };
  }),
);

const edgeRoutes = computed(() => buildTrafficFlowEdgeRoutes(layout.value));

const motionNodeBoxes = computed(() => {
  const boxes = new Map<string, MotionNodeBox>();
  for (const node of layout.value.nodes) {
    boxes.set(node.key, buildMotionNodeBox({
      key: node.key,
      x: node.x,
      y: node.y,
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
    }));
  }
  return boxes;
});

const motionEdgeRoutePoints = computed(() => {
  const routes = new Map<string, MotionPoint[]>();
  for (const [key, route] of edgeRoutes.value.entries()) {
    routes.set(key, routeToMotionPoints(route));
  }
  return routes;
});

const motionLayoutSignature = computed(() => {
  const nodeSignature = layout.value.nodes
    .map((node) => `${node.key}:${node.x}:${node.y}`)
    .join(";");
  const edgeSignature = layout.value.edges
    .map((edge) => `${edge.from}:${edge.to}:${edge.route}`)
    .join(";");
  return `${nodeSignature}|${edgeSignature}`;
});

const tokenViews = computed(() => {
  const transform = viewport.value;
  const now = rafNow.value;
  return visualTokens.value
    .map((token) => {
      const point = tokenPoint(token);
      if (!point) return null;
      return {
        token,
        x: point.x * transform.zoom + transform.x,
        y: point.y * transform.zoom + transform.y,
        colorClass: tokenColorClass(token),
        label: token.label,
        opacity: visualTokenOpacity(token, now),
        nearCache: isTokenNearCache(token, point),
      };
    })
    .filter((token): token is NonNullable<typeof token> => token !== null);
});

const cacheStorePulseViews = computed(() => {
  if (isAnimationStressed.value) return [];
  const cacheNode = layout.value.nodeByKey.get(CACHE_KEY);
  if (!cacheNode) return [];
  const transform = viewport.value;
  const center = {
    x: (cacheNode.x + NODE_WIDTH / 2) * transform.zoom + transform.x,
    y: (cacheNode.y + NODE_HEIGHT / 2) * transform.zoom + transform.y,
  };
  const now = rafNow.value;
  return cacheStorePulses.value
    .map((pulse) => {
      const age = now - pulse.startedAt;
      if (age < 0 || !cacheStorePulseActive(pulse.startedAt, now, CACHE_STORE_PULSE_MS)) return null;
      return {
        id: pulse.requestId,
        x: center.x,
        y: center.y,
        opacity: clamp(1 - age / CACHE_STORE_PULSE_MS, 0, 1),
      };
    })
    .filter((view): view is NonNullable<typeof view> => view !== null);
});

watch(
  () => props.requests,
  () => syncTokens(),
  { immediate: true },
);

watch(
  () => props.tracingEnabled,
  (enabled, previousEnabled) => {
    if (!enabled) {
      if (previousEnabled) {
        resetVisualPlayback(true);
      }
      return;
    }
    syncTokens();
  },
);

watch(
  () => visualTokens.value.length,
  (count) => emit("active-change", count),
  { immediate: true },
);

watch(
  skippedVisualizations,
  (count) => emit("skipped-change", count),
  { immediate: true },
);

watch(
  flowNodes,
  () => {
    if (didInitialFit || flowNodes.value.length === 0) return;
    didInitialFit = true;
    void nextTick(() => fitDiagram(0));
  },
  { immediate: true, flush: "post" },
);

watch(
  () => props.config,
  () => {
    requestPathCache.clear();
    tokenMotionPlanCache.clear();
  },
  { flush: "post" },
);

watch(
  motionLayoutSignature,
  () => {
    tokenMotionPlanCache.clear();
    for (const token of visualTokens.value) {
      updateToken(token, token.request);
    }
    if (visualTokens.value.length) {
      startAnimationLoop();
    }
  },
  { flush: "post" },
);

onMounted(() => {
  document.addEventListener("visibilitychange", handleVisibilityChange);
  if (typeof ResizeObserver !== "undefined" && flowShellRef.value) {
    resizeObserver = new ResizeObserver(scheduleDiagramFit);
    resizeObserver.observe(flowShellRef.value);
  } else {
    window.addEventListener("resize", scheduleDiagramFit);
  }
});

onBeforeUnmount(() => {
  document.removeEventListener("visibilitychange", handleVisibilityChange);
  if (resizeObserver) {
    resizeObserver.disconnect();
  } else {
    window.removeEventListener("resize", scheduleDiagramFit);
  }
  if (resizeFitTimer !== null) {
    window.clearTimeout(resizeFitTimer);
    resizeFitTimer = null;
  }
  stopAnimationLoop();
});

function syncTokens() {
  if (props.requests.length === 0) {
    resetVisualPlayback(false);
    return;
  }

  const requestById = new Map(props.requests.map((request) => [request.requestId, request]));
  requestPathCache.prune(new Set(requestById.keys()));
  pruneTokenMotionPlanCache(new Set(requestById.keys()));
  pruneSeenRequests(requestById);
  let changed = false;
  for (const token of visualTokens.value) {
    const request = requestById.get(token.requestId);
    if (!request) continue;
    if (maybeEnqueueCacheStorePulse(request)) {
      changed = true;
    }
    updateToken(token, request);
  }

  const oldestFirst = [...props.requests].reverse();
  for (const request of oldestFirst) {
    if (maybeEnqueueCacheStorePulse(request)) {
      changed = true;
    }
    if (seenRequestIds.has(request.requestId)) continue;
    seenRequestIds.add(request.requestId);

    if (visualTokens.value.length >= currentTokenCap()) {
      skippedVisualizations.value += 1;
      continue;
    }

    visualTokens.value.push(createToken(request));
    changed = true;
  }

  if (changed || visualTokens.value.length) {
    startAnimationLoop();
  }
}

function resetVisualPlayback(seedCurrentRequests: boolean) {
  visualTokens.value = [];
  cacheStorePulses.value = [];
  skippedVisualizations.value = 0;
  seenRequestIds.clear();
  seenCacheStorePulseRequestIds.clear();
  if (seedCurrentRequests) {
    seedCurrentRequestsAsSeen();
  }
  stopAnimationLoop();
  requestPathCache.clear();
  tokenMotionPlanCache.clear();
  emit("active-change", 0);
  emit("skipped-change", 0);
}

function pruneSeenRequests(requestById: Map<string, TraceRequest>) {
  const animatedRequestIds = new Set(visualTokens.value.map((token) => token.requestId));
  for (const requestId of seenRequestIds) {
    if (requestById.has(requestId) || animatedRequestIds.has(requestId)) continue;
    seenRequestIds.delete(requestId);
  }
  for (const requestId of seenCacheStorePulseRequestIds) {
    if (requestById.has(requestId) || animatedRequestIds.has(requestId)) continue;
    seenCacheStorePulseRequestIds.delete(requestId);
  }
}

function createToken(request: TraceRequest): VisualToken {
  const cached = cachedRequestPath(request);
  const motionPlan = motionPlanForCachedPath(request.requestId, cached);
  const now = nowMs();
  return createVisualToken({
    request,
    cached,
    motionPlan,
    activeTokenCount: visualTokens.value.length,
    now,
  });
}

function updateToken(token: VisualToken, request: TraceRequest) {
  const now = nowMs();
  const cached = cachedRequestPath(request);
  const motionPlan = motionPlanForCachedPath(request.requestId, cached);
  updateVisualToken({
    token,
    request,
    cached,
    motionPlan,
    activeTokenCount: visualTokens.value.length,
    now,
  });
}

function maybeEnqueueCacheStorePulse(request: TraceRequest): boolean {
  if (!shouldEnqueueCacheStorePulse(request, configIndex.value, seenCacheStorePulseRequestIds)) return false;
  cacheStorePulses.value.push({ requestId: request.requestId, startedAt: nowMs() });
  return true;
}

function pruneCacheStorePulses(now: number) {
  const active = cacheStorePulses.value.filter((pulse) => cacheStorePulseActive(pulse.startedAt, now, CACHE_STORE_PULSE_MS));
  if (active.length !== cacheStorePulses.value.length) {
    cacheStorePulses.value = active;
  }
}

function startAnimationLoop() {
  if (rafId !== null) return;
  rafId = window.requestAnimationFrame(tick);
}

function stopAnimationLoop() {
  if (rafId === null) return;
  window.cancelAnimationFrame(rafId);
  rafId = null;
}

function tick(now: number) {
  rafNow.value = now;
  updateFrameStress(now);
  let changed = false;

  for (const token of visualTokens.value) {
    advanceVisualToken(token, now);
  }
  pruneCacheStorePulses(now);

  const activeTokens = activeVisualTokens(visualTokens.value, now);
  if (activeTokens.length !== visualTokens.value.length) {
    visualTokens.value = activeTokens;
    changed = true;
  }
  if (visualTokens.value.length > currentTokenCap()) {
    const removed = visualTokens.value.length - currentTokenCap();
    visualTokens.value = visualTokens.value.slice(0, currentTokenCap());
    skippedVisualizations.value += removed;
    changed = true;
  }

  if (visualTokens.value.length || cacheStorePulses.value.length) {
    rafId = window.requestAnimationFrame(tick);
  } else {
    rafId = null;
  }

  if (changed) {
    emit("active-change", visualTokens.value.length);
  }
}

function tokenPoint(token: VisualToken): Point | null {
  return visualTokenPoint(token, motionNodeBoxes.value);
}

function cachedRequestPath(request: TraceRequest) {
  return requestPathCache.get(request, configIndex.value);
}

function motionPlanForCachedPath(requestId: string, cached: TrafficRequestPathCacheEntry): MotionPlan {
  const signature = `${cached.signature}|${cached.targetIndex.toString()}|${motionLayoutSignature.value}`;
  const cacheKey = `${requestId}\u0000${signature}`;
  const cachedPlan = tokenMotionPlanCache.get(cacheKey);
  if (cachedPlan) return cachedPlan;

  const motionPlan = buildMotionPlan({
    path: cached.path,
    targetNodeIndex: cached.targetIndex,
    nodes: motionNodeBoxes.value,
    edgeRoutes: motionEdgeRoutePoints.value,
    signature,
  });
  tokenMotionPlanCache.set(cacheKey, motionPlan);
  return motionPlan;
}

function pruneTokenMotionPlanCache(requestIds: Set<string>) {
  for (const key of tokenMotionPlanCache.keys()) {
    const separatorIndex = key.indexOf("\u0000");
    const requestId = separatorIndex >= 0 ? key.slice(0, separatorIndex) : key;
    if (!requestIds.has(requestId)) {
      tokenMotionPlanCache.delete(key);
    }
  }
}

function nowMs(): number {
  return typeof performance === "undefined" ? Date.now() : performance.now();
}

function currentTokenCap(): number {
  return renderedTokenCap(isAnimationStressed.value);
}

function updateFrameStress(now: number) {
  frameStressState = nextFrameStressState(frameStressState, now);
  isAnimationStressed.value = frameStressState.stressed;
}

function seedCurrentRequestsAsSeen() {
  for (const request of props.requests) {
    seenRequestIds.add(request.requestId);
  }
}

function handleVisibilityChange() {
  if (document.hidden) {
    visualTokens.value = [];
    cacheStorePulses.value = [];
    seedCurrentRequestsAsSeen();
    stopAnimationLoop();
    emit("active-change", 0);
    return;
  }
  frameStressState = resetFrameStressState(isAnimationStressed.value);
  seedCurrentRequestsAsSeen();
  if (props.tracingEnabled) {
    syncTokens();
  }
}

function fitDiagram(duration = 240) {
  void fitView({ padding: 0.18, duration, maxZoom: 1.1 });
}

function scheduleDiagramFit() {
  if (!flowNodes.value.length) return;
  if (resizeFitTimer !== null) {
    window.clearTimeout(resizeFitTimer);
  }
  resizeFitTimer = window.setTimeout(() => {
    resizeFitTimer = null;
    void nextTick(() => fitDiagram(160));
  }, 80);
}

function handleNodeClick(event: NodeMouseEvent) {
  const data = event.node.data as TrafficNodeData | undefined;
  const targets = data?.editTargets ?? [];
  if (!targets.length) return;
  emit("edit-node", {
    nodeKey: event.node.id,
    nodeLabel: data?.label || event.node.id,
    targets,
  });
}

function isTokenNearCache(token: VisualToken, point: Point): boolean {
  if (!token.cacheTone || !token.path.includes(CACHE_KEY)) return false;
  const cacheNode = layout.value.nodeByKey.get(CACHE_KEY);
  if (!cacheNode) return false;
  const bounds = nodeBounds(cacheNode);
  return pointInsideBounds(point, {
    left: bounds.left - CACHE_PROXIMITY_PX,
    right: bounds.right + CACHE_PROXIMITY_PX,
    top: bounds.top - CACHE_PROXIMITY_PX,
    bottom: bounds.bottom + CACHE_PROXIMITY_PX,
  });
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

</script>

<template>
  <div ref="flowShellRef" class="traffic-flow-shell" :class="{ 'traffic-flow-stressed': isAnimationStressed }">
    <div class="flow-status">
      <span>{{ visualTokens.length }} rendered</span>
      <span v-if="skippedVisualizations" class="flow-overflow">+{{ skippedVisualizations }} not rendered</span>
      <NButton size="tiny" secondary @click="fitDiagram()">Fit</NButton>
    </div>

    <VueFlow
      :id="FLOW_ID"
      class="traffic-vue-flow"
      :nodes="flowNodes"
      :edges="flowEdges"
      :node-types="nodeTypes"
      :edge-types="edgeTypes"
      :nodes-draggable="false"
      :nodes-connectable="false"
      :elements-selectable="false"
      :pan-on-drag="true"
      :zoom-on-scroll="true"
      :zoom-on-pinch="true"
      :zoom-on-double-click="false"
      :min-zoom="0.16"
      :max-zoom="1.4"
      :fit-view-on-init="true"
      :delete-key-code="null"
      :selection-key-code="null"
      :multi-selection-key-code="null"
      @node-click="handleNodeClick"
    />

    <div class="traffic-token-layer">
      <NButton
        v-for="view in tokenViews"
        :key="view.token.requestId"
        attr-type="button"
        class="traffic-token"
        :class="[view.colorClass, view.nearCache ? 'traffic-token-cache-near' : '']"
        :style="{
          transform: `translate3d(${view.x}px, ${view.y}px, 0) translate(-50%, -50%)`,
          opacity: view.opacity,
        }"
        :aria-label="`Open trace details for ${view.label}`"
        @click="emit('select', view.token.request)"
      >
        <span class="traffic-token-halo" />
        <span class="traffic-token-dot" />
      </NButton>
      <span
        v-for="pulse in cacheStorePulseViews"
        :key="pulse.id"
        class="traffic-cache-store-pulse"
        :style="{
          transform: `translate3d(${pulse.x}px, ${pulse.y}px, 0) translate(-50%, -50%)`,
          opacity: pulse.opacity,
        }"
      />
    </div>
  </div>
</template>

<style scoped>
.traffic-flow-shell {
  position: relative;
  height: min(58vh, 540px);
  min-height: 360px;
  contain: layout paint style;
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background:
    linear-gradient(var(--app-border-subtle) 1px, transparent 1px),
    linear-gradient(90deg, var(--app-border-subtle) 1px, transparent 1px),
    var(--app-panel-muted);
  background-size: 32px 32px;
}

.traffic-vue-flow {
  width: 100%;
  height: 100%;
  background: transparent;
  color: var(--app-text);
}

.flow-status {
  position: absolute;
  top: 12px;
  right: 12px;
  z-index: 7;
  display: flex;
  align-items: center;
  gap: 8px;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: color-mix(in srgb, var(--app-panel) 88%, transparent);
  padding: 4px 6px 4px 10px;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1;
  backdrop-filter: blur(8px);
}

.flow-overflow {
  color: var(--app-warning);
}

.flow-fit-button {
  height: 24px;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-panel-muted);
  padding: 0 9px;
  color: var(--app-text);
  font-size: 0.72rem;
  font-weight: 600;
  transition: border-color 140ms ease, color 140ms ease, background 140ms ease;
}

.flow-fit-button:hover {
  border-color: var(--app-border);
  background: var(--app-panel-muted);
  color: var(--app-text);
}

.traffic-token-layer {
  position: absolute;
  inset: 0;
  z-index: 6;
  contain: strict;
  pointer-events: none;
}

.traffic-token {
  position: absolute;
  top: 0;
  left: 0;
  width: 28px;
  height: 28px;
  border: 0;
  background: transparent;
  padding: 0;
  pointer-events: auto;
  transition: opacity 160ms ease;
  will-change: transform, opacity;
}

.traffic-token-dot,
.traffic-token-halo {
  position: absolute;
  inset: 50% auto auto 50%;
  border-radius: 999px;
  transform: translate(-50%, -50%);
}

.traffic-token-dot {
  width: 11px;
  height: 11px;
  box-shadow: 0 0 14px currentColor;
}

.traffic-token-halo {
  width: 25px;
  height: 25px;
  opacity: 0.18;
  animation: token-pulse 1.25s ease-out infinite;
}

.traffic-token-in-flight {
  color: var(--app-accent);
}

.traffic-token-success {
  color: var(--app-success);
}

.traffic-token-cache-hit {
  color: var(--app-success);
}

.traffic-token-cache-miss {
  color: var(--app-accent);
}

.traffic-token-cache-bypass {
  color: var(--app-text-muted);
}

.traffic-token-cache-stored {
  color: var(--app-success);
}

.traffic-token-client-error {
  color: var(--app-warning);
}

.traffic-token-server-error,
.traffic-token-failed {
  color: var(--app-error);
}

.traffic-token-dot,
.traffic-token-halo {
  background: currentColor;
}

.traffic-token-cache-near .traffic-token-dot {
  width: 13px;
  height: 13px;
}

.traffic-token-cache-near .traffic-token-halo {
  width: 34px;
  height: 34px;
  opacity: 0.28;
}

.traffic-cache-store-pulse {
  position: absolute;
  top: 0;
  left: 0;
  width: 74px;
  height: 74px;
  border: 1px solid rgb(52 211 153 / 85%);
  border-radius: 999px;
  background: radial-gradient(circle, rgb(52 211 153 / 24%) 0%, rgb(52 211 153 / 8%) 44%, transparent 68%);
  box-shadow: 0 0 28px rgb(52 211 153 / 22%);
  pointer-events: none;
  animation: cache-store-pulse 900ms ease-out both;
  will-change: transform, opacity;
}

.traffic-flow-stressed .traffic-token-halo {
  animation: none;
  opacity: 0.08;
}

.traffic-flow-stressed .traffic-cache-store-pulse {
  display: none;
}

:deep(.vue-flow__pane) {
  cursor: grab;
}

:deep(.vue-flow__pane.dragging) {
  cursor: grabbing;
}

:deep(.vue-flow__node-traffic) {
  border: 0;
  background: transparent;
  box-shadow: none;
}

:deep(.vue-flow__edge-path) {
  stroke: var(--app-border);
}

:deep(.vue-flow__edge.animated .vue-flow__edge-path) {
  stroke-dasharray: 6 8;
  animation: flow-dash 850ms linear infinite;
}

:deep(.vue-flow__attribution) {
  display: none;
}

@keyframes token-pulse {
  0% {
    transform: translate(-50%, -50%) scale(0.65);
    opacity: 0.24;
  }
  100% {
    transform: translate(-50%, -50%) scale(1.35);
    opacity: 0;
  }
}

@keyframes flow-dash {
  to {
    stroke-dashoffset: -14;
  }
}

@keyframes cache-store-pulse {
  0% {
    width: 30px;
    height: 30px;
    border-color: rgb(52 211 153 / 100%);
  }
  100% {
    width: 78px;
    height: 78px;
    border-color: rgb(52 211 153 / 0%);
  }
}

@media (max-width: 640px) {
  .traffic-flow-shell {
    height: 420px;
  }

  .flow-status {
    left: 12px;
    right: auto;
  }
}

@media (prefers-reduced-motion: reduce) {
  .traffic-token-halo {
    animation: none;
  }

  :deep(.vue-flow__edge.animated .vue-flow__edge-path) {
    animation: none;
  }

  .traffic-cache-store-pulse {
    display: none;
  }
}
</style>
