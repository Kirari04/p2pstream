import {
  ProxyState,
  PublicAcmeChallengeType,
  PublicBackendForwardMode,
  PublicBackendHealthTraceOutcome,
  PublicBackendHealthTraceSource,
  PublicBackendHealthStatus,
  PublicBackendLoadBalancing,
  PublicBackendType,
  PublicCacheQueryMode,
  PublicCacheScope,
  PublicCacheTtlMode,
  PublicListenerProtocol,
  PublicPolicyMatchBooleanOperator,
  PublicPolicyMatchConditionOperator,
  PublicPolicyMatchField,
  PublicRateLimitAlgorithm,
  PublicRateLimitKeySource,
  PublicRouteAction,
  PublicRouteRedirectTargetMode,
  PublicTlsCertificateSource,
  PublicTlsCertificateStatus,
  PublicTrafficShaperBudgetScope,
  PublicWafActivationMode,
  PublicWafCaptchaProviderType,
  PublicWafRuleAction,
  type Agent,
  type PublicBackend,
  type PublicBackendAgent,
  type PublicBackendHealthTrace,
  type PublicCacheRule,
  type PublicListener,
  type PublicListenerStatus,
  type PublicPolicyMatchCondition,
  type PublicPolicyMatchGroup,
  type PublicPolicyMatchRule,
  type PublicRateLimitRule,
  type PublicRoute,
  type PublicRouteBackend,
  type PublicTlsCertificate,
  type PublicTlsDnsCredential,
  type PublicTrafficShaperRule,
  type PublicWafCaptchaProvider,
  type PublicWafRule,
} from "@/gen/proto/p2pstream/v1/management_pb";

export type TlsMethod = "manual" | "http_01" | "tls_alpn_01" | "dns_01";

export function proxyStateLabel(state: ProxyState, proxyRunning = false): string {
  switch (state) {
    case ProxyState.STOPPED: return "Stopped";
    case ProxyState.STARTING: return "Starting";
    case ProxyState.RUNNING: return "Running";
    case ProxyState.STOPPING: return "Stopping";
    case ProxyState.ERROR: return "Error";
    default: return proxyRunning ? "Running" : "Unknown";
  }
}

export function severityForState(state: ProxyState): string {
  if (state === ProxyState.RUNNING) return "success";
  if (state === ProxyState.STARTING || state === ProxyState.STOPPING) return "warn";
  return "danger";
}

export function listenerRuntimeState(listener: PublicListener, status?: PublicListenerStatus): ProxyState {
  if (!listener.enabled) return ProxyState.STOPPED;
  return status?.state ?? ProxyState.STOPPED;
}

export function listenerStateLabel(listener: PublicListener, status?: PublicListenerStatus): string {
  if (!listener.enabled || status?.disabled) return "Disabled";
  return proxyStateLabel(listenerRuntimeState(listener, status));
}

export function backendName(id: bigint, backends: readonly PublicBackend[]): string {
  if (id === 0n) return "None";
  return backends.find((backend) => backend.id === id)?.name ?? `#${id.toString()}`;
}

export function agentName(id: bigint, agents: readonly Agent[]): string {
  const agent = agents.find((item) => item.id === id);
  return agent ? `${agent.name} (${agent.publicId})` : `#${id.toString()}`;
}

export function listenerName(id: bigint, listeners: readonly PublicListener[]): string {
  return listeners.find((listener) => listener.id === id)?.name ?? `#${id.toString()}`;
}

export function bindLabel(listener: PublicListener): string {
  return `${listener.bindAddress || "*"}:${listener.port.toString()}`;
}

export function protocolLabel(protocol: PublicListenerProtocol): string {
  return protocol === PublicListenerProtocol.HTTPS ? "HTTPS" : "HTTP";
}

export function backendTypeLabel(type: PublicBackendType): string {
  return type === PublicBackendType.STATIC ? "Static" : "Proxy forward";
}

export function forwardModeLabel(mode: PublicBackendForwardMode): string {
  return mode === PublicBackendForwardMode.AGENT_POOL ? "Agents" : "Direct";
}

