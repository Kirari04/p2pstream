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
  reuseExistingToken?: boolean;
  agentEnvironmentPath?: string;
  updaterEnrollmentToken?: string;
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
// Empty overrides select verified downloads from the chosen GitHub release.
export const DEFAULT_LOCAL_INSTALLER_PATH = "";
export const DEFAULT_LOCAL_AGENT_BINARY_PATH = "";
export const DEFAULT_LOCAL_UNINSTALLER_PATH = "";

export function normalizeManagementUrl(value: string): string {
  return value.trim().replace(/\/+$/, "");
}

export function agentSetupManagementUrl(configuredUrl: string | undefined, environmentUrl: string | undefined, browserOrigin: string): string {
  const configured = configuredUrl?.trim();
  const environment = environmentUrl?.trim();
  const selectedUrl = environment && (!configured || isLocalManagementUrl(configured)) ? environment : configured;
  if (selectedUrl) return normalizeManagementUrl(selectedUrl);
  const url = new URL(browserOrigin);
  if (url.port === "5173") url.port = "8081";
  url.protocol = "https:";
  return normalizeManagementUrl(url.toString());
}

export function isLocalManagementUrl(value: string): boolean {
  try {
    const host = new URL(value).hostname.toLowerCase().replace(/\.$/, "");
    return host === "localhost" || host.endsWith(".localhost") || host === "0.0.0.0" || host === "[::]" || host === "[::1]" || host.startsWith("127.");
  } catch { return true; }
}

export function agentSetupReleaseVersion(runningVersion?: string, configuredVersion?: string): string {
  return [runningVersion, configuredVersion].map((value) => value?.trim() ?? "").find(isValidReleaseVersion) || "latest";
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
  if (input.enableManagedUpdates) requirePinnedLinuxVersion(version);
  const installerPath = normalizeLocalPath(input.installerPath, DEFAULT_LOCAL_INSTALLER_PATH, "Installer");
  const agentBinaryPath = normalizeLocalPath(input.agentBinaryPath, DEFAULT_LOCAL_AGENT_BINARY_PATH, "Agent binary");
  const parts = [
    `MANAGEMENT_URL=${shellQuote(normalizeManagementUrl(input.managementUrl))}`,
    ...installTLSParts(input.tls),
    `AGENT_ID=${shellQuote(input.agentId)}`,
    ...(input.reuseExistingToken ? [] : [`AGENT_TOKEN=${shellQuote(input.agentToken)}`]),
    ...managedUpdateInstallParts(input),
    ...shellAgentDestinationPolicyParts(input),
    `P2PSTREAM_REPOSITORY=${shellQuote(repository)}`,
    `P2PSTREAM_VERSION=${shellQuote(version)}`,
  ];
  return releaseInstallerCommand(parts, repository, version, "install-agent.sh", installerPath, agentBinaryPath, input.reuseExistingToken ? normalizeLocalPath(input.agentEnvironmentPath, "/etc/p2pstream/agent.env", "Agent environment") : "");
}

export function linuxRotateAgentTokenSnippet(input: Pick<AgentSetupSnippetInput, "agentId" | "agentToken" | "agentEnvironmentPath">): string {
  const environmentPath = normalizeLocalPath(input.agentEnvironmentPath, "/etc/p2pstream/agent.env", "Agent environment");
  const parts = [`AGENT_ID=${shellQuote(input.agentId)}`, `AGENT_TOKEN=${shellQuote(input.agentToken)}`];
  // systemd parses its own EnvironmentFile format. No shell sourcing/eval and no
  // retrieval of the current tunnel secret by the management server or browser.
  const script = [
    "set -euo pipefail",
    `environment_file=${shellQuote(environmentPath)}`,
    existingEnvironmentCheck,
    existingAgentCommand(["AGENT_TOKEN"], `bash -c ${shellQuote(rotateTokenScript)} p2pstream-token "$environment_file"`),
  ].join("; ");
  return `sudo env ${parts.join(" ")} bash -c ${shellQuote(script)}`;
}

