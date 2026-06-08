import { spawnSync } from "child_process";
import http from "http";
import { AddressInfo, Socket } from "net";
import type { APIRequestContext } from "@playwright/test";
import { expect } from "@playwright/test";
import { connectRPC } from "../e2e/helpers/connect";

type JsonRecord = Record<string, any>;

export type DocsFixtureState = {
  listeners: {
    http: JsonRecord;
    https: JsonRecord;
  };
  agents: {
    bootstrap: JsonRecord;
    offline: JsonRecord;
    disabled: JsonRecord;
  };
  routes: {
    direct: JsonRecord;
    agent: JsonRecord;
    staticResponse: JsonRecord;
    redirect: JsonRecord;
    fallback: JsonRecord;
  };
  targets: {
    direct: JsonRecord;
    agent: JsonRecord;
    staticResponse: JsonRecord;
    fallback: JsonRecord;
  };
  templates: {
    generic: JsonRecord;
    maintenance: JsonRecord;
    rateLimit: JsonRecord;
    wafBlock: JsonRecord;
    captcha: JsonRecord;
    waitingRoom: JsonRecord;
  };
  wafRule: JsonRecord;
  captchaProvider: JsonRecord;
  rateLimitRule: JsonRecord;
  cacheRule: JsonRecord;
  trafficShaperRule: JsonRecord;
  dnsCredential: JsonRecord;
  tls: {
    httpChallenge: JsonRecord;
    dnsChallenge: JsonRecord;
  };
  environment: JsonRecord;
  accessToken: JsonRecord;
};

export type FixtureUpstream = {
  url: string;
  close: () => Promise<void>;
};

type SeedOptions = {
  httpPort: string;
  httpsPort: string;
  upstreamURL: string;
  databasePath: string;
  managementBaseURL: string;
};

export async function startFixtureUpstream(): Promise<FixtureUpstream> {
  const sockets = new Set<Socket>();
  const server = http.createServer((request, response) => {
    const url = new URL(request.url ?? "/", "http://fixture.local");
    response.setHeader("x-docs-fixture", "true");

    if (url.pathname === "/health") {
      response.writeHead(200, { "content-type": "text/plain; charset=utf-8" });
      response.end("ok");
      return;
    }

    if (url.pathname.startsWith("/api/")) {
      response.writeHead(200, { "content-type": "application/json; charset=utf-8" });
      response.end(
        JSON.stringify({
          ok: true,
          path: url.pathname,
          requestId: request.headers["x-request-id"] ?? "docs-fixture-request",
        }),
      );
      return;
    }

    if (url.pathname.startsWith("/assets/")) {
      response.writeHead(200, {
        "cache-control": "public, max-age=300",
        "content-type": url.pathname.endsWith(".css") ? "text/css; charset=utf-8" : "application/javascript; charset=utf-8",
      });
      response.end(url.pathname.endsWith(".css") ? "body { color: #24313f; }" : "window.docsFixture = true;");
      return;
    }

    if (url.pathname === "/slow") {
      setTimeout(() => {
        response.writeHead(200, { "content-type": "text/plain; charset=utf-8" });
        response.end("slow fixture response");
      }, 150);
      return;
    }

    if (url.pathname === "/error") {
      response.writeHead(503, { "content-type": "application/json; charset=utf-8" });
      response.end(JSON.stringify({ ok: false, error: "fixture upstream unavailable" }));
      return;
    }

    response.writeHead(200, { "content-type": "text/html; charset=utf-8" });
    response.end(`<!doctype html>
<html>
  <head><title>Docs Fixture App</title></head>
  <body>
    <main>
      <h1>Docs Fixture App</h1>
      <p>Deterministic upstream response for generated documentation screenshots.</p>
      <a href="/api/status">API status</a>
    </main>
  </body>
</html>`);
  });
  server.on("connection", (socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
  });

  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      resolve();
    });
  });

  const address = server.address() as AddressInfo;
  return {
    url: `http://127.0.0.1:${address.port}`,
    close: () => {
      for (const socket of sockets) {
        socket.destroy();
      }
      return new Promise<void>((resolve, reject) => {
        server.close((error) => (error ? reject(error) : resolve()));
      });
    },
  };
}

