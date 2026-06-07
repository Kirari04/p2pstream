import type { PublicWafTriggerConfig } from "@/gen/proto/p2pstream/v1/management_pb";

export type WafTriggerMetric =
  | "minimumRequestRate"
  | "trafficSpikeMultiplier"
  | "proxyActiveRequests"
  | "routeTargetActiveRequests"
  | "agentActiveRequests"
  | "serverCpuPercent"
  | "agentCpuPercent";

export type WafTriggerMetricState = {
  enabled: boolean;
  value: number;
};

export type WafTriggerForm = {
  requestWindowSeconds: number;
  metrics: Record<WafTriggerMetric, WafTriggerMetricState>;
  minimumActiveSeconds: number;
  quietSeconds: number;
};

export type WafTriggerPayload = {
  requestWindowMillis: bigint;
  minimumRequestRate: bigint;
  trafficSpikeMultiplier: number;
  proxyActiveRequests: bigint;
  routeTargetActiveRequests: bigint;
  agentActiveRequests: bigint;
  serverCpuPercent: number;
  agentCpuPercent: number;
  minimumActiveMillis: bigint;
  quietPeriodMillis: bigint;
};

const DEFAULT_REQUEST_WINDOW_SECONDS = 10;
const DEFAULT_MINIMUM_ACTIVE_SECONDS = 30;
const DEFAULT_QUIET_SECONDS = 60;

const METRIC_DEFAULTS: Record<WafTriggerMetric, number> = {
  minimumRequestRate: 50,
  trafficSpikeMultiplier: 4,
  proxyActiveRequests: 100,
  routeTargetActiveRequests: 100,
  agentActiveRequests: 50,
  serverCpuPercent: 85,
  agentCpuPercent: 85,
};

export function defaultWafTriggerForm(): WafTriggerForm {
  return {
    requestWindowSeconds: DEFAULT_REQUEST_WINDOW_SECONDS,
    metrics: {
      minimumRequestRate: enabledMetric(METRIC_DEFAULTS.minimumRequestRate),
      trafficSpikeMultiplier: enabledMetric(METRIC_DEFAULTS.trafficSpikeMultiplier),
      proxyActiveRequests: enabledMetric(METRIC_DEFAULTS.proxyActiveRequests),
      routeTargetActiveRequests: enabledMetric(METRIC_DEFAULTS.routeTargetActiveRequests),
      agentActiveRequests: enabledMetric(METRIC_DEFAULTS.agentActiveRequests),
      serverCpuPercent: enabledMetric(METRIC_DEFAULTS.serverCpuPercent),
      agentCpuPercent: enabledMetric(METRIC_DEFAULTS.agentCpuPercent),
    },
    minimumActiveSeconds: DEFAULT_MINIMUM_ACTIVE_SECONDS,
    quietSeconds: DEFAULT_QUIET_SECONDS,
  };
}

export function wafTriggerFormFromProto(triggers?: Partial<PublicWafTriggerConfig>): WafTriggerForm {
  if (!triggers) return defaultWafTriggerForm();
  return {
    requestWindowSeconds: millisToPositiveSeconds(triggers.requestWindowMillis, DEFAULT_REQUEST_WINDOW_SECONDS),
    metrics: {
      minimumRequestRate: metricFromValue(triggers.minimumRequestRate, METRIC_DEFAULTS.minimumRequestRate),
      trafficSpikeMultiplier: metricFromValue(triggers.trafficSpikeMultiplier, METRIC_DEFAULTS.trafficSpikeMultiplier),
      proxyActiveRequests: metricFromValue(triggers.proxyActiveRequests, METRIC_DEFAULTS.proxyActiveRequests),
      routeTargetActiveRequests: metricFromValue(triggers.routeTargetActiveRequests, METRIC_DEFAULTS.routeTargetActiveRequests),
      agentActiveRequests: metricFromValue(triggers.agentActiveRequests, METRIC_DEFAULTS.agentActiveRequests),
      serverCpuPercent: metricFromValue(triggers.serverCpuPercent, METRIC_DEFAULTS.serverCpuPercent),
      agentCpuPercent: metricFromValue(triggers.agentCpuPercent, METRIC_DEFAULTS.agentCpuPercent),
    },
    minimumActiveSeconds: millisToPositiveSeconds(triggers.minimumActiveMillis, DEFAULT_MINIMUM_ACTIVE_SECONDS),
    quietSeconds: millisToPositiveSeconds(triggers.quietPeriodMillis, DEFAULT_QUIET_SECONDS),
  };
}