const existingEnvironmentCheck = '[ -f "$environment_file" ] && [ ! -L "$environment_file" ] || { echo "Existing agent environment is missing or unsafe; use new-agent setup on a fresh host" >&2; exit 1; }';
const rotateTokenScript = [
  'set -euo pipefail; environment_file=$1',
  existingEnvironmentCheck,
  'tmp=$(mktemp "${environment_file}.XXXXXX")',
  'trap \'rm -f -- "$tmp"\' EXIT',
  'cp --preserve=mode,ownership -- "$environment_file" "$tmp"',
  'token=${AGENT_TOKEN//\\\\/\\\\\\\\}; token=${token//\\\"/\\\\\\\"}',
  'printf \'\\n\\nAGENT_TOKEN="%s"\\n\' "$token" >> "$tmp"',
  'mv -f -- "$tmp" "$environment_file"',
  'systemctl restart p2pstream-agent',
].join("; ");

function existingAgentCommand(environmentNames: string[], command: string): string {
  const clearDestinationPolicy = environmentNames.some((name) => name === "AGENT_ALLOW_TARGETS" || name === "AGENT_ALLOW_ANY_TARGET") ? "unset AGENT_ALLOW_TARGETS AGENT_ALLOW_ANY_TARGET; " : "";
  const check = 'set -euo pipefail; [ "${AGENT_ID:-}" = "$1" ] && [ -n "${AGENT_TOKEN:-}" ] || { echo "This host does not contain the selected agent identity and token" >&2; exit 1; }; shift; ' + clearDestinationPolicy + 'exec env "$@"';
  const overrides = environmentNames.map((name) => `${name}="$${name}"`).join(" ");
  // Escape dollars at the systemd ExecStart boundary (also supported by older
  // systemd releases), so shell code and literal credential values arrive intact.
  const escapeArguments = 'for index in "${!service_args[@]}"; do service_args[$index]=${service_args[$index]//\\$/\\$\\$}; done';
  return `service_args=(bash -c ${shellQuote(check)} p2pstream-existing "$AGENT_ID" ${overrides} ${command}); ${escapeArguments}; systemd-run --quiet --wait --pipe --collect --property=Type=exec --property="EnvironmentFile=$environment_file" "${'$'}{service_args[@]}"`;
}

function managedUpdateInstallParts(input: AgentSetupSnippetInput): string[] {
  const enrollmentToken = singleLine(input.updaterEnrollmentToken ?? "").trim();
  const authorityPublicKey = singleLine(input.agentUpdateAuthorityPublicKeyBase64 ?? "").trim();
  const authorityKeyId = singleLine(input.agentUpdateAuthorityKeyId ?? "").trim();
  const authorityEpoch = input.agentUpdateAuthorityEpoch ?? 0n;
  if (!input.enableManagedUpdates) return [];
  if (!enrollmentToken || !authorityPublicKey || !/^[0-9a-f]{64}$/.test(authorityKeyId) || authorityEpoch <= 0n) {
    throw new Error("Managed updates require the one-time enrollment token and pinned management authority.");
  }
  return [
    "P2PSTREAM_ENABLE_MANAGED_UPDATES=true",
    `P2PSTREAM_UPDATER_ENROLLMENT_TOKEN=${shellQuote(enrollmentToken)}`,
    `P2PSTREAM_AGENT_UPDATE_CHANNEL=${shellQuote(releaseChannelForVersion(normalizeReleaseVersion(input.version)))}`,
    `P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64=${shellQuote(authorityPublicKey)}`,
    `P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID=${shellQuote(authorityKeyId)}`,
    `P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH=${shellQuote(authorityEpoch.toString())}`,
  ];
}

