export type TrafficFlowEditTargetKind = "listener" | "route" | "backend" | "agent" | "rate-limit" | "waf" | "traffic-shaper";

export type TrafficFlowEditTarget = {
  kind: TrafficFlowEditTargetKind;
  id: string;
  label: string;
  subLabel?: string;
};

export type TrafficFlowEditRequest = {
  nodeKey: string;
  nodeLabel: string;
  targets: TrafficFlowEditTarget[];
};
