export type AgentSetupTLSConfig = {
  enabled?: boolean;
  managementCAFile?: string;
  managementCAPEMBase64?: string;
  agentTLSCertFile?: string;
  agentTLSKeyFile?: string;
  allowInsecureManagement?: boolean;
};

export type AgentSetupSnippetInput = {
  managementUrl: string;
  agentId: string;
  agentToken: string;
  updaterEnrollmentToken?: string;
  agentUpdateRootBase64?: string;
  agentUpdateAuthorityPublicKeyBase64?: string;
  agentUpdateAuthorityKeyId?: string;
  agentUpdateAuthorityEpoch?: bigint;
  enableManagedUpdates?: boolean;
  repository?: string;
  version?: string;
  scriptRef?: string;
  dockerImage?: string;
  installerPath?: string;
  agentBinaryPath?: string;
  allowTargets?: string[];
  allowAnyTarget?: boolean;
  tls?: AgentSetupTLSConfig;
};

export type ManagedUpdaterBootstrapSnippetInput = {
  managementUrl: string;
  agentId: string;
  updaterEnrollmentToken: string;
  agentUpdateRootBase64: string;
  agentUpdateAuthorityPublicKeyBase64: string;
  agentUpdateAuthorityKeyId: string;
  agentUpdateAuthorityEpoch: bigint;
	currentTunnelVersion: string;
	currentTunnelCommit: string;
  repository?: string;
  version?: string;
  scriptRef?: string;
  installerPath?: string;
  agentBinaryPath?: string;
};

export const FALLBACK_RELEASE_REPOSITORY = "Kirari04/p2pstream";
const RELEASE_REPOSITORY_PATTERN = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;
const RELEASE_VERSION_PATTERN = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;
const SCRIPT_REF_PATTERN = /^(main|[A-Fa-f0-9]{7,40})$/;
const LOCAL_PATH_PATTERN = /^\/[^\r\n\0]+$/;
export const DEFAULT_LOCAL_INSTALLER_PATH = "/path/to/p2pstream-install-agent.sh";
export const DEFAULT_LOCAL_AGENT_BINARY_PATH = "/path/to/p2pstream-agent-vX.Y.Z-linux-ARCH";
export const DEFAULT_LOCAL_UNINSTALLER_PATH = "/path/to/p2pstream-uninstall-agent.sh";

export function normalizeManagementUrl(value: string): string {
  return value.trim().replace(/\/+$/, "");
}

