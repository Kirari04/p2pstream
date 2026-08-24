import type {
  AgentConnectionSession,
  GetAgentAvailabilityResponse,
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

export function availabilitySegments(
  availability: GetAgentAvailabilityResponse | null | undefined,
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
