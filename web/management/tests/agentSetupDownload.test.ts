import { afterEach, describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdtempSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync, statSync, symlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { linuxInstallSnippet, linuxManagedUpdaterBootstrapSnippet, linuxRotateAgentTokenSnippet, linuxUninstallSnippet } from "../src/lib/agentSetupSnippets";

const roots: string[] = [];
afterEach(() => { for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true }); });

const input = {
  managementUrl: "https://remote.example.test:8081",
  agentId: "agent-existing",
  agentToken: "token' with $(touch SHOULD_NOT_EXIST) and `id`",
  repository: "Example/p2pstream",
  version: "v1.2.3-staging.84",
};
const authority = {
  updaterEnrollmentToken: "p2puet_'quoted-token",
  agentUpdateAuthorityPublicKeyBase64: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
  agentUpdateAuthorityKeyId: "a".repeat(64),
  agentUpdateAuthorityEpoch: 1n,
};

// All commands run against local fixtures. sudo and curl are replaced; neither
// the real installer nor a remote service is invoked by these tests.
function fixture(options: { arch?: string; legacy?: boolean; version?: string } = {}) {
  const version = options.version ?? input.version;
  const arch = options.arch ?? "amd64";
  const root = mkdtempSync(join(tmpdir(), "agent-setup-test-"));
  roots.push(root);
  const bin = join(root, "bin");
  const assets = join(root, "assets");
  const scratch = join(root, "scratch");
  for (const dir of [bin, assets, scratch]) mkdirSync(dir);
  const sourceRoot = join(root, `p2pstream-${version}`);
  mkdirSync(join(sourceRoot, "scripts"), { recursive: true });
  const installer = join(sourceRoot, "scripts/install-agent.sh");
  writeFileSync(installer, 'set -eu\nenv -0 > "$FIXTURE/result"\ncp "$P2PSTREAM_AGENT_BINARY_FILE" "$FIXTURE/installed-binary"\n');
  writeFileSync(join(sourceRoot, "scripts/uninstall-agent.sh"), 'set -eu\nenv -0 > "$FIXTURE/result"\n');
  const sourceAsset = `p2pstream_${version}_source.tar.gz`;
  expect(Bun.spawnSync(["tar", "-czf", join(assets, sourceAsset), "-C", root, `p2pstream-${version}`]).exitCode).toBe(0);
  const binary = join(root, "p2pstream");
  writeFileSync(binary, `fixture binary for ${version} ${arch}`);
  let binaryAsset = `p2pstream_${version}_linux_${arch}`;
  if (options.legacy) {
    binaryAsset += ".tar.gz";
    expect(Bun.spawnSync(["tar", "-czf", join(assets, binaryAsset), "-C", root, "./p2pstream"]).exitCode).toBe(0);
  } else {
    writeFileSync(join(assets, binaryAsset), readFileSync(binary));
  }
  const checksum = (name: string) => `${createHash("sha256").update(readFileSync(join(assets, name))).digest("hex")}  ${name}\n`;
  const manifest = checksum(sourceAsset) + checksum(binaryAsset);
  writeFileSync(join(assets, "checksums.txt"), manifest);
  writeFileSync(join(bin, "sudo"), '#!/bin/bash\nexec "$@"\n', { mode: 0o755 });
  writeFileSync(join(bin, "uname"), `#!/bin/bash\nif [ "$1" = -s ]; then echo Linux; else echo ${arch === "arm64" ? "aarch64" : "x86_64"}; fi\n`, { mode: 0o755 });
  writeFileSync(join(bin, "curl"), `#!/bin/bash
set -eu
output=; url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output=$2; shift 2 ;;
    --proto|--proto-redir|--retry|--connect-timeout|--max-time|--max-filesize|--write-out) shift 2 ;;
    --*) shift ;;
    *) url=$1; shift ;;
  esac
done
printf '%s\\n' "$url" >> "$FIXTURE/downloads"
if [ "$url" = "https://github.com/Example/p2pstream/releases/latest" ]; then
  printf '%s' "https://github.com/Example/p2pstream/releases/tag/\${RESOLVED_VERSION:-${version}}"
else
  [[ "$url" = "https://github.com/Example/p2pstream/releases/download/${version}/"* ]] || exit 22
  [ "\${url##*/}" != "\${FAIL_ASSET:-}" ] || exit 22
  cp "$FIXTURE/assets/\${url##*/}" "$output"
fi
`, { mode: 0o755 });
  const environmentPath = join(root, "agent.env");
  const existingToken = "existing' token with $LITERAL and `id`";
  writeFileSync(environmentPath, `AGENT_ID="${input.agentId}"\nAGENT_TOKEN="${existingToken}"\nMANAGEMENT_URL="https://localhost:8081"\n`, { mode: 0o640 });
  if (process.env.P2PSTREAM_TEST_USER_SYSTEMD === "true") {
    writeFileSync(join(bin, "systemd-run"), '#!/bin/bash\nexec /usr/bin/systemd-run --user --setenv="FIXTURE=$FIXTURE" --setenv="PATH=$PATH" "$@"\n', { mode: 0o755 });
  } else {
    writeFileSync(join(bin, "systemd-run"), `#!/bin/bash
set -eu
while [[ "$1" = --* ]]; do shift; done
args=()
for arg in "$@"; do
  args+=( "\${arg//\\$\\$/\\$}" )
done
exec env -i PATH="$PATH" FIXTURE="$FIXTURE" AGENT_ID="\${FIXTURE_EXISTING_ID:-${input.agentId}}" AGENT_TOKEN="$FIXTURE_EXISTING_TOKEN" MANAGEMENT_URL=https://localhost:8081 "\${args[@]}"
`, { mode: 0o755 });
  }
  writeFileSync(join(bin, "systemctl"), '#!/bin/bash\nprintf "%s\\n" "$*" >> "$FIXTURE/systemctl.log"\n', { mode: 0o755 });
  const run = (command: string, extraEnv: Record<string, string> = {}) => {
    const result = Bun.spawnSync(["bash", "-c", command], {
      cwd: root,
      stdin: "ignore",
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, FIXTURE: root, TMPDIR: scratch, FIXTURE_EXISTING_TOKEN: existingToken, ...extraEnv },
    });
    expect(readdirSync(scratch)).toEqual([]);
    const values = readdirSync(root).includes("result")
      ? Object.fromEntries(readFileSync(join(root, "result"), "utf8").split("\0").filter(Boolean).map((entry) => [entry.slice(0, entry.indexOf("=")), entry.slice(entry.indexOf("=") + 1)]))
      : null;
    return { exitCode: result.exitCode, stderr: result.stderr.toString(), values };
  };
  const downloads = () => readdirSync(root).includes("downloads") ? readFileSync(join(root, "downloads"), "utf8").trim().split("\n") : [];
  return { root, assets, installer, binary, sourceAsset, binaryAsset, manifest, run, downloads, environmentPath, existingToken };
}