export function normalizeRepository(value: string | undefined): string {
  const trimmed = (value ?? "").trim().replace(/^https:\/\/github\.com\//i, "").replace(/^git@github\.com:/i, "").replace(/\.git$/i, "");
  const repository = trimmed || FALLBACK_RELEASE_REPOSITORY;
  if (!isValidRepository(repository)) {
    throw new Error("GitHub repository must use owner/repo with letters, numbers, dots, underscores, or hyphens.");
  }
  return repository;
}

export function isValidRepository(value: string | undefined): boolean {
  return RELEASE_REPOSITORY_PATTERN.test((value ?? "").trim());
}

export function normalizeReleaseVersion(value: string | undefined): string {
  const version = singleLine(value ?? "").trim() || "latest";
  if (version === "latest" || isValidReleaseVersion(version)) {
    return version;
  }
  throw new Error("Release version must be latest or an exact SemVer tag.");
}

export function isValidScriptRef(value: string | undefined): boolean {
  const ref = singleLine(value ?? "").trim();
  return SCRIPT_REF_PATTERN.test(ref) || isValidReleaseVersion(ref);
}

export function scriptRefForVersion(version: string | undefined): string {
  const normalized = normalizeReleaseVersion(version);
  return normalized === "latest" ? "main" : normalized;
}

export function normalizeScriptRef(value: string | undefined, version: string | undefined): string {
  const scriptRef = singleLine(value ?? "").trim() || scriptRefForVersion(version);
  if (!isValidScriptRef(scriptRef)) {
    throw new Error("Installer script ref must be main, an exact SemVer tag, or a commit SHA.");
  }
  return scriptRef;
}

export function dockerImageForRepository(repository: string | undefined, version?: string): string {
  const imageTag = normalizeReleaseVersion(version);
  return `ghcr.io/${normalizeRepository(repository).toLowerCase()}:${imageTag}`;
}

export function linuxInstallSnippet(input: AgentSetupSnippetInput): string {
  const repository = normalizeRepository(input.repository);
  const version = normalizeReleaseVersion(input.version);
  requirePinnedLinuxVersion(version);
  const installerPath = normalizeLocalPath(input.installerPath, DEFAULT_LOCAL_INSTALLER_PATH, "Installer");
  const agentBinaryPath = normalizeLocalPath(input.agentBinaryPath, DEFAULT_LOCAL_AGENT_BINARY_PATH, "Agent binary");
  const parts = [
    `MANAGEMENT_URL=${shellQuote(normalizeManagementUrl(input.managementUrl))}`,
    ...installTLSParts(input.tls),
    `AGENT_ID=${shellQuote(input.agentId)}`,
    ...managedUpdateInstallParts(input),
    ...shellAgentDestinationPolicyParts(input),
    `P2PSTREAM_REPOSITORY=${shellQuote(repository)}`,
    `P2PSTREAM_VERSION=${shellQuote(version)}`,
    `P2PSTREAM_AGENT_BINARY_FILE=${shellQuote(agentBinaryPath)}`,
  ];
  const secrets: PromptedInstallerSecret[] = [
    { prompt: "Agent token", inputVariable: "P2PSTREAM_AGENT_TOKEN_INPUT", environmentVariable: "AGENT_TOKEN" },
  ];
  if (input.enableManagedUpdates) {
    secrets.push({ prompt: "Updater enrollment token", inputVariable: "P2PSTREAM_UPDATER_TOKEN_INPUT", environmentVariable: "P2PSTREAM_UPDATER_ENROLLMENT_TOKEN" });
  }
  return promptedInstallerCommand(parts, installerPath, secrets);
}

function managedUpdateInstallParts(input: AgentSetupSnippetInput): string[] {
  const enrollmentToken = singleLine(input.updaterEnrollmentToken ?? "").trim();
  const rootBase64 = singleLine(input.agentUpdateRootBase64 ?? "").trim();
  const authorityPublicKey = singleLine(input.agentUpdateAuthorityPublicKeyBase64 ?? "").trim();
  const authorityKeyId = singleLine(input.agentUpdateAuthorityKeyId ?? "").trim();
  const authorityEpoch = input.agentUpdateAuthorityEpoch ?? 0n;
  if (!input.enableManagedUpdates) return [];
  if (!enrollmentToken || !rootBase64 || !authorityPublicKey || !/^[0-9a-f]{64}$/.test(authorityKeyId) || authorityEpoch <= 0n) {
    throw new Error("Managed updates require the one-time enrollment token, pinned trust root, and pinned management authority.");
  }
  return [
    "P2PSTREAM_ENABLE_MANAGED_UPDATES=true",
    `P2PSTREAM_AGENT_UPDATE_CHANNEL=${shellQuote(releaseChannelForVersion(normalizeReleaseVersion(input.version)))}`,
    `P2PSTREAM_AGENT_UPDATE_ROOT_BASE64=${shellQuote(rootBase64)}`,
    `P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64=${shellQuote(authorityPublicKey)}`,
    `P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID=${shellQuote(authorityKeyId)}`,
    `P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH=${shellQuote(authorityEpoch.toString())}`,
  ];
}

export function linuxUninstallSnippet(input: Pick<AgentSetupSnippetInput, "installerPath" | "repository">): string {
  const path = normalizeLocalPath(input.installerPath, DEFAULT_LOCAL_UNINSTALLER_PATH, "Uninstaller");
  return `sudo env P2PSTREAM_UNINSTALL_CONFIRM=full-purge bash ${shellQuote(path)}`;
}

export function linuxManagedUpdaterBootstrapSnippet(input: ManagedUpdaterBootstrapSnippetInput): string {
  const repository = normalizeRepository(input.repository);
  const version = normalizeReleaseVersion(input.version);
  requirePinnedLinuxVersion(version);
  const installerPath = normalizeLocalPath(input.installerPath, DEFAULT_LOCAL_INSTALLER_PATH, "Installer");
  const agentBinaryPath = normalizeLocalPath(input.agentBinaryPath, DEFAULT_LOCAL_AGENT_BINARY_PATH, "Agent binary");
  const enrollmentToken = singleLine(input.updaterEnrollmentToken).trim();
  const rootBase64 = singleLine(input.agentUpdateRootBase64).trim();
  const authorityPublicKey = singleLine(input.agentUpdateAuthorityPublicKeyBase64).trim();
  const authorityKeyId = singleLine(input.agentUpdateAuthorityKeyId).trim();
  const currentTunnelVersion = singleLine(input.currentTunnelVersion).trim();
  const currentTunnelCommit = singleLine(input.currentTunnelCommit).trim();
  if (!enrollmentToken || !rootBase64 || !authorityPublicKey || !/^[0-9a-f]{64}$/.test(authorityKeyId) || input.agentUpdateAuthorityEpoch <= 0n) {
    throw new Error("Managed updates require the one-time enrollment token, pinned trust root, and pinned management authority.");
  }
  if (!RELEASE_VERSION_PATTERN.test(currentTunnelVersion) || !/^[0-9a-f]{40}$/.test(currentTunnelCommit)) {
    throw new Error("Updater bootstrap requires the exact live tunnel version and commit observed by management.");
  }
  const parts = [
    `MANAGEMENT_URL=${shellQuote(normalizeManagementUrl(input.managementUrl))}`,
    `AGENT_ID=${shellQuote(input.agentId)}`,
    "P2PSTREAM_ENABLE_MANAGED_UPDATES=true",
    `P2PSTREAM_AGENT_UPDATE_CHANNEL=${shellQuote(releaseChannelForVersion(version))}`,
    `P2PSTREAM_AGENT_UPDATE_ROOT_BASE64=${shellQuote(rootBase64)}`,
    `P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64=${shellQuote(authorityPublicKey)}`,
    `P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID=${shellQuote(authorityKeyId)}`,
    `P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH=${shellQuote(input.agentUpdateAuthorityEpoch.toString())}`,
    `P2PSTREAM_EXISTING_TUNNEL_VERSION=${shellQuote(currentTunnelVersion)}`,
    `P2PSTREAM_EXISTING_TUNNEL_COMMIT=${shellQuote(currentTunnelCommit)}`,
    `P2PSTREAM_REPOSITORY=${shellQuote(repository)}`,
    `P2PSTREAM_VERSION=${shellQuote(version)}`,
    `P2PSTREAM_AGENT_BINARY_FILE=${shellQuote(agentBinaryPath)}`,
  ];
  return promptedInstallerCommand(parts, installerPath, [
    { prompt: "Updater enrollment token", inputVariable: "P2PSTREAM_UPDATER_TOKEN_INPUT", environmentVariable: "P2PSTREAM_UPDATER_ENROLLMENT_TOKEN" },
  ]);
}

type PromptedInstallerSecret = {
  prompt: string;
  inputVariable: string;
  environmentVariable: string;
};

function promptedInstallerCommand(parts: string[], installerPath: string, secrets: PromptedInstallerSecret[]): string {
  const prompts = secrets.flatMap((secret) => [
    `read -r -s -p ${shellQuote(`${secret.prompt}: `)} ${secret.inputVariable}`,
    "printf '\\n' >&2",
  ]);
  const inputVariables = secrets.map((secret) => `"$${secret.inputVariable}"`).join(" ");
  const rootReads = secrets.map((secret) => `IFS= read -r ${secret.environmentVariable}`).join("; ");
  const rootExports = secrets.map((secret) => secret.environmentVariable).join(" ");
  const clearInputs = secrets.map((secret) => secret.inputVariable).join(" ");
  const rootScript = `set -eu; ${rootReads}; export ${rootExports}; exec bash "$1"`;
  return `{ ${prompts.join("; ")}; printf '%s\\n' ${inputVariables}; unset ${clearInputs}; } | sudo env ${parts.join(" ")} bash -c ${shellQuote(rootScript)} p2pstream-installer ${shellQuote(installerPath)}`;
}

function requirePinnedLinuxVersion(version: string): void {
  if (!isValidReleaseVersion(version)) {
    throw new Error("Linux installation requires an exact SemVer release or prerelease and locally pinned files.");
  }
}

function releaseChannelForVersion(version: string): "stable" | "staging" {
  return version.includes("-") ? "staging" : "stable";
}

function isValidReleaseVersion(version: string): boolean {
  if (version.length > 96) return false;
  const match = RELEASE_VERSION_PATTERN.exec(version);
  if (!match) return false;
  const prerelease = version.split("-", 2)[1];
  return !prerelease?.split(".").some((identifier) => /^\d+$/.test(identifier) && identifier.length > 1 && identifier.startsWith("0"));
}

function normalizeLocalPath(value: string | undefined, fallback: string, label: string): string {
  const result = singleLine(value ?? "").trim() || fallback;
  if (!LOCAL_PATH_PATTERN.test(result) || result.includes("//") || result.split("/").some((part) => part === "." || part === "..")) {
    throw new Error(`${label} path must be a clean absolute local path.`);
  }
  return result;
}

export function dockerComposeSnippet(input: AgentSetupSnippetInput): string {
  const version = normalizeReleaseVersion(input.version);
  const image = input.dockerImage?.trim() || dockerImageForRepository(input.repository, version);
  return `services:
  p2pstream-agent:
    image: ${yamlQuote(image)}
    command: ["/app/p2pstream", "agent"]
    environment:
      MANAGEMENT_URL: ${yamlQuote(normalizeManagementUrl(input.managementUrl))}
${dockerTLSLines(input.tls)}
      MANAGEMENT_TRUST_FILE: "/data/management-ca.pem"
      AGENT_ID: ${yamlQuote(input.agentId)}
      AGENT_TOKEN: ${yamlQuote(input.agentToken)}
${dockerAgentDestinationPolicyLine(input)}
${dockerTLSVolumes(input.tls)}
    restart: unless-stopped
volumes:
  p2pstream-agent-state:`;
}

export function cliSnippet(input: AgentSetupSnippetInput): string {
  const parts = [
    `MANAGEMENT_URL=${shellQuote(normalizeManagementUrl(input.managementUrl))}`,
    ...cliTLSParts(input.tls),
    `AGENT_ID=${shellQuote(input.agentId)}`,
    `AGENT_TOKEN=${shellQuote(input.agentToken)}`,
    ...shellAgentDestinationPolicyParts(input),
  ];
  return `${parts.join(" ")} p2pstream agent`;
}

function normalizedAllowTargets(values: string[] | undefined): string {
  const normalized = (values ?? [])
    .map((value) => singleLine(value).trim())
    .filter(Boolean);
  return normalized.join(",");
}

function normalizedAgentDestinationPolicy(input: AgentSetupSnippetInput): { allowTargets: string; allowAnyTarget: boolean } {
  const allowTargets = normalizedAllowTargets(input.allowTargets);
  const allowAnyTarget = Boolean(input.allowAnyTarget);
  if (allowTargets && allowAnyTarget) {
    throw new Error("Allow any target cannot be combined with a destination allowlist.");
  }
  return { allowTargets, allowAnyTarget };
}

function shellAgentDestinationPolicyParts(input: AgentSetupSnippetInput): string[] {
  const policy = normalizedAgentDestinationPolicy(input);
  if (policy.allowAnyTarget) return ["AGENT_ALLOW_ANY_TARGET=true"];
  if (policy.allowTargets) return [`AGENT_ALLOW_TARGETS=${shellQuote(policy.allowTargets)}`];
  return [];
}

function dockerAgentDestinationPolicyLine(input: AgentSetupSnippetInput): string {
  const policy = normalizedAgentDestinationPolicy(input);
  if (policy.allowAnyTarget) return `      AGENT_ALLOW_ANY_TARGET: "true"`;
  if (policy.allowTargets) return `      AGENT_ALLOW_TARGETS: ${yamlQuote(policy.allowTargets)}`;
  return "";
}

export function shellQuote(value: string): string {
  const clean = singleLine(value);
  if (clean === "") return "''";
  return "'" + clean.replace(/'/g, "'\\''") + "'";
}

export function envQuote(value: string): string {
  return `"${singleLine(value).replace(/\\/g, "\\\\").replace(/"/g, "\\\"")}"`;
}

export function yamlQuote(value: string): string {
  return JSON.stringify(singleLine(value));
}

function singleLine(value: string): string {
  return value.replace(/\r?\n/g, "");
}

function hasTLS(tls: AgentSetupTLSConfig | undefined): boolean {
  return Boolean(tls?.enabled || tls?.allowInsecureManagement);
}

function dockerTLSLines(tls: AgentSetupTLSConfig | undefined): string {
  if (!hasTLS(tls)) return "";
  const lines: string[] = [];
  if (tls?.managementCAPEMBase64) {
    lines.push(`      MANAGEMENT_CA_PEM_BASE64: ${yamlQuote(tls.managementCAPEMBase64)}`);
  } else if (tls?.managementCAFile) {
    lines.push(`      MANAGEMENT_CA_FILE: ${yamlQuote(tls.managementCAFile)}`);
  }
  if (tls?.agentTLSCertFile) {
    lines.push(`      AGENT_TLS_CERT_FILE: ${yamlQuote(tls.agentTLSCertFile)}`);
  }
  if (tls?.agentTLSKeyFile) {
    lines.push(`      AGENT_TLS_KEY_FILE: ${yamlQuote(tls.agentTLSKeyFile)}`);
  }
  if (tls?.allowInsecureManagement) {
    lines.push(`      AGENT_ALLOW_INSECURE_MANAGEMENT: "true"`);
  }
  return lines.join("\n");
}

function dockerTLSVolumes(tls: AgentSetupTLSConfig | undefined): string {
  const lines = ["    volumes:", "      - p2pstream-agent-state:/data"];
  if (tls?.managementCAFile || tls?.agentTLSCertFile || tls?.agentTLSKeyFile) {
    lines.push("      - /etc/p2pstream:/etc/p2pstream:ro");
  }
  return lines.join("\n");
}

function installTLSParts(tls: AgentSetupTLSConfig | undefined): string[] {
  if (!hasTLS(tls)) return [];
  const parts: string[] = [];
  if (tls?.managementCAPEMBase64) {
    parts.push(`MANAGEMENT_CA_PEM_BASE64=${shellQuote(tls.managementCAPEMBase64)}`);
  } else if (tls?.managementCAFile) {
    parts.push(`MANAGEMENT_CA_FILE=${shellQuote(tls.managementCAFile)}`);
  }
  if (tls?.agentTLSCertFile) {
    parts.push(`AGENT_TLS_CERT_FILE=${shellQuote(tls.agentTLSCertFile)}`);
  }
  if (tls?.agentTLSKeyFile) {
    parts.push(`AGENT_TLS_KEY_FILE=${shellQuote(tls.agentTLSKeyFile)}`);
  }
  if (tls?.allowInsecureManagement) {
    parts.push(`AGENT_ALLOW_INSECURE_MANAGEMENT=true`);
  }
  return parts;
}

function cliTLSParts(tls: AgentSetupTLSConfig | undefined): string[] {
	const trustFile = `MANAGEMENT_TRUST_FILE='./p2pstream-agent-state/management-ca.pem'`;
	if (!hasTLS(tls)) return [trustFile];
  const parts: string[] = [];
  if (tls?.managementCAPEMBase64) {
    parts.push(`MANAGEMENT_CA_PEM_BASE64=${shellQuote(tls.managementCAPEMBase64)}`);
  } else if (tls?.managementCAFile) {
    parts.push(`MANAGEMENT_CA_FILE=${shellQuote(tls.managementCAFile)}`);
  }
  if (tls?.agentTLSCertFile) {
    parts.push(`AGENT_TLS_CERT_FILE=${shellQuote(tls.agentTLSCertFile)}`);
  }
  if (tls?.agentTLSKeyFile) {
    parts.push(`AGENT_TLS_KEY_FILE=${shellQuote(tls.agentTLSKeyFile)}`);
  }
  if (tls?.allowInsecureManagement) {
    parts.push(`AGENT_ALLOW_INSECURE_MANAGEMENT=true`);
  }
	parts.push(trustFile);
  return parts;
}
