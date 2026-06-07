<script setup lang="ts">
import { computed, inject, ref, watch } from "vue";
import type { ComputedRef } from "vue";
import BanIcon from "@primevue/icons/ban";
import CheckIcon from "@primevue/icons/check";
import PencilIcon from "@primevue/icons/pencil";
import PlusIcon from "@primevue/icons/plus";
import RefreshIcon from "@primevue/icons/refresh";
import TimesCircleIcon from "@primevue/icons/timescircle";
import TrashIcon from "@primevue/icons/trash";
import { useManagementClient } from "@/composables/useManagementClient";
import ConfirmDialog from "@/components/ConfirmDialog.vue";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import AgentEditorModal from "@/components/editors/AgentEditorModal.vue";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { AGENT_ID_SYSTEM_LABEL_KEY, userAgentLabelPairs } from "@/lib/agentLabels";
import {
  FALLBACK_RELEASE_REPOSITORY,
  cliSnippet as buildCliSnippet,
  dockerComposeSnippet as buildDockerComposeSnippet,
  dockerImageForRepository,
  linuxInstallSnippet,
  linuxUninstallSnippet,
  normalizeManagementUrl as normalizeSetupManagementUrl,
} from "@/lib/agentSetupSnippets";
import {
  agentUptimeSummaryById,
  fleetUptimePercent,
  formatLongDuration,
  formatPercent,
  recentDisconnectCount,
} from "@/lib/dashboardStats";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import Tag from "@/volt/Tag.vue";
import type {
  Agent,
  AgentConnectionSession,
  AgentUptimeSummary,
  GetDashboardResponse,
  GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

type Runner = (action: () => Promise<void>) => Promise<boolean>;

const dashboard = inject<ComputedRef<GetDashboardResponse | null>>("dashboard");
const publicProxyConfig = inject<ComputedRef<GetPublicProxyConfigResponse | null>>("publicProxyConfig");
const runManagementAction = inject<Runner>("runManagementAction");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const { state: confirmState, confirm, handleConfirm: onConfirm, handleCancel: onCancel } = useConfirmDialog();
const status = computed(() => dashboard?.value?.status ?? null);
const config = computed(() => publicProxyConfig?.value ?? null);
const agents = computed(() => publicProxyConfig?.value?.agents ?? []);
const oneHourWindow = computed(() => dashboard?.value?.windows.find((w) => w.label === "1h"));
const dayWindow = computed(() => dashboard?.value?.windows.find((w) => w.label === "24h"));
const managementSecurity = computed(() => dashboard?.value?.managementSecurity ?? null);
const uptimeSummaries = computed(() => dashboard?.value?.agentUptimeSummaries ?? []);
const uptimeByAgentId = computed(() => agentUptimeSummaryById(uptimeSummaries.value));
const recentAgentConnections = computed(() => dashboard?.value?.recentAgentConnections ?? []);
const enabledAgents = computed(() => agents.value.filter((agent) => agent.enabled).length);
const connectedAgentCount = computed(() => uptimeSummaries.value.length
  ? uptimeSummaries.value.filter((summary) => summary.connected).length
  : agents.value.filter((agent) => agent.connected).length);
const activeAgentRequests = computed(() => agents.value.reduce((sum, agent) => sum + Number(agent.activeRequests || 0n), 0));
const fleetUptime = computed(() => fleetUptimePercent(uptimeSummaries.value));
const longestCurrentUptimeMillis = computed(() => Math.max(0, ...uptimeSummaries.value.map((summary) => Number(summary.currentUptimeMillis || 0n))));
const recentDisconnects = computed(() => recentDisconnectCount(recentAgentConnections.value, dashboard?.value?.generatedAtUnixMillis ?? BigInt(Date.now())));
const retentionDaysLabel = computed(() => `${(dashboard?.value?.retentionDays ?? 30n).toString()}d`);

const agentEditor = ref<InstanceType<typeof AgentEditorModal> | null>(null);
const rotateAgentToConfirm = ref<Agent | null>(null);
const issuedToken = ref("");
const issuedAgent = ref<Agent | null>(null);
const setupManagementUrl = ref(defaultManagementUrl());
const setupManagementCAFile = ref("");
const setupAgentTLSCertFile = ref("/etc/p2pstream/agent.crt.pem");
const setupAgentTLSKeyFile = ref("/etc/p2pstream/agent.key.pem");
const setupAllowInsecureManagement = ref(false);
const setupReleaseRepository = ref(defaultReleaseRepository());
const setupDockerImage = ref(dockerImageForRepository(setupReleaseRepository.value));
const setupDockerImageTouched = ref(false);
const setupTab = ref<"install" | "docker" | "cli">("install");
const setupCopyLabel = ref("Copy");
const uninstallAgent = ref<Agent | null>(null);
const uninstallReleaseRepository = ref(defaultReleaseRepository());
const uninstallCopyLabel = ref("Copy");
let setupCopyReset: number | undefined;
let uninstallCopyReset: number | undefined;

const busyDisabledReason = computed(() => isBusy?.value ? BUSY_REASON : "");
const normalizedManagementUrl = computed(() => normalizeSetupManagementUrl(setupManagementUrl.value));
const managementUsesTLS = computed(() => normalizedManagementUrl.value.toLowerCase().startsWith("https://"));
const agentClientCertificateRequired = computed(() => Boolean(managementSecurity.value?.agentClientCertificateRequired));
const embeddedManagementCAPEMBase64 = computed(() => {
  const pem = managementSecurity.value?.managementCaPem ?? "";
  if (!pem || !managementUsesTLS.value) return "";
  return window.btoa(pem);
});
const setupSnippetError = computed(() => {
  try {
    buildSetupSnippet();
    return "";
  } catch (err) {
    return err instanceof Error ? err.message : "Agent setup values are invalid.";
  }
});
const setupSnippet = computed(() => {
  if (setupSnippetError.value) return "";
  return buildSetupSnippet();
});
const uninstallSnippetError = computed(() => {
  try {
    buildUninstallSnippet();
    return "";
  } catch (err) {
    return err instanceof Error ? err.message : "Agent uninstall values are invalid.";
  }
});
const uninstallSnippet = computed(() => {
  if (uninstallSnippetError.value) return "";
  return buildUninstallSnippet();
});

function buildSetupSnippet(): string {
  if (!issuedAgent.value) return "";
  switch (setupTab.value) {
    case "docker":
      return dockerComposeSnippet();
    case "cli":
      return cliSnippet();
    default:
      return linuxInstallerSnippet();
  }
}

watch(setupReleaseRepository, (repository) => {
  if (!setupDockerImageTouched.value) {
    setupDockerImage.value = dockerImageForRepository(repository);
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

function uptimeForAgent(agent: Agent): AgentUptimeSummary | null {
  return uptimeByAgentId.value.get(agent.id.toString()) ?? null;
}

function agentConnected(agent: Agent): boolean {
  return uptimeForAgent(agent)?.connected ?? agent.connected;
}

function currentAgentDuration(agent: Agent): string {
  const uptime = uptimeForAgent(agent);
  if (!uptime) return "-";
  return uptime.connected
    ? formatLongDuration(uptime.currentUptimeMillis)
    : formatLongDuration(uptime.currentDowntimeMillis);
}

function currentAgentDurationKind(agent: Agent): string {
  return agentConnected(agent) ? "Uptime" : "Offline";
}

function agentUptimePercentLabel(agent: Agent): string {
  return formatPercent(uptimeForAgent(agent)?.uptimePercent);
}

function agentConnectionCounts(agent: Agent): string {
  const uptime = uptimeForAgent(agent);
  if (!uptime) return "-";
  return `${uptime.connectionCount.toString()} / ${uptime.disconnectCount.toString()}`;
}

function agentLastConnected(agent: Agent): bigint | undefined {
  const value = uptimeForAgent(agent)?.lastConnectedAtUnixMillis ?? agent.lastConnectedAtUnixMillis;
  return value === 0n ? undefined : value;
}

function agentLastDisconnected(agent: Agent): bigint | undefined {
  const value = uptimeForAgent(agent)?.lastDisconnectedAtUnixMillis ?? agent.lastDisconnectedAtUnixMillis;
  return value === 0n ? undefined : value;
}

function sessionAgentLabel(session: AgentConnectionSession): string {
  if (session.agentName) return session.agentName;
  if (session.agentPublicId) return session.agentPublicId;
  return session.agentId > 0n ? `agent #${session.agentId.toString()}` : "Unknown agent";
}

function sessionAgentDetail(session: AgentConnectionSession): string {
  if (session.agentName && session.agentPublicId) return session.agentPublicId;
  return session.agentId > 0n ? `agent #${session.agentId.toString()}` : "";
}

function openAddAgentModal() {
  agentEditor.value?.openCreate();
}

function editAgent(agent: Agent) {
  agentEditor.value?.openEdit(agent.id);
}

function agentUserLabels(agent: Agent) {
  return userAgentLabelPairs(agent.labels);
}

function exactAgentSelector(agent: Agent): string {
  const value = agent.labels[AGENT_ID_SYSTEM_LABEL_KEY] || agent.publicId;
  return `${AGENT_ID_SYSTEM_LABEL_KEY}=${value}`;
}

function openUninstallModal(agent: Agent) {
  uninstallAgent.value = agent;
  uninstallReleaseRepository.value = defaultReleaseRepository();
  uninstallCopyLabel.value = "Copy";
}

function closeUninstallModal() {
  uninstallAgent.value = null;
  uninstallCopyLabel.value = "Copy";
}

function deleteAgentDisabledReason(agent: Agent): string {
  if (isBusy?.value) return BUSY_REASON;
  if (agent.connected) return "Disconnect this agent before deleting it.";
  return "";
}

async function run(action: () => Promise<void>): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action);
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
  if (!await confirm("Delete Agent", "This agent and its backend assignments will be permanently removed.")) return;
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
  setupManagementCAFile.value = "";
  setupAgentTLSCertFile.value = "/etc/p2pstream/agent.crt.pem";
  setupAgentTLSKeyFile.value = "/etc/p2pstream/agent.key.pem";
  setupAllowInsecureManagement.value = false;
  setupReleaseRepository.value = defaultReleaseRepository();
  setupDockerImage.value = dockerImageForRepository(setupReleaseRepository.value);
  setupDockerImageTouched.value = false;
  setupTab.value = "install";
  setupCopyLabel.value = "Copy";
}

function handleAgentCreated(payload: { agent: Agent | null; token: string }) {
  openSetupModal(payload.agent, payload.token);
}

function defaultManagementUrl(): string {
  const configured = managementSecurity.value?.defaultManagementUrl;
  if (configured) {
    return configured.replace(/\/+$/, "");
  }
  const url = new URL(window.location.origin);
  url.protocol = "https:";
  if (url.port === "5173") {
    url.port = "8081";
  } else if (!url.port) {
    url.port = "8081";
  }
  return url.toString().replace(/\/$/, "");
}

function defaultReleaseRepository(): string {
  const configured = import.meta.env.VITE_RELEASE_REPOSITORY;
  return typeof configured === "string" && configured.trim() ? configured.trim() : FALLBACK_RELEASE_REPOSITORY;
}

function linuxInstallerSnippet(): string {
  if (!issuedAgent.value) return "";
  return linuxInstallSnippet(setupSnippetInput());
}

function dockerComposeSnippet(): string {
  if (!issuedAgent.value) return "";
  return buildDockerComposeSnippet(setupSnippetInput());
}

function cliSnippet(): string {
  if (!issuedAgent.value) return "";
  return buildCliSnippet(setupSnippetInput());
}

function buildUninstallSnippet(): string {
  return linuxUninstallSnippet({ repository: uninstallReleaseRepository.value });
}

function setupSnippetInput() {
  return {
    managementUrl: normalizedManagementUrl.value,
    agentId: issuedAgent.value?.publicId ?? "",
    agentToken: issuedToken.value,
    repository: setupReleaseRepository.value,
    dockerImage: setupDockerImage.value,
    tls: {
      enabled: managementUsesTLS.value,
      managementCAFile: embeddedManagementCAPEMBase64.value ? "" : setupManagementCAFile.value,
      managementCAPEMBase64: embeddedManagementCAPEMBase64.value,
      agentTLSCertFile: agentClientCertificateRequired.value ? setupAgentTLSCertFile.value : "",
      agentTLSKeyFile: agentClientCertificateRequired.value ? setupAgentTLSKeyFile.value : "",
      allowInsecureManagement: setupAllowInsecureManagement.value,
    },
  };
}

async function copySetupSnippet() {
  if (setupSnippetError.value) {
    setupCopyLabel.value = "Invalid";
    return;
  }
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

async function copyUninstallSnippet() {
  if (uninstallSnippetError.value) {
    uninstallCopyLabel.value = "Invalid";
    return;
  }
  try {
    await navigator.clipboard.writeText(uninstallSnippet.value);
    uninstallCopyLabel.value = "Copied";
  } catch {
    uninstallCopyLabel.value = "Select text";
  }
  if (uninstallCopyReset !== undefined) {
    window.clearTimeout(uninstallCopyReset);
  }
  uninstallCopyReset = window.setTimeout(() => {
    uninstallCopyLabel.value = "Copy";
  }, 1500);
}
</script>

<template>
  <div v-if="dashboard && status" class="space-y-8">
    <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
      <div>
        <h3 class="text-xl font-bold mb-2">Agents</h3>
        <p class="text-[#888] text-sm">Registered agents, connection state, and recent runtime metrics.</p>
      </div>
      <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
        <SecondaryButton size="small" label="Add Agent" :disabled="Boolean(busyDisabledReason)" @click="openAddAgentModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </DisabledHint>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div class="vercel-card p-5">
        <p class="vercel-card-title">Connected Agents</p>
        <span class="vercel-card-value">{{ connectedAgentCount }}/{{ enabledAgents }}</span>
        <p class="mt-2 text-xs text-[#888]">connected / enabled</p>
      </div>
      <div class="vercel-card p-5">
        <p class="vercel-card-title">Fleet Uptime</p>
        <span class="vercel-card-value">{{ formatPercent(fleetUptime) }}</span>
        <p class="mt-2 text-xs text-[#888]">{{ retentionDaysLabel }} retention</p>
      </div>
      <div class="vercel-card p-5">
        <p class="vercel-card-title">Longest Current Uptime</p>
        <span class="vercel-card-value">{{ formatLongDuration(longestCurrentUptimeMillis) }}</span>
        <p class="mt-2 text-xs text-[#888]">connected sessions</p>
      </div>
      <div class="vercel-card p-5">
        <p class="vercel-card-title">Recent Disconnects</p>
        <span class="vercel-card-value">{{ recentDisconnects }}</span>
        <p class="mt-2 text-xs text-[#888]">last 24h</p>
      </div>
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
        <span class="vercel-card-value">{{ activeAgentRequests }}</span>
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
        <table class="w-full min-w-[1180px] text-sm">
          <thead class="border-b border-[#333] text-left text-xs uppercase tracking-wider text-[#888]">
            <tr>
              <th class="px-5 py-3">Agent</th>
              <th class="px-5 py-3">State</th>
              <th class="px-5 py-3">Current</th>
              <th class="px-5 py-3">Uptime</th>
              <th class="px-5 py-3">Last Connected</th>
              <th class="px-5 py-3">Last Disconnected</th>
              <th class="px-5 py-3">Active Requests</th>
              <th class="px-5 py-3 text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="agent in agents" :key="agent.id.toString()" class="border-b border-[#1f1f1f] last:border-0">
              <td class="px-5 py-4">
                <p class="font-medium text-white">{{ agent.name }}</p>
                <p class="font-mono text-xs text-[#888]">{{ agent.publicId }}</p>
                <div class="mt-2 flex flex-wrap gap-1.5">
                  <span
                    v-for="label in agentUserLabels(agent)"
                    :key="label.id"
                    class="rounded border border-[#333] bg-[#101010] px-2 py-0.5 font-mono text-[11px] text-[#d4d4d8]"
                  >
                    {{ label.key }}={{ label.value }}
                  </span>
                  <span v-if="!agentUserLabels(agent).length" class="text-xs text-[#666]">No user labels</span>
                </div>
                <code class="mt-1 block break-all font-mono text-[11px] text-[#666]">{{ exactAgentSelector(agent) }}</code>
              </td>
              <td class="px-5 py-4">
                <div class="flex items-center gap-2">
                  <Tag :value="agentConnected(agent) ? 'Connected' : 'Offline'" :severity="agentConnected(agent) ? 'success' : 'warn'" />
                  <Tag v-if="!agent.enabled" value="Disabled" severity="warn" />
                </div>
              </td>
              <td class="px-5 py-4">
                <p class="font-mono text-xs text-[#d4d4d8]">{{ currentAgentDuration(agent) }}</p>
                <p class="mt-1 text-xs text-[#666]">{{ currentAgentDurationKind(agent) }}</p>
              </td>
              <td class="px-5 py-4">
                <p class="font-mono text-xs text-[#d4d4d8]">{{ agentUptimePercentLabel(agent) }}</p>
                <p class="mt-1 text-xs text-[#666]">connections {{ agentConnectionCounts(agent) }}</p>
              </td>
              <td class="px-5 py-4 font-mono text-xs text-[#d4d4d8]">{{ formatDate(agentLastConnected(agent)) }}</td>
              <td class="px-5 py-4 font-mono text-xs text-[#d4d4d8]">{{ formatDate(agentLastDisconnected(agent)) }}</td>
              <td class="px-5 py-4">
                <p class="font-mono text-xs text-[#d4d4d8]">{{ agent.activeRequests.toString() }}</p>
                <p v-if="agent.latestStats" class="mt-1 font-mono text-xs text-[#666]">
                  {{ bigIntLabel(agent.latestStats.memorySysMb) }} MB / {{ bigIntLabel(agent.latestStats.numGoroutine) }} goroutines
                </p>
              </td>
              <td class="px-5 py-4">
                <div class="flex justify-end gap-2">
                  <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
                    <SecondaryButton
                      size="small"
                      :aria-label="agent.enabled ? 'Disable agent' : 'Enable agent'"
                      :title="agent.enabled ? 'Disable agent' : 'Enable agent'"
                      :disabled="Boolean(busyDisabledReason)"
                      @click="setAgentEnabled(agent, !agent.enabled)"
                    >
                      <template #icon>
                        <BanIcon v-if="agent.enabled" class="h-3.5 w-3.5" />
                        <CheckIcon v-else class="h-3.5 w-3.5" />
                      </template>
                    </SecondaryButton>
                  </DisabledHint>
                  <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
                    <SecondaryButton size="small" aria-label="Rotate token" title="Rotate token" :disabled="Boolean(busyDisabledReason)" @click="rotateAgentToken(agent)">
                      <template #icon><RefreshIcon class="h-3.5 w-3.5" /></template>
                    </SecondaryButton>
                  </DisabledHint>
                  <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
                    <SecondaryButton size="small" aria-label="Edit agent" title="Edit agent" :disabled="Boolean(busyDisabledReason)" @click="editAgent(agent)">
                      <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
                    </SecondaryButton>
                  </DisabledHint>
                  <SecondaryButton size="small" aria-label="Show uninstall command" title="Show uninstall command" @click="openUninstallModal(agent)">
                    <template #icon><TimesCircleIcon class="h-3.5 w-3.5" /></template>
                  </SecondaryButton>
                  <DisabledHint :disabled="Boolean(deleteAgentDisabledReason(agent))" :reason="deleteAgentDisabledReason(agent)">
                    <DangerButton size="small" aria-label="Delete agent" title="Delete agent" :disabled="Boolean(deleteAgentDisabledReason(agent))" @click="deleteAgent(agent)">
                      <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
                    </DangerButton>
                  </DisabledHint>
                </div>
              </td>
            </tr>
            <tr v-if="!agents.length">
              <td colspan="8">
                <EmptyState
                  title="No agents registered"
                  description="Agents forward traffic to services behind NAT or firewalls by connecting outbound to this proxy."
                  action-label="Add Agent"
                  @action="openAddAgentModal"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section class="vercel-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4">
        <h4 class="text-sm font-semibold text-[#888] uppercase tracking-widest">Recent Connection Sessions</h4>
      </div>
      <div class="overflow-x-auto">
        <table class="w-full min-w-[860px] text-sm">
          <thead class="border-b border-[#333] text-left text-xs uppercase tracking-wider text-[#888]">
            <tr>
              <th class="px-5 py-3">Agent</th>
              <th class="px-5 py-3">Started</th>
              <th class="px-5 py-3">Ended</th>
              <th class="px-5 py-3">Duration</th>
              <th class="px-5 py-3">State</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="session in recentAgentConnections" :key="session.id.toString()" class="border-b border-[#1f1f1f] last:border-0">
              <td class="px-5 py-4">
                <p class="font-medium text-white">{{ sessionAgentLabel(session) }}</p>
                <p v-if="sessionAgentDetail(session)" class="font-mono text-xs text-[#888]">{{ sessionAgentDetail(session) }}</p>
              </td>
              <td class="px-5 py-4 font-mono text-xs text-[#d4d4d8]">{{ formatDate(session.connectedAtUnixMillis) }}</td>
              <td class="px-5 py-4 font-mono text-xs text-[#d4d4d8]">{{ session.active ? "-" : formatDate(session.disconnectedAtUnixMillis) }}</td>
              <td class="px-5 py-4 font-mono text-xs text-[#d4d4d8]">{{ formatLongDuration(session.durationMillis) }}</td>
              <td class="px-5 py-4">
                <Tag :value="session.active ? 'Active' : 'Closed'" :severity="session.active ? 'success' : 'secondary'" />
              </td>
            </tr>
            <tr v-if="!recentAgentConnections.length">
              <td colspan="5">
                <EmptyState
                  title="No connection sessions"
                  description="Agent connection sessions will appear after registered agents connect to management."
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <AgentEditorModal
      ref="agentEditor"
      :config="config"
      allow-create
      @created-agent="handleAgentCreated"
    />
    <ConfirmDialog :state="confirmState" @confirm="onConfirm" @cancel="onCancel" />

    <Modal :model-value="Boolean(rotateAgentToConfirm)" title="Rotate Agent Token" max-width="34rem" @update:model-value="closeRotateAgentModal">
      <div v-if="rotateAgentToConfirm" class="grid gap-5">
        <div class="grid gap-2">
          <p class="text-sm text-white">Rotate the token for {{ rotateAgentToConfirm.name }}?</p>
          <p class="text-sm leading-6 text-[#888]">
            The new token will be shown once. The active agent connection will be disconnected immediately. In-flight requests through this agent may fail, and future connections and stats reports must use the new token.
          </p>
        </div>
        <div class="rounded-md border border-[#333] bg-[#0b0b0b] p-3">
          <span class="mb-1 block text-xs font-medium uppercase tracking-wider text-[#888]">Agent ID</span>
          <code class="block overflow-x-auto font-mono text-xs text-white">{{ rotateAgentToConfirm.publicId }}</code>
        </div>
        <div class="flex justify-end gap-3">
          <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
            <SecondaryButton type="button" label="Cancel" :disabled="Boolean(busyDisabledReason)" @click="closeRotateAgentModal" />
          </DisabledHint>
          <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
            <DangerButton type="button" label="Rotate Token" :disabled="Boolean(busyDisabledReason)" @click="confirmRotateAgentToken" />
          </DisabledHint>
        </div>
      </div>
    </Modal>

    <Modal :model-value="Boolean(uninstallAgent)" title="Agent Uninstall" max-width="46rem" @update:model-value="closeUninstallModal">
      <div v-if="uninstallAgent" class="grid gap-5">
        <div class="grid gap-3 md:grid-cols-2">
          <div class="grid gap-1.5">
            <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Agent</span>
            <span class="rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-2 text-sm text-white">{{ uninstallAgent.name }}</span>
          </div>
          <div class="grid gap-1.5">
            <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Agent ID</span>
            <code class="overflow-x-auto rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-2 font-mono text-xs text-white">{{ uninstallAgent.publicId }}</code>
          </div>
        </div>

        <div class="rounded-md border border-[#5f3b1d] bg-[#160d05] p-3 text-xs leading-5 text-[#f5c28b]">
          <p class="font-semibold uppercase tracking-wider">Remote host full purge</p>
          <p class="mt-1 text-[#c79866]">
            Run this command on the Linux host where the shell installer was used. It stops and removes the systemd service, deletes the config directory and binary, and removes the p2pstream service user and group.
          </p>
          <p class="mt-2 text-[#c79866]">
            This does not delete the management agent record. Delete or disable the agent here after the host is removed.
          </p>
        </div>

        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          GitHub Repository
          <input v-model="uninstallReleaseRepository" class="vercel-input text-sm normal-case tracking-normal" placeholder="Kirari04/p2pstream" required />
        </label>

        <p v-if="uninstallSnippetError" class="rounded-md border border-[#5f1d1d] bg-[#160505] p-3 text-xs leading-5 text-[#f5a3a3]">{{ uninstallSnippetError }}</p>
        <pre v-else class="max-h-[260px] overflow-auto rounded-md border border-[#333] bg-[#050505] p-4 text-xs leading-5 text-white"><code>{{ uninstallSnippet }}</code></pre>

        <div class="flex justify-end gap-3">
          <SecondaryButton type="button" :label="uninstallCopyLabel" :disabled="Boolean(uninstallSnippetError)" @click="copyUninstallSnippet" />
          <Button label="Done" @click="closeUninstallModal" />
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

        <div class="grid gap-3 md:grid-cols-2">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Management URL
            <input v-model="setupManagementUrl" class="vercel-input text-sm normal-case tracking-normal" required />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            GitHub Repository
            <input v-model="setupReleaseRepository" class="vercel-input text-sm normal-case tracking-normal" placeholder="Kirari04/p2pstream" required />
          </label>
          <label v-if="setupTab === 'docker'" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Docker Image
            <input
              v-model="setupDockerImage"
              class="vercel-input text-sm normal-case tracking-normal"
              required
              @input="setupDockerImageTouched = true"
            />
          </label>
        </div>

        <div v-if="!managementUsesTLS" class="rounded-md border border-[#5f3b1d] bg-[#160d05] p-3 text-xs leading-5 text-[#f5c28b]">
          <p class="font-semibold uppercase tracking-wider">Insecure management URL</p>
          <p class="mt-1 text-[#c79866]">Agents reject HTTP management URLs by default. Enable the override only for isolated local development.</p>
          <label class="mt-3 flex items-center gap-2 text-[#f5c28b]">
            <input v-model="setupAllowInsecureManagement" type="checkbox" />
            Allow insecure agent management connection
          </label>
        </div>

        <div v-if="managementUsesTLS" class="grid gap-3 md:grid-cols-3">
          <label v-if="!embeddedManagementCAPEMBase64" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Management CA file
            <input
              v-model="setupManagementCAFile"
              class="vercel-input text-sm normal-case tracking-normal"
              placeholder="/etc/p2pstream/management-ca.pem"
            />
          </label>
          <div v-else class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Management CA
            <div class="rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-2 text-xs normal-case leading-5 tracking-normal text-[#d4d4d8]">
              Embedded pinned CA from this management server
            </div>
          </div>
          <label v-if="agentClientCertificateRequired" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Agent Certificate
            <input v-model="setupAgentTLSCertFile" class="vercel-input text-sm normal-case tracking-normal" required />
          </label>
          <label v-if="agentClientCertificateRequired" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Agent Key
            <input v-model="setupAgentTLSKeyFile" class="vercel-input text-sm normal-case tracking-normal" required />
          </label>
        </div>

        <div class="flex flex-wrap gap-2" role="tablist" aria-label="Agent setup format">
          <button
            type="button"
            class="rounded-md border px-3 py-2 text-sm transition"
            :class="setupTab === 'install' ? 'border-white bg-white text-black' : 'border-[#333] bg-[#0b0b0b] text-[#d4d4d8] hover:border-[#555] hover:text-white'"
            @click="setupTab = 'install'"
          >
            Linux install
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

        <p v-if="setupSnippetError" class="rounded-md border border-[#5f1d1d] bg-[#160505] p-3 text-xs leading-5 text-[#f5a3a3]">{{ setupSnippetError }}</p>
        <pre v-else class="max-h-[360px] overflow-auto rounded-md border border-[#333] bg-[#050505] p-4 text-xs leading-5 text-white"><code>{{ setupSnippet }}</code></pre>

        <div class="flex justify-end gap-3">
          <SecondaryButton type="button" :label="setupCopyLabel" :disabled="Boolean(setupSnippetError)" @click="copySetupSnippet" />
          <Button label="Done" @click="clearIssuedToken" />
        </div>
      </div>
    </Modal>
  </div>
</template>
