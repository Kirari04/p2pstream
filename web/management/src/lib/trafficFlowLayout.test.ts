import { describe, expect, test } from "bun:test";
import {
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  PublicCacheQueryMode,
  PublicCacheScope,
  PublicCacheTtlMode,
  PublicRateLimitAlgorithm,
  PublicTrafficShaperBudgetScope,
  PublicRouteAction,
  TrafficTraceStage,
  type Agent,
  type GetPublicProxyConfigResponse,
  type PublicCacheRule,
  type PublicRateLimitRule,
  type PublicTrafficShaperRule,
  type PublicRoute,
  type PublicRouteTarget,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  CACHE_KEY,
  TrafficRequestPathCache,
  agentKey,
  targetKey,
  buildTrafficFlowRequestPath,
  createTrafficFlowConfigIndex,
  listenerKey,
  redirectKey,
  routeKey,
  requestUsesCacheNode,
  targetIndexForTraceRequest,
} from "@/lib/trafficFlowLayout";
import { DEFAULT_ROUTE_KEY, RATE_LIMIT_KEY, TRAFFIC_SHAPER_KEY } from "@/lib/trafficFlowLayout";
import { newTraceRequest } from "@/lib/trafficTraceStore";
import type { TraceRequest } from "@/types/trafficTrace";

