export type MotionPoint = {
  x: number;
  y: number;
};

export type MotionNodeBox = {
  key: string;
  left: number;
  right: number;
  top: number;
  bottom: number;
  center: MotionPoint;
  entry: MotionPoint;
  exit: MotionPoint;
};

export type MotionSegmentKind = "node-exit" | "edge" | "node-through" | "node-entry";

export type MotionSegment = {
  kind: MotionSegmentKind;
  fromKey: string;
  toKey: string;
  length: number;
  points: MotionPoint[];
};

export type MotionPlan = {
  signature: string;
  path: string[];
  targetNodeIndex: number;
  segments: MotionSegment[];
  totalLength: number;
  targetLength: number;
};

export function motionEdgeKey(from: string, to: string): string {
  return `${from}->${to}`;
}

export function buildMotionNodeBox(input: {
  key: string;
  x: number;
  y: number;
  width: number;
  height: number;
}): MotionNodeBox {
  const center = {
    x: input.x + input.width / 2,
    y: input.y + input.height / 2,
  };
  return {
    key: input.key,
    left: input.x,
    right: input.x + input.width,
    top: input.y,
    bottom: input.y + input.height,
    center,
    entry: { x: input.x, y: center.y },
    exit: { x: input.x + input.width, y: center.y },
  };
}

export function buildMotionPlan(input: {
  path: string[];
  targetNodeIndex: number;
  nodes: Map<string, MotionNodeBox>;
  edgeRoutes: Map<string, MotionPoint[]>;
  signature: string;
}): MotionPlan {
  const path = dedupeConsecutive(input.path);
  const targetNodeIndex = clampIndex(input.targetNodeIndex, path.length);
  const segments: MotionSegment[] = [];
  let totalLength = 0;
  let targetLength = targetNodeIndex === 0 ? 0 : -1;

  for (let index = 0; index < path.length - 1; index += 1) {
    const fromKey = path[index];
    const toKey = path[index + 1];
    const fromNode = input.nodes.get(fromKey);
    const toNode = input.nodes.get(toKey);
    if (!fromNode || !toNode) continue;

    if (index === 0) {
      totalLength += pushSegment(segments, {
        kind: "node-exit",
        fromKey,
        toKey: fromKey,
        points: [fromNode.center, fromNode.exit],
      });
    }

    totalLength += pushSegment(segments, {
      kind: "edge",
      fromKey,
      toKey,
      points: normalizedEdgePoints(input.edgeRoutes.get(motionEdgeKey(fromKey, toKey)), fromNode.exit, toNode.entry),
    });

    const isFinalPathNode = index + 1 === path.length - 1;
    const isTargetNode = index + 1 === targetNodeIndex;

    if (isFinalPathNode) {
      totalLength += pushSegment(segments, {
        kind: "node-entry",
        fromKey: toKey,
        toKey,
        points: [toNode.entry, toNode.center],
      });
      if (isTargetNode) targetLength = totalLength;
      continue;
    }

    const throughStart = totalLength;
    totalLength += pushSegment(segments, {
      kind: "node-through",
      fromKey: toKey,
      toKey,
      points: [toNode.entry, toNode.center, toNode.exit],
    });
    if (isTargetNode) {
      targetLength = throughStart + polylineLength([toNode.entry, toNode.center]);
    }
  }

  if (targetLength < 0) {
    targetLength = totalLength;
  }

  return {
    signature: input.signature,
    path,
    targetNodeIndex,
    segments,
    totalLength,
    targetLength: clamp(targetLength, 0, totalLength),
  };
}

export function pointAtMotionDistance(plan: MotionPlan, distance: number): MotionPoint | null {
  if (plan.segments.length === 0) return null;

  const targetDistance = clamp(distance, 0, plan.totalLength);
  let traversed = 0;

  for (const segment of plan.segments) {
    const nextTraversed = traversed + segment.length;
    if (targetDistance <= nextTraversed) {
      return pointAtPolylineDistance(segment.points, targetDistance - traversed);
    }
    traversed = nextTraversed;
  }

  return lastPoint(plan.segments.at(-1)?.points);
}

export function motionSegmentEnd(segment: MotionSegment): MotionPoint | null {
  return lastPoint(segment.points);
}

export function motionSegmentStart(segment: MotionSegment): MotionPoint | null {
  return segment.points[0] ?? null;
}

function pushSegment(
  segments: MotionSegment[],
  segment: Omit<MotionSegment, "length">,
): number {
  const points = dedupeAdjacentPoints(segment.points);
  const length = polylineLength(points);
  if (points.length < 2 || length <= 0) return 0;
  segments.push({ ...segment, points, length });
  return length;
}

function normalizedEdgePoints(
  routePoints: MotionPoint[] | undefined,
  fallbackStart: MotionPoint,
  fallbackEnd: MotionPoint,
): MotionPoint[] {
  const points = routePoints && routePoints.length >= 2
    ? routePoints.map((point) => ({ x: point.x, y: point.y }))
    : [fallbackStart, fallbackEnd];

  if (!samePoint(points[0], fallbackStart)) {
    points.unshift(fallbackStart);
  }
  if (!samePoint(points[points.length - 1], fallbackEnd)) {
    points.push(fallbackEnd);
  }
  return points;
}

function pointAtPolylineDistance(points: MotionPoint[], distance: number): MotionPoint | null {
  if (!points.length) return null;
  if (points.length === 1) return points[0];

  let traversed = 0;
  for (let index = 0; index < points.length - 1; index += 1) {
    const from = points[index];
    const to = points[index + 1];
    const length = pointDistance(from, to);
    if (length <= 0) continue;

    const nextTraversed = traversed + length;
    if (distance <= nextTraversed) {
      const progress = clamp((distance - traversed) / length, 0, 1);
      return {
        x: from.x + (to.x - from.x) * progress,
        y: from.y + (to.y - from.y) * progress,
      };
    }
    traversed = nextTraversed;
  }

  return lastPoint(points);
}

function polylineLength(points: MotionPoint[]): number {
  let length = 0;
  for (let index = 0; index < points.length - 1; index += 1) {
    length += pointDistance(points[index], points[index + 1]);
  }
  return length;
}

function pointDistance(a: MotionPoint, b: MotionPoint): number {
  return Math.hypot(b.x - a.x, b.y - a.y);
}

function dedupeAdjacentPoints(points: MotionPoint[]): MotionPoint[] {
  return points.filter((point, index) => index === 0 || !samePoint(point, points[index - 1]));
}

function dedupeConsecutive(values: string[]): string[] {
  return values.filter((value, index) => index === 0 || values[index - 1] !== value);
}

function samePoint(a: MotionPoint | undefined, b: MotionPoint | undefined): boolean {
  if (!a || !b) return false;
  return Math.abs(a.x - b.x) < 0.001 && Math.abs(a.y - b.y) < 0.001;
}

function lastPoint(points: MotionPoint[] | undefined): MotionPoint | null {
  return points?.[points.length - 1] ?? null;
}

function clampIndex(index: number, length: number): number {
  if (length <= 0) return 0;
  return Math.min(length - 1, Math.max(0, Math.trunc(index)));
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
