import {
  CACHE_KEY,
  RATE_LIMIT_KEY,
  TRAFFIC_SHAPER_KEY,
  WAF_KEY,
  agentKey,
  agentsMatchingTargetSelector,
  isRedirectRoute,
  listenerDefaultRouteKey,
  listenerKey,
  redirectKey,
  requestUsesCacheNode,
  requestUsesRateLimitNode,
  requestUsesTrafficShaperNode,
  requestUsesWafNode,
  routeConfigForRequest,
  routeKey,
  routeTargetAssignments,
  targetKey,
  type TrafficFlowConfigIndex,
  type TrafficRequestPathCacheEntry,
} from "@/lib/trafficFlowLayout";
import { motionEdgeKey, type MotionPoint } from "@/lib/trafficMotion";
import {
  BEZIER_LENGTH_SAMPLES,
  BYPASS_LANE_GAP,
  BYPASS_X_GAP,
  CACHE_SIDE_CENTER_Y,
  COLUMN_X,
  EDGE_CURVATURE,
  MIN_BYPASS_Y,
  MIN_CENTER_Y,
  NODE_HEIGHT,
  NODE_WIDTH,
  ROW_GAP,
  type AgentNodeStatus,
  type Bounds,
  type CacheNodeStatus,
  type CacheNodeTone,
  type CubicSegment,
  type DiagramEdge,
  type DiagramEdgeRoute,
  type DiagramNode,
  type DiagramNodeInput,
  type EdgeRouteGeometry,
  type Point,
  type TrafficFlowGraph,
  type TrafficNodeKind,
  type VisualTokenCacheTone,
} from "@/lib/trafficFlowModel";
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
import type { TrafficFlowEditTarget } from "@/types/trafficFlowEdit";
import type { TraceRequest } from "@/types/trafficTrace";

