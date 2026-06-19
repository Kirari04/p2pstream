<script setup lang="ts">
import { computed, h, inject, ref, watch } from "vue";
import type { ComputedRef } from "vue";
import { NButton, NButtonGroup, NCheckbox, NDataTable, NInput, NModal, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { Ban as BanIcon } from "@lucide/vue";
import { Check as CheckIcon } from "@lucide/vue";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { CircleX as TimesCircleIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { useManagementClient } from "@/composables/useManagementClient";
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
import { modalCardStyle } from "@/lib/naiveUi";
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

const { confirm } = useConfirmDialog();
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
const setupTabOptions: Array<{ value: "install" | "docker" | "cli"; label: string }> = [
  { value: "install", label: "Linux install" },
  { value: "docker", label: "Docker Compose" },
  { value: "cli", label: "CLI" },
];

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
const agentColumns = computed<DataTableColumns<Agent>>(() => [
  {
    title: "Agent",
    key: "agent",
    minWidth: 280,
    render: (agent) => h("div", [
      h("p", { class: "font-medium text-white" }, agent.name),
      h("p", { class: "font-mono text-xs text-[#888]" }, agent.publicId),
      h("div", { class: "mt-2 flex flex-wrap gap-1.5" }, [
        ...agentUserLabels(agent).map((label) => h(
          "span",
          { key: label.id, class: "rounded border border-[#333] bg-[#101010] px-2 py-0.5 font-mono text-[11px] text-[#d4d4d8]" },
          `${label.key}=${label.value}`,
        )),
        !agentUserLabels(agent).length ? h("span", { class: "text-xs text-[#666]" }, "No user labels") : null,
      ]),
      h("code", { class: "mt-1 block break-all font-mono text-[11px] text-[#666]" }, exactAgentSelector(agent)),
    ]),
  },
  {
    title: "State",
    key: "state",
    width: 160,
    render: (agent) => h("div", { class: "flex items-center gap-2" }, [
      h(NTag, { size: "small", bordered: false, type: agentConnected(agent) ? "success" : "warning" }, { default: () => agentConnected(agent) ? "Connected" : "Offline" }),
      !agent.enabled ? h(NTag, { size: "small", bordered: false, type: "warning" }, { default: () => "Disabled" }) : null,
    ]),
  },
  {
    title: "Current",
    key: "current",
    width: 150,
    render: (agent) => h("div", [
      h("p", { class: "font-mono text-xs text-[#d4d4d8]" }, currentAgentDuration(agent)),
      h("p", { class: "mt-1 text-xs text-[#666]" }, currentAgentDurationKind(agent)),
    ]),
  },
  {
    title: "Uptime",
    key: "uptime",
    width: 150,
    render: (agent) => h("div", [
      h("p", { class: "font-mono text-xs text-[#d4d4d8]" }, agentUptimePercentLabel(agent)),
      h("p", { class: "mt-1 text-xs text-[#666]" }, `connections ${agentConnectionCounts(agent)}`),
    ]),
  },
  { title: "Last Connected", key: "lastConnected", width: 190, render: (agent) => h("span", { class: "font-mono text-xs" }, formatDate(agentLastConnected(agent))) },
  { title: "Last Disconnected", key: "lastDisconnected", width: 190, render: (agent) => h("span", { class: "font-mono text-xs" }, formatDate(agentLastDisconnected(agent))) },
  {
    title: "Active Requests",
    key: "activeRequests",
    width: 170,
    render: (agent) => h("div", [
      h("p", { class: "font-mono text-xs text-[#d4d4d8]" }, agent.activeRequests.toString()),
      agent.latestStats
        ? h("p", { class: "mt-1 font-mono text-xs text-[#666]" }, `${bigIntLabel(agent.latestStats.memorySysMb)} MB / ${bigIntLabel(agent.latestStats.numGoroutine)} goroutines`)
        : null,
    ]),
  },
  {
    title: "Actions",
    key: "actions",
    width: 260,
    align: "right",
    render: (agent) => h("div", { class: "flex justify-end gap-2" }, [
      h(DisabledHint, { disabled: Boolean(busyDisabledReason.value), reason: busyDisabledReason.value }, {
        default: () => h(NButton, {
          secondary: true,
          size: "small",
          "aria-label": agent.enabled ? "Disable agent" : "Enable agent",
          title: agent.enabled ? "Disable agent" : "Enable agent",
          disabled: Boolean(busyDisabledReason.value),
          onClick: () => void setAgentEnabled(agent, !agent.enabled),
        }, { icon: () => agent.enabled ? h(BanIcon, { class: "h-3.5 w-3.5" }) : h(CheckIcon, { class: "h-3.5 w-3.5" }) }),
      }),
      h(DisabledHint, { disabled: Boolean(busyDisabledReason.value), reason: busyDisabledReason.value }, {
        default: () => h(NButton, {
          secondary: true,
          size: "small",
          "aria-label": "Rotate token",
          title: "Rotate token",
          disabled: Boolean(busyDisabledReason.value),
          onClick: () => rotateAgentToken(agent),
        }, { icon: () => h(RefreshIcon, { class: "h-3.5 w-3.5" }) }),
      }),
      h(DisabledHint, { disabled: Boolean(busyDisabledReason.value), reason: busyDisabledReason.value }, {
        default: () => h(NButton, {
          secondary: true,
          size: "small",
          "aria-label": "Edit agent",
          title: "Edit agent",
          disabled: Boolean(busyDisabledReason.value),
          onClick: () => editAgent(agent),
        }, { icon: () => h(PencilIcon, { class: "h-3.5 w-3.5" }) }),
      }),
      h(NButton, {
        secondary: true,
        size: "small",
        "aria-label": "Show uninstall command",
        title: "Show uninstall command",
        onClick: () => openUninstallModal(agent),
      }, { icon: () => h(TimesCircleIcon, { class: "h-3.5 w-3.5" }) }),
      h(DisabledHint, { disabled: Boolean(deleteAgentDisabledReason(agent)), reason: deleteAgentDisabledReason(agent) }, {
        default: () => h(NButton, {
          type: "error",
          size: "small",
          "aria-label": "Delete agent",
          title: "Delete agent",
          disabled: Boolean(deleteAgentDisabledReason(agent)),
          onClick: () => void deleteAgent(agent),
        }, { icon: () => h(TrashIcon, { class: "h-3.5 w-3.5" }) }),
      }),
    ]),
  },
]);
const sessionColumns = computed<DataTableColumns<AgentConnectionSession>>(() => [
  {
    title: "Agent",
    key: "agent",
    minWidth: 220,
    render: (session) => h("div", [
      h("p", { class: "font-medium text-white" }, sessionAgentLabel(session)),
      sessionAgentDetail(session) ? h("p", { class: "font-mono text-xs text-[#888]" }, sessionAgentDetail(session)) : null,
    ]),
  },
  { title: "Started", key: "started", width: 190, render: (session) => h("span", { class: "font-mono text-xs" }, formatDate(session.connectedAtUnixMillis)) },
  { title: "Ended", key: "ended", width: 190, render: (session) => h("span", { class: "font-mono text-xs" }, session.active ? "-" : formatDate(session.disconnectedAtUnixMillis)) },
  { title: "Duration", key: "duration", width: 150, render: (session) => h("span", { class: "font-mono text-xs" }, formatLongDuration(session.durationMillis)) },
  {
    title: "State",
    key: "state",
    width: 120,
    render: (session) => h(NTag, { size: "small", bordered: false, type: session.active ? "success" : "default" }, { default: () => session.active ? "Active" : "Closed" }),
  },
]);

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

function handleRotateModalUpdate(show: boolean) {
  if (!show) closeRotateAgentModal();
}

function handleUninstallModalUpdate(show: boolean) {
  if (!show) closeUninstallModal();
}

function handleSetupModalUpdate(show: boolean) {
  if (!show) clearIssuedToken();
}

function agentRowKey(agent: Agent): string {
  return agent.id.toString();
}

function agentRowProps(agent: Agent): Record<string, string> {
  return {
    "data-testid": `agent-row-${agent.publicId}`,
  };
}

function sessionRowKey(session: AgentConnectionSession): string {
  return session.id.toString();
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
  if (!await confirm("Delete Agent", "This agent and its agent-selected target matches will be permanently removed.")) return;
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
        <NButton secondary size="small" :disabled="Boolean(busyDisabledReason)" @click="openAddAgentModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          Add Agent
        </NButton>
      </DisabledHint>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div class="app-card p-5">
        <p class="app-card-title">Connected Agents</p>
        <span class="app-card-value">{{ connectedAgentCount }}/{{ enabledAgents }}</span>
        <p class="mt-2 text-xs text-[#888]">connected / enabled</p>
      </div>
      <div class="app-card p-5">
        <p class="app-card-title">Fleet Uptime</p>
        <span class="app-card-value">{{ formatPercent(fleetUptime) }}</span>
        <p class="mt-2 text-xs text-[#888]">{{ retentionDaysLabel }} retention</p>
      </div>
      <div class="app-card p-5">
        <p class="app-card-title">Longest Current Uptime</p>
        <span class="app-card-value">{{ formatLongDuration(longestCurrentUptimeMillis) }}</span>
        <p class="mt-2 text-xs text-[#888]">connected sessions</p>
      </div>
      <div class="app-card p-5">
        <p class="app-card-title">Recent Disconnects</p>
        <span class="app-card-value">{{ recentDisconnects }}</span>
        <p class="mt-2 text-xs text-[#888]">last 24h</p>
      </div>
    </div>

    <div class="grid gap-6 sm:grid-cols-2 lg:grid-cols-3">
      <div class="app-card p-6">
        <p class="app-card-title">Memory Usage (Sys)</p>
        <span class="app-card-value">{{ bigIntLabel(status.latestAgentStats?.memorySysMb) }} MB</span>
      </div>
      <div class="app-card p-6">
        <p class="app-card-title">Goroutines</p>
        <span class="app-card-value">{{ bigIntLabel(status.latestAgentStats?.numGoroutine) }}</span>
      </div>
      <div class="app-card p-6">
        <p class="app-card-title">Active Requests</p>
        <span class="app-card-value">{{ activeAgentRequests }}</span>
      </div>
      <div class="app-card p-6">
        <p class="app-card-title">Avg Memory (1h)</p>
        <span class="app-card-value">{{ bigIntLabel(oneHourWindow?.agentAvgMemoryMb) }} MB</span>
      </div>
      <div class="app-card p-6">
        <p class="app-card-title">Max Memory (24h)</p>
        <span class="app-card-value">{{ bigIntLabel(dayWindow?.agentMaxMemoryMb) }} MB</span>
      </div>
      <div class="app-card p-6">
        <p class="app-card-title">Max Goroutines (24h)</p>
        <span class="app-card-value">{{ bigIntLabel(dayWindow?.agentMaxGoroutines) }}</span>
      </div>
    </div>

    <section class="app-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4">
        <h4 class="text-sm font-semibold text-[#888] uppercase tracking-widest">Registered Agents</h4>
      </div>
      <NDataTable
        v-if="agents.length"
        :columns="agentColumns"
        :data="agents"
        :row-key="agentRowKey"
        :row-props="agentRowProps"
        :pagination="false"
        :bordered="false"
        :single-line="false"
        :scroll-x="1280"
        size="small"
      />
      <EmptyState
        v-else
        title="No agents registered"
        description="Agents forward traffic to services behind NAT or firewalls by connecting outbound to this proxy."
        action-label="Add Agent"
        @action="openAddAgentModal"
      />
    </section>

    <section class="app-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4">
        <h4 class="text-sm font-semibold text-[#888] uppercase tracking-widest">Recent Connection Sessions</h4>
      </div>
      <NDataTable
        v-if="recentAgentConnections.length"
        :columns="sessionColumns"
        :data="recentAgentConnections"
        :row-key="sessionRowKey"
        :pagination="false"
        :bordered="false"
        :single-line="false"
        :scroll-x="860"
        size="small"
      />
      <EmptyState
        v-else
        title="No connection sessions"
        description="Agent connection sessions will appear after registered agents connect to management."
      />
    </section>

    <AgentEditorModal
      ref="agentEditor"
      :config="config"
      allow-create
      @created-agent="handleAgentCreated"
    />

    <NModal
      :show="Boolean(rotateAgentToConfirm)"
      preset="card"
      title="Rotate Agent Token"
      :style="modalCardStyle('34rem')"
      :bordered="false"
      @update:show="handleRotateModalUpdate"
    >
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
            <NButton secondary attr-type="button" :disabled="Boolean(busyDisabledReason)" @click="closeRotateAgentModal">Cancel</NButton>
          </DisabledHint>
          <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
            <NButton type="error" attr-type="button" :disabled="Boolean(busyDisabledReason)" @click="confirmRotateAgentToken">Rotate Token</NButton>
          </DisabledHint>
        </div>
      </div>
    </NModal>

    <NModal
      :show="Boolean(uninstallAgent)"
      preset="card"
      title="Agent Uninstall"
      :style="modalCardStyle('46rem')"
      :bordered="false"
      @update:show="handleUninstallModalUpdate"
    >
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
          <NInput v-model:value="uninstallReleaseRepository" size="small" placeholder="Kirari04/p2pstream" required />
        </label>

        <p v-if="uninstallSnippetError" class="rounded-md border border-[#5f1d1d] bg-[#160505] p-3 text-xs leading-5 text-[#f5a3a3]">{{ uninstallSnippetError }}</p>
        <pre v-else class="max-h-[260px] overflow-auto rounded-md border border-[#333] bg-[#050505] p-4 text-xs leading-5 text-white"><code>{{ uninstallSnippet }}</code></pre>

        <div class="flex justify-end gap-3">
          <NButton secondary attr-type="button" :disabled="Boolean(uninstallSnippetError)" @click="copyUninstallSnippet">{{ uninstallCopyLabel }}</NButton>
          <NButton type="primary" attr-type="button" @click="closeUninstallModal">Done</NButton>
        </div>
      </div>
    </NModal>

    <NModal
      :show="Boolean(issuedToken && issuedAgent)"
      preset="card"
      title="Agent Setup"
      :style="modalCardStyle('48rem')"
      :bordered="false"
      @update:show="handleSetupModalUpdate"
    >
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
            <NInput v-model:value="setupManagementUrl" size="small" required />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            GitHub Repository
            <NInput v-model:value="setupReleaseRepository" size="small" placeholder="Kirari04/p2pstream" required />
          </label>
          <label v-if="setupTab === 'docker'" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Docker Image
            <NInput
              v-model:value="setupDockerImage"
              size="small"
              required
              @update:value="setupDockerImageTouched = true"
            />
          </label>
        </div>

        <div v-if="!managementUsesTLS" class="rounded-md border border-[#5f3b1d] bg-[#160d05] p-3 text-xs leading-5 text-[#f5c28b]">
          <p class="font-semibold uppercase tracking-wider">Insecure management URL</p>
          <p class="mt-1 text-[#c79866]">Agents reject HTTP management URLs by default. Enable the override only for isolated local development.</p>
          <NCheckbox v-model:checked="setupAllowInsecureManagement" class="mt-3 text-[#f5c28b]">
            Allow insecure agent management connection
          </NCheckbox>
        </div>

        <div v-if="managementUsesTLS" class="grid gap-3 md:grid-cols-3">
          <label v-if="!embeddedManagementCAPEMBase64" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Management CA file
            <NInput v-model:value="setupManagementCAFile" size="small" placeholder="/etc/p2pstream/management-ca.pem" />
          </label>
          <div v-else class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Management CA
            <div class="rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-2 text-xs normal-case leading-5 tracking-normal text-[#d4d4d8]">
              Embedded pinned CA from this management server
            </div>
          </div>
          <label v-if="agentClientCertificateRequired" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Agent Certificate
            <NInput v-model:value="setupAgentTLSCertFile" size="small" required />
          </label>
          <label v-if="agentClientCertificateRequired" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Agent Key
            <NInput v-model:value="setupAgentTLSKeyFile" size="small" required />
          </label>
        </div>

        <NButtonGroup class="flex flex-wrap" role="tablist" aria-label="Agent setup format" size="small">
          <NButton
            v-for="tab in setupTabOptions"
            :key="tab.value"
            attr-type="button"
            :type="setupTab === tab.value ? 'primary' : 'default'"
            @click="setupTab = tab.value"
          >
            {{ tab.label }}
          </NButton>
        </NButtonGroup>

        <p v-if="setupSnippetError" class="rounded-md border border-[#5f1d1d] bg-[#160505] p-3 text-xs leading-5 text-[#f5a3a3]">{{ setupSnippetError }}</p>
        <pre v-else class="max-h-[360px] overflow-auto rounded-md border border-[#333] bg-[#050505] p-4 text-xs leading-5 text-white"><code>{{ setupSnippet }}</code></pre>

        <div class="flex justify-end gap-3">
          <NButton secondary attr-type="button" :disabled="Boolean(setupSnippetError)" @click="copySetupSnippet">{{ setupCopyLabel }}</NButton>
          <NButton type="primary" attr-type="button" @click="clearIssuedToken">Done</NButton>
        </div>
      </div>
    </NModal>
  </div>
</template>
