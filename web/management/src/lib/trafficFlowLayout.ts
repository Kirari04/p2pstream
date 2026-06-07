import {
  PublicBackendForwardMode,
  PublicBackendType,
  PublicCacheTtlMode,
  PublicRateLimitAlgorithm,
  PublicTrafficShaperBudgetScope,
  PublicRouteAction,
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  PublicWafActivationMode,
  PublicWafRuleAction,
  type Agent,
  type GetPublicProxyConfigResponse,
  type PublicCacheRule,
  type PublicRateLimitRule,
  type PublicTrafficShaperRule,
  type PublicWafRule,
  type PublicRoute,
  type PublicRouteTarget,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { TrafficTraceStage as TraceStage } from "@/gen/proto/p2pstream/v1/management_pb";
import type { TrafficFlowEditTarget } from "@/types/trafficFlowEdit";
import type { TraceRequest } from "@/types/trafficTrace";

export const DEFAULT_ROUTE_KEY = "route:default";
export const RATE_LIMIT_KEY = "rate-limit";
export const WAF_KEY = "waf";
export const CACHE_KEY = "cache";
export const TRAFFIC_SHAPER_KEY = "traffic-shaper";

export type TrafficFlowConfigIndex = {
  routesByListenerId: Map<string, PublicRoute[]>;
  routeById: Map<string, PublicRoute>;
  routeTargetById: Map<string, PublicRouteTarget>;
  routeTargetsByRouteId: Map<string, PublicRouteTarget[]>;
  agentById: Map<string, Agent>;
  enabledRateLimitTargets: TrafficFlowEditTarget[];
  hasEnabledRateLimitRules: boolean;
  enabledWafTargets: TrafficFlowEditTarget[];
  hasEnabledWafRules: boolean;
  enabledTrafficShaperTargets: TrafficFlowEditTarget[];
  hasEnabledTrafficShaperRules: boolean;
  enabledCacheTargets: TrafficFlowEditTarget[];
  hasEnabledCacheRules: boolean;
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
  const routeTargetById = new Map<string, PublicRouteTarget>();
  const routeTargetsByRouteId = new Map<string, PublicRouteTarget[]>();
  const agentById = new Map<string, Agent>();
  const enabledRateLimitTargets = [...(config?.rateLimitRules ?? [])]
    .filter((rule) => rule.enabled)
    .sort(compareRateLimitRules)
    .map(rateLimitEditTarget);
  const enabledWafTargets = [...(config?.wafRules ?? [])]
    .filter((rule) => rule.enabled)
    .sort(compareWafRules)
    .map(wafEditTarget);
  const enabledTrafficShaperTargets = [...(config?.trafficShaperRules ?? [])]
    .filter((rule) => rule.enabled)
    .sort(compareTrafficShaperRules)
    .map(trafficShaperEditTarget);
  const enabledCacheTargets = [...(config?.cacheRules ?? [])]
    .filter((rule) => rule.enabled)
    .sort(compareCacheRules)
    .map(cacheEditTarget);

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
  for (const agent of config?.agents ?? []) {
    agentById.set(agent.id.toString(), agent);
  }
  for (const target of config?.routeTargets ?? []) {
    routeTargetById.set(target.id.toString(), target);
    const key = target.routeId.toString();
    const targets = routeTargetsByRouteId.get(key) ?? [];
    targets.push(target);
    routeTargetsByRouteId.set(key, targets);
  }
  for (const targets of routeTargetsByRouteId.values()) {
    targets.sort(compareRouteTargets);
  }

  return {
    routesByListenerId,
    routeById,
    routeTargetById,
    routeTargetsByRouteId,
    agentById,
    enabledRateLimitTargets,
    hasEnabledRateLimitRules: enabledRateLimitTargets.length > 0,
    enabledWafTargets,
    hasEnabledWafRules: enabledWafTargets.length > 0,
    enabledTrafficShaperTargets,
    hasEnabledTrafficShaperRules: enabledTrafficShaperTargets.length > 0,
    enabledCacheTargets,
    hasEnabledCacheRules: enabledCacheTargets.length > 0,
  };
}

export function routeTargetAssignments(route: PublicRoute, index: TrafficFlowConfigIndex | null): PublicRouteTarget[] {
  if (route.targets.length) return [...route.targets].sort(compareRouteTargets);
  return index?.routeTargetsByRouteId.get(route.id.toString()) ?? [];
}

export function buildTrafficFlowRequestPath(request: TraceRequest, index: TrafficFlowConfigIndex | null): string[] {
  const path = ["ingress"];

  if (request.listenerId > 0n) {
    path.push(listenerKey(request.listenerId));
  }

  if (requestUsesWafNode(request, index)) {
    path.push(WAF_KEY);
  }

  if (isWafTerminalStage(request.stage)) {
    path.push("response");
    return dedupeConsecutive(path);
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

  const selectedTargetId = request.routeTargetId || request.backendId;
  if (selectedTargetId > 0n) {
    path.push(backendKey(selectedTargetId));

    if (requestTargetType(request) === PublicRouteTargetType.STATIC) {
      path.push("static-response");
    } else {
      if (requestTraversesCacheNode(request)) {
        path.push(CACHE_KEY);
        if (request.stage === TraceStage.CACHE_LOOKUP) {
          return dedupeConsecutive(path);
        }
        if (request.stage === TraceStage.CACHE_HIT || request.cacheStatus.toLowerCase() === "hit") {
          path.push("response");
          return dedupeConsecutive(path);
        }
      }
      if (request.agentId > 0n) {
        path.push(agentKey(request.agentId));
        path.push("upstream");
      } else if (requestTargetTransport(request) !== PublicRouteTargetTransport.AGENT) {
        path.push("upstream");
      }
    }
  }

  path.push("response");
  return dedupeConsecutive(path);
}

export function targetIndexForTraceRequest(request: TraceRequest, path: string[]): number {
  if (request.stage === TraceStage.CACHE_STORED) {
    const responseIndex = path.indexOf("response");
    if (responseIndex >= 0) return responseIndex;
  }
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
    request.routeTargetId,
    request.routeTargetType,
    request.routeTargetTransport,
    request.backendId,
    request.backendType,
    request.forwardMode,
    request.agentId,
    request.rateLimitRuleId,
    request.wafRuleId,
    request.trafficShaperRuleId,
    request.cacheRuleId,
    request.cacheStatus,
  ].join("|");
}

export function requestUsesRateLimitNode(request: TraceRequest, index: TrafficFlowConfigIndex | null): boolean {
  if (request.rateLimitRuleId > 0n || request.stage === TraceStage.RATE_LIMITED) return true;
  return index?.hasEnabledRateLimitRules ?? false;
}

export function requestUsesWafNode(request: TraceRequest, index: TrafficFlowConfigIndex | null): boolean {
  if (request.wafRuleId > 0n || isWafTerminalStage(request.stage)) return true;
  return index?.hasEnabledWafRules ?? false;
}

export function requestUsesTrafficShaperNode(request: TraceRequest, index: TrafficFlowConfigIndex | null): boolean {
  if (request.trafficShaperRuleId > 0n || request.stage === TraceStage.TRAFFIC_SHAPER_SELECTED) return true;
  return index?.hasEnabledTrafficShaperRules ?? false;
}

export function requestUsesCacheNode(request: TraceRequest, index: TrafficFlowConfigIndex | null): boolean {
  if (request.cacheRuleId > 0n || request.stage === TraceStage.CACHE_LOOKUP || request.stage === TraceStage.CACHE_HIT || request.stage === TraceStage.CACHE_MISS || request.stage === TraceStage.CACHE_BYPASS || request.stage === TraceStage.CACHE_STORED) return true;
  return index?.hasEnabledCacheRules ?? false;
}

function requestTraversesCacheNode(request: TraceRequest): boolean {
  return request.stage === TraceStage.CACHE_LOOKUP ||
    request.stage === TraceStage.CACHE_HIT ||
    request.cacheStatus.toLowerCase() === "hit";
}

function requestTargetType(request: TraceRequest): PublicRouteTargetType {
  if (request.routeTargetType) return request.routeTargetType;
  if (request.backendType === PublicBackendType.STATIC) return PublicRouteTargetType.STATIC;
  if (request.backendType === PublicBackendType.PROXY_FORWARD) return PublicRouteTargetType.PROXY;
  return PublicRouteTargetType.UNSPECIFIED;
}

function requestTargetTransport(request: TraceRequest): PublicRouteTargetTransport {
  if (request.routeTargetTransport) return request.routeTargetTransport;
  if (request.forwardMode === PublicBackendForwardMode.AGENT_POOL) return PublicRouteTargetTransport.AGENT;
  if (request.forwardMode === PublicBackendForwardMode.DIRECT) return PublicRouteTargetTransport.DIRECT;
  return PublicRouteTargetTransport.UNSPECIFIED;
}

export function routeConfigForRequest(request: TraceRequest, index: TrafficFlowConfigIndex | null): PublicRoute | undefined {
  if (request.routeId <= 0n) return undefined;
  return index?.routeById.get(request.routeId.toString());
}

export function isTerminalTraceRequest(request: TraceRequest): boolean {
  return request.stage === TraceStage.RESPONSE_SENT ||
    request.stage === TraceStage.FAILED ||
    request.stage === TraceStage.RATE_LIMITED ||
    isWafTerminalStage(request.stage);
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
      return request.routeTargetId > 0n ? backendKey(request.routeTargetId) : request.backendId > 0n ? backendKey(request.backendId) : "response";
    case TraceStage.AGENT_SELECTED:
      return request.agentId > 0n ? agentKey(request.agentId) : request.backendId > 0n ? backendKey(request.backendId) : "response";
    case TraceStage.TRAFFIC_SHAPER_SELECTED:
      return TRAFFIC_SHAPER_KEY;
    case TraceStage.WAF_EVALUATED:
    case TraceStage.WAF_BLOCKED:
    case TraceStage.WAF_CAPTCHA_CHALLENGED:
    case TraceStage.WAF_CAPTCHA_VERIFIED:
    case TraceStage.WAF_WAITING_ROOM:
      return WAF_KEY;
    case TraceStage.CACHE_HIT:
    case TraceStage.CACHE_LOOKUP:
      return CACHE_KEY;
    case TraceStage.CACHE_MISS:
    case TraceStage.CACHE_BYPASS:
      return request.routeTargetId > 0n ? backendKey(request.routeTargetId) : request.backendId > 0n ? backendKey(request.backendId) : "response";
    case TraceStage.CACHE_STORED:
      return "response";
    case TraceStage.UPSTREAM_STARTED:
      if (requestTargetType(request) === PublicRouteTargetType.STATIC) return "static-response";
      if (request.agentId > 0n || requestTargetTransport(request) !== PublicRouteTargetTransport.AGENT) return "upstream";
      return request.routeTargetId > 0n ? backendKey(request.routeTargetId) : request.backendId > 0n ? backendKey(request.backendId) : "response";
    case TraceStage.UPSTREAM_RESPONDED:
      return requestTargetType(request) === PublicRouteTargetType.STATIC ? "static-response" : "upstream";
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

function wafEditTarget(rule: PublicWafRule): TrafficFlowEditTarget {
  return {
    kind: "waf",
    id: rule.id.toString(),
    label: rule.name || `WAF ${rule.id.toString()}`,
    subLabel: `${wafActionLabel(rule.action)} / ${wafActivationLabel(rule.activationMode)} / P${rule.priority.toString()}`,
  };
}

function cacheEditTarget(rule: PublicCacheRule): TrafficFlowEditTarget {
  return {
    kind: "cache",
    id: rule.id.toString(),
    label: rule.name || `Cache ${rule.id.toString()}`,
    subLabel: `${cacheTtlModeLabel(rule.ttlMode)} / P${rule.priority.toString()}`,
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

function compareCacheRules(a: PublicCacheRule, b: PublicCacheRule): number {
  if (a.priority !== b.priority) return a.priority < b.priority ? -1 : 1;
  if (a.id === b.id) return 0;
  return a.id < b.id ? -1 : 1;
}

function compareWafRules(a: PublicWafRule, b: PublicWafRule): number {
  if (a.priority !== b.priority) return a.priority < b.priority ? -1 : 1;
  if (a.id === b.id) return 0;
  return a.id < b.id ? -1 : 1;
}

function compareRoutes(a: PublicRoute, b: PublicRoute): number {
  if (a.priority !== b.priority) return a.priority < b.priority ? -1 : 1;
  if (a.id === b.id) return 0;
  return a.id < b.id ? -1 : 1;
}

function compareRouteTargets(a: PublicRouteTarget, b: PublicRouteTarget): number {
  if (a.priorityGroup !== b.priorityGroup) return a.priorityGroup < b.priorityGroup ? -1 : 1;
  if (a.position !== b.position) return a.position < b.position ? -1 : 1;
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

function trafficShaperScopeLabel(scope: PublicTrafficShaperRule["budgetScope"]): string {
  return scope === PublicTrafficShaperBudgetScope.PER_REQUEST ? "Per request" : "Per key";
}

function wafActionLabel(action: PublicWafRuleAction): string {
  switch (action) {
    case PublicWafRuleAction.CAPTCHA:
      return "Captcha";
    case PublicWafRuleAction.WAITING_ROOM:
      return "Waiting room";
    default:
      return "Block";
  }
}

function wafActivationLabel(mode: PublicWafActivationMode): string {
  return mode === PublicWafActivationMode.AUTOMATIC ? "Automatic" : "Always";
}

function cacheTtlModeLabel(mode: PublicCacheTtlMode): string {
  return mode === PublicCacheTtlMode.ORIGIN ? "Origin TTL" : "Fixed TTL";
}

function isWafTerminalStage(stage: TraceStage): boolean {
  return stage === TraceStage.WAF_BLOCKED ||
    stage === TraceStage.WAF_CAPTCHA_CHALLENGED ||
    stage === TraceStage.WAF_WAITING_ROOM;
}