export function routeAction(route: PublicRoute): PublicRouteAction {
  return route.action === PublicRouteAction.REDIRECT ? PublicRouteAction.REDIRECT : PublicRouteAction.FORWARD;
}

export function routeAssignments(route: PublicRoute, routeBackends: readonly PublicRouteBackend[]): Array<PublicRouteBackend | { backendId: bigint; enabled: boolean; weight: bigint }> {
  if (route.backendAssignments.length) return route.backendAssignments;
  const assignments = routeBackends.filter((assignment) => assignment.routeId === route.id);
  if (assignments.length) return assignments;
  return route.backendId > 0n ? [{ backendId: route.backendId, enabled: true, weight: 100n }] : [];
}

export function routeDestinationLabel(route: PublicRoute, backends: readonly PublicBackend[], routeBackends: readonly PublicRouteBackend[]): string {
  if (routeAction(route) === PublicRouteAction.REDIRECT) {
    return `Redirect ${route.redirectStatusCode || 302}`;
  }
  const assignments = routeAssignments(route, routeBackends);
  if (assignments.length > 1) return `${assignments.length.toString()} backends`;
  return backendName(assignments[0]?.backendId ?? route.backendId, backends);
}

export function routeTargetSummary(route: PublicRoute, backends: readonly PublicBackend[], routeBackends: readonly PublicRouteBackend[]): string {
  if (routeAction(route) !== PublicRouteAction.REDIRECT) {
    const assignments = routeAssignments(route, routeBackends);
    const names = assignments.map((assignment) => backendName(assignment.backendId, backends)).join(", ");
    const fallback = route.fallbackBackendId > 0n ? ` / fallback ${backendName(route.fallbackBackendId, backends)}` : "";
    return `${loadBalancingLabel(route.loadBalancing)} / ${names || backendName(route.backendId, backends)}${fallback}`;
  }
  const target = route.redirectTarget || redirectModeLabel(route.redirectTargetMode);
  return `${redirectModeLabel(route.redirectTargetMode)} -> ${target}`;
}

export function redirectModeLabel(mode: PublicRouteRedirectTargetMode): string {
  switch (mode) {
    case PublicRouteRedirectTargetMode.EXTERNAL_ORIGIN_KEEP_PATH:
      return "External origin";
    case PublicRouteRedirectTargetMode.ABSOLUTE_URL:
      return "Absolute URL";
    case PublicRouteRedirectTargetMode.SAME_HOST_PATH:
      return "Same host";
    default:
      return "Redirect";
  }
}

export function loadBalancingLabel(algorithm: PublicBackendLoadBalancing): string {
  switch (algorithm) {
    case PublicBackendLoadBalancing.WEIGHTED_ROUND_ROBIN: return "Weighted round-robin";
    case PublicBackendLoadBalancing.RANDOM: return "Random";
    case PublicBackendLoadBalancing.WEIGHTED_RANDOM: return "Weighted random";
    case PublicBackendLoadBalancing.LEAST_ACTIVE_REQUESTS: return "Least active";
    case PublicBackendLoadBalancing.WEIGHTED_LEAST_ACTIVE_REQUESTS: return "Weighted least active";
    default: return "Round-robin";
  }
}

export function backendHealthLabel(backend: PublicBackend): string {
  if (backend.backendType === PublicBackendType.PROXY_FORWARD && backend.forwardMode === PublicBackendForwardMode.AGENT_POOL) {
    return backendAgentAvailabilitySummary(backend, []);
  }
  if (!backend.healthCheck?.enabled) return "Health unknown";
  switch (backend.healthCheck.status) {
    case PublicBackendHealthStatus.HEALTHY:
      return "Healthy";
    case PublicBackendHealthStatus.UNHEALTHY:
      return "Unhealthy";
    case PublicBackendHealthStatus.DISABLED:
      return "Health disabled";
    default:
      return "Health unknown";
  }
}

