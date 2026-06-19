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
  RATE_LIMIT_KEY,
  TRAFFIC_SHAPER_KEY,
  WAF_KEY,
  TrafficRequestPathCache,
  agentKey,
  targetKey,
  createTrafficFlowConfigIndex,
  agentsMatchingTargetSelector,
  isRedirectRoute,
  isTerminalTraceRequest,
  listenerDefaultRouteKey,
  listenerKey,
  redirectKey,
  routeTargetAssignments,
  requestUsesRateLimitNode,
  requestUsesCacheNode,
  requestUsesTrafficShaperNode,
  requestUsesWafNode,
  routeConfigForRequest,
  routeKey,
  type TrafficRequestPathCacheEntry,
} from "@/lib/trafficFlowLayout";
import {
  buildMotionNodeBox,
  buildMotionPlan,
  motionEdgeKey,
  pointAtMotionDistance,
  type MotionNodeBox,
  type MotionPlan,
  type MotionPoint,
} from "@/lib/trafficMotion";
import {
  PublicRouteRedirectTargetMode,
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  TrafficTraceStage,
  type Agent,
  type GetPublicProxyConfigResponse,
  type PublicRoute,
  type PublicRouteTarget,
} from "@/gen/proto/p2pstream/v1/management_pb";
import type { TrafficFlowEditRequest, TrafficFlowEditTarget } from "@/types/trafficFlowEdit";
import type { TraceRequest } from "@/types/trafficTrace";
import "@vue-flow/core/dist/style.css";
import "@vue-flow/core/dist/theme-default.css";

export type TrafficNodeKind = "ingress" | "listener" | "waf" | "rate-limit" | "traffic-shaper" | "cache" | "route" | "target" | "redirect" | "agent" | "upstream" | "response";
type AgentNodeStatus = {
  state: "connected" | "offline" | "disabled" | "unknown";
  label: string;
};
export type CacheNodeTone = "hit" | "miss" | "bypass" | "stored" | "lookup" | "neutral";
type CacheNodeStatus = {
  label: string;
  tone: CacheNodeTone;
};

export type TrafficNodeData = {
  label: string;
  subLabel: string;
  kind: TrafficNodeKind;
  editTargets: TrafficFlowEditTarget[];
  agentStatus?: AgentNodeStatus;
  cacheStatus?: CacheNodeStatus;
};

type DiagramNode = TrafficNodeData & {
  key: string;
  column: number;
  x: number;
  y: number;
};
type DiagramNodeInput = Omit<DiagramNode, "x" | "y" | "editTargets"> & {
  editTargets?: TrafficFlowEditTarget[];
};

type DiagramEdgeRoute = "default" | "agent-bypass" | "intermediate-bypass";
type DiagramEdge = {
  from: string;
  to: string;
  route: DiagramEdgeRoute;
};
type Point = { x: number; y: number };
type CubicSegment = {
  source: Point;
  sourceControl: Point;
  targetControl: Point;
  target: Point;
  lengthTable: Array<{ t: number; length: number }>;
  totalLength: number;
};
type EdgeRouteGeometry = {
  from: string;
  to: string;
  route: DiagramEdgeRoute;
  segments: CubicSegment[];
  totalLength: number;
  path: string;
};
type Bounds = {
  left: number;
  right: number;
  top: number;
  bottom: number;
};
type VisualTokenStatus = "in-flight" | "success" | "client-error" | "server-error" | "failed";
type VisualTokenCacheTone = "hit" | "miss" | "bypass" | "stored" | "";

type VisualToken = {
  requestId: string;
  request: TraceRequest;
  path: string[];
  label: string;
  motionPlan: MotionPlan;
  currentDistance: number;
  targetDistance: number;
  startedAt: number;
  updatedAt: number;
  durationMs: number;
  finishedAt: number | null;
  status: VisualTokenStatus;
  cacheTone: VisualTokenCacheTone;
  skipped: boolean;
};
type CacheStorePulse = {
  requestId: string;
  startedAt: number;
};

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

const NODE_WIDTH = 152;
const NODE_HEIGHT = 58;
const COLUMN_X = [0, 190, 380, 570, 760, 950, 1140, 1330, 1520, 1710, 1900];
const ROW_GAP = 92;
const MIN_CENTER_Y = 92;
const CACHE_SIDE_CENTER_Y = MIN_CENTER_Y;
const FLOW_ID = "traffic-flow-diagram";
const EDGE_CURVATURE = 0.25;
const BEZIER_LENGTH_SAMPLES = 32;
const BYPASS_LANE_GAP = 54;
const BYPASS_X_GAP = 24;
const MIN_BYPASS_Y = 28;

const MIN_PLAYBACK_MS = 1800;
const MAX_PLAYBACK_MS = 6000;
const BASE_PLAYBACK_MS = 1200;
const PER_HOP_MS = 450;
const LOW_BURST_THRESHOLD = 12;
const HIGH_BURST_THRESHOLD = 40;
const MAX_RENDERED_TOKENS_NORMAL = 36;
const MAX_RENDERED_TOKENS_STRESSED = 16;
const FRAME_STRESS_MS = 40;
const FRAME_RECOVERY_MS = 20;
const FRAME_RECOVERY_COUNT = 12;
const COMPLETION_HOLD_MS = 2000;
const COMPLETION_FADE_MS = 650;
const CACHE_PROXIMITY_PX = 40;
const CACHE_STORE_PULSE_MS = 900;

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
const visualTokens = ref<VisualToken[]>([]);
const cacheStorePulses = ref<CacheStorePulse[]>([]);
const rafNow = ref(typeof performance === "undefined" ? Date.now() : performance.now());
const skippedVisualizations = ref(0);
const isAnimationStressed = ref(false);
const seenRequestIds = new Set<string>();
const seenCacheStorePulseRequestIds = new Set<string>();
let rafId: number | null = null;
let didInitialFit = false;
let previousFrameAt: number | null = null;
let recoveryFrames = 0;