describe("trafficFlowLayout", () => {
  test("direct target request path includes upstream", () => {
    const request = traceRequest({
      listenerId: 1n,
      defaultRoute: true,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      stage: TrafficTraceStage.RESPONSE_SENT,
    });

    expect(buildTrafficFlowRequestPath(request, emptyIndex())).toEqual([
      "ingress",
      listenerKey(1n),
      DEFAULT_ROUTE_KEY,
      targetKey(2n),
      "upstream",
      "response",
    ]);
  });

  test("agent target request path includes selected agent", () => {
    const request = traceRequest({
      listenerId: 1n,
      routeId: 3n,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.AGENT,
      agentId: 7n,
      stage: TrafficTraceStage.RESPONSE_SENT,
    });

    expect(buildTrafficFlowRequestPath(request, emptyIndex())).toEqual([
      "ingress",
      listenerKey(1n),
      routeKey(3n),
      targetKey(2n),
      agentKey(7n),
      "upstream",
      "response",
    ]);
  });

  test("static target request path includes static response", () => {
    const request = traceRequest({
      listenerId: 1n,
      defaultRoute: true,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.STATIC,
      stage: TrafficTraceStage.RESPONSE_SENT,
    });

    expect(buildTrafficFlowRequestPath(request, emptyIndex())).toContain("static-response");
  });

  test("rate-limited request path ends at response after rate limit", () => {
    const request = traceRequest({
      listenerId: 1n,
      rateLimitRuleId: 9n,
      stage: TrafficTraceStage.RATE_LIMITED,
    });

    expect(buildTrafficFlowRequestPath(request, emptyIndex())).toEqual([
      "ingress",
      listenerKey(1n),
      RATE_LIMIT_KEY,
      "response",
    ]);
  });

  test("traffic-shaped request path includes shaper after rate limit", () => {
    const index = createTrafficFlowConfigIndex(configWith({
      rateLimitRules: [rateLimitRule({ id: 9n, enabled: true })],
      trafficShaperRules: [trafficShaperRule({ id: 10n, enabled: true })],
    }));
    const request = traceRequest({
      listenerId: 1n,
      defaultRoute: true,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      trafficShaperRuleId: 10n,
      stage: TrafficTraceStage.TRAFFIC_SHAPER_SELECTED,
    });

    expect(buildTrafficFlowRequestPath(request, index)).toEqual([
      "ingress",
      listenerKey(1n),
      RATE_LIMIT_KEY,
      TRAFFIC_SHAPER_KEY,
      DEFAULT_ROUTE_KEY,
      targetKey(2n),
      "upstream",
      "response",
    ]);
  });

  test("redirect request path includes redirect node and response", () => {
    const index = createTrafficFlowConfigIndex(configWith({
      routes: [route({ id: 3n, listenerId: 1n, action: PublicRouteAction.REDIRECT })],
    }));
    const request = traceRequest({
      listenerId: 1n,
      routeId: 3n,
      stage: TrafficTraceStage.RESPONSE_SENT,
    });

    expect(buildTrafficFlowRequestPath(request, index)).toEqual([
      "ingress",
      listenerKey(1n),
      routeKey(3n),
      redirectKey(3n),
      "response",
    ]);
  });

  test("cache hit path exits directly to response", () => {
    const request = traceRequest({
      listenerId: 1n,
      routeId: 3n,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      cacheRuleId: 4n,
      cacheStatus: "hit",
      stage: TrafficTraceStage.CACHE_HIT,
    });

    expect(buildTrafficFlowRequestPath(request, emptyIndex())).toEqual([
      "ingress",
      listenerKey(1n),
      routeKey(3n),
      targetKey(2n),
      CACHE_KEY,
      "response",
    ]);
  });

  test("cache miss path continues to direct upstream", () => {
    const request = traceRequest({
      listenerId: 1n,
      routeId: 3n,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      cacheRuleId: 4n,
      cacheStatus: "miss",
      stage: TrafficTraceStage.CACHE_MISS,
    });

    expect(buildTrafficFlowRequestPath(request, emptyIndex())).toEqual([
      "ingress",
      listenerKey(1n),
      routeKey(3n),
      targetKey(2n),
      "upstream",
      "response",
    ]);
    expect(buildTrafficFlowRequestPath(request, emptyIndex())).not.toContain(CACHE_KEY);
  });

  test("cache bypass path continues through selected agent", () => {
    const request = traceRequest({
      listenerId: 1n,
      routeId: 3n,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.AGENT,
      agentId: 7n,
      cacheRuleId: 4n,
      cacheStatus: "bypass",
      stage: TrafficTraceStage.CACHE_BYPASS,
    });

    expect(buildTrafficFlowRequestPath(request, emptyIndex())).toEqual([
      "ingress",
      listenerKey(1n),
      routeKey(3n),
      targetKey(2n),
      agentKey(7n),
      "upstream",
      "response",
    ]);
    expect(buildTrafficFlowRequestPath(request, emptyIndex())).not.toContain(CACHE_KEY);
  });

  test("static target does not include cache", () => {
    const index = createTrafficFlowConfigIndex(configWith({
      cacheRules: [cacheRule({ id: 4n, enabled: true })],
    }));
    const request = traceRequest({
      listenerId: 1n,
      defaultRoute: true,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.STATIC,
      stage: TrafficTraceStage.RESPONSE_SENT,
    });

    expect(buildTrafficFlowRequestPath(request, index)).not.toContain(CACHE_KEY);
  });

  test("redirect route does not include cache", () => {
    const index = createTrafficFlowConfigIndex(configWith({
      routes: [route({ id: 3n, listenerId: 1n, action: PublicRouteAction.REDIRECT })],
      cacheRules: [cacheRule({ id: 4n, enabled: true })],
    }));
    const request = traceRequest({
      listenerId: 1n,
      routeId: 3n,
      stage: TrafficTraceStage.RESPONSE_SENT,
    });

    expect(buildTrafficFlowRequestPath(request, index)).not.toContain(CACHE_KEY);
  });

  test("cache stored targets response", () => {
    const request = traceRequest({
      listenerId: 1n,
      routeId: 3n,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      cacheRuleId: 4n,
      cacheStatus: "stored",
      stage: TrafficTraceStage.CACHE_STORED,
    });
    const path = buildTrafficFlowRequestPath(request, emptyIndex());

    expect(path).toEqual([
      "ingress",
      listenerKey(1n),
      routeKey(3n),
      targetKey(2n),
      "upstream",
      "response",
    ]);
    expect(path).not.toContain(CACHE_KEY);
    expect(targetIndexForTraceRequest(request, path)).toBe(path.indexOf("response"));
  });

  test("cache lookup path targets cache", () => {
    const request = traceRequest({
      listenerId: 1n,
      routeId: 3n,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      cacheRuleId: 4n,
      cacheStatus: "miss",
      stage: TrafficTraceStage.CACHE_LOOKUP,
    });
    const path = buildTrafficFlowRequestPath(request, emptyIndex());

    expect(path).toEqual([
      "ingress",
      listenerKey(1n),
      routeKey(3n),
      targetKey(2n),
      CACHE_KEY,
    ]);
    expect(targetIndexForTraceRequest(request, path)).toBe(path.indexOf(CACHE_KEY));
  });

  test("enabled cache config keeps cache node visible without forcing path hop", () => {
    const index = createTrafficFlowConfigIndex(configWith({
      cacheRules: [cacheRule({ id: 4n, enabled: true })],
    }));
    const request = traceRequest({
      listenerId: 1n,
      defaultRoute: true,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      stage: TrafficTraceStage.RESPONSE_SENT,
    });

    expect(requestUsesCacheNode(request, index)).toBe(true);
    expect(buildTrafficFlowRequestPath(request, index)).not.toContain(CACHE_KEY);
  });

  test("path cache returns stable paths and invalidates on signature changes", () => {
    const cache = new TrafficRequestPathCache();
    const request = traceRequest({
      requestId: "cached",
      listenerId: 1n,
      defaultRoute: true,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      stage: TrafficTraceStage.BACKEND_SELECTED,
    });
    const first = cache.get(request, emptyIndex());
    const second = cache.get(request, emptyIndex());
    expect(second).toBe(first);

    request.stage = TrafficTraceStage.RESPONSE_SENT;
    const changed = cache.get(request, emptyIndex());
    expect(changed).not.toBe(first);
    expect(changed.path.at(-1)).toBe("response");
  });

  test("config indexes group routes, agents, and route targets", () => {
    const index = createTrafficFlowConfigIndex(configWith({
      routes: [route({ id: 3n, listenerId: 1n }), route({ id: 4n, listenerId: 1n, priority: 1n })],
      routeTargets: [routeTarget({ id: 2n, routeId: 3n })],
      agents: [agent({ id: 7n })],
      rateLimitRules: [rateLimitRule({ id: 9n, enabled: true })],
      trafficShaperRules: [trafficShaperRule({ id: 10n, enabled: true })],
    }));

    expect(index.routesByListenerId.get("1")?.map((item) => item.id)).toEqual([4n, 3n]);
    expect(index.routeTargetById.get("2")?.id).toBe(2n);
    expect(index.agentById.get("7")?.id).toBe(7n);
    expect(index.routeTargetsByRouteId.get("3")?.[0]?.id).toBe(2n);
    expect(index.enabledRateLimitTargets[0]?.kind).toBe("rate-limit");
    expect(index.hasEnabledRateLimitRules).toBe(true);
    expect(index.enabledTrafficShaperTargets[0]?.kind).toBe("traffic-shaper");
    expect(index.hasEnabledTrafficShaperRules).toBe(true);
  });
});

