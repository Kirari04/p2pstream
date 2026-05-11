import { describe, expect, test } from "bun:test";
import {
  PublicBackendForwardMode,
  PublicBackendType,
  PublicRateLimitAlgorithm,
  PublicRouteAction,
  TrafficTraceStage,
  type Agent,
  type GetPublicProxyConfigResponse,
  type PublicBackend,
  type PublicBackendAgent,
  type PublicRateLimitRule,
  type PublicRoute,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  TrafficRequestPathCache,
  agentKey,
  backendKey,
  buildTrafficFlowRequestPath,
  createTrafficFlowConfigIndex,
  listenerKey,
  redirectKey,
  routeKey,
} from "@/lib/trafficFlowLayout";
import { DEFAULT_ROUTE_KEY, RATE_LIMIT_KEY } from "@/lib/trafficFlowLayout";
import { newTraceRequest } from "@/lib/trafficTraceStore";
import type { TraceRequest } from "@/types/trafficTrace";

describe("trafficFlowLayout", () => {
  test("direct backend request path includes upstream", () => {
    const request = traceRequest({
      listenerId: 1n,
      defaultRoute: true,
      backendId: 2n,
      backendType: PublicBackendType.PROXY_FORWARD,
      forwardMode: PublicBackendForwardMode.DIRECT,
      stage: TrafficTraceStage.RESPONSE_SENT,
    });

    expect(buildTrafficFlowRequestPath(request, emptyIndex())).toEqual([
      "ingress",
      listenerKey(1n),
      DEFAULT_ROUTE_KEY,
      backendKey(2n),
      "upstream",
      "response",
    ]);
  });

  test("agent backend request path includes selected agent", () => {
    const request = traceRequest({
      listenerId: 1n,
      routeId: 3n,
      backendId: 2n,
      backendType: PublicBackendType.PROXY_FORWARD,
      forwardMode: PublicBackendForwardMode.AGENT_POOL,
      agentId: 7n,
      stage: TrafficTraceStage.RESPONSE_SENT,
    });

    expect(buildTrafficFlowRequestPath(request, emptyIndex())).toEqual([
      "ingress",
      listenerKey(1n),
      routeKey(3n),
      backendKey(2n),
      agentKey(7n),
      "upstream",
      "response",
    ]);
  });

  test("static backend request path includes static response", () => {
    const request = traceRequest({
      listenerId: 1n,
      defaultRoute: true,
      backendId: 2n,
      backendType: PublicBackendType.STATIC,
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

  test("path cache returns stable paths and invalidates on signature changes", () => {
    const cache = new TrafficRequestPathCache();
    const request = traceRequest({
      requestId: "cached",
      listenerId: 1n,
      defaultRoute: true,
      backendId: 2n,
      backendType: PublicBackendType.PROXY_FORWARD,
      forwardMode: PublicBackendForwardMode.DIRECT,
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

  test("config indexes group routes, agents, and backend assignments", () => {
    const index = createTrafficFlowConfigIndex(configWith({
      routes: [route({ id: 3n, listenerId: 1n }), route({ id: 4n, listenerId: 1n, priority: 1n })],
      backends: [backend({ id: 2n })],
      agents: [agent({ id: 7n })],
      backendAgents: [backendAgent({ backendId: 2n, agentId: 7n })],
      rateLimitRules: [rateLimitRule({ id: 9n, enabled: true })],
    }));

    expect(index.routesByListenerId.get("1")?.map((item) => item.id)).toEqual([4n, 3n]);
    expect(index.backendById.get("2")?.id).toBe(2n);
    expect(index.agentById.get("7")?.id).toBe(7n);
    expect(index.backendAgentsByBackendId.get("2")?.[0]?.agentId).toBe(7n);
    expect(index.enabledRateLimitTargets[0]?.kind).toBe("rate-limit");
    expect(index.hasEnabledRateLimitRules).toBe(true);
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
    backends: [],
    backendAgents: [],
    agents: [],
    routes: [],
    rateLimitRules: [],
    tlsCertificates: [],
    proxy: undefined,
    ...overrides,
  } as GetPublicProxyConfigResponse;
}

function route(overrides: Partial<PublicRoute>): PublicRoute {
  return {
    $typeName: "p2pstream.v1.PublicRoute",
    id: 0n,
    listenerId: 0n,
    priority: 100n,
    hostPattern: "",
    pathPrefix: "",
    backendId: 0n,
    enabled: true,
    action: PublicRouteAction.FORWARD,
    redirectTargetMode: 0,
    redirectTarget: "",
    redirectStatusCode: 302n,
    redirectPreservePathSuffix: true,
    redirectPreserveQuery: true,
    ...overrides,
  } as PublicRoute;
}

function backend(overrides: Partial<PublicBackend>): PublicBackend {
  return {
    $typeName: "p2pstream.v1.PublicBackend",
    id: 0n,
    name: "",
    targetOrigin: "",
    enabled: true,
    createdAtUnixMillis: 0n,
    updatedAtUnixMillis: 0n,
    backendType: PublicBackendType.PROXY_FORWARD,
    forwardMode: PublicBackendForwardMode.DIRECT,
    loadBalancing: 0,
    tlsSkipVerify: false,
    staticStatusCode: 200n,
    staticResponseHeaders: [],
    staticResponseBody: "",
    agentAssignments: [],
    upstreamRequestHeaders: [],
    upstreamBasicAuth: undefined,
    ...overrides,
  } as PublicBackend;
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
    ...overrides,
  } as Agent;
}

function backendAgent(overrides: Partial<PublicBackendAgent>): PublicBackendAgent {
  return {
    $typeName: "p2pstream.v1.PublicBackendAgent",
    backendId: 0n,
    agentId: 0n,
    position: 0n,
    weight: 100n,
    enabled: true,
    ...overrides,
  } as PublicBackendAgent;
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
