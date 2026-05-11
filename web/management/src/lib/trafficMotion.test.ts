import { describe, expect, test } from "bun:test";
import {
  buildMotionNodeBox,
  buildMotionPlan,
  motionEdgeKey,
  motionSegmentEnd,
  motionSegmentStart,
  pointAtMotionDistance,
  type MotionNodeBox,
  type MotionPoint,
} from "@/lib/trafficMotion";

describe("trafficMotion", () => {
  test("two-node path moves from source interior through edge into target center", () => {
    const nodes = nodeMap(["ingress", "response"]);
    const plan = buildMotionPlan({
      path: ["ingress", "response"],
      targetNodeIndex: 1,
      nodes,
      edgeRoutes: edgeMap(nodes, "ingress", "response"),
      signature: "two-node",
    });

    expect(plan.segments.map((segment) => segment.kind)).toEqual(["node-exit", "edge", "node-entry"]);
    expect(pointAtMotionDistance(plan, 0)).toEqual(nodes.get("ingress")?.center);
    expect(pointAtMotionDistance(plan, plan.targetLength)).toEqual(nodes.get("response")?.center);
  });

  test("three-node path includes a through segment in the middle node", () => {
    const nodes = nodeMap(["ingress", "listener", "response"]);
    const plan = buildMotionPlan({
      path: ["ingress", "listener", "response"],
      targetNodeIndex: 2,
      nodes,
      edgeRoutes: new Map([
        ...edgeMap(nodes, "ingress", "listener"),
        ...edgeMap(nodes, "listener", "response"),
      ]),
      signature: "three-node",
    });

    expect(plan.segments.map((segment) => segment.kind)).toEqual([
      "node-exit",
      "edge",
      "node-through",
      "edge",
      "node-entry",
    ]);
    expect(plan.segments[2]?.points).toEqual([
      nodes.get("listener")?.entry,
      nodes.get("listener")?.center,
      nodes.get("listener")?.exit,
    ]);
  });

  test("final target ends at the node center, not the exit", () => {
    const nodes = nodeMap(["ingress", "response"]);
    const plan = buildMotionPlan({
      path: ["ingress", "response"],
      targetNodeIndex: 1,
      nodes,
      edgeRoutes: edgeMap(nodes, "ingress", "response"),
      signature: "final-center",
    });

    expect(pointAtMotionDistance(plan, plan.targetLength)).toEqual(nodes.get("response")?.center);
    expect(pointAtMotionDistance(plan, plan.targetLength)).not.toEqual(nodes.get("response")?.exit);
  });

  test("intermediate target stops at the intermediate node center", () => {
    const nodes = nodeMap(["ingress", "listener", "response"]);
    const plan = buildMotionPlan({
      path: ["ingress", "listener", "response"],
      targetNodeIndex: 1,
      nodes,
      edgeRoutes: new Map([
        ...edgeMap(nodes, "ingress", "listener"),
        ...edgeMap(nodes, "listener", "response"),
      ]),
      signature: "intermediate-center",
    });

    expect(pointAtMotionDistance(plan, plan.targetLength)).toEqual(nodes.get("listener")?.center);
  });

  test("consecutive motion segments are continuous", () => {
    const nodes = nodeMap(["ingress", "listener", "backend", "response"]);
    const plan = buildMotionPlan({
      path: ["ingress", "listener", "backend", "response"],
      targetNodeIndex: 3,
      nodes,
      edgeRoutes: new Map([
        ...edgeMap(nodes, "ingress", "listener"),
        ...edgeMap(nodes, "listener", "backend"),
        ...edgeMap(nodes, "backend", "response"),
      ]),
      signature: "continuous",
    });

    for (let index = 0; index < plan.segments.length - 1; index += 1) {
      expect(motionSegmentEnd(plan.segments[index])).toEqual(motionSegmentStart(plan.segments[index + 1]));
    }
  });

  test("motion length includes node interiors and edge distance", () => {
    const nodes = nodeMap(["ingress", "response"]);
    const plan = buildMotionPlan({
      path: ["ingress", "response"],
      targetNodeIndex: 1,
      nodes,
      edgeRoutes: edgeMap(nodes, "ingress", "response"),
      signature: "length",
    });

    const ingress = nodes.get("ingress")!;
    const response = nodes.get("response")!;
    const expectedLength =
      distance(ingress.center, ingress.exit) +
      distance(ingress.exit, response.entry) +
      distance(response.entry, response.center);

    expect(plan.totalLength).toBeCloseTo(expectedLength, 6);
  });

  test("same input creates stable geometry", () => {
    const nodes = nodeMap(["ingress", "response"]);
    const input = {
      path: ["ingress", "response"],
      targetNodeIndex: 1,
      nodes,
      edgeRoutes: edgeMap(nodes, "ingress", "response"),
      signature: "stable",
    };

    expect(buildMotionPlan(input)).toEqual(buildMotionPlan(input));
  });

  test("missing node or edge data is handled without throwing", () => {
    const nodes = nodeMap(["ingress", "response"]);

    expect(() => buildMotionPlan({
      path: ["ingress", "missing", "response"],
      targetNodeIndex: 2,
      nodes,
      edgeRoutes: new Map(),
      signature: "missing-node",
    })).not.toThrow();

    const fallbackPlan = buildMotionPlan({
      path: ["ingress", "response"],
      targetNodeIndex: 1,
      nodes,
      edgeRoutes: new Map(),
      signature: "missing-edge",
    });
    expect(fallbackPlan.segments.map((segment) => segment.kind)).toEqual(["node-exit", "edge", "node-entry"]);
    expect(pointAtMotionDistance(fallbackPlan, fallbackPlan.targetLength)).toEqual(nodes.get("response")?.center);
  });
});

function nodeMap(keys: string[]): Map<string, MotionNodeBox> {
  return new Map(keys.map((key, index) => [
    key,
    buildMotionNodeBox({
      key,
      x: index * 200,
      y: 0,
      width: 100,
      height: 40,
    }),
  ]));
}

function edgeMap(
  nodes: Map<string, MotionNodeBox>,
  from: string,
  to: string,
): Map<string, MotionPoint[]> {
  const fromNode = nodes.get(from)!;
  const toNode = nodes.get(to)!;
  return new Map([[motionEdgeKey(from, to), [fromNode.exit, toNode.entry]]]);
}

function distance(a: MotionPoint, b: MotionPoint): number {
  return Math.hypot(b.x - a.x, b.y - a.y);
}
