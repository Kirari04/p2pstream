import { defineConfig } from "@playwright/test";
import { resolve } from "node:path";
import { createManagementWebServer, repoDir } from "./playwright.shared";

const managementPort = process.env.DOCS_SCREENSHOT_MANAGEMENT_PORT ?? "19091";
const frontendPort = process.env.DOCS_SCREENSHOT_FRONTEND_PORT ?? "5173";
const httpPort = process.env.DOCS_SCREENSHOT_HTTP_PORT ?? "19080";
const httpsPort = process.env.DOCS_SCREENSHOT_HTTPS_PORT ?? "19443";
const dataDir = resolve(repoDir, "tmp/docs-screenshots-data");
const cacheDir = resolve(repoDir, "tmp/docs-screenshots-cache");
const binaryPath = resolve(repoDir, "tmp/p2pstream-docs-screenshots");

export default defineConfig({
  testDir: "./docs-screenshots",
  fullyParallel: false,
  workers: 1,
  timeout: 180_000,
  expect: {
    timeout: 15_000,
  },
  webServer: createManagementWebServer({
    managementPort,
    frontendPort,
    dataDir,
    cacheDir,
    binaryPath,
    setupToken: "docs-screenshot-setup-token",
    agentId: "docs-agent-zurich",
    agentName: "Docs Agent Zurich",
    agentToken: "docs-agent-token",
    managementPublicURL: `https://127.0.0.1:${managementPort}`,
    cleanupPorts: [managementPort, httpPort, httpsPort],
    timeout: 180_000,
  }),
  use: {
    baseURL: `http://127.0.0.1:${frontendPort}`,
    ignoreHTTPSErrors: true,
    trace: "retain-on-failure",
    viewport: { width: 1796, height: 1080 },
    deviceScaleFactor: 1,
  },
  projects: [
    {
      name: "docs-screenshots",
    },
  ],
});
