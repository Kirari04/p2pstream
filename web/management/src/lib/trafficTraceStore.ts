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
import type { TraceRenderStats, TraceRequest, TraceRequestView } from "@/types/trafficTrace";
import { emptyTraceRenderStats } from "@/types/trafficTrace";

export const MAX_RETAINED_REQUESTS = 200;
export const MAX_RENDERED_TABLE_ROWS = 80;
export const MAX_DIAGRAM_REQUESTS = 40;
export const MAX_EVENTS_PER_REQUEST = 24;
export const FLUSH_INTERVAL_MS = 100;
export const MAX_PENDING_EVENTS_BEFORE_AGGRESSIVE_SAMPLE = 800;
export const MAX_PENDING_REQUESTS_PER_FLUSH = 120;

export type TrafficTraceStoreSnapshot = {
  tableRows: TraceRequestView[];
  diagramRequests: TraceRequest[];
  stats: TraceRenderStats;
  lastSequence: bigint;
};

type StoredTraceRequest = TraceRequest & {
  view: TraceRequestView;
  lastEventSequence: bigint;
};

type TrafficTraceStoreOptions = {
  maxRetainedRequests?: number;
  maxRenderedTableRows?: number;
  maxDiagramRequests?: number;
  maxEventsPerRequest?: number;
  flushIntervalMs?: number;
  maxPendingEventsBeforeAggressiveSample?: number;
  maxPendingRequestsPerFlush?: number;
  schedule?: (flush: () => void, delayMs: number) => unknown;
  cancelSchedule?: (handle: unknown) => void;
};

type StoreConfig = Required<TrafficTraceStoreOptions>;

const defaultSchedule = (flush: () => void, delayMs: number): unknown => {
  if (typeof window !== "undefined") {
    return window.setTimeout(flush, delayMs);
  }
  return setTimeout(flush, delayMs);
};

const defaultCancelSchedule = (handle: unknown) => {
  if (typeof window !== "undefined") {
    window.clearTimeout(handle as number);
    return;
  }
  clearTimeout(handle as ReturnType<typeof setTimeout>);
};

export class TrafficTraceStore {
  private readonly config: StoreConfig;
  private readonly requestsById = new Map<string, StoredTraceRequest>();
  private readonly requestOrder: string[] = [];
  private readonly pendingByRequestId = new Map<string, TrafficTraceEvent>();
  private flushHandle: unknown = null;
  private snapshotListener: ((snapshot: TrafficTraceStoreSnapshot) => void) | null;
  private sampledEvents = 0;
  private sampledRequests = 0;
  private sequence = 0n;

  constructor(
    snapshotListener: ((snapshot: TrafficTraceStoreSnapshot) => void) | null = null,
    options: TrafficTraceStoreOptions = {},
  ) {
    this.snapshotListener = snapshotListener;
    this.config = {
      maxRetainedRequests: options.maxRetainedRequests ?? MAX_RETAINED_REQUESTS,
      maxRenderedTableRows: options.maxRenderedTableRows ?? MAX_RENDERED_TABLE_ROWS,
      maxDiagramRequests: options.maxDiagramRequests ?? MAX_DIAGRAM_REQUESTS,
      maxEventsPerRequest: options.maxEventsPerRequest ?? MAX_EVENTS_PER_REQUEST,
      flushIntervalMs: options.flushIntervalMs ?? FLUSH_INTERVAL_MS,
      maxPendingEventsBeforeAggressiveSample: options.maxPendingEventsBeforeAggressiveSample ?? MAX_PENDING_EVENTS_BEFORE_AGGRESSIVE_SAMPLE,
      maxPendingRequestsPerFlush: options.maxPendingRequestsPerFlush ?? MAX_PENDING_REQUESTS_PER_FLUSH,
      schedule: options.schedule ?? defaultSchedule,
      cancelSchedule: options.cancelSchedule ?? defaultCancelSchedule,
    };
  }

  get lastSequence(): bigint {
    return this.sequence;
  }

  setSnapshotListener(listener: ((snapshot: TrafficTraceStoreSnapshot) => void) | null) {
    this.snapshotListener = listener;
  }