function emptyIndex() {
  return createTrafficFlowConfigIndex(configWith({}));
}

function traceRequest(overrides: Partial<TraceRequest>): TraceRequest {
  return {
    ...newTraceRequest(overrides.requestId ?? "req"),
    ...overrides,
  };
}

function configWith(overrides: Partial<GetPublicProxyConfigResponse>): GetPublicProxyConfigResponse {
  return {
    $typeName: "p2pstream.v1.GetPublicProxyConfigResponse",
    listeners: [],
    agents: [],
    routes: [],
    routeTargets: [],
    rateLimitRules: [],
    trafficShaperRules: [],
    cacheRules: [],
    tlsCertificates: [],
    proxy: undefined,
    ...overrides,
  } as GetPublicProxyConfigResponse;
}

function cacheRule(overrides: Partial<PublicCacheRule>): PublicCacheRule {
  return {
    $typeName: "p2pstream.v1.PublicCacheRule",
    id: 0n,
    name: "",
    priority: 100n,
    enabled: true,
    match: undefined,
    routeIds: [],
    targetIds: [],
    scope: PublicCacheScope.SELECTED_BACKEND,
    ttlMode: PublicCacheTtlMode.FIXED,
    ttlMillis: 3_600_000n,
    queryMode: PublicCacheQueryMode.FULL,
    queryParams: [],
    varyHeaders: ["Accept-Encoding"],
    cacheStatusCodes: [200n],
    maxObjectBytes: 104_857_600n,
    addCacheStatusHeader: true,
    allowCookieRequests: false,
    createdAtUnixMillis: 0n,
    updatedAtUnixMillis: 0n,
    ...overrides,
  } as PublicCacheRule;
}

