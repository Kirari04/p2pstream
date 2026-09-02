import type { DashboardDiagnosticsSample } from "@/gen/proto/p2pstream/v1/management_pb";

export type DiagnosticsSectionKey = "overview" | "retries" | "failures" | "samples";
export type DiagnosticDimensionKey = "error" | "listener" | "route" | "target" | "agent" | "retry-error" | "retry-agent";
export type DiagnosticDimensionFilter = Readonly<{
  key: DiagnosticDimensionKey;
  label: string;
  title: string;
}>;

export const DIAGNOSTICS_SECTIONS = [
  {
    key: "overview",
    label: "Overview",
    path: "/monitor/diagnostics/overview",
    description: "Outcome health and response-code distribution for the selected window.",
  },
  {
    key: "retries",
    label: "Retry health",
    path: "/monitor/diagnostics/retries",
    description: "Retry activation, transport recovery, rules, and first-attempt failures.",
  },
  {
    key: "failures",
    label: "Failure map",
    path: "/monitor/diagnostics/failures",
    description: "Ranked error, listener, route, target, and agent dimensions.",
  },
  {
    key: "samples",
    label: "Request samples",
    path: "/monitor/diagnostics/samples",
    description: "Retained non-success, proxy-failure, and retry request evidence.",
  },
] as const satisfies ReadonlyArray<{
  key: DiagnosticsSectionKey;
  label: string;
  path: string;
  description: string;
}>;

export function normalizeDiagnosticsSection(value: unknown): DiagnosticsSectionKey {
  const section = Array.isArray(value) ? value[0] : value;
  if (section === "retries" || section === "failures" || section === "samples") return section;
  return "overview";
}

export function filterDiagnosticSamples<T extends DashboardDiagnosticsSample>(
  samples: readonly T[],
  filter: DiagnosticDimensionFilter | null,
): T[] {
  if (!filter) return [...samples];
  return samples.filter((sample) => diagnosticSampleDimensionValue(sample, filter.key) === filter.label);
}

export function diagnosticSampleDimensionValue(
  sample: DashboardDiagnosticsSample,
  key: DiagnosticDimensionKey,
): string {
  switch (key) {
    case "error": return sample.errorKind;
    case "listener": return sample.listenerLabel;
    case "route": return sample.routeLabel;
    case "target": return sample.routeTargetLabel;
    case "agent": return sample.agentLabel;
    case "retry-error": return sample.retryErrorKind;
    case "retry-agent": return sample.retryFailedAgentLabel;
  }
}
