<script setup lang="ts">
import { useNarrowScreen } from "@/composables/useMediaQuery";
import { computed, h, inject, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { NAlert, NButton, NDataTable, NDrawer, NDrawerContent, NDropdown, NInput, NModal, NTab, NTabs, NTag } from "naive-ui";
import type { DataTableColumns, DropdownDividerOption, DropdownOption } from "naive-ui";
import { Ban as BanIcon } from "@lucide/vue";
import { Check as CheckIcon } from "@lucide/vue";
import { Copy as CopyIcon } from "@lucide/vue";
import { Ellipsis as EllipsisIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { Search as SearchIcon } from "@lucide/vue";
import { CircleX as TimesCircleIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { useManagementClient } from "@/composables/useManagementClient";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import MetricCard from "@/components/ui/MetricCard.vue";
import MetricStrip from "@/components/ui/MetricStrip.vue";
import PageHeader from "@/components/ui/PageHeader.vue";
import SectionCard from "@/components/ui/SectionCard.vue";
import AgentAvailabilityChart from "@/components/AgentAvailabilityChart.vue";
import AgentEditorModal from "@/components/editors/AgentEditorModal.vue";
import AgentSetupModal from "@/components/AgentSetupModal.vue";
import { dashboardKey, isBusyKey, publicProxyConfigKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { AGENT_ID_SYSTEM_LABEL_KEY, userAgentLabelPairs } from "@/lib/agentLabels";
import { agentSetupReleaseVersion, FALLBACK_RELEASE_REPOSITORY, linuxUninstallSnippet } from "@/lib/agentSetupSnippets";
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
import { editorDrawerWidth, modalCardStyle, modalScrollableContentStyle } from "@/lib/naiveUi";
import type {
  Agent,
  AgentConnectionSession,
  AgentUptimeSummary,
  GetAgentAvailabilityResponse,
  ListAgentAvailabilityResponse,
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
const status = computed(() => dashboard?.value?.status ?? null);
const config = computed(() => publicProxyConfig?.value ?? null);
const agents = computed(() => publicProxyConfig?.value?.agents ?? []);
const oneHourWindow = computed(() => dashboard?.value?.windows.find((w) => w.label === "1h"));
const dayWindow = computed(() => dashboard?.value?.windows.find((w) => w.label === "24h"));
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
const versionDriftCount = computed(() => agentsNeedingUpdate.value + agentsWithBuildMismatch.value + agentsWithUnverifiedBuild.value);
const versionDriftDetail = computed(() => {
  if (!totalAgents.value) return "No agents registered";
  const parts = [
    agentsNeedingUpdate.value ? `${agentsNeedingUpdate.value} update${agentsNeedingUpdate.value === 1 ? "" : "s"} available` : "",
    agentsWithBuildMismatch.value ? `${agentsWithBuildMismatch.value} build mismatch${agentsWithBuildMismatch.value === 1 ? "" : "es"}` : "",
    agentsWithUnverifiedBuild.value ? `${agentsWithUnverifiedBuild.value} build unverified` : "",
  ].filter(Boolean);
  return parts.length ? parts.join(" · ") : "All builds match the server";
});
const registeredDetail = computed(() => {
  if (!totalAgents.value) return "None yet";
  return disabledAgents.value ? `${enabledAgents.value} enabled · ${disabledAgents.value} disabled` : `${enabledAgents.value} enabled`;
});
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
  sampledMetric("Memory sys", status.value?.latestAgentStats?.memorySysMb, " MB", "Latest sample"),
  sampledMetric("Goroutines", status.value?.latestAgentStats?.numGoroutine, "", "Latest sample"),
  {
    label: "Active requests",
    value: activeAgentRequests.value.toString(),
    detail: "Across agents",
  },
  sampledMetric("Avg memory", oneHourWindow.value?.agentAvgMemoryMb, " MB", "1h window"),
  sampledMetric("Max memory", dayWindow.value?.agentMaxMemoryMb, " MB", "24h window"),
  sampledMetric("Max goroutines", dayWindow.value?.agentMaxGoroutines, "", "24h window"),
]);

/** Agent process samples are never legitimately zero, so a missing or zero sample reads as a quiet empty. */
function sampledMetric(label: string, value: bigint | undefined, unit: string, detail: string) {
  if (value === undefined || value === 0n) return { label, value: "—", detail: "No samples yet" };
  return { label, value: `${bigIntLabel(value)}${unit}`, detail };
}
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
const fleetAvailability = ref<ListAgentAvailabilityResponse | null>(null);
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
const setupModal = ref<InstanceType<typeof AgentSetupModal> | null>(null);
const uninstallAgent = ref<Agent | null>(null);
const uninstallReleaseRepository = ref(defaultReleaseRepository());
const uninstallCopyLabel = ref("Copy");
let uninstallCopyReset: number | undefined;
/** Details drawer target. Looked up by ID so the drawer follows label edits and closes if the agent is deleted. */
const detailsAgentId = ref("");
const detailsAgent = computed(() => detailsAgentId.value
  ? agents.value.find((agent) => agent.id.toString() === detailsAgentId.value) ?? null
  : null);
const detailsAgentLabels = computed(() => detailsAgent.value ? agentUserLabels(detailsAgent.value) : []);
const detailsRuntime = computed(() => detailsAgent.value ? agentRuntimeDetails(detailsAgent.value) : []);
type DetailsCopyField = "id" | "selector";
const detailsCopy = ref<{ field: DetailsCopyField; state: "copied" | "failed" } | null>(null);
let detailsCopyReset: number | undefined;

/** Sums to the table's scroll width, which must stay inside the card at a 1280px viewport. */
const AGENT_COLUMN_WIDTHS = { agent: 210, status: 160, capacity: 170, version: 140, actions: 256 } as const;
/** Narrow screens stack each agent into one cell instead of scrolling a wide table under a pinned column. */
const compactLayout = useNarrowScreen();

const sessionPagination = computed(() => ({ page: sessionPage.value, pageSize: 12 }));

const busyDisabledReason = computed(() => isBusy?.value ? BUSY_REASON : "");
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

const agentColumns = computed<DataTableColumns<Agent>>(() => compactLayout.value
  ? [
      {
        title: "Agent",
        key: "agent",
        render: (agent) => renderAgentIdentity(agent, true),
      },
    ]
  : [
      {
        title: "Agent",
        key: "agent",
        minWidth: AGENT_COLUMN_WIDTHS.agent,
        render: (agent) => renderAgentIdentity(agent),
      },
      {
        title: "Status",
        key: "health",
        width: AGENT_COLUMN_WIDTHS.status,
        render: (agent) => renderAgentStatus(agent),
      },
      {
        title: "Capacity",
        key: "capacity",
        width: AGENT_COLUMN_WIDTHS.capacity,
        render: (agent) => renderAgentCapacity(agent),
      },
      {
        title: "Version",
        key: "version",
        width: AGENT_COLUMN_WIDTHS.version,
        render: (agent) => renderAgentVersion(agent),
      },
      {
        title: "Actions",
        key: "actions",
        width: AGENT_COLUMN_WIDTHS.actions,
        align: "right",
        fixed: "right",
        render: (agent) => renderAgentActions(agent),
      },
    ]);
const agentTableScrollX = computed(() => compactLayout.value
  ? undefined
  : Object.values(AGENT_COLUMN_WIDTHS).reduce((sum, width) => sum + width, 0));
const sessionColumns = computed<DataTableColumns<AgentConnectionSession>>(() => compactLayout.value
  ? [
      {
        title: "Session",
        key: "agent",
        render: (session) => h("div", { class: "agent-session-cell" }, [
          renderSessionAgent(session),
          h("p", { class: "agent-meta" }, session.active
            ? `Since ${formatDate(session.connectedAtUnixMillis)}`
            : `${formatDate(session.connectedAtUnixMillis)} · ${formatLongDuration(session.durationMillis)}`),
        ]),
      },
      { title: "State", key: "state", width: 88, render: (session) => renderSessionState(session) },
    ]
  : [
      {
        title: "Agent",
        key: "agent",
        minWidth: 200,
        render: (session) => renderSessionAgent(session),
      },
      { title: "Started", key: "started", width: 176, render: (session) => h("span", { class: "agent-table-date" }, formatDate(session.connectedAtUnixMillis)) },
      { title: "Ended", key: "ended", width: 176, render: (session) => h("span", { class: "agent-table-date" }, session.active ? "—" : formatDate(session.disconnectedAtUnixMillis)) },
      { title: "Duration", key: "duration", width: 112, render: (session) => h("span", { class: "agent-table-date" }, formatLongDuration(session.durationMillis)) },
      { title: "State", key: "state", width: 88, render: (session) => renderSessionState(session) },
    ]);
const sessionTableScrollX = computed(() => compactLayout.value ? undefined : 200 + 176 + 176 + 112 + 88);

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

watch(detailsAgent, (agent) => {
  if (!agent && detailsAgentId.value) closeAgentDetails();
});

watch(filteredAgentConnections, (sessions) => {
  const maxPage = Math.max(1, Math.ceil(sessions.length / 12));
  if (sessionPage.value > maxPage) sessionPage.value = maxPage;
});

function bigIntLabel(value: bigint | undefined): string {
  if (value === undefined) return "0";
  return new Intl.NumberFormat().format(Number(value));
}

function formatDate(value: bigint | undefined): string {
  if (value === undefined || value === 0n) return "—";
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
  if (duration === "—") return agentConnected(agent) ? "Connected" : "Offline duration unavailable";
  return agentConnected(agent) ? `${duration} current uptime` : `${duration} offline`;
}

function currentAgentDuration(agent: Agent): string {
  const uptime = uptimeForAgent(agent);
  if (!uptime) return "—";
  return uptime.connected
    ? formatLongDuration(uptime.currentUptimeMillis)
    : formatLongDuration(uptime.currentDowntimeMillis);
}

function agentUptimePercentLabel(agent: Agent): string {
  return formatPercent(uptimeForAgent(agent)?.uptimePercent);
}

function agentConnectionCounts(agent: Agent): string {
  const uptime = uptimeForAgent(agent);
  if (!uptime) return "—";
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

function buildStatusForAgent(agent: Agent) {
  return agentBuildStatus({
    agentVersion: agent.version,
    agentCommit: agent.commit,
    serverVersion: status.value?.version,
    serverCommit: status.value?.commit,
  });
}

function renderAgentIdentity(agent: Agent, compact = false) {
  return h("div", { class: "agent-cell" }, [
    h("bdi", {
      class: "agent-cell__name",
      dir: "ltr",
      title: diagnosticInspectionText(agent.name),
    }, diagnosticExcerpt(agent.name, 64).text),
    h("p", {
      class: "agent-cell__id mono-text",
      title: diagnosticInspectionText(agent.publicId),
    }, [h("bdi", { dir: "ltr" }, diagnosticInspectionText(agent.publicId))]),
    compact
      ? h("div", { class: "agent-cell__compact" }, [
          renderAgentStatus(agent),
          renderAgentCapacity(agent, true),
          renderAgentVersion(agent),
        ])
      : null,
    renderAgentDetails(agent),
    compact ? renderAgentActions(agent) : null,
  ]);
}

function renderAgentDetails(agent: Agent) {
  const labels = agentUserLabels(agent);
  return h("button", {
    type: "button",
    class: "agent-details-trigger",
    "aria-label": agentActionLabel("Show details for", agent),
    "aria-haspopup": "dialog",
    onClick: () => openAgentDetails(agent),
  }, `Details · ${labels.length} label${labels.length === 1 ? "" : "s"}`);
}

function openAgentDetails(agent: Agent) {
  detailsCopy.value = null;
  detailsAgentId.value = agent.id.toString();
}

function closeAgentDetails() {
  detailsAgentId.value = "";
  detailsCopy.value = null;
}

function handleDetailsDrawerUpdate(show: boolean) {
  if (!show) closeAgentDetails();
}

function editAgentFromDetails() {
  if (detailsAgent.value) editAgent(detailsAgent.value);
}

async function copyAgentDetail(field: DetailsCopyField, value: string) {
  try {
    await navigator.clipboard.writeText(value);
    detailsCopy.value = { field, state: "copied" };
  } catch {
    detailsCopy.value = { field, state: "failed" };
  }
  window.clearTimeout(detailsCopyReset);
  detailsCopyReset = window.setTimeout(() => { detailsCopy.value = null; }, 1800);
}

onBeforeUnmount(() => window.clearTimeout(detailsCopyReset));

interface AgentRuntimeDetail {
  label: string;
  value: string;
  tone?: "warning";
}

function agentRuntimeDetails(agent: Agent): AgentRuntimeDetail[] {
  const capacity = summarizeAgentCapacity(agent);
  const commit = shortBuildCommit(agent.commit);
  const connected = agentConnected(agent);
  const details: AgentRuntimeDetail[] = [
    { label: "Uptime", value: agentUptimePercentLabel(agent) },
    { label: "Connect / drop", value: agentConnectionCounts(agent) },
    connected
      ? { label: "Last connected", value: formatDate(agentLastConnected(agent)) }
      : { label: "Last disconnected", value: formatDate(agentLastDisconnected(agent)) },
  ];
  if (capacity.state === "offline") {
    details.push({ label: "Capacity", value: "Negotiated when the agent reconnects" });
  } else if (capacity.state === "unreported") {
    details.push({ label: "Capacity", value: "Update this agent to expose tunnel capacity.", tone: "warning" });
  } else {
    details.push(
      capacity.adaptive
        ? { label: "Current allowance", value: capacity.admissionLimit.toString() }
        : { label: "Advertised", value: capacity.advertised.toString() },
      {
        label: capacity.adaptive
          ? `Memory${capacity.memorySource ? ` · ${diagnosticExcerpt(capacity.memorySource, 24).text}` : ""}`
          : "Process memory",
        value: agentCapacityMemoryLabel(capacity),
      },
    );
    if (capacity.adaptive) {
      details.push(
        { label: "File descriptors", value: agentCapacityFileDescriptorLabel(capacity) },
        { label: "Protocol guard", value: capacity.negotiated.toString() },
      );
    }
    if (capacity.sensorDegraded) {
      details.push({
        label: "Resource sensor",
        value: capacity.lastGoodUnixMillis
          ? `Unavailable · last good ${formatDate(capacity.lastGoodUnixMillis)}`
          : "Unavailable · new streams are paused",
        tone: "warning",
      });
    }
  }
  details.push({ label: "Build", value: commit ? diagnosticInspectionText(commit) : "Not reported" });
  if (status.value?.version) {
    details.push({ label: "Server", value: diagnosticExcerpt(status.value.version, 24).text });
  }
  return details;
}

function renderAgentStatus(agent: Agent) {
  const state = agentOperationalState(agent);
  const uptime = uptimeForAgent(agent);
  return h("div", { class: "agent-status" }, [
    h("p", { class: "agent-status__label" }, [
      h("span", { class: ["agent-state-dot", `agent-state-dot--${state}`], "aria-hidden": "true" }),
      h("span", agentOperationalStateLabel(agent)),
    ]),
    h("p", { class: "agent-meta" }, currentAgentStateSummary(agent)),
    uptime && agentUptimePercentLabel(agent) !== "—"
      ? h("p", { class: "agent-meta" }, `${agentUptimePercentLabel(agent)} uptime`)
      : null,
  ]);
}

function renderAgentVersion(agent: Agent) {
  const build = buildStatusForAgent(agent);
  const version = agent.version ? diagnosticExcerpt(agent.version, 32).text : "Unknown";
  return h("div", { class: "agent-version" }, [
    h("code", {
      class: "agent-version__value mono-text",
      title: agent.version ? diagnosticInspectionText(agent.version) : "Agent version has not been reported",
    }, [h("bdi", { dir: "ltr" }, version)]),
    h(NTag, {
      size: "small",
      bordered: false,
      type: agentBuildStatusType(build.state),
    }, { default: () => build.label }),
  ]);
}

function renderAgentCapacity(agent: Agent, compact = false) {
  const capacity = summarizeAgentCapacity(agent);
  if (capacity.state === "offline") {
    return h("div", { class: "agent-capacity" }, [
      h("p", { class: "agent-capacity__header" }, [
        h("span", { class: "agent-capacity__value muted-text" }, "Tunnel offline"),
      ]),
      compact ? null : h("p", { class: "agent-meta" }, "Capacity unavailable"),
    ]);
  }
  if (capacity.state === "unreported") {
    return h("div", { class: "agent-capacity" }, [
      h("p", { class: "agent-capacity__header" }, [
        h("span", { class: "agent-capacity__value" }, "Not reported"),
        h(NTag, { size: "small", bordered: false, type: "warning" }, { default: () => "Legacy agent" }),
      ]),
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
  const headline = capacity.adaptive ? "Adaptive" : `${capacity.negotiated.toString()} streams`;
  return h("div", { class: "agent-capacity" }, [
    h("p", { class: "agent-capacity__header" }, [
      h("span", { class: "agent-capacity__value" }, headline),
      h(NTag, { size: "small", bordered: false, type: stateType }, { default: () => stateLabel }),
    ]),
    h("p", { class: "agent-meta" }, `${capacity.active.toString()} active · ${capacity.headroom.toString()} available`),
    compact ? null : h("div", {
      class: "agent-capacity__meter",
      role: "progressbar",
      "aria-label": "Tunnel stream utilization",
      "aria-valuemin": 0,
      "aria-valuemax": 100,
      "aria-valuenow": capacity.utilizationPercent,
    }, [h("span", { style: { transform: `scaleX(${capacity.utilizationPercent / 100})` } })]),
  ]);
}

function agentCapacityMemoryLabel(capacity: ReturnType<typeof summarizeAgentCapacity>): string {
  if (capacity.memoryUsageBytes !== undefined && capacity.memoryLimitBytes !== undefined && capacity.memoryLimitBytes > 0n) {
    return `${formatBytes(capacity.memoryUsageBytes)} / ${formatBytes(capacity.memoryLimitBytes)}${capacity.memoryPercent === undefined ? "" : ` · ${capacity.memoryPercent.toFixed(1)}%`}`;
  }
  return capacity.memorySysMb === undefined ? "No sample" : `${bigIntLabel(capacity.memorySysMb)} MB process`;
}

function agentCapacityFileDescriptorLabel(capacity: ReturnType<typeof summarizeAgentCapacity>): string {
  if (capacity.fileDescriptorsUsed !== undefined && capacity.fileDescriptorsLimit !== undefined && capacity.fileDescriptorsLimit > 0n) {
    return `${capacity.fileDescriptorsUsed.toString()} / ${capacity.fileDescriptorsLimit.toString()}${capacity.fileDescriptorsPercent === undefined ? "" : ` · ${capacity.fileDescriptorsPercent.toFixed(1)}%`}`;
  }
  return "No sample";
}

function renderAgentActions(agent: Agent) {
  return h("div", { class: "agent-row-actions" }, [
    h(NButton, {
      secondary: true,
      size: "small",
      "aria-label": agentActionLabel("Investigate", agent),
      onClick: () => void investigateAgent(agent),
    }, { default: () => "Investigate" }),
    h(DisabledHint, { disabled: Boolean(busyDisabledReason.value), reason: busyDisabledReason.value }, {
      default: () => h(NButton, {
        secondary: true,
        size: "small",
        "aria-label": agentActionLabel("Edit", agent),
        disabled: Boolean(busyDisabledReason.value),
        onClick: () => editAgent(agent),
      }, { default: () => "Edit" }),
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
  ]);
}

function renderSessionAgent(session: AgentConnectionSession) {
  return h("div", { class: "agent-session-cell" }, [
    h("p", {
      class: "agent-session-value agent-session-value--name",
      title: diagnosticInspectionText(sessionAgentLabel(session)),
    }, [h("bdi", { dir: "ltr" }, diagnosticExcerpt(sessionAgentLabel(session), 64).text)]),
    sessionAgentDetail(session)
      ? h("p", {
          class: "agent-session-value mono-text agent-meta",
          title: diagnosticInspectionText(sessionAgentDetail(session)),
        }, [h("bdi", { dir: "ltr" }, diagnosticExcerpt(sessionAgentDetail(session), 64).text)])
      : null,
  ]);
}

function renderSessionState(session: AgentConnectionSession) {
  return h(NTag, { size: "small", bordered: false, type: session.active ? "success" : "default" }, { default: () => session.active ? "Active" : "Closed" });
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
  await router.replace(agentPublicId
    ? { path: "/agent/activity", query: { agent: agentPublicId } }
    : { path: "/agent/activity" });
}

/** Loads the single-agent timeline, or every agent's lanes when no agent is selected. Both refresh with the dashboard. */
async function loadAgentAvailability() {
  const requestSequence = ++availabilityRequestSequence;
  const agentPublicId = investigatedAgentPublicId.value;
  const windowLabel = availabilityWindow.value;
  if (activeAgentSection.value !== "activity") {
    availability.value = null;
    fleetAvailability.value = null;
    availabilityError.value = "";
    availabilityLoading.value = false;
    return;
  }
  if (!agentPublicId) {
    await loadFleetAvailability(requestSequence, windowLabel);
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

async function loadFleetAvailability(requestSequence: number, windowLabel: AvailabilityWindow) {
  availability.value = null;
  if (fleetAvailability.value?.windowLabel !== windowLabel) fleetAvailability.value = null;
  availabilityLoading.value = true;
  availabilityError.value = "";
  try {
    const response = await managementClient.listAgentAvailability({ windowLabel });
    if (requestSequence !== availabilityRequestSequence) return;
    fleetAvailability.value = response;
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
      key: "reinstall",
      label: "Reinstall / repair",
      icon: () => h(RefreshIcon, { class: "icon-sm" }),
      disabled: busy,
      props: { "aria-label": agentActionLabel("Reinstall or repair", agent) },
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
    case "reinstall":
      if (!busyDisabledReason.value) void setupModal.value?.openExisting(agent, "reinstall");
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
    setupModal.value?.openRotated(resp.agent ?? agent, resp.token);
  });
  if (ok) {
    closeRotateAgentModal();
  }
}

async function deleteAgent(agent: Agent) {
  const name = diagnosticInspectionText(agent.name);
  const publicID = diagnosticInspectionText(agent.publicId);
  if (!await lifecycleDialog.confirm(
    "Delete agent",
    `Agent \u2068${name}\u2069 (\u2068${publicID}\u2069) and its agent-selected target matches will be permanently removed.`,
  )) return;
  await run(async () => {
    await managementClient.deleteAgent({ id: agent.id });
  });
}

function defaultReleaseRepository(): string {
  return import.meta.env.VITE_RELEASE_REPOSITORY?.trim() || FALLBACK_RELEASE_REPOSITORY;
}

function buildUninstallSnippet(): string {
  return linuxUninstallSnippet({ repository: uninstallReleaseRepository.value, version: agentSetupReleaseVersion(status.value?.version, import.meta.env.VITE_RELEASE_REF) });
}

function handleAgentCreated(payload: CreatedAgentSetup) { setupModal.value?.openCreated(payload); }

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
  <div v-if="dashboard && status" class="agent-page">
    <PageHeader title="Agents" :description="activeAgentSectionMeta.description">
      <template #badge>
        <NTag size="small" :bordered="false" :type="fleetStatusType">{{ fleetStatusLabel }}</NTag>
      </template>
      <template #actions>
        <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
          <NButton type="primary" size="small" :disabled="Boolean(busyDisabledReason)" @click="openAddAgentModal">
            <template #icon><PlusIcon class="icon-sm" /></template>
            Add agent
          </NButton>
        </DisabledHint>
      </template>
    </PageHeader>

    <NTabs
      class="agent-section-tabs"
      type="line"
      size="small"
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

    <MetricStrip v-if="activeAgentSection === 'fleet'" label="Fleet summary" class="agent-fleet-strip">
      <MetricCard
        label="Fleet connection"
        :value="enabledAgents ? `${connectedAgentCount}/${enabledAgents}` : '—'"
        :tone="fleetStatusType === 'default' ? 'muted' : fleetStatusType"
        :dot="Boolean(enabledAgents)"
        :detail="fleetSummary"
      />
      <MetricCard label="Registered" :value="String(totalAgents)" :detail="registeredDetail" />
      <MetricCard
        label="Fleet uptime"
        :value="formatPercent(fleetUptime)"
        :detail="fleetUptime === null ? 'No uptime history yet' : `${retentionDaysLabel} retention`"
      />
      <MetricCard
        label="Longest live session"
        :value="formatLongDuration(longestCurrentUptimeMillis)"
        :detail="longestCurrentUptimeMillis > 0 ? 'Current uptime' : 'No live session'"
      />
      <MetricCard
        label="Recent disconnects"
        :value="String(recentDisconnects)"
        :tone="recentDisconnects ? 'warning' : 'default'"
        detail="Last 24 hours"
      />
      <MetricCard
        label="Version drift"
        :value="totalAgents ? String(versionDriftCount) : '—'"
        :tone="versionDriftCount ? 'warning' : 'default'"
        :detail="versionDriftDetail"
      />
    </MetricStrip>

    <AgentAvailabilityChart
      v-if="activeAgentSection === 'activity'"
      :availability="availability"
      :fleet-availability="fleetAvailability"
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

    <SectionCard
      v-if="activeAgentSection === 'activity'"
      title="Runtime pressure"
      description="Latest agent process stats plus rolling dashboard windows."
    >
      <template #badge>
        <NTag size="small" :bordered="false">{{ activeAgentRequests }} active requests</NTag>
      </template>
      <MetricStrip label="Runtime pressure">
        <MetricCard
          v-for="metric in runtimeMetrics"
          :key="metric.label"
          :label="metric.label"
          :value="metric.value"
          :tone="metric.value === '—' ? 'muted' : 'default'"
          :detail="metric.detail"
        />
      </MetricStrip>
    </SectionCard>

    <SectionCard
      v-if="activeAgentSection === 'fleet'"
      title="Registered agents"
      description="Offline enabled agents first. Open an agent's details for its exact selector, labels and runtime data."
      flush
      class="agent-table-card"
    >
      <template #badge>
        <NTag size="small" :bordered="false">{{ totalAgents }} total</NTag>
      </template>
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
        <p class="agent-filter-summary" aria-live="polite">{{ filteredAgentSummary }}</p>
      </div>
      <NDataTable
        v-if="filteredAgents.length"
        :columns="agentColumns"
        :data="filteredAgents"
        :row-key="agentRowKey"
        :row-props="agentRowProps"
        :pagination="false"
        :bordered="false"
       
        :scroll-x="agentTableScrollX"
        size="small"
      />
      <div v-else-if="agents.length" class="agent-empty">
        <p class="agent-empty__title">No agents match these filters</p>
        <p class="agent-empty__description">Try another name, ID, label, or connection state.</p>
        <NButton secondary size="small" @click="clearAgentFilters">Clear filters</NButton>
      </div>
      <EmptyState
        v-else
        title="No agents registered"
        description="Agents forward traffic to services behind NAT or firewalls by connecting outbound to this proxy."
        action-label="Add agent"
        @action="openAddAgentModal"
      />
    </SectionCard>

    <SectionCard
      v-if="activeAgentSection === 'activity'"
      title="Recent connection sessions"
      :description="`Connection lifetime history retained for ${retentionDaysLabel}.`"
      flush
      class="agent-table-card"
    >
      <template #badge>
        <NTag size="small" :bordered="false">{{ filteredAgentConnections.length }} sessions</NTag>
      </template>
      <template v-if="investigatedAgentPublicId || inspectedSegmentSummary" #actions>
        <div v-if="investigatedAgentPublicId" class="agent-filter-chip">
          <NTag size="small" :bordered="false" type="warning">
            Investigating <bdi dir="ltr">{{ diagnosticExcerpt(investigatedAgent?.name || investigatedAgentPublicId, 64).text }}</bdi>
          </NTag>
          <NButton
            secondary
            size="tiny"
            :aria-label="`Clear investigation for agent ⁨${diagnosticExcerpt(investigatedAgentPublicId, 64).text}⁩`"
            @click="clearInvestigatedAgent"
          >
            Clear
          </NButton>
        </div>
        <div v-if="inspectedSegmentSummary" class="agent-filter-chip">
          <NTag size="small" :bordered="false" type="info">{{ inspectedSegmentSummary }}</NTag>
          <NButton text size="tiny" attr-type="button" @click="clearInspectedAvailabilitySegment">Clear selection</NButton>
        </div>
      </template>
      <NDataTable
        v-if="filteredAgentConnections.length"
        :columns="sessionColumns"
        :data="filteredAgentConnections"
        :row-key="sessionRowKey"
        :row-props="sessionRowProps"
        :pagination="sessionPagination"
        :bordered="false"
       
        :scroll-x="sessionTableScrollX"
        size="small"
        @update:page="sessionPage = $event"
      />
      <div v-else-if="investigatedAgentPublicId" class="agent-empty">
        <p class="agent-empty__title">No connection sessions</p>
        <p class="agent-empty__description">No retained connection sessions match this agent.</p>
        <NButton secondary size="small" @click="clearInvestigatedAgent">Show all sessions</NButton>
      </div>
      <EmptyState
        v-else
        title="No connection sessions"
        description="Agent connection sessions will appear after registered agents connect to management."
      />
    </SectionCard>

    <NDrawer
      :show="Boolean(detailsAgent)"
      placement="right"
      :width="editorDrawerWidth('34rem')"
      aria-label="Agent details"
      class="editor-drawer agent-details-drawer"
      @update:show="handleDetailsDrawerUpdate"
    >
      <NDrawerContent closable>
        <template #header>
          <div class="agent-details-drawer__header">
            <span>Agent details</span>
            <bdi
              v-if="detailsAgent"
              dir="ltr"
              class="agent-details-drawer__subtitle"
              :title="diagnosticInspectionText(detailsAgent.name)"
            >{{ diagnosticExcerpt(detailsAgent.name, 72).text }}</bdi>
          </div>
        </template>
        <div v-if="detailsAgent" class="agent-details">
          <section class="agent-details__group" aria-labelledby="agent-details-identity">
            <h3 id="agent-details-identity" class="agent-details__heading">Identity</h3>
            <dl class="agent-details__fields">
              <div class="agent-details__field">
                <dt>Agent ID</dt>
                <dd class="agent-details__copy-row">
                  <code class="agent-details__value mono-text"><bdi dir="ltr">{{ diagnosticInspectionText(detailsAgent.publicId) }}</bdi></code>
                  <NButton
                    text
                    size="tiny"
                    aria-label="Copy agent ID"
                    title="Copy agent ID"
                    @click="copyAgentDetail('id', detailsAgent.publicId)"
                  >
                    <template #icon><CopyIcon class="icon-sm" /></template>
                  </NButton>
                </dd>
                <dd v-if="detailsCopy?.field === 'id'" :class="['agent-details__copy-status', detailsCopy.state === 'copied' ? 'success-text' : 'warning-text']" role="status">
                  {{ detailsCopy.state === 'copied' ? 'Copied' : 'Copy failed; select the text instead' }}
                </dd>
              </div>
              <div class="agent-details__field">
                <dt>Exact selector</dt>
                <dd class="agent-details__copy-row">
                  <code class="agent-details__value mono-text"><bdi dir="ltr">{{ diagnosticInspectionText(AGENT_ID_SYSTEM_LABEL_KEY) }}</bdi>=<bdi dir="ltr">{{ diagnosticInspectionText(exactAgentSelectorValue(detailsAgent)) }}</bdi></code>
                  <NButton
                    text
                    size="tiny"
                    aria-label="Copy exact selector"
                    title="Copy exact selector"
                    @click="copyAgentDetail('selector', exactAgentSelector(detailsAgent))"
                  >
                    <template #icon><CopyIcon class="icon-sm" /></template>
                  </NButton>
                </dd>
                <dd v-if="detailsCopy?.field === 'selector'" :class="['agent-details__copy-status', detailsCopy.state === 'copied' ? 'success-text' : 'warning-text']" role="status">
                  {{ detailsCopy.state === 'copied' ? 'Copied' : 'Copy failed; select the text instead' }}
                </dd>
              </div>
            </dl>
          </section>

          <section class="agent-details__group editor-group" aria-labelledby="agent-details-labels">
            <h3 id="agent-details-labels" class="agent-details__heading">Labels</h3>
            <ul v-if="detailsAgentLabels.length" class="agent-details__labels">
              <li v-for="label in detailsAgentLabels" :key="label.id">
                <NTag size="small" :bordered="false" class="agent-details__label mono-text">
                  <bdi dir="ltr">{{ diagnosticInspectionText(label.key) }}</bdi>=<bdi dir="ltr">{{ diagnosticInspectionText(label.value) }}</bdi>
                </NTag>
              </li>
            </ul>
            <p v-else class="agent-details__empty">No user labels</p>
          </section>

          <section class="agent-details__group editor-group" aria-labelledby="agent-details-runtime">
            <h3 id="agent-details-runtime" class="agent-details__heading">Runtime</h3>
            <dl class="agent-details__runtime">
              <div v-for="item in detailsRuntime" :key="item.label" class="agent-details__runtime-item">
                <dt>{{ item.label }}</dt>
                <dd :class="item.tone ? `${item.tone}-text` : undefined"><bdi dir="ltr">{{ item.value }}</bdi></dd>
              </div>
            </dl>
          </section>

          <div class="editor-drawer-actions agent-details__actions">
            <NButton secondary attr-type="button" @click="closeAgentDetails">Close</NButton>
            <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
              <NButton type="primary" attr-type="button" :disabled="Boolean(busyDisabledReason)" @click="editAgentFromDetails">Edit agent</NButton>
            </DisabledHint>
          </div>
        </div>
      </NDrawerContent>
    </NDrawer>

    <AgentEditorModal
      ref="agentEditor"
      :config="config"
      allow-create
      @created-agent="handleAgentCreated"
    />

    <NModal
      :show="Boolean(rotateAgentToConfirm)"
      preset="card"
      title="Rotate agent token"
      :style="modalCardStyle('34rem')"
      :content-style="modalScrollableContentStyle()"
      :bordered="false"
      @update:show="handleRotateModalUpdate"
    >
      <div v-if="rotateAgentToConfirm" class="agent-modal">
        <div class="agent-modal__copy">
          <p class="agent-modal__lead">Rotate the token for <bdi class="agent-inline-hostile" dir="ltr" :title="diagnosticInspectionText(rotateAgentToConfirm.name)">{{ diagnosticExcerpt(rotateAgentToConfirm.name, 72).text }}</bdi>?</p>
          <p class="agent-modal__muted">
            The new token will be shown once. The active agent connection will be disconnected immediately. In-flight requests through this agent may fail, and future connections and stats reports must use the new token.
          </p>
        </div>
        <div class="agent-modal__field">
          <span class="agent-modal__label">Agent ID</span>
          <code class="agent-modal-value agent-modal__inset mono-text"><bdi dir="ltr">{{ diagnosticInspectionText(rotateAgentToConfirm.publicId) }}</bdi></code>
        </div>
        <div class="agent-modal__actions">
          <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
            <NButton secondary attr-type="button" :disabled="Boolean(busyDisabledReason)" @click="closeRotateAgentModal">Cancel</NButton>
          </DisabledHint>
          <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
            <NButton type="error" attr-type="button" :disabled="Boolean(busyDisabledReason)" @click="confirmRotateAgentToken">Rotate token</NButton>
          </DisabledHint>
        </div>
      </div>
    </NModal>

    <NModal
      :show="Boolean(uninstallAgent)"
      preset="card"
      title="Agent uninstall"
      :style="modalCardStyle('46rem')"
      :content-style="modalScrollableContentStyle()"
      :bordered="false"
      @update:show="handleUninstallModalUpdate"
    >
      <div v-if="uninstallAgent" class="agent-modal">
        <dl class="agent-modal__identity">
          <div class="agent-modal__field">
            <dt class="agent-modal__label">Agent</dt>
            <dd><bdi class="agent-modal-value" dir="ltr" :title="diagnosticInspectionText(uninstallAgent.name)">{{ diagnosticExcerpt(uninstallAgent.name, 72).text }}</bdi></dd>
          </div>
          <div class="agent-modal__field">
            <dt class="agent-modal__label">Agent ID</dt>
            <dd><code class="agent-modal-value mono-text"><bdi dir="ltr">{{ diagnosticInspectionText(uninstallAgent.publicId) }}</bdi></code></dd>
          </div>
        </dl>

        <NAlert type="warning" :bordered="false" title="Remote host full purge">
          <p>
            Run this command on the Linux host where the shell installer was used. It stops and removes the systemd service, deletes the config directory and binary, and removes the p2pstream service user and group.
          </p>
          <p class="agent-modal__alert-followup">
            This does not delete the management agent record. Delete or disable the agent here after the host is removed.
          </p>
        </NAlert>

        <label class="agent-modal__field">
          <span class="agent-modal__label">GitHub repository</span>
          <NInput v-model:value="uninstallReleaseRepository" size="small" placeholder="Kirari04/p2pstream" required />
        </label>

        <NAlert v-if="uninstallSnippetError" type="error" :bordered="false">{{ uninstallSnippetError }}</NAlert>
        <pre v-else class="command-block"><code>{{ uninstallSnippet }}</code></pre>

        <div class="agent-modal__actions">
          <NButton secondary attr-type="button" :disabled="Boolean(uninstallSnippetError)" @click="copyUninstallSnippet">{{ uninstallCopyLabel }}</NButton>
          <NButton type="primary" attr-type="button" @click="closeUninstallModal">Done</NButton>
        </div>
      </div>
    </NModal>

    <AgentSetupModal ref="setupModal" />
  </div>
</template>

<style scoped>
.agent-page {
  display: grid;
  min-width: 0;
  gap: 1.25rem;
}

.agent-section-tabs {
  min-width: 0;
  margin-top: -0.25rem;
}

.agent-section-tabs :deep(.n-tabs-nav) {
  margin-bottom: 0;
}

.agent-fleet-strip :deep(.metric-card__detail) {
  white-space: normal;
}

/* Registered agents and sessions tables */
.agent-table-toolbar {
  display: grid;
  align-items: center;
  gap: 0.75rem;
  border-bottom: 1px solid var(--app-border-subtle);
  padding: 0.75rem 1.25rem;
}

.agent-search-input,
.agent-state-filter {
  min-width: 0;
}

.agent-filter-summary {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.agent-empty {
  display: grid;
  justify-items: center;
  gap: 0.5rem;
  padding: 2.5rem 1.25rem;
  text-align: center;
}

.agent-empty__title {
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
}

.agent-empty__description {
  margin-bottom: 0.25rem;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
}

.agent-filter-chip {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.375rem;
}

.agent-filter-chip :deep(.n-tag) {
  min-width: 0;
  max-width: 20rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-table-card :deep(.n-data-table-th),
.agent-table-card :deep(.n-data-table-td) {
  padding: 0.75rem;
  vertical-align: top;
}

.agent-table-card :deep(.n-data-table-th:first-child),
.agent-table-card :deep(.n-data-table-td:first-child) {
  padding-left: 1.25rem;
}

.agent-table-card :deep(.n-data-table-th:last-child),
.agent-table-card :deep(.n-data-table-td:last-child) {
  padding-right: 1.25rem;
}

.agent-table-card :deep(.n-data-table-tr.agent-session-row--highlighted .n-data-table-td) {
  background: color-mix(in srgb, var(--app-accent) 10%, var(--app-panel)) !important;
}

.agent-table-card :deep(.n-data-table-tr.agent-session-row--highlighted .n-data-table-td:first-child) {
  box-shadow: inset 2px 0 0 var(--app-accent);
}

.agent-table-card :deep(.agent-meta) {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
  line-height: 1.45;
  overflow-wrap: anywhere;
}

.agent-table-card :deep(.agent-table-date) {
  color: var(--app-text);
  font-size: 0.8125rem;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

/* Agent identity cell */
.agent-table-card :deep(.agent-cell) {
  display: grid;
  min-width: 0;
  gap: 0.125rem;
}

.agent-table-card :deep(.agent-cell__name) {
  display: block;
  min-width: 0;
  overflow: hidden;
  color: var(--app-text);
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-table-card :deep(.agent-cell__id) {
  min-width: 0;
  overflow: hidden;
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-table-card :deep(.agent-cell__compact) {
  display: grid;
  gap: 0.5rem;
  margin-top: 0.5rem;
}

.agent-table-card :deep(.agent-details-trigger) {
  width: fit-content;
  max-width: 100%;
  margin: 0.125rem 0 0 -0.25rem;
  border: 0;
  border-radius: 4px;
  background: transparent;
  padding: 0.125rem 0.25rem;
  color: var(--app-text-muted);
  cursor: pointer;
  font: inherit;
  font-size: 0.75rem;
  line-height: 1.5;
  text-align: left;
}

.agent-table-card :deep(.agent-details-trigger:hover) {
  background: var(--app-hover);
  color: var(--app-text);
}

.agent-table-card :deep(.agent-details-trigger:focus-visible) {
  outline: 2px solid var(--app-focus);
  outline-offset: 1px;
}

/* Status, capacity and version cells */
.agent-table-card :deep(.agent-status),
.agent-table-card :deep(.agent-capacity),
.agent-table-card :deep(.agent-version) {
  display: grid;
  min-width: 0;
  align-content: start;
  gap: 0.25rem;
}

.agent-table-card :deep(.agent-status__label),
.agent-table-card :deep(.agent-capacity__header) {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.375rem 0.5rem;
  margin: 0;
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 500;
}

.agent-table-card :deep(.agent-status__label) {
  flex-wrap: nowrap;
  align-items: flex-start;
}

.agent-table-card :deep(.agent-state-dot) {
  width: 0.5rem;
  height: 0.5rem;
  flex: 0 0 auto;
  margin-top: 0.375rem;
  border-radius: 50%;
  background: var(--app-text-muted);
}

.agent-table-card :deep(.agent-state-dot--connected) {
  background: var(--app-success);
}

.agent-table-card :deep(.agent-state-dot--offline) {
  background: var(--app-warning);
}

.agent-table-card :deep(.agent-capacity__value) {
  font-variant-numeric: tabular-nums;
}

.agent-table-card :deep(.agent-capacity__meter) {
  position: relative;
  height: 3px;
  overflow: hidden;
  margin-top: 0.125rem;
  border-radius: 999px;
  background: var(--app-border-subtle);
}

.agent-table-card :deep(.agent-capacity__meter > span) {
  position: absolute;
  inset: 0;
  border-radius: inherit;
  background: var(--app-accent);
  transform-origin: left center;
  transition: transform 180ms ease-out;
}

.agent-table-card :deep(.agent-version) {
  justify-items: start;
}

.agent-table-card :deep(.agent-version__value) {
  min-width: 0;
  max-width: 100%;
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.75rem;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-table-card :deep(.agent-row-actions) {
  display: flex;
  flex-wrap: nowrap;
  justify-content: flex-end;
  gap: 0.375rem;
}

.agent-table-card :deep(.agent-cell .agent-row-actions) {
  justify-content: flex-start;
  margin-top: 0.625rem;
}

/* Sessions */
.agent-table-card :deep(.agent-session-cell) {
  display: grid;
  min-width: 0;
  gap: 0.125rem;
}

.agent-table-card :deep(.agent-session-value) {
  max-width: 20rem;
  overflow: hidden;
  margin: 0;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-table-card :deep(.agent-session-value--name) {
  color: var(--app-text);
  font-weight: 500;
}

/* Modals (teleported, so these classes are used directly in this template) */
.agent-modal {
  display: grid;
  gap: 1.25rem;
}

.agent-modal__copy {
  display: grid;
  gap: 0.5rem;
}

.agent-modal__lead {
  color: var(--app-text);
  font-size: 0.875rem;
}

.agent-modal__muted {
  color: var(--app-text-muted);
  font-size: 0.875rem;
  line-height: 1.6;
}

.agent-modal__identity {
  display: grid;
  gap: 1rem;
  margin: 0;
}

.agent-modal__identity dd {
  margin: 0;
}

.agent-modal__field {
  display: grid;
  min-width: 0;
  gap: 0.375rem;
}

.agent-modal__label {
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  font-weight: 500;
}

.agent-modal__inset {
  display: block;
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 0.5rem 0.75rem;
}

.agent-modal-value {
  display: block;
  min-width: 0;
  max-width: 100%;
  color: var(--app-text);
  font-size: 0.8125rem;
  overflow-wrap: anywhere;
  unicode-bidi: isolate;
}

code.agent-modal-value {
  font-size: 0.75rem;
}

.agent-modal__alert-followup {
  margin-top: 0.5rem;
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


/* Agent details drawer (teleported; slot content keeps this component's scope) */
.agent-details-drawer__header {
  display: grid;
  min-width: 0;
  gap: 0.125rem;
}

.agent-details-drawer__subtitle {
  display: block;
  min-width: 0;
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  font-weight: 400;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-details {
  display: grid;
  min-width: 0;
  gap: 1.25rem;
}

.agent-details__group {
  display: grid;
  min-width: 0;
  gap: 0.75rem;
}

.agent-details__heading {
  margin: 0;
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
}

.agent-details__fields {
  display: grid;
  gap: 0.875rem;
  margin: 0;
}

.agent-details__field {
  display: grid;
  min-width: 0;
  gap: 0.25rem;
}

.agent-details__field dt,
.agent-details__runtime dt {
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.agent-details__field dd {
  margin: 0;
}

.agent-details__copy-row {
  display: flex;
  min-width: 0;
  align-items: flex-start;
  gap: 0.5rem;
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 0.5rem 0.625rem;
}

.agent-details__copy-row .n-button {
  flex: 0 0 auto;
  margin-top: 0.0625rem;
}

.agent-details__value {
  min-width: 0;
  flex: 1 1 auto;
  color: var(--app-text);
  font-size: 0.8125rem;
  line-height: 1.5;
  /* IDs and selectors break at punctuation first; mid-token only when nothing else fits. */
  overflow-wrap: anywhere;
  word-break: normal;
  unicode-bidi: isolate;
}

.agent-details__copy-status {
  font-size: 0.75rem;
}

.agent-details__labels {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  gap: 0.375rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.agent-details__labels li {
  min-width: 0;
  max-width: 100%;
}

.agent-details__label {
  max-width: 100%;
  font-size: 0.75rem;
}

.agent-details__label :deep(.n-tag__content) {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-details__empty {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
}

.agent-details__runtime {
  display: grid;
  margin: 0;
}

.agent-details__runtime-item {
  display: grid;
  grid-template-columns: minmax(8rem, 2fr) minmax(0, 3fr);
  align-items: baseline;
  gap: 0.75rem;
  border-bottom: 1px solid var(--app-border-subtle);
  padding: 0.4375rem 0;
}

.agent-details__runtime-item:last-child {
  border-bottom: 0;
}

.agent-details__runtime dd {
  margin: 0;
  color: var(--app-text);
  font-size: 0.8125rem;
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
}

.agent-details__runtime dd.warning-text {
  color: var(--app-warning);
}

.agent-details__actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 0.5rem;
}

.agent-modal__actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 0.5rem;
}

@media (min-width: 640px) {
  .agent-table-toolbar {
    grid-template-columns: minmax(14rem, 1fr) 13rem auto;
  }

  .agent-filter-summary {
    justify-self: end;
    text-align: right;
  }

  .agent-modal__identity {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 639px) {
  .agent-table-toolbar {
    padding-inline: 1rem;
  }

  .agent-table-card :deep(.n-data-table-th:first-child),
  .agent-table-card :deep(.n-data-table-td:first-child) {
    padding-left: 1rem;
  }

  .agent-table-card :deep(.n-data-table-th:last-child),
  .agent-table-card :deep(.n-data-table-td:last-child) {
    padding-right: 1rem;
  }
}

@media (pointer: coarse) {
  .agent-table-card :deep(.agent-row-actions .n-button),
  .agent-table-card :deep(.agent-details-trigger) {
    min-height: 2.75rem;
  }
}

@media (prefers-reduced-motion: reduce) {
  .agent-table-card :deep(.agent-capacity__meter > span) {
    transition: none;
  }
}
</style>