export function backendHealthSeverity(backend: PublicBackend): "success" | "warn" | "danger" | "info" {
  if (backend.backendType === PublicBackendType.PROXY_FORWARD && backend.forwardMode === PublicBackendForwardMode.AGENT_POOL) {
    const { available, total } = backendAgentAvailability(backend, []);
    if (total === 0) return "warn";
    if (available === 0) return "danger";
    return available === total ? "success" : "warn";
  }
  switch (backend.healthCheck?.status) {
    case PublicBackendHealthStatus.HEALTHY:
      return "success";
    case PublicBackendHealthStatus.UNHEALTHY:
      return "danger";
    case PublicBackendHealthStatus.DISABLED:
      return "warn";
    default:
      return "info";
  }
}

export function healthTraceOutcomeLabel(outcome: PublicBackendHealthTraceOutcome): string {
  switch (outcome) {
    case PublicBackendHealthTraceOutcome.SUCCESS:
      return "Success";
    case PublicBackendHealthTraceOutcome.FAILURE:
      return "Failed";
    case PublicBackendHealthTraceOutcome.SKIPPED:
      return "Skipped";
    default:
      return "Unknown";
  }
}

export function healthTraceOutcomeSeverity(outcome: PublicBackendHealthTraceOutcome): "success" | "warn" | "danger" | "info" {
  switch (outcome) {
    case PublicBackendHealthTraceOutcome.SUCCESS:
      return "success";
    case PublicBackendHealthTraceOutcome.FAILURE:
      return "danger";
    case PublicBackendHealthTraceOutcome.SKIPPED:
      return "warn";
    default:
      return "info";
  }
}

export function healthTraceSourceLabel(source: PublicBackendHealthTraceSource): string {
  switch (source) {
    case PublicBackendHealthTraceSource.ACTIVE_CHECK:
      return "Active check";
    case PublicBackendHealthTraceSource.PASSIVE_FAILURE:
      return "Passive failure";
    case PublicBackendHealthTraceSource.AGENT_CONNECTIVITY:
      return "Agent connectivity";
    default:
      return "Unknown";
  }
}

export function healthTraceReasonSummary(trace: PublicBackendHealthTrace): string {
  if (trace.errorKind === "success" || trace.outcome === PublicBackendHealthTraceOutcome.SUCCESS) {
    return trace.statusCode ? `HTTP ${trace.statusCode.toString()}` : "OK";
  }
  if (trace.errorKind === "unexpected_status" && trace.statusCode) {
    return `HTTP ${trace.statusCode.toString()} outside ${trace.expectedStatusMin.toString()}-${trace.expectedStatusMax.toString()}`;
  }
  if (trace.error) return trace.error;
  switch (trace.errorKind) {
    case "timeout": return "Timed out";
    case "health_check_timeout": return "Health check timed out";
    case "health_check_cancelled": return "Health check cancelled";
    case "request_failed": return "Request failed";
    case "response_body_read_failed": return "Response body read failed";
    case "request_encode_failed": return "Request encode failed";
    case "agent_disconnected": return "Agent disconnected";
    case "agent_failed": return "Agent failed";
    case "response_decode_failed": return "Response decode failed";
    case "health_check_skipped": return "Health check skipped";
    case "passive_failure": return "Passive failure";
    case "agent_connected": return "Agent connected";
    case "agent_disconnected_event": return "Agent disconnected";
    default: return trace.errorKind || "No reason captured";
  }
}

export function healthTraceTransitionSummary(trace: PublicBackendHealthTrace): string {
  const before = healthStatusLabel(trace.statusBefore);
  const after = healthStatusLabel(trace.statusAfter);
  const availability = trace.availableAfter ? "available" : "unavailable";
  if (before === after) return `${after} / ${availability}`;
  return `${before} -> ${after} / ${availability}`;
}

