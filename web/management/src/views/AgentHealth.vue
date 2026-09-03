<script setup lang="ts">
import { computed, h, inject, nextTick, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { NAlert, NButton, NCheckbox, NDataTable, NDropdown, NInput, NModal, NTab, NTabs, NTag } from "naive-ui";
import type { DataTableColumns, DropdownDividerOption, DropdownOption } from "naive-ui";
import { Ban as BanIcon } from "@lucide/vue";
import { Check as CheckIcon } from "@lucide/vue";
import { Ellipsis as EllipsisIcon } from "@lucide/vue";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { Search as SearchIcon } from "@lucide/vue";
import { CircleX as TimesCircleIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { useManagementClient } from "@/composables/useManagementClient";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import AgentAvailabilityChart from "@/components/AgentAvailabilityChart.vue";
import AgentEditorModal from "@/components/editors/AgentEditorModal.vue";
import { dashboardKey, isBusyKey, publicProxyConfigKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { AGENT_ID_SYSTEM_LABEL_KEY, userAgentLabelPairs } from "@/lib/agentLabels";
import {
  DEFAULT_LOCAL_AGENT_BINARY_PATH,
  DEFAULT_LOCAL_INSTALLER_PATH,
  FALLBACK_RELEASE_REPOSITORY,
  cliSnippet as buildCliSnippet,
  dockerComposeSnippet as buildDockerComposeSnippet,
  dockerImageForRepository,
  linuxInstallSnippet,
  linuxUninstallSnippet,
  normalizeManagementUrl as normalizeSetupManagementUrl,
  normalizeReleaseVersion,
} from "@/lib/agentSetupSnippets";
import {
  agentUptimeSummaryById,
  fleetUptimePercent,
  formatBytes,
  formatLongDuration,
  formatPercent,
  recentDisconnectCount,
} from "@/lib/dashboardStats";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";
import { messageFromError } from "@/lib/errors";
import { sessionForAvailabilitySegment } from "@/lib/agentAvailability";
import type { AvailabilitySegment, AvailabilityWindow } from "@/lib/agentAvailability";
import { agentBuildStatus, shortBuildCommit, type AgentBuildState } from "@/lib/agentVersion";
import { summarizeAgentCapacity } from "@/lib/agentCapacity";
import { modalCardStyle, modalScrollableContentStyle } from "@/lib/naiveUi";
import type {
  Agent,
  AgentConnectionSession,
  AgentUptimeSummary,
  GetAgentAvailabilityResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";
import type { CreatedAgentSetup } from "@/types/agentSetup";

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
  {
    key: "updates",
    label: "Updates",
    path: "/agent/updates",
    description: "Trusted releases, rollout safety, and managed agent update campaigns.",
  },
] as const;

type AgentSectionKey = typeof agentSections[number]["key"];

const dashboard = inject(dashboardKey, computed(() => null));
const publicProxyConfig = inject(publicProxyConfigKey, computed(() => null));
const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));

const lifecycleDialog = useConfirmDialog();
const discardSetupDialog = useConfirmDialog();
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
const agentsNeedingUpdate = computed(() => agents.value.filter((agent) => buildStatusForAgent(agent).state === "update_available").length);
const agentsWithBuildMismatch = computed(() => agents.value.filter((agent) => buildStatusForAgent(agent).state === "different").length);
const agentsWithUnverifiedBuild = computed(() => agents.value.filter((agent) => buildStatusForAgent(agent).state === "unverified").length);
const fleetUptime = computed(() => fleetUptimePercent(uptimeSummaries.value));
const longestCurrentUptimeMillis = computed(() => Math.max(0, ...uptimeSummaries.value.map((summary) => Number(summary.currentUptimeMillis || 0n))));
const recentDisconnects = computed(() => recentDisconnectCount(recentAgentConnections.value, dashboard?.value?.generatedAtUnixMillis ?? BigInt(Date.now())));
const retentionDaysLabel = computed(() => `${(dashboard?.value?.retentionDays ?? 30n).toString()}d`);
const connectedAgentPercent = computed(() => {
  if (!enabledAgents.value) return 0;
  return Math.round((connectedAgentCount.value / enabledAgents.value) * 100);
});
const connectedAgentPercentStyle = computed(() => ({
  transform: `scaleX(${Math.min(100, Math.max(0, connectedAgentPercent.value)) / 100})`,
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

type AgentStateFilter = "all" | "attention" | "version" | "connected" | "disabled";

const agentSearch = ref("");
const agentStateFilter = ref<AgentStateFilter>("all");
const agentStateOptions: Array<{ label: string; value: AgentStateFilter }> = [
  { label: "All states", value: "all" },
  { label: "Needs attention (offline)", value: "attention" },
  { label: "Version differs", value: "version" },
  { label: "Connected", value: "connected" },
  { label: "Disabled", value: "disabled" },
];
const filteredAgents = computed(() => {
  const query = agentSearch.value.trim().toLocaleLowerCase();
  return [...agents.value]
    .filter((agent) => agentMatchesState(agent, agentStateFilter.value))
    .filter((agent) => !query || agentSearchText(agent).includes(query))
    .sort(compareAgentsByAttention);
});
const agentFilterActive = computed(() => Boolean(agentSearch.value.trim()) || agentStateFilter.value !== "all");
const filteredAgentSummary = computed(() => {
  if (!agentFilterActive.value) return `${totalAgents.value} agents · offline and version-different agents first`;
  return `${filteredAgents.value.length} of ${totalAgents.value} agents`;
});
const investigatedAgentPublicId = computed(() => {
  const value = Array.isArray(route.query.agent) ? route.query.agent[0] : route.query.agent;
  return typeof value === "string" ? value : "";
});
const investigatedAgent = computed(() => agents.value.find((agent) => agent.publicId === investigatedAgentPublicId.value) ?? null);
const filteredAgentConnections = computed(() => {
  if (!investigatedAgentPublicId.value) return recentAgentConnections.value;
  return recentAgentConnections.value.filter((session) => session.agentPublicId === investigatedAgentPublicId.value);
});
const activityAgentOptions = computed(() => agents.value.map((agent) => ({
  label: `${agent.name} · ${agent.publicId}`,
  value: agent.publicId,
})));
const availabilityWindow = ref<AvailabilityWindow>("24h");
const availability = ref<GetAgentAvailabilityResponse | null>(null);
const availabilityLoading = ref(false);
const availabilityError = ref("");
const availabilityRetry = ref(0);
const sessionPage = ref(1);
const highlightedSessionId = ref("");
const inspectedSegmentSummary = ref("");
let availabilityRequestSequence = 0;

const agentEditor = ref<InstanceType<typeof AgentEditorModal> | null>(null);
const openAgentActionMenuId = ref("");
const rotateAgentToConfirm = ref<Agent | null>(null);
const issuedToken = ref("");
const issuedAgent = ref<Agent | null>(null);
const issuedUpdaterEnrollmentToken = ref("");
const issuedUpdaterAuthorityPublicKeyBase64 = ref("");
const issuedUpdaterAuthorityKeyId = ref("");
const issuedUpdaterAuthorityEpoch = ref(0n);
const setupManagedUpdates = ref(false);
const setupContext = ref<"create" | "rotate">("create");
const setupManagementUrl = ref(defaultManagementUrl());
const setupManagementCAFile = ref("");
const setupAgentTLSCertFile = ref("/etc/p2pstream/agent.crt.pem");
const setupAgentTLSKeyFile = ref("/etc/p2pstream/agent.key.pem");
const setupAllowInsecureManagement = ref(false);
const setupAgentAllowTargets = ref("");
const setupAgentAllowAnyTarget = ref(false);
const setupReleaseRepository = ref(defaultReleaseRepository());
const setupReleaseVersion = ref(defaultReleaseVersion());
const setupInstallerPath = ref(DEFAULT_LOCAL_INSTALLER_PATH);
const setupAgentBinaryPath = ref(DEFAULT_LOCAL_AGENT_BINARY_PATH);
const setupDockerImage = ref(defaultDockerImage(setupReleaseRepository.value, setupReleaseVersion.value));
const setupDockerImageTouched = ref(false);
const setupTab = ref<"install" | "docker" | "cli">("install");
const setupCopyLabel = ref("Copy");
const setupSnippetWasCopied = ref(false);
const issuedTokenWasCopied = ref(false);
const issuedUpdaterTokenWasCopied = ref(false);
const issuedTokenCopyLabel = ref("Copy token");
const issuedUpdaterTokenCopyLabel = ref("Copy token");
const setupAdvancedOpen = ref(false);
const uninstallAgent = ref<Agent | null>(null);
const uninstallReleaseRepository = ref(defaultReleaseRepository());
const uninstallCopyLabel = ref("Copy");
let uninstallCopyReset: number | undefined;

const sessionPagination = computed(() => ({ page: sessionPage.value, pageSize: 12 }));

const busyDisabledReason = computed(() => isBusy?.value ? BUSY_REASON : "");
const normalizedManagementUrl = computed(() => normalizeSetupManagementUrl(setupManagementUrl.value));
const managementUsesTLS = computed(() => normalizedManagementUrl.value.toLowerCase().startsWith("https://"));
const agentClientCertificateRequired = computed(() => Boolean(managementSecurity.value?.agentClientCertificateRequired));
const setupIsRotation = computed(() => setupContext.value === "rotate");
const setupCredentialCopyComplete = computed(() => setupTab.value !== "install" || (
  issuedTokenWasCopied.value && (!setupManagedUpdates.value || !issuedUpdaterEnrollmentToken.value || issuedUpdaterTokenWasCopied.value)
));
const setupHandoffComplete = computed(() => setupSnippetWasCopied.value && setupCredentialCopyComplete.value);
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
    minWidth: 300,
    render: (agent) => h("div", { class: "agent-cell" }, [
      h("div", { class: "agent-cell__header" }, [
        h("bdi", {
          class: "agent-cell__name",
          dir: "ltr",
          title: diagnosticInspectionText(agent.name),
        }, diagnosticExcerpt(agent.name, 64).text),
      ]),
      h("p", {
        class: "agent-cell__id mono-text",
        title: diagnosticInspectionText(agent.publicId),
      }, [h("bdi", { dir: "ltr" }, diagnosticInspectionText(agent.publicId))]),
      h("div", { class: "agent-cell__mobile-build" }, [
        renderAgentVersion(agent, true),
        renderAgentCapacity(agent, true),
      ]),
      h("details", { class: "agent-exact-details" }, [
        h("summary", {
          "aria-label": agentActionLabel("Show exact identity and selectors for", agent),
        }, `Identity & selectors · ${agentUserLabels(agent).length} label${agentUserLabels(agent).length === 1 ? "" : "s"}`),
        h("div", { class: "agent-exact-details__body" }, [
          h("div", { class: "agent-exact-field" }, [
            h("span", "Agent ID"),
            h("code", { class: "mono-text" }, [h("bdi", { dir: "ltr" }, diagnosticInspectionText(agent.publicId))]),
          ]),
          h("div", { class: "agent-exact-field" }, [
            h("span", "Exact selector"),
            h("code", { class: "mono-text" }, [
              h("bdi", { dir: "ltr" }, diagnosticInspectionText(AGENT_ID_SYSTEM_LABEL_KEY)),
              "=",
              h("bdi", { dir: "ltr" }, diagnosticInspectionText(exactAgentSelectorValue(agent))),
            ]),
          ]),
          h("div", { class: "agent-exact-field" }, [
            h("span", "User labels"),
            agentUserLabels(agent).length
              ? h("ul", { class: "agent-exact-labels" }, agentUserLabels(agent).map((label) => h("li", { key: label.id }, [
                  h("code", { class: "mono-text" }, [
                    h("bdi", { dir: "ltr" }, diagnosticInspectionText(label.key)),
                    "=",
                    h("bdi", { dir: "ltr" }, diagnosticInspectionText(label.value)),
                  ]),
                ])))
              : h("p", { class: "copy-xs muted-text" }, "No user labels"),
          ]),
        ]),
      ]),
    ]),
  },
  {
    title: "Health & reliability",
    key: "health",
    width: 260,
    render: (agent) => h("div", { class: "agent-state-cell" }, [
      h("div", { class: "agent-state-cell__status" }, [
        h("span", { class: ["agent-state-dot", `agent-state-dot--${agentOperationalState(agent)}`], "aria-hidden": "true" }),
        h("span", { class: "weight-semibold base-text" }, agentOperationalStateLabel(agent)),
      ]),
      h("p", { class: "agent-state-cell__duration mono-text copy-xs muted-text" }, currentAgentStateSummary(agent)),
      h("p", { class: "agent-metric-line" }, [
        h("span", { class: "muted-text" }, "Fleet uptime"),
        h("strong", { class: "mono-text base-text" }, agentUptimePercentLabel(agent)),
      ]),
      h("p", { class: "agent-metric-line" }, [
        h("span", { class: "muted-text" }, "Connect / drop"),
        h("strong", { class: "mono-text base-text" }, agentConnectionCounts(agent)),
      ]),
      h("p", { class: "agent-subline mono-text muted-text" }, agentLastTransitionLabel(agent)),
    ]),
  },
  {
    title: "Capacity",
    key: "capacity",
    width: 270,
    render: (agent) => renderAgentCapacity(agent),
  },
  {
    title: "Version",
    key: "version",
    width: 220,
    render: (agent) => renderAgentVersion(agent),
  },
  {
    title: "Actions",
    key: "actions",
    width: 330,
    align: "right",
    render: (agent) => h("div", { class: "agent-row-actions" }, [
      h(NButton, {
        secondary: true,
        size: "small",
        "aria-label": agentActionLabel("Investigate", agent),
        onClick: () => void investigateAgent(agent),
      }, { icon: () => h(SearchIcon, { class: "icon-sm" }), default: () => "Investigate" }),
      h(DisabledHint, { disabled: Boolean(busyDisabledReason.value), reason: busyDisabledReason.value }, {
        default: () => h(NButton, {
          secondary: true,
          size: "small",
          "aria-label": agentActionLabel("Edit", agent),
          disabled: Boolean(busyDisabledReason.value),
          onClick: () => editAgent(agent),
        }, { icon: () => h(PencilIcon, { class: "icon-sm" }), default: () => "Edit" }),
      }),
      h(NDropdown, {
        trigger: "click",
        placement: "bottom-end",
        show: openAgentActionMenuId.value === agent.id.toString(),
        options: agentLifecycleOptions(agent),
        menuProps: () => ({
          id: agentActionMenuId(agent),
          role: "menu",
          "aria-label": agentActionLabel("Lifecycle actions for", agent),
        }),
        nodeProps: (option: DropdownOption) => ({
          role: "menuitem",
          "aria-disabled": option.disabled ? ("true" as const) : undefined,
        }),
        onUpdateShow: (show: boolean) => setAgentActionMenuOpen(agent, show),
        onSelect: (key: string | number) => handleAgentLifecycleAction(agent, String(key)),
      }, {
        default: () => h(NButton, {
          secondary: true,
          size: "small",
          "aria-label": agentActionLabel("More actions for", agent),
          "aria-haspopup": "menu",
          "aria-controls": agentActionMenuId(agent),
          "aria-expanded": openAgentActionMenuId.value === agent.id.toString() ? "true" : "false",
        }, { icon: () => h(EllipsisIcon, { class: "icon-sm" }), default: () => "More" }),
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
      h("p", {
        class: "agent-session-value weight-medium base-text",
        title: diagnosticInspectionText(sessionAgentLabel(session)),
      }, [h("bdi", { dir: "ltr" }, diagnosticExcerpt(sessionAgentLabel(session), 64).text)]),
      sessionAgentDetail(session)
        ? h("p", {
            class: "agent-session-value mono-text copy-xs muted-text",
            title: diagnosticInspectionText(sessionAgentDetail(session)),
          }, [h("bdi", { dir: "ltr" }, diagnosticExcerpt(sessionAgentDetail(session), 64).text)])
        : null,
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

watch(setupSnippet, () => {
  setupSnippetWasCopied.value = false;
  setupCopyLabel.value = setupCopyActionLabel();
});

watch(managementUsesTLS, (usesTLS) => {
  if (!usesTLS && issuedAgent.value) {
    setupAdvancedOpen.value = true;
  }
});

watch(
  [
    activeAgentSection,
    investigatedAgentPublicId,
    availabilityWindow,
    () => dashboard?.value?.generatedAtUnixMillis ?? 0n,
    availabilityRetry,
  ],
  () => void loadAgentAvailability(),
  { immediate: true },
);

watch([investigatedAgentPublicId, availabilityWindow], clearInspectedAvailabilitySegment);

watch(filteredAgentConnections, (sessions) => {
  const maxPage = Math.max(1, Math.ceil(sessions.length / 12));
  if (sessionPage.value > maxPage) sessionPage.value = maxPage;
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

function agentOperationalState(agent: Agent): "connected" | "offline" | "disabled" {
  if (!agent.enabled) return "disabled";
  return agentConnected(agent) ? "connected" : "offline";
}

function agentOperationalStateLabel(agent: Agent): string {
  switch (agentOperationalState(agent)) {
    case "connected":
      return "Connected";
    case "offline":
      return "Offline · needs attention";
    default:
      return "Disabled";
  }
}

function currentAgentStateSummary(agent: Agent): string {
  if (!agent.enabled) return "Not accepting traffic";
  const duration = currentAgentDuration(agent);
  if (duration === "-") return agentConnected(agent) ? "Connected" : "Offline duration unavailable";
  return agentConnected(agent) ? `${duration} current uptime` : `${duration} offline`;
}

function currentAgentDuration(agent: Agent): string {
  const uptime = uptimeForAgent(agent);
  if (!uptime) return "-";
  return uptime.connected
    ? formatLongDuration(uptime.currentUptimeMillis)
    : formatLongDuration(uptime.currentDowntimeMillis);
}

function agentUptimePercentLabel(agent: Agent): string {
  return formatPercent(uptimeForAgent(agent)?.uptimePercent);
}

function agentConnectionCounts(agent: Agent): string {
  const uptime = uptimeForAgent(agent);
  if (!uptime) return "-";
  return `${uptime.connectionCount.toString()} / ${uptime.disconnectCount.toString()}`;
}

function agentLastTransitionLabel(agent: Agent): string {
  if (agentConnected(agent)) return `Last connected ${formatDate(agentLastConnected(agent))}`;
  return `Last disconnected ${formatDate(agentLastDisconnected(agent))}`;
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

function buildStatusForAgent(agent: Agent) {
  return agentBuildStatus({
    agentVersion: agent.version,
    agentCommit: agent.commit,
    serverVersion: status.value?.version,
    serverCommit: status.value?.commit,
  });
}

function renderAgentVersion(agent: Agent, compact = false) {
  const build = buildStatusForAgent(agent);
  const version = agent.version ? diagnosticExcerpt(agent.version, 32).text : "Unknown";
  const commit = shortBuildCommit(agent.commit);
  const serverVersion = diagnosticExcerpt(status.value?.version ?? "", 24).text;
  const details = [
    commit ? `build ${diagnosticInspectionText(commit)}` : "",
    status.value?.version ? `server ${serverVersion}` : "",
  ].filter(Boolean).join(" · ");
  return h("div", { class: ["agent-version-cell", compact && "agent-version-cell--compact"] }, [
    h("div", { class: "agent-version-cell__header" }, [
      h("code", {
        class: "mono-text agent-version-cell__value",
        title: agent.version ? diagnosticInspectionText(agent.version) : "Agent version has not been reported",
      }, [h("bdi", { dir: "ltr" }, version)]),
      h(NTag, {
        size: "small",
        bordered: false,
        type: agentBuildStatusType(build.state),
      }, { default: () => build.label }),
    ]),
    compact ? null : h("p", { class: "agent-version-cell__detail mono-text muted-text" }, details || "Waiting for a compatible heartbeat"),
  ]);
}

function renderAgentCapacity(agent: Agent, compact = false) {
  const capacity = summarizeAgentCapacity(agent);
  if (capacity.state === "offline") {
    return h("div", { class: ["agent-capacity-cell", compact && "agent-capacity-cell--compact"] }, [
      h("div", { class: "agent-capacity-cell__header" }, [
        h("strong", { class: "agent-capacity-cell__value" }, "Tunnel offline"),
        h(NTag, { size: "small", bordered: false, type: "default" }, { default: () => "Unavailable" }),
      ]),
      compact ? null : h("p", { class: "agent-subline muted-text" }, "Capacity is negotiated when the agent reconnects."),
    ]);
  }
  if (capacity.state === "unreported") {
    return h("div", { class: ["agent-capacity-cell", compact && "agent-capacity-cell--compact"] }, [
      h("div", { class: "agent-capacity-cell__header" }, [
        h("strong", { class: "agent-capacity-cell__value" }, "Not reported"),
        h(NTag, { size: "small", bordered: false, type: "warning" }, { default: () => "Legacy agent" }),
      ]),
      compact ? null : h("p", { class: "agent-subline warning-text" }, "Update this agent to expose tunnel capacity."),
    ]);
  }

  const stateLabel = capacity.state === "pressured"
    ? capacity.pressure === "critical" ? "Critical pressure" : "Throttling"
    : capacity.state === "degraded" ? "Sensor degraded"
    : capacity.adaptive ? capacity.pressure === "healthy" ? "Healthy" : "Adaptive"
    : capacity.state === "server_capped" ? "Server capped" : "Fixed";
  const stateType = capacity.state === "pressured"
    ? capacity.pressure === "critical" ? "error" : "warning"
    : capacity.state === "degraded" ? "warning"
    : capacity.adaptive ? "success"
    : capacity.state === "server_capped" ? "warning" : "info";
  const memory = capacity.memoryUsageBytes !== undefined && capacity.memoryLimitBytes !== undefined && capacity.memoryLimitBytes > 0n
    ? `${formatBytes(capacity.memoryUsageBytes)} / ${formatBytes(capacity.memoryLimitBytes)}${capacity.memoryPercent === undefined ? "" : ` · ${capacity.memoryPercent.toFixed(1)}%`}`
    : capacity.memorySysMb === undefined ? "No sample" : `${bigIntLabel(capacity.memorySysMb)} MB process`;
  const headline = capacity.adaptive ? "Adaptive admission" : `${capacity.negotiated.toString()} streams`;
  const fileDescriptors = capacity.fileDescriptorsUsed !== undefined && capacity.fileDescriptorsLimit !== undefined && capacity.fileDescriptorsLimit > 0n
    ? `${capacity.fileDescriptorsUsed.toString()} / ${capacity.fileDescriptorsLimit.toString()}${capacity.fileDescriptorsPercent === undefined ? "" : ` · ${capacity.fileDescriptorsPercent.toFixed(1)}%`}`
    : "No sample";
  return h("div", { class: ["agent-capacity-cell", compact && "agent-capacity-cell--compact"] }, [
    h("div", { class: "agent-capacity-cell__header" }, [
      h("strong", { class: "agent-capacity-cell__value" }, headline),
      h(NTag, { size: "small", bordered: false, type: stateType }, { default: () => stateLabel }),
    ]),
    h("p", { class: "agent-capacity-cell__summary mono-text" }, `${capacity.active.toString()} active · ${capacity.headroom.toString()} immediately available`),
    compact ? null : h("div", {
      class: "agent-capacity-meter",
      role: "progressbar",
      "aria-label": "Tunnel stream utilization",
      "aria-valuemin": 0,
      "aria-valuemax": 100,
      "aria-valuenow": capacity.utilizationPercent,
    }, [h("span", { style: { transform: `scaleX(${capacity.utilizationPercent / 100})` } })]),
    compact ? null : h("div", { class: "agent-capacity-cell__details" }, [
      h("p", { class: "agent-metric-line" }, [
        h("span", { class: "muted-text" }, capacity.adaptive ? "Current allowance" : "Advertised"),
        h("strong", { class: "mono-text base-text" }, capacity.adaptive ? capacity.admissionLimit.toString() : capacity.advertised.toString()),
      ]),
      h("p", { class: "agent-metric-line" }, [
        h("span", { class: "muted-text" }, capacity.adaptive ? `Memory${capacity.memorySource ? ` · ${capacity.memorySource}` : ""}` : "Process memory"),
        h("strong", { class: "mono-text base-text" }, memory),
      ]),
      capacity.adaptive ? h("p", { class: "agent-metric-line" }, [
        h("span", { class: "muted-text" }, "File descriptors"),
        h("strong", { class: "mono-text base-text" }, fileDescriptors),
      ]) : null,
      capacity.sensorDegraded ? h("p", { class: "agent-subline warning-text" }, capacity.lastGoodUnixMillis
        ? `Resource sensor unavailable · last good ${formatDate(capacity.lastGoodUnixMillis)}`
        : "Resource sensor unavailable · new streams are paused") : null,
      capacity.adaptive ? h("p", { class: "agent-metric-line" }, [
        h("span", { class: "muted-text" }, "Protocol guard"),
        h("strong", { class: "mono-text base-text" }, capacity.negotiated.toString()),
      ]) : null,
    ]),
  ]);
}

function agentBuildStatusType(state: AgentBuildState): "default" | "success" | "warning" | "info" {
  switch (state) {
    case "current":
      return "success";
    case "update_available":
    case "different":
    case "unverified":
      return "warning";
    case "ahead":
    case "reported":
      return "info";
    default:
      return "default";
  }
}

function agentMatchesState(agent: Agent, filter: AgentStateFilter): boolean {
  const state = agentOperationalState(agent);
  switch (filter) {
    case "attention":
      return state === "offline";
    case "version": {
      const buildState = buildStatusForAgent(agent).state;
      return buildState === "update_available" || buildState === "different" || buildState === "unverified";
    }
    case "connected":
      return state === "connected";
    case "disabled":
      return state === "disabled";
    default:
      return true;
  }
}

function agentSearchText(agent: Agent): string {
  return [
    agent.name,
    agent.publicId,
    agent.version,
    agent.commit,
    exactAgentSelector(agent),
    ...Object.entries(agent.labels).flatMap(([key, value]) => [key, value, `${key}=${value}`]),
  ].join("\n").toLocaleLowerCase();
}

function compareAgentsByAttention(left: Agent, right: Agent): number {
  const rank = (agent: Agent) => {
    const state = agentOperationalState(agent);
    if (state === "offline") return 0;
    const buildState = buildStatusForAgent(agent).state;
    if (buildState === "update_available" || buildState === "different" || buildState === "unverified") return 1;
    if (state === "connected") return 2;
    return 3;
  };
  return rank(left) - rank(right)
    || left.name.localeCompare(right.name)
    || left.publicId.localeCompare(right.publicId);
}

function clearAgentFilters() {
  agentSearch.value = "";
  agentStateFilter.value = "all";
}

function openAddAgentModal() {
  agentEditor.value?.openCreate();
}

function editAgent(agent: Agent) {
  agentEditor.value?.openEdit(agent.id);
}

async function investigateAgent(agent: Agent) {
  await router.push({ path: "/agent/activity", query: { agent: agent.publicId } });
}

async function clearInvestigatedAgent() {
  await router.replace({ path: "/agent/activity" });
}

async function selectActivityAgent(agentPublicId: string) {
  if (agentPublicId === investigatedAgentPublicId.value) return;
  await router.replace({ path: "/agent/activity", query: { agent: agentPublicId } });
}

async function loadAgentAvailability() {
  const requestSequence = ++availabilityRequestSequence;
  const agentPublicId = investigatedAgentPublicId.value;
  const windowLabel = availabilityWindow.value;
  if (activeAgentSection.value !== "activity" || !agentPublicId) {
    availability.value = null;
    availabilityError.value = "";
    availabilityLoading.value = false;
    return;
  }

  if (availability.value?.agentPublicId !== agentPublicId || availability.value?.windowLabel !== windowLabel) {
    availability.value = null;
  }
  availabilityLoading.value = true;
  availabilityError.value = "";
  try {
    const response = await managementClient.getAgentAvailability({
      agentPublicId,
      windowLabel,
    });
    if (requestSequence !== availabilityRequestSequence) return;
    availability.value = response;
  } catch (error) {
    if (requestSequence !== availabilityRequestSequence) return;
    availabilityError.value = messageFromError(error);
  } finally {
    if (requestSequence === availabilityRequestSequence) availabilityLoading.value = false;
  }
}

function retryAgentAvailability() {
  availabilityRetry.value += 1;
}

function inspectAvailabilitySegment(segment: AvailabilitySegment) {
  const duration = formatLongDuration(segment.endMillis - segment.startMillis);
  const state = segment.state === "online" ? "Online session" : "Outage";
  const match = sessionForAvailabilitySegment(segment, filteredAgentConnections.value);
  if (!match) {
    highlightedSessionId.value = "";
    inspectedSegmentSummary.value = `${state} · ${duration} · outside the recent session table`;
    return;
  }

  const relation = match.relation === "reconnect"
    ? "reconnect highlighted"
    : match.relation === "disconnect"
      ? "disconnect highlighted"
      : "session highlighted";
  inspectedSegmentSummary.value = `${state} · ${duration} · ${relation}`;
  highlightedSessionId.value = match.session.id.toString();
  const sessionIndex = filteredAgentConnections.value.findIndex((session) => session.id === match.session.id);
  if (sessionIndex >= 0) sessionPage.value = Math.floor(sessionIndex / 12) + 1;
  void revealHighlightedSession();
}

function clearInspectedAvailabilitySegment() {
  highlightedSessionId.value = "";
  inspectedSegmentSummary.value = "";
}

async function revealHighlightedSession() {
  await nextTick();
  window.requestAnimationFrame(() => {
    const row = document.querySelector<HTMLElement>(`[data-session-id="${highlightedSessionId.value}"]`);
    if (!row) return;
    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    row.scrollIntoView({ behavior: reducedMotion ? "auto" : "smooth", block: "center" });
  });
}

function agentUserLabels(agent: Agent) {
  return userAgentLabelPairs(agent.labels);
}

function exactAgentSelector(agent: Agent): string {
  return `${AGENT_ID_SYSTEM_LABEL_KEY}=${exactAgentSelectorValue(agent)}`;
}

function exactAgentSelectorValue(agent: Agent): string {
  return agent.labels[AGENT_ID_SYSTEM_LABEL_KEY] || agent.publicId;
}

function agentActionLabel(action: string, agent: Agent): string {
  const name = diagnosticExcerpt(agent.name, 48).text;
  const publicID = diagnosticExcerpt(agent.publicId, 64).text;
  return `${action} agent \u2068${name}\u2069 (\u2068${publicID}\u2069)`;
}

function agentActionMenuId(agent: Agent): string {
  return `agent-lifecycle-menu-${agent.id.toString()}`;
}

function setAgentActionMenuOpen(agent: Agent, show: boolean) {
  openAgentActionMenuId.value = show ? agent.id.toString() : "";
}

function agentLifecycleOptions(agent: Agent): Array<DropdownOption | DropdownDividerOption> {
  const busy = Boolean(busyDisabledReason.value);
  const deleteReason = deleteAgentDisabledReason(agent);
  return [
    {
      key: "toggle",
      label: agent.enabled ? "Disable agent" : "Enable agent",
      icon: () => agent.enabled ? h(BanIcon, { class: "icon-sm" }) : h(CheckIcon, { class: "icon-sm" }),
      disabled: busy,
      props: {
        "aria-label": agentActionLabel(agent.enabled ? "Disable" : "Enable", agent),
        title: busyDisabledReason.value || undefined,
      },
    },
    {
      key: "rotate",
      label: "Rotate token",
      icon: () => h(RefreshIcon, { class: "icon-sm" }),
      disabled: busy,
      props: {
        "aria-label": agentActionLabel("Rotate token for", agent),
        title: busyDisabledReason.value || undefined,
      },
    },
    {
      key: "uninstall",
      label: "Show uninstall command",
      icon: () => h(TimesCircleIcon, { class: "icon-sm" }),
      props: {
        "aria-label": agentActionLabel("Show uninstall command for", agent),
      },
    },
    { type: "divider", key: "danger-divider" },
    {
      key: "delete",
      label: "Delete agent",
      icon: () => h(TrashIcon, { class: "icon-sm" }),
      disabled: Boolean(deleteReason),
      props: {
        "aria-label": agentActionLabel("Delete", agent),
        title: deleteReason || undefined,
      },
    },
  ];
}

function handleAgentLifecycleAction(agent: Agent, action: string) {
  openAgentActionMenuId.value = "";
  switch (action) {
    case "toggle":
      if (!busyDisabledReason.value) void setAgentEnabled(agent, !agent.enabled);
      break;
    case "rotate":
      if (!busyDisabledReason.value) rotateAgentToken(agent);
      break;
    case "uninstall":
      openUninstallModal(agent);
      break;
    case "delete":
      if (!deleteAgentDisabledReason(agent)) void deleteAgent(agent);
      break;
  }
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
  if (!show) void requestClearIssuedToken();
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

function sessionRowProps(session: AgentConnectionSession): Record<string, string> {
  const highlighted = highlightedSessionId.value === session.id.toString();
  return {
    "data-session-id": session.id.toString(),
    class: highlighted ? "agent-session-row--highlighted" : "",
    "aria-current": highlighted ? "true" : "false",
  };
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
  const name = diagnosticInspectionText(agent.name);
  const publicID = diagnosticInspectionText(agent.publicId);
  if (!await lifecycleDialog.confirm(
    "Delete Agent",
    `Agent \u2068${name}\u2069 (\u2068${publicID}\u2069) and its agent-selected target matches will be permanently removed.`,
  )) return;
  await run(async () => {
    await managementClient.deleteAgent({ id: agent.id });
  });
}

function clearIssuedToken() {
  issuedToken.value = "";
  issuedAgent.value = null;
  issuedUpdaterEnrollmentToken.value = "";
  issuedUpdaterAuthorityPublicKeyBase64.value = "";
  issuedUpdaterAuthorityKeyId.value = "";
  issuedUpdaterAuthorityEpoch.value = 0n;
  setupManagedUpdates.value = false;
  setupContext.value = "create";
  setupSnippetWasCopied.value = false;
  issuedTokenWasCopied.value = false;
  issuedUpdaterTokenWasCopied.value = false;
  issuedTokenCopyLabel.value = "Copy token";
  issuedUpdaterTokenCopyLabel.value = "Copy token";
  setupAdvancedOpen.value = false;
  setupCopyLabel.value = setupCopyActionLabel();
}

async function requestClearIssuedToken() {
	if (issuedToken.value && !setupHandoffComplete.value) {
		const missing = [
			!setupSnippetWasCopied.value ? "setup command" : "",
			setupTab.value === "install" && !issuedTokenWasCopied.value ? "agent token" : "",
			setupTab.value === "install" && setupManagedUpdates.value && issuedUpdaterEnrollmentToken.value && !issuedUpdaterTokenWasCopied.value ? "updater token" : "",
		].filter(Boolean).join(", ");
    const confirmed = await discardSetupDialog.confirm(
      "Close Without Copying?",
			`The ${missing} ${missing.includes(",") ? "have" : "has"} not been copied. Closing now permanently discards the one-time credentials.`,
			"Discard Credentials",
    );
    if (!confirmed) return;
  }
  clearIssuedToken();
}

function openSetupModal(agent: Agent | null, token: string, context: "create" | "rotate" = "create", updater?: Pick<CreatedAgentSetup, "updaterEnrollmentToken" | "updaterPinnedRepository" | "updaterManagementAuthorityPublicKeyBase64" | "updaterManagementAuthorityKeyId" | "updaterManagementAuthorityEpoch">) {
  if (!agent || !token) return;
  issuedAgent.value = agent;
  issuedToken.value = token;
  issuedUpdaterEnrollmentToken.value = updater?.updaterEnrollmentToken ?? "";
  issuedUpdaterAuthorityPublicKeyBase64.value = updater?.updaterManagementAuthorityPublicKeyBase64 ?? "";
  issuedUpdaterAuthorityKeyId.value = updater?.updaterManagementAuthorityKeyId ?? "";
  issuedUpdaterAuthorityEpoch.value = updater?.updaterManagementAuthorityEpoch ?? 0n;
  setupManagedUpdates.value = Boolean(issuedUpdaterEnrollmentToken.value && issuedUpdaterAuthorityPublicKeyBase64.value && issuedUpdaterAuthorityKeyId.value && issuedUpdaterAuthorityEpoch.value > 0n && context === "create");
  setupContext.value = context;
  setupManagementUrl.value = defaultManagementUrl();
  setupManagementCAFile.value = "";
  setupAgentTLSCertFile.value = "/etc/p2pstream/agent.crt.pem";
  setupAgentTLSKeyFile.value = "/etc/p2pstream/agent.key.pem";
  setupAllowInsecureManagement.value = false;
  setupAgentAllowTargets.value = "";
  setupAgentAllowAnyTarget.value = false;
  setupReleaseRepository.value = updater?.updaterPinnedRepository || defaultReleaseRepository();
  setupReleaseVersion.value = defaultReleaseVersion();
  setupInstallerPath.value = DEFAULT_LOCAL_INSTALLER_PATH;
  setupAgentBinaryPath.value = DEFAULT_LOCAL_AGENT_BINARY_PATH;
  setupDockerImage.value = defaultDockerImage(setupReleaseRepository.value, setupReleaseVersion.value);
  setupDockerImageTouched.value = false;
  setupTab.value = "install";
  setupSnippetWasCopied.value = false;
  issuedTokenWasCopied.value = false;
  issuedUpdaterTokenWasCopied.value = false;
  issuedTokenCopyLabel.value = "Copy token";
  issuedUpdaterTokenCopyLabel.value = "Copy token";
  setupAdvancedOpen.value = !managementUsesTLS.value || agentClientCertificateRequired.value;
  setupCopyLabel.value = setupCopyActionLabel();
}

function handleAgentCreated(payload: CreatedAgentSetup) {
  openSetupModal(payload.agent, payload.token, "create", payload);
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
  const version = typeof configured === "string" ? configured.trim() : "";
  if (immutableReleaseVersion(version)) return version;
  const runningVersion = status.value?.version?.trim() ?? "";
  if (immutableReleaseVersion(runningVersion)) return runningVersion;
  return "latest";
}

function immutableReleaseVersion(version: string): boolean {
  try {
    return normalizeReleaseVersion(version) === version && version !== "latest";
  } catch {
    return false;
  }
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
    updaterEnrollmentToken: issuedUpdaterEnrollmentToken.value,
    agentUpdateAuthorityPublicKeyBase64: issuedUpdaterAuthorityPublicKeyBase64.value,
    agentUpdateAuthorityKeyId: issuedUpdaterAuthorityKeyId.value,
    agentUpdateAuthorityEpoch: issuedUpdaterAuthorityEpoch.value,
    enableManagedUpdates: setupManagedUpdates.value && setupTab.value === "install",
    repository: setupReleaseRepository.value,
    version: setupReleaseVersion.value,
    scriptRef: installerScriptRef(),
    dockerImage: setupDockerImage.value,
    installerPath: setupInstallerPath.value,
    agentBinaryPath: setupAgentBinaryPath.value,
    allowTargets: setupAgentAllowAnyTarget.value ? [] : splitSetupAgentAllowTargets(setupAgentAllowTargets.value),
    allowAnyTarget: setupAgentAllowAnyTarget.value,
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

function splitSetupAgentAllowTargets(value: string): string[] {
  return value.split(/[\s,]+/).map((entry) => entry.trim()).filter(Boolean);
}

async function copySetupSnippet() {
  if (setupSnippetError.value) {
    setupCopyLabel.value = "Invalid";
    setupSnippetWasCopied.value = false;
    return;
  }
  try {
    await navigator.clipboard.writeText(setupSnippet.value);
    setupCopyLabel.value = "Copied";
    setupSnippetWasCopied.value = true;
  } catch {
    setupCopyLabel.value = "Select command";
    setupSnippetWasCopied.value = false;
  }
}

async function copyIssuedCredential(value: string, kind: "agent" | "updater") {
  try {
    await navigator.clipboard.writeText(value);
    if (kind === "agent") {
      issuedTokenWasCopied.value = true;
      issuedTokenCopyLabel.value = "Copied";
    } else {
      issuedUpdaterTokenWasCopied.value = true;
      issuedUpdaterTokenCopyLabel.value = "Copied";
    }
  } catch {
    if (kind === "agent") {
      issuedTokenWasCopied.value = false;
      issuedTokenCopyLabel.value = "Select token";
    } else {
      issuedUpdaterTokenWasCopied.value = false;
      issuedUpdaterTokenCopyLabel.value = "Select token";
    }
  }
}

function setupCopyActionLabel(): string {
  switch (setupTab.value) {
    case "docker":
      return "Copy Docker Compose";
    case "cli":
      return "Copy CLI command";
    default:
      return "Copy install command";
  }
}

function handleSetupAdvancedToggle(event: Event) {
  setupAdvancedOpen.value = (event.currentTarget as HTMLDetailsElement).open;
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
          <NTag v-if="agentsNeedingUpdate" size="small" :bordered="false" type="warning">
            {{ agentsNeedingUpdate }} update{{ agentsNeedingUpdate === 1 ? '' : 's' }} available
          </NTag>
          <NTag v-if="agentsWithBuildMismatch" size="small" :bordered="false" type="warning">
            {{ agentsWithBuildMismatch }} build mismatch{{ agentsWithBuildMismatch === 1 ? '' : 'es' }}
          </NTag>
          <NTag v-if="agentsWithUnverifiedBuild" size="small" :bordered="false" type="warning">
            {{ agentsWithUnverifiedBuild }} build unverified
          </NTag>
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

    <AgentAvailabilityChart
      v-if="activeAgentSection === 'activity'"
      :availability="availability"
      :selected-agent-id="investigatedAgentPublicId"
      :agent-options="activityAgentOptions"
      :window="availabilityWindow"
      :loading="availabilityLoading"
      :error="availabilityError"
      @select-agent="selectActivityAgent"
      @update:window="availabilityWindow = $event"
      @inspect-segment="inspectAvailabilitySegment"
      @retry="retryAgentAvailability"
    />

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
          <p>Offline enabled agents are shown first. Expand identity details only when you need exact selectors.</p>
        </div>
        <NTag size="small" :bordered="false" type="default">{{ totalAgents }} total</NTag>
      </div>
      <div v-if="agents.length" class="agent-table-toolbar">
        <NInput
          v-model:value="agentSearch"
          clearable
          size="small"
          placeholder="Search name, ID, version, or label"
          :input-props="{ 'aria-label': 'Search agents' }"
          class="agent-search-input"
        >
          <template #prefix><SearchIcon class="icon-sm" aria-hidden="true" /></template>
        </NInput>
        <AccessibleSelect
          v-model:value="agentStateFilter"
          accessible-label="Filter agents by state"
          size="small"
          :options="agentStateOptions"
          class="agent-state-filter"
        />
        <p class="agent-filter-summary copy-xs muted-text" aria-live="polite">{{ filteredAgentSummary }}</p>
      </div>
      <NDataTable
        v-if="filteredAgents.length"
        :columns="agentColumns"
        :data="filteredAgents"
        :row-key="agentRowKey"
        :row-props="agentRowProps"
        :pagination="false"
        :bordered="false"
        :single-line="false"
        :scroll-x="1260"
        size="small"
      />
      <div v-else-if="agents.length" class="agent-filter-empty">
        <p class="weight-semibold base-text">No agents match these filters</p>
        <p class="copy-sm muted-text">Try another name, ID, label, or connection state.</p>
        <NButton secondary size="small" @click="clearAgentFilters">Clear filters</NButton>
      </div>
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
        <div class="agent-activity-context">
          <div v-if="investigatedAgentPublicId" class="agent-investigation-filter">
            <NTag size="small" :bordered="false" type="warning">
              Investigating <bdi dir="ltr">{{ diagnosticExcerpt(investigatedAgent?.name || investigatedAgentPublicId, 64).text }}</bdi>
            </NTag>
            <NButton
              secondary
              size="tiny"
              :aria-label="`Clear investigation for agent \u2068${diagnosticExcerpt(investigatedAgentPublicId, 64).text}\u2069`"
              @click="clearInvestigatedAgent"
            >
              Clear
            </NButton>
          </div>
          <div v-if="inspectedSegmentSummary" class="agent-chart-selection">
            <NTag size="small" :bordered="false" type="info">{{ inspectedSegmentSummary }}</NTag>
            <NButton text size="tiny" attr-type="button" @click="clearInspectedAvailabilitySegment">Clear selection</NButton>
          </div>
          <NTag size="small" :bordered="false" type="default">{{ filteredAgentConnections.length }} sessions</NTag>
        </div>
      </div>
      <NDataTable
        v-if="filteredAgentConnections.length"
        :columns="sessionColumns"
        :data="filteredAgentConnections"
        :row-key="sessionRowKey"
        :row-props="sessionRowProps"
        :pagination="sessionPagination"
        :bordered="false"
        :single-line="false"
        :scroll-x="870"
        size="small"
        @update:page="sessionPage = $event"
      />
      <EmptyState
        v-else
        title="No connection sessions"
        :description="investigatedAgentPublicId
          ? 'No retained connection sessions match this agent.'
          : 'Agent connection sessions will appear after registered agents connect to management.'"
      />
      <div v-if="investigatedAgentPublicId && !filteredAgentConnections.length" class="agent-filter-empty__action">
        <NButton secondary size="small" @click="clearInvestigatedAgent">Show all sessions</NButton>
      </div>
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
      :content-style="modalScrollableContentStyle()"
      :bordered="false"
      @update:show="handleRotateModalUpdate"
    >
      <div v-if="rotateAgentToConfirm" class="layout-grid space-xl">
        <div class="layout-grid space-sm">
          <p class="copy-sm base-text">Rotate the token for <bdi class="agent-inline-hostile" dir="ltr" :title="diagnosticInspectionText(rotateAgentToConfirm.name)">{{ diagnosticExcerpt(rotateAgentToConfirm.name, 72).text }}</bdi>?</p>
          <p class="copy-sm line-relaxed muted-text">
            The new token will be shown once. The active agent connection will be disconnected immediately. In-flight requests through this agent may fail, and future connections and stats reports must use the new token.
          </p>
        </div>
        <div class="round-md framed frame-standard muted-bg pad-md">
          <span class="margin-bottom-xs flow-box copy-xs weight-medium label-case letter-wide muted-text">Agent ID</span>
          <code class="agent-modal-value flow-box scroll-x mono-text copy-xs base-text"><bdi dir="ltr">{{ diagnosticInspectionText(rotateAgentToConfirm.publicId) }}</bdi></code>
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
      :content-style="modalScrollableContentStyle()"
      :bordered="false"
      @update:show="handleUninstallModalUpdate"
    >
      <div v-if="uninstallAgent" class="layout-grid space-xl">
        <div class="layout-grid space-md mq-md-cols-two">
          <div class="layout-grid space-xs">
            <span class="copy-xs weight-medium label-case letter-wide muted-text">Agent</span>
            <bdi class="agent-modal-value round-md framed frame-standard muted-bg pad-x-md pad-y-sm copy-sm base-text" dir="ltr" :title="diagnosticInspectionText(uninstallAgent.name)">{{ diagnosticExcerpt(uninstallAgent.name, 72).text }}</bdi>
          </div>
          <div class="layout-grid space-xs">
            <span class="copy-xs weight-medium label-case letter-wide muted-text">Agent ID</span>
            <code class="agent-modal-value scroll-x round-md framed frame-standard muted-bg pad-x-md pad-y-sm mono-text copy-xs base-text"><bdi dir="ltr">{{ diagnosticInspectionText(uninstallAgent.publicId) }}</bdi></code>
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
      :content-style="modalScrollableContentStyle()"
      :bordered="false"
      :mask-closable="false"
      :close-on-esc="false"
      @update:show="handleSetupModalUpdate"
    >
      <div v-if="issuedAgent" class="layout-grid space-xl">
        <div class="layout-grid space-md mq-md-cols-two">
          <div class="layout-grid space-xs">
            <span class="copy-xs weight-medium label-case letter-wide muted-text">Agent</span>
            <bdi class="agent-modal-value round-md framed frame-standard muted-bg pad-x-md pad-y-sm copy-sm base-text" dir="ltr" :title="diagnosticInspectionText(issuedAgent.name)">{{ diagnosticExcerpt(issuedAgent.name, 72).text }}</bdi>
          </div>
          <div class="layout-grid space-xs">
            <span class="copy-xs weight-medium label-case letter-wide muted-text">Generated ID</span>
            <code class="agent-modal-value scroll-x round-md framed frame-standard muted-bg pad-x-md pad-y-sm mono-text copy-xs base-text"><bdi dir="ltr">{{ diagnosticInspectionText(issuedAgent.publicId) }}</bdi></code>
          </div>
        </div>

		<NAlert :type="setupHandoffComplete ? 'success' : 'warning'" :show-icon="false">
		  {{ setupHandoffComplete
            ? setupTab === 'install'
			  ? 'Command and required credentials copied. Run the command, then paste each token only into its matching hidden prompt.'
              : 'Setup command copied. Keep it secure; this configuration contains one-time credentials.'
			: setupTab === 'install'
			  ? 'These credentials are shown once. Copy the command and every required token before closing this dialog.'
			  : 'This setup is shown once. Copy it before closing this dialog.' }}
        </NAlert>

		<div class="layout-grid space-xs">
		  <div class="agent-credential-heading layout-row align-center space-sm">
			<span class="copy-xs weight-medium label-case letter-wide muted-text">One-Time Agent Token</span>
			<NButton size="tiny" secondary attr-type="button" @click="copyIssuedCredential(issuedToken, 'agent')">{{ issuedTokenCopyLabel }}</NButton>
		  </div>
		  <code class="flow-box wrap-anywhere round-md framed frame-standard muted-bg pad-md mono-text copy-xs base-text">{{ issuedToken }}</code>
		</div>

        <div v-if="setupIsRotation" class="round-md framed frame-standard muted-bg pad-md copy-xs line-normal base-text">
          <p class="weight-semibold label-case letter-wide">Existing Linux agent</p>
          <p class="margin-top-xs muted-text">
            Run the Linux reinstall command on the existing agent host. It rewrites the agent environment, refreshes embedded management CA material, and restarts p2pstream-agent.
          </p>
        </div>

        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Management URL
          <NInput v-model:value="setupManagementUrl" size="small" required />
        </label>

        <div class="round-md framed frame-standard muted-bg pad-md">
          <div class="layout-row align-center space-sm">
            <NTag size="small" :bordered="false" type="success">Adaptive capacity</NTag>
            <span class="copy-xs weight-semibold base-text">Default for this setup command</span>
          </div>
          <p class="margin-top-xs copy-xs line-normal muted-text">
            No <code>TUNNEL_MAX_CONCURRENT_REQUESTS</code> value is written. The agent uses available memory normally, begins gradual admission control at 80%, and pauses new streams at 90%.
          </p>
          <p v-if="setupIsRotation && setupTab === 'install'" class="margin-top-xs copy-xs line-normal muted-text">
            A Linux reinstall preserves an existing explicit value in <code>/etc/p2pstream/agent.env</code>; remove that line to return an older fixed installation to adaptive mode.
          </p>
        </div>

        <div v-if="issuedUpdaterEnrollmentToken && !setupIsRotation" class="round-md framed frame-standard muted-bg pad-md">
          <div class="layout-row align-center space-sm">
            <NTag size="small" :bordered="false" :type="setupManagedUpdates ? 'success' : 'default'">Managed updates</NTag>
            <span class="copy-xs weight-semibold base-text">Linux/systemd only</span>
          </div>
          <NCheckbox v-model:checked="setupManagedUpdates" class="margin-top-md">
            Enroll this host for route-aware updates
          </NCheckbox>
          <p class="margin-top-xs copy-xs line-normal muted-text">
            Uses a separate single-use updater identity. The tunnel token cannot approve updates, and future releases do not require token rotation.
          </p>
          <p class="margin-top-xs copy-xs line-normal muted-text">
            Release source {{ setupReleaseRepository }} on GitHub · exact versions and SHA-256 digests remain enforced
          </p>
          <p class="margin-top-xs copy-xs line-normal muted-text">
            Management authority epoch {{ issuedUpdaterAuthorityEpoch }} · <code>{{ issuedUpdaterAuthorityKeyId.slice(0, 12) }}…{{ issuedUpdaterAuthorityKeyId.slice(-10) }}</code>
          </p>
		  <div v-if="setupManagedUpdates" class="layout-grid space-xs margin-top-md">
			<div class="agent-credential-heading layout-row align-center space-sm">
			  <span class="copy-xs weight-medium label-case letter-wide muted-text">Updater Enrollment Token</span>
			  <NButton size="tiny" secondary attr-type="button" @click="copyIssuedCredential(issuedUpdaterEnrollmentToken, 'updater')">{{ issuedUpdaterTokenCopyLabel }}</NButton>
			</div>
			<code class="flow-box wrap-anywhere round-md framed frame-standard base-bg pad-md mono-text copy-xs base-text">{{ issuedUpdaterEnrollmentToken }}</code>
		  </div>
        </div>

        <details class="agent-advanced-options" :open="setupAdvancedOpen" @toggle="handleSetupAdvancedToggle">
          <summary>
            <span>Advanced setup options</span>
            <small>Destination policy, repository, release, and TLS</small>
          </summary>
          <div class="agent-advanced-options__body">
            <div class="layout-grid space-md mq-md-cols-two">
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Agent Destination Allowlist
                <NInput
                  v-model:value="setupAgentAllowTargets"
                  size="small"
                  :disabled="setupAgentAllowAnyTarget"
                  placeholder="app.internal:443, 10.0.5.0/24:8080"
                />
                <small class="normal-text line-normal letter-normal">
                  Exact hostnames, IPs, or CIDRs with optional ports. Blank uses loopback-only defaults; a Linux reinstall preserves its existing policy.
                </small>
              </label>
              <div class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Destination Scope
                <NCheckbox v-model:checked="setupAgentAllowAnyTarget">
                  Allow any destination reachable by this agent
                </NCheckbox>
                <small class="normal-text line-normal letter-normal">
                  Use only when unrestricted network reachability is intentional and documented.
                </small>
              </div>
            </div>

            <div v-if="setupAgentAllowAnyTarget" class="warning-panel pad-md copy-xs line-normal">
              Unrestricted mode lets management request connections to every destination the agent host can reach.
            </div>

            <div class="layout-grid space-md mq-md-cols-two">
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                GitHub Repository
                <NInput v-model:value="setupReleaseRepository" size="small" placeholder="Kirari04/p2pstream" required />
              </label>
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Release Version
                <NInput v-model:value="setupReleaseVersion" size="small" placeholder="vX.Y.Z" required />
                <small v-if="setupTab === 'install'" class="normal-text line-normal letter-normal">Linux requires an exact immutable SemVer tag; prereleases use the isolated staging update channel.</small>
              </label>
              <label v-if="setupTab === 'install'" class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Pinned Installer File
                <NInput v-model:value="setupInstallerPath" size="small" placeholder="/path/to/p2pstream-install-agent.sh" required />
              </label>
              <label v-if="setupTab === 'install'" class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Pinned Raw Agent Binary
                <NInput v-model:value="setupAgentBinaryPath" size="small" placeholder="/path/to/p2pstream-agent-vX.Y.Z-linux-amd64" required />
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
          </div>
        </details>

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

        <p v-if="setupTab === 'install'" class="copy-xs line-normal muted-text">
          Linux setup runs only a locally supplied, independently pinned installer and raw binary. Remote scripts are never piped into root, and the displayed token is entered through a hidden prompt instead of shell arguments.
        </p>

        <p v-if="setupSnippetError" class="error-panel pad-md copy-xs line-normal">{{ setupSnippetError }}</p>
        <pre v-else class="max-height-md scroll-any round-md framed frame-standard muted-bg pad-lg copy-xs line-normal base-text"><code>{{ setupSnippet }}</code></pre>

        <p class="visually-hidden" aria-live="polite">
		  {{ setupHandoffComplete ? 'Setup command and required credentials copied.' : '' }}
        </p>
        <div class="layout-row layout-column-reverse space-md mq-sm-row mq-sm-end">
          <NButton secondary attr-type="button" @click="requestClearIssuedToken">Done</NButton>
          <NButton type="primary" attr-type="button" :disabled="Boolean(setupSnippetError)" @click="copySetupSnippet">{{ setupCopyLabel }}</NButton>
        </div>
      </div>
    </NModal>
  </div>
</template>

<style>
.agent-credential-heading {
  justify-content: space-between;
}

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
  width: 100%;
  height: 100%;
  border-radius: inherit;
  background: linear-gradient(90deg, var(--app-accent), var(--app-success));
  transform-origin: left center;
  transition: transform 180ms ease;
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

.agent-table-toolbar {
  display: grid;
  align-items: center;
  gap: 0.75rem;
  border-bottom: 1px solid var(--app-border-subtle);
  padding: 0.75rem 1rem;
  background: var(--app-panel-muted);
}

.agent-search-input,
.agent-state-filter {
  min-width: 0;
}

.agent-filter-summary {
  margin: 0;
}

.agent-filter-empty {
  display: grid;
  justify-items: center;
  gap: 0.5rem;
  padding: 2.5rem 1.25rem;
  text-align: center;
}

.agent-filter-empty p {
  margin: 0;
}

.agent-filter-empty__action {
  display: flex;
  justify-content: center;
  margin-top: -1.75rem;
  padding: 0 1.25rem 2.5rem;
}

.agent-activity-context {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
}

.agent-investigation-filter {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.35rem;
}

.agent-chart-selection {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.35rem;
}

.agent-investigation-filter .n-tag,
.agent-chart-selection .n-tag {
  min-width: 0;
  max-width: 20rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-table-card .n-data-table-th,
.agent-table-card .n-data-table-td {
  padding: 0.7rem 0.75rem;
}

.agent-table-card .n-data-table-tr.agent-session-row--highlighted .n-data-table-td {
  background: color-mix(in srgb, var(--app-accent) 11%, var(--app-panel)) !important;
}

.agent-table-card .n-data-table-tr.agent-session-row--highlighted .n-data-table-td:first-child {
  box-shadow: inset 3px 0 0 var(--app-accent);
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
  gap: 0.3rem;
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
  display: block;
  min-width: 0;
  max-width: 24rem;
  overflow: hidden;
  color: var(--app-text);
  font-weight: 700;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-cell__id {
  min-width: 0;
  overflow: hidden;
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-cell__mobile-build {
  display: none;
}

.agent-exact-details {
  min-width: 0;
  margin-top: 0.1rem;
}

.agent-exact-details summary {
  width: fit-content;
  max-width: 100%;
  cursor: pointer;
  color: var(--app-text-muted);
  font-size: 0.6875rem;
  font-weight: 600;
  line-height: 1.5;
}

.agent-exact-details[open] summary {
  margin-bottom: 0.55rem;
  color: var(--app-text);
}

.agent-exact-details__body {
  display: grid;
  gap: 0.6rem;
  max-width: 100%;
  max-height: 14rem;
  overflow: auto;
  border-left: 2px solid var(--app-border);
  padding: 0.25rem 0 0.25rem 0.65rem;
}

.agent-exact-field {
  display: grid;
  gap: 0.2rem;
  min-width: 0;
}

.agent-exact-field > span {
  color: var(--app-text-muted);
  font-size: 0.65rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.agent-exact-field code {
  display: block;
  max-width: 100%;
  color: var(--app-text);
  font-size: 0.6875rem;
  overflow-wrap: anywhere;
  unicode-bidi: isolate;
}

.agent-exact-labels {
  display: grid;
  gap: 0.2rem;
  min-width: 0;
  margin: 0;
  padding: 0;
  list-style: none;
}

.agent-state-cell {
  display: grid;
  gap: 0.4rem;
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

.agent-state-dot--disabled {
  background: var(--app-text-muted);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--app-text-muted) 14%, transparent);
}

.agent-state-cell__duration {
  min-height: 1.15rem;
  margin: 0;
  overflow-wrap: anywhere;
}

.agent-compact-stack {
  display: grid;
  gap: 0.35rem;
  min-width: 0;
}

.agent-capacity-cell {
  display: grid;
  min-width: 0;
  gap: 0.42rem;
  border-left: 2px solid color-mix(in srgb, var(--app-info) 55%, var(--app-border));
  padding-left: 0.7rem;
}

.agent-capacity-cell__header {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.45rem;
}

.agent-capacity-cell__value {
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 750;
  letter-spacing: -0.015em;
}

.agent-capacity-cell__summary {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.6875rem;
  line-height: 1.4;
}

.agent-capacity-meter {
  position: relative;
  height: 0.3rem;
  overflow: hidden;
  border-radius: 999px;
  background: color-mix(in srgb, var(--app-border) 68%, transparent);
}

.agent-capacity-meter > span {
  position: absolute;
  inset: 0;
  border-radius: inherit;
  background: linear-gradient(90deg, var(--app-info), var(--app-success));
  transform-origin: left center;
  transition: transform 180ms ease-out;
}

.agent-capacity-cell__details {
  display: grid;
  gap: 0.22rem;
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 0.38rem;
}

.agent-capacity-cell--compact {
  gap: 0.2rem;
  margin-top: 0.35rem;
}

.agent-version-cell {
  display: grid;
  min-width: 0;
  gap: 0.4rem;
}

.agent-version-cell__header {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.45rem;
}

.agent-version-cell__value {
  min-width: 0;
  max-width: 100%;
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.75rem;
  font-weight: 700;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-version-cell__detail {
  min-width: 0;
  overflow: hidden;
  margin: 0;
  font-size: 0.6875rem;
  line-height: 1.45;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-version-cell--compact {
  gap: 0;
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
  flex-wrap: nowrap;
  justify-content: flex-end;
  gap: 0.45rem;
}

.agent-session-value {
  max-width: 20rem;
  overflow: hidden;
  margin: 0;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-modal-value {
  min-width: 0;
  max-width: 100%;
  overflow-wrap: anywhere;
  unicode-bidi: isolate;
}

.agent-inline-hostile {
  display: inline-block;
  max-width: min(100%, 24rem);
  overflow: hidden;
  vertical-align: bottom;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-advanced-options {
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
}

.agent-advanced-options summary {
  display: list-item;
  cursor: pointer;
  padding: 0.85rem 1rem;
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 600;
}

.agent-advanced-options summary::marker {
  color: var(--app-text-muted);
}

.agent-advanced-options summary small {
  display: block;
  margin-top: 0.15rem;
  margin-left: 1rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-weight: 400;
}

.agent-advanced-options__body {
  display: grid;
  gap: 1rem;
  border-top: 1px solid var(--app-border-subtle);
  padding: 1rem;
  background: var(--app-panel);
}

.agent-setup-tabs {
  max-width: 100%;
}

.agent-setup-tabs .n-tabs-nav {
  width: min(100%, 32rem);
}

@media (max-width: 639px) {
  .agent-cell__mobile-build {
    display: block;
    margin-top: 0.15rem;
  }
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

  .agent-table-toolbar {
    grid-template-columns: minmax(14rem, 1fr) 12rem auto;
  }

  .agent-filter-summary {
    justify-self: end;
    text-align: right;
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

@media (pointer: coarse) {
  .agent-row-actions .n-button,
  .agent-exact-details summary,
  .agent-advanced-options summary {
    min-height: 2.75rem;
  }
}
</style>
