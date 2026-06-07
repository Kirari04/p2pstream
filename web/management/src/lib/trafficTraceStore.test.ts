import { describe, expect, test } from "bun:test";
import type { TrafficTraceEvent } from "@/gen/proto/p2pstream/v1/management_pb";
import { TrafficTraceStage } from "@/gen/proto/p2pstream/v1/management_pb";
import {
  MAX_DIAGRAM_REQUESTS,
  MAX_EVENTS_PER_REQUEST,
  MAX_RENDERED_TABLE_ROWS,
  MAX_RETAINED_REQUESTS,
  TrafficTraceStore,
  traceStreamRequestForSequence,
} from "@/lib/trafficTraceStore";

describe("TrafficTraceStore", () => {
  test("creates a request from the first event", () => {
    const store = newTestStore();
    store.enqueue(traceEvent({ requestId: "req-1", sequence: 1n, method: "GET", path: "/one" }));
    const snapshot = store.flush();

    expect(snapshot.tableRows).toHaveLength(1);
    expect(snapshot.tableRows[0]?.requestId).toBe("req-1");
    expect(snapshot.tableRows[0]?.methodLabel).toBe("GET");
    expect(store.get("req-1")?.path).toBe("/one");
  });

  test("merges multiple events for the same request into one retained request", () => {
    const store = newTestStore();
    store.enqueue(traceEvent({ requestId: "req-1", sequence: 1n, stage: TrafficTraceStage.RECEIVED }));
    store.flush();
    store.enqueue(traceEvent({ requestId: "req-1", sequence: 2n, stage: TrafficTraceStage.RESPONSE_SENT, statusCode: 204n, durationMs: 12n }));
    const snapshot = store.flush();

    expect(snapshot.stats.retainedRequests).toBe(1);
    expect(store.get("req-1")?.events).toHaveLength(2);
    expect(snapshot.tableRows[0]?.statusLabel).toBe("204");
    expect(snapshot.tableRows[0]?.durationLabel).toBe("12 ms");
  });

  test("preserves traffic shaper fields", () => {
    const store = newTestStore();
    store.enqueue(traceEvent({
      requestId: "req-shaped",
      sequence: 1n,
      stage: TrafficTraceStage.TRAFFIC_SHAPER_SELECTED,
      trafficShaperRuleId: 42n,
      trafficShaperRuleName: "downloads",
      trafficShaperBudgetScope: 1,
      trafficShaperDownloadBytesPerSecond: 128_000n,
      trafficShaperResponseExemptBytes: 64_000n,
    }));
    const snapshot = store.flush();
    const request = store.get("req-shaped");

    expect(request?.trafficShaperRuleId).toBe(42n);
    expect(request?.trafficShaperRuleName).toBe("downloads");
    expect(request?.trafficShaperDownloadBytesPerSecond).toBe(128_000n);
    expect(request?.trafficShaperResponseExemptBytes).toBe(64_000n);
    expect(snapshot.tableRows[0]?.flowLabel).toContain("Shaper: downloads");
  });

  test("keeps newest requests first", () => {
    const store = newTestStore();
    store.enqueue(traceEvent({ requestId: "old", sequence: 1n }));
    store.enqueue(traceEvent({ requestId: "new", sequence: 2n }));
    const snapshot = store.flush();

    expect(snapshot.tableRows.map((request) => request.requestId)).toEqual(["new", "old"]);
  });

  test("caps retained requests", () => {
    const store = newTestStore({ maxPendingEventsBeforeAggressiveSample: 1_000, maxPendingRequestsPerFlush: 1_000 });
    for (let index = 0; index < MAX_RETAINED_REQUESTS + 5; index += 1) {
      store.enqueue(traceEvent({ requestId: `req-${index}`, sequence: BigInt(index + 1) }));
    }
    const snapshot = store.flush();

    expect(snapshot.stats.retainedRequests).toBe(MAX_RETAINED_REQUESTS);
    expect(store.get("req-0")).toBeNull();
    expect(store.get(`req-${MAX_RETAINED_REQUESTS + 4}`)?.requestId).toBe(`req-${MAX_RETAINED_REQUESTS + 4}`);
  });

  test("caps table rows and diagram requests", () => {
    const store = newTestStore({ maxPendingEventsBeforeAggressiveSample: 1_000, maxPendingRequestsPerFlush: 1_000 });
    for (let index = 0; index < MAX_RETAINED_REQUESTS; index += 1) {
      store.enqueue(traceEvent({ requestId: `req-${index}`, sequence: BigInt(index + 1) }));
    }
    const snapshot = store.flush();

    expect(snapshot.tableRows).toHaveLength(MAX_RENDERED_TABLE_ROWS);
    expect(snapshot.diagramRequests).toHaveLength(MAX_DIAGRAM_REQUESTS);
  });

  test("collapses pending repeated updates and counts overwritten events as sampled", () => {
    const store = newTestStore();
    store.enqueue(traceEvent({ requestId: "req-1", sequence: 1n, stage: TrafficTraceStage.RECEIVED }));
    store.enqueue(traceEvent({ requestId: "req-1", sequence: 2n, stage: TrafficTraceStage.BACKEND_SELECTED }));
    const snapshot = store.flush();

    expect(snapshot.stats.sampledEvents).toBe(1);
    expect(store.get("req-1")?.stage).toBe(TrafficTraceStage.BACKEND_SELECTED);
    expect(store.get("req-1")?.events).toHaveLength(1);
  });

  test("prefers terminal events over later non-terminal pending updates", () => {
    const store = newTestStore();
    store.enqueue(traceEvent({ requestId: "req-1", sequence: 1n, stage: TrafficTraceStage.RESPONSE_SENT, statusCode: 200n }));
    store.enqueue(traceEvent({ requestId: "req-1", sequence: 2n, stage: TrafficTraceStage.UPSTREAM_STARTED }));
    const snapshot = store.flush();

    expect(snapshot.stats.sampledEvents).toBe(1);
    expect(store.get("req-1")?.stage).toBe(TrafficTraceStage.RESPONSE_SENT);
    expect(store.get("req-1")?.statusCode).toBe(200n);
  });

  test("aggressive backlog sampling skips new non-terminal requests first", () => {
    const store = newTestStore({
      maxPendingEventsBeforeAggressiveSample: 2,
      maxPendingRequestsPerFlush: 2,
    });
    store.enqueue(traceEvent({ requestId: "pending-1", sequence: 1n, stage: TrafficTraceStage.RECEIVED }));
    store.enqueue(traceEvent({ requestId: "pending-2", sequence: 2n, stage: TrafficTraceStage.RECEIVED }));
    store.enqueue(traceEvent({ requestId: "terminal", sequence: 3n, stage: TrafficTraceStage.RESPONSE_SENT, statusCode: 200n }));
    store.enqueue(traceEvent({ requestId: "pending-3", sequence: 4n, stage: TrafficTraceStage.RECEIVED }));
    const snapshot = store.flush();

    expect(store.get("terminal")?.stage).toBe(TrafficTraceStage.RESPONSE_SENT);
    expect(snapshot.stats.sampledRequests).toBeGreaterThan(0);
    expect(snapshot.stats.retainedRequests).toBeLessThan(4);
  });

  test("caps per-request event history", () => {
    const store = newTestStore();
    for (let index = 0; index < MAX_EVENTS_PER_REQUEST + 3; index += 1) {
      store.enqueue(traceEvent({ requestId: "req-1", sequence: BigInt(index + 1), stage: TrafficTraceStage.UPSTREAM_STARTED }));
      store.flush();
    }

    const request = store.get("req-1");
    expect(request?.events).toHaveLength(MAX_EVENTS_PER_REQUEST);
    expect(request?.sampledEventCount).toBe(3);
  });

  test("clear resets requests, pending events, stats, and sequence state", () => {
    const store = newTestStore();
    store.enqueue(traceEvent({ requestId: "req-1", sequence: 12n }));
    store.flush();
    store.enqueue(traceEvent({ requestId: "pending", sequence: 13n }));
    const snapshot = store.clear();

    expect(snapshot.stats.retainedRequests).toBe(0);
    expect(snapshot.stats.pendingEvents).toBe(0);
    expect(snapshot.lastSequence).toBe(0n);
    expect(store.get("req-1")).toBeNull();
  });

  test("selected request can be retrieved after updates and disappears after eviction", () => {
    const store = newTestStore({ maxRetainedRequests: 2, maxPendingEventsBeforeAggressiveSample: 10, maxPendingRequestsPerFlush: 10 });
    store.enqueue(traceEvent({ requestId: "selected", sequence: 1n }));
    store.flush();
    expect(store.get("selected")?.requestId).toBe("selected");

    store.enqueue(traceEvent({ requestId: "second", sequence: 2n }));
    store.enqueue(traceEvent({ requestId: "third", sequence: 3n }));
    store.flush();
    expect(store.get("selected")).toBeNull();

    store.clear();
    expect(store.get("third")).toBeNull();
  });

  test("stream request disables replay when no sequence has been seen", () => {
    expect(traceStreamRequestForSequence(0n)).toEqual({
      replayRecent: false,
      afterSequence: 0n,
    });
  });

  test("stream request enables replay only after the last seen sequence", () => {
    expect(traceStreamRequestForSequence(12n)).toEqual({
      replayRecent: true,
      afterSequence: 12n,
    });
  });

  test("stream request disables replay after clear resets sequence", () => {
    const store = newTestStore();
    store.enqueue(traceEvent({ requestId: "req-1", sequence: 7n }));
    store.flush();
    expect(traceStreamRequestForSequence(store.lastSequence)).toEqual({
      replayRecent: true,
      afterSequence: 7n,
    });

    store.clear();
    expect(traceStreamRequestForSequence(store.lastSequence)).toEqual({
      replayRecent: false,
      afterSequence: 0n,
    });
  });
});

