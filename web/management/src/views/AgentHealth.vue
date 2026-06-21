<script setup lang="ts">
import { computed, h, inject, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { NAlert, NButton, NCheckbox, NDataTable, NInput, NModal, NTab, NTabs, NTag } from "naive-ui";
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
import { dashboardKey, isBusyKey, publicProxyConfigKey, runManagementActionKey } from "@/composables/managementContextKeys";
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
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();
const route = useRoute();
const router = useRouter();

const agentSections = [
  {
    key: "fleet",
    label: "Fleet",
    path: "/agent",
    description: "Fleet state, availability, selectors, and agent lifecycle actions.",
  },
  {
    key: "activity",
    label: "Activity",
    path: "/agent/activity",
    description: "Runtime pressure, process metrics, and recent connection sessions.",
  },
] as const;

type AgentSectionKey = typeof agentSections[number]["key"];

const dashboard = inject(dashboardKey, computed(() => null));
const publicProxyConfig = inject(publicProxyConfigKey, computed(() => null));
const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));

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
const totalAgents = computed(() => agents.value.length);
const disabledAgents = computed(() => Math.max(0, totalAgents.value - enabledAgents.value));
const connectedAgentCount = computed(() => uptimeSummaries.value.length
  ? uptimeSummaries.value.filter((summary) => summary.connected).length
  : agents.value.filter((agent) => agent.connected).length);
const offlineEnabledAgents = computed(() => Math.max(0, enabledAgents.value - connectedAgentCount.value));
const activeAgentRequests = computed(() => agents.value.reduce((sum, agent) => sum + Number(agent.activeRequests || 0n), 0));
const fleetUptime = computed(() => fleetUptimePercent(uptimeSummaries.value));
const longestCurrentUptimeMillis = computed(() => Math.max(0, ...uptimeSummaries.value.map((summary) => Number(summary.currentUptimeMillis || 0n))));
const recentDisconnects = computed(() => recentDisconnectCount(recentAgentConnections.value, dashboard?.value?.generatedAtUnixMillis ?? BigInt(Date.now())));
const retentionDaysLabel = computed(() => `${(dashboard?.value?.retentionDays ?? 30n).toString()}d`);
const connectedAgentPercent = computed(() => {
  if (!enabledAgents.value) return 0;
  return Math.round((connectedAgentCount.value / enabledAgents.value) * 100);
});
const connectedAgentPercentStyle = computed(() => ({
  width: `${Math.min(100, Math.max(0, connectedAgentPercent.value))}%`,
}));
const fleetStatusType = computed<"success" | "warning" | "error" | "default">(() => {
  if (!totalAgents.value) return "default";
  if (connectedAgentCount.value === enabledAgents.value && enabledAgents.value > 0) return "success";
  if (connectedAgentCount.value === 0 && enabledAgents.value > 0) return "error";
  return "warning";
});
const fleetStatusLabel = computed(() => {
  if (!totalAgents.value) return "No agents";
  if (connectedAgentCount.value === enabledAgents.value && enabledAgents.value > 0) return "Healthy";
  if (connectedAgentCount.value === 0 && enabledAgents.value > 0) return "Disconnected";
  return "Degraded";
});
const fleetSummary = computed(() => {
  if (!totalAgents.value) return "Create an agent to connect private upstreams to this proxy.";
  if (!enabledAgents.value) return `${totalAgents.value} registered, all disabled.`;
  if (!offlineEnabledAgents.value) return `${connectedAgentPercent.value}% of enabled agents are connected.`;
  return `${offlineEnabledAgents.value} enabled agent${offlineEnabledAgents.value === 1 ? "" : "s"} offline.`;
});
const runtimeMetrics = computed(() => [
  {
    label: "Memory sys",
    value: `${bigIntLabel(status.value?.latestAgentStats?.memorySysMb)} MB`,
    detail: "latest sample",
  },
  {
    label: "Goroutines",
    value: bigIntLabel(status.value?.latestAgentStats?.numGoroutine),
    detail: "latest sample",
  },
  {
    label: "Active requests",
    value: activeAgentRequests.value.toString(),
    detail: "across agents",
  },
  {
    label: "Avg memory",
    value: `${bigIntLabel(oneHourWindow.value?.agentAvgMemoryMb)} MB`,
    detail: "1h window",
  },
  {
    label: "Max memory",
    value: `${bigIntLabel(dayWindow.value?.agentMaxMemoryMb)} MB`,
    detail: "24h window",
  },
  {
    label: "Max goroutines",
    value: bigIntLabel(dayWindow.value?.agentMaxGoroutines),
    detail: "24h window",
  },
]);
const activeAgentSection = computed<AgentSectionKey>(() => normalizeAgentSection(route.params.section));
const activeAgentSectionMeta = computed(() =>
  agentSections.find((section) => section.key === activeAgentSection.value) ?? agentSections[0],
);