const layout = computed(() => {
  const nodes = new Map<string, Omit<DiagramNode, "x" | "y">>();
  const edges: DiagramEdge[] = [];

  const addNode = (node: DiagramNodeInput) => {
    const existing = nodes.get(node.key);
    if (!existing) {
      nodes.set(node.key, { ...node, editTargets: dedupeEditTargets(node.editTargets ?? []) });
      return;
    }
    nodes.set(node.key, {
      ...existing,
      editTargets: dedupeEditTargets([...existing.editTargets, ...(node.editTargets ?? [])]),
      agentStatus: mergeAgentStatus(existing.agentStatus, node.agentStatus),
      cacheStatus: mergeCacheStatus(existing.cacheStatus, node.cacheStatus),
    });
  };
  const addEdge = (from: string, to: string, route: DiagramEdgeRoute = "default") => {
    if (!from || !to || from === to) return;
    edges.push({ from, to, route });
  };
  const index = configIndex.value;

  addNode({ key: "ingress", label: "Ingress", subLabel: "Public request", column: 0, kind: "ingress", editTargets: [] });
  addNode({ key: "response", label: "Response", subLabel: "Client", column: 10, kind: "response", editTargets: [] });

  const listeners = props.config?.listeners ?? [];
  const targets = props.config?.routeTargets ?? [];
  const enabledRateLimitTargets = index.enabledRateLimitTargets;
  const enabledWafTargets = index.enabledWafTargets;
  const enabledCacheTargets = index.enabledCacheTargets;
  const enabledTrafficShaperTargets = index.enabledTrafficShaperTargets;
  const showWafNode = index.hasEnabledWafRules || props.requests.some((request) => requestUsesWafNode(request, index));
  const showRateLimitNode = index.hasEnabledRateLimitRules || props.requests.some((request) => requestUsesRateLimitNode(request, index));
  const showTrafficShaperNode = index.hasEnabledTrafficShaperRules || props.requests.some((request) => requestUsesTrafficShaperNode(request, index));
  const showCacheNode = index.hasEnabledCacheRules || props.requests.some((request) => requestUsesCacheNode(request, index));

  if (showWafNode) {
    addNode({
      key: WAF_KEY,
      label: "WAF",
      subLabel: enabledWafTargets.length ? `${enabledWafTargets.length.toString()} enabled` : "Observed",
      column: 2,
      kind: "waf",
      editTargets: enabledWafTargets,
    });
  }

  if (showRateLimitNode) {
    addNode({
      key: RATE_LIMIT_KEY,
      label: "Rate limit",
      subLabel: enabledRateLimitTargets.length ? `${enabledRateLimitTargets.length.toString()} enabled` : "Observed",
      column: 3,
      kind: "rate-limit",
      editTargets: enabledRateLimitTargets,
    });
  }

  if (showTrafficShaperNode) {
    addNode({
      key: TRAFFIC_SHAPER_KEY,
      label: "Traffic shaper",
      subLabel: enabledTrafficShaperTargets.length ? `${enabledTrafficShaperTargets.length.toString()} enabled` : "Observed",
      column: 4,
      kind: "traffic-shaper",
      editTargets: enabledTrafficShaperTargets,
    });
  }

  if (showCacheNode) {
    addNode({
      key: CACHE_KEY,
      label: "Cache",
      subLabel: enabledCacheTargets.length ? `${enabledCacheTargets.length.toString()} enabled` : "Observed",
      column: 7,
      kind: "cache",
      editTargets: enabledCacheTargets,
      cacheStatus: { label: "READY", tone: "neutral" },
    });
  }

  for (const listener of listeners) {
    const listenerNodeKey = listenerKey(listener.id);
    const routeEntryKey = showTrafficShaperNode ? TRAFFIC_SHAPER_KEY : showRateLimitNode ? RATE_LIMIT_KEY : showWafNode ? WAF_KEY : listenerNodeKey;
    const rateEntryKey = showWafNode ? WAF_KEY : listenerNodeKey;
    const shaperEntryKey = showRateLimitNode ? RATE_LIMIT_KEY : showWafNode ? WAF_KEY : listenerNodeKey;
    addNode({
      key: listenerNodeKey,
      label: listener.name || `Listener ${listener.id.toString()}`,
      subLabel: `${listener.bindAddress || "*"}:${listener.port.toString()}`,
      column: 1,
      kind: "listener",
      editTargets: [listenerEditTarget(listener.id, listener.name || `Listener ${listener.id.toString()}`, `${listener.bindAddress || "*"}:${listener.port.toString()}`)],
    });
    addEdge("ingress", listenerNodeKey);
    if (showWafNode) {
      addEdge(listenerNodeKey, WAF_KEY);
    }
    if (showRateLimitNode) {
      addEdge(rateEntryKey, RATE_LIMIT_KEY);
    }
    if (showTrafficShaperNode) {
      addEdge(shaperEntryKey, TRAFFIC_SHAPER_KEY);
    }

    const defaultRouteKey = listenerDefaultRouteKey(listener.id);
    addNode({
      key: defaultRouteKey,
      label: "Default route",
      subLabel: "Listener fallbacks",
      column: 5,
      kind: "route",
      editTargets: [listenerEditTarget(listener.id, listener.name || `Listener ${listener.id.toString()}`, "Default target")],
    });
    addEdge(routeEntryKey, defaultRouteKey);
    const defaultRoute = (index.routesByListenerId.get(listener.id.toString()) ?? []).find((route) => route.isDefault);
    if (defaultRoute) {
      for (const target of routeTargetAssignments(defaultRoute, index).filter((item) => item.enabled)) {
        addEdge(defaultRouteKey, targetKey(target.id));
      }
    } else {
      addEdge(defaultRouteKey, "response", "agent-bypass");
    }

    for (const route of index.routesByListenerId.get(listener.id.toString()) ?? []) {
      if (route.isDefault) continue;
      const key = routeKey(route.id);
      addNode({
        key,
        label: routeLabel(route.hostPattern, route.pathPrefix, route.id),
        subLabel: `P${route.priority.toString()}`,
        column: 5,
        kind: "route",
        editTargets: [routeEditTarget(route)],
      });
      addEdge(routeEntryKey, key);
      if (isRedirectRoute(route)) {
        addRedirectNode(route, addNode);
        addEdge(key, redirectKey(route.id));
        addEdge(redirectKey(route.id), "response", "intermediate-bypass");
      } else {
        const assignments = routeTargetAssignments(route, index).filter((target) => target.enabled);
        if (assignments.length) {
          for (const target of assignments) {
            addEdge(key, targetKey(target.id));
          }
        } else {
          addEdge(key, "response", "agent-bypass");
        }
      }
    }
  }

  for (const target of targets) {
    const key = targetKey(target.id);
    const isStatic = target.targetType === PublicRouteTargetType.STATIC;
    const isAgentTarget = target.transport === PublicRouteTargetTransport.AGENT;
    addNode({
      key,
      label: target.name || `Target ${target.id.toString()}`,
      subLabel: isStatic ? "Static" : isAgentTarget ? "Agent selected" : "Direct",
      column: 6,
      kind: "target",
      editTargets: [targetEditTarget(target.id, target.name || `Target ${target.id.toString()}`, isStatic ? "Static" : isAgentTarget ? "Agent selected" : "Direct")],
    });

    if (isStatic) {
      addStaticNode(addNode, targetEditTarget(target.id, target.name || `Target ${target.id.toString()}`, "Static"));
      addEdge(key, "static-response", "agent-bypass");
      addEdge("static-response", "response");
      continue;
    }

    if (isAgentTarget) {
      const matchedAgents = agentsMatchingTarget(target, props.config?.agents ?? []);
      if (matchedAgents.length) {
        for (const agent of matchedAgents) {
          const agentNodeKey = agentKey(agent.id);
          addNode({
            key: agentNodeKey,
            label: agent.name || `Agent ${agent.id.toString()}`,
            subLabel: agent.publicId,
            column: 8,
            kind: "agent",
            agentStatus: agentNodeStatus(agent),
            editTargets: [agentEditTarget(agent.id, agent.name || `Agent ${agent.id.toString()}`, agent.publicId)],
          });
          addUpstreamNode(addNode, targetEditTarget(target.id, target.name || `Target ${target.id.toString()}`, "Agent selected"));
          if (showCacheNode) {
            addEdge(key, CACHE_KEY);
            addEdge(CACHE_KEY, "response", "intermediate-bypass");
          }
          addEdge(key, agentNodeKey);
          addEdge(agentNodeKey, "upstream");
          addEdge("upstream", "response");
        }
      } else {
        if (showCacheNode) {
          addEdge(key, CACHE_KEY);
          addEdge(CACHE_KEY, "response", "agent-bypass");
        }
        addEdge(key, "response", "agent-bypass");
      }
      continue;
    }

    addUpstreamNode(addNode, targetEditTarget(target.id, target.name || `Target ${target.id.toString()}`, "Direct"));
    if (showCacheNode) {
      addEdge(key, CACHE_KEY);
      addEdge(CACHE_KEY, "response", "intermediate-bypass");
    }
    addEdge(key, "upstream", "agent-bypass");
    addEdge("upstream", "response");
  }

  for (const request of props.requests) {
    addObservedNodes(request, addNode);
    const path = cachedRequestPath(request).path;
    for (let index = 0; index < path.length - 1; index += 1) {
      addEdge(path[index], path[index + 1], routeForRequestSegment(request, path[index], path[index + 1]));
    }
  }

  if (!listeners.length && !props.requests.length) {
    const policyPath = [
      showWafNode ? WAF_KEY : "",
      showRateLimitNode ? RATE_LIMIT_KEY : "",
      showTrafficShaperNode ? TRAFFIC_SHAPER_KEY : "",
    ].filter(Boolean);
    if (policyPath.length) {
      addEdge("ingress", policyPath[0]);
      for (let index = 0; index < policyPath.length - 1; index += 1) {
        addEdge(policyPath[index], policyPath[index + 1]);
      }
      addEdge(policyPath[policyPath.length - 1], "response", "intermediate-bypass");
    } else {
      addEdge("ingress", "response");
    }
  }

  const byColumn = new Map<number, Omit<DiagramNode, "x" | "y">[]>();
  for (const node of nodes.values()) {
    const group = byColumn.get(node.column) ?? [];
    group.push(node);
    byColumn.set(node.column, group);
  }

  const positioned: DiagramNode[] = [];
  for (const [column, group] of byColumn.entries()) {
    group.sort((a, b) => kindOrder(a.kind) - kindOrder(b.kind) || a.label.localeCompare(b.label));
    group.forEach((node, index) => {
      const centerY = node.key === CACHE_KEY ? CACHE_SIDE_CENTER_Y : yPosition(index, group.length);
      positioned.push({
        ...node,
        x: COLUMN_X[column] ?? COLUMN_X[0],
        y: centerY - NODE_HEIGHT / 2,
      });
    });
  }

  const nodeByKey = new Map(positioned.map((node) => [node.key, node]));
  const uniqueEdges = dedupeEdges(edges).filter((edge) => nodeByKey.has(edge.from) && nodeByKey.has(edge.to));
  return { nodes: positioned, nodeByKey, edges: uniqueEdges };
});

