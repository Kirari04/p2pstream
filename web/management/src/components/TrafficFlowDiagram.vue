<script setup lang="ts">
import { computed, markRaw, nextTick, onBeforeUnmount, ref, watch } from "vue";
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
import TrafficFlowEdge from "@/components/TrafficFlowEdge.vue";
import TrafficFlowNode from "@/components/TrafficFlowNode.vue";
import {
  PublicBackendForwardMode,
  PublicBackendType,
  PublicRateLimitAlgorithm,
  PublicRouteAction,
  PublicRouteRedirectTargetMode,
  TrafficTraceStage,
  type Agent,
  type GetPublicProxyConfigResponse,
  type PublicRateLimitRule,
  type PublicRoute,
  type TrafficTraceEvent,
} from "@/gen/proto/p2pstream/v1/management_pb";
import type { TrafficFlowEditRequest, TrafficFlowEditTarget } from "@/types/trafficFlowEdit";
import "@vue-flow/core/dist/style.css";
import "@vue-flow/core/dist/theme-default.css";

type TraceRequest = {
  requestId: string;
  method: string;
  host: string;
  path: string;
  query: string;
  stage: TrafficTraceStage;
  statusCode: bigint;
  durationMs: bigint;
  errorKind: string;
  listenerId: bigint;
  listenerName: string;
  routeId: bigint;
  routeLabel: string;
  defaultRoute: boolean;
  backendId: bigint;
  backendName: string;
  backendType: PublicBackendType;
  forwardMode: PublicBackendForwardMode;
  targetOrigin: string;
  agentId: bigint;
  agentName: string;
  agentPublicId: string;
  requestBytes: bigint;
  responseBytes: bigint;
  rateLimitRuleId: bigint;
  rateLimitRuleName: string;
  rateLimitAlgorithm: PublicRateLimitAlgorithm;
  visible: boolean;
  completedAt: number | null;
  latestEvent: TrafficTraceEvent | null;
  events: TrafficTraceEvent[];
};

export type TrafficNodeKind = "ingress" | "listener" | "rate-limit" | "route" | "backend" | "redirect" | "agent" | "upstream" | "response";
type AgentNodeStatus = {
  state: "connected" | "offline" | "disabled" | "unknown";
  label: string;
};

