import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { AgentUpdateAssignmentState as State, AgentUpdateCampaignState as CampaignState, AgentUpdateDesiredAction as Action, AgentUpdateCampaignSchema, AgentUpdateOverviewAgentSchema } from "@/gen/proto/p2pstream/v1/management_pb";
import { agentIsAlreadyOnUpdateTarget, agentRolloutStatus, agentUpdatePreviewBlockerLabel, canResumeUpdateCampaign, recoverableUpdateAssignments, retryableStageAssignments, updateRecoveryMessage } from "./agentUpdateRecovery";
import { createAgentUpdatePolling } from "./agentUpdatePolling";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

async function waitFor(condition: () => boolean) {
  for (let attempt = 0; attempt < 100 && !condition(); attempt += 1) await pause(5);
  expect(condition()).toBe(true);
}

function pause(milliseconds: number) { return new Promise<void>((resolve) => setTimeout(resolve, milliseconds)); }

describe("agent update workflow review regressions", () => {
  test("preview explains same-target and updater compatibility blockers", () => {
    expect(agentUpdatePreviewBlockerLabel("already_on_target")).toBe("Already running this release");
    expect(agentUpdatePreviewBlockerLabel("updater_version_incompatible", "v0.1.53-staging.88")).toContain("Repair the pinned updater to v0.1.53-staging.88 or newer");
    expect(agentUpdatePreviewBlockerLabel("active_assignment")).toContain("existing campaign");
  });

  test("already-current hosts are identified by both live version and commit", () => {
    const target = { version: "v0.1.53-staging.88", commit: "a".repeat(40) };
    const agent = create(AgentUpdateOverviewAgentSchema, { updaterEnrolled: true, connected: true, tunnelVersion: target.version, tunnelCommit: target.commit });
    expect(agentIsAlreadyOnUpdateTarget(agent, target)).toBe(true);
    expect(agentRolloutStatus(agent, [], target)).toBe("Already running this release");
    agent.tunnelCommit = "b".repeat(40);
    expect(agentIsAlreadyOnUpdateTarget(agent, target)).toBe(false);
    agent.tunnelCommit = target.commit;
    agent.connected = false;
    expect(agentIsAlreadyOnUpdateTarget(agent, target)).toBe(false);
    expect(agentIsAlreadyOnUpdateTarget(agent, undefined)).toBe(false);
  });
  test("recovering reserved hosts does not silently restart other failed rollouts", () => {
    const campaign = create(AgentUpdateCampaignSchema, {
      state: CampaignState.PAUSED,
      assignments: [
        { id: 1n, state: State.BLOCKED, cordoned: true, desiredAction: Action.NONE },
        { id: 2n, state: State.FAILED, cordoned: false, desiredAction: Action.NONE },
        { id: 3n, state: State.FAILED, cordoned: true, desiredAction: Action.ROLLBACK },
      ],
    });
    expect(recoverableUpdateAssignments(campaign).map((item) => item.id)).toEqual([1n]);
    expect(retryableStageAssignments(campaign).map((item) => item.id)).toEqual([2n]);
    expect(canResumeUpdateCampaign(campaign)).toBe(false);
    campaign.assignments[0]!.state = State.FAILED;
    campaign.assignments[0]!.desiredAction = Action.ROLLBACK;
    expect(recoverableUpdateAssignments(campaign)).toEqual([]);
    expect(updateRecoveryMessage(campaign)).toContain("retry the other failed assignments before resuming");
    expect(canResumeUpdateCampaign(campaign)).toBe(false);
    campaign.assignments[1]!.state = State.PENDING;
    expect(canResumeUpdateCampaign(campaign)).toBe(true);
    campaign.state = CampaignState.CANCELLED;
    expect(retryableStageAssignments(campaign)).toEqual([]);
    expect(canResumeUpdateCampaign(campaign)).toBe(false);
  });

  test("a refresh after a mutation queues one fresh read without overlapping requests", async () => {
    const responses = [deferred<number>(), deferred<number>()];
    const applied: number[] = [];
    let loads = 0;
    let busy = false;
    const polling = createAgentUpdatePolling({
      scope: () => "environment-a",
      load: () => responses[loads++]!.promise,
      apply: (value) => applied.push(value),
      error: () => { throw new Error("Unexpected refresh error"); },
      loading: (value) => { busy = value; },
      canPoll: () => true,
    });
    try {
      const first = polling.refresh();
      const afterMutation = polling.refresh();
      const duplicate = polling.refresh();
      expect(loads).toBe(1);
      responses[0]!.resolve(1);
      await waitFor(() => loads === 2);
      expect(busy).toBe(true);
      responses[1]!.resolve(2);
      await Promise.all([first, afterMutation, duplicate]);
      expect(loads).toBe(2);
      expect(applied).toEqual([1, 2]);
      expect(busy).toBe(false);
    } finally { polling.stop(); }
  });

  test("switching environments discards a late response even if transport ignores abort", async () => {
    let scope = "environment-a";
    const oldResponse = deferred<string>();
    const signals: AbortSignal[] = [];
    const applied: string[] = [];
    const errors: unknown[] = [];
    const polling = createAgentUpdatePolling({
      scope: () => scope,
      load: (signal) => { signals.push(signal); return scope === "environment-a" ? oldResponse.promise : Promise.resolve(scope); },
      apply: (value) => applied.push(value),
      error: (error) => errors.push(error),
      loading: () => {},
      canPoll: () => true,
    });
    try {
      const first = polling.refresh();
      scope = "environment-b";
      const next = polling.scopeChanged();
      expect(signals[0]!.aborted).toBe(true);
      oldResponse.resolve("environment-a");
      await Promise.all([first, next]);
      expect(applied).toEqual(["environment-b"]);
      expect(errors).toEqual([]);
    } finally { polling.stop(); }
  });

  test("stopping the page aborts a pending request and prevents any late writes", async () => {
    const response = deferred<string>();
    const applied: string[] = [];
    const errors: unknown[] = [];
    let signal: AbortSignal | undefined;
    let loads = 0;
    const polling = createAgentUpdatePolling({
      scope: () => "environment-a",
      load: (value) => { signal = value; loads += 1; return response.promise; },
      apply: (value) => applied.push(value),
      error: (error) => errors.push(error),
      loading: () => {},
      canPoll: () => true,
      intervalMillis: 5,
    });
    const pending = polling.start();
    polling.stop();
    expect(signal!.aborted).toBe(true);
    response.reject(new Error("Late transport failure"));
    await pending;
    await polling.refresh();
    await pause(20);
    expect(applied).toEqual([]);
    expect(errors).toEqual([]);
    expect(loads).toBe(1);
  });

  test("polling reflects completed recovery automatically and pauses while actions are busy", async () => {
    let canPoll = true;
    let serverState = "rollback pending";
    const applied: string[] = [];
    const polling = createAgentUpdatePolling({
      scope: () => "environment-a",
      load: async () => serverState,
      apply: (value) => applied.push(value),
      error: () => { throw new Error("Unexpected refresh error"); },
      loading: () => {},
      canPoll: () => canPoll,
      intervalMillis: 5,
    });
    try {
      await polling.start();
      expect(applied).toEqual(["rollback pending"]);
      serverState = "recovered; ready for rollout";
      await waitFor(() => applied.includes(serverState));
      canPoll = false;
      const count = applied.length;
      await pause(20);
      expect(applied).toHaveLength(count);
      canPoll = true;
      await waitFor(() => applied.length > count);
    } finally { polling.stop(); }
  });
});
