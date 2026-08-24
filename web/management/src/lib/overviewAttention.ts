import { ProxyState, type DashboardWindowSummary } from "@/gen/proto/p2pstream/v1/management_pb";
import {
  trafficPolicyAttentionWarnings,
  type TrafficPolicyAttentionWarning,
  type TrafficPolicyKind,
  type TrafficPolicyWorkbenchConfig,
} from "@/lib/trafficPolicyWorkbench";

export type OverviewAttentionSeverity = "error" | "warning" | "info";

export type OverviewAttentionItem = Readonly<{
  key: string;
  severity: OverviewAttentionSeverity;
  title: string;
  detail: string;
  actionLabel: string;
  actionRoute: string;
}>;

type ListenerRuntimeInput = Readonly<{
  listenerId: bigint;
  state: ProxyState;
  lastError: string;
  running: boolean;
  disabled: boolean;
}>;

type ProxyRuntimeInput = Readonly<{
  state: ProxyState;
  lastError: string;
  listeners: readonly ListenerRuntimeInput[];
}>;

export type OverviewStatusInput = Readonly<{
  proxyRunning?: boolean;
  proxyLastError?: string;
  proxy?: ProxyRuntimeInput;
}>;

export type OverviewAttentionConfig = TrafficPolicyWorkbenchConfig & Readonly<{
  listeners?: readonly Readonly<{ id: bigint; enabled: boolean; name?: string }>[];
  proxy?: ProxyRuntimeInput;
  agents?: readonly Readonly<{ enabled: boolean; connected: boolean; name?: string }>[];
}>;

export type OverviewTrafficWindow = Partial<Pick<
  DashboardWindowSummary,
  "proxyRequests" | "proxyClientError" | "proxyServerError" | "proxyInternalError"
>>;

export type OverviewAttentionInput = Readonly<{
  status?: OverviewStatusInput | null;
  config?: OverviewAttentionConfig | null;
  trafficWindow?: OverviewTrafficWindow | null;
  /** Optional when the caller already computed policy warnings for another view. */
  policyWarnings?: readonly TrafficPolicyAttentionWarning[] | null;
}>;

const ACTIONABLE_POLICY_WARNING_CODES = new Set<TrafficPolicyAttentionWarning["code"]>([
  "duplicate-priority",
  "captcha-provider-missing",
  "captcha-provider-disabled",
  "captcha-provider-secret-missing",
  "cache-settings-disabled",
  "cache-allows-cookie-requests",
  "retry-duplicate-risk",
]);

const POLICY_DESTINATIONS: Record<TrafficPolicyKind, Readonly<{ label: string; route: string }>> = {
  "rate-limit": { label: "Rate-limit", route: "/policies/rate-limits" },
  waf: { label: "WAF", route: "/policies/waf" },
  cache: { label: "Cache", route: "/policies/cache" },
  retry: { label: "Retry", route: "/policies/retries" },
  "traffic-shaper": { label: "Traffic-shaper", route: "/policies/traffic-shaper" },
};

const DETAIL_LIMIT = 280;

/**
 * Derive the Overview "Needs attention" list from server-confirmed runtime and
 * configuration data. All titles and routes are static. Runtime error excerpts
 * are bounded plain text; consumers must render detail as text, never as HTML.
 */
