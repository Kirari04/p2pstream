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
    ...installTLSParts(input.tls),
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
  if (!tls?.managementCAFile && !tls?.agentTLSCertFile && !tls?.agentTLSKeyFile) return "";
  return `    volumes:
      - /etc/p2pstream:/etc/p2pstream:ro`;
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