const agentEditor = ref<InstanceType<typeof AgentEditorModal> | null>(null);
const rotateAgentToConfirm = ref<Agent | null>(null);
const issuedToken = ref("");
const issuedAgent = ref<Agent | null>(null);
const setupContext = ref<"create" | "rotate">("create");
const setupManagementUrl = ref(defaultManagementUrl());
const setupManagementCAFile = ref("");
const setupAgentTLSCertFile = ref("/etc/p2pstream/agent.crt.pem");
const setupAgentTLSKeyFile = ref("/etc/p2pstream/agent.key.pem");
const setupAllowInsecureManagement = ref(false);
const setupReleaseRepository = ref(defaultReleaseRepository());
const setupReleaseVersion = ref(defaultReleaseVersion());
const setupDockerImage = ref(defaultDockerImage(setupReleaseRepository.value, setupReleaseVersion.value));
const setupDockerImageTouched = ref(false);
const setupTab = ref<"install" | "docker" | "cli">("install");
const setupCopyLabel = ref("Copy");
const uninstallAgent = ref<Agent | null>(null);
const uninstallReleaseRepository = ref(defaultReleaseRepository());
const uninstallCopyLabel = ref("Copy");
let setupCopyReset: number | undefined;
let uninstallCopyReset: number | undefined;

const sessionPagination = { pageSize: 12 };

const busyDisabledReason = computed(() => isBusy?.value ? BUSY_REASON : "");
const normalizedManagementUrl = computed(() => normalizeSetupManagementUrl(setupManagementUrl.value));
const managementUsesTLS = computed(() => normalizedManagementUrl.value.toLowerCase().startsWith("https://"));
const agentClientCertificateRequired = computed(() => Boolean(managementSecurity.value?.agentClientCertificateRequired));
const setupIsRotation = computed(() => setupContext.value === "rotate");
const setupModalTitle = computed(() => setupIsRotation.value ? "Agent Reinstall" : "Agent Setup");
const setupLinuxTabLabel = computed(() => setupIsRotation.value ? "Linux reinstall" : "Linux install");
const setupTabOptions = computed<Array<{ value: "install" | "docker" | "cli"; label: string }>>(() => [
  { value: "install", label: setupLinuxTabLabel.value },
  { value: "docker", label: "Docker Compose" },
  { value: "cli", label: "CLI" },
]);
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

function normalizeAgentSection(value: unknown): AgentSectionKey {
  const section = Array.isArray(value) ? value[0] : value;
  return section === "activity" ? "activity" : "fleet";
}

async function selectAgentSection(value: string | number) {
  const section = agentSections.find((item) => item.key === value);
  if (!section || section.key === activeAgentSection.value) return;
  await router.push(section.path);
}

