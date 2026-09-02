import { describe, expect, test } from "bun:test";
import type { DashboardDiagnosticsSample } from "@/gen/proto/p2pstream/v1/management_pb";
import {
  DIAGNOSTICS_SECTIONS,
  filterDiagnosticSamples,
  normalizeDiagnosticsSection,
  type DiagnosticDimensionFilter,
} from "@/lib/diagnosticsWorkbench";

function sample(overrides: Partial<DashboardDiagnosticsSample>): DashboardDiagnosticsSample {
  return {
    errorKind: "",
    listenerLabel: "",
    routeLabel: "",
    routeTargetLabel: "",
    agentLabel: "",
    retryErrorKind: "",
    retryFailedAgentLabel: "",
    ...overrides,
  } as DashboardDiagnosticsSample;
}

describe("diagnostics workbench", () => {
  test("defines stable routed investigation sections", () => {
    expect(DIAGNOSTICS_SECTIONS.map((section) => section.key)).toEqual([
      "overview",
      "retries",
      "failures",
      "samples",
    ]);
    expect(DIAGNOSTICS_SECTIONS.map((section) => section.path)).toEqual([
      "/monitor/diagnostics/overview",
      "/monitor/diagnostics/retries",
      "/monitor/diagnostics/failures",
      "/monitor/diagnostics/samples",
    ]);
  });

  test("normalizes absent and untrusted section values to overview", () => {
    expect(normalizeDiagnosticsSection("samples")).toBe("samples");
    expect(normalizeDiagnosticsSection(["retries", "samples"])).toBe("retries");
    expect(normalizeDiagnosticsSection("../../settings/api-tokens")).toBe("overview");
    expect(normalizeDiagnosticsSection(undefined)).toBe("overview");
  });

  test("applies one visible exact-match filter to loaded samples", () => {
    const samples = [
      sample({ errorKind: "agent_server_capacity", agentLabel: "edge-01" }),
      sample({ errorKind: "agent_server_capacity_extra", agentLabel: "edge-02" }),
      sample({ errorKind: "agent_server_capacity", agentLabel: "edge-03" }),
    ];
    const filter: DiagnosticDimensionFilter = {
      key: "error",
      label: "agent_server_capacity",
      title: "Error kinds",
    };

    expect(filterDiagnosticSamples(samples, filter).map((row) => row.agentLabel)).toEqual(["edge-01", "edge-03"]);
    expect(filterDiagnosticSamples(samples, null)).toEqual(samples);
  });

  test("keeps retry attribution distinct from final agent attribution", () => {
    const samples = [
      sample({ agentLabel: "recovered-on", retryFailedAgentLabel: "first-failed" }),
      sample({ agentLabel: "first-failed", retryFailedAgentLabel: "different-first-failure" }),
    ];
    const filter: DiagnosticDimensionFilter = {
      key: "retry-agent",
      label: "first-failed",
      title: "First-failed agent",
    };

    expect(filterDiagnosticSamples(samples, filter).map((row) => row.agentLabel)).toEqual(["recovered-on"]);
  });
});
