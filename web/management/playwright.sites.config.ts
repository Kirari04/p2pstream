import { defineConfig } from "@playwright/test";
import { repoDir } from "./playwright.shared";

const port = process.env.PLAYWRIGHT_MANAGEMENT_PORT ?? "19481";
if (!/^\d+$/.test(port) || Number(port) < 1024 || Number(port) > 65535) {
  throw new Error("PLAYWRIGHT_MANAGEMENT_PORT must be an unprivileged TCP port");
}
process.env.PLAYWRIGHT_MANAGEMENT_PORT = port;

export default defineConfig({
  testDir: "./e2e",
  testMatch: /sites(?:-[\w-]+)?\.spec\.ts/,
  fullyParallel: false,
  workers: 1,
  timeout: 60_000,
  globalTimeout: 10 * 60_000,
  expect: { timeout: 10_000 },
  webServer: {
    command: "bash scripts/test-sites-e2e-server.sh",
    cwd: repoDir,
    url: `https://127.0.0.1:${port}/`,
    ignoreHTTPSErrors: true,
    timeout: 120_000,
    reuseExistingServer: false,
    stdout: "pipe",
    stderr: "pipe",
    gracefulShutdown: { signal: "SIGTERM", timeout: 10_000 },
  },
  use: {
    baseURL: `https://localhost:${port}`,
    ignoreHTTPSErrors: true,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    viewport: { width: 1280, height: 900 },
  },
  projects: [{ name: "sites-built-management" }],
});