const activeCacheStatus = computed<CacheNodeStatus | undefined>(() => {
  const cacheNode = layout.value.nodeByKey.get(CACHE_KEY);
  if (!cacheNode) return undefined;
  const now = rafNow.value;
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
    const point = tokenPoint(token, now);
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

const edgeRoutes = computed(() => {
  const routes = new Map<string, EdgeRouteGeometry>();
  for (const edge of layout.value.edges) {
    const sourceNode = layout.value.nodeByKey.get(edge.from);
    const targetNode = layout.value.nodeByKey.get(edge.to);
    if (!sourceNode || !targetNode) continue;

    const source = sourceHandlePoint(sourceNode);
    const target = targetHandlePoint(targetNode);
    const bypassBounds = bypassBoundsForEdge(edge);
    const route =
      edge.route !== "default" && bypassBounds
        ? buildBypassRoute(edge, source, target, bypassBounds)
        : buildDefaultBezierRoute(edge, source, target);
    routes.set(
      edgeKey(edge.from, edge.to),
      route,
    );
  }
  return routes;
});

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
      const point = tokenPoint(token, now);
      if (!point) return null;
      return {
        token,
        x: point.x * transform.zoom + transform.x,
        y: point.y * transform.zoom + transform.y,
        colorClass: tokenColorClass(token),
        label: token.label,
        opacity: tokenOpacity(token, now),
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
      if (age < 0 || age > CACHE_STORE_PULSE_MS) return null;
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
    void nextTick(() => fitDiagram(180));
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
});

onBeforeUnmount(() => {
  document.removeEventListener("visibilitychange", handleVisibilityChange);
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
  const durationMs = playbackDuration(cached.path.length - 1, visualTokens.value.length);
  return {
    requestId: request.requestId,
    request,
    path: cached.path,
    label: requestLabel(request),
    motionPlan,
    currentDistance: 0,
    targetDistance: motionPlan.targetLength,
    startedAt: now,
    updatedAt: now,
    durationMs,
    finishedAt: null,
    status: statusForRequest(request),
    cacheTone: cacheToneForRequest(request),
    skipped: false,
  };
}

function updateToken(token: VisualToken, request: TraceRequest) {
  advanceToken(token, nowMs());
  const cached = cachedRequestPath(request);
  const motionPlan = motionPlanForCachedPath(request.requestId, cached);
  token.request = request;
  token.path = cached.path;
  token.label = requestLabel(request);
  token.motionPlan = motionPlan;
  token.currentDistance = clamp(token.currentDistance, 0, motionPlan.targetLength);
  token.targetDistance = motionPlan.targetLength;
  token.durationMs = Math.max(token.durationMs, playbackDuration(token.path.length - 1, visualTokens.value.length));
  token.status = statusForRequest(request);
  token.cacheTone = cacheToneForRequest(request);
}

function maybeEnqueueCacheStorePulse(request: TraceRequest): boolean {
  if (cacheToneForRequest(request) !== "stored") return false;
  if (!requestUsesCacheNode(request, configIndex.value)) return false;
  if (seenCacheStorePulseRequestIds.has(request.requestId)) return false;
  seenCacheStorePulseRequestIds.add(request.requestId);
  cacheStorePulses.value.push({ requestId: request.requestId, startedAt: nowMs() });
  return true;
}

function pruneCacheStorePulses(now: number) {
  const active = cacheStorePulses.value.filter((pulse) => now - pulse.startedAt <= CACHE_STORE_PULSE_MS);
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
    advanceToken(token, now);
  }
  pruneCacheStorePulses(now);

  const activeTokens = visualTokens.value.filter((token) => {
    if (token.finishedAt === null) return true;
    return now - token.finishedAt <= COMPLETION_HOLD_MS + COMPLETION_FADE_MS;
  });
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

function advanceToken(token: VisualToken, now: number) {
  if (token.finishedAt !== null) {
    token.updatedAt = now;
    return;
  }

  const elapsed = Math.max(0, now - token.updatedAt);
  token.updatedAt = now;
  if (token.currentDistance < token.targetDistance) {
    const speed = Math.max(token.motionPlan.totalLength, token.targetDistance, 1) / token.durationMs;
    token.currentDistance = Math.min(token.targetDistance, token.currentDistance + speed * elapsed);
  }

  if (isTerminalTraceRequest(token.request) && token.currentDistance >= token.targetDistance && token.finishedAt === null) {
    token.finishedAt = now;
  }
}

function tokenPoint(token: VisualToken, _now: number): Point | null {
  const point = pointAtMotionDistance(token.motionPlan, token.currentDistance);
  if (point) return point;
  const fallbackKey = token.path[Math.min(token.motionPlan.targetNodeIndex, token.path.length - 1)] ?? "ingress";
  return motionNodeBoxes.value.get(fallbackKey)?.center ?? null;
}

function tokenOpacity(token: VisualToken, now: number): number {
  if (token.finishedAt === null) return 1;
  const age = now - token.finishedAt;
  if (age <= COMPLETION_HOLD_MS) return 1;
  return clamp(1 - (age - COMPLETION_HOLD_MS) / COMPLETION_FADE_MS, 0, 1);
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

function requestLabel(request: TraceRequest): string {
  return `${request.method || "REQUEST"} ${request.path || "/"}`;
}

function nowMs(): number {
  return typeof performance === "undefined" ? Date.now() : performance.now();
}

function currentTokenCap(): number {
  return isAnimationStressed.value ? MAX_RENDERED_TOKENS_STRESSED : MAX_RENDERED_TOKENS_NORMAL;
}

function updateFrameStress(now: number) {
  if (previousFrameAt === null) {
    previousFrameAt = now;
    return;
  }
  const delta = now - previousFrameAt;
  previousFrameAt = now;
  if (delta > FRAME_STRESS_MS) {
    isAnimationStressed.value = true;
    recoveryFrames = 0;
    return;
  }
  if (delta < FRAME_RECOVERY_MS) {
    recoveryFrames += 1;
    if (recoveryFrames >= FRAME_RECOVERY_COUNT) {
      isAnimationStressed.value = false;
    }
    return;
  }
  recoveryFrames = 0;
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
  previousFrameAt = null;
  recoveryFrames = 0;
  seedCurrentRequestsAsSeen();
  if (props.tracingEnabled) {
    syncTokens();
  }
}

function routeForRequestSegment(request: TraceRequest, from: string, to: string): DiagramEdgeRoute {
  if (from.startsWith("redirect:") && to === "response") return "intermediate-bypass";
  if (from === RATE_LIMIT_KEY && to === "response") return "intermediate-bypass";
  if (from === TRAFFIC_SHAPER_KEY && to === "response") return "intermediate-bypass";
  if (from === CACHE_KEY && to === "response") return "intermediate-bypass";
  const targetNode = request.routeTargetId > 0n ? targetKey(request.routeTargetId) : "";
  if (from !== targetNode) return "default";
  if (to === CACHE_KEY) return "default";
  if (request.agentId > 0n) return "default";
  if (to === "static-response" || to === "upstream" || to === "response") return "agent-bypass";
  return "default";
}

function sourceHandlePoint(node: DiagramNode): Point {
  return {
    x: node.x + NODE_WIDTH,
    y: node.y + NODE_HEIGHT / 2,
  };
}

function targetHandlePoint(node: DiagramNode): Point {
  return {
    x: node.x,
    y: node.y + NODE_HEIGHT / 2,
  };
}

function calculateControlOffset(distance: number, curvature: number): number {
  if (distance >= 0) return 0.5 * distance;
  return curvature * 25 * Math.sqrt(-distance);
}

function bezierControls(source: Point, target: Point): { sourceControl: Point; targetControl: Point } {
  const offset = calculateControlOffset(target.x - source.x, EDGE_CURVATURE);
  return {
    sourceControl: {
      x: source.x + offset,
      y: source.y,
    },
    targetControl: {
      x: target.x - offset,
      y: target.y,
    },
  };
}

function buildDefaultBezierRoute(edge: DiagramEdge, source: Point, target: Point): EdgeRouteGeometry {
  const controls = bezierControls(source, target);
  return buildEdgeRouteGeometry(edge, [
    buildCubicSegment(source, controls.sourceControl, controls.targetControl, target),
  ]);
}

function bypassBoundsForEdge(edge: DiagramEdge): Bounds | null {
  if (edge.route === "agent-bypass") {
    return boundsForNodes(layout.value.nodes.filter((node) => node.kind === "agent" && node.key !== edge.from && node.key !== edge.to));
  }
  if (edge.route === "intermediate-bypass") {
    return boundsForNodes(layout.value.nodes.filter((node) => {
      if (node.key === edge.from || node.key === edge.to) return false;
      return node.kind === "route" || node.kind === "target" || node.kind === "redirect" || node.kind === "agent" || node.kind === "upstream";
    }));
  }
  return null;
}

function buildBypassRoute(edge: DiagramEdge, source: Point, target: Point, obstacleBounds: Bounds): EdgeRouteGeometry {
  const laneY = bypassLaneY(source, target, obstacleBounds);
  const entryX = obstacleBounds.left - BYPASS_X_GAP;
  const exitX = obstacleBounds.right + BYPASS_X_GAP;
  const laneEntry = { x: entryX, y: laneY };
  const laneExit = { x: exitX, y: laneY };

  return buildEdgeRouteGeometry(edge, [
    buildCubicSegment(
      source,
      { x: source.x + 32, y: source.y },
      { x: laneEntry.x - 32, y: laneEntry.y },
      laneEntry,
    ),
    buildCubicSegment(
      laneEntry,
      { x: laneEntry.x + 40, y: laneEntry.y },
      { x: laneExit.x - 40, y: laneExit.y },
      laneExit,
    ),
    buildCubicSegment(
      laneExit,
      { x: laneExit.x + 32, y: laneExit.y },
      { x: target.x - 32, y: target.y },
      target,
    ),
  ]);
}

function buildEdgeRouteGeometry(edge: DiagramEdge, segments: CubicSegment[]): EdgeRouteGeometry {
  return {
    from: edge.from,
    to: edge.to,
    route: edge.route,
    segments,
    totalLength: segments.reduce((sum, segment) => sum + segment.totalLength, 0),
    path: routePath(segments),
  };
}

function buildCubicSegment(
  source: Point,
  sourceControl: Point,
  targetControl: Point,
  target: Point,
): CubicSegment {
  const segment: CubicSegment = {
    source,
    sourceControl,
    targetControl,
    target,
    lengthTable: [{ t: 0, length: 0 }],
    totalLength: 0,
  };
  let previous = source;
  let totalLength = 0;

  for (let index = 1; index <= BEZIER_LENGTH_SAMPLES; index += 1) {
    const t = index / BEZIER_LENGTH_SAMPLES;
    const point = cubicBezierPoint(segment, t);
    totalLength += pointDistance(previous, point);
    segment.lengthTable.push({ t, length: totalLength });
    previous = point;
  }

  segment.totalLength = totalLength;
  return segment;
}

function cubicBezierPoint(segment: CubicSegment, t: number): Point {
  const inv = 1 - t;
  return {
    x:
      inv * inv * inv * segment.source.x +
      3 * inv * inv * t * segment.sourceControl.x +
      3 * inv * t * t * segment.targetControl.x +
      t * t * t * segment.target.x,
    y:
      inv * inv * inv * segment.source.y +
      3 * inv * inv * t * segment.sourceControl.y +
      3 * inv * t * t * segment.targetControl.y +
      t * t * t * segment.target.y,
  };
}

function pointAtRouteProgress(route: EdgeRouteGeometry, progress: number): Point {
  const targetLength = route.totalLength * clamp(progress, 0, 1);
  let traversed = 0;

  for (const segment of route.segments) {
    const nextTraversed = traversed + segment.totalLength;
    if (targetLength <= nextTraversed) {
      const segmentProgress = segment.totalLength <= 0 ? 0 : (targetLength - traversed) / segment.totalLength;
      return pointAtSegmentProgress(segment, segmentProgress);
    }
    traversed = nextTraversed;
  }

  return route.segments.at(-1)?.target ?? { x: 0, y: 0 };
}

function routeToMotionPoints(route: EdgeRouteGeometry): MotionPoint[] {
  const sampleCount = Math.max(2, Math.ceil(route.totalLength / 24));
  const points: MotionPoint[] = [];
  for (let index = 0; index <= sampleCount; index += 1) {
    points.push(pointAtRouteProgress(route, index / sampleCount));
  }
  return points;
}

function pointAtSegmentProgress(segment: CubicSegment, progress: number): Point {
  const targetLength = segment.totalLength * clamp(progress, 0, 1);
  if (segment.totalLength <= 0) return cubicBezierPoint(segment, progress);

  let previous = segment.lengthTable[0];
  for (let index = 1; index < segment.lengthTable.length; index += 1) {
    const current = segment.lengthTable[index];
    if (current.length < targetLength) {
      previous = current;
      continue;
    }

    const lengthSpan = current.length - previous.length;
    const localProgress = lengthSpan <= 0 ? 0 : (targetLength - previous.length) / lengthSpan;
    const t = previous.t + (current.t - previous.t) * localProgress;
    return cubicBezierPoint(segment, t);
  }

  return segment.target;
}

function routePath(segments: CubicSegment[]): string {
  return segments
    .map((segment, index) => {
      const start = index === 0 ? `M${segment.source.x},${segment.source.y} ` : "";
      return `${start}C${segment.sourceControl.x},${segment.sourceControl.y} ${segment.targetControl.x},${segment.targetControl.y} ${segment.target.x},${segment.target.y}`;
    })
    .join(" ");
}

function pointDistance(a: Point, b: Point): number {
  return Math.hypot(b.x - a.x, b.y - a.y);
}

function nodeBounds(node: DiagramNode): Bounds {
  return {
    left: node.x,
    right: node.x + NODE_WIDTH,
    top: node.y,
    bottom: node.y + NODE_HEIGHT,
  };
}

function boundsForNodes(nodes: DiagramNode[]): Bounds | null {
  if (!nodes.length) return null;
  return nodes.reduce((bounds, node) => {
    const next = nodeBounds(node);
    return {
      left: Math.min(bounds.left, next.left),
      right: Math.max(bounds.right, next.right),
      top: Math.min(bounds.top, next.top),
      bottom: Math.max(bounds.bottom, next.bottom),
    };
  }, nodeBounds(nodes[0]));
}

function bypassLaneY(source: Point, target: Point, agentBounds: Bounds): number {
  const averageY = (source.y + target.y) / 2;
  const topLane = Math.min(source.y, target.y, agentBounds.top) - BYPASS_LANE_GAP;
  const bottomLane = Math.max(source.y, target.y, agentBounds.bottom) + BYPASS_LANE_GAP;

  if (topLane >= MIN_BYPASS_Y) {
    const topCost = Math.abs(averageY - topLane);
    const bottomCost = Math.abs(averageY - bottomLane);
    return topCost <= bottomCost ? topLane : bottomLane;
  }

  return bottomLane;
}

function fitDiagram(duration = 240) {
  void fitView({ padding: 0.18, duration, maxZoom: 1.1 });
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

function addObservedNodes(
  request: TraceRequest,
  addNode: (node: DiagramNodeInput) => void,
) {
  const index = configIndex.value;
  if (requestUsesWafNode(request, index)) {
    addNode({
      key: WAF_KEY,
      label: "WAF",
      subLabel: request.wafRuleName || "Observed",
      column: 2,
      kind: "waf",
      editTargets: request.wafRuleId > 0n
        ? [wafEditTarget(request.wafRuleId, request.wafRuleName || `WAF ${request.wafRuleId.toString()}`, "Observed")]
        : index.enabledWafTargets,
    });
  }

  if (requestUsesRateLimitNode(request, index)) {
    addNode({
      key: RATE_LIMIT_KEY,
      label: "Rate limit",
      subLabel: request.rateLimitRuleName || "Observed",
      column: 3,
      kind: "rate-limit",
      editTargets: index.enabledRateLimitTargets,
    });
  }

  if (requestUsesTrafficShaperNode(request, index)) {
    addNode({
      key: TRAFFIC_SHAPER_KEY,
      label: "Traffic shaper",
      subLabel: request.trafficShaperRuleName || "Observed",
      column: 4,
      kind: "traffic-shaper",
      editTargets: request.trafficShaperRuleId > 0n
        ? [trafficShaperEditTarget(request.trafficShaperRuleId, request.trafficShaperRuleName || `Traffic shaper ${request.trafficShaperRuleId.toString()}`, "Observed")]
        : index.enabledTrafficShaperTargets,
    });
  }

  if (requestUsesCacheNode(request, index)) {
    addNode({
      key: CACHE_KEY,
      label: "Cache",
      subLabel: request.cacheRuleName || request.cacheStatus || "Observed",
      column: 7,
      kind: "cache",
      cacheStatus: cacheStatusForRequest(request),
      editTargets: request.cacheRuleId > 0n
        ? [cacheEditTarget(request.cacheRuleId, request.cacheRuleName || `Cache ${request.cacheRuleId.toString()}`, request.cacheStatus || "Observed")]
        : index.enabledCacheTargets,
    });
  }

  if (request.listenerId > 0n) {
    addNode({
      key: listenerKey(request.listenerId),
      label: request.listenerName || `Listener ${request.listenerId.toString()}`,
      subLabel: "Observed",
      column: 1,
      kind: "listener",
      editTargets: [listenerEditTarget(request.listenerId, request.listenerName || `Listener ${request.listenerId.toString()}`, "Observed")],
    });
  }

  if (request.routeId > 0n) {
    addNode({
      key: routeKey(request.routeId),
      label: request.routeLabel || `Route ${request.routeId.toString()}`,
      subLabel: "Observed",
      column: 5,
      kind: "route",
      editTargets: [routeEditTargetByID(request.routeId, request.routeLabel || `Route ${request.routeId.toString()}`, "Observed")],
    });
    const route = routeConfigForRequest(request, index);
    if (route && isRedirectRoute(route)) {
      addRedirectNode(route, addNode);
    }
  } else if (request.defaultRoute && request.listenerId > 0n) {
    addNode({
      key: listenerDefaultRouteKey(request.listenerId),
      label: "Default route",
      subLabel: "Observed fallback",
      column: 5,
      kind: "route",
      editTargets: request.listenerId > 0n
        ? [listenerEditTarget(request.listenerId, request.listenerName || `Listener ${request.listenerId.toString()}`, "Default route")]
        : [],
    });
  }

  const selectedTargetId = requestTargetID(request);
  if (selectedTargetId > 0n) {
    addNode({
      key: targetKey(selectedTargetId),
      label: requestTargetLabel(request),
      subLabel: targetTypeLabel(request),
      column: 6,
      kind: "target",
      editTargets: [targetEditTarget(selectedTargetId, requestTargetLabel(request), targetTypeLabel(request))],
    });
  }

  if (requestTargetType(request) === PublicRouteTargetType.STATIC) {
    addStaticNode(addNode, selectedTargetId > 0n ? targetEditTarget(selectedTargetId, requestTargetLabel(request), "Static") : undefined);
  } else if (selectedTargetId > 0n && requestTargetTransport(request) !== PublicRouteTargetTransport.AGENT) {
    addUpstreamNode(addNode, targetEditTarget(selectedTargetId, requestTargetLabel(request), "Direct"));
  }

  if (request.agentId > 0n) {
    const agent = index.agentById.get(request.agentId.toString());
    addNode({
      key: agentKey(request.agentId),
      label: request.agentName || request.agentPublicId || `Agent ${request.agentId.toString()}`,
      subLabel: request.agentPublicId || "Observed",
      column: 8,
      kind: "agent",
      agentStatus: agentNodeStatus(agent),
      editTargets: [agentEditTarget(request.agentId, request.agentName || request.agentPublicId || `Agent ${request.agentId.toString()}`, request.agentPublicId || "Observed")],
    });
    addUpstreamNode(addNode, selectedTargetId > 0n ? targetEditTarget(selectedTargetId, requestTargetLabel(request), "Agent selected") : undefined);
  }
}

function addStaticNode(addNode: (node: DiagramNodeInput) => void, target?: TrafficFlowEditTarget) {
  addNode({ key: "static-response", label: "Static", subLabel: "Generated", column: 9, kind: "upstream", editTargets: target ? [target] : [] });
}

function addUpstreamNode(addNode: (node: DiagramNodeInput) => void, target?: TrafficFlowEditTarget) {
  addNode({ key: "upstream", label: "Upstream", subLabel: "Origin", column: 9, kind: "upstream", editTargets: target ? [target] : [] });
}

function addRedirectNode(route: PublicRoute, addNode: (node: DiagramNodeInput) => void) {
  addNode({
    key: redirectKey(route.id),
    label: "Redirect",
    subLabel: redirectNodeSubLabel(route),
    column: 6,
    kind: "redirect",
    editTargets: [routeEditTarget(route)],
  });
}

function yPosition(index: number, total: number): number {
  if (total <= 1) return 236;
  const visibleRows = Math.min(total, 4);
  const centeringOffset = Math.max(0, (4 - visibleRows) * ROW_GAP * 0.5);
  return MIN_CENTER_Y + centeringOffset + index * ROW_GAP;
}

function dedupeEdges(edges: DiagramEdge[]): DiagramEdge[] {
  const byKey = new Map<string, DiagramEdge>();
  for (const edge of edges) {
    const key = edgeKey(edge.from, edge.to);
    const existing = byKey.get(key);
    if (!existing || edgeRoutePriority(edge.route) > edgeRoutePriority(existing.route)) {
      byKey.set(key, edge);
    }
  }
  return [...byKey.values()];
}

function edgeRoutePriority(route: DiagramEdgeRoute): number {
  switch (route) {
    case "intermediate-bypass": return 2;
    case "agent-bypass": return 1;
    default: return 0;
  }
}

function edgeKey(from: string, to: string): string {
  return motionEdgeKey(from, to);
}

function dedupeEditTargets(targets: TrafficFlowEditTarget[]): TrafficFlowEditTarget[] {
  const byKey = new Map<string, TrafficFlowEditTarget>();
  for (const target of targets) {
    byKey.set(`${target.kind}:${target.id}`, target);
  }
  return [...byKey.values()];
}

function agentNodeStatus(agent: Agent | undefined): AgentNodeStatus {
  if (!agent) return { state: "unknown", label: "Unknown" };
  if (!agent.enabled) return { state: "disabled", label: "Disabled" };
  if (agent.connected) return { state: "connected", label: "Connected" };
  return { state: "offline", label: "Offline" };
}

function agentsMatchingTarget(target: PublicRouteTarget, agents: readonly Agent[]): Agent[] {
  return agentsMatchingTargetSelector(target, agents);
}

function mergeAgentStatus(existing: AgentNodeStatus | undefined, next: AgentNodeStatus | undefined): AgentNodeStatus | undefined {
  if (!existing) return next;
  if (!next) return existing;
  if (existing.state === "unknown" && next.state !== "unknown") return next;
  return existing;
}

function mergeCacheStatus(existing: CacheNodeStatus | undefined, next: CacheNodeStatus | undefined): CacheNodeStatus | undefined {
  if (!existing) return next;
  if (!next) return existing;
  return cacheTonePriority(next.tone) > cacheTonePriority(existing.tone) ? next : existing;
}

function playbackDuration(hopCount: number, activeTokens: number): number {
  const raw = BASE_PLAYBACK_MS + Math.max(1, hopCount) * PER_HOP_MS;
  if (activeTokens < LOW_BURST_THRESHOLD) return clamp(raw, 2500, MAX_PLAYBACK_MS);
  if (activeTokens < HIGH_BURST_THRESHOLD) return clamp(raw, 2000, 4500);
  return clamp(raw, MIN_PLAYBACK_MS, 3000);
}

function statusForRequest(request: TraceRequest): VisualTokenStatus {
  if (request.stage === TrafficTraceStage.FAILED) return "failed";
  const status = Number(request.statusCode);
  if (status >= 500) return "server-error";
  if (status >= 400) return "client-error";
  if (status >= 200) return "success";
  return "in-flight";
}

function cacheToneForRequest(request: TraceRequest): VisualTokenCacheTone {
  const status = request.cacheStatus.toLowerCase();
  if (status === "hit" || request.stage === TrafficTraceStage.CACHE_HIT) return "hit";
  if (status === "miss" || request.stage === TrafficTraceStage.CACHE_MISS) return "miss";
  if (status === "bypass" || request.stage === TrafficTraceStage.CACHE_BYPASS) return "bypass";
  if (status === "stored" || request.stage === TrafficTraceStage.CACHE_STORED) return "stored";
  return "";
}

function cacheStatusForTone(tone: Exclude<VisualTokenCacheTone, ""> | CacheNodeTone): CacheNodeStatus {
  switch (tone) {
    case "hit": return { label: "HIT", tone: "hit" };
    case "miss": return { label: "MISS", tone: "miss" };
    case "bypass": return { label: "BYPASS", tone: "bypass" };
    case "stored": return { label: "STORED", tone: "stored" };
    case "lookup": return { label: "LOOKUP", tone: "lookup" };
    default: return { label: "READY", tone: "neutral" };
  }
}

function cacheStatusForRequest(request: TraceRequest): CacheNodeStatus {
  return cacheStatusForTone(cacheToneForRequest(request) || "lookup");
}

function cacheTonePriority(tone: CacheNodeTone): number {
  switch (tone) {
    case "hit": return 6;
    case "stored": return 5;
    case "miss": return 4;
    case "bypass": return 3;
    case "lookup": return 2;
    default: return 1;
  }
}

function tokenColorClass(token: VisualToken): string {
  if (token.status === "client-error" || token.status === "server-error" || token.status === "failed") {
    return `traffic-token-${token.status}`;
  }
  return token.cacheTone ? `traffic-token-cache-${token.cacheTone}` : `traffic-token-${token.status}`;
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

function pointInsideBounds(point: Point, bounds: Bounds): boolean {
  return point.x >= bounds.left && point.x <= bounds.right && point.y >= bounds.top && point.y <= bounds.bottom;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function kindOrder(kind: TrafficNodeKind): number {
  switch (kind) {
    case "ingress": return 0;
    case "listener": return 1;
    case "waf": return 2;
    case "rate-limit": return 3;
    case "traffic-shaper": return 4;
    case "route": return 5;
    case "target": return 6;
    case "redirect": return 6;
    case "cache": return 7;
    case "agent": return 8;
    case "upstream": return 9;
    case "response": return 10;
    default: return 99;
  }
}

function routeLabel(hostPattern: string, pathPrefix: string, id: bigint): string {
  const parts = [hostPattern, pathPrefix].filter(Boolean);
  return parts.length ? parts.join(" ") : `Route ${id.toString()}`;
}

function listenerEditTarget(id: bigint | string | number, label: string, subLabel?: string): TrafficFlowEditTarget {
  return { kind: "listener", id: id.toString(), label, subLabel };
}

function routeEditTarget(route: PublicRoute): TrafficFlowEditTarget {
  return routeEditTargetByID(route.id, routeLabel(route.hostPattern, route.pathPrefix, route.id), `P${route.priority.toString()}`);
}

function routeEditTargetByID(id: bigint | string | number, label: string, subLabel?: string): TrafficFlowEditTarget {
  return { kind: "route", id: id.toString(), label, subLabel };
}

function targetEditTarget(id: bigint | string | number, label: string, subLabel?: string): TrafficFlowEditTarget {
  return { kind: "target", id: id.toString(), label, subLabel };
}

function agentEditTarget(id: bigint | string | number, label: string, subLabel?: string): TrafficFlowEditTarget {
  return { kind: "agent", id: id.toString(), label, subLabel };
}

function trafficShaperEditTarget(id: bigint | string | number, label: string, subLabel?: string): TrafficFlowEditTarget {
  return { kind: "traffic-shaper", id: id.toString(), label, subLabel };
}

function wafEditTarget(id: bigint | string | number, label: string, subLabel?: string): TrafficFlowEditTarget {
  return { kind: "waf", id: id.toString(), label, subLabel };
}

function cacheEditTarget(id: bigint | string | number, label: string, subLabel?: string): TrafficFlowEditTarget {
  return { kind: "cache", id: id.toString(), label, subLabel };
}

function targetTypeLabel(request: TraceRequest): string {
  if (requestTargetType(request) === PublicRouteTargetType.STATIC) return "Static";
  if (requestTargetTransport(request) === PublicRouteTargetTransport.AGENT) return "Agent selected";
  return "Direct";
}

function requestTargetID(request: TraceRequest): bigint {
  return request.routeTargetId;
}

function requestTargetLabel(request: TraceRequest): string {
  const id = requestTargetID(request);
  return request.routeTargetName || `Target ${id.toString()}`;
}

function requestTargetType(request: TraceRequest): PublicRouteTargetType {
  return request.routeTargetType || PublicRouteTargetType.UNSPECIFIED;
}

function requestTargetTransport(request: TraceRequest): PublicRouteTargetTransport {
  return request.routeTargetTransport || PublicRouteTargetTransport.UNSPECIFIED;
}

function redirectNodeSubLabel(route: PublicRoute): string {
  const status = Number(route.redirectStatusCode || 302);
  return `${status} ${redirectModeShortLabel(route.redirectTargetMode)}`;
}

function redirectModeShortLabel(mode: PublicRouteRedirectTargetMode): string {
  switch (mode) {
    case PublicRouteRedirectTargetMode.EXTERNAL_ORIGIN_KEEP_PATH:
      return "origin";
    case PublicRouteRedirectTargetMode.ABSOLUTE_URL:
      return "url";
    case PublicRouteRedirectTargetMode.SAME_HOST_PATH:
      return "same-host";
    default:
      return "redirect";
  }
}
</script>

<template>
  <div class="traffic-flow-shell" :class="{ 'traffic-flow-stressed': isAnimationStressed }">
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
      :min-zoom="0.35"
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
