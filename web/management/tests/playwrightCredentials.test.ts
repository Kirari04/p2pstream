import { describe, expect, test } from "bun:test";
import { spawnSync } from "node:child_process";
import { resolve } from "node:path";

const managementDir = resolve(import.meta.dir, "..");
const names = ["PLAYWRIGHT_SETUP_TOKEN", "PLAYWRIGHT_AGENT_TOKEN", "PLAYWRIGHT_ADMIN_PASSWORD"];

function run(code: string, extraEnv: Record<string, string> = {}) {
  const env = { ...process.env };
  delete env.PLAYWRIGHT_FIXTURE_CREDENTIALS_INITIALIZED;
  for (const name of names) delete env[name];
  return spawnSync(process.execPath, ["-e", code], {
    cwd: managementDir,
    env: { ...env, ...extraEnv },
    encoding: "utf8",
    timeout: 15_000,
  });
}

const runnerCode = `
  import { initializeFixtureCredentials, fixtureCredential } from "./playwright.credentials";
  import { createManagementWebServer } from "./playwright.shared";
  initializeFixtureCredentials();
  const initial = ${JSON.stringify(names)}.map(fixtureCredential);
  initializeFixtureCredentials();
  const { defaultPassword, defaultSetupToken } = await import("./e2e/helpers/auth");
  const servers = createManagementWebServer({
    managementPort: "19081", frontendPort: "5173", dataDir: "/tmp/test-data",
    cacheDir: "/tmp/test-cache", binaryPath: "/tmp/test-binary", agentId: "test-agent", agentName: "Test Agent",
  });
  console.log(JSON.stringify({ initial, current: ${JSON.stringify(names)}.map(fixtureCredential),
    defaultPassword, defaultSetupToken, servers, inherited: {
      ...Object.fromEntries(${JSON.stringify(names)}.map(name => [name, process.env[name]])),
      PLAYWRIGHT_FIXTURE_CREDENTIALS_INITIALIZED: process.env.PLAYWRIGHT_FIXTURE_CREDENTIALS_INITIALIZED,
    } }));
`;

describe("Playwright fixture credentials", () => {
  test("each runner generates distinct credentials, then workers and helpers reuse them", () => {
    const first = run(runnerCode, Object.fromEntries(names.map(name => [name, "inherited-static-value"])));
    expect(first.status).toBe(0);
    const initial = JSON.parse(first.stdout);
    expect(new Set(initial.initial).size).toBe(3);
    for (const value of initial.initial) expect(value).toMatch(/^[0-9a-f]{64}$/);
    expect(initial.current).toEqual(initial.initial);
    expect(initial.defaultSetupToken).toBe(initial.initial[0]);
    expect(initial.defaultPassword).toBe(initial.initial[2]);
    const worker = run(runnerCode, initial.inherited);
    expect(worker.status).toBe(0);
    expect(JSON.parse(worker.stdout).initial).toEqual(initial.initial);
    const next = run(runnerCode);
    expect(next.status).toBe(0);
    for (const value of JSON.parse(next.stdout).initial) expect(initial.initial).not.toContain(value);
  });

  test("backend binds loopback and receives secrets through env instead of shell literals", () => {
    const result = run(runnerCode);
    expect(result.status).toBe(0);
    const { servers, initial } = JSON.parse(result.stdout);
    const backend = servers[0];
    expect(backend.env.PLAYWRIGHT_SETUP_TOKEN).toBe(initial[0]);
    expect(backend.env.PLAYWRIGHT_AGENT_TOKEN).toBe(initial[1]);
    expect(backend.command).toContain("MANAGEMENT_BIND_ADDRESS=127.0.0.1");
    expect(backend.command).toContain('MANAGEMENT_SETUP_TOKEN="$PLAYWRIGHT_SETUP_TOKEN"');
    expect(backend.command).toContain('BOOTSTRAP_AGENT_TOKEN="$PLAYWRIGHT_AGENT_TOKEN"');
    expect(backend.command).toContain('AGENT_TOKEN="$PLAYWRIGHT_AGENT_TOKEN"');
    expect(servers[1].command).toContain("--host 127.0.0.1");
    for (const secret of initial) {
      expect(backend.command).not.toContain(secret);
      expect(servers[1].command).not.toContain(secret);
    }
  });

  test("auth helpers fail closed without runner credentials", () => {
    const result = run('await import("./e2e/helpers/auth")');
    expect(result.status).not.toBe(0);
    expect(result.stderr).toContain("must be supplied by the Playwright runner config");
  });

  test("an inherited incomplete runner environment fails instead of rotating in a worker", () => {
    const result = run(runnerCode, { PLAYWRIGHT_FIXTURE_CREDENTIALS_INITIALIZED: "1" });
    expect(result.status).not.toBe(0);
    expect(result.stderr).toContain("must be supplied by the Playwright runner config");
  });
});
