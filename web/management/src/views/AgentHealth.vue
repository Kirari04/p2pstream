<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import BanIcon from "@primevue/icons/ban";
import CheckIcon from "@primevue/icons/check";
import PencilIcon from "@primevue/icons/pencil";
import PlusIcon from "@primevue/icons/plus";
import RefreshIcon from "@primevue/icons/refresh";
import TrashIcon from "@primevue/icons/trash";
import { managementClient } from "@/api/managementClient";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import Tag from "@/volt/Tag.vue";
import type {
  Agent,
  GetDashboardResponse,
  GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

type Runner = (action: () => Promise<void>) => Promise<boolean>;

const dashboard = inject<ComputedRef<GetDashboardResponse | null>>("dashboard");
const publicProxyConfig = inject<ComputedRef<GetPublicProxyConfigResponse | null>>("publicProxyConfig");
const runManagementAction = inject<Runner>("runManagementAction");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const status = computed(() => dashboard?.value?.status ?? null);
const agents = computed(() => publicProxyConfig?.value?.agents ?? []);
const oneHourWindow = computed(() => dashboard?.value?.windows.find((w) => w.label === "1h"));
const dayWindow = computed(() => dashboard?.value?.windows.find((w) => w.label === "24h"));

const isAgentModalOpen = ref(false);
const rotateAgentToConfirm = ref<Agent | null>(null);
const issuedToken = ref("");
const issuedAgent = ref<Agent | null>(null);
const setupManagementUrl = ref(defaultManagementUrl());
const setupDockerImage = ref("p2pstream:local");
const setupTab = ref<"systemd" | "docker" | "cli">("systemd");
const setupCopyLabel = ref("Copy");
let setupCopyReset: number | undefined;

const agentForm = reactive({
  id: "",
  name: "",
  enabled: true,
});

const normalizedManagementUrl = computed(() => setupManagementUrl.value.trim().replace(/\/+$/, ""));
const setupSnippet = computed(() => {
  if (!issuedAgent.value) return "";
  switch (setupTab.value) {
    case "docker":
      return dockerComposeSnippet();
    case "cli":
      return cliSnippet();
    default:
      return systemdSnippet();
  }
});

function bigIntLabel(value: bigint | undefined): string {
  if (value === undefined) return "0";
  return new Intl.NumberFormat().format(Number(value));
}

function formatDate(value: bigint | undefined): string {
  if (value === undefined || value === 0n) return "-";
  return new Date(Number(value)).toLocaleString();
}

function resetAgentForm() {
  agentForm.id = "";
  agentForm.name = "";
  agentForm.enabled = true;
}

function openAddAgentModal() {
  resetAgentForm();
  isAgentModalOpen.value = true;
}

function editAgent(agent: Agent) {
  agentForm.id = agent.id.toString();
  agentForm.name = agent.name;
  agentForm.enabled = agent.enabled;
  isAgentModalOpen.value = true;
}

async function run(action: () => Promise<void>): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action);
}

async function submitAgent() {
  await run(async () => {
    if (agentForm.id) {
      await managementClient.updateAgent({
        id: BigInt(agentForm.id),
        name: agentForm.name,
        enabled: agentForm.enabled,
      });
    } else {
      const resp = await managementClient.createAgent({
        name: agentForm.name,
        enabled: agentForm.enabled,
      });
      openSetupModal(resp.agent ?? null, resp.token);
    }
    isAgentModalOpen.value = false;
  });
}

async function setAgentEnabled(agent: Agent, enabled: boolean) {
  await run(async () => {
    await managementClient.updateAgent({
      id: agent.id,
      name: agent.name,
      enabled,
    });
  });
}

function rotateAgentToken(agent: Agent) {
  rotateAgentToConfirm.value = agent;
}

function closeRotateAgentModal() {
  rotateAgentToConfirm.value = null;
}

async function confirmRotateAgentToken() {
  const agent = rotateAgentToConfirm.value;
  if (!agent) return;
  const ok = await run(async () => {
    const resp = await managementClient.rotateAgentToken({ id: agent.id });
    openSetupModal(resp.agent ?? agent, resp.token);
  });
  if (ok) {
    closeRotateAgentModal();
  }
}

async function deleteAgent(agent: Agent) {
  if (!window.confirm("Delete this agent?")) return;
  await run(async () => {
    await managementClient.deleteAgent({ id: agent.id });
  });
}

function clearIssuedToken() {
  issuedToken.value = "";
  issuedAgent.value = null;
  setupCopyLabel.value = "Copy";
}

function openSetupModal(agent: Agent | null, token: string) {
  if (!agent || !token) return;
  issuedAgent.value = agent;
  issuedToken.value = token;
  setupManagementUrl.value = defaultManagementUrl();
  setupDockerImage.value = "p2pstream:local";
  setupTab.value = "systemd";
  setupCopyLabel.value = "Copy";
}