  enqueue(event: TrafficTraceEvent) {
    if (event.sequence > this.sequence) {
      this.sequence = event.sequence;
    }
    const requestId = traceRequestId(event);
    const pending = this.pendingByRequestId.get(requestId);
    if (pending) {
      this.sampledEvents += 1;
      if (isTerminalStage(pending.stage) && !isTerminalStage(event.stage)) {
        this.scheduleFlush();
        return;
      }
    }
    this.pendingByRequestId.set(requestId, event);
    this.scheduleFlush();
  }

  flush(): TrafficTraceStoreSnapshot {
    this.clearScheduledFlush();
    const pending = [...this.pendingByRequestId.values()];
    this.pendingByRequestId.clear();
    if (pending.length) {
      this.mergePendingEvents(pending);
    }
    const snapshot = this.snapshot();
    this.snapshotListener?.(snapshot);
    return snapshot;
  }

  snapshot(): TrafficTraceStoreSnapshot {
    const tableRows = this.requestOrder
      .slice(0, this.config.maxRenderedTableRows)
      .map((requestId) => this.requestsById.get(requestId)?.view)
      .filter((request): request is TraceRequestView => request !== undefined);
    const diagramRequests = this.requestOrder
      .slice(0, this.config.maxDiagramRequests)
      .map((requestId) => this.requestsById.get(requestId))
      .filter((request): request is StoredTraceRequest => request !== undefined);
    return {
      tableRows,
      diagramRequests,
      stats: {
        retainedRequests: this.requestOrder.length,
        renderedTableRows: tableRows.length,
        diagramRequests: diagramRequests.length,
        sampledEvents: this.sampledEvents,
        sampledRequests: this.sampledRequests,
        pendingEvents: this.pendingByRequestId.size,
      },
      lastSequence: this.sequence,
    };
  }

  clear(): TrafficTraceStoreSnapshot {
    this.clearScheduledFlush();
    this.requestsById.clear();
    this.requestOrder.splice(0);
    this.pendingByRequestId.clear();
    this.sampledEvents = 0;
    this.sampledRequests = 0;
    this.sequence = 0n;
    const snapshot = this.snapshot();
    this.snapshotListener?.(snapshot);
    return snapshot;
  }

  get(requestId: string): TraceRequest | null {
    return this.requestsById.get(requestId) ?? null;
  }

  private scheduleFlush() {
    if (this.flushHandle !== null) return;
    this.flushHandle = this.config.schedule(() => {
      this.flushHandle = null;
      this.flush();
    }, this.config.flushIntervalMs);
  }

  private clearScheduledFlush() {
    if (this.flushHandle === null) return;
    this.config.cancelSchedule(this.flushHandle);
    this.flushHandle = null;
  }

  private mergePendingEvents(pending: TrafficTraceEvent[]) {
    const aggressive = pending.length > this.config.maxPendingEventsBeforeAggressiveSample;
    const ordered = pending.sort(comparePendingEvents);
    let processed = 0;
    for (const event of ordered) {
      const requestId = traceRequestId(event);
      const existing = this.requestsById.get(requestId);
      if (processed >= this.config.maxPendingRequestsPerFlush) {
        this.sampleSkippedEvent(existing);
        continue;
      }
      if (aggressive && !existing && !isTerminalStage(event.stage)) {
        this.sampledRequests += 1;
        continue;
      }
      this.mergeEvent(event, requestId, existing);
      processed += 1;
    }
    this.sortRequestOrder();
    this.enforceRetentionLimit();
  }

