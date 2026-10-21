import {
  ManagementTlsAgentRolloutState,
  ManagementTlsRotationPhase,
  type ManagementTlsAgentRollout,
} from "@/gen/proto/p2pstream/v1/management_pb";

export interface ManagementTlsFleetSummary {
  enabled: number;
  participants: number;
  readyParticipants: number;
  attention: number;
  rolloutPercent: number;
}

export function summarizeManagementTlsFleet(agents: readonly ManagementTlsAgentRollout[]): ManagementTlsFleetSummary {
  const enabled = agents.filter((agent) => agent.enabled).length;
  const participants = agents.filter((agent) => agent.includedInRollout).length;
  const readyParticipants = agents.filter((agent) => agent.includedInRollout && agent.state === ManagementTlsAgentRolloutState.READY).length;
  const attention = agents.filter((agent) => agent.needsTrustAttention).length;
  return {
    enabled,
    participants,
    readyParticipants,
    attention,
    rolloutPercent: participants === 0 ? 100 : Math.round((readyParticipants / participants) * 100),
  };
}

export function managementTlsAgentDetail(agent: ManagementTlsAgentRollout, phase: ManagementTlsRotationPhase): string {
  if (!agent.enabled && agent.needsTrustAttention) return "Excluded from gates, but trust is stale. Repair or reinstall before enabling.";
  if (!agent.enabled) return "Excluded from rollout gates; confirmed trust remains current.";
  if (agent.state === ManagementTlsAgentRolloutState.INCOMPATIBLE && phase === ManagementTlsRotationPhase.CLEANING_UP) return "Not a cleanup participant because this version could not install managed trust.";
  if (agent.state === ManagementTlsAgentRolloutState.INCOMPATIBLE) return "Upgrade by rerunning the full agent install command.";
  if (!agent.connected && agent.needsTrustAttention) return "Offline with stale or unconfirmed trust. Use repair if it cannot reconnect.";
  if (!agent.connected) return "Offline, with the expected durable trust previously confirmed.";
  return `Installed generation ${agent.installedGeneration.toString()}`;
}

export function managementCertificateExpiryWarning(notAfterUnixMillis: bigint, nowUnixMillis = Date.now()): string {
  if (notAfterUnixMillis === 0n) return "";
  const days = Math.ceil((Number(notAfterUnixMillis) - nowUnixMillis) / 86_400_000);
  if (days <= 0) return "The active management certificate has expired. Install a valid certificate immediately.";
  if (days <= 30) return `The active management certificate expires in ${days} day${days === 1 ? "" : "s"}. Stage a replacement now.`;
  return "";
}

export interface ManagementCertificateExpiryStatus {
  label: string;
  type: "default" | "success" | "warning" | "error";
}

export function managementCertificateExpiryStatus(notAfterUnixMillis: bigint, nowUnixMillis = Date.now()): ManagementCertificateExpiryStatus {
  if (notAfterUnixMillis === 0n) return { label: "Expiry unknown", type: "default" };
  const days = Math.ceil((Number(notAfterUnixMillis) - nowUnixMillis) / 86_400_000);
  if (days <= 0) return { label: "Expired", type: "error" };
  const label = `${days.toLocaleString("en-US")} day${days === 1 ? "" : "s"} left`;
  return { label, type: days <= 30 ? "warning" : "success" };
}

export const managementTlsRotationSteps = ["Stage", "Distribute trust", "Activate", "Retire old trust"] as const;

/** Returns the 1-based current step for the forward rotation path, or 0 when no forward rotation is in progress. */
export function managementTlsRotationStep(phase: ManagementTlsRotationPhase): number {
  if (phase === ManagementTlsRotationPhase.DISTRIBUTING) return 2;
  if (phase === ManagementTlsRotationPhase.ACTIVE) return 3;
  if (phase === ManagementTlsRotationPhase.RETIRING) return 4;
  return 0;
}