const agentColumns = computed<DataTableColumns<Agent>>(() => [
  {
    title: "Agent",
    key: "agent",
    minWidth: 340,
    render: (agent) => h("div", { class: "agent-cell" }, [
      h("div", { class: "agent-cell__header" }, [
        h("span", { class: "agent-cell__name" }, agent.name),
        !agent.enabled ? h(NTag, { size: "small", bordered: false, type: "warning" }, { default: () => "Disabled" }) : null,
      ]),
      h("p", { class: "agent-cell__id mono-text" }, agent.publicId),
      h("div", { class: "agent-cell__labels" }, [
        ...agentUserLabels(agent).map((label) => h(
          NTag,
          { key: label.id, size: "small", bordered: true, class: "mono-text" },
          { default: () => `${label.key}=${label.value}` },
        )),
        !agentUserLabels(agent).length ? h("span", { class: "copy-xs muted-text" }, "No user labels") : null,
      ]),
      h("div", { class: "agent-selector" }, [
        h("span", { class: "agent-selector__label" }, "Selector"),
        h("code", { class: "agent-selector__value mono-text" }, exactAgentSelector(agent)),
      ]),
    ]),
  },
  {
    title: "State",
    key: "state",
    width: 190,
    render: (agent) => h("div", { class: "agent-state-cell" }, [
      h("div", { class: "agent-state-cell__status" }, [
        h("span", { class: ["agent-state-dot", agentConnected(agent) ? "agent-state-dot--connected" : "agent-state-dot--offline"] }),
        h("span", { class: "weight-semibold base-text" }, agentConnected(agent) ? "Connected" : "Offline"),
      ]),
      h("p", { class: "margin-top-xs mono-text copy-xs muted-text" }, currentAgentDuration(agent)),
      h("p", { class: "copy-xs muted-text" }, currentAgentDurationKind(agent)),
    ]),
  },
  {
    title: "Reliability",
    key: "reliability",
    width: 230,
    render: (agent) => h("div", { class: "agent-compact-stack" }, [
      h("p", { class: "agent-metric-line" }, [
        h("span", { class: "muted-text" }, "Uptime"),
        h("strong", { class: "mono-text base-text" }, agentUptimePercentLabel(agent)),
      ]),
      h("p", { class: "agent-metric-line" }, [
        h("span", { class: "muted-text" }, "Connections"),
        h("strong", { class: "mono-text base-text" }, agentConnectionCounts(agent)),
      ]),
      h("p", { class: "agent-subline mono-text muted-text" }, `Last up ${formatDate(agentLastConnected(agent))}`),
      h("p", { class: "agent-subline mono-text muted-text" }, `Last down ${formatDate(agentLastDisconnected(agent))}`),
    ]),
  },
  {
    title: "Runtime",
    key: "runtime",
    width: 210,
    render: (agent) => h("div", { class: "agent-compact-stack" }, [
      h("p", { class: "agent-metric-line" }, [
        h("span", { class: "muted-text" }, "Active"),
        h("strong", { class: "mono-text base-text" }, agent.activeRequests.toString()),
      ]),
      ...(agent.latestStats
        ? [
            h("p", { class: "agent-metric-line" }, [
              h("span", { class: "muted-text" }, "Memory"),
              h("strong", { class: "mono-text base-text" }, `${bigIntLabel(agent.latestStats.memorySysMb)} MB`),
            ]),
            h("p", { class: "agent-metric-line" }, [
              h("span", { class: "muted-text" }, "Goroutines"),
              h("strong", { class: "mono-text base-text" }, bigIntLabel(agent.latestStats.numGoroutine)),
            ]),
          ]
        : [h("p", { class: "copy-xs muted-text" }, "No runtime sample")]),
    ]),
  },
  {
    title: "Actions",
    key: "actions",
    width: 230,
    align: "right",
    render: (agent) => h("div", { class: "agent-row-actions" }, [
      h(DisabledHint, { disabled: Boolean(busyDisabledReason.value), reason: busyDisabledReason.value }, {
        default: () => h(NButton, {
          secondary: true,
          circle: true,
          size: "small",
          "aria-label": agent.enabled ? "Disable agent" : "Enable agent",
          title: agent.enabled ? "Disable agent" : "Enable agent",
          disabled: Boolean(busyDisabledReason.value),
          onClick: () => void setAgentEnabled(agent, !agent.enabled),
        }, { icon: () => agent.enabled ? h(BanIcon, { class: "icon-sm" }) : h(CheckIcon, { class: "icon-sm" }) }),
      }),
      h(DisabledHint, { disabled: Boolean(busyDisabledReason.value), reason: busyDisabledReason.value }, {
        default: () => h(NButton, {
          secondary: true,
          circle: true,
          size: "small",
          "aria-label": "Rotate token",
          title: "Rotate token",
          disabled: Boolean(busyDisabledReason.value),
          onClick: () => rotateAgentToken(agent),
        }, { icon: () => h(RefreshIcon, { class: "icon-sm" }) }),
      }),
      h(DisabledHint, { disabled: Boolean(busyDisabledReason.value), reason: busyDisabledReason.value }, {
        default: () => h(NButton, {
          secondary: true,
          circle: true,
          size: "small",
          "aria-label": "Edit agent",
          title: "Edit agent",
          disabled: Boolean(busyDisabledReason.value),
          onClick: () => editAgent(agent),
        }, { icon: () => h(PencilIcon, { class: "icon-sm" }) }),
      }),
      h(NButton, {
        secondary: true,
        circle: true,
        size: "small",
        "aria-label": "Show uninstall command",
        title: "Show uninstall command",
        onClick: () => openUninstallModal(agent),
      }, { icon: () => h(TimesCircleIcon, { class: "icon-sm" }) }),
      h(DisabledHint, { disabled: Boolean(deleteAgentDisabledReason(agent)), reason: deleteAgentDisabledReason(agent) }, {
        default: () => h(NButton, {
          type: "error",
          circle: true,
          size: "small",
          "aria-label": "Delete agent",
          title: "Delete agent",
          disabled: Boolean(deleteAgentDisabledReason(agent)),
          onClick: () => void deleteAgent(agent),
        }, { icon: () => h(TrashIcon, { class: "icon-sm" }) }),
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
      h("p", { class: "weight-medium base-text" }, sessionAgentLabel(session)),
      sessionAgentDetail(session) ? h("p", { class: "mono-text copy-xs muted-text" }, sessionAgentDetail(session)) : null,
    ]),
  },
  { title: "Started", key: "started", width: 190, render: (session) => h("span", { class: "mono-text copy-xs" }, formatDate(session.connectedAtUnixMillis)) },
  { title: "Ended", key: "ended", width: 190, render: (session) => h("span", { class: "mono-text copy-xs" }, session.active ? "-" : formatDate(session.disconnectedAtUnixMillis)) },
  { title: "Duration", key: "duration", width: 150, render: (session) => h("span", { class: "mono-text copy-xs" }, formatLongDuration(session.durationMillis)) },
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

watch([setupReleaseRepository, setupReleaseVersion], ([repository, version]) => {
  if (!setupDockerImageTouched.value) {
    setupDockerImage.value = defaultDockerImage(repository, version);
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
    openSetupModal(resp.agent ?? agent, resp.token, "rotate");
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
  setupContext.value = "create";
  setupCopyLabel.value = "Copy";
}

function openSetupModal(agent: Agent | null, token: string, context: "create" | "rotate" = "create") {
  if (!agent || !token) return;
  issuedAgent.value = agent;
  issuedToken.value = token;
  setupContext.value = context;
  setupManagementUrl.value = defaultManagementUrl();
  setupManagementCAFile.value = "";
  setupAgentTLSCertFile.value = "/etc/p2pstream/agent.crt.pem";
  setupAgentTLSKeyFile.value = "/etc/p2pstream/agent.key.pem";
  setupAllowInsecureManagement.value = false;
  setupReleaseRepository.value = defaultReleaseRepository();
  setupReleaseVersion.value = defaultReleaseVersion();
  setupDockerImage.value = defaultDockerImage(setupReleaseRepository.value, setupReleaseVersion.value);
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

function defaultReleaseVersion(): string {
  const configured = import.meta.env.VITE_RELEASE_REF;
  if (typeof configured !== "string") return "latest";
  const version = configured.trim();
  if (version === "staging" || /^v\d+\.\d+\.\d+$/.test(version)) {
    return version;
  }
  return "latest";
}

function defaultDockerImage(repository: string, version: string): string {
  try {
    return dockerImageForRepository(repository, version);
  } catch {
    return dockerImageForRepository(repository);
  }
}

function installerScriptRef(): string {
  const version = setupReleaseVersion.value.trim();
  return version === "latest" || version === "" ? "main" : version;
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
    version: setupReleaseVersion.value,
    scriptRef: installerScriptRef(),
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
  <div v-if="dashboard && status" class="agent-page stack-xl">
    <div class="agent-page__header">
      <div class="min-width-zero">
        <div class="agent-title-row">
          <h3 class="copy-xl weight-bold">Agents</h3>
          <NTag size="small" :bordered="false" :type="fleetStatusType">{{ fleetStatusLabel }}</NTag>
        </div>
        <p class="muted-text copy-sm">{{ activeAgentSectionMeta.description }}</p>
      </div>
      <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
        <NButton type="primary" size="small" :disabled="Boolean(busyDisabledReason)" @click="openAddAgentModal">
          <template #icon><PlusIcon class="icon-sm" /></template>
          Add Agent
        </NButton>
      </DisabledHint>
    </div>

    <NTabs
      class="agent-section-tabs"
      type="line"
      :value="activeAgentSection"
      @update:value="selectAgentSection"
    >
      <NTab
        v-for="section in agentSections"
        :key="section.key"
        :name="section.key"
        :tab="section.label"
      />
    </NTabs>

    <section v-if="activeAgentSection === 'fleet'" class="surface-card agent-overview">
      <div class="agent-overview__main">
        <div class="agent-overview__heading">
          <p class="stat-label">Fleet connection</p>
          <strong>{{ connectedAgentCount }}/{{ enabledAgents }}</strong>
        </div>
        <div class="agent-connection-meter" aria-hidden="true">
          <span :style="connectedAgentPercentStyle"></span>
        </div>
        <p class="copy-sm muted-text">{{ fleetSummary }}</p>
        <div class="agent-overview__tags">
          <NTag size="small" :bordered="false" type="info">{{ totalAgents }} registered</NTag>
          <NTag size="small" :bordered="false" type="success">{{ enabledAgents }} enabled</NTag>
          <NTag v-if="disabledAgents" size="small" :bordered="false" type="warning">{{ disabledAgents }} disabled</NTag>
        </div>
      </div>

      <div class="agent-overview__metrics">
        <div class="agent-overview__metric">
          <span>Fleet uptime</span>
          <strong>{{ formatPercent(fleetUptime) }}</strong>
          <small>{{ retentionDaysLabel }} retention</small>
        </div>
        <div class="agent-overview__metric">
          <span>Longest live session</span>
          <strong>{{ formatLongDuration(longestCurrentUptimeMillis) }}</strong>
          <small>current uptime</small>
        </div>
        <div class="agent-overview__metric">
          <span>Recent disconnects</span>
          <strong :class="recentDisconnects ? 'warning-text' : 'base-text'">{{ recentDisconnects }}</strong>
          <small>last 24h</small>
        </div>
      </div>
    </section>

    <section v-if="activeAgentSection === 'activity'" class="surface-card agent-runtime-card">
      <div class="agent-section-header">
        <div>
          <h4>Runtime pressure</h4>
          <p>Latest agent process stats plus rolling dashboard windows.</p>
        </div>
        <NTag size="small" :bordered="false" type="default">{{ activeAgentRequests }} active requests</NTag>
      </div>
      <div class="agent-runtime-grid">
        <div v-for="metric in runtimeMetrics" :key="metric.label" class="agent-runtime-metric">
          <span>{{ metric.label }}</span>
          <strong>{{ metric.value }}</strong>
          <small>{{ metric.detail }}</small>
        </div>
      </div>
    </section>

    <section v-if="activeAgentSection === 'fleet'" class="surface-card hide-overflow agent-table-card">
      <div class="agent-section-header agent-section-header--table">
        <div>
          <h4>Registered agents</h4>
          <p>Connection state, selector labels, uptime, and host runtime samples.</p>
        </div>
        <NTag size="small" :bordered="false" type="default">{{ totalAgents }} total</NTag>
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
        :scroll-x="1080"
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

    <section v-if="activeAgentSection === 'activity'" class="surface-card hide-overflow agent-table-card">
      <div class="agent-section-header agent-section-header--table">
        <div>
          <h4>Recent connection sessions</h4>
          <p>Connection lifetime history retained for {{ retentionDaysLabel }}.</p>
        </div>
        <NTag size="small" :bordered="false" type="default">{{ recentAgentConnections.length }} sessions</NTag>
      </div>
      <NDataTable
        v-if="recentAgentConnections.length"
        :columns="sessionColumns"
        :data="recentAgentConnections"
        :row-key="sessionRowKey"
        :pagination="sessionPagination"
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
      <div v-if="rotateAgentToConfirm" class="layout-grid space-xl">
        <div class="layout-grid space-sm">
          <p class="copy-sm base-text">Rotate the token for {{ rotateAgentToConfirm.name }}?</p>
          <p class="copy-sm line-relaxed muted-text">
            The new token will be shown once. The active agent connection will be disconnected immediately. In-flight requests through this agent may fail, and future connections and stats reports must use the new token.
          </p>
        </div>
        <div class="round-md framed frame-standard muted-bg pad-md">
          <span class="margin-bottom-xs flow-box copy-xs weight-medium label-case letter-wide muted-text">Agent ID</span>
          <code class="flow-box scroll-x mono-text copy-xs base-text">{{ rotateAgentToConfirm.publicId }}</code>
        </div>
        <div class="layout-row align-end-row space-md">
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
      <div v-if="uninstallAgent" class="layout-grid space-xl">
        <div class="layout-grid space-md mq-md-cols-two">
          <div class="layout-grid space-xs">
            <span class="copy-xs weight-medium label-case letter-wide muted-text">Agent</span>
            <span class="round-md framed frame-standard muted-bg pad-x-md pad-y-sm copy-sm base-text">{{ uninstallAgent.name }}</span>
          </div>
          <div class="layout-grid space-xs">
            <span class="copy-xs weight-medium label-case letter-wide muted-text">Agent ID</span>
            <code class="scroll-x round-md framed frame-standard muted-bg pad-x-md pad-y-sm mono-text copy-xs base-text">{{ uninstallAgent.publicId }}</code>
          </div>
        </div>

        <div class="warning-panel pad-md copy-xs line-normal">
          <p class="weight-semibold label-case letter-wide">Remote host full purge</p>
          <p class="margin-top-xs deemphasized">
            Run this command on the Linux host where the shell installer was used. It stops and removes the systemd service, deletes the config directory and binary, and removes the p2pstream service user and group.
          </p>
          <p class="margin-top-sm deemphasized">
            This does not delete the management agent record. Delete or disable the agent here after the host is removed.
          </p>
        </div>

        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          GitHub Repository
          <NInput v-model:value="uninstallReleaseRepository" size="small" placeholder="Kirari04/p2pstream" required />
        </label>

        <p v-if="uninstallSnippetError" class="error-panel pad-md copy-xs line-normal">{{ uninstallSnippetError }}</p>
        <pre v-else class="max-height-sm scroll-any round-md framed frame-standard muted-bg pad-lg copy-xs line-normal base-text"><code>{{ uninstallSnippet }}</code></pre>

        <div class="layout-row align-end-row space-md">
          <NButton secondary attr-type="button" :disabled="Boolean(uninstallSnippetError)" @click="copyUninstallSnippet">{{ uninstallCopyLabel }}</NButton>
          <NButton type="primary" attr-type="button" @click="closeUninstallModal">Done</NButton>
        </div>
      </div>
    </NModal>

    <NModal
      :show="Boolean(issuedToken && issuedAgent)"
      preset="card"
      :title="setupModalTitle"
      :style="modalCardStyle('48rem')"
      :bordered="false"
      @update:show="handleSetupModalUpdate"
    >
      <div v-if="issuedAgent" class="layout-grid space-xl">
        <div class="layout-grid space-md mq-md-cols-two">
          <div class="layout-grid space-xs">
            <span class="copy-xs weight-medium label-case letter-wide muted-text">Agent</span>
            <span class="round-md framed frame-standard muted-bg pad-x-md pad-y-sm copy-sm base-text">{{ issuedAgent.name }}</span>
          </div>
          <div class="layout-grid space-xs">
            <span class="copy-xs weight-medium label-case letter-wide muted-text">Generated ID</span>
            <code class="scroll-x round-md framed frame-standard muted-bg pad-x-md pad-y-sm mono-text copy-xs base-text">{{ issuedAgent.publicId }}</code>
          </div>
        </div>

        <NAlert type="warning" :show-icon="false">
          This token is shown once. Store it before closing this dialog.
        </NAlert>

        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          One-Time Token
          <code class="flow-box wrap-anywhere round-md framed frame-standard muted-bg pad-md mono-text copy-xs base-text">{{ issuedToken }}</code>
        </label>

        <div v-if="setupIsRotation" class="round-md framed frame-standard muted-bg pad-md copy-xs line-normal base-text">
          <p class="weight-semibold label-case letter-wide">Existing Linux agent</p>
          <p class="margin-top-xs muted-text">
            Run the Linux reinstall command on the existing agent host. It rewrites the agent environment, refreshes embedded management CA material, and restarts p2pstream-agent.
          </p>
        </div>

        <div class="layout-grid space-md mq-md-cols-two">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Management URL
            <NInput v-model:value="setupManagementUrl" size="small" required />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            GitHub Repository
            <NInput v-model:value="setupReleaseRepository" size="small" placeholder="Kirari04/p2pstream" required />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Release Version
            <NInput v-model:value="setupReleaseVersion" size="small" placeholder="latest, staging, or vX.Y.Z" required />
          </label>
          <label v-if="setupTab === 'docker'" class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Docker Image
            <NInput
              v-model:value="setupDockerImage"
              size="small"
              required
              @update:value="setupDockerImageTouched = true"
            />
          </label>
        </div>

        <div v-if="!managementUsesTLS" class="warning-panel pad-md copy-xs line-normal">
          <p class="weight-semibold label-case letter-wide">Insecure management URL</p>
          <p class="margin-top-xs deemphasized">Agents reject HTTP management URLs by default. Enable the override only for isolated local development.</p>
          <NCheckbox v-model:checked="setupAllowInsecureManagement" class="margin-top-md">
            Allow insecure agent management connection
          </NCheckbox>
        </div>

        <div v-if="managementUsesTLS" class="layout-grid space-md mq-md-cols-three">
          <label v-if="!embeddedManagementCAPEMBase64" class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Management CA file
            <NInput v-model:value="setupManagementCAFile" size="small" placeholder="/etc/p2pstream/management-ca.pem" />
          </label>
          <div v-else class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Management CA
            <div class="round-md framed frame-standard muted-bg pad-x-md pad-y-sm copy-xs normal-text line-normal letter-normal base-text">
              Embedded pinned CA from this management server
            </div>
          </div>
          <label v-if="agentClientCertificateRequired" class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Agent Certificate
            <NInput v-model:value="setupAgentTLSCertFile" size="small" required />
          </label>
          <label v-if="agentClientCertificateRequired" class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Agent Key
            <NInput v-model:value="setupAgentTLSKeyFile" size="small" required />
          </label>
        </div>

        <NTabs
          class="agent-setup-tabs"
          type="segment"
          size="small"
          :value="setupTab"
          @update:value="(value) => setupTab = value as 'install' | 'docker' | 'cli'"
        >
          <NTab
            v-for="tab in setupTabOptions"
            :key="tab.value"
            :name="tab.value"
            :tab="tab.label"
          />
        </NTabs>

        <p v-if="setupSnippetError" class="error-panel pad-md copy-xs line-normal">{{ setupSnippetError }}</p>
        <pre v-else class="max-height-md scroll-any round-md framed frame-standard muted-bg pad-lg copy-xs line-normal base-text"><code>{{ setupSnippet }}</code></pre>

        <div class="layout-row align-end-row space-md">
          <NButton secondary attr-type="button" :disabled="Boolean(setupSnippetError)" @click="copySetupSnippet">{{ setupCopyLabel }}</NButton>
          <NButton type="primary" attr-type="button" @click="clearIssuedToken">Done</NButton>
        </div>
      </div>
    </NModal>
  </div>
</template>

<style>
.agent-page__header {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 1rem;
}

.agent-title-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.75rem;
  margin-bottom: 0.35rem;
}

.agent-section-tabs {
  min-width: 0;
}

.agent-section-tabs .n-tabs-nav {
  margin-bottom: 0;
}

.agent-overview {
  display: grid;
  gap: 1rem;
  padding: 1rem;
}

.agent-overview__main {
  display: grid;
  align-content: start;
  gap: 0.75rem;
  min-width: 0;
}

.agent-overview__heading {
  display: flex;
  align-items: end;
  justify-content: space-between;
  gap: 1rem;
}

.agent-overview__heading strong {
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 2rem;
  font-weight: 700;
  letter-spacing: 0;
  line-height: 1;
}

.agent-connection-meter {
  height: 0.5rem;
  overflow: hidden;
  border-radius: 999px;
  background: var(--app-panel-muted);
}

.agent-connection-meter span {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: linear-gradient(90deg, var(--app-accent), var(--app-success));
  transition: width 180ms ease;
}

.agent-overview__tags {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}

.agent-overview__metrics {
  display: grid;
  overflow: hidden;
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
}

.agent-overview__metric {
  display: grid;
  gap: 0.35rem;
  min-width: 0;
  padding: 0.85rem;
}

.agent-overview__metric + .agent-overview__metric {
  border-top: 1px solid var(--app-border-subtle);
}

.agent-overview__metric span,
.agent-runtime-metric span {
  color: var(--app-text-muted);
  font-size: 0.7rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.agent-overview__metric strong,
.agent-runtime-metric strong {
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 1.15rem;
  font-weight: 700;
  letter-spacing: 0;
}

.agent-overview__metric small,
.agent-runtime-metric small {
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.agent-runtime-card,
.agent-table-card {
  overflow: hidden;
}

.agent-section-header {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 0.75rem;
  border-bottom: 1px solid var(--app-border);
  padding: 1rem 1.25rem;
}

.agent-section-header h4 {
  margin: 0;
  color: var(--app-text);
  font-size: 1rem;
  font-weight: 700;
  letter-spacing: 0;
}

.agent-section-header p {
  margin: 0.25rem 0 0;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  line-height: 1.5;
}

.agent-runtime-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.agent-runtime-metric {
  display: grid;
  gap: 0.35rem;
  min-width: 0;
  border-top: 1px solid var(--app-border-subtle);
  padding: 1rem;
}

.agent-runtime-metric:nth-child(odd) {
  border-right: 1px solid var(--app-border-subtle);
}

.agent-cell {
  display: grid;
  gap: 0.45rem;
  min-width: 0;
}

.agent-cell__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  min-width: 0;
}

.agent-cell__name {
  min-width: 0;
  color: var(--app-text);
  font-weight: 700;
  overflow-wrap: anywhere;
}

.agent-cell__id {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  overflow-wrap: anywhere;
}

.agent-cell__labels {
  display: flex;
  flex-wrap: wrap;
  gap: 0.35rem;
}

.agent-selector {
  display: grid;
  gap: 0.2rem;
  max-width: 100%;
  border-left: 2px solid var(--app-border);
  padding-left: 0.55rem;
}

.agent-selector__label {
  color: var(--app-text-muted);
  font-size: 0.65rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.agent-selector__value {
  color: var(--app-text-muted);
  font-size: 0.6875rem;
  overflow-wrap: anywhere;
}

.agent-state-cell {
  min-width: 0;
}

.agent-state-cell__status {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.agent-state-dot {
  width: 0.6rem;
  height: 0.6rem;
  flex: 0 0 auto;
  border-radius: 999px;
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--app-border) 50%, transparent);
}

.agent-state-dot--connected {
  background: var(--app-success);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--app-success) 18%, transparent);
}

.agent-state-dot--offline {
  background: var(--app-warning);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--app-warning) 18%, transparent);
}

.agent-compact-stack {
  display: grid;
  gap: 0.35rem;
  min-width: 0;
}

.agent-metric-line {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 0.75rem;
  margin: 0;
  font-size: 0.75rem;
}

.agent-metric-line strong {
  font-weight: 700;
  text-align: right;
}

.agent-subline {
  margin: 0;
  font-size: 0.6875rem;
  line-height: 1.45;
  overflow-wrap: anywhere;
}

.agent-row-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 0.45rem;
}

.agent-setup-tabs {
  max-width: 100%;
}

.agent-setup-tabs .n-tabs-nav {
  width: min(100%, 32rem);
}

@media (min-width: 640px) {
  .agent-page__header,
  .agent-section-header {
    flex-direction: row;
    align-items: flex-end;
    justify-content: space-between;
  }

  .agent-overview__metrics {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .agent-overview__metric + .agent-overview__metric {
    border-top: 0;
    border-left: 1px solid var(--app-border-subtle);
  }

  .agent-runtime-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .agent-runtime-metric {
    border-right: 1px solid var(--app-border-subtle);
  }

  .agent-runtime-metric:nth-child(3n) {
    border-right: 0;
  }
}

@media (min-width: 960px) {
  .agent-overview {
    grid-template-columns: minmax(18rem, 0.9fr) minmax(0, 1.4fr);
    align-items: stretch;
    padding: 1.25rem;
  }

  .agent-overview__metrics {
    align-self: stretch;
  }

  .agent-runtime-grid {
    grid-template-columns: repeat(6, minmax(0, 1fr));
  }

  .agent-runtime-metric {
    border-right: 1px solid var(--app-border-subtle);
  }

  .agent-runtime-metric:nth-child(3n) {
    border-right: 1px solid var(--app-border-subtle);
  }

  .agent-runtime-metric:nth-child(6n) {
    border-right: 0;
  }
}
</style>