export async function seedDocsFixture(
  request: APIRequestContext,
  baseURL: string,
  options: SeedOptions,
): Promise<DocsFixtureState> {
  const publicConfig = await connectRPC<any>(request, baseURL, "GetPublicProxyConfig", {});
  const httpListener = publicConfig.listeners.find((listener: JsonRecord) => listener.name === "public-http");
  const httpsListener = publicConfig.listeners.find((listener: JsonRecord) => listener.name === "public-https");
  expect(httpListener).toBeTruthy();
  expect(httpsListener).toBeTruthy();

  const updatedHttpListener = await connectRPC<any>(request, baseURL, "UpdatePublicListener", {
    id: httpListener.id,
    name: "docs-http-edge",
    bindAddress: "127.0.0.1",
    port: Number(options.httpPort),
    enabled: true,
    protocol: "PUBLIC_LISTENER_PROTOCOL_HTTP",
  });

  const updatedHttpsListener = await connectRPC<any>(request, baseURL, "UpdatePublicListener", {
    id: httpsListener.id,
    name: "docs-https-edge",
    bindAddress: "127.0.0.1",
    port: Number(options.httpsPort),
    enabled: true,
    protocol: "PUBLIC_LISTENER_PROTOCOL_HTTPS",
  });

  const bootstrapAgent =
    publicConfig.agents.find((agent: JsonRecord) => agent.publicId === "docs-agent-zurich") ?? publicConfig.agents[0];
  expect(bootstrapAgent).toBeTruthy();

  const updatedBootstrapAgent = await connectRPC<any>(request, baseURL, "UpdateAgent", {
    id: bootstrapAgent.id,
    name: "Zurich Home Lab Agent",
    labels: {
      site: "home-lab",
      region: "zurich",
      role: "app",
      env: "docs",
    },
    enabled: true,
  });

  const offlineAgent = await connectRPC<any>(request, baseURL, "CreateAgent", {
    name: "Remote Backup Agent",
    labels: {
      site: "remote",
      region: "basel",
      role: "backup",
      env: "docs",
    },
    enabled: true,
  });

  const disabledAgent = await connectRPC<any>(request, baseURL, "CreateAgent", {
    name: "Disabled Maintenance Agent",
    labels: {
      site: "lab",
      region: "geneva",
      role: "maintenance",
      env: "docs",
    },
    enabled: false,
  });

  const templates = {
    generic: await createResponseTemplate(request, baseURL, {
      name: "docs-generic-json",
      kind: "PUBLIC_RESPONSE_TEMPLATE_KIND_GENERIC_BODY",
      statusCode: 200,
      contentType: "application/json; charset=utf-8",
      body: '{"ok": true, "message": "served by p2pstream"}',
    }),
    maintenance: await createResponseTemplate(request, baseURL, {
      name: "docs-maintenance-page",
      kind: "PUBLIC_RESPONSE_TEMPLATE_KIND_GENERIC_BODY",
      statusCode: 503,
      contentType: "text/html; charset=utf-8",
      body: "<!doctype html><h1>Maintenance window</h1><p>The fixture app is temporarily paused.</p>",
    }),
    rateLimit: await createResponseTemplate(request, baseURL, {
      name: "docs-rate-limit-page",
      kind: "PUBLIC_RESPONSE_TEMPLATE_KIND_GENERIC_BODY",
      statusCode: 429,
      contentType: "text/html; charset=utf-8",
      body: "<!doctype html><h1>Too many requests</h1><p>Please try again later.</p>",
    }),
    wafBlock: await createResponseTemplate(request, baseURL, {
      name: "docs-waf-block-page",
      kind: "PUBLIC_RESPONSE_TEMPLATE_KIND_GENERIC_BODY",
      statusCode: 403,
      contentType: "text/html; charset=utf-8",
      body: "<!doctype html><h1>Request blocked</h1><p>Reference: docs-fixture</p>",
    }),
    captcha: await createResponseTemplate(request, baseURL, {
      name: "docs-captcha-page",
      kind: "PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_CAPTCHA_PAGE",
      statusCode: 403,
      contentType: "text/html; charset=utf-8",
      body: "<!doctype html><h1>Verification required</h1><p>{{ .host }} needs an extra check.</p>{{ .captcha_element_html }}",
    }),
    waitingRoom: await createResponseTemplate(request, baseURL, {
      name: "docs-waiting-room-page",
      kind: "PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_WAITING_ROOM_PAGE",
      statusCode: 202,
      contentType: "text/html; charset=utf-8",
      body: "<!doctype html><h1>Waiting room</h1><p>Position {{ .queue_position }}. Retry in {{ .retry_after_seconds }} seconds.</p>",
    }),
  };

  const directRoute = await connectRPC<any>(request, baseURL, "CreatePublicRoute", {
    listenerId: updatedHttpListener.listener.id,
    enabled: true,
    action: "PUBLIC_ROUTE_ACTION_FORWARD",
    priority: 10,
    hostPattern: "app.example.test",
    pathPrefix: "/",
    targetLoadBalancing: "PUBLIC_ROUTE_TARGET_LOAD_BALANCING_ROUND_ROBIN",
    targets: [
      {
        name: "Local app upstream",
        position: 0,
        priorityGroup: 0,
        enabled: true,
        targetType: "PUBLIC_ROUTE_TARGET_TYPE_PROXY",
        transport: "PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT",
        url: options.upstreamURL,
        weight: 1,
        upstreamRequestHeaders: [
          { name: "x-docs-route", value: "direct" },
          { name: "x-forwarded-proto", value: "http" },
        ],
        upstreamResponseHeaderTimeoutMillis: "10000",
      },
    ],
  });

  const agentRoute = await connectRPC<any>(request, baseURL, "CreatePublicRoute", {
    listenerId: updatedHttpListener.listener.id,
    enabled: true,
    action: "PUBLIC_ROUTE_ACTION_FORWARD",
    priority: 20,
    hostPattern: "api.example.test",
    pathPrefix: "/api",
    targetLoadBalancing: "PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_LEAST_ACTIVE_REQUESTS",
    targets: [
      {
        name: "Home lab agent pool",
        position: 0,
        priorityGroup: 0,
        enabled: true,
        targetType: "PUBLIC_ROUTE_TARGET_TYPE_PROXY",
        transport: "PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT",
        url: options.upstreamURL,
        weight: 2,
        agentSelector: {
          matchLabels: {
            site: "home-lab",
            role: "app",
          },
        },
        agentLoadBalancing: "PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_LEAST_ACTIVE_REQUESTS",
        upstreamResponseHeaderTimeoutMillis: "15000",
      },
    ],
  });

  const staticRoute = await connectRPC<any>(request, baseURL, "CreatePublicRoute", {
    listenerId: updatedHttpListener.listener.id,
    enabled: true,
    action: "PUBLIC_ROUTE_ACTION_FORWARD",
    priority: 30,
    hostPattern: "maintenance.example.test",
    pathPrefix: "/",
    targetLoadBalancing: "PUBLIC_ROUTE_TARGET_LOAD_BALANCING_ROUND_ROBIN",
    targets: [
      {
        name: "Maintenance page",
        position: 0,
        priorityGroup: 0,
        enabled: true,
        targetType: "PUBLIC_ROUTE_TARGET_TYPE_STATIC",
        transport: "PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT",
        staticStatusCode: 503,
        staticResponseBodyMode: "PUBLIC_RESPONSE_BODY_MODE_TEMPLATE",
        staticResponseTemplateId: templates.maintenance.template.id,
        staticResponseHeaders: [
          { name: "content-type", value: "text/html; charset=utf-8" },
          { name: "retry-after", value: "120" },
        ],
        weight: 1,
      },
    ],
  });

  const redirectRoute = await connectRPC<any>(request, baseURL, "CreatePublicRoute", {
    listenerId: updatedHttpListener.listener.id,
    enabled: true,
    action: "PUBLIC_ROUTE_ACTION_REDIRECT",
    priority: 40,
    hostPattern: "old.example.test",
    pathPrefix: "/docs",
    redirectTargetMode: "PUBLIC_ROUTE_REDIRECT_TARGET_MODE_EXTERNAL_ORIGIN_KEEP_PATH",
    redirectTarget: "https://docs.example.test",
    redirectStatusCode: 308,
    redirectPreservePathSuffix: true,
    redirectPreserveQuery: true,
  });

  const fallbackRoute = await connectRPC<any>(request, baseURL, "CreatePublicRoute", {
    listenerId: updatedHttpListener.listener.id,
    enabled: true,
    action: "PUBLIC_ROUTE_ACTION_FORWARD",
    priority: 1000,
    hostPattern: "fallback.example.test",
    pathPrefix: "/",
    targetLoadBalancing: "PUBLIC_ROUTE_TARGET_LOAD_BALANCING_ROUND_ROBIN",
    targets: [
      {
        name: "Fallback JSON",
        position: 0,
        priorityGroup: 0,
        enabled: true,
        targetType: "PUBLIC_ROUTE_TARGET_TYPE_STATIC",
        transport: "PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT",
        staticStatusCode: 404,
        staticResponseBodyMode: "PUBLIC_RESPONSE_BODY_MODE_INLINE",
        staticResponseBody: '{"error":"route not found"}',
        staticResponseHeaders: [{ name: "content-type", value: "application/json; charset=utf-8" }],
        weight: 1,
      },
    ],
  });

  await connectRPC<any>(request, baseURL, "UpdatePublicCacheSettings", {
    enabled: true,
    maxDiskBytes: "104857600",
    maxMemoryBytes: "67108864",
    memoryHotObjectMaxBytes: "1048576",
    maxEntries: 500,
    cleanupIntervalMillis: "30000",
  });

  const cacheRule = await connectRPC<any>(request, baseURL, "CreatePublicCacheRule", {
    name: "cache-static-assets",
    enabled: true,
    priority: 10,
    scope: "PUBLIC_CACHE_SCOPE_ROUTE",
    routeIds: [directRoute.route.id],
    targetIds: [directRoute.route.targets[0].id],
    ttlMode: "PUBLIC_CACHE_TTL_MODE_FIXED",
    ttlMillis: "300000",
    cacheStatusCodes: [200],
    matchRule: { celExpression: 'method == "GET" && path_prefix(path, "/assets")' },
    queryMode: "PUBLIC_CACHE_QUERY_MODE_FULL",
    varyHeaders: ["accept-encoding"],
    allowCookieRequests: false,
    maxObjectBytes: "1048576",
    addCacheStatusHeader: true,
  });

  const rateLimitRule = await connectRPC<any>(request, baseURL, "CreatePublicRateLimitRule", {
    name: "api-burst-protection",
    enabled: true,
    priority: 10,
    algorithm: "PUBLIC_RATE_LIMIT_ALGORITHM_SLIDING_WINDOW",
    limit: 120,
    windowMillis: "60000",
    burst: 20,
    matchRule: { celExpression: 'host == "api.example.test" && path_prefix(path, "/api")' },
    keyParts: [
      { source: "PUBLIC_RATE_LIMIT_KEY_SOURCE_REMOTE_IP" },
      { source: "PUBLIC_RATE_LIMIT_KEY_SOURCE_HOST" },
    ],
    responseStatusCode: 429,
    responseContentType: "text/html; charset=utf-8",
    responseBodyMode: "PUBLIC_RESPONSE_BODY_MODE_TEMPLATE",
    responseBodyTemplateId: templates.rateLimit.template.id,
  });

  const trafficShaperRule = await connectRPC<any>(request, baseURL, "CreatePublicTrafficShaperRule", {
    name: "cap-large-downloads",
    enabled: true,
    priority: 20,
    budgetScope: "PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_KEY",
    matchRule: { celExpression: 'path_prefix(path, "/assets")' },
    keyParts: [
      { source: "PUBLIC_RATE_LIMIT_KEY_SOURCE_REMOTE_IP" },
      { source: "PUBLIC_RATE_LIMIT_KEY_SOURCE_PATH" },
    ],
    downloadBytesPerSecond: "262144",
    uploadBytesPerSecond: "131072",
    burstBytes: "524288",
    requestExemptBytes: "4096",
    responseExemptBytes: "4096",
  });

  const captchaProvider = await connectRPC<any>(request, baseURL, "CreatePublicWafCaptchaProvider", {
    name: "docs-turnstile-provider",
    enabled: true,
    providerType: "PUBLIC_WAF_CAPTCHA_PROVIDER_TYPE_TURNSTILE",
    siteKey: "1x00000000000000000000AA",
    secretKey: "fixture-turnstile-secret",
  });

  const wafRule = await connectRPC<any>(request, baseURL, "CreatePublicWafRule", {
    name: "challenge-admin-paths",
    enabled: true,
    priority: 10,
    activationMode: "PUBLIC_WAF_ACTIVATION_MODE_AUTOMATIC",
    matchRule: { celExpression: 'path_prefix(path, "/admin")' },
    action: "PUBLIC_WAF_RULE_ACTION_CAPTCHA",
    captchaProviderId: captchaProvider.provider.id,
    captchaPassTtlMillis: "900000",
    blockResponseStatusCode: 403,
    blockResponseContentType: "text/html; charset=utf-8",
    blockResponseBodyMode: "PUBLIC_RESPONSE_BODY_MODE_TEMPLATE",
    blockResponseTemplateId: templates.wafBlock.template.id,
    captchaPageTemplateId: templates.captcha.template.id,
    keyParts: [{ source: "PUBLIC_RATE_LIMIT_KEY_SOURCE_REMOTE_IP" }],
    triggers: {
      requestWindowMillis: "60000",
      minimumRequestRate: 180,
      trafficSpikeMultiplier: 2.5,
      proxyActiveRequests: 200,
      minimumActiveMillis: "30000",
      quietPeriodMillis: "120000",
    },
  });

  const dnsCredential = await connectRPC<any>(request, baseURL, "CreatePublicTlsDnsCredential", {
    name: "cloudflare-docs-zone",
    provider: "PUBLIC_DNS_PROVIDER_CLOUDFLARE",
    cloudflareZoneId: "023e105f4ecef8ad9ca31a8372d0c353",
    apiToken: "fixture-cloudflare-token",
    enabled: true,
  });

  const httpChallenge = await connectRPC<any>(request, baseURL, "CreatePublicTlsCertificate", {
    listenerId: updatedHttpsListener.listener.id,
    hostnamePattern: "app.example.test",
    enabled: true,
    source: "PUBLIC_TLS_CERTIFICATE_SOURCE_ACME",
    acmeEmail: "admin@example.test",
    acmeCa: "PUBLIC_ACME_CA_LETS_ENCRYPT_STAGING",
    acmeChallengeType: "PUBLIC_ACME_CHALLENGE_TYPE_HTTP_01",
  });

  const dnsChallenge = await connectRPC<any>(request, baseURL, "CreatePublicTlsCertificate", {
    listenerId: updatedHttpsListener.listener.id,
    hostnamePattern: "*.example.test",
    enabled: true,
    source: "PUBLIC_TLS_CERTIFICATE_SOURCE_ACME",
    acmeEmail: "admin@example.test",
    acmeCa: "PUBLIC_ACME_CA_LETS_ENCRYPT_STAGING",
    acmeChallengeType: "PUBLIC_ACME_CHALLENGE_TYPE_DNS_01",
    dnsCredentialId: dnsCredential.credential.id,
  });

  const accessToken = await connectRPC<any>(request, baseURL, "CreateManagementAccessToken", {
    name: "Docs remote environment token",
    enabled: true,
  });

  const environment = await connectRPC<any>(request, baseURL, "CreateEnvironment", {
    name: "Remote staging",
    managementUrl: options.managementBaseURL,
    transport: "ENVIRONMENT_TRANSPORT_DIRECT",
    accessToken: accessToken.token,
    responseHeaderTimeoutMillis: "10000",
    enabled: true,
  });

  await connectRPC<any>(request, baseURL, "DiscoverEnvironmentCertificate", {
    id: environment.environment.id,
  });

  const state: DocsFixtureState = {
    listeners: {
      http: updatedHttpListener.listener,
      https: updatedHttpsListener.listener,
    },
    agents: {
      bootstrap: updatedBootstrapAgent.agent,
      offline: offlineAgent.agent,
      disabled: disabledAgent.agent,
    },
    routes: {
      direct: directRoute.route,
      agent: agentRoute.route,
      staticResponse: staticRoute.route,
      redirect: redirectRoute.route,
      fallback: fallbackRoute.route,
    },
    targets: {
      direct: directRoute.route.targets[0],
      agent: agentRoute.route.targets[0],
      staticResponse: staticRoute.route.targets[0],
      fallback: fallbackRoute.route.targets[0],
    },
    templates: Object.fromEntries(Object.entries(templates).map(([key, value]) => [key, value.template])) as DocsFixtureState["templates"],
    wafRule: wafRule.rule,
    captchaProvider: captchaProvider.provider,
    rateLimitRule: rateLimitRule.rule,
    cacheRule: cacheRule.rule,
    trafficShaperRule: trafficShaperRule.rule,
    dnsCredential: dnsCredential.credential,
    tls: {
      httpChallenge: httpChallenge.tlsCertificate,
      dnsChallenge: dnsChallenge.tlsCertificate,
    },
    environment: environment.environment,
    accessToken: accessToken.accessToken,
  };

  seedTelemetry(options.databasePath, state);
  return state;
}