describe("copy-and-paste Linux setup commands", () => {
  for (const arch of ["amd64", "arm64"]) {
    test(`downloads and verifies ${arch} installer and binary without prompting`, () => {
      const f = fixture({ arch });
      const result = f.run(linuxInstallSnippet({ ...input, ...authority, enableManagedUpdates: true }));
      expect(result.stderr).toBe("");
      expect(result.exitCode).toBe(0);
      expect(result.values?.AGENT_TOKEN).toBe(input.agentToken);
      expect(result.values?.P2PSTREAM_UPDATER_ENROLLMENT_TOKEN).toBe(authority.updaterEnrollmentToken);
      expect(result.values?.MANAGEMENT_URL).toBe(input.managementUrl);
      expect(result.values?.P2PSTREAM_AGENT_UPDATE_CHANNEL).toBe("staging");
      expect(result.values?.P2PSTREAM_VERSION).toBe(input.version);
      expect(readFileSync(join(f.root, "installed-binary"), "utf8")).toBe(`fixture binary for ${input.version} ${arch}`);
      expect(f.downloads().map((url) => url.split("/").pop())).toEqual(["checksums.txt", f.sourceAsset, f.binaryAsset]);
      expect(readdirSync(f.root)).not.toContain("SHOULD_NOT_EXIST");
    });
  }

  test("supports older releases with archived binaries and resolves latest once", () => {
    const f = fixture({ version: "v0.1.52", legacy: true });
    const result = f.run(linuxInstallSnippet({ ...input, version: "latest" }));
    expect(result.stderr).toBe("");
    expect(result.exitCode).toBe(0);
    expect(result.values?.P2PSTREAM_VERSION).toBe("v0.1.52");
    expect(f.downloads()).toHaveLength(4);
    expect(f.downloads()[0]).toEndWith("/releases/latest");
    expect(f.downloads().slice(1).every((url) => url.includes("/download/v0.1.52/"))).toBe(true);
    expect(readFileSync(join(f.root, "installed-binary"), "utf8")).toContain("v0.1.52");
  });

  test("bootstraps with the enrollment credential and preserves the existing tunnel identity", () => {
    const f = fixture();
    const result = f.run(linuxManagedUpdaterBootstrapSnippet({ ...input, ...authority, currentTunnelVersion: "v0.1.52", currentTunnelCommit: "b".repeat(40) }));
    expect(result.stderr).toBe("");
    expect(result.exitCode).toBe(0);
    expect(result.values?.AGENT_TOKEN).toBeUndefined();
    expect(result.values?.P2PSTREAM_UPDATER_ENROLLMENT_TOKEN).toBe(authority.updaterEnrollmentToken);
    expect(result.values?.P2PSTREAM_EXISTING_TUNNEL_VERSION).toBe("v0.1.52");
    expect(result.values?.P2PSTREAM_EXISTING_TUNNEL_COMMIT).toBe("b".repeat(40));
  });

  test("keeps explicit local files available without network access", () => {
    const f = fixture();
    const result = f.run(linuxInstallSnippet({ ...input, installerPath: f.installer, agentBinaryPath: f.binary }));
    expect(result.exitCode).toBe(0);
    expect(result.values?.AGENT_TOKEN).toBe(input.agentToken);
    expect(f.downloads()).toEqual([]);
  });

  test("downloads only the verified source for uninstall", () => {
    const f = fixture();
    const result = f.run(linuxUninstallSnippet(input));
    expect(result.stderr).toBe("");
    expect(result.exitCode).toBe(0);
    expect(result.values?.P2PSTREAM_UNINSTALL_CONFIRM).toBe("full-purge");
    expect(result.values?.AGENT_TOKEN).toBeUndefined();
    expect(f.downloads().map((url) => url.split("/").pop())).toEqual(["checksums.txt", f.sourceAsset]);
  });

  test("reinstall reuses the existing host token and applies the edited management URL", () => {
    const f = fixture();
    const command = linuxInstallSnippet({ ...input, agentToken: "", reuseExistingToken: true, agentEnvironmentPath: f.environmentPath, ...authority, enableManagedUpdates: true });
    expect(command).not.toContain(f.existingToken);
    const result = f.run(command);
    expect(result.stderr).toBe("");
    expect(result.exitCode).toBe(0);
    expect(result.values?.AGENT_TOKEN).toBe(f.existingToken);
    expect(result.values?.MANAGEMENT_URL).toBe(input.managementUrl);
    expect(result.values?.P2PSTREAM_UPDATER_ENROLLMENT_TOKEN).toBe(authority.updaterEnrollmentToken);
    expect(result.values?.P2PSTREAM_AGENT_UPDATE_CHANNEL).toBe("staging");
  });

  test("reinstall refuses the wrong agent host before running its installer", () => {
    const f = fixture();
    const result = f.run(linuxInstallSnippet({ ...input, agentId: "wrong-agent", agentToken: "", reuseExistingToken: true, agentEnvironmentPath: f.environmentPath }));
    expect(result.exitCode).not.toBe(0);
    expect(result.stderr).toContain("selected agent identity");
    expect(result.values).toBeNull();
  });

  test("reinstall rejects missing and symlinked environment files", () => {
    const f = fixture();
    for (const path of [join(f.root, "missing.env"), join(f.root, "symlink.env")]) {
      if (path.endsWith("symlink.env")) symlinkSync(f.environmentPath, path);
      const result = f.run(linuxInstallSnippet({ ...input, agentToken: "", reuseExistingToken: true, agentEnvironmentPath: path }));
      expect(result.exitCode).not.toBe(0);
      expect(result.values).toBeNull();
    }
    expect(f.downloads()).toEqual([]);
  });

  test("token rotation only updates credentials and restarts, preserving existing settings and permissions", () => {
    const f = fixture();
    const before = readFileSync(f.environmentPath, "utf8");
    const token = "new' token with \"quotes\", $LITERAL, `id`, and \\slashes";
    const result = f.run(linuxRotateAgentTokenSnippet({ agentId: input.agentId, agentToken: token, agentEnvironmentPath: f.environmentPath }));
    expect(result.stderr).toBe("");
    expect(result.exitCode).toBe(0);
    expect(readFileSync(f.environmentPath, "utf8")).toBe(before + '\n\nAGENT_TOKEN="' + token.replaceAll("\\", "\\\\").replaceAll('"', '\\"') + '"\n');
    expect(statSync(f.environmentPath).mode & 0o777).toBe(0o640);
    expect(readFileSync(join(f.root, "systemctl.log"), "utf8")).toBe("restart p2pstream-agent\n");
    expect(f.downloads()).toEqual([]);
    expect(readdirSync(f.root)).not.toContain("installed-binary");
  });

  test("token rotation refuses the wrong host without modifying credentials", () => {
    const f = fixture();
    const before = readFileSync(f.environmentPath, "utf8");
    const result = f.run(linuxRotateAgentTokenSnippet({ agentId: "wrong-agent", agentToken: "new-token", agentEnvironmentPath: f.environmentPath }));
    expect(result.exitCode).not.toBe(0);
    expect(readFileSync(f.environmentPath, "utf8")).toBe(before);
    expect(readdirSync(f.root)).not.toContain("systemctl.log");
  });

  for (const failure of ["source mismatch", "binary mismatch", "missing checksum", "duplicate checksum", "download failure", "invalid latest"]) {
    test(`does not execute the installer on ${failure}`, () => {
      const f = fixture();
      if (failure === "source mismatch") writeFileSync(join(f.assets, f.sourceAsset), "tampered");
      if (failure === "binary mismatch") writeFileSync(join(f.assets, f.binaryAsset), "tampered");
      if (failure === "missing checksum") writeFileSync(join(f.assets, "checksums.txt"), "");
      if (failure === "duplicate checksum") writeFileSync(join(f.assets, "checksums.txt"), f.manifest.repeat(2));
      const result = f.run(linuxInstallSnippet({ ...input, version: failure === "invalid latest" ? "latest" : input.version }), {
        ...(failure === "download failure" ? { FAIL_ASSET: f.binaryAsset } : {}),
        ...(failure === "invalid latest" ? { RESOLVED_VERSION: "../../malicious" } : {}),
      });
      expect(result.exitCode).not.toBe(0);
      expect(result.values).toBeNull();
    });
  }
});