function route(overrides: Partial<PublicRoute>): PublicRoute {
  return {
    $typeName: "p2pstream.v1.PublicRoute",
    id: 0n,
    listenerId: 0n,
    priority: 100n,
    hostPattern: "",
    pathPrefix: "",
    enabled: true,
    action: PublicRouteAction.FORWARD,
    redirectTargetMode: 0,
    redirectTarget: "",
    redirectStatusCode: 302n,
    redirectPreservePathSuffix: true,
    redirectPreserveQuery: true,
    targetLoadBalancing: 0,
    isDefault: false,
    targets: [],
    ...overrides,
  } as PublicRoute;
}

function routeTarget(overrides: Partial<PublicRouteTarget>): PublicRouteTarget {
  return {
    $typeName: "p2pstream.v1.PublicRouteTarget",
    id: 0n,
    routeId: 0n,
    name: "",
    position: 0n,
    priorityGroup: 0n,
    weight: 100n,
    enabled: true,
    targetType: 1,
    url: "",
    transport: 1,
    agentSelector: undefined,
    agentLoadBalancing: 0,
    tlsSkipVerify: false,
    upstreamResponseHeaderTimeoutMillis: 60000n,
    upstreamRequestHeaders: [],
    upstreamBasicAuth: undefined,
    healthCheck: undefined,
    staticStatusCode: 200n,
    staticResponseHeaders: [],
    staticResponseBody: "",
    staticResponseBodyMode: 0,
    staticResponseTemplateId: 0n,
    health: undefined,
    ...overrides,
  } as PublicRouteTarget;
}

function agent(overrides: Partial<Agent>): Agent {
  return {
    $typeName: "p2pstream.v1.Agent",
    id: 0n,
    publicId: "",
    name: "",
    enabled: true,
    connected: false,
    activeRequests: 0n,
    createdAtUnixMillis: 0n,
    updatedAtUnixMillis: 0n,
    lastConnectedAtUnixMillis: 0n,
    lastDisconnectedAtUnixMillis: 0n,
    latestStats: undefined,
    labels: {},
    ...overrides,
  } as Agent;
}

function rateLimitRule(overrides: Partial<PublicRateLimitRule>): PublicRateLimitRule {
  return {
    $typeName: "p2pstream.v1.PublicRateLimitRule",
    id: 0n,
    name: "",
    priority: 100n,
    enabled: true,
    algorithm: PublicRateLimitAlgorithm.FIXED_WINDOW,
    limit: 10n,
    windowMillis: 60_000n,
    burst: 0n,
    keyParts: [],
    match: undefined,
    responseStatusCode: 429n,
    responseBody: "",
    responseContentType: "text/plain",
    responseHeaders: [],
    createdAtUnixMillis: 0n,
    updatedAtUnixMillis: 0n,
    ...overrides,
  } as PublicRateLimitRule;
}

function trafficShaperRule(overrides: Partial<PublicTrafficShaperRule>): PublicTrafficShaperRule {
  return {
    $typeName: "p2pstream.v1.PublicTrafficShaperRule",
    id: 0n,
    name: "",
    priority: 100n,
    enabled: true,
    budgetScope: PublicTrafficShaperBudgetScope.PER_KEY,
    uploadBytesPerSecond: 0n,
    downloadBytesPerSecond: 1024n,
    burstBytes: 0n,
    requestExemptBytes: 0n,
    responseExemptBytes: 0n,
    keyParts: [],
    match: undefined,
    createdAtUnixMillis: 0n,
    updatedAtUnixMillis: 0n,
    ...overrides,
  } as PublicTrafficShaperRule;
}