export function healthTraceTargetLabel(trace: PublicBackendHealthTrace, agents: readonly Agent[] = []): string {
  if (!trace.agentId) return "Direct";
  const agent = agents.find((item) => item.id === trace.agentId);
  if (agent) return agentName(trace.agentId, agents);
  if (trace.agentName) return trace.agentPublicId ? `${trace.agentName} (${trace.agentPublicId})` : trace.agentName;
  if (trace.agentPublicId) return trace.agentPublicId;
  return `#${trace.agentId.toString()}`;
}

function healthStatusLabel(status: PublicBackendHealthStatus): string {
  switch (status) {
    case PublicBackendHealthStatus.HEALTHY:
      return "Healthy";
    case PublicBackendHealthStatus.UNHEALTHY:
      return "Unhealthy";
    case PublicBackendHealthStatus.DISABLED:
      return "Disabled";
    case PublicBackendHealthStatus.DISCONNECTED:
      return "Disconnected";
    default:
      return "Unknown";
  }
}

export function rateLimitAlgorithmLabel(algorithm: PublicRateLimitAlgorithm): string {
  switch (algorithm) {
    case PublicRateLimitAlgorithm.SLIDING_WINDOW: return "Sliding window";
    case PublicRateLimitAlgorithm.TOKEN_BUCKET: return "Token bucket";
    case PublicRateLimitAlgorithm.LEAKY_BUCKET: return "Leaky bucket";
    default: return "Fixed window";
  }
}

export function durationMillisLabel(windowMillis: bigint): string {
  const seconds = Math.max(1, Math.round(Number(windowMillis || 0n) / 1000));
  if (seconds < 60) return `${seconds.toString()}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60).toString()}m`;
  return `${Math.round(seconds / 3600).toString()}h`;
}

export function rateLimitRuleSummary(rule: PublicRateLimitRule): string {
  const burst = rule.algorithm === PublicRateLimitAlgorithm.TOKEN_BUCKET || rule.algorithm === PublicRateLimitAlgorithm.LEAKY_BUCKET
    ? `, burst ${Number(rule.burst || rule.limit).toString()}`
    : "";
  return `${Number(rule.limit).toString()} / ${durationMillisLabel(rule.windowMillis)}${burst}`;
}

export function publicPolicyMatchSummary(rule: PublicRateLimitRule | PublicWafRule | PublicTrafficShaperRule): string {
  return policyMatchRuleSummary(rule.matchRule);
}

function policyMatchRuleSummary(rule?: PublicPolicyMatchRule): string {
  const builderSummary = rule?.builder?.root ? policyMatchBuilderSummary(rule.builder.root) : "";
  if (builderSummary) return builderSummary;
  const expression = rule?.celExpression?.trim() ?? "";
  if (!expression || expression === "true") return "Any request";
  return `CEL: ${truncateText(expression, 72)}`;
}

function policyMatchBuilderSummary(root: PublicPolicyMatchGroup): string {
  if (policyMatchGroupIsComplex(root)) return "Complex builder rule";
  const conditions = collectPolicyMatchConditions(root);
  if (!conditions.length) return "";
  const visible = conditions.slice(0, 3).map(policyMatchConditionSummary);
  const extra = conditions.length - visible.length;
  return extra > 0 ? `${visible.join(" / ")} / +${extra.toString()}` : visible.join(" / ");
}

function policyMatchGroupIsComplex(group: PublicPolicyMatchGroup): boolean {
  return group.negated ||
    Boolean(group.operator && group.operator !== PublicPolicyMatchBooleanOperator.ALL) ||
    group.groups.length > 0;
}

function collectPolicyMatchConditions(group: PublicPolicyMatchGroup): PublicPolicyMatchCondition[] {
  return [
    ...group.conditions.filter(policyMatchConditionHasContent),
    ...group.groups.flatMap(collectPolicyMatchConditions),
  ];
}

function policyMatchConditionHasContent(condition: PublicPolicyMatchCondition): boolean {
  if ((condition.field === PublicPolicyMatchField.HEADER ||
    condition.field === PublicPolicyMatchField.COOKIE ||
    condition.field === PublicPolicyMatchField.QUERY_PARAM) && !condition.name.trim()) {
    return false;
  }
  return condition.operator === PublicPolicyMatchConditionOperator.PRESENT || condition.values.length > 0;
}

