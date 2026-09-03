import { describe, expect, test } from "bun:test";
import {
  cliSnippet,
  dockerComposeSnippet,
  dockerImageForRepository,
  isValidScriptRef,
  isValidRepository,
  linuxInstallSnippet,
  linuxManagedUpdaterBootstrapSnippet,
  linuxUninstallSnippet,
  normalizeReleaseVersion,
  normalizeRepository,
  normalizeScriptRef,
  scriptRefForVersion,
  shellQuote,
  yamlQuote,
} from "@/lib/agentSetupSnippets";

const baseInput = {
  managementUrl: "https://mgmt.example.test/",
  agentId: "agent-mfrggzdfmztwq2lkmmxgg33nna",
  agentToken: "token'value",
  repository: "ExampleUser/p2pstream",
  version: "v1.2.3",
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
    }
  });

  test("uses GHCR image default from repository", () => {
    expect(dockerImageForRepository("ExampleUser/p2pstream")).toBe("ghcr.io/exampleuser/p2pstream:latest");
    expect(dockerImageForRepository("ExampleUser/p2pstream", "v1.2.3-staging.17")).toBe("ghcr.io/exampleuser/p2pstream:v1.2.3-staging.17");
    expect(dockerImageForRepository("ExampleUser/p2pstream", "v1.2.3")).toBe("ghcr.io/exampleuser/p2pstream:v1.2.3");
  });

  test("builds one-line Linux installer snippet", () => {
    const snippet = linuxInstallSnippet(baseInput);

    expect(snippet).toStartWith("{ read -r -s -p 'Agent token: '");
    expect(snippet).toContain("MANAGEMENT_URL='https://mgmt.example.test'");
    expect(snippet).toContain("AGENT_ID='agent-mfrggzdfmztwq2lkmmxgg33nna'");
    expect(snippet).not.toContain(baseInput.agentToken);
    expect(snippet).toContain("IFS= read -r AGENT_TOKEN");
    expect(snippet).toContain("export AGENT_TOKEN");
    expect(snippet).not.toContain("AGENT_ALLOW_TARGETS");
    expect(snippet).toContain("P2PSTREAM_REPOSITORY='ExampleUser/p2pstream'");
    expect(snippet).toContain("P2PSTREAM_VERSION='v1.2.3'");
    expect(snippet).toContain("P2PSTREAM_AGENT_BINARY_FILE='/path/to/p2pstream-agent-vX.Y.Z-linux-ARCH'");
    expect(snippet).toEndWith("p2pstream-installer '/path/to/p2pstream-install-agent.sh'");
    expect(snippet).not.toContain("curl");
    expect(snippet).not.toContain("\n");
  });

  test("bootstraps the managed updater only with its separate one-time enrollment token", () => {
    const snippet = linuxInstallSnippet({
      ...baseInput,
      enableManagedUpdates: true,
      updaterEnrollmentToken: "p2puet_separate-root-owned-bootstrap",
      agentUpdateRootBase64: "eyJzY2hlbWFfdmVyc2lvbiI6MX0=",
      agentUpdateAuthorityPublicKeyBase64: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
      agentUpdateAuthorityKeyId: "a".repeat(64),
      agentUpdateAuthorityEpoch: 1n,
    });

    expect(snippet).toContain("P2PSTREAM_ENABLE_MANAGED_UPDATES=true");
    expect(snippet).not.toContain("p2puet_separate-root-owned-bootstrap");
    expect(snippet).toContain("Updater enrollment token: ");
    expect(snippet).toContain("IFS= read -r P2PSTREAM_UPDATER_ENROLLMENT_TOKEN");
    expect(snippet).toContain("P2PSTREAM_AGENT_UPDATE_ROOT_BASE64='eyJzY2hlbWFfdmVyc2lvbiI6MX0='");
    expect(snippet).toContain("P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='");
    expect(snippet).toContain(`P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID='${"a".repeat(64)}'`);
    expect(snippet).toContain("P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH='1'");
    expect(snippet).toContain("P2PSTREAM_AGENT_UPDATE_CHANNEL='stable'");
    expect(snippet).toContain("IFS= read -r AGENT_TOKEN; IFS= read -r P2PSTREAM_UPDATER_ENROLLMENT_TOKEN");
    expect(dockerComposeSnippet({ ...baseInput, enableManagedUpdates: true, updaterEnrollmentToken: "p2puet_unused" }))
      .not.toContain("P2PSTREAM_UPDATER_ENROLLMENT_TOKEN");
    expect(cliSnippet({ ...baseInput, enableManagedUpdates: true, updaterEnrollmentToken: "p2puet_unused" }))
      .not.toContain("P2PSTREAM_UPDATER_ENROLLMENT_TOKEN");
  });

  test("fails closed when managed-update bootstrap trust is incomplete", () => {
    expect(() => linuxInstallSnippet({ ...baseInput, enableManagedUpdates: true })).toThrow("pinned trust root");
    expect(() => linuxInstallSnippet({
      ...baseInput,
      enableManagedUpdates: true,
      updaterEnrollmentToken: "p2puet_token",
    })).toThrow("pinned trust root");
  });

  test("builds updater-only bootstrap without exposing or rotating the tunnel token", () => {
    const snippet = linuxManagedUpdaterBootstrapSnippet({
      managementUrl: "https://mgmt.example.test:8081/",
      agentId: "agent-existing",
      updaterEnrollmentToken: "p2puet_one-time",
      agentUpdateRootBase64: "eyJyb290IjoxfQ==",
      agentUpdateAuthorityPublicKeyBase64: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
      agentUpdateAuthorityKeyId: "a".repeat(64),
      agentUpdateAuthorityEpoch: 9n,
	  currentTunnelVersion: "v1.0.0",
	  currentTunnelCommit: "b".repeat(40),
      repository: "ExampleUser/p2pstream",
      version: "v1.2.3",
    });
    expect(snippet).toContain("MANAGEMENT_URL='https://mgmt.example.test:8081'");
    expect(snippet).toContain("AGENT_ID='agent-existing'");
    expect(snippet).toContain("P2PSTREAM_ENABLE_MANAGED_UPDATES=true");
    expect(snippet).not.toContain("p2puet_one-time");
    expect(snippet).toContain("Updater enrollment token: ");
    expect(snippet).toContain("IFS= read -r P2PSTREAM_UPDATER_ENROLLMENT_TOKEN");
    expect(snippet).toContain("P2PSTREAM_AGENT_UPDATE_ROOT_BASE64='eyJyb290IjoxfQ=='");
    expect(snippet).toContain("P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH='9'");
	expect(snippet).toContain("P2PSTREAM_EXISTING_TUNNEL_VERSION='v1.0.0'");
	expect(snippet).toContain(`P2PSTREAM_EXISTING_TUNNEL_COMMIT='${"b".repeat(40)}'`);
    expect(snippet).not.toContain("AGENT_TOKEN=");
    expect(snippet).not.toContain("\n");
  });

  test("uses immutable staging prereleases in Linux installer snippets", () => {
    const version = "v1.2.3-staging.17";
    const snippet = linuxInstallSnippet({ ...baseInput, version });
    expect(snippet).toContain(`P2PSTREAM_VERSION='${version}'`);
    expect(dockerComposeSnippet({ ...baseInput, version })).toContain(`image: \"ghcr.io/exampleuser/p2pstream:${version}\"`);
    expect(() => linuxInstallSnippet({ ...baseInput, version: "staging" })).toThrow("latest or an exact SemVer tag");
    const managedSnippet = linuxInstallSnippet({
      ...baseInput,
      version,
      enableManagedUpdates: true,
      updaterEnrollmentToken: "p2puet_staging",
      agentUpdateRootBase64: "e30=",
      agentUpdateAuthorityPublicKeyBase64: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
      agentUpdateAuthorityKeyId: "a".repeat(64),
      agentUpdateAuthorityEpoch: 1n,
    });
    expect(managedSnippet).toContain("P2PSTREAM_AGENT_UPDATE_CHANNEL='staging'");
  });

  test("adds pinned release version to Linux installer and Docker snippets", () => {
    const snippet = linuxInstallSnippet({ ...baseInput, version: "v1.2.3" });

    expect(snippet).not.toContain("raw.githubusercontent.com");
    expect(snippet).toContain("P2PSTREAM_VERSION='v1.2.3'");
    expect(dockerComposeSnippet({ ...baseInput, version: "v1.2.3" })).toContain("image: \"ghcr.io/exampleuser/p2pstream:v1.2.3\"");
    expect(cliSnippet({ ...baseInput, version: "v1.2.3" })).not.toContain("P2PSTREAM_VERSION");
  });

  test("validates release versions and installer script refs", () => {
    expect(normalizeReleaseVersion("")).toBe("latest");
    expect(normalizeReleaseVersion("v1.2.3-staging.17")).toBe("v1.2.3-staging.17");
    expect(scriptRefForVersion("latest")).toBe("main");
    expect(scriptRefForVersion("v1.2.3-staging.17")).toBe("v1.2.3-staging.17");
    expect(scriptRefForVersion("v1.2.3")).toBe("v1.2.3");
    expect(normalizeScriptRef("", "latest")).toBe("main");
    expect(normalizeScriptRef("abcdef0", "latest")).toBe("abcdef0");
    expect(isValidScriptRef("staging")).toBe(false);

    for (const version of ["staging", "nightly", "dev", "v1.2", "v1.2.3-01", "latest;id"]) {
      expect(() => normalizeReleaseVersion(version)).toThrow("Release version must be latest or an exact SemVer tag.");
      expect(() => linuxInstallSnippet({ ...baseInput, version })).toThrow();
      expect(() => dockerComposeSnippet({ ...baseInput, version })).toThrow("Release version must be latest or an exact SemVer tag.");
    }

    for (const scriptRef of ["main;id", "feature/test", "../main", "main\nid", ""]) {
      if (scriptRef === "") continue;
      expect(isValidScriptRef(scriptRef)).toBe(false);
      expect(() => normalizeScriptRef(scriptRef, "latest")).toThrow("Installer script ref must be main, an exact SemVer tag, or a commit SHA.");
    }
  });

  test("builds Linux uninstall snippet from a local pinned file", () => {
    const snippet = linuxUninstallSnippet({});

    expect(snippet).toBe("sudo env P2PSTREAM_UNINSTALL_CONFIRM=full-purge bash '/path/to/p2pstream-uninstall-agent.sh'");
    expect(snippet).not.toContain("\n");
  });

  test("builds Linux uninstall snippet with an explicit local path and no agent secrets", () => {
    const snippet = linuxUninstallSnippet({ installerPath: "/srv/pinned/uninstall-agent-v1.2.3.sh" });

    expect(snippet).toContain("sudo env P2PSTREAM_UNINSTALL_CONFIRM=full-purge bash '/srv/pinned/uninstall-agent-v1.2.3.sh'");
    expect(snippet).not.toContain("AGENT_TOKEN");
    expect(snippet).not.toContain("AGENT_ID");
    expect(snippet).not.toContain("MANAGEMENT_URL");
  });

  test("rejects unsafe local installer and binary paths", () => {
    expect(() => linuxInstallSnippet({ ...baseInput, installerPath: "https://example.test/install.sh" })).toThrow("clean absolute local path");
    expect(() => linuxInstallSnippet({ ...baseInput, agentBinaryPath: "/tmp/../evil" })).toThrow("clean absolute local path");
    expect(() => linuxUninstallSnippet({ installerPath: "/tmp//evil" })).toThrow("clean absolute local path");
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
    expect(dockerComposeSnippet(input)).toContain('MANAGEMENT_TRUST_FILE: "/data/management-ca.pem"');
    expect(dockerComposeSnippet(input)).toContain("p2pstream-agent-state:/data");
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

  test("builds Docker Compose snippet with selected pinned image", () => {
    const snippet = dockerComposeSnippet(baseInput);

    expect(snippet).toContain("image: \"ghcr.io/exampleuser/p2pstream:v1.2.3\"");
    expect(snippet).toContain("command: [\"/app/p2pstream\", \"agent\"]");
    expect(snippet).toContain("MANAGEMENT_URL: \"https://mgmt.example.test\"");
    expect(snippet).toContain("AGENT_TOKEN: \"token'value\"");
    expect(snippet).toContain("p2pstream-agent-state:");
    expect(snippet).not.toContain("AGENT_ALLOW_TARGETS");
  });

  test("builds CLI snippet without repository fields", () => {
    const snippet = cliSnippet(baseInput);

    expect(snippet).toBe("MANAGEMENT_URL='https://mgmt.example.test' MANAGEMENT_TRUST_FILE='./p2pstream-agent-state/management-ca.pem' AGENT_ID='agent-mfrggzdfmztwq2lkmmxgg33nna' AGENT_TOKEN='token'\\''value' p2pstream agent");
  });

  test("uses an explicit custom agent destination allowlist", () => {
    const input = { ...baseInput, allowTargets: ["app.internal:443", "10.0.5.0/24:8080"] };
    expect(linuxInstallSnippet(input)).toContain("AGENT_ALLOW_TARGETS='app.internal:443,10.0.5.0/24:8080'");
    expect(dockerComposeSnippet(input)).toContain("AGENT_ALLOW_TARGETS: \"app.internal:443,10.0.5.0/24:8080\"");
  });

  test("uses an explicit unrestricted destination opt-in", () => {
    const input = { ...baseInput, allowAnyTarget: true };
    expect(linuxInstallSnippet(input)).toContain("AGENT_ALLOW_ANY_TARGET=true");
    expect(dockerComposeSnippet(input)).toContain('AGENT_ALLOW_ANY_TARGET: "true"');
    expect(() => cliSnippet({ ...input, allowTargets: ["app.internal:443"] })).toThrow("cannot be combined");
  });
});