  private mergeEvent(event: TrafficTraceEvent, requestId: string, existing: StoredTraceRequest | undefined) {
    const now = Date.now();
    const request = existing ?? newStoredTraceRequest(requestId, now);
    request.events.push(event);
    if (request.events.length > this.config.maxEventsPerRequest) {
      const removed = request.events.length - this.config.maxEventsPerRequest;
      request.events.splice(0, removed);
      request.sampledEventCount += removed;
      this.sampledEvents += removed;
    }
    request.latestEvent = event;
    request.visible = true;
    request.stage = event.stage;
    request.method = event.method || request.method;
    request.host = event.host || request.host;
    request.path = event.path || request.path;
    request.query = event.query || request.query;
    request.statusCode = event.statusCode || request.statusCode;
    request.durationMs = event.durationMs || request.durationMs;
    request.errorKind = event.errorKind || request.errorKind;
    request.listenerId = event.listenerId || request.listenerId;
    request.listenerName = event.listenerName || request.listenerName;
    request.routeId = event.routeId || request.routeId;
    request.routeLabel = event.routeLabel || request.routeLabel;
    request.defaultRoute = event.defaultRoute || request.defaultRoute;
    request.backendId = event.backendId || request.backendId;
    request.backendName = event.backendName || request.backendName;
    request.backendType = event.backendType || request.backendType;
    request.forwardMode = event.forwardMode || request.forwardMode;
    request.targetOrigin = event.targetOrigin || request.targetOrigin;
    request.agentId = event.agentId || request.agentId;
    request.agentName = event.agentName || request.agentName;
    request.agentPublicId = event.agentPublicId || request.agentPublicId;
    request.requestBytes = event.requestBytes || request.requestBytes;
    request.responseBytes = event.responseBytes || request.responseBytes;
    request.rateLimitRuleId = event.rateLimitRuleId || request.rateLimitRuleId;
    request.rateLimitRuleName = event.rateLimitRuleName || request.rateLimitRuleName;
    request.rateLimitAlgorithm = event.rateLimitAlgorithm || request.rateLimitAlgorithm;
    request.trafficShaperRuleId = event.trafficShaperRuleId || request.trafficShaperRuleId;
    request.trafficShaperRuleName = event.trafficShaperRuleName || request.trafficShaperRuleName;
    request.trafficShaperBudgetScope = event.trafficShaperBudgetScope || request.trafficShaperBudgetScope;
    request.trafficShaperUploadBytesPerSecond = event.trafficShaperUploadBytesPerSecond || request.trafficShaperUploadBytesPerSecond;
    request.trafficShaperDownloadBytesPerSecond = event.trafficShaperDownloadBytesPerSecond || request.trafficShaperDownloadBytesPerSecond;
    request.trafficShaperRequestExemptBytes = event.trafficShaperRequestExemptBytes || request.trafficShaperRequestExemptBytes;
    request.trafficShaperResponseExemptBytes = event.trafficShaperResponseExemptBytes || request.trafficShaperResponseExemptBytes;
    request.wafRuleId = event.wafRuleId || request.wafRuleId;
    request.wafRuleName = event.wafRuleName || request.wafRuleName;
    request.wafAction = event.wafAction || request.wafAction;
    request.wafActivationMode = event.wafActivationMode || request.wafActivationMode;
    request.wafAutomaticActive = event.wafAutomaticActive || request.wafAutomaticActive;
    request.wafChallengeKind = event.wafChallengeKind || request.wafChallengeKind;
    request.lastSeenAt = now;
    request.lastEventSequence = event.sequence;
    request.version += 1;
    if (isTerminalStage(event.stage)) {
      request.completedAt = now;
    }
    request.view = createTraceRequestView(request);

    if (!existing) {
      this.requestsById.set(requestId, request);
    }
    this.moveToFront(requestId);
  }

  private sampleSkippedEvent(existing: StoredTraceRequest | undefined) {
    this.sampledEvents += 1;
    if (existing) {
      existing.sampledEventCount += 1;
      existing.version += 1;
      existing.view = createTraceRequestView(existing);
      return;
    }
    this.sampledRequests += 1;
  }

  private moveToFront(requestId: string) {
    const currentIndex = this.requestOrder.indexOf(requestId);
    if (currentIndex === 0) return;
    if (currentIndex > 0) {
      this.requestOrder.splice(currentIndex, 1);
    }
    this.requestOrder.unshift(requestId);
  }

  private enforceRetentionLimit() {
    while (this.requestOrder.length > this.config.maxRetainedRequests) {
      const removed = this.requestOrder.pop();
      if (removed) {
        this.requestsById.delete(removed);
      }
    }
  }