function newTestStore(options: ConstructorParameters<typeof TrafficTraceStore>[1] = {}) {
  return new TrafficTraceStore(null, {
    schedule: () => "scheduled",
    cancelSchedule: () => {},
    ...options,
  });
}

function traceEvent(overrides: Partial<TrafficTraceEvent>): TrafficTraceEvent {
  return {
    $typeName: "p2pstream.v1.TrafficTraceEvent",
    sequence: 0n,
    requestId: "",
    stage: TrafficTraceStage.RECEIVED,
    occurredAtUnixMillis: 0n,
    method: "",
    host: "",
    path: "",
    query: "",
    listenerId: 0n,
    listenerName: "",
    routeId: 0n,
    routeLabel: "",
    defaultRoute: false,
    backendId: 0n,
    backendName: "",
    backendType: 0,
    forwardMode: 0,
    targetOrigin: "",
    agentId: 0n,
    agentPublicId: "",
    agentName: "",
    statusCode: 0n,
    durationMs: 0n,
    errorKind: "",
    requestHeaders: {},
    responseHeaders: {},
    requestBytes: 0n,
    responseBytes: 0n,
    debugAttributes: {},
    rateLimitRuleId: 0n,
    rateLimitRuleName: "",
    rateLimitAlgorithm: 0,
    trafficShaperRuleId: 0n,
    trafficShaperRuleName: "",
    trafficShaperBudgetScope: 0,
    trafficShaperUploadBytesPerSecond: 0n,
    trafficShaperDownloadBytesPerSecond: 0n,
    trafficShaperRequestExemptBytes: 0n,
    trafficShaperResponseExemptBytes: 0n,
    wafRuleId: 0n,
    wafRuleName: "",
    wafAction: 0,
    wafActivationMode: 0,
    wafAutomaticActive: false,
    wafChallengeKind: "",
    cacheRuleId: 0n,
    cacheRuleName: "",
    cacheStatus: "",
    cacheKeyDigest: "",
    routeTargetId: 0n,
    routeTargetName: "",
    routeTargetType: 0,
    routeTargetTransport: 0,
    ...overrides,
  };
}
