import { isTerminalTraceRequest, requestUsesCacheNode, type TrafficFlowConfigIndex, type TrafficRequestPathCacheEntry } from "@/lib/trafficFlowLayout";
import { cacheToneForRequest } from "@/lib/trafficFlowGraph";
import {
  BASE_PLAYBACK_MS,
  COMPLETION_FADE_MS,
  COMPLETION_HOLD_MS,
  FRAME_RECOVERY_COUNT,
  FRAME_RECOVERY_MS,
  FRAME_STRESS_MS,
  HIGH_BURST_THRESHOLD,
  LOW_BURST_THRESHOLD,
  MAX_PLAYBACK_MS,
  MAX_RENDERED_TOKENS_NORMAL,
  MAX_RENDERED_TOKENS_STRESSED,
  MIN_PLAYBACK_MS,
  PER_HOP_MS,
  type VisualToken,
  type VisualTokenStatus,
} from "@/lib/trafficFlowModel";
import { pointAtMotionDistance, type MotionNodeBox, type MotionPlan, type MotionPoint } from "@/lib/trafficMotion";
import { TrafficTraceStage } from "@/gen/proto/p2pstream/v1/management_pb";
import type { TraceRequest } from "@/types/trafficTrace";

export type FrameStressState = {
  previousFrameAt: number | null;
  recoveryFrames: number;
  stressed: boolean;
};

export const initialFrameStressState: FrameStressState = {
  previousFrameAt: null,
  recoveryFrames: 0,
  stressed: false,
};

export function renderedTokenCap(stressed: boolean): number {
  return stressed ? MAX_RENDERED_TOKENS_STRESSED : MAX_RENDERED_TOKENS_NORMAL;
}

export function playbackDuration(hopCount: number, activeTokens: number): number {
  const raw = BASE_PLAYBACK_MS + Math.max(1, hopCount) * PER_HOP_MS;
  if (activeTokens < LOW_BURST_THRESHOLD) return clamp(raw, 2500, MAX_PLAYBACK_MS);
  if (activeTokens < HIGH_BURST_THRESHOLD) return clamp(raw, 2000, 4500);
  return clamp(raw, MIN_PLAYBACK_MS, 3000);
}

export function statusForRequest(request: TraceRequest): VisualTokenStatus {
  if (request.stage === TrafficTraceStage.FAILED) return "failed";
  const status = Number(request.statusCode);
  if (status >= 500) return "server-error";
  if (status >= 400) return "client-error";
  if (status >= 200) return "success";
  return "in-flight";
}

export function requestLabel(request: TraceRequest): string {
  return `${request.method || "REQUEST"} ${request.path || "/"}`;
}

export function createVisualToken(input: {
  request: TraceRequest;
  cached: TrafficRequestPathCacheEntry;
  motionPlan: MotionPlan;
  activeTokenCount: number;
  now: number;
}): VisualToken {
  return {
    requestId: input.request.requestId,
    request: input.request,
    path: input.cached.path,
    label: requestLabel(input.request),
    motionPlan: input.motionPlan,
    currentDistance: 0,
    targetDistance: input.motionPlan.targetLength,
    startedAt: input.now,
    updatedAt: input.now,
    durationMs: playbackDuration(input.cached.path.length - 1, input.activeTokenCount),
    finishedAt: null,
    status: statusForRequest(input.request),
    cacheTone: cacheToneForRequest(input.request),
    skipped: false,
  };
}

// Mutates input.token in place; this runs in the animation hot path.
export function updateVisualToken(input: {
  token: VisualToken;
  request: TraceRequest;
  cached: TrafficRequestPathCacheEntry;
  motionPlan: MotionPlan;
  activeTokenCount: number;
  now: number;
}) {
  advanceVisualToken(input.token, input.now);
  input.token.request = input.request;
  input.token.path = input.cached.path;
  input.token.label = requestLabel(input.request);
  input.token.motionPlan = input.motionPlan;
  input.token.currentDistance = clamp(input.token.currentDistance, 0, input.motionPlan.targetLength);
  input.token.targetDistance = input.motionPlan.targetLength;
  input.token.durationMs = Math.max(input.token.durationMs, playbackDuration(input.token.path.length - 1, input.activeTokenCount));
  input.token.status = statusForRequest(input.request);
  input.token.cacheTone = cacheToneForRequest(input.request);
}