export function deriveOverviewAttention(input: OverviewAttentionInput): OverviewAttentionItem[] {
  const items: OverviewAttentionItem[] = [];
  const proxy = input.status?.proxy ?? input.config?.proxy;
  const proxyError = firstNonEmpty(proxy?.lastError, input.status?.proxyLastError);

  if (proxy?.state === ProxyState.ERROR || proxyError) {
    items.push(item({
      key: "proxy-error",
      severity: "error",
      title: "Proxy runtime reported an error",
      detail: proxyError ? `Latest error: ${proxyError}` : "The proxy entered an error state.",
      actionLabel: "Open diagnostics",
      actionRoute: "/monitor/diagnostics",
    }));
  } else if (proxy?.state === ProxyState.STOPPED || (
    input.status !== null && input.status !== undefined &&
    !proxy && input.status.proxyRunning === false
  )) {
    items.push(item({
      key: "proxy-stopped",
      severity: "warning",
      title: "Proxy is stopped",
      detail: "Public listeners are not accepting traffic.",
      actionLabel: "Review listeners",
      actionRoute: "/proxy/listeners",
    }));
  }

  const listenerStatuses = runtimeListenerStatuses(input);
  const enabledListenerIds = new Map(
    (input.config?.listeners ?? []).map((listener) => [listener.id.toString(), listener.enabled]),
  );
  const relevantListeners = listenerStatuses.filter((listener) => {
    const configuredEnabled = enabledListenerIds.get(listener.listenerId.toString());
    return configuredEnabled ?? !listener.disabled;
  });
  const listenersWithErrors = relevantListeners.filter((listener) => (
    listener.state === ProxyState.ERROR || firstNonEmpty(listener.lastError) !== ""
  ));
  const listenerErrors = new Set(listenersWithErrors);
  const downListeners = relevantListeners.filter((listener) => (
    !listenerErrors.has(listener) &&
    !listener.running &&
    listener.state !== ProxyState.STARTING &&
    listener.state !== ProxyState.STOPPING
  ));

  if (listenersWithErrors.length > 0) {
    const firstError = listenersWithErrors.map((listener) => firstNonEmpty(listener.lastError)).find(Boolean);
    const count = listenersWithErrors.length;
    items.push(item({
      key: "listener-errors",
      severity: "error",
      title: count === 1 ? "A listener has a runtime error" : `${formatCount(count)} listeners have runtime errors`,
      detail: firstError
        ? `${count === 1 ? "Latest error" : "First reported error"}: ${firstError}`
        : "One or more enabled listeners entered an error state.",
      actionLabel: "Review listeners",
      actionRoute: "/proxy/listeners",
    }));
  }

  if (downListeners.length > 0) {
    const count = downListeners.length;
    items.push(item({
      key: "listeners-down",
      severity: "warning",
      title: count === 1 ? "An enabled listener is down" : `${formatCount(count)} enabled listeners are down`,
      detail: "The affected listeners are not currently accepting traffic.",
      actionLabel: "Review listeners",
      actionRoute: "/proxy/listeners",
    }));
  }

  const disconnectedAgents = (input.config?.agents ?? []).filter((agent) => agent.enabled && !agent.connected).length;
  if (disconnectedAgents > 0) {
    items.push(item({
      key: "agents-disconnected",
      severity: "warning",
      title: disconnectedAgents === 1
        ? "An enabled agent is disconnected"
        : `${formatCount(disconnectedAgents)} enabled agents are disconnected`,
      detail: "Routes that depend on unavailable agents may not be able to reach their targets.",
      actionLabel: "Review agents",
      actionRoute: "/agent/fleet",
    }));
  }

  const proxyFailures = nonNegative(input.trafficWindow?.proxyInternalError);
  if (proxyFailures > 0n) {
    items.push(item({
      key: "proxy-failures",
      severity: "error",
      title: "Proxy requests are failing internally",
      detail: `${formatCount(proxyFailures)} internal ${proxyFailures === 1n ? "failure" : "failures"} in the selected window.`,
      actionLabel: "Open diagnostics",
      actionRoute: "/monitor/diagnostics",
    }));
  }

  const nonSuccess = nonNegative(input.trafficWindow?.proxyClientError) + nonNegative(input.trafficWindow?.proxyServerError);
  if (nonSuccess > 0n) {
    const requests = nonNegative(input.trafficWindow?.proxyRequests);
    const requestContext = requests > 0n ? ` of ${formatCount(requests)} total requests` : "";
    items.push(item({
      key: "non-success-responses",
      severity: "warning",
      title: "Non-success responses detected",
      detail: `${formatCount(nonSuccess)} client or server error ${nonSuccess === 1n ? "response" : "responses"}${requestContext} in the selected window.`,
      actionLabel: "Inspect traffic",
      actionRoute: "/monitor/traffic",
    }));
  }

  const warnings = input.policyWarnings ?? (input.config ? trafficPolicyAttentionWarnings(input.config) : []);
  const warningsByPolicy = new Map<TrafficPolicyKind, number>();
  for (const warning of warnings) {
    if (!warning.policyKind || !ACTIONABLE_POLICY_WARNING_CODES.has(warning.code)) continue;
    warningsByPolicy.set(warning.policyKind, (warningsByPolicy.get(warning.policyKind) ?? 0) + 1);
  }
  for (const kind of Object.keys(POLICY_DESTINATIONS) as TrafficPolicyKind[]) {
    const count = warningsByPolicy.get(kind) ?? 0;
    if (count === 0) continue;
    const destination = POLICY_DESTINATIONS[kind];
    items.push(item({
      key: `policy-${kind}`,
      severity: "warning",
      title: `${destination.label} configuration needs review`,
      detail: `${formatCount(count)} actionable configuration ${count === 1 ? "warning was" : "warnings were"} detected.`,
      actionLabel: `Review ${destination.label.toLowerCase()}`,
      actionRoute: destination.route,
    }));
  }

  return items.sort((left, right) => severityRank(left.severity) - severityRank(right.severity));
}

function runtimeListenerStatuses(input: OverviewAttentionInput): readonly ListenerRuntimeInput[] {
  const configStatuses = input.config?.proxy?.listeners ?? [];
  if (configStatuses.length > 0) return configStatuses;
  return input.status?.proxy?.listeners ?? [];
}

function item(value: OverviewAttentionItem): OverviewAttentionItem {
  return { ...value, detail: plainTextExcerpt(value.detail) };
}

function firstNonEmpty(...values: readonly (string | null | undefined)[]): string {
  for (const value of values) {
    const excerpt = plainTextExcerpt(value ?? "");
    if (excerpt) return excerpt;
  }
  return "";
}

function plainTextExcerpt(value: string): string {
  const plain = value
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/g, "�")
    .replace(/\s+/g, " ")
    .trim();
  const codePoints = Array.from(plain);
  if (codePoints.length <= DETAIL_LIMIT) return plain;
  return `${codePoints.slice(0, DETAIL_LIMIT - 1).join("")}…`;
}

function nonNegative(value: bigint | undefined): bigint {
  return value && value > 0n ? value : 0n;
}

function formatCount(value: bigint | number): string {
  return value.toLocaleString("en-US");
}

function severityRank(severity: OverviewAttentionSeverity): number {
  switch (severity) {
    case "error": return 0;
    case "warning": return 1;
    case "info": return 2;
  }
}
