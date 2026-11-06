import {
  AgentUpdateAssignmentState as AssignmentState,
  AgentUpdateCampaignState as CampaignState,
  AgentUpdateDesiredAction as Action,
  type AgentUpdateCampaign,
  type AgentUpdateOverviewAgent,
  type AgentUpdateTarget,
} from "@/gen/proto/p2pstream/v1/management_pb";

export function retryableUpdateAssignments(campaign: AgentUpdateCampaign) {
  if (![CampaignState.RUNNING, CampaignState.PAUSED, CampaignState.CANCELLED].includes(campaign.state)) return [];
  return campaign.assignments.filter((assignment) => {
    if (assignment.state === AssignmentState.BLOCKED && assignment.cordoned) return true;
    if (campaign.state === CampaignState.CANCELLED || campaign.state === CampaignState.COMPLETED || assignment.cordoned) return false;
    return assignment.state === AssignmentState.BLOCKED || assignment.state === AssignmentState.CANCELLED ||
      (assignment.state === AssignmentState.FAILED && assignment.desiredAction === Action.NONE);
  });
}

export function recoverableUpdateAssignments(campaign: AgentUpdateCampaign) {
  return retryableUpdateAssignments(campaign).filter((assignment) => assignment.cordoned);
}

export function retryableStageAssignments(campaign: AgentUpdateCampaign) {
  return retryableUpdateAssignments(campaign).filter((assignment) => !assignment.cordoned);
}

export function canResumeUpdateCampaign(campaign: AgentUpdateCampaign): boolean {
  return campaign.state === CampaignState.PAUSED && !campaign.assignments.some((assignment) =>
    [AssignmentState.FAILED, AssignmentState.BLOCKED].includes(assignment.state) &&
    !(assignment.cordoned && assignment.desiredAction === Action.ROLLBACK),
  );
}

export function agentUpdatePreviewBlockerLabel(blocker: string, minimumUpdaterVersion?: string): string {
  if (blocker === "already_on_target") return "Already running this release";
  if (blocker === "updater_version_incompatible") return `Repair the pinned updater to ${minimumUpdaterVersion || "a compatible version"} or newer before rollout`;
  if (blocker === "updater_not_recently_seen") return "Updater is not checking in; inspect its service before rollout";
  if (blocker === "active_assignment") return "Finish recovery in the existing campaign before another rollout";
  return blocker.replaceAll("_", " ");
}

export function updateRecoveryMessage(campaign: AgentUpdateCampaign): string {
  const reserved = campaign.assignments.filter((assignment) => assignment.cordoned);
  if (reserved.some((assignment) => assignment.state === AssignmentState.BLOCKED)) {
    return "Recovery is required before these hosts can join another rollout. Recover agents requests rollback; the updater must finish and establish a fresh agent connection.";
  }
  if (reserved.some((assignment) => assignment.desiredAction === Action.ROLLBACK)) {
    if (campaign.state === CampaignState.PAUSED) {
      return retryableStageAssignments(campaign).length
        ? "Rollback recovery is queued. Cancel this campaign to recover the reserved hosts, or retry the other failed assignments before resuming the rollout."
        : "Rollback recovery is queued. Resume or cancel this campaign to let the updater complete recovery.";
    }
    return "Waiting for the updater to complete rollback and reconnect. Hosts remain reserved until recovery is verified.";
  }
  if (reserved.length && campaign.state === CampaignState.CANCELLED) {
    return "Waiting for recovery evidence and a fresh agent connection before releasing these hosts for another rollout.";
  }
  return "";
}

export function agentIsAlreadyOnUpdateTarget(agent: AgentUpdateOverviewAgent, target?: Pick<AgentUpdateTarget, "version" | "commit"> | null): boolean {
  return Boolean(agent.connected && target?.version && target.commit && agent.tunnelVersion === target.version && agent.tunnelCommit === target.commit);
}

export function agentRolloutStatus(agent: AgentUpdateOverviewAgent, campaigns: AgentUpdateCampaign[], target?: Pick<AgentUpdateTarget, "version" | "commit"> | null): string {
  if (!agent.updaterEnrolled) return "Updater not enrolled";
  if (agent.activeAssignmentId) {
    const campaign = campaigns.find((item) => item.assignments.some((assignment) => assignment.id === agent.activeAssignmentId));
    const assignment = campaign?.assignments.find((item) => item.id === agent.activeAssignmentId);
    if (assignment?.desiredAction === Action.ROLLBACK) return "Rollback recovery pending — see the existing campaign";
    if (assignment?.cordoned && (assignment.state === AssignmentState.BLOCKED || campaign?.state === CampaignState.CANCELLED)) {
      return "Recovery required — see the existing campaign";
    }
    return campaign ? `Assigned to ${campaign.name}` : "Already assigned to another update";
  }
  if (agentIsAlreadyOnUpdateTarget(agent, target)) return "Already running this release";
  return agent.connected ? "Ready for preview" : "Disconnected";
}
