import type { MotionPlan } from "@/lib/trafficMotion";
import type { TrafficFlowEditTarget } from "@/types/trafficFlowEdit";
import type { TraceRequest } from "@/types/trafficTrace";

export type TrafficNodeKind = "ingress" | "listener" | "waf" | "rate-limit" | "traffic-shaper" | "cache" | "route" | "target" | "redirect" | "agent" | "upstream" | "response";

export type AgentNodeStatus = {
  state: "connected" | "offline" | "disabled" | "unknown";
  label: string;
};

export type CacheNodeTone = "hit" | "miss" | "bypass" | "stored" | "lookup" | "neutral";

export type CacheNodeStatus = {
  label: string;
  tone: CacheNodeTone;
};

export type TrafficNodeData = {
  label: string;
  subLabel: string;
  kind: TrafficNodeKind;
  editTargets: TrafficFlowEditTarget[];
  agentStatus?: AgentNodeStatus;
  cacheStatus?: CacheNodeStatus;
};

export type DiagramNode = TrafficNodeData & {
  key: string;
  column: number;
  x: number;
  y: number;
};

export type DiagramNodeInput = Omit<DiagramNode, "x" | "y" | "editTargets"> & {
  editTargets?: TrafficFlowEditTarget[];
};

export type DiagramEdgeRoute = "default" | "agent-bypass" | "intermediate-bypass";

export type DiagramEdge = {
  from: string;
  to: string;
  route: DiagramEdgeRoute;
};

export type TrafficFlowGraph = {
  nodes: DiagramNode[];
  nodeByKey: Map<string, DiagramNode>;
  edges: DiagramEdge[];
};

export type Point = { x: number; y: number };

export type CubicSegment = {
  source: Point;
  sourceControl: Point;
  targetControl: Point;
  target: Point;
  lengthTable: Array<{ t: number; length: number }>;
  totalLength: number;
};

export type EdgeRouteGeometry = {
  from: string;
  to: string;
  route: DiagramEdgeRoute;
  segments: CubicSegment[];
  totalLength: number;
  path: string;
};

export type Bounds = {
  left: number;
  right: number;
  top: number;
  bottom: number;
};

export type VisualTokenStatus = "in-flight" | "success" | "client-error" | "server-error" | "failed";
export type VisualTokenCacheTone = "hit" | "miss" | "bypass" | "stored" | "";

export type VisualToken = {
  requestId: string;
  request: TraceRequest;
  path: string[];
  label: string;
  motionPlan: MotionPlan;
  currentDistance: number;
  targetDistance: number;
  startedAt: number;
  updatedAt: number;
  durationMs: number;
  finishedAt: number | null;
  status: VisualTokenStatus;
  cacheTone: VisualTokenCacheTone;
  skipped: boolean;
};

export type CacheStorePulse = {
  requestId: string;
  startedAt: number;
};

export const NODE_WIDTH = 152;
export const NODE_HEIGHT = 58;
export const COLUMN_X = [0, 190, 380, 570, 760, 950, 1140, 1330, 1520, 1710, 1900];
export const ROW_GAP = 92;
export const MIN_CENTER_Y = 92;
export const CACHE_SIDE_CENTER_Y = MIN_CENTER_Y;
export const FLOW_ID = "traffic-flow-diagram";
export const EDGE_CURVATURE = 0.25;
export const BEZIER_LENGTH_SAMPLES = 32;
export const BYPASS_LANE_GAP = 54;
export const BYPASS_X_GAP = 24;
export const MIN_BYPASS_Y = 28;

export const MIN_PLAYBACK_MS = 1800;
export const MAX_PLAYBACK_MS = 6000;
export const BASE_PLAYBACK_MS = 1200;
export const PER_HOP_MS = 450;
export const LOW_BURST_THRESHOLD = 12;
export const HIGH_BURST_THRESHOLD = 40;
export const MAX_RENDERED_TOKENS_NORMAL = 36;
export const MAX_RENDERED_TOKENS_STRESSED = 16;
export const FRAME_STRESS_MS = 40;
export const FRAME_RECOVERY_MS = 20;
export const FRAME_RECOVERY_COUNT = 12;
export const COMPLETION_HOLD_MS = 2000;
export const COMPLETION_FADE_MS = 650;
export const CACHE_PROXIMITY_PX = 40;
export const CACHE_STORE_PULSE_MS = 900;
