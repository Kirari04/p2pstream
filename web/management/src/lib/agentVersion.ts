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

const releaseVersionPattern = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/;

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

type ReleaseVersion = Readonly<{
  core: readonly [major: bigint, minor: bigint, patch: bigint];
  prerelease: readonly string[];
}>;

function parseReleaseVersion(value: string): ReleaseVersion | null {
  if (value.length > 96) return null;
  const match = releaseVersionPattern.exec(value);
  if (!match) return null;
  const prerelease = match[4]?.split(".") ?? [];
  if (prerelease.some((identifier) => /^\d+$/.test(identifier) && identifier.length > 1 && identifier.startsWith("0"))) return null;
  return { core: [BigInt(match[1]), BigInt(match[2]), BigInt(match[3])], prerelease };
}

function compareReleaseVersions(left: ReleaseVersion, right: ReleaseVersion): number {
  for (let index = 0; index < left.core.length; index += 1) {
    if (left.core[index] < right.core[index]) return -1;
    if (left.core[index] > right.core[index]) return 1;
  }
  if (left.prerelease.length === 0 || right.prerelease.length === 0) {
    if (left.prerelease.length === right.prerelease.length) return 0;
    return left.prerelease.length === 0 ? 1 : -1;
  }
  const count = Math.max(left.prerelease.length, right.prerelease.length);
  for (let index = 0; index < count; index += 1) {
    const leftPart = left.prerelease[index];
    const rightPart = right.prerelease[index];
    if (leftPart === undefined || rightPart === undefined) return leftPart === undefined ? -1 : 1;
    if (leftPart === rightPart) continue;
    const leftNumeric = /^\d+$/.test(leftPart);
    const rightNumeric = /^\d+$/.test(rightPart);
    if (leftNumeric && rightNumeric) return BigInt(leftPart) < BigInt(rightPart) ? -1 : 1;
    if (leftNumeric !== rightNumeric) return leftNumeric ? -1 : 1;
    return leftPart < rightPart ? -1 : 1;
  }
  return 0;
}