// Mutates token in place; callers should not treat this helper as pure.
export function advanceVisualToken(token: VisualToken, now: number) {
  if (token.finishedAt !== null) {
    token.updatedAt = now;
    return;
  }

  const elapsed = Math.max(0, now - token.updatedAt);
  token.updatedAt = now;
  if (token.currentDistance < token.targetDistance) {
    const speed = Math.max(token.motionPlan.totalLength, token.targetDistance, 1) / token.durationMs;
    token.currentDistance = Math.min(token.targetDistance, token.currentDistance + speed * elapsed);
  }

  if (isTerminalTraceRequest(token.request) && token.currentDistance >= token.targetDistance && token.finishedAt === null) {
    token.finishedAt = now;
  }
}

export function activeVisualTokens(tokens: readonly VisualToken[], now: number): VisualToken[] {
  return tokens.filter((token) => {
    if (token.finishedAt === null) return true;
    return now - token.finishedAt <= COMPLETION_HOLD_MS + COMPLETION_FADE_MS;
  });
}

export function visualTokenPoint(token: VisualToken, motionNodeBoxes: Map<string, MotionNodeBox>): MotionPoint | null {
  const point = pointAtMotionDistance(token.motionPlan, token.currentDistance);
  if (point) return point;
  const fallbackKey = token.path[Math.min(token.motionPlan.targetNodeIndex, token.path.length - 1)] ?? "ingress";
  return motionNodeBoxes.get(fallbackKey)?.center ?? null;
}

export function visualTokenOpacity(token: VisualToken, now: number): number {
  if (token.finishedAt === null) return 1;
  const age = now - token.finishedAt;
  if (age <= COMPLETION_HOLD_MS) return 1;
  return clamp(1 - (age - COMPLETION_HOLD_MS) / COMPLETION_FADE_MS, 0, 1);
}

export function tokenColorClass(token: VisualToken): string {
  if (token.status === "client-error" || token.status === "server-error" || token.status === "failed") {
    return `traffic-token-${token.status}`;
  }
  return token.cacheTone ? `traffic-token-cache-${token.cacheTone}` : `traffic-token-${token.status}`;
}

export function shouldEnqueueCacheStorePulse(
  request: TraceRequest,
  index: TrafficFlowConfigIndex,
  seenRequestIds: Set<string>,
): boolean {
  if (cacheToneForRequest(request) !== "stored") return false;
  if (!requestUsesCacheNode(request, index)) return false;
  if (seenRequestIds.has(request.requestId)) return false;
  seenRequestIds.add(request.requestId);
  return true;
}

export function nextFrameStressState(state: FrameStressState, now: number): FrameStressState {
  if (state.previousFrameAt === null) {
    return { ...state, previousFrameAt: now };
  }
  const delta = now - state.previousFrameAt;
  if (delta > FRAME_STRESS_MS) {
    return {
      previousFrameAt: now,
      recoveryFrames: 0,
      stressed: true,
    };
  }
  if (delta < FRAME_RECOVERY_MS) {
    const recoveryFrames = state.recoveryFrames + 1;
    return {
      previousFrameAt: now,
      recoveryFrames,
      stressed: recoveryFrames >= FRAME_RECOVERY_COUNT ? false : state.stressed,
    };
  }
  return {
    previousFrameAt: now,
    recoveryFrames: 0,
    stressed: state.stressed,
  };
}

export function resetFrameStressState(stressed = false): FrameStressState {
  return {
    previousFrameAt: null,
    recoveryFrames: 0,
    stressed,
  };
}

export function cacheStorePulseActive(startedAt: number, now: number, durationMs: number): boolean {
  return now - startedAt <= durationMs;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