function policyMatchConditionSummary(condition: PublicPolicyMatchCondition): string {
  const target = policyMatchConditionTarget(condition);
  if (condition.operator === PublicPolicyMatchConditionOperator.PRESENT) {
    return condition.negated ? `not ${target} present` : `${target} present`;
  }
  const body = `${target} ${policyMatchOperatorLabel(condition.operator)} ${policyMatchValuesSummary(condition.values)}`;
  return condition.negated ? `not ${body}` : body;
}

function policyMatchConditionTarget(condition: PublicPolicyMatchCondition): string {
  const name = condition.name.trim();
  switch (condition.field) {
    case PublicPolicyMatchField.METHOD: return "method";
    case PublicPolicyMatchField.PROTOCOL: return "protocol";
    case PublicPolicyMatchField.HOST: return "host";
    case PublicPolicyMatchField.REMOTE_IP: return "ip";
    case PublicPolicyMatchField.HEADER: return name ? `header:${name}` : "header";
    case PublicPolicyMatchField.COOKIE: return name ? `cookie:${name}` : "cookie";
    case PublicPolicyMatchField.QUERY_PARAM: return name ? `query:${name}` : "query";
    default: return "path";
  }
}

function policyMatchOperatorLabel(operator: PublicPolicyMatchConditionOperator): string {
  switch (operator) {
    case PublicPolicyMatchConditionOperator.PREFIX: return "prefix";
    case PublicPolicyMatchConditionOperator.SUFFIX: return "suffix";
    case PublicPolicyMatchConditionOperator.CONTAINS: return "contains";
    case PublicPolicyMatchConditionOperator.MATCHES: return "matches";
    case PublicPolicyMatchConditionOperator.IN: return "in";
    case PublicPolicyMatchConditionOperator.CIDR: return "cidr";
    case PublicPolicyMatchConditionOperator.HOST_PATTERN: return "matches";
    default: return "=";
  }
}

function policyMatchValuesSummary(values: readonly string[]): string {
  const visible = values.slice(0, 2);
  const extra = values.length - visible.length;
  const suffix = extra > 0 ? `,+${extra.toString()}` : "";
  return `${visible.join(",")}${suffix}`;
}

function truncateText(value: string, maxLength: number): string {
  return value.length > maxLength ? `${value.slice(0, Math.max(0, maxLength - 3))}...` : value;
}

