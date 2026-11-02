import { describe, expect, test } from "bun:test";
import {
  PublicRouteAction,
  PublicRouteRedirectTargetMode,
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  PublicSiteListenerBehavior,
  TrafficTraceStage,
  type Agent,
  type GetPublicProxyConfigResponse,
  type PublicListener,
  type PublicRoute,
  type PublicRouteTarget,
  type PublicSite,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  CACHE_KEY,
  RATE_LIMIT_KEY,
  SITES_REDIRECT_KEY,
  WAF_KEY,
  agentKey,
  createTrafficFlowConfigIndex,
  listenerDefaultRouteKey,
  listenerKey,
  redirectKey,
  routeKey,
  siteKey,
  targetKey,
  TrafficRequestPathCache,
  type TrafficFlowDetail,
} from "@/lib/trafficFlowLayout";
import { buildTrafficFlowGraph } from "@/lib/trafficFlowGraph";
import { newTraceRequest } from "@/lib/trafficTraceStore";
import type { TraceRequest } from "@/types/trafficTrace";

describe("trafficFlowGraph", () => {
  test("shared Site routes connect to Serve listeners and keep each Site default distinct", () => {
    const shared = site({ id: 7n, name: "Application" });
    const fallback = site({ id: 8n, name: "Fallback", defaultSite: true });
    const draft = site({ id: 9n, published: false });
    const config = configWith({
      listeners: [listener(), { ...listener(), id: 2n }, { ...listener(), id: 3n }],
      sites: [shared, fallback, draft],
      routes: [
        route({ id: 11n, siteId: 7n, isDefault: true }),
        route({ id: 12n, siteId: 8n, isDefault: true }),
        route({ id: 13n, siteId: 9n }),
      ],
      routeTargets: [routeTarget({ id: 21n, routeId: 11n }), routeTarget({ id: 22n, routeId: 13n })],
    });
    const layout = buildLayout(config, []);
    const edges = edgeKeys(layout.edges);
    expect(edges).toContain(`${listenerKey(1n)}->${routeKey(11n)}`);
    expect(edges).toContain(`${listenerKey(2n)}->${routeKey(11n)}`);
    expect(edges).toContain(`${listenerKey(1n)}->${routeKey(12n)}`);
    expect(edges).not.toContain(`${listenerKey(3n)}->${routeKey(11n)}`);
    expect(layout.nodeByKey.get(routeKey(11n))?.subLabel).toContain("Application");
    expect(layout.nodeByKey.get(routeKey(12n))?.subLabel).toContain("Fallback");
    expect(layout.nodeByKey.has(routeKey(13n))).toBe(false);
    expect(layout.nodeByKey.has(targetKey(22n))).toBe(false);
    expect(edges).toContain(`${routeKey(11n)}->${targetKey(21n)}`);
  });

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

describe("trafficFlowGraph sites view", () => {
  test("shows one node per site, keeps the standalone default route, and drops target nodes", () => {
    const layout = buildLayout(sitesConfig(), [], "sites");
    const column5 = layout.nodes.filter((node) => node.column === 5).map((node) => node.key).sort();

    expect(column5).toEqual([siteKey(7n), siteKey(8n), listenerDefaultRouteKey(1n), routeKey(40n)].sort());
    expect(layout.nodes.some((node) => node.kind === "target")).toBe(false);
    expect(layout.nodeByKey.has(routeKey(11n))).toBe(false);
    expect(layout.nodeByKey.has(redirectKey(14n))).toBe(false);

    const app = layout.nodeByKey.get(siteKey(7n));
    expect(app?.kind).toBe("site");
    expect(app?.label).toBe("Application");
    expect(app?.subLabel).toBe("4 routes · app.example.com");
    expect(app?.editTargets).toEqual([{ kind: "site", id: "7", label: "Application", subLabel: "4 routes · app.example.com" }]);
    expect(layout.nodeByKey.get(siteKey(8n))?.subLabel).toBe("1 route · Any hostname");
    expect(layout.nodeByKey.get(listenerDefaultRouteKey(1n))?.label).toBe("Default route");
  });

  test("aggregates site edges over enabled targets and dedupes them", () => {
    const layout = buildLayout(sitesConfig(), [], "sites");
    const edges = edgeKeys(layout.edges);
    const app = siteKey(7n);

    expect(edges).toContain(`${listenerKey(1n)}->${app}`);
    expect(edges).toContain(`${app}->upstream`);
    expect(edges).toContain(`${app}->${agentKey(5n)}`);
    expect(edges).toContain(`${app}->static-response`);
    expect(edges).toContain(`${app}->${SITES_REDIRECT_KEY}`);
    expect(edges).toContain(`${SITES_REDIRECT_KEY}->response`);
    expect(edges).toContain(`${agentKey(5n)}->upstream`);
    // The disabled static target of the fallback site contributes nothing; the site has no other target.
    expect(edges).toContain(`${siteKey(8n)}->response`);
    expect(edges).not.toContain(`${siteKey(8n)}->static-response`);
    expect(edges).toContain(`${listenerDefaultRouteKey(1n)}->upstream`);
    expect(edges).toContain(`${routeKey(40n)}->upstream`);
    expect(new Set(edges).size).toBe(edges.length);
    expect(layout.edges.find((edge) => edge.from === app && edge.to === "upstream")?.route).toBe("agent-bypass");
    for (const edge of layout.edges) {
      expect(edge.from.startsWith("target:") || edge.to.startsWith("target:")).toBe(false);
    }
  });

  test("closes the gap left by the collapsed target column", () => {
    const layout = buildLayout(sitesConfig(), [], "sites");
    const xs = [...new Set(layout.nodes.map((node) => node.x))].sort((a, b) => a - b);
    const gaps = xs.slice(1).map((x, index) => x - xs[index]);
    expect(new Set(gaps).size).toBe(1);
    expect(layout.nodeByKey.get("response")?.x).toBe(xs.at(-1));
  });

  test("maps a traced site request onto the site node so every segment is an edge", () => {
    const config = sitesConfig();
    const index = createTrafficFlowConfigIndex(config);
    const cache = new TrafficRequestPathCache();
    const request = traceRequest({
      requestId: "site-agent",
      listenerId: 1n,
      routeId: 12n,
      routeTargetId: 22n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.AGENT,
      agentId: 5n,
      stage: TrafficTraceStage.UPSTREAM_STARTED,
    });
    const sitesEntry = cache.get(request, index, "sites");
    expect(sitesEntry.path).toEqual(["ingress", listenerKey(1n), siteKey(7n), agentKey(5n), "upstream", "response"]);
    expect(sitesEntry.path[sitesEntry.targetIndex]).toBe("upstream");

    const layout = buildLayout(config, [request], "sites");
    const edges = edgeKeys(layout.edges);
    for (let position = 0; position < sitesEntry.path.length - 1; position += 1) {
      expect(edges).toContain(`${sitesEntry.path[position]}->${sitesEntry.path[position + 1]}`);
    }

    const routesEntry = cache.get(request, index, "routes");
    expect(routesEntry.path).toContain(targetKey(22n));
    expect(routesEntry.signature).not.toBe(sitesEntry.signature);
  });

  test("points a backend-selected token at the site and maps redirects to the shared node", () => {
    const config = sitesConfig();
    const index = createTrafficFlowConfigIndex(config);
    const cache = new TrafficRequestPathCache();
    const selected = cache.get(traceRequest({
      requestId: "selected",
      listenerId: 1n,
      routeId: 11n,
      routeTargetId: 21n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      stage: TrafficTraceStage.BACKEND_SELECTED,
    }), index, "sites");
    expect(selected.path[selected.targetIndex]).toBe(siteKey(7n));

    const redirected = traceRequest({ requestId: "redirect", listenerId: 1n, routeId: 14n, stage: TrafficTraceStage.RESPONSE_SENT });
    const entry = cache.get(redirected, index, "sites");
    expect(entry.path).toEqual(["ingress", listenerKey(1n), siteKey(7n), SITES_REDIRECT_KEY, "response"]);
    const edges = edgeKeys(buildLayout(config, [redirected], "sites").edges);
    expect(edges).toContain(`${siteKey(7n)}->${SITES_REDIRECT_KEY}`);
    expect(edges).toContain(`${SITES_REDIRECT_KEY}->response`);
  });

  test("keeps standalone default routes and unknown observed routes renderable", () => {
    const config = sitesConfig();
    const index = createTrafficFlowConfigIndex(config);
    const cache = new TrafficRequestPathCache();
    const fallback = traceRequest({
      requestId: "fallback",
      listenerId: 1n,
      defaultRoute: true,
      routeTargetId: 31n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      stage: TrafficTraceStage.RESPONSE_SENT,
    });
    const unknown = traceRequest({
      requestId: "unknown",
      listenerId: 1n,
      routeId: 99n,
      routeLabel: "gone.example.com /old",
      routeTargetId: 98n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      stage: TrafficTraceStage.RESPONSE_SENT,
    });
    expect(cache.get(fallback, index, "sites").path).toEqual(["ingress", listenerKey(1n), listenerDefaultRouteKey(1n), "upstream", "response"]);
    expect(cache.get(unknown, index, "sites").path).toEqual(["ingress", listenerKey(1n), routeKey(99n), "upstream", "response"]);

    const layout = buildLayout(config, [fallback, unknown], "sites");
    const edges = edgeKeys(layout.edges);
    expect(layout.nodeByKey.get(routeKey(99n))?.label).toBe("gone.example.com /old");
    expect(layout.nodeByKey.has(targetKey(98n))).toBe(false);
    expect(edges).toContain(`${listenerKey(1n)}->${routeKey(99n)}`);
    expect(edges).toContain(`${routeKey(99n)}->upstream`);
    expect(layout.edges.find((edge) => edge.from === routeKey(99n) && edge.to === "upstream")?.route).toBe("agent-bypass");
    expect(edges).toContain(`${listenerDefaultRouteKey(1n)}->upstream`);
  });

  test("routes view is unchanged by the detail switch", () => {
    const request = traceRequest({
      requestId: "site-agent",
      listenerId: 1n,
      routeId: 12n,
      routeTargetId: 22n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.AGENT,
      agentId: 5n,
      stage: TrafficTraceStage.UPSTREAM_STARTED,
    });
    const implicit = buildLayout(sitesConfig(), [request]);
    const routes = buildLayout(sitesConfig(), [request], "routes");

    expect(routes.nodes).toEqual(implicit.nodes);
    expect(routes.edges).toEqual(implicit.edges);
    expect(routes.nodes.some((node) => node.kind === "site")).toBe(false);
    expect(routes.nodeByKey.get(routeKey(11n))?.kind).toBe("route");
    expect(routes.nodeByKey.get(targetKey(21n))?.kind).toBe("target");
    expect(routes.nodeByKey.get(redirectKey(14n))?.kind).toBe("redirect");
    expect(edgeKeys(routes.edges)).toContain(`${routeKey(12n)}->${targetKey(22n)}`);
  });
});

function sitesConfig(): GetPublicProxyConfigResponse {
  const app = site({
    id: 7n,
    name: "Application",
    canonicalHostname: "app.example.com",
    listenerBindings: [{ listenerId: 1n, behavior: PublicSiteListenerBehavior.SERVE }],
  } as Partial<PublicSite>);
  const fallback = site({
    id: 8n,
    name: "Fallback",
    defaultSite: true,
    listenerBindings: [{ listenerId: 1n, behavior: PublicSiteListenerBehavior.SERVE }],
  } as Partial<PublicSite>);
  return configWith({
    listeners: [listener()],
    sites: [app, fallback],
    agents: [agent({ id: 5n, name: "edge", publicId: "agent-5", connected: true })],
    routes: [
      route({ id: 11n, siteId: 7n, isDefault: true }),
      route({ id: 12n, siteId: 7n, pathPrefix: "/api", priority: 10n }),
      route({ id: 13n, siteId: 7n, pathPrefix: "/status", priority: 20n }),
      route({
        id: 14n,
        siteId: 7n,
        pathPrefix: "/old",
        priority: 30n,
        action: PublicRouteAction.REDIRECT,
        redirectTargetMode: PublicRouteRedirectTargetMode.ABSOLUTE_URL,
        redirectTarget: "https://example.test/new",
      }),
      route({ id: 15n, siteId: 8n, isDefault: true }),
      route({ id: 30n, listenerId: 1n, isDefault: true }),
      route({ id: 40n, listenerId: 1n, hostPattern: "legacy.example.com", priority: 50n }),
    ],
    routeTargets: [
      routeTarget({ id: 21n, routeId: 11n }),
      routeTarget({ id: 23n, routeId: 11n, position: 1n }),
      routeTarget({ id: 22n, routeId: 12n, transport: PublicRouteTargetTransport.AGENT }),
      routeTarget({ id: 24n, routeId: 13n, targetType: PublicRouteTargetType.STATIC, staticStatusCode: 204n }),
      routeTarget({ id: 25n, routeId: 15n, targetType: PublicRouteTargetType.STATIC, enabled: false }),
      routeTarget({ id: 31n, routeId: 30n }),
      routeTarget({ id: 41n, routeId: 40n }),
    ],
  });
}

function buildLayout(config: GetPublicProxyConfigResponse, requests: TraceRequest[], detail?: TrafficFlowDetail) {
  const index = createTrafficFlowConfigIndex(config);
  const cache = new TrafficRequestPathCache();
  return buildTrafficFlowGraph({
    config,
    requests,
    configIndex: index,
    requestPath: (request) => cache.get(request, index, detail),
    detail,
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
    retryRules: [],
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
    siteId: 0n,
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

function site(overrides: Partial<PublicSite>): PublicSite {
  return {
    id: 0n,
    name: "Site",
    enabled: true,
    published: true,
    listenerBindings: [
      { listenerId: 1n, behavior: PublicSiteListenerBehavior.SERVE },
      { listenerId: 2n, behavior: PublicSiteListenerBehavior.SERVE },
      { listenerId: 3n, behavior: PublicSiteListenerBehavior.REDIRECT_HTTPS, redirectListenerId: 2n },
    ],
    ...overrides,
  } as PublicSite;
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
    version: "",
    commit: "",
    ...overrides,
  } as Agent;
}
