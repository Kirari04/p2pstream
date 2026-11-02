import type {
  AgentAvailabilitySummary,
  AgentConnectionSession,
  GetAgentAvailabilityResponse,
  ListAgentAvailabilityResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

export type AvailabilityWindow = "24h" | "7d" | "30d";
export type AvailabilityState = "online" | "offline";

export type AvailabilitySegment = {
  startMillis: number;
  endMillis: number;
  state: AvailabilityState;
  active: boolean;
};

export type AvailabilityRange = {
  startMillis: number;
  endMillis: number;
};

export type AvailabilitySessionMatch = {
  session: AgentConnectionSession;
  relation: "session" | "disconnect" | "reconnect";
};

/** The part of a single-agent or fleet availability response that describes one agent's timeline. */
export type AvailabilityTimeline = Pick<
  GetAgentAvailabilityResponse,
  "observedSinceUnixMillis" | "observedUntilUnixMillis" | "intervals"
>;

export function availabilitySegments(
  availability: AvailabilityTimeline | null | undefined,
): AvailabilitySegment[] {
  if (!availability) return [];
  const observedStart = safeEpochMillis(availability.observedSinceUnixMillis);
  const observedEnd = safeEpochMillis(availability.observedUntilUnixMillis);
  if (observedEnd <= observedStart) return [];

  const normalized = availability.intervals
    .map((interval) => ({
      startMillis: clamp(safeEpochMillis(interval.connectedAtUnixMillis), observedStart, observedEnd),
      endMillis: clamp(safeEpochMillis(interval.disconnectedAtUnixMillis), observedStart, observedEnd),
      active: interval.active,
    }))
    .filter((interval) => interval.endMillis > interval.startMillis)
    .sort((left, right) => left.startMillis - right.startMillis || left.endMillis - right.endMillis);

  const online: Array<{ startMillis: number; endMillis: number; active: boolean }> = [];
  for (const interval of normalized) {
    const previous = online.at(-1);
    if (!previous || interval.startMillis > previous.endMillis) {
      online.push({ ...interval });
      continue;
    }
    previous.endMillis = Math.max(previous.endMillis, interval.endMillis);
    previous.active ||= interval.active;
  }

  const segments: AvailabilitySegment[] = [];
  let cursor = observedStart;
  for (const interval of online) {
    if (interval.startMillis > cursor) {
      segments.push({ startMillis: cursor, endMillis: interval.startMillis, state: "offline", active: false });
    }
    segments.push({ ...interval, state: "online" });
    cursor = Math.max(cursor, interval.endMillis);
  }
  if (cursor < observedEnd) {
    segments.push({ startMillis: cursor, endMillis: observedEnd, state: "offline", active: false });
  }
  return segments;
}

export function availabilityTicks(
  availability: GetAgentAvailabilityResponse | null | undefined,
  count = 5,
): number[] {
  if (!availability || count < 2) return [];
  const start = safeEpochMillis(availability.observedSinceUnixMillis);
  const end = safeEpochMillis(availability.observedUntilUnixMillis);
  return availabilityRangeTicks(start, end, count);
}

export function availabilityRangeTicks(startMillis: number, endMillis: number, count = 5): number[] {
  if (endMillis <= startMillis || count < 2) return [];
  return Array.from(
    { length: count },
    (_, index) => startMillis + ((endMillis - startMillis) * index) / (count - 1),
  );
}

export function clipAvailabilitySegments(
  segments: AvailabilitySegment[],
  range: AvailabilityRange,
): AvailabilitySegment[] {
  if (range.endMillis <= range.startMillis) return [];
  return segments
    .map((segment) => ({
      ...segment,
      startMillis: Math.max(segment.startMillis, range.startMillis),
      endMillis: Math.min(segment.endMillis, range.endMillis),
    }))
    .filter((segment) => segment.endMillis > segment.startMillis);
}

export function recentAvailabilityFocusRange(
  segments: AvailabilitySegment[],
  maxSegments = 18,
): AvailabilityRange | null {
  if (segments.length <= maxSegments || maxSegments < 2) return null;
  const fullStart = segments[0]?.startMillis ?? 0;
  const fullEnd = segments.at(-1)?.endMillis ?? fullStart;
  const anchor = segments[Math.max(0, segments.length - maxSegments)];
  if (!anchor || fullEnd <= fullStart) return null;

  const visibleDuration = fullEnd - anchor.startMillis;
  const fullDuration = fullEnd - fullStart;
  if (visibleDuration >= fullDuration * 0.9) return null;
  const padding = Math.max(60_000, visibleDuration * 0.06);
  return {
    startMillis: Math.max(fullStart, anchor.startMillis - padding),
    endMillis: fullEnd,
  };
}

export function availabilitySegmentFocusRange(
  segment: AvailabilitySegment,
  observedRange: AvailabilityRange,
): AvailabilityRange | null {
  const fullDuration = observedRange.endMillis - observedRange.startMillis;
  const segmentDuration = segment.endMillis - segment.startMillis;
  if (fullDuration <= 0 || segmentDuration <= 0 || segmentDuration >= fullDuration * 0.65) return null;

  const desiredDuration = Math.min(
    fullDuration * 0.65,
    Math.max(15 * 60_000, segmentDuration * 2.5),
  );
  if (desiredDuration >= fullDuration * 0.9) return null;
  const center = segment.startMillis + segmentDuration / 2;
  let startMillis = center - desiredDuration / 2;
  let endMillis = center + desiredDuration / 2;
  if (startMillis < observedRange.startMillis) {
    endMillis += observedRange.startMillis - startMillis;
    startMillis = observedRange.startMillis;
  }
  if (endMillis > observedRange.endMillis) {
    startMillis -= endMillis - observedRange.endMillis;
    endMillis = observedRange.endMillis;
  }
  return {
    startMillis: Math.max(observedRange.startMillis, startMillis),
    endMillis: Math.min(observedRange.endMillis, endMillis),
  };
}

export function sessionForAvailabilitySegment(
  segment: AvailabilitySegment,
  sessions: AgentConnectionSession[],
  toleranceMillis = 5_000,
): AvailabilitySessionMatch | null {
  if (segment.state === "offline") {
    const reconnect = closestSession(
      sessions,
      (session) => Math.abs(safeEpochMillis(session.connectedAtUnixMillis) - segment.endMillis),
      toleranceMillis,
    );
    if (reconnect) return { session: reconnect, relation: "reconnect" };
    const disconnected = closestSession(
      sessions.filter((session) => !session.active && session.disconnectedAtUnixMillis > 0n),
      (session) => Math.abs(safeEpochMillis(session.disconnectedAtUnixMillis) - segment.startMillis),
      toleranceMillis,
    );
    return disconnected ? { session: disconnected, relation: "disconnect" } : null;
  }

  const session = closestSession(
    sessions,
    (candidate) => Math.abs(safeEpochMillis(candidate.connectedAtUnixMillis) - segment.startMillis),
    toleranceMillis,
  );
  return session ? { session, relation: "session" } : null;
}

function closestSession(
  sessions: AgentConnectionSession[],
  distanceFor: (session: AgentConnectionSession) => number,
  toleranceMillis: number,
): AgentConnectionSession | null {
  let best: { session: AgentConnectionSession; distance: number } | null = null;
  for (const session of sessions) {
    const distance = distanceFor(session);
    if (distance > toleranceMillis || (best && distance >= best.distance)) continue;
    best = { session, distance };
  }
  return best?.session ?? null;
}

export function availabilityObservedMillis(
  availability: GetAgentAvailabilityResponse | null | undefined,
): number {
  if (!availability) return 0;
  return Math.max(
    0,
    safeEpochMillis(availability.observedUntilUnixMillis) - safeEpochMillis(availability.observedSinceUnixMillis),
  );
}

function safeEpochMillis(value: bigint): number {
  const max = BigInt(Number.MAX_SAFE_INTEGER);
  const min = BigInt(Number.MIN_SAFE_INTEGER);
  if (value > max) return Number.MAX_SAFE_INTEGER;
  if (value < min) return Number.MIN_SAFE_INTEGER;
  return Number(value);
}

function clamp(value: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, value));
}