export function linuxUninstallSnippet(input: Pick<AgentSetupSnippetInput, "installerPath" | "repository" | "version">): string {
  const path = normalizeLocalPath(input.installerPath, DEFAULT_LOCAL_UNINSTALLER_PATH, "Uninstaller");
  return releaseInstallerCommand(["P2PSTREAM_UNINSTALL_CONFIRM=full-purge"], normalizeRepository(input.repository), normalizeReleaseVersion(input.version), "uninstall-agent.sh", path);
}

export function linuxManagedUpdaterBootstrapSnippet(input: ManagedUpdaterBootstrapSnippetInput): string {
  const repository = normalizeRepository(input.repository);
  const version = normalizeReleaseVersion(input.version);
  requirePinnedLinuxVersion(version);
  const installerPath = normalizeLocalPath(input.installerPath, DEFAULT_LOCAL_INSTALLER_PATH, "Installer");
  const agentBinaryPath = normalizeLocalPath(input.agentBinaryPath, DEFAULT_LOCAL_AGENT_BINARY_PATH, "Agent binary");
  const enrollmentToken = singleLine(input.updaterEnrollmentToken).trim();
  const authorityPublicKey = singleLine(input.agentUpdateAuthorityPublicKeyBase64).trim();
  const authorityKeyId = singleLine(input.agentUpdateAuthorityKeyId).trim();
  const currentTunnelVersion = singleLine(input.currentTunnelVersion).trim();
  const currentTunnelCommit = singleLine(input.currentTunnelCommit).trim();
  if (!enrollmentToken || !authorityPublicKey || !/^[0-9a-f]{64}$/.test(authorityKeyId) || input.agentUpdateAuthorityEpoch <= 0n) {
    throw new Error("Managed updates require the one-time enrollment token and pinned management authority.");
  }
  if (!isValidReleaseVersion(currentTunnelVersion) || !/^[0-9a-f]{40}$/.test(currentTunnelCommit)) {
    throw new Error("Updater bootstrap requires the exact live tunnel version and commit observed by management.");
  }
  const parts = [
    `MANAGEMENT_URL=${shellQuote(normalizeManagementUrl(input.managementUrl))}`,
    `AGENT_ID=${shellQuote(input.agentId)}`,
    "P2PSTREAM_ENABLE_MANAGED_UPDATES=true",
    `P2PSTREAM_UPDATER_ENROLLMENT_TOKEN=${shellQuote(enrollmentToken)}`,
    `P2PSTREAM_AGENT_UPDATE_CHANNEL=${shellQuote(releaseChannelForVersion(version))}`,
    `P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64=${shellQuote(authorityPublicKey)}`,
    `P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID=${shellQuote(authorityKeyId)}`,
    `P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH=${shellQuote(input.agentUpdateAuthorityEpoch.toString())}`,
    `P2PSTREAM_EXISTING_TUNNEL_VERSION=${shellQuote(currentTunnelVersion)}`,
    `P2PSTREAM_EXISTING_TUNNEL_COMMIT=${shellQuote(currentTunnelCommit)}`,
    `P2PSTREAM_REPOSITORY=${shellQuote(repository)}`,
    `P2PSTREAM_VERSION=${shellQuote(version)}`,
  ];
  return releaseInstallerCommand(parts, repository, version, "install-agent.sh", installerPath, agentBinaryPath);
}