export function rateLimitKeySummary(rule: PublicRateLimitRule | PublicWafRule): string {
  const parts = rule.keyParts.length ? rule.keyParts : [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  return parts.map((part) => {
    const label = rateLimitKeySourceLabel(part.source);
    return part.name ? `${label}:${part.name}` : label;
  }).join(" + ");
}

export function rateLimitKeySourceLabel(source: PublicRateLimitKeySource): string {
  switch (source) {
    case PublicRateLimitKeySource.HOST: return "host";
    case PublicRateLimitKeySource.METHOD: return "method";
    case PublicRateLimitKeySource.PATH: return "path";
    case PublicRateLimitKeySource.PROTOCOL: return "protocol";
    case PublicRateLimitKeySource.HEADER: return "header";
    case PublicRateLimitKeySource.COOKIE: return "cookie";
    case PublicRateLimitKeySource.QUERY_PARAM: return "query";
    default: return "ip";
  }
}

export function wafActionLabel(action: PublicWafRuleAction): string {
  switch (action) {
    case PublicWafRuleAction.CAPTCHA: return "Captcha";
    case PublicWafRuleAction.WAITING_ROOM: return "Waiting room";
    default: return "Block";
  }
}

export function wafActivationLabel(mode: PublicWafActivationMode): string {
  return mode === PublicWafActivationMode.AUTOMATIC ? "Automatic" : "Always";
}

export function wafProviderLabel(type: PublicWafCaptchaProviderType): string {
  switch (type) {
    case PublicWafCaptchaProviderType.HCAPTCHA: return "hCaptcha";
    case PublicWafCaptchaProviderType.RECAPTCHA_V2: return "reCAPTCHA v2";
    default: return "Turnstile";
  }
}

export function wafRuleSummary(rule: PublicWafRule, providers: readonly PublicWafCaptchaProvider[]): string {
  if (rule.action === PublicWafRuleAction.CAPTCHA) {
    const provider = providers.find((item) => item.id === rule.captchaProviderId);
    return `provider ${provider?.name ?? "missing"} / pass ${durationMillisLabel(rule.captchaPassTtlMillis)}`;
  }
  if (rule.action === PublicWafRuleAction.WAITING_ROOM) {
    const room = rule.waitingRoom;
    return `capacity ${room?.maxAdmittedSessions?.toString() ?? "50"} / admit ${room?.admissionRatePerSecond?.toString() ?? "10"}/s`;
  }
  return `response ${rule.blockResponseStatusCode.toString()}`;
}

export function cacheTtlModeLabel(mode: PublicCacheTtlMode): string {
  return mode === PublicCacheTtlMode.ORIGIN ? "Origin TTL" : "Fixed TTL";
}

export function cacheScopeLabel(scope: PublicCacheScope): string {
  return scope === PublicCacheScope.ROUTE ? "Route scope" : "Backend scope";
}

export function cacheQueryModeLabel(mode: PublicCacheQueryMode): string {
  switch (mode) {
    case PublicCacheQueryMode.IGNORE: return "ignore query";
    case PublicCacheQueryMode.ALLOWLIST: return "allowlist query";
    case PublicCacheQueryMode.DENYLIST: return "denylist query";
    default: return "full query";
  }
}

export function cacheRuleSummary(rule: PublicCacheRule): string {
  const ttl = durationMillisLabel(rule.ttlMillis);
  const statuses = rule.cacheStatusCodes.length ? rule.cacheStatusCodes.join(",") : "200,203,204,301,308";
  const maxMb = Math.max(1, Math.round(Number(rule.maxObjectBytes || 0n) / 1024 / 1024));
  const cookies = rule.allowCookieRequests ? " / cookie requests" : "";
  return `${cacheTtlModeLabel(rule.ttlMode)} ${ttl} / status ${statuses} / max ${maxMb.toString()} MiB${cookies}`;
}

export function cacheRuleMatchSummary(rule: PublicCacheRule): string {
  const parts: string[] = [];
  const matchSummary = policyMatchRuleSummary(rule.matchRule);
  if (matchSummary !== "Any request") parts.push(matchSummary);
  if (rule.routeIds.length) parts.push(`${rule.routeIds.length.toString()} route${rule.routeIds.length === 1 ? "" : "s"}`);
  if (rule.backendIds.length) parts.push(`${rule.backendIds.length.toString()} backend${rule.backendIds.length === 1 ? "" : "s"}`);
  return parts.length ? parts.join(" / ") : "Any request";
}

export function bytesToMiB(value: bigint): number {
  return Math.max(1, Math.round(Number(value || 0n) / 1024 / 1024));
}

export function bytesToKiB(value: bigint): number {
  return Math.max(1, Math.round(Number(value || 0n) / 1024));
}

export function miBToBytes(value: number): bigint {
  return BigInt(Math.max(1, Math.round(value || 0)) * 1024 * 1024);
}

export function kiBToBytes(value: number): bigint {
  return BigInt(Math.max(1, Math.round(value || 0)) * 1024);
}

export function trafficShaperScopeLabel(scope: PublicTrafficShaperBudgetScope): string {
  return scope === PublicTrafficShaperBudgetScope.PER_REQUEST ? "Per request" : "Per key";
}

export function trafficShaperBytesLabel(bytes: bigint): string {
  const value = Number(bytes || 0n);
  if (value <= 0) return "unlimited";
  const kib = value / 1024;
  if (kib < 1024) return `${Math.round(kib).toString()} KiB/s`;
  return `${(kib / 1024).toFixed(1)} MiB/s`;
}

export function trafficShaperKibLabel(bytes: bigint): string {
  const value = Number(bytes || 0n);
  if (value <= 0) return "0 KiB";
  const kib = value / 1024;
  if (kib < 1024) return `${Math.round(kib).toString()} KiB`;
  return `${(kib / 1024).toFixed(1)} MiB`;
}

export function trafficShaperRuleSummary(rule: PublicTrafficShaperRule): string {
  return `up ${trafficShaperBytesLabel(rule.uploadBytesPerSecond)} / down ${trafficShaperBytesLabel(rule.downloadBytesPerSecond)}`;
}

export function trafficShaperBudgetSummary(rule: PublicTrafficShaperRule): string {
  const burst = trafficShaperKibLabel(rule.burstBytes);
  const requestFree = trafficShaperKibLabel(rule.requestExemptBytes);
  const responseFree = trafficShaperKibLabel(rule.responseExemptBytes);
  return `burst ${burst} / free req ${requestFree}, res ${responseFree}`;
}

export function trafficShaperKeySummary(rule: PublicTrafficShaperRule): string {
  if (rule.budgetScope === PublicTrafficShaperBudgetScope.PER_REQUEST) return "per request";
  const parts = rule.keyParts.length ? rule.keyParts : [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  return parts.map((part) => {
    const label = rateLimitKeySourceLabel(part.source);
    return part.name ? `${label}:${part.name}` : label;
  }).join(" + ");
}

export function backendSummary(backend: PublicBackend): string {
  if (backend.backendType === PublicBackendType.STATIC) {
    const body = backend.staticResponseBody.trim();
    const suffix = body ? ` - ${body.slice(0, 72)}` : "";
    return `${backend.staticStatusCode.toString()}${suffix}`;
  }
  return backend.targetOrigin;
}

export function assignmentsForBackend(backend: PublicBackend, backendAgents: readonly PublicBackendAgent[]): PublicBackendAgent[] {
  if (backend.agentAssignments.length) return backend.agentAssignments;
  return backendAgents.filter((assignment) => assignment.backendId === backend.id);
}

export function backendAgentSummary(backend: PublicBackend, backendAgents: readonly PublicBackendAgent[], agents: readonly Agent[]): string {
  if (backend.backendType !== PublicBackendType.PROXY_FORWARD || backend.forwardMode !== PublicBackendForwardMode.AGENT_POOL) {
    return "";
  }
  const assignments = assignmentsForBackend(backend, backendAgents).filter((assignment) => assignment.enabled);
  if (!assignments.length) return "No enabled agents";
  return assignments.map((assignment) => `${agentName(assignment.agentId, agents)} ${backendAgentHealthLabel(assignment)} x${assignment.weight.toString()}`).join(", ");
}

export function backendAgentAvailability(backend: PublicBackend, backendAgents: readonly PublicBackendAgent[]): { available: number; total: number } {
  if (backend.backendType !== PublicBackendType.PROXY_FORWARD || backend.forwardMode !== PublicBackendForwardMode.AGENT_POOL) {
    return { available: 0, total: 0 };
  }
  const assignments = assignmentsForBackend(backend, backendAgents).filter((assignment) => assignment.enabled);
  return {
    available: assignments.filter((assignment) => assignment.health?.available).length,
    total: assignments.length,
  };
}

export function backendAgentAvailabilitySummary(backend: PublicBackend, backendAgents: readonly PublicBackendAgent[]): string {
  const { available, total } = backendAgentAvailability(backend, backendAgents);
  if (total === 0) return "No enabled agents";
  return `${available.toString()}/${total.toString()} ${total === 1 ? "agent" : "agents"} available`;
}

export function backendAgentHealthLabel(assignment: PublicBackendAgent): string {
  if (!assignment.enabled) return "disabled";
  switch (assignment.health?.status) {
    case PublicBackendHealthStatus.HEALTHY:
      return "healthy";
    case PublicBackendHealthStatus.UNHEALTHY:
      return "unhealthy";
    case PublicBackendHealthStatus.DISCONNECTED:
      return "disconnected";
    case PublicBackendHealthStatus.DISABLED:
      return "disabled";
    default:
      return "unknown";
  }
}

export function upstreamHeaderCount(backend: PublicBackend): number {
  return backend.upstreamRequestHeaders.length;
}

export function isDefaultSelfSignedCertificate(cert: PublicTlsCertificate): boolean {
  return cert.hostnamePattern === "p2pstream.local";
}

export function tlsCertificateSummary(cert: PublicTlsCertificate): string {
  if (isDefaultSelfSignedCertificate(cert)) return "Default self-signed certificate";
  if (cert.source === PublicTlsCertificateSource.ACME) {
    return "Managed by Let's Encrypt";
  }
  return "Manual certificate";
}

export function tlsCertificateValiditySummary(cert: PublicTlsCertificate): string {
  const issued = formatUnixMillis(cert.issuedAtUnixMillis);
  const expires = formatUnixMillis(cert.expiresAtUnixMillis);
  if (issued && expires) return `Valid ${issued} - ${expires}`;
  if (expires) return `Valid until ${expires}`;
  return "";
}

export function tlsCertificateRenewalSummary(cert: PublicTlsCertificate): string {
  if (cert.source !== PublicTlsCertificateSource.ACME) return "";
  const renewal = formatUnixMillis(cert.nextRenewalAtUnixMillis);
  return renewal ? `Renews ${renewal}` : "";
}

export function tlsMethodForCertificate(cert: PublicTlsCertificate): TlsMethod {
  if (cert.source !== PublicTlsCertificateSource.ACME) return "manual";
  if (cert.acmeChallengeType === PublicAcmeChallengeType.TLS_ALPN_01) return "tls_alpn_01";
  if (cert.acmeChallengeType === PublicAcmeChallengeType.DNS_01) return "dns_01";
  return "http_01";
}

export function tlsSourceLabel(cert: PublicTlsCertificate): string {
  switch (tlsMethodForCertificate(cert)) {
    case "http_01": return "HTTP-01";
    case "tls_alpn_01": return "TLS-ALPN-01";
    case "dns_01": return "DNS-01";
    default: return "Manual";
  }
}

export function tlsStatusLabel(cert: PublicTlsCertificate): string {
  if (!cert.enabled) return "Disabled";
  switch (cert.status) {
    case PublicTlsCertificateStatus.PENDING: return "Pending";
    case PublicTlsCertificateStatus.RENEWING: return "Renewing";
    case PublicTlsCertificateStatus.ERROR: return "Error";
    case PublicTlsCertificateStatus.READY: return "Ready";
    default: return cert.source === PublicTlsCertificateSource.ACME ? "Pending" : "Ready";
  }
}

export function tlsStatusSeverity(cert: PublicTlsCertificate): string {
  if (!cert.enabled) return "warn";
  if (cert.status === PublicTlsCertificateStatus.ERROR) return "danger";
  if (cert.status === PublicTlsCertificateStatus.PENDING || cert.status === PublicTlsCertificateStatus.RENEWING) return "warn";
  return "success";
}

export function acmeChallengeTypeForMethod(method: TlsMethod): PublicAcmeChallengeType {
  if (method === "tls_alpn_01") return PublicAcmeChallengeType.TLS_ALPN_01;
  if (method === "dns_01") return PublicAcmeChallengeType.DNS_01;
  return PublicAcmeChallengeType.HTTP_01;
}

export function tlsSourceForMethod(method: TlsMethod): PublicTlsCertificateSource {
  return method === "manual" ? PublicTlsCertificateSource.MANUAL : PublicTlsCertificateSource.ACME;
}

export function dnsCredentialName(id: bigint, credentials: readonly PublicTlsDnsCredential[]): string {
  if (id === 0n) return "None";
  return credentials.find((credential) => credential.id === id)?.name ?? `#${id.toString()}`;
}

export function formatUnixMillis(value: bigint): string {
  if (!value || value === 0n) return "";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium" }).format(new Date(Number(value)));
}
