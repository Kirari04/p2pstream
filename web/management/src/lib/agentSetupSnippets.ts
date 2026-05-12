export type AgentSetupTLSConfig = {
  enabled?: boolean;
  managementCAFile?: string;
  agentTLSCertFile?: string;
  agentTLSKeyFile?: string;
};

export type AgentSetupSnippetInput = {
  managementUrl: string;
  agentId: string;
  agentToken: string;
  repository?: string;
  dockerImage?: string;
  tls?: AgentSetupTLSConfig;
};

export const FALLBACK_RELEASE_REPOSITORY = "Kirari04/p2pstream";

export function normalizeManagementUrl(value: string): string {
  return value.trim().replace(/\/+$/, "");
}

export function normalizeRepository(value: string | undefined): string {
  const trimmed = (value ?? "").trim().replace(/^https:\/\/github\.com\//i, "").replace(/^git@github\.com:/i, "").replace(/\.git$/i, "");
  return trimmed || FALLBACK_RELEASE_REPOSITORY;
}

export function dockerImageForRepository(repository: string | undefined): string {
  return `ghcr.io/${normalizeRepository(repository).toLowerCase()}:latest`;
}

export function linuxInstallSnippet(input: AgentSetupSnippetInput): string {
  const repository = normalizeRepository(input.repository);
  const parts = [
    `MANAGEMENT_URL=${shellQuote(normalizeManagementUrl(input.managementUrl))}`,
    ...cliTLSParts(input.tls),
    `AGENT_ID=${shellQuote(input.agentId)}`,
    `AGENT_TOKEN=${shellQuote(input.agentToken)}`,
    `P2PSTREAM_REPOSITORY=${shellQuote(repository)}`,
  ];
  return `curl -fsSL https://raw.githubusercontent.com/${repository}/main/scripts/install-agent.sh | sudo env ${parts.join(" ")} bash`;
}

export function dockerComposeSnippet(input: AgentSetupSnippetInput): string {
  const image = input.dockerImage?.trim() || dockerImageForRepository(input.repository);
  return `services:
  p2pstream-agent:
    image: ${yamlQuote(image)}
    command: ["/app/p2pstream", "agent"]
    environment:
      MANAGEMENT_URL: ${yamlQuote(normalizeManagementUrl(input.managementUrl))}
${dockerTLSLines(input.tls)}
      AGENT_ID: ${yamlQuote(input.agentId)}
      AGENT_TOKEN: ${yamlQuote(input.agentToken)}
${dockerTLSVolumes(input.tls)}
    restart: unless-stopped`;
}

export function cliSnippet(input: AgentSetupSnippetInput): string {
  const parts = [
    `MANAGEMENT_URL=${shellQuote(normalizeManagementUrl(input.managementUrl))}`,
    ...cliTLSParts(input.tls),
    `AGENT_ID=${shellQuote(input.agentId)}`,
    `AGENT_TOKEN=${shellQuote(input.agentToken)}`,
  ];
  return `${parts.join(" ")} p2pstream agent`;
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
  return Boolean(tls?.enabled);
}

function dockerTLSLines(tls: AgentSetupTLSConfig | undefined): string {
  if (!hasTLS(tls)) return "";
  return `      MANAGEMENT_CA_FILE: ${yamlQuote(tls?.managementCAFile ?? "")}
      AGENT_TLS_CERT_FILE: ${yamlQuote(tls?.agentTLSCertFile ?? "")}
      AGENT_TLS_KEY_FILE: ${yamlQuote(tls?.agentTLSKeyFile ?? "")}`;
}

function dockerTLSVolumes(tls: AgentSetupTLSConfig | undefined): string {
  if (!hasTLS(tls)) return "";
  return `    volumes:
      - /etc/p2pstream:/etc/p2pstream:ro`;
}

function cliTLSParts(tls: AgentSetupTLSConfig | undefined): string[] {
  if (!hasTLS(tls)) return [];
  return [
    `MANAGEMENT_CA_FILE=${shellQuote(tls?.managementCAFile ?? "")}`,
    `AGENT_TLS_CERT_FILE=${shellQuote(tls?.agentTLSCertFile ?? "")}`,
    `AGENT_TLS_KEY_FILE=${shellQuote(tls?.agentTLSKeyFile ?? "")}`,
  ];
}