  private sortRequestOrder() {
    this.requestOrder.sort((left, right) => {
      const leftSequence = this.requestsById.get(left)?.lastEventSequence ?? 0n;
      const rightSequence = this.requestsById.get(right)?.lastEventSequence ?? 0n;
      if (leftSequence === rightSequence) return 0;
      return leftSequence > rightSequence ? -1 : 1;
    });
  }
}

export function traceRequestId(event: TrafficTraceEvent): string {
  return event.requestId || `trace-${event.sequence.toString()}`;
}

export function traceStreamRequestForSequence(lastSequence: bigint): {
  replayRecent: boolean;
  afterSequence: bigint;
} {
  return {
    replayRecent: lastSequence > 0n,
    afterSequence: lastSequence,
  };
}

export function newTraceRequest(requestId: string, now = Date.now()): TraceRequest {
  return {
    requestId,
    method: "",
    host: "",
    path: "",
    query: "",
    stage: TrafficTraceStage.UNSPECIFIED,
    statusCode: 0n,
    durationMs: 0n,
    errorKind: "",
    listenerId: 0n,
    listenerName: "",
    routeId: 0n,
    routeLabel: "",
    defaultRoute: false,
    backendId: 0n,
    backendName: "",
    backendType: PublicBackendType.UNSPECIFIED,
    forwardMode: PublicBackendForwardMode.UNSPECIFIED,
    targetOrigin: "",
    agentId: 0n,
    agentName: "",
    agentPublicId: "",
    requestBytes: 0n,
    responseBytes: 0n,
    rateLimitRuleId: 0n,
    rateLimitRuleName: "",
    rateLimitAlgorithm: PublicRateLimitAlgorithm.UNSPECIFIED,
    trafficShaperRuleId: 0n,
    trafficShaperRuleName: "",
    trafficShaperBudgetScope: PublicTrafficShaperBudgetScope.UNSPECIFIED,
    trafficShaperUploadBytesPerSecond: 0n,
    trafficShaperDownloadBytesPerSecond: 0n,
    trafficShaperRequestExemptBytes: 0n,
    trafficShaperResponseExemptBytes: 0n,
    wafRuleId: 0n,
    wafRuleName: "",
    wafAction: PublicWafRuleAction.UNSPECIFIED,
    wafActivationMode: PublicWafActivationMode.UNSPECIFIED,
    wafAutomaticActive: false,
    wafChallengeKind: "",
    visible: true,
    completedAt: null,
    latestEvent: null,
    events: [],
    version: 0,
    sampledEventCount: 0,
    firstSeenAt: now,
    lastSeenAt: now,
  };
}

export function createTraceRequestView(request: TraceRequest): TraceRequestView {
  const stageLabel = traceStageLabel(request.stage);
  return {
    requestId: request.requestId,
    version: request.version,
    methodLabel: request.method || "-",
    pathLabel: request.path || "/",
    requestIdLabel: request.requestId,
    flowLabel: traceFlowLabel(request),
    stageLabel,
    statusLabel: request.statusCode ? request.statusCode.toString() : stageLabel,
    statusClass: requestStatusClass(request),
    durationLabel: formatDuration(request.durationMs),
    sampledEventCount: request.sampledEventCount,
  };
}

export function traceStageLabel(stage: TrafficTraceStage): string {
  switch (stage) {
    case TrafficTraceStage.RECEIVED: return "Received";
    case TrafficTraceStage.ROUTE_RESOLVED: return "Route";
    case TrafficTraceStage.BACKEND_SELECTED: return "Backend";
    case TrafficTraceStage.AGENT_SELECTED: return "Agent";
    case TrafficTraceStage.WAF_EVALUATED: return "WAF";
    case TrafficTraceStage.WAF_BLOCKED: return "WAF blocked";
    case TrafficTraceStage.WAF_CAPTCHA_CHALLENGED: return "Captcha";
    case TrafficTraceStage.WAF_CAPTCHA_VERIFIED: return "Captcha passed";
    case TrafficTraceStage.WAF_WAITING_ROOM: return "Waiting room";
    case TrafficTraceStage.TRAFFIC_SHAPER_SELECTED: return "Shaper";
    case TrafficTraceStage.UPSTREAM_STARTED: return "Upstream";
    case TrafficTraceStage.UPSTREAM_RESPONDED: return "Responded";
    case TrafficTraceStage.RESPONSE_SENT: return "Done";
    case TrafficTraceStage.FAILED: return "Failed";
    case TrafficTraceStage.RATE_LIMITED: return "Rate limited";
    default: return "Waiting";
  }
}

