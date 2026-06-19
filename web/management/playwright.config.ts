import { defineConfig } from "@playwright/test";
import { createManagementWebServer, repoDir } from "./playwright.shared";
import { resolve } from "node:path";

const managementPort = portFromEnv(process.env.PLAYWRIGHT_MANAGEMENT_PORT, "19081");
const frontendPort = portFromEnv(process.env.PLAYWRIGHT_FRONTEND_PORT, "5173");
const dataDir = resolve(repoDir, "tmp/playwright-data");
const cacheDir = resolve(repoDir, "tmp/playwright-cache");
const e2eBinary = resolve(repoDir, "tmp/p2pstream-playwright");

function portFromEnv(value: string | undefined, fallback: string): string {
  if (!value) return fallback;
  const trimmed = value.trim();
  if (!/^\d+$/.test(trimmed)) return fallback;
  const port = Number.parseInt(trimmed, 10);
  return port >= 1 && port <= 65535 ? port.toString() : fallback;
}

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  timeout: 60_000,
  expect: {
    timeout: 10_000,
  },
  webServer: createManagementWebServer({
    managementPort,
    frontendPort,
    dataDir,
    cacheDir,
    binaryPath: e2eBinary,
    setupToken: "playwright-setup-token",
    agentId: "playwright-agent",
    agentName: "Playwright Agent",
    agentToken: "playwright-agent-token",
  }),
  use: {
    ignoreHTTPSErrors: true,
    trace: "retain-on-failure",
    viewport: { width: 1280, height: 900 },
  },
  projects: [
    {
      name: "management-proxy",
      use: { baseURL: `https://localhost:${managementPort}` },
    },
    {
      name: "vite-direct",
      use: { baseURL: `http://127.0.0.1:${frontendPort}` },
    },
  ],
});
