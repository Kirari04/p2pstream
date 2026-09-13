import type { ManagementClient } from "@/api/managementClient";
import type { CreatedAgentSetup } from "@/types/agentSetup";

export type AgentSetupSubject = { id: bigint; publicId: string; name: string; version?: string; commit?: string };
export type AgentSetupMode = "create" | "reinstall" | "enable-updates" | "rotate";
export type ManagedAgentSetup = Pick<CreatedAgentSetup, "updaterEnrollmentToken" | "updaterEnrollmentExpiresAtUnixMillis" | "updaterPinnedRepository" | "updaterManagementAuthorityPublicKeyBase64" | "updaterManagementAuthorityKeyId" | "updaterManagementAuthorityEpoch">;

export function bytesToBase64(bytes: Uint8Array): string {
  return btoa(Array.from(bytes, (byte) => String.fromCharCode(byte)).join(""));
}

export async function prepareExistingAgentSetup(client: ManagementClient, agent: AgentSetupSubject, mode: "reinstall" | "enable-updates", isCurrent: () => boolean = () => true) {
  // Capture both methods before awaiting: a routed client must not switch the
  // token issuer halfway through preparation when the environment changes.
  const getOverview = client.getAgentUpdateOverview.bind(client);
  const generateToken = client.generateAgentUpdaterEnrollmentToken.bind(client);
  const overview = await getOverview({});
  const target = overview.trustedTargets[0];
  const existing = overview.agents.find((candidate) => candidate.agentId === agent.id);
  if (!existing || existing.agentPublicId !== agent.publicId) throw new Error("This agent no longer belongs to the selected environment. Refresh the fleet and try again.");
  if (existing.activeAssignmentId) throw new Error("Finish or cancel this agent's update campaign before changing its installation.");
  if (!target || !overview.managementAuthority) {
    const reason = overview.managementAuthorityWarning || "No trusted update release is available.";
    if (mode === "enable-updates" || existing?.updaterEnrolled) throw new Error(reason);
    return { managed: null, version: "", enrolled: false, notice: `Managed updates are unavailable: ${reason} This command will repair the agent using its existing token.` };
  }
  if (mode === "enable-updates" && (!existing?.tunnelVersion || !existing.tunnelCommit)) {
    throw new Error("Connect this agent so management can identify the running build before enabling updates.");
  }
  if (!isCurrent()) throw new Error("Setup was closed before enrollment preparation completed.");
  const response = await generateToken({ agentId: agent.id, ttlMillis: 600000n });
  const managed: ManagedAgentSetup = {
    updaterEnrollmentToken: response.token,
    updaterEnrollmentExpiresAtUnixMillis: response.expiresAtUnixMillis,
    updaterPinnedRepository: response.pinnedRepository,
    updaterManagementAuthorityPublicKeyBase64: bytesToBase64(response.managementAuthority?.publicKey ?? new Uint8Array()),
    updaterManagementAuthorityKeyId: response.managementAuthority?.keyId ?? "",
    updaterManagementAuthorityEpoch: response.managementAuthority?.epoch ?? 0n,
  };
  const enrolled = Boolean(existing.updaterEnrolled);
  const notice = enrolled && mode === "reinstall"
    ? `This host is already managed. Repair refreshes its updater and configuration but keeps the agent binary${existing.tunnelVersion ? ` at ${existing.tunnelVersion}` : ""}. To upgrade to ${target.version}, use Agents → Updates → Plan rollout.`
    : "";
  return { managed, version: target.version, enrolled, notice, liveVersion: existing.tunnelVersion, liveCommit: existing.tunnelCommit };
}
