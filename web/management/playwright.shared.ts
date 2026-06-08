import type { PlaywrightTestConfig } from "@playwright/test";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const managementDir = dirname(fileURLToPath(import.meta.url));
export const repoDir = resolve(managementDir, "../..");

export type ManagementWebServerOptions = {
  managementPort: string;
  frontendPort: string;
  dataDir: string;
  cacheDir: string;
  binaryPath: string;
  setupToken: string;
  agentId: string;
  agentName: string;
  agentToken: string;
  managementPublicURL?: string;
  cleanupPorts?: string[];
  timeout?: number;
};

export function shellQuote(value: string): string {
  return `'${value.replaceAll("'", "'\\''")}'`;
}

export function sqliteDatabaseURL(dataDir: string): string {
  return `file:${dataDir}/p2pstream.db?_busy_timeout=10000&_fk=1&_journal_mode=WAL&_synchronous=NORMAL&cache=private&mode=rwc`;
}

export function createManagementWebServer(options: ManagementWebServerOptions): NonNullable<PlaywrightTestConfig["webServer"]> {
  const databaseURL = sqliteDatabaseURL(options.dataDir);
  const caFile = resolve(options.dataDir, "certs/management/ca.crt.pem");
  const cleanupPorts = Array.from(new Set(options.cleanupPorts ?? [options.managementPort]))
    .filter((port) => port.trim() !== "");
  const publicURLPart = options.managementPublicURL
    ? [`MANAGEMENT_PUBLIC_URL=${shellQuote(options.managementPublicURL)}`]
    : [];
  const backendCommand = [
    "set -e",
    cleanupPortsCommand(cleanupPorts),
    `cleanup_ports TERM`,
    `sleep 0.2`,
    `cleanup_ports KILL`,
    `rm -rf ${shellQuote(options.dataDir)} ${shellQuote(options.cacheDir)}`,
    `mkdir -p ${shellQuote(options.dataDir)} ${shellQuote(options.cacheDir)} ${shellQuote(resolve(repoDir, "tmp"))}`,
    `cd ${shellQuote(repoDir)}`,
    "make frontend-install generate-proto",
    `go build -o ${shellQuote(options.binaryPath)} .`,
    `cleanup() { trap - INT TERM EXIT; kill \${FRONTEND_PID:-} \${AGENT_PID:-} \${SERVER_PID:-} 2>/dev/null || true; for pid in \${FRONTEND_PID:-} \${AGENT_PID:-} \${SERVER_PID:-}; do if [ -n "$pid" ]; then wait "$pid" 2>/dev/null || true; fi; done; }`,
    `trap "cleanup; exit 0" INT TERM`,
    `trap "cleanup" EXIT`,
    [
      `CONFIG_DIR=${shellQuote(options.dataDir)}`,
      `DATABASE_URL=${shellQuote(databaseURL)}`,
      `PUBLIC_CACHE_DIR=${shellQuote(options.cacheDir)}`,
      `MANAGEMENT_PORT=${options.managementPort}`,
      `MANAGEMENT_SETUP_TOKEN=${shellQuote(options.setupToken)}`,
      `BOOTSTRAP_AGENT_ID=${shellQuote(options.agentId)}`,
      `BOOTSTRAP_AGENT_NAME=${shellQuote(options.agentName)}`,
      `BOOTSTRAP_AGENT_TOKEN=${shellQuote(options.agentToken)}`,
      `MANAGEMENT_UI_DEV_PROXY=http://127.0.0.1:${options.frontendPort}`,
      ...publicURLPart,
      "ENV=development",
      `${shellQuote(options.binaryPath)} server & SERVER_PID=$!`,
    ].join(" "),
    `CA_FILE=${shellQuote(caFile)}`,
    `ready=0; for i in $(seq 1 100); do if [ -s "$CA_FILE" ] && curl --cacert "$CA_FILE" -fsS -H 'Content-Type: application/json' --data '{}' https://127.0.0.1:${options.managementPort}/p2pstream.v1.AgentManagementService/GetSetupState >/dev/null 2>&1; then ready=1; break; fi; sleep 0.2; done; if [ "$ready" != 1 ]; then echo "management server did not become ready"; exit 1; fi`,
    [
      `AGENT_ID=${shellQuote(options.agentId)}`,
      `AGENT_TOKEN=${shellQuote(options.agentToken)}`,
      `MANAGEMENT_URL=https://127.0.0.1:${options.managementPort}`,
      `MANAGEMENT_CA_FILE="$CA_FILE"`,
      `${shellQuote(options.binaryPath)} agent & AGENT_PID=$!`,
    ].join(" "),
    `wait "$SERVER_PID" "$AGENT_PID"`,
  ].join("\n");

  const frontendCommand = [
    "set -e",
    cleanupPortsCommand([options.frontendPort]),
    `cleanup_ports TERM`,
    `sleep 0.2`,
    `cleanup_ports KILL`,
    `cd ${shellQuote(managementDir)}`,
    `unset FORCE_COLOR; export NO_COLOR=1`,
    [
      `VITE_MANAGEMENT_PROXY_TARGET=https://127.0.0.1:${options.managementPort}`,
      "VITE_MANAGEMENT_PROXY_SECURE=false",
      "VITE_HMR_PROTOCOL=wss",
      "VITE_HMR_HOST=localhost",
      `VITE_HMR_CLIENT_PORT=${options.managementPort}`,
      `node node_modules/vite/bin/vite.js --host 127.0.0.1 --port ${options.frontendPort} --strictPort`,
    ].join(" "),
  ].join("\n");

  return [
    {
      name: "management",
      command: backendCommand,
      port: Number(options.managementPort),
      timeout: options.timeout ?? 120_000,
      reuseExistingServer: false,
      stdout: "pipe",
      stderr: "pipe",
      gracefulShutdown: { signal: "SIGTERM", timeout: 1_000 },
    },
    {
      name: "frontend",
      command: frontendCommand,
      url: `http://127.0.0.1:${options.frontendPort}`,
      timeout: options.timeout ?? 120_000,
      reuseExistingServer: false,
      stdout: "pipe",
      stderr: "pipe",
      gracefulShutdown: { signal: "SIGTERM", timeout: 1_000 },
    },
  ];
}

function cleanupPortsCommand(ports: string[]): string {
  const cleanupPortArgs = ports.map(shellQuote).join(" ");
  return `cleanup_ports() { signal="$1"; if command -v ss >/dev/null 2>&1; then for port in ${cleanupPortArgs}; do pids=$(ss -H -ltnp "sport = :$port" 2>/dev/null | sed -n 's/.*pid=\\([0-9][0-9]*\\).*/\\1/p' | sort -u); [ -z "$pids" ] || kill "-$signal" $pids 2>/dev/null || true; done; fi; }`;
}
