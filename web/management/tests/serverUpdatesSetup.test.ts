import { afterAll, beforeAll, expect, test } from "bun:test";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { resolve } from "node:path";
import { build } from "vite";
import { createSSRApp, type Component } from "vue";
import { renderToString } from "vue/server-renderer";

type SetupFixture = {
  overview: Record<string, unknown>;
  connectionError?: string;
  pending?: { instanceId: string; operationId: string; planToken: string };
};
const globals = globalThis as typeof globalThis & { __serverSetupFixture?: SetupFixture };
let directory: string;
let view: Component;

beforeAll(async () => {
  await mkdir(resolve("tmp"), { recursive: true });
  directory = await mkdtemp(resolve("tmp/server-setup-ssr-"));
  await build({
    configFile: resolve("vite.config.ts"), logLevel: "error",
    plugins: [{
      name: "server-setup-ssr-fixture", enforce: "pre",
      transform(source, id) {
        if (!id.endsWith("/views/ServerUpdates.vue")) return;
        // Seed only network/storage observations; render the production
        // component and real computed guards without a browser or server.
        return source
          .replace("ref<GetServerUpdateOverviewResponse | null>(null)", "ref<GetServerUpdateOverviewResponse | null>((globalThis as any).__serverSetupFixture.overview)")
          .replace('const connectionError = ref("");', 'const connectionError = ref((globalThis as any).__serverSetupFixture.connectionError || "");')
          .replace("ref<PendingServerUpdate | null>(null)", "ref<PendingServerUpdate | null>((globalThis as any).__serverSetupFixture.pending || null)");
      },
    }],
    build: { ssr: resolve("src/views/ServerUpdates.vue"), outDir: directory, emptyOutDir: true },
  });
  view = (await import(resolve(directory, "ServerUpdates.js"))).default;
}, 30000);
afterAll(async () => { delete globals.__serverSetupFixture; if (directory) await rm(directory, { recursive: true, force: true }); });

const recipe = "sudo verified-installer --migrate-deployment --COMPOSE_OPTIONS--";
function fixture(migration: boolean): SetupFixture {
  return { overview: { version: "v1.2.3", channel: "staging", commit: "abcd", installationId: "installation-id", instanceId: "instance-id", architecture: "amd64", executorConfigured: migration, executorAvailable: true, setupCommand: recipe, blockers: ["Finish active agent updates before updating the server"] } };
}
async function render(value: SetupFixture) {
  globals.__serverSetupFixture = value;
  return renderToString(createSSRApp(view));
}

test("legacy migration stays visible despite software-update executor blockers", async () => {
  const html = await render(fixture(true));
  expect(html).toContain("Upgrade host updater");
  expect(html).toContain("sudo verified-installer --migrate-deployment --");
  expect(html).toContain("Copy migration command");
  expect(html).not.toContain("Resolve before setup");
});

test("initial enrollment remains gated by observational preflight blockers", async () => {
  const html = await render(fixture(false));
  expect(html).toContain("Resolve before setup");
  expect(html).not.toContain("sudo verified-installer");
  expect(html).not.toContain("Copy setup command");
});

test("migration command prerequisites stay visible independently of executor blockers", async () => {
  const unavailable = fixture(true);
  unavailable.overview.setupCommand = "";
  unavailable.overview.setupUnavailable = "Published migration bundle is unavailable";
  const html = await render(unavailable);
  expect(html).toContain("Migration command unavailable");
  expect(html).toContain("Published migration bundle is unavailable");
  expect(html).not.toContain("Resolve before setup");
});

test("migration still requires a fresh management response and no active or unconfirmed update", async () => {
  const disconnected = fixture(true);
  disconnected.connectionError = "server restarting";
  expect(await render(disconnected)).not.toContain("sudo verified-installer");
  const running = fixture(true);
  running.overview.operation = { id: "update-id", phase: "preparing", previousVersion: "v1.2.3", targetVersion: "v1.2.4", startedAtUnixMillis: 0n, updatedAtUnixMillis: 0n };
  expect(await render(running)).not.toContain("sudo verified-installer");
  const unconfirmed = fixture(true);
  unconfirmed.pending = { instanceId: "instance-id", operationId: "update-id", planToken: "token" };
  expect(await render(unconfirmed)).not.toContain("sudo verified-installer");
});
