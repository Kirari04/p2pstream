import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import {
  AgentUpdateAssignmentState as State,
  AgentUpdateCampaignState as CampaignState,
  AgentUpdateDesiredAction as Action,
  AgentUpdateCampaignSchema,
  AgentUpdateOverviewAgentSchema,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { agentRolloutStatus, retryableUpdateAssignments, updateRecoveryMessage } from "./agentUpdateRecovery";

describe("agent update recovery", () => {
  test("a cancelled campaign recovers fenced hosts without resubmitting terminal or in-flight failures", () => {
    const campaign = create(AgentUpdateCampaignSchema, {
      state: CampaignState.CANCELLED,
      assignments: [
        { id: 1n, state: State.BLOCKED, cordoned: true, desiredAction: Action.NONE },
        { id: 2n, state: State.FAILED, cordoned: true, desiredAction: Action.ROLLBACK },
        { id: 3n, state: State.FAILED, cordoned: false, desiredAction: Action.NONE },
        { id: 4n, state: State.CANCELLED, cordoned: false, desiredAction: Action.NONE },
        { id: 5n, state: State.BLOCKED, cordoned: true, desiredAction: Action.ROLLBACK },
      ],
    });
    expect(retryableUpdateAssignments(campaign).map((item) => item.id)).toEqual([1n, 5n]);
    expect(updateRecoveryMessage(campaign)).toContain("Recover agents");
    campaign.state = CampaignState.COMPLETED;
    expect(retryableUpdateAssignments(campaign)).toEqual([]);
  });

  test("paused staging failures can retry while an already queued rollback is excluded", () => {
    const campaign = create(AgentUpdateCampaignSchema, {
      state: CampaignState.PAUSED,
      assignments: [
        { id: 1n, state: State.FAILED, desiredAction: Action.NONE },
        { id: 2n, state: State.FAILED, cordoned: true, desiredAction: Action.ROLLBACK },
      ],
    });
    expect(retryableUpdateAssignments(campaign).map((item) => item.id)).toEqual([1n]);
    expect(updateRecoveryMessage(campaign)).toContain("retry the other failed assignments before resuming");
    campaign.state = CampaignState.CANCELLED;
    expect(retryableUpdateAssignments(campaign)).toEqual([]);
    expect(updateRecoveryMessage(campaign)).toContain("Waiting for the updater");
  });

  test("blocked hosts get recovery guidance and release the notice after verified recovery", () => {
    const campaign = create(AgentUpdateCampaignSchema, {
      state: CampaignState.CANCELLED,
      assignments: [{ id: 1n, state: State.BLOCKED, cordoned: true, desiredAction: Action.NONE }],
    });
    expect(updateRecoveryMessage(campaign)).toContain("Recover agents");
    campaign.assignments[0]!.state = State.AWAITING_TUNNEL;
    expect(updateRecoveryMessage(campaign)).toContain("fresh agent connection");
    campaign.assignments[0]!.cordoned = false;
    campaign.assignments[0]!.state = State.FAILED;
    expect(updateRecoveryMessage(campaign)).toBe("");
  });

  test("rollout picker distinguishes recovery reservations from ordinary assignments", () => {
    const campaign = create(AgentUpdateCampaignSchema, {
      name: "Old campaign", state: CampaignState.CANCELLED,
      assignments: [{ id: 42n, state: State.BLOCKED, cordoned: true, desiredAction: Action.NONE }],
    });
    const agent = create(AgentUpdateOverviewAgentSchema, { updaterEnrolled: true, connected: true, activeAssignmentId: 42n });
    expect(agentRolloutStatus(agent, [campaign])).toContain("Recovery required");
    campaign.assignments[0]!.desiredAction = Action.ROLLBACK;
    expect(agentRolloutStatus(agent, [campaign])).toContain("Rollback recovery pending");
    agent.activeAssignmentId = 0n;
    expect(agentRolloutStatus(agent, [campaign])).toBe("Ready for preview");
    agent.activeAssignmentId = 42n;
    campaign.assignments[0]!.desiredAction = Action.STAGE;
    campaign.assignments[0]!.cordoned = false;
    campaign.assignments[0]!.state = State.STAGING;
    campaign.state = CampaignState.RUNNING;
    expect(agentRolloutStatus(agent, [campaign])).toBe("Assigned to Old campaign");
  });
});