function defaultManagementUrl(): string {
  const url = new URL(window.location.origin);
  if (url.port === "5173") {
    url.port = "8081";
  }
  return url.toString().replace(/\/$/, "");
}

function shellQuote(value: string): string {
  if (value === "") return "''";
  return "'" + value.replace(/'/g, "'\\''") + "'";
}

function envQuote(value: string): string {
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, "\\\"").replace(/\r?\n/g, "")}"`;
}

function yamlQuote(value: string): string {
  return JSON.stringify(value);
}

function systemdSnippet(): string {
  if (!issuedAgent.value) return "";
  return `sudo install -d -m 0755 /etc/p2pstream
sudo tee /etc/p2pstream/agent.env >/dev/null <<'EOF'
MANAGEMENT_URL=${envQuote(normalizedManagementUrl.value)}
AGENT_ID=${envQuote(issuedAgent.value.publicId)}
AGENT_TOKEN=${envQuote(issuedToken.value)}
EOF

sudo tee /etc/systemd/system/p2pstream-agent.service >/dev/null <<'EOF'
[Unit]
Description=p2pstream agent
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/p2pstream/agent.env
ExecStart=/usr/local/bin/p2pstream agent
Restart=always
RestartSec=5s
User=root

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now p2pstream-agent`;
}

function dockerComposeSnippet(): string {
  if (!issuedAgent.value) return "";
  return `services:
  p2pstream-agent:
    image: ${yamlQuote(setupDockerImage.value.trim() || "p2pstream:local")}
    command: ["/app/p2pstream", "agent"]
    environment:
      MANAGEMENT_URL: ${yamlQuote(normalizedManagementUrl.value)}
      AGENT_ID: ${yamlQuote(issuedAgent.value.publicId)}
      AGENT_TOKEN: ${yamlQuote(issuedToken.value)}
    restart: unless-stopped`;
}

function cliSnippet(): string {
  if (!issuedAgent.value) return "";
  return `MANAGEMENT_URL=${shellQuote(normalizedManagementUrl.value)} AGENT_ID=${shellQuote(issuedAgent.value.publicId)} AGENT_TOKEN=${shellQuote(issuedToken.value)} p2pstream agent`;
}

async function copySetupSnippet() {
  try {
    await navigator.clipboard.writeText(setupSnippet.value);
    setupCopyLabel.value = "Copied";
  } catch {
    setupCopyLabel.value = "Select text";
  }
  if (setupCopyReset !== undefined) {
    window.clearTimeout(setupCopyReset);
  }
  setupCopyReset = window.setTimeout(() => {
    setupCopyLabel.value = "Copy";
  }, 1500);
}
</script>

