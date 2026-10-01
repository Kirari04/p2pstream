import type { AgentStreamCapacitySummary } from "@/gen/proto/p2pstream/v1/management_pb";

export type AdmissionCapacitySignal = {
  label: string;
  tone: "success" | "warning" | "error";
  constrained: boolean;
};

export function admissionCapacitySignal(capacity: AgentStreamCapacitySummary): AdmissionCapacitySignal {
  if (capacity.memoryPressure === "critical") {
    return { label: "Admission paused · critical pressure", tone: "error", constrained: true };
  }
  if (capacity.resourceSampleError !== "" || (capacity.adaptive && capacity.memoryPressure === "unknown")) {
    return { label: "Admission sensing degraded", tone: "warning", constrained: true };
  }

  // A zero adaptive allowance pauses admission even with no live streams.
  // Measured memory can remain healthy while application reservations fill it.
  const totalFull = capacity.adaptive
    ? capacity.totalInUse >= capacity.adaptiveAdmissionLimit
    : capacity.totalCapacity > 0n && capacity.totalInUse >= capacity.totalCapacity;
  const publicFull = capacity.publicCapacity > 0n && (capacity.adaptive
    ? capacity.publicInUse >= capacity.adaptivePublicAdmissionLimit
    : capacity.publicInUse >= capacity.publicCapacity);
  if (totalFull || publicFull || capacity.waiters > 0n) {
    return { label: "Admission constrained", tone: "warning", constrained: true };
  }
  if (capacity.memoryPressure === "soft") {
    return { label: "Resource pressure · soft", tone: "warning", constrained: true };
  }
  return { label: "Headroom available", tone: "success", constrained: false };
}