/**
 * Fleet lanes add two states to the single-agent timeline:
 * - "unobserved": the agent did not exist yet (or was outside retention), so
 *   the time is neither uptime nor downtime;
 * - "omitted": the server dropped this agent's oldest intervals to bound the
 *   fleet response, so the state before the first kept interval is unknown.
 */
export type FleetLaneState = AvailabilityState | "unobserved" | "omitted";

export type FleetLaneSegment = {
  startMillis: number;
  endMillis: number;
  state: FleetLaneState;
  active: boolean;
};

export type FleetAvailabilityLane = {
  summary: AgentAvailabilitySummary;
  segments: FleetLaneSegment[];
  observedMillis: number;
  /** Uptime as a 0..1 fraction, or null when nothing was observed in the window. */
  uptimeFraction: number | null;
};

export type FleetAvailabilityOverview = {
  agentCount: number;
  enabledCount: number;
  disabledCount: number;
  /** Mean uptime fraction across enabled agents with observed time, or null. */
  averageUptimeFraction: number | null;
  disconnectCount: number;
  truncatedCount: number;
};

export function fleetAvailabilityRange(
  response: ListAgentAvailabilityResponse | null | undefined,
): AvailabilityRange {
  if (!response) return { startMillis: 0, endMillis: 0 };
  return {
    startMillis: safeEpochMillis(response.windowSinceUnixMillis),
    endMillis: safeEpochMillis(response.windowUntilUnixMillis),
  };
}

