import { describe, expect, test } from "bun:test";
import {
  cliSnippet,
  dockerComposeSnippet,
  dockerImageForRepository,
  isValidRepository,
  linuxInstallSnippet,
  linuxUninstallSnippet,
  normalizeRepository,
  shellQuote,
  yamlQuote,
} from "@/lib/agentSetupSnippets";

const baseInput = {
  managementUrl: "https://mgmt.example.test/",
  agentId: "agent-mfrggzdfmztwq2lkmmxgg33nna",
  agentToken: "token'value",
  repository: "ExampleUser/p2pstream",
};

describe("agentSetupSnippets", () => {
  test("quotes shell values safely", () => {
    expect(shellQuote("plain")).toBe("'plain'");
    expect(shellQuote("token'value")).toBe("'token'\\''value'");
    expect(shellQuote("line\nbreak")).toBe("'linebreak'");
    expect(shellQuote("")).toBe("''");
  });

  test("quotes YAML values safely", () => {
    expect(yamlQuote("token:value")).toBe("\"token:value\"");
    expect(yamlQuote("line\nbreak")).toBe("\"linebreak\"");
  });

  test("normalizes repository values", () => {
    expect(normalizeRepository("https://github.com/Owner/p2pstream.git")).toBe("Owner/p2pstream");
    expect(normalizeRepository("git@github.com:Owner/p2pstream.git")).toBe("Owner/p2pstream");
    expect(normalizeRepository("")).toBe("Kirari04/p2pstream");
  });

  test("rejects unsafe repository values before building snippets", () => {
    for (const repository of [
      "owner/repo;id",
      "owner/repo$(id)",
      "owner/repo\nid",
      "owner /repo",
      "https://evil.example/owner/repo",
    ]) {
      expect(isValidRepository(repository)).toBe(false);
      expect(() => normalizeRepository(repository)).toThrow("GitHub repository must use owner/repo");
      expect(() => linuxInstallSnippet({ ...baseInput, repository })).toThrow("GitHub repository must use owner/repo");
      expect(() => linuxUninstallSnippet({ repository })).toThrow("GitHub repository must use owner/repo");
    }
  });

  test("uses GHCR image default from repository", () => {
    expect(dockerImageForRepository("ExampleUser/p2pstream")).toBe("ghcr.io/exampleuser/p2pstream:latest");
  });

  test("builds one-line Linux installer snippet", () => {
    const snippet = linuxInstallSnippet(baseInput);

    expect(snippet).toContain("curl -fsSL https://raw.githubusercontent.com/ExampleUser/p2pstream/main/scripts/install-agent.sh");
    expect(snippet).toContain("sudo env");
    expect(snippet).toContain("MANAGEMENT_URL='https://mgmt.example.test'");
    expect(snippet).toContain("AGENT_ID='agent-mfrggzdfmztwq2lkmmxgg33nna'");
    expect(snippet).toContain("AGENT_TOKEN='token'\\''value'");
    expect(snippet).toContain("P2PSTREAM_REPOSITORY='ExampleUser/p2pstream'");
    expect(snippet).not.toContain("\n");
  });

  test("builds Linux uninstall snippet with default repository", () => {
    const snippet = linuxUninstallSnippet({ repository: "" });

    expect(snippet).toBe("curl -fsSL https://raw.githubusercontent.com/Kirari04/p2pstream/main/scripts/uninstall-agent.sh | sudo env P2PSTREAM_UNINSTALL_CONFIRM=full-purge bash");
    expect(snippet).not.toContain("\n");
  });

  test("builds Linux uninstall snippet with normalized repository and no agent secrets", () => {
    const snippet = linuxUninstallSnippet({ repository: "https://github.com/ExampleUser/p2pstream.git" });

    expect(snippet).toContain("curl -fsSL https://raw.githubusercontent.com/ExampleUser/p2pstream/main/scripts/uninstall-agent.sh");
    expect(snippet).toContain("sudo env P2PSTREAM_UNINSTALL_CONFIRM=full-purge bash");
    expect(snippet).not.toContain("AGENT_TOKEN");
    expect(snippet).not.toContain("AGENT_ID");
    expect(snippet).not.toContain("MANAGEMENT_URL");
  });

  test("adds TLS variables only when enabled", () => {
    const withoutTLS = linuxInstallSnippet(baseInput);
    const withTLS = linuxInstallSnippet({
      ...baseInput,
      tls: {
        enabled: true,
        managementCAFile: "/etc/p2pstream/ca.pem",
        agentTLSCertFile: "/etc/p2pstream/agent.crt.pem",
        agentTLSKeyFile: "/etc/p2pstream/agent.key.pem",
      },
    });

    expect(withoutTLS).not.toContain("MANAGEMENT_CA_FILE");
    expect(withTLS).toContain("MANAGEMENT_CA_FILE='/etc/p2pstream/ca.pem'");
    expect(withTLS).toContain("AGENT_TLS_CERT_FILE='/etc/p2pstream/agent.crt.pem'");
    expect(withTLS).toContain("AGENT_TLS_KEY_FILE='/etc/p2pstream/agent.key.pem'");
  });

  test("embeds management CA base64 in setup snippets", () => {
    const input = {
      ...baseInput,
      tls: {
        enabled: true,
        managementCAPEMBase64: "LS0tQ0EtLS0=",
      },
    };

    expect(linuxInstallSnippet(input)).toContain("MANAGEMENT_CA_PEM_BASE64='LS0tQ0EtLS0='");
    expect(dockerComposeSnippet(input)).toContain("MANAGEMENT_CA_PEM_BASE64: \"LS0tQ0EtLS0=\"");
    expect(cliSnippet(input)).toContain("MANAGEMENT_CA_PEM_BASE64='LS0tQ0EtLS0='");
  });

  test("adds explicit insecure management opt-in for HTTP snippets", () => {
    const input = {
      ...baseInput,
      managementUrl: "http://mgmt.example.test",
      tls: {
        allowInsecureManagement: true,
      },
    };

    expect(linuxInstallSnippet(input)).toContain("AGENT_ALLOW_INSECURE_MANAGEMENT=true");
    expect(dockerComposeSnippet(input)).toContain("AGENT_ALLOW_INSECURE_MANAGEMENT: \"true\"");
    expect(cliSnippet(input)).toContain("AGENT_ALLOW_INSECURE_MANAGEMENT=true");
  });

  test("builds Docker Compose snippet with default GHCR image", () => {
    const snippet = dockerComposeSnippet(baseInput);

    expect(snippet).toContain("image: \"ghcr.io/exampleuser/p2pstream:latest\"");
    expect(snippet).toContain("command: [\"/app/p2pstream\", \"agent\"]");
    expect(snippet).toContain("MANAGEMENT_URL: \"https://mgmt.example.test\"");
    expect(snippet).toContain("AGENT_TOKEN: \"token'value\"");
  });

  test("builds CLI snippet without repository fields", () => {
    const snippet = cliSnippet(baseInput);

    expect(snippet).toBe("MANAGEMENT_URL='https://mgmt.example.test' AGENT_ID='agent-mfrggzdfmztwq2lkmmxgg33nna' AGENT_TOKEN='token'\\''value' p2pstream agent");
  });
});