export type TrafficNodeData = {
  label: string;
  subLabel: string;
  kind: TrafficNodeKind;
  editTargets: TrafficFlowEditTarget[];
  agentStatus?: AgentNodeStatus;
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

type VisualToken = {
  requestId: string;
  request: TraceRequest;
  path: string[];
  visualIndex: number;
  targetIndex: number;
  startedAt: number;
  segmentStartedAt: number;
  durationMs: number;
  finishedAt: number | null;
  status: VisualTokenStatus;
  skipped: boolean;
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
const COLUMN_X = [0, 200, 400, 600, 800, 1000, 1200, 1400];
const ROW_GAP = 92;
const MIN_CENTER_Y = 92;
const FLOW_ID = "traffic-flow-diagram";
const DEFAULT_ROUTE_KEY = "route:default";
const RATE_LIMIT_KEY = "rate-limit";
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
const MAX_RENDERED_TOKENS = 60;
const COMPLETION_HOLD_MS = 2000;
const COMPLETION_FADE_MS = 650;

const nodeTypes: NodeTypesObject = {
  traffic: markRaw(TrafficFlowNode),
};
const edgeTypes: EdgeTypesObject = {
  trafficBypass: markRaw(TrafficFlowEdge),
};

const { fitView, viewport } = useVueFlow(FLOW_ID);
const visualTokens = ref<VisualToken[]>([]);
const rafNow = ref(typeof performance === "undefined" ? Date.now() : performance.now());
const skippedVisualizations = ref(0);
const seenRequestIds = new Set<string>();
let rafId: number | null = null;
let didInitialFit = false;

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
    });
  };
  const addEdge = (from: string, to: string, route: DiagramEdgeRoute = "default") => {
    if (!from || !to || from === to) return;
    edges.push({ from, to, route });
  };

  addNode({ key: "ingress", label: "Ingress", subLabel: "Public request", column: 0, kind: "ingress", editTargets: [] });
  addNode({ key: "response", label: "Response", subLabel: "Client", column: 7, kind: "response", editTargets: [] });

  const listeners = props.config?.listeners ?? [];
  const routes = props.config?.routes ?? [];
  const backends = props.config?.backends ?? [];
  const agents = props.config?.agents ?? [];
  const backendAgents = props.config?.backendAgents ?? [];
  const enabledRateLimitTargets = enabledRateLimitRuleTargets();
  const showRateLimitNode = enabledRateLimitTargets.length > 0 || props.requests.some((request) => request.rateLimitRuleId > 0n || request.stage === TrafficTraceStage.RATE_LIMITED);

  if (showRateLimitNode) {
    addNode({
      key: RATE_LIMIT_KEY,
      label: "Rate limit",
      subLabel: enabledRateLimitTargets.length ? `${enabledRateLimitTargets.length.toString()} enabled` : "Observed",
      column: 2,
      kind: "rate-limit",
      editTargets: enabledRateLimitTargets,
    });
  }

  for (const listener of listeners) {
    const listenerNodeKey = listenerKey(listener.id);
    const routeEntryKey = showRateLimitNode ? RATE_LIMIT_KEY : listenerNodeKey;
    addNode({
      key: listenerNodeKey,
      label: listener.name || `Listener ${listener.id.toString()}`,
      subLabel: `${listener.bindAddress || "*"}:${listener.port.toString()}`,
      column: 1,
      kind: "listener",
      editTargets: [listenerEditTarget(listener.id, listener.name || `Listener ${listener.id.toString()}`, `${listener.bindAddress || "*"}:${listener.port.toString()}`)],
    });
    addEdge("ingress", listenerNodeKey);
    if (showRateLimitNode) {
      addEdge(listenerNodeKey, RATE_LIMIT_KEY);
    }

    addNode({
      key: DEFAULT_ROUTE_KEY,
      label: "Default route",
      subLabel: "Listener fallbacks",
      column: 3,
      kind: "route",
      editTargets: [listenerEditTarget(listener.id, listener.name || `Listener ${listener.id.toString()}`, "Default backend")],
    });
    addEdge(routeEntryKey, DEFAULT_ROUTE_KEY);
    addEdge(DEFAULT_ROUTE_KEY, backendKey(listener.defaultBackendId));

    for (const route of routes.filter((item) => item.listenerId === listener.id)) {
      const key = routeKey(route.id);
      addNode({
        key,
        label: routeLabel(route.hostPattern, route.pathPrefix, route.id),
        subLabel: `P${route.priority.toString()}`,
        column: 3,
        kind: "route",
        editTargets: [routeEditTarget(route)],
      });
      addEdge(routeEntryKey, key);
      if (isRedirectRoute(route)) {
        addRedirectNode(route, addNode);
        addEdge(key, redirectKey(route.id));
        addEdge(redirectKey(route.id), "response", "intermediate-bypass");
      } else {
        addEdge(key, backendKey(route.backendId));
      }
    }
  }

  for (const backend of backends) {
    const key = backendKey(backend.id);
    const isStatic = backend.backendType === PublicBackendType.STATIC;
    const isAgentPool = backend.forwardMode === PublicBackendForwardMode.AGENT_POOL;
    addNode({
      key,
      label: backend.name || `Backend ${backend.id.toString()}`,
      subLabel: isStatic ? "Static" : isAgentPool ? "Agent pool" : "Direct",
      column: 4,
      kind: "backend",
      editTargets: [backendEditTarget(backend.id, backend.name || `Backend ${backend.id.toString()}`, isStatic ? "Static" : isAgentPool ? "Agent pool" : "Direct")],
    });

    if (isStatic) {
      addStaticNode(addNode, backendEditTarget(backend.id, backend.name || `Backend ${backend.id.toString()}`, "Static"));
      addEdge(key, "static-response", "agent-bypass");
      addEdge("static-response", "response");
      continue;
    }

    if (isAgentPool) {
      const assignments = backend.agentAssignments.length
        ? backend.agentAssignments
        : backendAgents.filter((assignment) => assignment.backendId === backend.id);
      const enabledAssignments = assignments.filter((assignment) => assignment.enabled);

      if (enabledAssignments.length) {
        for (const assignment of enabledAssignments) {
          const agent = agents.find((item) => item.id === assignment.agentId);
          const agentNodeKey = agentKey(assignment.agentId);
          addNode({
            key: agentNodeKey,
            label: agent?.name || `Agent ${assignment.agentId.toString()}`,
            subLabel: agent?.publicId || `x${assignment.weight.toString()}`,
            column: 5,
            kind: "agent",
            agentStatus: agentNodeStatus(agent),
            editTargets: [agentEditTarget(assignment.agentId, agent?.name || `Agent ${assignment.agentId.toString()}`, agent?.publicId || "")],
          });
          addUpstreamNode(addNode, backendEditTarget(backend.id, backend.name || `Backend ${backend.id.toString()}`, "Agent pool"));
          addEdge(key, agentNodeKey);
          addEdge(agentNodeKey, "upstream");
          addEdge("upstream", "response");
        }
      } else {
        addEdge(key, "response", "agent-bypass");
      }
      continue;
    }

    addUpstreamNode(addNode, backendEditTarget(backend.id, backend.name || `Backend ${backend.id.toString()}`, "Direct"));
    addEdge(key, "upstream", "agent-bypass");
    addEdge("upstream", "response");
  }

  for (const request of props.requests) {
    addObservedNodes(request, addNode);
    const path = buildRequestPath(request);
    for (let index = 0; index < path.length - 1; index += 1) {
      addEdge(path[index], path[index + 1], routeForRequestSegment(request, path[index], path[index + 1]));
    }
  }

  if (!listeners.length && !props.requests.length && showRateLimitNode) {
    addEdge("ingress", RATE_LIMIT_KEY);
    addEdge(RATE_LIMIT_KEY, "response", "intermediate-bypass");
  } else if (!listeners.length && !props.requests.length) {
    addEdge("ingress", "response");
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
      positioned.push({
        ...node,
        x: COLUMN_X[column] ?? COLUMN_X[0],
        y: yPosition(index, group.length) - NODE_HEIGHT / 2,
      });
    });
  }

  const nodeByKey = new Map(positioned.map((node) => [node.key, node]));
  const uniqueEdges = dedupeEdges(edges).filter((edge) => nodeByKey.has(edge.from) && nodeByKey.has(edge.to));
  return { nodes: positioned, nodeByKey, edges: uniqueEdges };
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
        color: "#52525b",
        width: 16,
        height: 16,
      },
      style: {
        stroke: "#3f3f46",
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

const nodeCenters = computed(() => {
  const centers = new Map<string, Point>();
  for (const node of layout.value.nodes) {
    centers.set(node.key, { x: node.x + NODE_WIDTH / 2, y: node.y + NODE_HEIGHT / 2 });
  }
  return centers;
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
        colorClass: tokenColorClass(token.status),
        label: `${token.request.method || "REQUEST"} ${token.request.path || "/"}`,
        opacity: tokenOpacity(token, now),
      };
    })
    .filter((token): token is NonNullable<typeof token> => token !== null);
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
    void nextTick(() => fitDiagram(180));
  },
  { flush: "post" },
);

