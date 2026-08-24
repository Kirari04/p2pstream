export type AgentBuildState =
  | "unknown"
  | "reported"
  | "unverified"
  | "current"
  | "update_available"
  | "ahead"
  | "different";

export type AgentBuildStatus = Readonly<{
  state: AgentBuildState;
  label: string;
}>;

export type AgentBuildIdentity = Readonly<{
  agentVersion?: string;
  agentCommit?: string;
  serverVersion?: string;
  serverCommit?: string;
}>;

const releaseVersionPattern = /^v(\d+)\.(\d+)\.(\d+)$/;

export function agentBuildStatus(identity: AgentBuildIdentity): AgentBuildStatus {
  const agentVersion = identity.agentVersion?.trim() ?? "";
  const agentCommit = identity.agentCommit?.trim() ?? "";
  const serverVersion = identity.serverVersion?.trim() ?? "";
  const serverCommit = identity.serverCommit?.trim() ?? "";

  if (!agentVersion) return { state: "unknown", label: "Not reported" };
  if (!serverVersion) return { state: "reported", label: "Reported" };

  if (agentVersion === serverVersion) {
    if (agentCommit && serverCommit && agentCommit !== serverCommit) {
      return { state: "different", label: "Different build" };
    }
    if (!agentCommit && serverCommit && !parseReleaseVersion(agentVersion)) {
      return { state: "unverified", label: "Build unverified" };
    }
    return { state: "current", label: "Matches server" };
  }

  const agentRelease = parseReleaseVersion(agentVersion);
  const serverRelease = parseReleaseVersion(serverVersion);
  if (agentRelease && serverRelease) {
    const comparison = compareReleaseVersions(agentRelease, serverRelease);
    if (comparison < 0) return { state: "update_available", label: "Update available" };
    if (comparison > 0) return { state: "ahead", label: "Ahead of server" };
  }

  return { state: "different", label: "Differs from server" };
}

export function shortBuildCommit(commit: string | undefined, length = 12): string {
  const normalized = commit?.trim() ?? "";
  return Array.from(normalized).slice(0, Math.max(1, length)).join("");
}

type ReleaseVersion = readonly [major: bigint, minor: bigint, patch: bigint];

function parseReleaseVersion(value: string): ReleaseVersion | null {
  const match = releaseVersionPattern.exec(value);
  if (!match) return null;
  return [BigInt(match[1]), BigInt(match[2]), BigInt(match[3])];
}

function compareReleaseVersions(left: ReleaseVersion, right: ReleaseVersion): number {
  for (let index = 0; index < left.length; index += 1) {
    if (left[index] < right[index]) return -1;
    if (left[index] > right[index]) return 1;
  }
  return 0;
}