export function requestStatusClass(request: Pick<TraceRequest, "stage" | "statusCode">): string {
  if (request.stage === TrafficTraceStage.FAILED) return "text-red-400";
  if (request.stage === TrafficTraceStage.WAF_BLOCKED) return "text-red-400";
  if (request.stage === TrafficTraceStage.WAF_CAPTCHA_CHALLENGED || request.stage === TrafficTraceStage.WAF_WAITING_ROOM) return "text-amber-400";
  if (request.stage === TrafficTraceStage.RATE_LIMITED) return "text-amber-400";
  const status = Number(request.statusCode);
  if (status >= 500) return "text-red-400";
  if (status >= 400) return "text-amber-400";
  if (status >= 200) return "text-green-400";
  return "text-[#888]";
}

export function traceFlowLabel(request: TraceRequest): string {
  const parts = [request.listenerName || "Listener"];
  if (request.wafRuleName || request.wafRuleId > 0n) {
    parts.push(request.wafRuleName ? `WAF: ${request.wafRuleName}` : "WAF");
  }
  if (request.rateLimitRuleName || request.stage === TrafficTraceStage.RATE_LIMITED) {
    parts.push(request.rateLimitRuleName ? `Rate limit: ${request.rateLimitRuleName}` : "Rate limit");
  }
  if (request.trafficShaperRuleName || request.stage === TrafficTraceStage.TRAFFIC_SHAPER_SELECTED) {
    parts.push(request.trafficShaperRuleName ? `Shaper: ${request.trafficShaperRuleName}` : "Traffic shaper");
  }
  if (request.routeLabel || request.defaultRoute) {
    parts.push(request.routeLabel || "Default route");
  }
  if (request.backendName) {
    parts.push(request.backendName);
  }
  if (request.agentName || request.agentPublicId) {
    parts.push(request.agentName || request.agentPublicId);
  }
  return parts.join(" -> ");
}

export function formatDuration(value: bigint | undefined): string {
  if (value === undefined || value === 0n) return "-";
  const millis = Number(value);
  if (millis < 1000) return `${millis} ms`;
  return `${(millis / 1000).toFixed(1)} s`;
}

export function isTerminalStage(stage: TrafficTraceStage): boolean {
  return stage === TrafficTraceStage.RESPONSE_SENT ||
    stage === TrafficTraceStage.FAILED ||
    stage === TrafficTraceStage.RATE_LIMITED;
}

export function isEmptyStats(stats: TraceRenderStats): boolean {
  const empty = emptyTraceRenderStats();
  return stats.retainedRequests === empty.retainedRequests &&
    stats.renderedTableRows === empty.renderedTableRows &&
    stats.diagramRequests === empty.diagramRequests &&
    stats.sampledEvents === empty.sampledEvents &&
    stats.sampledRequests === empty.sampledRequests &&
    stats.pendingEvents === empty.pendingEvents;
}

function newStoredTraceRequest(requestId: string, now: number): StoredTraceRequest {
  const request = newTraceRequest(requestId, now) as StoredTraceRequest;
  request.lastEventSequence = 0n;
  request.view = createTraceRequestView(request);
  return request;
}

function comparePendingEvents(a: TrafficTraceEvent, b: TrafficTraceEvent): number {
  const aTerminal = isTerminalStage(a.stage);
  const bTerminal = isTerminalStage(b.stage);
  if (aTerminal !== bTerminal) return aTerminal ? -1 : 1;
  if (a.sequence === b.sequence) return 0;
  return a.sequence > b.sequence ? -1 : 1;
}
