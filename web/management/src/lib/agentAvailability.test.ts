import { describe, expect, test } from "bun:test";
import type { GetAgentAvailabilityResponse } from "@/gen/proto/p2pstream/v1/management_pb";
import {
  availabilityObservedMillis,
  availabilitySegmentFocusRange,
  availabilitySegments,
  availabilityTicks,
  clipAvailabilitySegments,
  recentAvailabilityFocusRange,
  sessionForAvailabilitySegment,
} from "./agentAvailability";
import type { AgentConnectionSession } from "@/gen/proto/p2pstream/v1/management_pb";

describe("availabilitySegments", () => {
  test("builds an exact online/offline state timeline", () => {
    const response = availabilityResponse({
      observedSinceUnixMillis: 0n,
      observedUntilUnixMillis: 100n,
      intervals: [
        availabilityInterval(10n, 30n),
        availabilityInterval(50n, 80n, true),
      ],
    });

    expect(availabilitySegments(response)).toEqual([
      { startMillis: 0, endMillis: 10, state: "offline", active: false },
      { startMillis: 10, endMillis: 30, state: "online", active: false },
      { startMillis: 30, endMillis: 50, state: "offline", active: false },
      { startMillis: 50, endMillis: 80, state: "online", active: true },
      { startMillis: 80, endMillis: 100, state: "offline", active: false },
    ]);
  });

  test("clips and merges overlapping intervals defensively", () => {
    const response = availabilityResponse({
      observedSinceUnixMillis: 10n,
      observedUntilUnixMillis: 90n,
      intervals: [
        availabilityInterval(40n, 70n),
        availabilityInterval(0n, 30n),
        availabilityInterval(25n, 50n),
        availabilityInterval(95n, 100n),
      ],
    });

    expect(availabilitySegments(response)).toEqual([
      { startMillis: 10, endMillis: 70, state: "online", active: false },
      { startMillis: 70, endMillis: 90, state: "offline", active: false },
    ]);
  });
});

test("availabilityTicks includes both observed boundaries", () => {
  const response = availabilityResponse({ observedSinceUnixMillis: 100n, observedUntilUnixMillis: 500n });
  expect(availabilityTicks(response)).toEqual([100, 200, 300, 400, 500]);
  expect(availabilityObservedMillis(response)).toBe(400);
});

test("recentAvailabilityFocusRange keeps recent dense activity in context", () => {
  const segments = Array.from({ length: 24 }, (_, index) => ({
    startMillis: index * 100_000,
    endMillis: (index + 1) * 100_000,
    state: index % 2 === 0 ? "offline" as const : "online" as const,
    active: false,
  }));
  expect(recentAvailabilityFocusRange(segments, 6)).toEqual({
    startMillis: 1_740_000,
    endMillis: 2_400_000,
  });
  expect(recentAvailabilityFocusRange(segments, 24)).toBeNull();
});

test("availabilitySegmentFocusRange expands a narrow incident without leaving observation bounds", () => {
  expect(availabilitySegmentFocusRange(
    { startMillis: 3_500_000, endMillis: 3_560_000, state: "offline", active: false },
    { startMillis: 0, endMillis: 3_600_000 },
  )).toEqual({ startMillis: 2_700_000, endMillis: 3_600_000 });
  expect(availabilitySegmentFocusRange(
    { startMillis: 0, endMillis: 3_000_000, state: "offline", active: false },
    { startMillis: 0, endMillis: 3_600_000 },
  )).toBeNull();
});

test("clipAvailabilitySegments preserves state while clipping to the focused range", () => {
  expect(clipAvailabilitySegments([
    { startMillis: 0, endMillis: 50, state: "offline", active: false },
    { startMillis: 50, endMillis: 100, state: "online", active: true },
  ], { startMillis: 25, endMillis: 75 })).toEqual([
    { startMillis: 25, endMillis: 50, state: "offline", active: false },
    { startMillis: 50, endMillis: 75, state: "online", active: true },
  ]);
});

test("sessionForAvailabilitySegment links outages to reconnects and online spans to their session", () => {
  const previous = agentConnectionSession(1n, 100n, 200n);
  const reconnect = agentConnectionSession(2n, 300n, 500n);
  expect(sessionForAvailabilitySegment(
    { startMillis: 200, endMillis: 300, state: "offline", active: false },
    [previous, reconnect],
  )).toEqual({ session: reconnect, relation: "reconnect" });
  expect(sessionForAvailabilitySegment(
    { startMillis: 300, endMillis: 500, state: "online", active: false },
    [previous, reconnect],
  )).toEqual({ session: reconnect, relation: "session" });
});

function availabilityResponse(overrides: Partial<GetAgentAvailabilityResponse> = {}): GetAgentAvailabilityResponse {
  return {
    $typeName: "p2pstream.v1.GetAgentAvailabilityResponse",
    agentPublicId: "agent-test",
    agentName: "Test agent",
    windowLabel: "24h",
    observedSinceUnixMillis: 0n,
    observedUntilUnixMillis: 0n,
    uptimeMillis: 0n,
    downtimeMillis: 0n,
    uptimePercent: 0,
    disconnectCount: 0n,
    longestDowntimeMillis: 0n,
    connected: false,
    intervals: [],
    ...overrides,
  };
}

function availabilityInterval(connectedAt: bigint, disconnectedAt: bigint, active = false) {
  return {
    $typeName: "p2pstream.v1.AgentAvailabilityInterval" as const,
    connectedAtUnixMillis: connectedAt,
    disconnectedAtUnixMillis: disconnectedAt,
    active,
  };
}

function agentConnectionSession(id: bigint, connectedAt: bigint, disconnectedAt: bigint): AgentConnectionSession {
  return {
    $typeName: "p2pstream.v1.AgentConnectionSession",
    id,
    agentId: 1n,
    agentPublicId: "agent-test",
    agentName: "Test agent",
    connectedAtUnixMillis: connectedAt,
    disconnectedAtUnixMillis: disconnectedAt,
    durationMillis: disconnectedAt - connectedAt,
    active: false,
  };
}