export async function sendPublicProxyTraffic(request: APIRequestContext, httpPort: string): Promise<void> {
  const urls = [
    { host: "app.example.test", path: "/" },
    { host: "app.example.test", path: "/admin" },
    { host: "app.example.test", path: "/assets/app.css" },
    { host: "api.example.test", path: "/api/status" },
    { host: "maintenance.example.test", path: "/" },
    { host: "old.example.test", path: "/docs/start?from=legacy" },
    { host: "missing.example.test", path: "/unknown" },
  ];

  for (const target of urls) {
    await request.get(`http://127.0.0.1:${httpPort}${target.path}`, {
      failOnStatusCode: false,
      maxRedirects: 0,
      headers: {
        host: target.host,
        "user-agent": "p2pstream-docs-screenshot/1.0",
        "x-request-id": `docs-${target.host}-${target.path}`.replace(/[^a-z0-9]+/gi, "-"),
      },
    });
  }
}

function createResponseTemplate(
  request: APIRequestContext,
  baseURL: string,
  template: {
    name: string;
    kind: string;
    statusCode: number;
    contentType: string;
    body: string;
  },
) {
  return connectRPC<any>(request, baseURL, "CreatePublicResponseTemplate", {
    name: template.name,
    kind: template.kind,
    description: "Generated fixture template for documentation screenshots.",
    contentType: template.contentType,
    body: template.body,
  });
}

