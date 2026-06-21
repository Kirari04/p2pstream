import { describe, expect, test } from "bun:test";
import {
  PublicRouteAction,
  PublicRouteRedirectTargetMode,
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  TrafficTraceStage,
  type Agent,
  type GetPublicProxyConfigResponse,
  type PublicListener,
  type PublicRoute,
  type PublicRouteTarget,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  CACHE_KEY,
  RATE_LIMIT_KEY,
  WAF_KEY,
  agentKey,
  createTrafficFlowConfigIndex,
  listenerKey,
  redirectKey,
  routeKey,
  targetKey,
  TrafficRequestPathCache,
} from "@/lib/trafficFlowLayout";
import { buildTrafficFlowGraph } from "@/lib/trafficFlowGraph";
import { newTraceRequest } from "@/lib/trafficTraceStore";
import type { TraceRequest } from "@/types/trafficTrace";

describe("trafficFlowGraph", () => {
  test("generates listener, route, and target nodes from config", () => {
    const config = configWith({
      listeners: [listener()],
      routes: [route({ id: 3n, listenerId: 1n })],
      routeTargets: [routeTarget({ id: 2n, routeId: 3n })],
    });
    const layout = buildLayout(config, []);

    expect(layout.nodeByKey.get(listenerKey(1n))?.kind).toBe("listener");
    expect(layout.nodeByKey.get(routeKey(3n))?.kind).toBe("route");
    expect(layout.nodeByKey.get(targetKey(2n))?.kind).toBe("target");
    expect(edgeKeys(layout.edges)).toContain(`${routeKey(3n)}->${targetKey(2n)}`);
    expect(edgeKeys(layout.edges)).toContain(`${targetKey(2n)}->upstream`);
  });

  test("includes WAF, rate-limit, and cache path nodes from traced requests", () => {
    const request = traceRequest({
      listenerId: 1n,
      routeId: 3n,
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      wafRuleId: 8n,
      rateLimitRuleId: 9n,
      cacheRuleId: 4n,
      cacheStatus: "miss",
      stage: TrafficTraceStage.CACHE_LOOKUP,
    });
    const layout = buildLayout(configWith({}), [request]);

    expect(layout.nodeByKey.get(WAF_KEY)?.kind).toBe("waf");
    expect(layout.nodeByKey.get(RATE_LIMIT_KEY)?.kind).toBe("rate-limit");
    expect(layout.nodeByKey.get(CACHE_KEY)?.kind).toBe("cache");
    expect(edgeKeys(layout.edges)).toContain(`${WAF_KEY}->${RATE_LIMIT_KEY}`);
    expect(edgeKeys(layout.edges)).toContain(`${targetKey(2n)}->${CACHE_KEY}`);
  });

  test("generates agent target path nodes for matching agents", () => {
    const config = configWith({
      listeners: [listener()],
      routes: [route({ id: 3n, listenerId: 1n })],
      routeTargets: [routeTarget({
        id: 2n,
        routeId: 3n,
        transport: PublicRouteTargetTransport.AGENT,
        agentSelector: {
          $typeName: "p2pstream.v1.PublicAgentSelector",
          matchLabels: { site: "edge" },
        },
      })],
      agents: [agent({ id: 7n, publicId: "agent-7", labels: { site: "edge" }, connected: true })],
    });
    const layout = buildLayout(config, []);

    expect(layout.nodeByKey.get(agentKey(7n))?.kind).toBe("agent");
    expect(layout.nodeByKey.get(agentKey(7n))?.agentStatus?.state).toBe("connected");
    expect(edgeKeys(layout.edges)).toContain(`${targetKey(2n)}->${agentKey(7n)}`);
    expect(edgeKeys(layout.edges)).toContain(`${agentKey(7n)}->upstream`);
  });

  test("routes redirect actions through redirect and response nodes", () => {
    const config = configWith({
      listeners: [listener()],
      routes: [route({
        id: 3n,
        listenerId: 1n,
        action: PublicRouteAction.REDIRECT,
        redirectTargetMode: PublicRouteRedirectTargetMode.ABSOLUTE_URL,
        redirectTarget: "https://example.test/new",
      })],
    });
    const layout = buildLayout(config, []);

    expect(layout.nodeByKey.get(redirectKey(3n))?.kind).toBe("redirect");
    expect(layout.nodeByKey.get(redirectKey(3n))?.subLabel).toBe("302 url");
    expect(edgeKeys(layout.edges)).toContain(`${routeKey(3n)}->${redirectKey(3n)}`);
    expect(edgeKeys(layout.edges)).toContain(`${redirectKey(3n)}->response`);
  });

  test("routes static targets through the static response node", () => {
    const config = configWith({
      listeners: [listener()],
      routes: [route({ id: 3n, listenerId: 1n })],
      routeTargets: [routeTarget({
        id: 2n,
        routeId: 3n,
        targetType: PublicRouteTargetType.STATIC,
        staticStatusCode: 204n,
      })],
    });
    const layout = buildLayout(config, []);

    expect(layout.nodeByKey.get("static-response")?.kind).toBe("upstream");
    expect(edgeKeys(layout.edges)).toContain(`${targetKey(2n)}->static-response`);
    expect(edgeKeys(layout.edges)).toContain("static-response->response");
  });

  test("routes terminal policy decisions directly to response", () => {
    const wafBlocked = traceRequest({
      requestId: "waf-blocked",
      listenerId: 1n,
      wafRuleId: 8n,
      stage: TrafficTraceStage.WAF_BLOCKED,
    });
    const rateLimited = traceRequest({
      requestId: "rate-limited",
      listenerId: 1n,
      rateLimitRuleId: 9n,
      stage: TrafficTraceStage.RATE_LIMITED,
    });
    const layout = buildLayout(configWith({}), [wafBlocked, rateLimited]);

    expect(edgeKeys(layout.edges)).toContain(`${WAF_KEY}->response`);
    expect(edgeKeys(layout.edges)).toContain(`${RATE_LIMIT_KEY}->response`);
  });

  test("preserves attacker-controlled trace labels as node data", () => {
    const hostileLabel = `<script>alert("x")</script> ${"very-long-hostname.".repeat(8)}`;
    const request = traceRequest({
      requestId: "hostile-labels",
      listenerId: 1n,
      listenerName: hostileLabel,
      routeId: 3n,
      routeLabel: `/prefix?<img src=x onerror=alert(1)> ${"segment/".repeat(16)}`,
      routeTargetId: 2n,
      routeTargetName: `target ${"<b>bold</b>".repeat(12)}`,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      stage: TrafficTraceStage.UPSTREAM_STARTED,
    });
    const layout = buildLayout(configWith({}), [request]);

    expect(layout.nodeByKey.get(listenerKey(1n))?.label).toBe(hostileLabel);
    expect(layout.nodeByKey.get(routeKey(3n))?.label).toContain("<img");
    expect(layout.nodeByKey.get(targetKey(2n))?.label).toContain("<b>bold</b>");
    expect(edgeKeys(layout.edges)).toContain(`${targetKey(2n)}->upstream`);
  });
});

function buildLayout(config: GetPublicProxyConfigResponse, requests: TraceRequest[]) {
  const index = createTrafficFlowConfigIndex(config);
  const cache = new TrafficRequestPathCache();
  return buildTrafficFlowGraph({
    config,
    requests,
    configIndex: index,
    requestPath: (request) => cache.get(request, index),
  });
}

function edgeKeys(edges: Array<{ from: string; to: string }>): string[] {
  return edges.map((edge) => `${edge.from}->${edge.to}`);
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
    tlsDnsCredentials: [],
    wafCaptchaProviders: [],
    wafRules: [],
    responseTemplates: [],
    tlsCertificates: [],
    proxy: undefined,
    ...overrides,
  } as GetPublicProxyConfigResponse;
}

function listener(): PublicListener {
  return {
    $typeName: "p2pstream.v1.PublicListener",
    id: 1n,
    name: "HTTP",
    bindAddress: "",
    port: 8080n,
    protocol: 1,
    enabled: true,
  } as PublicListener;
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
    targetType: PublicRouteTargetType.PROXY,
    url: "http://127.0.0.1:9000",
    transport: PublicRouteTargetTransport.DIRECT,
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
