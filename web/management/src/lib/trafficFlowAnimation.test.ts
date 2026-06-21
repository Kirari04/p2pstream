import { describe, expect, test } from "bun:test";
import {
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  TrafficTraceStage,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { createTrafficFlowConfigIndex } from "@/lib/trafficFlowLayout";
import {
  initialFrameStressState,
  nextFrameStressState,
  renderedTokenCap,
  shouldEnqueueCacheStorePulse,
} from "@/lib/trafficFlowAnimation";
import { MAX_RENDERED_TOKENS_NORMAL, MAX_RENDERED_TOKENS_STRESSED } from "@/lib/trafficFlowModel";
import { newTraceRequest } from "@/lib/trafficTraceStore";
import type { TraceRequest } from "@/types/trafficTrace";

describe("trafficFlowAnimation", () => {
  test("uses a lower token cap while animation is stressed", () => {
    expect(renderedTokenCap(false)).toBe(MAX_RENDERED_TOKENS_NORMAL);
    expect(renderedTokenCap(true)).toBe(MAX_RENDERED_TOKENS_STRESSED);
  });

  test("enters stress on slow frames and recovers after sustained fast frames", () => {
    let state = nextFrameStressState(initialFrameStressState, 0);
    state = nextFrameStressState(state, 50);
    expect(state.stressed).toBe(true);

    for (let index = 0; index < 12; index += 1) {
      state = nextFrameStressState(state, 60 + index * 10);
    }

    expect(state.stressed).toBe(false);
  });

  test("cache-store pulse is emitted once for stored cache requests", () => {
    const seen = new Set<string>();
    const index = createTrafficFlowConfigIndex({
      $typeName: "p2pstream.v1.GetPublicProxyConfigResponse",
      listeners: [],
      agents: [],
      routes: [],
      routeTargets: [],
      rateLimitRules: [],
      trafficShaperRules: [],
      cacheRules: [],
      tlsDnsCredentials: [],
      wafCaptchaProviders: [],
      wafRules: [],
      responseTemplates: [],
      tlsCertificates: [],
      proxy: undefined,
    } as GetPublicProxyConfigResponse);
    const request = traceRequest({
      requestId: "cache-store",
      routeTargetId: 2n,
      routeTargetType: PublicRouteTargetType.PROXY,
      routeTargetTransport: PublicRouteTargetTransport.DIRECT,
      cacheRuleId: 4n,
      cacheStatus: "stored",
      stage: TrafficTraceStage.CACHE_STORED,
    });

    expect(shouldEnqueueCacheStorePulse(request, index, seen)).toBe(true);
    expect(shouldEnqueueCacheStorePulse(request, index, seen)).toBe(false);
    expect(seen.has("cache-store")).toBe(true);
  });
});

function traceRequest(overrides: Partial<TraceRequest>): TraceRequest {
  return {
    ...newTraceRequest(overrides.requestId ?? "req"),
    ...overrides,
  };
}