<template>
  <div v-if="dashboard && status" class="space-y-8">
    <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
      <div>
        <h3 class="text-xl font-bold mb-2">Agent Health</h3>
        <p class="text-[#888] text-sm">Registered agents, connection state, and recent runtime metrics.</p>
      </div>
      <SecondaryButton size="small" label="Add Agent" :disabled="isBusy" @click="openAddAgentModal">
        <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
      </SecondaryButton>
    </div>

    <div class="grid gap-6 sm:grid-cols-2 lg:grid-cols-3">
      <div class="vercel-card p-6">
        <p class="vercel-card-title">Memory Usage (Sys)</p>
        <span class="vercel-card-value">{{ bigIntLabel(status.latestAgentStats?.memorySysMb) }} MB</span>
      </div>
      <div class="vercel-card p-6">
        <p class="vercel-card-title">Goroutines</p>
        <span class="vercel-card-value">{{ bigIntLabel(status.latestAgentStats?.numGoroutine) }}</span>
      </div>
      <div class="vercel-card p-6">
        <p class="vercel-card-title">Active Requests</p>
        <span class="vercel-card-value">{{ status.latestAgentStats?.activeRequests ?? 0 }}</span>
      </div>
      <div class="vercel-card p-6">
        <p class="vercel-card-title">Avg Memory (1h)</p>
        <span class="vercel-card-value">{{ bigIntLabel(oneHourWindow?.agentAvgMemoryMb) }} MB</span>
      </div>
      <div class="vercel-card p-6">
        <p class="vercel-card-title">Max Memory (24h)</p>
        <span class="vercel-card-value">{{ bigIntLabel(dayWindow?.agentMaxMemoryMb) }} MB</span>
      </div>
      <div class="vercel-card p-6">
        <p class="vercel-card-title">Max Goroutines (24h)</p>
        <span class="vercel-card-value">{{ bigIntLabel(dayWindow?.agentMaxGoroutines) }}</span>
      </div>
    </div>

    <section class="vercel-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4">
        <h4 class="text-sm font-semibold text-[#888] uppercase tracking-widest">Registered Agents</h4>
      </div>
      <div class="overflow-x-auto">
        <table class="w-full min-w-[980px] text-sm">
          <thead class="border-b border-[#333] text-left text-xs uppercase tracking-wider text-[#888]">
            <tr>
              <th class="px-5 py-3">Agent</th>
              <th class="px-5 py-3">State</th>
              <th class="px-5 py-3">Active</th>
              <th class="px-5 py-3">Last Connected</th>
              <th class="px-5 py-3">Latest Stats</th>
              <th class="px-5 py-3 text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="agent in agents" :key="agent.id.toString()" class="border-b border-[#1f1f1f] last:border-0">
              <td class="px-5 py-4">
                <p class="font-medium text-white">{{ agent.name }}</p>
                <p class="font-mono text-xs text-[#888]">{{ agent.publicId }}</p>
              </td>
              <td class="px-5 py-4">
                <div class="flex items-center gap-2">
                  <Tag :value="agent.connected ? 'Connected' : 'Offline'" :severity="agent.connected ? 'success' : 'warn'" class="!bg-[#111] !border-[#333] !text-white" />
                  <Tag v-if="!agent.enabled" value="Disabled" severity="warn" class="!bg-[#111] !border-[#333] !text-white" />
                </div>
              </td>
              <td class="px-5 py-4 font-mono text-xs text-[#d4d4d8]">{{ agent.activeRequests.toString() }}</td>
              <td class="px-5 py-4 font-mono text-xs text-[#d4d4d8]">{{ formatDate(agent.lastConnectedAtUnixMillis) }}</td>
              <td class="px-5 py-4 font-mono text-xs text-[#d4d4d8]">
                {{ bigIntLabel(agent.latestStats?.memorySysMb) }} MB /
                {{ bigIntLabel(agent.latestStats?.numGoroutine) }} goroutines
              </td>
              <td class="px-5 py-4">
                <div class="flex justify-end gap-2">
                  <SecondaryButton
                    size="small"
                    :aria-label="agent.enabled ? 'Disable agent' : 'Enable agent'"
                    :title="agent.enabled ? 'Disable agent' : 'Enable agent'"
                    :disabled="isBusy"
                    @click="setAgentEnabled(agent, !agent.enabled)"
                  >
                    <template #icon>
                      <BanIcon v-if="agent.enabled" class="h-3.5 w-3.5" />
                      <CheckIcon v-else class="h-3.5 w-3.5" />
                    </template>
                  </SecondaryButton>
                  <SecondaryButton size="small" aria-label="Rotate token" title="Rotate token" :disabled="isBusy" @click="rotateAgentToken(agent)">
                    <template #icon><RefreshIcon class="h-3.5 w-3.5" /></template>
                  </SecondaryButton>
                  <SecondaryButton size="small" aria-label="Edit agent" title="Edit agent" :disabled="isBusy" @click="editAgent(agent)">
                    <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
                  </SecondaryButton>
                  <DangerButton size="small" aria-label="Delete agent" title="Delete agent" :disabled="isBusy || agent.connected" @click="deleteAgent(agent)">
                    <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
                  </DangerButton>
                </div>
              </td>
            </tr>
            <tr v-if="!agents.length">
              <td colspan="6" class="px-5 py-8 text-center text-sm text-[#888]">No agents registered.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <div class="vercel-card p-8">
      <h4 class="text-sm font-semibold text-[#888] uppercase tracking-widest mb-6">Connection History</h4>
      <div class="grid gap-6 sm:grid-cols-2">
        <div class="flex flex-col">
          <span class="text-xs text-[#666] mb-1">Last Connected</span>
          <span class="text-sm font-mono">{{ formatDate(dashboard.agentConnections?.lastConnectedAtUnixMillis) }}</span>
        </div>
        <div class="flex flex-col">
          <span class="text-xs text-[#666] mb-1">Last Disconnected</span>
          <span class="text-sm font-mono">{{ formatDate(dashboard.agentConnections?.lastDisconnectedAtUnixMillis) }}</span>
        </div>
        <div class="flex flex-col">
          <span class="text-xs text-[#666] mb-1">Total Connections</span>
          <span class="text-sm font-mono">{{ bigIntLabel(dashboard.agentConnections?.totalConnections) }}</span>
        </div>
        <div class="flex flex-col">
          <span class="text-xs text-[#666] mb-1">Active Since</span>
          <span class="text-sm font-mono">{{ formatDate(dashboard.agentConnections?.activeConnectedAtUnixMillis) }}</span>
        </div>
      </div>
    </div>

    <Modal v-model="isAgentModalOpen" :title="agentForm.id ? 'Edit Agent' : 'Add Agent'" max-width="32rem">
      <form @submit.prevent="submitAgent" class="grid gap-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Name
          <input v-model="agentForm.name" class="vercel-input text-sm normal-case tracking-normal" required />
        </label>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
          <input v-model="agentForm.enabled" type="checkbox" class="h-4 w-4 accent-white" />
          Enabled
        </label>
        <div class="mt-4 flex justify-end gap-3">
          <SecondaryButton type="button" label="Cancel" @click="isAgentModalOpen = false" />
          <Button class="!bg-white !text-black !border-white" :label="agentForm.id ? 'Save Changes' : 'Create Agent'" type="submit" :disabled="isBusy" />
        </div>
      </form>
    </Modal>

    <Modal :model-value="Boolean(rotateAgentToConfirm)" title="Rotate Agent Token" max-width="34rem" @update:model-value="closeRotateAgentModal">
      <div v-if="rotateAgentToConfirm" class="grid gap-5">
        <div class="grid gap-2">
          <p class="text-sm text-white">Rotate the token for {{ rotateAgentToConfirm.name }}?</p>
          <p class="text-sm leading-6 text-[#888]">
            The new token will be shown once. Existing connected sockets can keep running until they disconnect, but future connections and stats reports must use the new token.
          </p>
        </div>
        <div class="rounded-md border border-[#333] bg-[#0b0b0b] p-3">
          <span class="mb-1 block text-xs font-medium uppercase tracking-wider text-[#888]">Agent ID</span>
          <code class="block overflow-x-auto font-mono text-xs text-white">{{ rotateAgentToConfirm.publicId }}</code>
        </div>
        <div class="flex justify-end gap-3">
          <SecondaryButton type="button" label="Cancel" :disabled="isBusy" @click="closeRotateAgentModal" />
          <DangerButton type="button" label="Rotate Token" :disabled="isBusy" @click="confirmRotateAgentToken" />
        </div>
      </div>
    </Modal>

    <Modal :model-value="Boolean(issuedToken && issuedAgent)" title="Agent Setup" max-width="48rem" @update:model-value="clearIssuedToken">
      <div v-if="issuedAgent" class="grid gap-5">
        <div class="grid gap-3 md:grid-cols-2">
          <div class="grid gap-1.5">
            <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Agent</span>
            <span class="rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-2 text-sm text-white">{{ issuedAgent.name }}</span>
          </div>
          <div class="grid gap-1.5">
            <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Generated ID</span>
            <code class="overflow-x-auto rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-2 font-mono text-xs text-white">{{ issuedAgent.publicId }}</code>
          </div>
        </div>

        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          One-Time Token
          <code class="block break-all rounded-md border border-[#333] bg-[#0b0b0b] p-3 font-mono text-xs text-white">{{ issuedToken }}</code>
        </label>

        <div class="grid gap-3 md:grid-cols-[minmax(0,1fr)_14rem]">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Management URL
            <input v-model="setupManagementUrl" class="vercel-input text-sm normal-case tracking-normal" required />
          </label>
          <label v-if="setupTab === 'docker'" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Docker Image
            <input v-model="setupDockerImage" class="vercel-input text-sm normal-case tracking-normal" required />
          </label>
        </div>

        <div class="flex flex-wrap gap-2" role="tablist" aria-label="Agent setup format">
          <button
            type="button"
            class="rounded-md border px-3 py-2 text-sm transition"
            :class="setupTab === 'systemd' ? 'border-white bg-white text-black' : 'border-[#333] bg-[#0b0b0b] text-[#d4d4d8] hover:border-[#555] hover:text-white'"
            @click="setupTab = 'systemd'"
          >
            systemd
          </button>
          <button
            type="button"
            class="rounded-md border px-3 py-2 text-sm transition"
            :class="setupTab === 'docker' ? 'border-white bg-white text-black' : 'border-[#333] bg-[#0b0b0b] text-[#d4d4d8] hover:border-[#555] hover:text-white'"
            @click="setupTab = 'docker'"
          >
            Docker Compose
          </button>
          <button
            type="button"
            class="rounded-md border px-3 py-2 text-sm transition"
            :class="setupTab === 'cli' ? 'border-white bg-white text-black' : 'border-[#333] bg-[#0b0b0b] text-[#d4d4d8] hover:border-[#555] hover:text-white'"
            @click="setupTab = 'cli'"
          >
            CLI
          </button>
        </div>

        <pre class="max-h-[360px] overflow-auto rounded-md border border-[#333] bg-[#050505] p-4 text-xs leading-5 text-white"><code>{{ setupSnippet }}</code></pre>

        <div class="flex justify-end gap-3">
          <SecondaryButton type="button" :label="setupCopyLabel" @click="copySetupSnippet" />
          <Button class="!bg-white !text-black !border-white" label="Done" @click="clearIssuedToken" />
        </div>
      </div>
    </Modal>
  </div>
</template>
