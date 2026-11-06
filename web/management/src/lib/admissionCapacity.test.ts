import { create } from "@bufbuild/protobuf";
import { describe, expect, test } from "bun:test";
import { AgentStreamCapacitySummarySchema } from "@/gen/proto/p2pstream/v1/management_pb";
import { admissionCapacitySignal } from "./admissionCapacity";

describe("admissionCapacitySignal", () => {
  const healthy = {
    adaptive: true,
    memoryPressure: "healthy",
    totalCapacity: 65_536n,
    publicCapacity: 65_472n,
    totalInUse: 50n,
    publicInUse: 50n,
    adaptiveAdmissionLimit: 100n,
    adaptivePublicAdmissionLimit: 96n,
  };

  test("reports exhausted admission despite healthy sampled memory", () => {
    const observed = create(AgentStreamCapacitySummarySchema, {
      ...healthy,
      adaptiveAdmissionLimit: 48n,
      adaptivePublicAdmissionLimit: 44n,
    });
    expect(admissionCapacitySignal(observed)).toEqual({
      label: "Admission constrained", tone: "warning", constrained: true,
    });
  });

  test("detects zero allowance and exhausted public reserve independently", () => {
    for (const state of [
      { totalInUse: 0n, publicInUse: 0n, adaptiveAdmissionLimit: 0n, adaptivePublicAdmissionLimit: 0n },
      { adaptivePublicAdmissionLimit: 50n },
      { totalInUse: 0n, publicInUse: 0n, adaptivePublicAdmissionLimit: 0n },
    ]) {
      expect(admissionCapacitySignal(create(AgentStreamCapacitySummarySchema, { ...healthy, ...state })).constrained).toBe(true);
    }
  });

  test("shows available capacity only while the public lane and sensors permit it", () => {
    expect(admissionCapacitySignal(create(AgentStreamCapacitySummarySchema, healthy))).toEqual({
      label: "Headroom available", tone: "success", constrained: false,
    });
    for (const state of [
      { memoryPressure: "unknown" },
      { resourceSampleError: "sample failed" },
    ]) {
      expect(admissionCapacitySignal(create(AgentStreamCapacitySummarySchema, { ...healthy, ...state })).label).toBe("Admission sensing degraded");
    }
  });

  test("reports critical, soft, and waiting states without claiming healthy admission", () => {
    expect(admissionCapacitySignal(create(AgentStreamCapacitySummarySchema, { ...healthy, memoryPressure: "critical" })).tone).toBe("error");
    expect(admissionCapacitySignal(create(AgentStreamCapacitySummarySchema, { ...healthy, memoryPressure: "soft" })).label).toBe("Resource pressure · soft");
    expect(admissionCapacitySignal(create(AgentStreamCapacitySummarySchema, { ...healthy, waiters: 1n })).label).toBe("Admission constrained");
  });

  test("uses fixed operator ceilings and permits an intentionally disabled public lane", () => {
    expect(admissionCapacitySignal(create(AgentStreamCapacitySummarySchema, {
      ...healthy, adaptive: false, totalCapacity: 50n,
    })).constrained).toBe(true);
    expect(admissionCapacitySignal(create(AgentStreamCapacitySummarySchema, {
      ...healthy, publicCapacity: 0n, publicInUse: 0n, adaptivePublicAdmissionLimit: 0n,
    })).constrained).toBe(false);
  });
});