export function wafTriggerPayloadFromForm(form: WafTriggerForm): WafTriggerPayload {
  return {
    requestWindowMillis: secondsToMillis(form.requestWindowSeconds),
    minimumRequestRate: metricBigInt(form.metrics.minimumRequestRate),
    trafficSpikeMultiplier: metricNumber(form.metrics.trafficSpikeMultiplier),
    proxyActiveRequests: metricBigInt(form.metrics.proxyActiveRequests),
    routeTargetActiveRequests: metricBigInt(form.metrics.routeTargetActiveRequests),
    agentActiveRequests: metricBigInt(form.metrics.agentActiveRequests),
    serverCpuPercent: metricNumber(form.metrics.serverCpuPercent),
    agentCpuPercent: metricNumber(form.metrics.agentCpuPercent),
    minimumActiveMillis: secondsToMillis(form.minimumActiveSeconds),
    quietPeriodMillis: secondsToMillis(form.quietSeconds),
  };
}

export function setWafTriggerMetricEnabled(form: WafTriggerForm, metric: WafTriggerMetric, enabled: boolean): WafTriggerForm {
  const next = cloneWafTriggerForm(form);
  const current = next.metrics[metric];
  current.enabled = enabled;
  current.value = enabled && current.value <= 0 ? METRIC_DEFAULTS[metric] : enabled ? current.value : 0;
  return next;
}

function cloneWafTriggerForm(form: WafTriggerForm): WafTriggerForm {
  return {
    requestWindowSeconds: form.requestWindowSeconds,
    metrics: {
      minimumRequestRate: { ...form.metrics.minimumRequestRate },
      trafficSpikeMultiplier: { ...form.metrics.trafficSpikeMultiplier },
      proxyActiveRequests: { ...form.metrics.proxyActiveRequests },
      routeTargetActiveRequests: { ...form.metrics.routeTargetActiveRequests },
      agentActiveRequests: { ...form.metrics.agentActiveRequests },
      serverCpuPercent: { ...form.metrics.serverCpuPercent },
      agentCpuPercent: { ...form.metrics.agentCpuPercent },
    },
    minimumActiveSeconds: form.minimumActiveSeconds,
    quietSeconds: form.quietSeconds,
  };
}

function enabledMetric(value: number): WafTriggerMetricState {
  return { enabled: true, value };
}

function metricFromValue(value: bigint | number | undefined, fallback: number): WafTriggerMetricState {
  if (value === undefined) return enabledMetric(fallback);
  const numeric = Number(value);
  if (!Number.isFinite(numeric) || numeric <= 0) return { enabled: false, value: 0 };
  return { enabled: true, value: numeric };
}

function metricNumber(metric: WafTriggerMetricState): number {
  if (!metric.enabled) return 0;
  return Math.max(0, Number(metric.value) || 0);
}

function metricBigInt(metric: WafTriggerMetricState): bigint {
  return BigInt(Math.max(0, Math.round(metricNumber(metric))));
}

function millisToPositiveSeconds(value: bigint | undefined, fallback: number): number {
  if (value === undefined || value <= 0n) return fallback;
  return Math.max(1, Math.round(Number(value) / 1000));
}

function secondsToMillis(value: number): bigint {
  return BigInt(Math.max(1, Math.round((Number(value) || 0) * 1000)));
}