onBeforeUnmount(() => {
  stopAnimationLoop();
});

function syncTokens() {
  if (props.requests.length === 0) {
    resetVisualPlayback(false);
    return;
  }

  const requestById = new Map(props.requests.map((request) => [request.requestId, request]));
  pruneSeenRequests(requestById);
  let changed = false;
  for (const token of visualTokens.value) {
    const request = requestById.get(token.requestId);
    if (!request) continue;
    updateToken(token, request);
  }

  const oldestFirst = [...props.requests].reverse();
  for (const request of oldestFirst) {
    if (seenRequestIds.has(request.requestId)) continue;
    seenRequestIds.add(request.requestId);

    if (visualTokens.value.length >= MAX_RENDERED_TOKENS) {
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
  skippedVisualizations.value = 0;
  seenRequestIds.clear();
  if (seedCurrentRequests) {
    for (const request of props.requests) {
      seenRequestIds.add(request.requestId);
    }
  }
  stopAnimationLoop();
  emit("active-change", 0);
  emit("skipped-change", 0);
}

function pruneSeenRequests(requestById: Map<string, TraceRequest>) {
  const animatedRequestIds = new Set(visualTokens.value.map((token) => token.requestId));
  for (const requestId of seenRequestIds) {
    if (requestById.has(requestId) || animatedRequestIds.has(requestId)) continue;
    seenRequestIds.delete(requestId);
  }
}

function createToken(request: TraceRequest): VisualToken {
  const path = buildRequestPath(request);
  const now = typeof performance === "undefined" ? Date.now() : performance.now();
  const durationMs = playbackDuration(path.length - 1, visualTokens.value.length);
  return {
    requestId: request.requestId,
    request,
    path,
    visualIndex: 0,
    targetIndex: targetIndexForRequest(request, path),
    startedAt: now,
    segmentStartedAt: now,
    durationMs,
    finishedAt: null,
    status: statusForRequest(request),
    skipped: false,
  };
}

function updateToken(token: VisualToken, request: TraceRequest) {
  token.request = request;
  token.path = buildRequestPath(request);
  token.targetIndex = Math.max(token.targetIndex, targetIndexForRequest(request, token.path));
  token.durationMs = Math.max(token.durationMs, playbackDuration(token.path.length - 1, visualTokens.value.length));
  token.status = statusForRequest(request);
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
  let changed = false;

  for (const token of visualTokens.value) {
    advanceToken(token, now);
  }

  const activeTokens = visualTokens.value.filter((token) => {
    if (token.finishedAt === null) return true;
    return now - token.finishedAt <= COMPLETION_HOLD_MS + COMPLETION_FADE_MS;
  });
  if (activeTokens.length !== visualTokens.value.length) {
    visualTokens.value = activeTokens;
    changed = true;
  }

  if (visualTokens.value.length) {
    rafId = window.requestAnimationFrame(tick);
  } else {
    rafId = null;
  }

  if (changed) {
    emit("active-change", visualTokens.value.length);
  }
}

function advanceToken(token: VisualToken, now: number) {
  const finalIndex = Math.min(token.targetIndex, token.path.length - 1);
  const segmentDuration = tokenSegmentDuration(token);

  while (token.visualIndex < finalIndex && now - token.segmentStartedAt >= segmentDuration) {
    token.visualIndex += 1;
    token.segmentStartedAt += segmentDuration;
  }

  if (isTerminal(token.request) && token.visualIndex >= token.path.length - 1 && token.finishedAt === null) {
    token.finishedAt = now;
  }
}

function tokenPoint(token: VisualToken, now: number): Point | null {
  const fromKey = token.path[token.visualIndex];
  const from = nodeCenters.value.get(fromKey) ?? nodeCenters.value.get("ingress");
  if (!from) return null;

  const targetIndex = Math.min(token.targetIndex, token.path.length - 1);
  if (token.visualIndex >= targetIndex) return from;

  const toKey = token.path[token.visualIndex + 1];
  const progress = easeInOutCubic(clamp((now - token.segmentStartedAt) / tokenSegmentDuration(token), 0, 1));
  const route = edgeRoutes.value.get(edgeKey(fromKey, toKey));
  if (route) return pointAtRouteProgress(route, progress);

  const next = nodeCenters.value.get(toKey);
  if (!next) return from;

  return {
    x: from.x + (next.x - from.x) * progress,
    y: from.y + (next.y - from.y) * progress,
  };
}

function tokenSegmentDuration(token: VisualToken): number {
  return token.durationMs / Math.max(1, token.path.length - 1);
}

function tokenOpacity(token: VisualToken, now: number): number {
  if (token.finishedAt === null) return 1;
  const age = now - token.finishedAt;
  if (age <= COMPLETION_HOLD_MS) return 1;
  return clamp(1 - (age - COMPLETION_HOLD_MS) / COMPLETION_FADE_MS, 0, 1);
}

function routeForRequestSegment(request: TraceRequest, from: string, to: string): DiagramEdgeRoute {
  if (from.startsWith("redirect:") && to === "response") return "intermediate-bypass";
  if (from === RATE_LIMIT_KEY && to === "response") return "intermediate-bypass";
  const backendNode = request.backendId > 0n ? backendKey(request.backendId) : "";
  if (from !== backendNode) return "default";
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
      return node.kind === "route" || node.kind === "backend" || node.kind === "redirect" || node.kind === "agent" || node.kind === "upstream";
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

function buildRequestPath(request: TraceRequest): string[] {
  const path = ["ingress"];

  if (request.listenerId > 0n) {
    path.push(listenerKey(request.listenerId));
  }

  if (requestUsesRateLimitNode(request)) {
    path.push(RATE_LIMIT_KEY);
  }

  if (request.stage === TrafficTraceStage.RATE_LIMITED) {
    path.push("response");
    return dedupeConsecutive(path);
  }

  if (request.routeId > 0n) {
    path.push(routeKey(request.routeId));
  } else if (request.defaultRoute && request.listenerId > 0n) {
    path.push(DEFAULT_ROUTE_KEY);
  }

  const redirectNodeKey = redirectKeyForRequest(request);
  if (redirectNodeKey) {
    path.push(redirectNodeKey);
    path.push("response");
    return dedupeConsecutive(path);
  }

  if (request.backendId > 0n) {
    path.push(backendKey(request.backendId));

    if (request.backendType === PublicBackendType.STATIC) {
      path.push("static-response");
    } else if (request.agentId > 0n) {
      path.push(agentKey(request.agentId));
      path.push("upstream");
    } else if (request.forwardMode !== PublicBackendForwardMode.AGENT_POOL) {
      path.push("upstream");
    }
  }

  path.push("response");
  return dedupeConsecutive(path);
}

function targetIndexForRequest(request: TraceRequest, path: string[]): number {
  const targetKey = nodeKeyForStage(request);
  const index = path.indexOf(targetKey);
  if (index >= 0) return index;
  if (isTerminal(request)) return path.length - 1;
  return Math.max(0, path.length - 2);
}

function nodeKeyForStage(request: TraceRequest): string {
  switch (request.stage) {
    case TrafficTraceStage.RECEIVED:
      return request.listenerId > 0n ? listenerKey(request.listenerId) : "ingress";
    case TrafficTraceStage.ROUTE_RESOLVED:
      if (request.routeId > 0n) return routeKey(request.routeId);
      if (request.defaultRoute && request.listenerId > 0n) return DEFAULT_ROUTE_KEY;
      return request.listenerId > 0n ? listenerKey(request.listenerId) : "ingress";
    case TrafficTraceStage.BACKEND_SELECTED:
      return request.backendId > 0n ? backendKey(request.backendId) : "response";
    case TrafficTraceStage.AGENT_SELECTED:
      return request.agentId > 0n ? agentKey(request.agentId) : request.backendId > 0n ? backendKey(request.backendId) : "response";
    case TrafficTraceStage.UPSTREAM_STARTED:
      if (request.backendType === PublicBackendType.STATIC) return "static-response";
      if (request.agentId > 0n || request.forwardMode !== PublicBackendForwardMode.AGENT_POOL) return "upstream";
      return request.backendId > 0n ? backendKey(request.backendId) : "response";
    case TrafficTraceStage.UPSTREAM_RESPONDED:
      return request.backendType === PublicBackendType.STATIC ? "static-response" : "upstream";
    case TrafficTraceStage.RATE_LIMITED:
      return "response";
    case TrafficTraceStage.RESPONSE_SENT:
    case TrafficTraceStage.FAILED:
      return "response";
    default:
      return "ingress";
  }
}

function addObservedNodes(
  request: TraceRequest,
  addNode: (node: DiagramNodeInput) => void,
) {
  if (requestUsesRateLimitNode(request)) {
    addNode({
      key: RATE_LIMIT_KEY,
      label: "Rate limit",
      subLabel: request.rateLimitRuleName || "Observed",
      column: 2,
      kind: "rate-limit",
      editTargets: enabledRateLimitRuleTargets(),
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
      column: 3,
      kind: "route",
      editTargets: [routeEditTargetByID(request.routeId, request.routeLabel || `Route ${request.routeId.toString()}`, "Observed")],
    });
    const route = routeConfigForRequest(request);
    if (route && isRedirectRoute(route)) {
      addRedirectNode(route, addNode);
    }
  } else if (request.defaultRoute && request.listenerId > 0n) {
    addNode({
      key: DEFAULT_ROUTE_KEY,
      label: "Default route",
      subLabel: "Observed fallback",
      column: 3,
      kind: "route",
      editTargets: request.listenerId > 0n
        ? [listenerEditTarget(request.listenerId, request.listenerName || `Listener ${request.listenerId.toString()}`, "Default backend")]
        : [],
    });
  }

  if (request.backendId > 0n) {
    addNode({
      key: backendKey(request.backendId),
      label: request.backendName || `Backend ${request.backendId.toString()}`,
      subLabel: backendTypeLabel(request),
      column: 4,
      kind: "backend",
      editTargets: [backendEditTarget(request.backendId, request.backendName || `Backend ${request.backendId.toString()}`, backendTypeLabel(request))],
    });
  }

  if (request.backendType === PublicBackendType.STATIC) {
    addStaticNode(addNode, request.backendId > 0n ? backendEditTarget(request.backendId, request.backendName || `Backend ${request.backendId.toString()}`, "Static") : undefined);
  } else if (request.backendId > 0n && request.forwardMode !== PublicBackendForwardMode.AGENT_POOL) {
    addUpstreamNode(addNode, backendEditTarget(request.backendId, request.backendName || `Backend ${request.backendId.toString()}`, "Direct"));
  }

  if (request.agentId > 0n) {
    const agent = props.config?.agents.find((item) => item.id === request.agentId);
    addNode({
      key: agentKey(request.agentId),
      label: request.agentName || request.agentPublicId || `Agent ${request.agentId.toString()}`,
      subLabel: request.agentPublicId || "Observed",
      column: 5,
      kind: "agent",
      agentStatus: agentNodeStatus(agent),
      editTargets: [agentEditTarget(request.agentId, request.agentName || request.agentPublicId || `Agent ${request.agentId.toString()}`, request.agentPublicId || "Observed")],
    });
    addUpstreamNode(addNode, request.backendId > 0n ? backendEditTarget(request.backendId, request.backendName || `Backend ${request.backendId.toString()}`, "Agent pool") : undefined);
  }
}

function addStaticNode(addNode: (node: DiagramNodeInput) => void, target?: TrafficFlowEditTarget) {
  addNode({ key: "static-response", label: "Static", subLabel: "Generated", column: 6, kind: "upstream", editTargets: target ? [target] : [] });
}

function addUpstreamNode(addNode: (node: DiagramNodeInput) => void, target?: TrafficFlowEditTarget) {
  addNode({ key: "upstream", label: "Upstream", subLabel: "Origin", column: 6, kind: "upstream", editTargets: target ? [target] : [] });
}

function addRedirectNode(route: PublicRoute, addNode: (node: DiagramNodeInput) => void) {
  addNode({
    key: redirectKey(route.id),
    label: "Redirect",
    subLabel: redirectNodeSubLabel(route),
    column: 4,
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
  return `${from}->${to}`;
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

function mergeAgentStatus(existing: AgentNodeStatus | undefined, next: AgentNodeStatus | undefined): AgentNodeStatus | undefined {
  if (!existing) return next;
  if (!next) return existing;
  if (existing.state === "unknown" && next.state !== "unknown") return next;
  return existing;
}

function dedupeConsecutive(values: string[]): string[] {
  return values.filter((value, index) => index === 0 || values[index - 1] !== value);
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

function tokenColorClass(status: VisualTokenStatus): string {
  return `traffic-token-${status}`;
}

function isTerminal(request: TraceRequest): boolean {
  return request.stage === TrafficTraceStage.RESPONSE_SENT ||
    request.stage === TrafficTraceStage.FAILED ||
    request.stage === TrafficTraceStage.RATE_LIMITED;
}

function easeInOutCubic(value: number): number {
  return value < 0.5 ? 4 * value * value * value : 1 - Math.pow(-2 * value + 2, 3) / 2;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function kindOrder(kind: TrafficNodeKind): number {
  switch (kind) {
    case "ingress": return 0;
    case "listener": return 1;
    case "rate-limit": return 2;
    case "route": return 3;
    case "backend": return 4;
    case "redirect": return 4;
    case "agent": return 5;
    case "upstream": return 6;
    case "response": return 7;
    default: return 99;
  }
}

function listenerKey(id: bigint | string | number): string {
  return `listener:${id.toString()}`;
}

function routeKey(id: bigint | string | number): string {
  return `route:${id.toString()}`;
}

function backendKey(id: bigint | string | number): string {
  return `backend:${id.toString()}`;
}

function redirectKey(id: bigint | string | number): string {
  return `redirect:${id.toString()}`;
}

function agentKey(id: bigint | string | number): string {
  return `agent:${id.toString()}`;
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

function backendEditTarget(id: bigint | string | number, label: string, subLabel?: string): TrafficFlowEditTarget {
  return { kind: "backend", id: id.toString(), label, subLabel };
}

function agentEditTarget(id: bigint | string | number, label: string, subLabel?: string): TrafficFlowEditTarget {
  return { kind: "agent", id: id.toString(), label, subLabel };
}

function rateLimitEditTarget(rule: PublicRateLimitRule): TrafficFlowEditTarget {
  return {
    kind: "rate-limit",
    id: rule.id.toString(),
    label: rule.name || `Rate limit ${rule.id.toString()}`,
    subLabel: `${rateLimitAlgorithmLabel(rule.algorithm)} / P${rule.priority.toString()}`,
  };
}

function enabledRateLimitRuleTargets(): TrafficFlowEditTarget[] {
  return [...(props.config?.rateLimitRules ?? [])]
    .filter((rule) => rule.enabled)
    .sort(compareRateLimitRules)
    .map(rateLimitEditTarget);
}

function compareRateLimitRules(a: PublicRateLimitRule, b: PublicRateLimitRule): number {
  if (a.priority !== b.priority) return a.priority < b.priority ? -1 : 1;
  if (a.id === b.id) return 0;
  return a.id < b.id ? -1 : 1;
}

function rateLimitAlgorithmLabel(algorithm: PublicRateLimitAlgorithm): string {
  switch (algorithm) {
    case PublicRateLimitAlgorithm.SLIDING_WINDOW:
      return "Sliding window";
    case PublicRateLimitAlgorithm.TOKEN_BUCKET:
      return "Token bucket";
    case PublicRateLimitAlgorithm.LEAKY_BUCKET:
      return "Leaky bucket";
    default:
      return "Fixed window";
  }
}

function backendTypeLabel(request: TraceRequest): string {
  if (request.backendType === PublicBackendType.STATIC) return "Static";
  if (request.forwardMode === PublicBackendForwardMode.AGENT_POOL) return "Agent pool";
  return "Direct";
}

function isRedirectRoute(route: PublicRoute): boolean {
  return route.action === PublicRouteAction.REDIRECT;
}

function routeConfigForRequest(request: TraceRequest): PublicRoute | undefined {
  if (request.routeId <= 0n) return undefined;
  return props.config?.routes.find((route) => route.id === request.routeId);
}

function requestUsesRateLimitNode(request: TraceRequest): boolean {
  if (request.rateLimitRuleId > 0n || request.stage === TrafficTraceStage.RATE_LIMITED) return true;
  return (props.config?.rateLimitRules ?? []).some((rule) => rule.enabled);
}

function redirectKeyForRequest(request: TraceRequest): string {
  const route = routeConfigForRequest(request);
  return route && isRedirectRoute(route) ? redirectKey(route.id) : "";
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
  <div class="traffic-flow-shell">
    <div class="flow-status">
      <span>{{ visualTokens.length }} rendered</span>
      <span v-if="skippedVisualizations" class="flow-overflow">+{{ skippedVisualizations }} not rendered</span>
      <button type="button" class="flow-fit-button" @click="fitDiagram()">Fit</button>
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
      <button
        v-for="view in tokenViews"
        :key="view.token.requestId"
        type="button"
        class="traffic-token"
        :class="view.colorClass"
        :style="{
          transform: `translate(${view.x}px, ${view.y}px) translate(-50%, -50%)`,
          opacity: view.opacity,
        }"
        :aria-label="`Open trace details for ${view.label}`"
        @click="emit('select', view.token.request)"
      >
        <span class="traffic-token-halo" />
        <span class="traffic-token-dot" />
      </button>
    </div>
  </div>
</template>

<style scoped>
.traffic-flow-shell {
  position: relative;
  height: min(58vh, 540px);
  min-height: 360px;
  overflow: hidden;
  border: 1px solid #333;
  border-radius: 6px;
  background:
    linear-gradient(#111 1px, transparent 1px),
    linear-gradient(90deg, #111 1px, transparent 1px),
    #050505;
  background-size: 32px 32px;
}

.traffic-vue-flow {
  width: 100%;
  height: 100%;
  background: transparent;
  color: #ededed;
}

.flow-status {
  position: absolute;
  top: 12px;
  right: 12px;
  z-index: 7;
  display: flex;
  align-items: center;
  gap: 8px;
  border: 1px solid #333;
  border-radius: 999px;
  background: rgb(0 0 0 / 78%);
  padding: 4px 6px 4px 10px;
  color: #888;
  font-size: 0.75rem;
  line-height: 1;
  backdrop-filter: blur(8px);
}

.flow-overflow {
  color: #f59e0b;
}

.flow-fit-button {
  height: 24px;
  border: 1px solid #333;
  border-radius: 999px;
  background: #080808;
  padding: 0 9px;
  color: #d4d4d8;
  font-size: 0.72rem;
  font-weight: 600;
  transition: border-color 140ms ease, color 140ms ease, background 140ms ease;
}

.flow-fit-button:hover {
  border-color: #666;
  background: #111;
  color: #fff;
}

.traffic-token-layer {
  position: absolute;
  inset: 0;
  z-index: 6;
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
  color: #22d3ee;
}

.traffic-token-success {
  color: #22c55e;
}

.traffic-token-client-error {
  color: #f59e0b;
}

.traffic-token-server-error,
.traffic-token-failed {
  color: #ef4444;
}

.traffic-token-dot,
.traffic-token-halo {
  background: currentColor;
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
  stroke: #3f3f46;
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

@media (max-width: 640px) {
  .traffic-flow-shell {
    height: 420px;
  }

  .flow-status {
    left: 12px;
    right: auto;
  }
}
</style>
