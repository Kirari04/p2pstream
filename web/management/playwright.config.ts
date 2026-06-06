import { defineConfig } from "@playwright/test";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const managementPort = process.env.PLAYWRIGHT_MANAGEMENT_PORT ?? "19081";
const managementDir = dirname(fileURLToPath(import.meta.url));
const repoDir = resolve(managementDir, "../..");
const dataDir = resolve(repoDir, "tmp/playwright-data");
const cacheDir = resolve(repoDir, "tmp/playwright-cache");
const e2eBinary = resolve(repoDir, "tmp/p2pstream-playwright");
const databaseURL = `file:${dataDir}/p2pstream.db?_busy_timeout=10000&_fk=1&_journal_mode=WAL&_synchronous=NORMAL&cache=private&mode=rwc`;

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", "'\\''")}'`;
}

const webServerCommand = [
  "set -e",
  `make -C ${shellQuote(repoDir)} kill >/dev/null 2>&1 || true`,
  `rm -rf ${shellQuote(dataDir)} ${shellQuote(cacheDir)}`,
  `mkdir -p ${shellQuote(dataDir)} ${shellQuote(cacheDir)} ${shellQuote(resolve(repoDir, "tmp"))}`,
  `cd ${shellQuote(repoDir)}`,
  "make frontend-install generate-proto",
  `go build -o ${shellQuote(e2eBinary)} .`,
  `cleanup() { trap - INT TERM EXIT; kill \${FRONTEND_PID:-} \${AGENT_PID:-} \${SERVER_PID:-} 2>/dev/null || true; for pid in \${FRONTEND_PID:-} \${AGENT_PID:-} \${SERVER_PID:-}; do if [ -n "$pid" ]; then wait "$pid" 2>/dev/null || true; fi; done; }`,
  `trap "cleanup; exit 0" INT TERM`,
  `trap "cleanup" EXIT`,
  [
    `CONFIG_DIR=${shellQuote(dataDir)}`,
    `DATABASE_URL=${shellQuote(databaseURL)}`,
    `PUBLIC_CACHE_DIR=${shellQuote(cacheDir)}`,
    `MANAGEMENT_PORT=${managementPort}`,
    "MANAGEMENT_SETUP_TOKEN=playwright-setup-token",
    "BOOTSTRAP_AGENT_ID=playwright-agent",
    "BOOTSTRAP_AGENT_NAME='Playwright Agent'",
    "BOOTSTRAP_AGENT_TOKEN=playwright-agent-token",
    "MANAGEMENT_UI_DEV_PROXY=http://127.0.0.1:5173",
    "ENV=development",
    `${shellQuote(e2eBinary)} server & SERVER_PID=$!`,
  ].join(" "),
  `CA_FILE=${shellQuote(resolve(dataDir, "certs/management/ca.crt.pem"))}`,
  `ready=0; for i in $(seq 1 100); do if [ -s "$CA_FILE" ] && curl --cacert "$CA_FILE" -fsS -H 'Content-Type: application/json' --data '{}' https://127.0.0.1:${managementPort}/p2pstream.v1.AgentManagementService/GetSetupState >/dev/null 2>&1; then ready=1; break; fi; sleep 0.2; done; if [ "$ready" != 1 ]; then echo "management server did not become ready"; exit 1; fi`,
  [
    "AGENT_ID=playwright-agent",
    "AGENT_TOKEN=playwright-agent-token",
    `MANAGEMENT_URL=https://127.0.0.1:${managementPort}`,
    `MANAGEMENT_CA_FILE="$CA_FILE"`,
    `${shellQuote(e2eBinary)} agent & AGENT_PID=$!`,
  ].join(" "),
  `cd ${shellQuote(managementDir)}`,
  [
    `VITE_MANAGEMENT_PROXY_TARGET=https://127.0.0.1:${managementPort}`,
    "VITE_MANAGEMENT_PROXY_SECURE=false",
    "VITE_HMR_PROTOCOL=wss",
    "VITE_HMR_HOST=localhost",
    `VITE_HMR_CLIENT_PORT=${managementPort}`,
    "bun run dev & FRONTEND_PID=$!",
  ].join(" "),
  `wait "$SERVER_PID" "$AGENT_PID" "$FRONTEND_PID"`,
].join("\n");

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  timeout: 60_000,
  expect: {
    timeout: 10_000,
  },
  webServer: {
    command: webServerCommand,
    url: "http://127.0.0.1:5173",
    timeout: 120_000,
    reuseExistingServer: false,
    stdout: "pipe",
    stderr: "pipe",
  },
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
      use: { baseURL: "http://127.0.0.1:5173" },
    },
  ],
});
