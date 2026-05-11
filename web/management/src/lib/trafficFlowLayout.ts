import {
  PublicBackendForwardMode,
  PublicBackendType,
  PublicRateLimitAlgorithm,
  PublicTrafficShaperBudgetScope,
  PublicRouteAction,
  type Agent,
  type GetPublicProxyConfigResponse,
  type PublicBackend,
  type PublicBackendAgent,
  type PublicRateLimitRule,
  type PublicTrafficShaperRule,
  type PublicRoute,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { TrafficTraceStage as TraceStage } from "@/gen/proto/p2pstream/v1/management_pb";
import type { TrafficFlowEditTarget } from "@/types/trafficFlowEdit";
import type { TraceRequest } from "@/types/trafficTrace";

export const DEFAULT_ROUTE_KEY = "route:default";
export const RATE_LIMIT_KEY = "rate-limit";
export const TRAFFIC_SHAPER_KEY = "traffic-shaper";

export type TrafficFlowConfigIndex = {
  routesByListenerId: Map<string, PublicRoute[]>;
  routeById: Map<string, PublicRoute>;
  backendById: Map<string, PublicBackend>;
  agentById: Map<string, Agent>;
  backendAgentsByBackendId: Map<string, PublicBackendAgent[]>;
  enabledRateLimitTargets: TrafficFlowEditTarget[];
  hasEnabledRateLimitRules: boolean;
  enabledTrafficShaperTargets: TrafficFlowEditTarget[];
  hasEnabledTrafficShaperRules: boolean;
};

export type TrafficRequestPathCacheEntry = {
  signature: string;
  path: string[];
  targetIndex: number;
};

export class TrafficRequestPathCache {
  private readonly cache = new Map<string, TrafficRequestPathCacheEntry>();

  get(request: TraceRequest, index: TrafficFlowConfigIndex | null): TrafficRequestPathCacheEntry {
    const signature = trafficRequestPathSignature(request);
    const cached = this.cache.get(request.requestId);
    if (cached?.signature === signature) {
      return cached;
    }
    const path = buildTrafficFlowRequestPath(request, index);
    const entry = {
      signature,
      path,
      targetIndex: targetIndexForTraceRequest(request, path),
    };
    this.cache.set(request.requestId, entry);
    return entry;
  }

  clear() {
    this.cache.clear();
  }

  prune(requestIds: Set<string>) {
    for (const requestId of this.cache.keys()) {
      if (!requestIds.has(requestId)) {
        this.cache.delete(requestId);
      }
    }
  }
}

export function createTrafficFlowConfigIndex(config: GetPublicProxyConfigResponse | null): TrafficFlowConfigIndex {
  const routesByListenerId = new Map<string, PublicRoute[]>();
  const routeById = new Map<string, PublicRoute>();
  const backendById = new Map<string, PublicBackend>();
  const agentById = new Map<string, Agent>();
  const backendAgentsByBackendId = new Map<string, PublicBackendAgent[]>();
  const enabledRateLimitTargets = [...(config?.rateLimitRules ?? [])]
    .filter((rule) => rule.enabled)
    .sort(compareRateLimitRules)
    .map(rateLimitEditTarget);
  const enabledTrafficShaperTargets = [...(config?.trafficShaperRules ?? [])]
    .filter((rule) => rule.enabled)
    .sort(compareTrafficShaperRules)
    .map(trafficShaperEditTarget);

  for (const route of config?.routes ?? []) {
    routeById.set(route.id.toString(), route);
    const key = route.listenerId.toString();
    const routes = routesByListenerId.get(key) ?? [];
    routes.push(route);
    routesByListenerId.set(key, routes);
  }
  for (const routes of routesByListenerId.values()) {
    routes.sort(compareRoutes);
  }
  for (const backend of config?.backends ?? []) {
    backendById.set(backend.id.toString(), backend);
  }
  for (const agent of config?.agents ?? []) {
    agentById.set(agent.id.toString(), agent);
  }
  for (const assignment of config?.backendAgents ?? []) {
    const key = assignment.backendId.toString();
    const assignments = backendAgentsByBackendId.get(key) ?? [];
    assignments.push(assignment);
    backendAgentsByBackendId.set(key, assignments);
  }
  for (const assignments of backendAgentsByBackendId.values()) {
    assignments.sort(compareBackendAgents);
  }

  return {
    routesByListenerId,
    routeById,
    backendById,
    agentById,
    backendAgentsByBackendId,
    enabledRateLimitTargets,
    hasEnabledRateLimitRules: enabledRateLimitTargets.length > 0,
    enabledTrafficShaperTargets,
    hasEnabledTrafficShaperRules: enabledTrafficShaperTargets.length > 0,
  };
}

export function buildTrafficFlowRequestPath(request: TraceRequest, index: TrafficFlowConfigIndex | null): string[] {
  const path = ["ingress"];

  if (request.listenerId > 0n) {
    path.push(listenerKey(request.listenerId));
  }

  if (requestUsesRateLimitNode(request, index)) {
    path.push(RATE_LIMIT_KEY);
  }

  if (request.stage === TraceStage.RATE_LIMITED) {
    path.push("response");
    return dedupeConsecutive(path);
  }

  if (requestUsesTrafficShaperNode(request, index)) {
    path.push(TRAFFIC_SHAPER_KEY);
  }

  if (request.routeId > 0n) {
    path.push(routeKey(request.routeId));
  } else if (request.defaultRoute && request.listenerId > 0n) {
    path.push(DEFAULT_ROUTE_KEY);
  }

  const redirectNodeKey = redirectKeyForRequest(request, index);
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

export function targetIndexForTraceRequest(request: TraceRequest, path: string[]): number {
  const targetKey = nodeKeyForTraceStage(request);
  const index = path.indexOf(targetKey);
  if (index >= 0) return index;
  if (isTerminalTraceRequest(request)) return path.length - 1;
  return Math.max(0, path.length - 2);
}

export function trafficRequestPathSignature(request: TraceRequest): string {
  return [
    request.stage,
    request.listenerId,
    request.routeId,
    request.defaultRoute,
    request.backendId,
    request.backendType,
    request.forwardMode,
    request.agentId,
    request.rateLimitRuleId,
    request.trafficShaperRuleId,
  ].join("|");
}

export function requestUsesRateLimitNode(request: TraceRequest, index: TrafficFlowConfigIndex | null): boolean {
  if (request.rateLimitRuleId > 0n || request.stage === TraceStage.RATE_LIMITED) return true;
  return index?.hasEnabledRateLimitRules ?? false;
}

export function requestUsesTrafficShaperNode(request: TraceRequest, index: TrafficFlowConfigIndex | null): boolean {
  if (request.trafficShaperRuleId > 0n || request.stage === TraceStage.TRAFFIC_SHAPER_SELECTED) return true;
  return index?.hasEnabledTrafficShaperRules ?? false;
}

export function routeConfigForRequest(request: TraceRequest, index: TrafficFlowConfigIndex | null): PublicRoute | undefined {
  if (request.routeId <= 0n) return undefined;
  return index?.routeById.get(request.routeId.toString());
}

export function isTerminalTraceRequest(request: TraceRequest): boolean {
  return request.stage === TraceStage.RESPONSE_SENT ||
    request.stage === TraceStage.FAILED ||
    request.stage === TraceStage.RATE_LIMITED;
}

export function isRedirectRoute(route: PublicRoute): boolean {
  return route.action === PublicRouteAction.REDIRECT;
}

export function listenerKey(id: bigint | string | number): string {
  return `listener:${id.toString()}`;
}

export function routeKey(id: bigint | string | number): string {
  return `route:${id.toString()}`;
}

export function backendKey(id: bigint | string | number): string {
  return `backend:${id.toString()}`;
}

export function redirectKey(id: bigint | string | number): string {
  return `redirect:${id.toString()}`;
}

export function agentKey(id: bigint | string | number): string {
  return `agent:${id.toString()}`;
}

function nodeKeyForTraceStage(request: TraceRequest): string {
  switch (request.stage) {
    case TraceStage.RECEIVED:
      return request.listenerId > 0n ? listenerKey(request.listenerId) : "ingress";
    case TraceStage.ROUTE_RESOLVED:
      if (request.routeId > 0n) return routeKey(request.routeId);
      if (request.defaultRoute && request.listenerId > 0n) return DEFAULT_ROUTE_KEY;
      return request.listenerId > 0n ? listenerKey(request.listenerId) : "ingress";
    case TraceStage.BACKEND_SELECTED:
      return request.backendId > 0n ? backendKey(request.backendId) : "response";
    case TraceStage.AGENT_SELECTED:
      return request.agentId > 0n ? agentKey(request.agentId) : request.backendId > 0n ? backendKey(request.backendId) : "response";
    case TraceStage.TRAFFIC_SHAPER_SELECTED:
      return TRAFFIC_SHAPER_KEY;
    case TraceStage.UPSTREAM_STARTED:
      if (request.backendType === PublicBackendType.STATIC) return "static-response";
      if (request.agentId > 0n || request.forwardMode !== PublicBackendForwardMode.AGENT_POOL) return "upstream";
      return request.backendId > 0n ? backendKey(request.backendId) : "response";
    case TraceStage.UPSTREAM_RESPONDED:
      return request.backendType === PublicBackendType.STATIC ? "static-response" : "upstream";
    case TraceStage.RATE_LIMITED:
      return "response";
    case TraceStage.RESPONSE_SENT:
    case TraceStage.FAILED:
      return "response";
    default:
      return "ingress";
  }
}

function redirectKeyForRequest(request: TraceRequest, index: TrafficFlowConfigIndex | null): string {
  const route = routeConfigForRequest(request, index);
  return route && isRedirectRoute(route) ? redirectKey(route.id) : "";
}

function dedupeConsecutive(values: string[]): string[] {
  return values.filter((value, index) => index === 0 || values[index - 1] !== value);
}

function rateLimitEditTarget(rule: PublicRateLimitRule): TrafficFlowEditTarget {
  return {
    kind: "rate-limit",
    id: rule.id.toString(),
    label: rule.name || `Rate limit ${rule.id.toString()}`,
    subLabel: `${rateLimitAlgorithmLabel(rule.algorithm)} / P${rule.priority.toString()}`,
  };
}

function trafficShaperEditTarget(rule: PublicTrafficShaperRule): TrafficFlowEditTarget {
  return {
    kind: "traffic-shaper",
    id: rule.id.toString(),
    label: rule.name || `Traffic shaper ${rule.id.toString()}`,
    subLabel: `${trafficShaperScopeLabel(rule.budgetScope)} / P${rule.priority.toString()}`,
  };
}

function compareRateLimitRules(a: PublicRateLimitRule, b: PublicRateLimitRule): number {
  if (a.priority !== b.priority) return a.priority < b.priority ? -1 : 1;
  if (a.id === b.id) return 0;
  return a.id < b.id ? -1 : 1;
}

function compareTrafficShaperRules(a: PublicTrafficShaperRule, b: PublicTrafficShaperRule): number {
  if (a.priority !== b.priority) return a.priority < b.priority ? -1 : 1;
  if (a.id === b.id) return 0;
  return a.id < b.id ? -1 : 1;
}

function compareRoutes(a: PublicRoute, b: PublicRoute): number {
  if (a.priority !== b.priority) return a.priority < b.priority ? -1 : 1;
  if (a.id === b.id) return 0;
  return a.id < b.id ? -1 : 1;
}

function compareBackendAgents(a: PublicBackendAgent, b: PublicBackendAgent): number {
  if (a.position !== b.position) return a.position < b.position ? -1 : 1;
  if (a.agentId === b.agentId) return 0;
  return a.agentId < b.agentId ? -1 : 1;
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

function trafficShaperScopeLabel(scope: PublicTrafficShaperRule["budgetScope"]): string {
  return scope === PublicTrafficShaperBudgetScope.PER_REQUEST ? "Per request" : "Per key";
}