function seedTelemetry(databasePath: string, state: DocsFixtureState): void {
  const directRouteId = Number(state.routes.direct.id);
  const directTargetId = Number(state.targets.direct.id);
  const agentRouteId = Number(state.routes.agent.id);
  const agentTargetId = Number(state.targets.agent.id);
  const staticRouteId = Number(state.routes.staticResponse.id);
  const staticTargetId = Number(state.targets.staticResponse.id);
  const redirectRouteId = Number(state.routes.redirect.id);
  const bootstrapAgentId = Number(state.agents.bootstrap.id);
  const offlineAgentId = Number(state.agents.offline.id);
  const disabledAgentId = Number(state.agents.disabled.id);

  const sql = `
PRAGMA foreign_keys = ON;

UPDATE agents
SET last_connected_at = datetime('now', '-2 minutes'),
    last_disconnected_at = NULL
WHERE id = ${bootstrapAgentId};

UPDATE agents
SET last_connected_at = datetime('now', '-5 hours'),
    last_disconnected_at = datetime('now', '-3 hours')
WHERE id = ${offlineAgentId};

UPDATE agents
SET last_connected_at = datetime('now', '-2 days'),
    last_disconnected_at = datetime('now', '-2 days')
WHERE id = ${disabledAgentId};

INSERT INTO agent_stats (
  agent_id, reported_at, memory_mb, goroutines, req_success, req_client_error,
  req_server_error, req_internal_error, bytes_rx, bytes_tx, cpu_percent
) VALUES
  (${bootstrapAgentId}, datetime('now', '-8 minutes'), 84, 33, 430, 8, 3, 0, 1269760, 8388608, 12.4),
  (${bootstrapAgentId}, datetime('now', '-4 minutes'), 92, 35, 700, 11, 5, 1, 1769472, 12582912, 18.1),
  (${bootstrapAgentId}, datetime('now', '-1 minutes'), 96, 38, 930, 14, 7, 1, 2244608, 16777216, 21.8),
  (${offlineAgentId}, datetime('now', '-3 hours'), 48, 0, 118, 1, 1, 0, 524288, 1048576, 0.0);

INSERT INTO proxy_request_events (
  occurred_at, status_code, duration_ms, error_kind, listener_id, route_target_id, route_id,
  waf_rule_id, waf_action, agent_id, request_bytes, response_bytes, cache_rule_id, cache_status, cache_bytes
) VALUES
  (datetime('now', '-18 minutes'), 200, 42, '', ${Number(state.listeners.http.id)}, ${directTargetId}, ${directRouteId}, NULL, '', NULL, 720, 6144, NULL, 'miss', 0),
  (datetime('now', '-16 minutes'), 200, 18, '', ${Number(state.listeners.http.id)}, ${directTargetId}, ${directRouteId}, NULL, '', NULL, 0, 3840, ${Number(state.cacheRule.id)}, 'hit', 3840),
  (datetime('now', '-13 minutes'), 200, 87, '', ${Number(state.listeners.http.id)}, ${agentTargetId}, ${agentRouteId}, NULL, '', ${bootstrapAgentId}, 420, 1280, NULL, 'bypass', 0),
  (datetime('now', '-11 minutes'), 503, 9, '', ${Number(state.listeners.http.id)}, ${staticTargetId}, ${staticRouteId}, NULL, '', NULL, 0, 912, NULL, 'bypass', 0),
  (datetime('now', '-7 minutes'), 308, 4, '', ${Number(state.listeners.http.id)}, NULL, ${redirectRouteId}, NULL, '', NULL, 0, 0, NULL, 'bypass', 0),
  (datetime('now', '-5 minutes'), 403, 3, '', ${Number(state.listeners.http.id)}, NULL, ${directRouteId}, ${Number(state.wafRule.id)}, 'captcha', NULL, 300, 2048, NULL, 'bypass', 0);

INSERT INTO connections (agent_id, connected_at, disconnected_at) VALUES
  (${offlineAgentId}, datetime('now', '-5 hours'), datetime('now', '-3 hours')),
  (${disabledAgentId}, datetime('now', '-2 days'), datetime('now', '-2 days'));

UPDATE observability_rollup_state
SET proxy_backfill_upper_id = COALESCE((SELECT MAX(id) FROM proxy_request_events), 0),
    proxy_backfilled_through_id = 0,
    agent_backfill_upper_id = COALESCE((SELECT MAX(id) FROM agent_stats), 0),
    agent_backfilled_through_id = 0
WHERE id = 1;
`;

  runSqlite(databasePath, sql);
}

function runSqlite(databasePath: string, sql: string): void {
  const result = spawnSync("sqlite3", [databasePath], {
    input: sql,
    encoding: "utf8",
  });

  if (result.error) {
    throw result.error;
  }

  if (result.status !== 0) {
    throw new Error(`sqlite3 exited with ${result.status}: ${result.stderr}`);
  }
}