export function buildTrafficFlowGraph(input: {
  config: GetPublicProxyConfigResponse | null;
  requests: readonly TraceRequest[];
  configIndex: TrafficFlowConfigIndex;
  requestPath: (request: TraceRequest) => TrafficRequestPathCacheEntry;
}): TrafficFlowGraph {
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
  const index = input.configIndex;

  addNode({ key: "ingress", label: "Ingress", subLabel: "Public request", column: 0, kind: "ingress", editTargets: [] });
  addNode({ key: "response", label: "Response", subLabel: "Client", column: 10, kind: "response", editTargets: [] });

  const listeners = input.config?.listeners ?? [];
  const targets = input.config?.routeTargets ?? [];
  const enabledRateLimitTargets = index.enabledRateLimitTargets;
  const enabledWafTargets = index.enabledWafTargets;
  const enabledCacheTargets = index.enabledCacheTargets;
  const enabledTrafficShaperTargets = index.enabledTrafficShaperTargets;
  const showWafNode = index.hasEnabledWafRules || input.requests.some((request) => requestUsesWafNode(request, index));
  const showRateLimitNode = index.hasEnabledRateLimitRules || input.requests.some((request) => requestUsesRateLimitNode(request, index));
  const showTrafficShaperNode = index.hasEnabledTrafficShaperRules || input.requests.some((request) => requestUsesTrafficShaperNode(request, index));
  const showCacheNode = index.hasEnabledCacheRules || input.requests.some((request) => requestUsesCacheNode(request, index));

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
    if (showWafNode) addEdge(listenerNodeKey, WAF_KEY);
    if (showRateLimitNode) addEdge(rateEntryKey, RATE_LIMIT_KEY);
    if (showTrafficShaperNode) addEdge(shaperEntryKey, TRAFFIC_SHAPER_KEY);

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
          for (const target of assignments) addEdge(key, targetKey(target.id));
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
      const matchedAgents = agentsMatchingTarget(target, input.config?.agents ?? []);
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

  for (const request of input.requests) {
    addObservedNodes(request, addNode, index);
    const path = input.requestPath(request).path;
    for (let index = 0; index < path.length - 1; index += 1) {
      addEdge(path[index], path[index + 1], routeForRequestSegment(request, path[index], path[index + 1]));
    }
  }

  if (!listeners.length && !input.requests.length) {
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
}

export function buildTrafficFlowEdgeRoutes(layout: TrafficFlowGraph): Map<string, EdgeRouteGeometry> {
  const routes = new Map<string, EdgeRouteGeometry>();
  for (const edge of layout.edges) {
    const sourceNode = layout.nodeByKey.get(edge.from);
    const targetNode = layout.nodeByKey.get(edge.to);
    if (!sourceNode || !targetNode) continue;

    const source = sourceHandlePoint(sourceNode);
    const target = targetHandlePoint(targetNode);
    const bypassBounds = bypassBoundsForEdge(layout, edge);
    const route =
      edge.route !== "default" && bypassBounds
        ? buildBypassRoute(edge, source, target, bypassBounds)
        : buildDefaultBezierRoute(edge, source, target);
    routes.set(edgeKey(edge.from, edge.to), route);
  }
  return routes;
}

export function routeToMotionPoints(route: EdgeRouteGeometry): MotionPoint[] {
  const sampleCount = Math.max(2, Math.ceil(route.totalLength / 24));
  const points: MotionPoint[] = [];
  for (let index = 0; index <= sampleCount; index += 1) {
    points.push(pointAtRouteProgress(route, index / sampleCount));
  }
  return points;
}

export function nodeBounds(node: DiagramNode): Bounds {
  return {
    left: node.x,
    right: node.x + NODE_WIDTH,
    top: node.y,
    bottom: node.y + NODE_HEIGHT,
  };
}

export function pointInsideBounds(point: Point, bounds: Bounds): boolean {
  return point.x >= bounds.left && point.x <= bounds.right && point.y >= bounds.top && point.y <= bounds.bottom;
}

export function edgeKey(from: string, to: string): string {
  return motionEdgeKey(from, to);
}

export function cacheStatusForTone(tone: Exclude<VisualTokenCacheTone, ""> | CacheNodeTone): CacheNodeStatus {
  switch (tone) {
    case "hit": return { label: "HIT", tone: "hit" };
    case "miss": return { label: "MISS", tone: "miss" };
    case "bypass": return { label: "BYPASS", tone: "bypass" };
    case "stored": return { label: "STORED", tone: "stored" };
    case "lookup": return { label: "LOOKUP", tone: "lookup" };
    default: return { label: "READY", tone: "neutral" };
  }
}

export function cacheStatusForRequest(request: TraceRequest): CacheNodeStatus {
  return cacheStatusForTone(cacheToneForRequest(request) || "lookup");
}

export function cacheToneForRequest(request: TraceRequest): VisualTokenCacheTone {
  const status = request.cacheStatus.toLowerCase();
  if (status === "hit" || request.stage === TrafficTraceStage.CACHE_HIT) return "hit";
  if (status === "miss" || request.stage === TrafficTraceStage.CACHE_MISS) return "miss";
  if (status === "bypass" || request.stage === TrafficTraceStage.CACHE_BYPASS) return "bypass";
  if (status === "stored" || request.stage === TrafficTraceStage.CACHE_STORED) return "stored";
  return "";
}

export function cacheTonePriority(tone: CacheNodeTone): number {
  switch (tone) {
    case "hit": return 6;
    case "stored": return 5;
    case "miss": return 4;
    case "bypass": return 3;
    case "lookup": return 2;
    default: return 1;
  }
}

function addObservedNodes(
  request: TraceRequest,
  addNode: (node: DiagramNodeInput) => void,
  index: TrafficFlowConfigIndex,
) {
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
    if (route && isRedirectRoute(route)) addRedirectNode(route, addNode);
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

function bypassBoundsForEdge(layout: TrafficFlowGraph, edge: DiagramEdge): Bounds | null {
  if (edge.route === "agent-bypass") {
    return boundsForNodes(layout.nodes.filter((node) => node.kind === "agent" && node.key !== edge.from && node.key !== edge.to));
  }
  if (edge.route === "intermediate-bypass") {
    return boundsForNodes(layout.nodes.filter((node) => {
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
    buildCubicSegment(source, { x: source.x + 32, y: source.y }, { x: laneEntry.x - 32, y: laneEntry.y }, laneEntry),
    buildCubicSegment(laneEntry, { x: laneEntry.x + 40, y: laneEntry.y }, { x: laneExit.x - 40, y: laneExit.y }, laneExit),
    buildCubicSegment(laneExit, { x: laneExit.x + 32, y: laneExit.y }, { x: target.x - 32, y: target.y }, target),
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

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