/** Segments for one agent on the fleet's shared time axis, covering the whole range. */
export function fleetLaneSegments(
  summary: AgentAvailabilitySummary,
  range: AvailabilityRange,
): FleetLaneSegment[] {
  if (range.endMillis <= range.startMillis) return [];
  const observedStart = clamp(safeEpochMillis(summary.observedSinceUnixMillis), range.startMillis, range.endMillis);
  const observedEnd = clamp(safeEpochMillis(summary.observedUntilUnixMillis), range.startMillis, range.endMillis);
  if (observedEnd <= observedStart) {
    return [{ ...range, state: "unobserved", active: false }];
  }

  let knownStart = observedStart;
  if (summary.intervalsTruncated && summary.intervals.length) {
    const earliestKept = Math.min(...summary.intervals.map((interval) => safeEpochMillis(interval.connectedAtUnixMillis)));
    knownStart = clamp(earliestKept, observedStart, observedEnd);
  }

  const segments: FleetLaneSegment[] = [];
  if (observedStart > range.startMillis) {
    segments.push({ startMillis: range.startMillis, endMillis: observedStart, state: "unobserved", active: false });
  }
  if (knownStart > observedStart) {
    segments.push({ startMillis: observedStart, endMillis: knownStart, state: "omitted", active: false });
  }
  segments.push(...availabilitySegments({
    observedSinceUnixMillis: BigInt(knownStart),
    observedUntilUnixMillis: BigInt(observedEnd),
    intervals: summary.intervals,
  }));
  if (observedEnd < range.endMillis) {
    segments.push({ startMillis: observedEnd, endMillis: range.endMillis, state: "unobserved", active: false });
  }
  return segments;
}

/** One lane per agent: enabled agents first, worst uptime first, never-observed agents after observed ones. */
export function fleetAvailabilityLanes(
  response: ListAgentAvailabilityResponse | null | undefined,
): FleetAvailabilityLane[] {
  if (!response) return [];
  const range = fleetAvailabilityRange(response);
  return response.agents
    .map((summary) => {
      const observedMillis = Math.max(
        0,
        safeEpochMillis(summary.observedUntilUnixMillis) - safeEpochMillis(summary.observedSinceUnixMillis),
      );
      return {
        summary,
        segments: fleetLaneSegments(summary, range),
        observedMillis,
        uptimeFraction: observedMillis > 0 && Number.isFinite(summary.uptimePercent)
          ? clamp(summary.uptimePercent, 0, 1)
          : null,
      };
    })
    .sort(compareFleetLanes);
}

function compareFleetLanes(left: FleetAvailabilityLane, right: FleetAvailabilityLane): number {
  const enabledRank = (lane: FleetAvailabilityLane) => (lane.summary.enabled ? 0 : 1);
  const observedRank = (lane: FleetAvailabilityLane) => (lane.uptimeFraction === null ? 1 : 0);
  return enabledRank(left) - enabledRank(right)
    || observedRank(left) - observedRank(right)
    || (left.uptimeFraction ?? 0) - (right.uptimeFraction ?? 0)
    || Number(right.summary.disconnectCount - left.summary.disconnectCount)
    || left.summary.agentName.localeCompare(right.summary.agentName)
    || left.summary.agentPublicId.localeCompare(right.summary.agentPublicId);
}

export function fleetAvailabilityOverview(lanes: FleetAvailabilityLane[]): FleetAvailabilityOverview {
  const enabled = lanes.filter((lane) => lane.summary.enabled);
  const observed = enabled.filter((lane) => lane.uptimeFraction !== null);
  return {
    agentCount: lanes.length,
    enabledCount: enabled.length,
    disabledCount: lanes.length - enabled.length,
    averageUptimeFraction: observed.length
      ? observed.reduce((sum, lane) => sum + (lane.uptimeFraction ?? 0), 0) / observed.length
      : null,
    disconnectCount: lanes.reduce((sum, lane) => sum + Number(lane.summary.disconnectCount), 0),
    truncatedCount: lanes.filter((lane) => lane.summary.intervalsTruncated).length,
  };
}

/** The lane segment under a 0..1 position along the shared axis. */
export function fleetLaneSegmentAt(
  segments: FleetLaneSegment[],
  range: AvailabilityRange,
  ratio: number,
): FleetLaneSegment | null {
  if (!segments.length || range.endMillis <= range.startMillis) return null;
  const millis = range.startMillis + clamp(ratio, 0, 1) * (range.endMillis - range.startMillis);
  return segments.find((segment) => millis >= segment.startMillis && millis < segment.endMillis)
    ?? segments.at(-1)
    ?? null;
}

/** Uptime with one decimal so 99.2% and 99.9% stay distinguishable; exact 0 and 100 stay whole. */
export function formatUptimeFraction(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "—";
  const percent = clamp(value, 0, 1) * 100;
  if (percent >= 100) return "100%";
  if (percent <= 0) return "0%";
  // Never round a lossy agent up to 100%.
  return `${(Math.floor(percent * 10) / 10).toFixed(1)}%`;
}
