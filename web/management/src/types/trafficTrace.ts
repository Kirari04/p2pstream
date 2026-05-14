import type { TrafficTraceEvent } from "@/gen/proto/p2pstream/v1/management_pb";
import {
  PublicBackendForwardMode,
  PublicBackendType,
  PublicRateLimitAlgorithm,
  PublicTrafficShaperBudgetScope,
  PublicWafActivationMode,
  PublicWafRuleAction,
  TrafficTraceStage,
} from "@/gen/proto/p2pstream/v1/management_pb";

export type TraceRequest = {
  requestId: string;
  method: string;
  host: string;
  path: string;
  query: string;
  stage: TrafficTraceStage;
  statusCode: bigint;
  durationMs: bigint;
  errorKind: string;
  listenerId: bigint;
  listenerName: string;
  routeId: bigint;
  routeLabel: string;
  defaultRoute: boolean;
  backendId: bigint;
  backendName: string;
  backendType: PublicBackendType;
  forwardMode: PublicBackendForwardMode;
  targetOrigin: string;
  agentId: bigint;
  agentName: string;
  agentPublicId: string;
  requestBytes: bigint;
  responseBytes: bigint;
  rateLimitRuleId: bigint;
  rateLimitRuleName: string;
  rateLimitAlgorithm: PublicRateLimitAlgorithm;
  trafficShaperRuleId: bigint;
  trafficShaperRuleName: string;
  trafficShaperBudgetScope: PublicTrafficShaperBudgetScope;
  trafficShaperUploadBytesPerSecond: bigint;
  trafficShaperDownloadBytesPerSecond: bigint;
  trafficShaperRequestExemptBytes: bigint;
  trafficShaperResponseExemptBytes: bigint;
  wafRuleId: bigint;
  wafRuleName: string;
  wafAction: PublicWafRuleAction;
  wafActivationMode: PublicWafActivationMode;
  wafAutomaticActive: boolean;
  wafChallengeKind: string;
  visible: boolean;
  completedAt: number | null;
  latestEvent: TrafficTraceEvent | null;
  events: TrafficTraceEvent[];
  version: number;
  sampledEventCount: number;
  firstSeenAt: number;
  lastSeenAt: number;
};

export type TraceRequestView = {
  requestId: string;
  version: number;
  methodLabel: string;
  pathLabel: string;
  requestIdLabel: string;
  flowLabel: string;
  stageLabel: string;
  statusLabel: string;
  statusClass: string;
  durationLabel: string;
  sampledEventCount: number;
};

export type TraceRenderStats = {
  retainedRequests: number;
  renderedTableRows: number;
  diagramRequests: number;
  sampledEvents: number;
  sampledRequests: number;
  pendingEvents: number;
};

export function emptyTraceRenderStats(): TraceRenderStats {
  return {
    retainedRequests: 0,
    renderedTableRows: 0,
    diagramRequests: 0,
    sampledEvents: 0,
    sampledRequests: 0,
    pendingEvents: 0,
  };
}