function releaseInstallerCommand(parts: string[], repository: string, version: string, scriptName: "install-agent.sh" | "uninstall-agent.sh", installerPath: string, agentBinaryPath = "", existingEnvironmentPath = ""): string {
  const needsBinary = scriptName === "install-agent.sh";
  if (!existingEnvironmentPath && installerPath && (!needsBinary || (agentBinaryPath && version !== "latest"))) {
    const binary = needsBinary ? ` P2PSTREAM_AGENT_BINARY_FILE=${shellQuote(agentBinaryPath)}` : "";
    return `sudo env ${parts.join(" ")}${binary} bash ${shellQuote(installerPath)}`;
  }
  // Keep the bootstrap self-contained so it also works with already published
  // releases. Extract only named, checksummed assets; never execute a curl pipe.
  const script = [
    "set -euo pipefail",
    'repository=$1; version=$2; installer=$3; binary=$4; script_name=$5',
    ...(existingEnvironmentPath ? [`environment_file=${shellQuote(existingEnvironmentPath)}`, existingEnvironmentCheck] : []),
    'tmp=$(mktemp -d)',
    'trap \'rm -rf -- "$tmp"\' EXIT',
    'download() { curl --proto "=https" --proto-redir "=https" --fail --location --silent --show-error --retry 3 --connect-timeout 15 --max-time 300 --max-filesize 536870912 "$@"; }',
    'if [ "$version" = latest ]; then resolved=$(download --output /dev/null --write-out "%{url_effective}" "https://github.com/$repository/releases/latest"); prefix="https://github.com/$repository/releases/tag/"; [[ "$resolved" = "$prefix"* ]] || { echo "Cannot resolve latest release" >&2; exit 1; }; version=${resolved#"$prefix"}; [[ "$version" =~ ^v(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\\.[0-9A-Za-z-]+)*)?$ ]] || { echo "Invalid release version" >&2; exit 1; }; fi',
    'export P2PSTREAM_VERSION="$version"',
    'base="https://github.com/$repository/releases/download/$version"',
    'if [ -z "$installer" ] || { [ "$script_name" = install-agent.sh ] && [ -z "$binary" ]; }; then download --output "$tmp/checksums.txt" "$base/checksums.txt"; fi',
    'digest() { awk -v name="$1" \'$2 == name || $2 == "*" name { print $1 }\' "$tmp/checksums.txt"; }',
    'fetch_verified() { local expected; expected=$(digest "$1"); [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || { echo "Missing or ambiguous checksum for $1" >&2; return 1; }; download --output "$tmp/$1" "$base/$1"; printf "%s  %s\\n" "$expected" "$tmp/$1" | sha256sum --check --status || { echo "Checksum mismatch for $1" >&2; return 1; }; }',
    'if [ -z "$installer" ]; then source="p2pstream_${version}_source.tar.gz"; fetch_verified "$source"; installer="$tmp/$script_name"; tar -xOf "$tmp/$source" "p2pstream-$version/scripts/$script_name" > "$installer"; fi',
    ...(needsBinary ? [
      'if [ -z "$binary" ]; then [ "$(uname -s)" = Linux ] || { echo "Linux is required" >&2; exit 1; }; case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo "Unsupported agent architecture" >&2; exit 1 ;; esac; asset="p2pstream_${version}_linux_$arch"; if [ -n "$(digest "$asset")" ]; then fetch_verified "$asset"; binary="$tmp/$asset"; else asset="$asset.tar.gz"; fetch_verified "$asset"; binary="$tmp/p2pstream"; tar -xOf "$tmp/$asset" ./p2pstream > "$binary"; fi; fi',
      'export P2PSTREAM_AGENT_BINARY_FILE="$binary"',
    ] : []),
    ...(existingEnvironmentPath ? [
      'if [ -e /etc/p2pstream-updater/enrolled.json ] && [ "${P2PSTREAM_ENABLE_MANAGED_UPDATES:-false}" != true ]; then echo "Repairing a managed agent requires its update authority; refresh management and try again" >&2; exit 1; fi',
      existingAgentCommand([...parts.map((part) => part.split("=")[0]!), "P2PSTREAM_AGENT_BINARY_FILE"], 'bash "$installer"'),
    ] : ['bash "$installer"']),
  ].join("; ");
  return `sudo env ${parts.join(" ")} bash -c ${shellQuote(script)} p2pstream-setup ${[repository, version, installerPath, agentBinaryPath, scriptName].map(shellQuote).join(" ")}`;
}

function requirePinnedLinuxVersion(version: string): void {
  if (!isValidReleaseVersion(version)) {
    throw new Error("Managed updates require an exact SemVer release or prerelease.");
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
  if (!result) return "";
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
