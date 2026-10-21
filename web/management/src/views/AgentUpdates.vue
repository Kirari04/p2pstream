<script setup lang="ts">
import { useNarrowScreen } from "@/composables/useMediaQuery";
import { computed, h, inject, onMounted, onUnmounted, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { NAlert, NButton, NCheckbox, NDataTable, NInput, NInputNumber, NModal, NSpin, NTab, NTabs, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import {
  CheckCircle2 as CheckIcon,
  CirclePause as PauseIcon,
  CirclePlay as PlayIcon,
  RefreshCw as RefreshIcon,
  RotateCcw as RetryIcon,
  TriangleAlert as WarningIcon,
} from "@lucide/vue";
import MetricCard from "@/components/ui/MetricCard.vue";
import MetricStrip from "@/components/ui/MetricStrip.vue";
import PageHeader from "@/components/ui/PageHeader.vue";
import SectionCard from "@/components/ui/SectionCard.vue";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";
import { dashboardKey, isBusyKey, runManagementActionKey, selectedEnvironmentIdKey, selectedEnvironmentBlockedKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import { messageFromError } from "@/lib/errors";
import { agentIsAlreadyOnUpdateTarget, agentRolloutStatus, agentUpdatePreviewBlockerLabel, canResumeUpdateCampaign, recoverableUpdateAssignments, retryableStageAssignments, updateRecoveryMessage } from "@/lib/agentUpdateRecovery";
import { createAgentUpdatePolling } from "@/lib/agentUpdatePolling";
import { modalCardStyle, modalScrollableContentStyle } from "@/lib/naiveUi";
import AgentSetupModal from "@/components/AgentSetupModal.vue";
import {
  AgentUpdateAssignmentState,
  AgentUpdateCampaignState,
  AgentUpdateDesiredAction,
  type AgentUpdateAssignment,
  type AgentUpdateCampaign,
  type AgentUpdateOverviewAgent,
  type AgentUpdatePreviewAgent,
  type GetAgentUpdateOverviewResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();
const router = useRouter();
const dashboard = inject(dashboardKey, computed(() => null));
const isBusy = inject(isBusyKey, computed(() => false));
const runManagementAction = inject(runManagementActionKey);
const selectedEnvironmentId = inject(selectedEnvironmentIdKey, computed(() => "0"));
const selectedEnvironmentBlocked = inject(selectedEnvironmentBlockedKey, computed(() => ""));

const sections = [
  { key: "fleet", label: "Fleet", path: "/agent" },
  { key: "activity", label: "Activity", path: "/agent/activity" },
  { key: "updates", label: "Updates", path: "/agent/updates" },
] as const;

const overview = ref<GetAgentUpdateOverviewResponse | null>(null);
const campaigns = ref<AgentUpdateCampaign[]>([]);
const loading = ref(false);
const operationError = ref("");
const campaignErrors = ref<Record<string, string>>({});
const planOpen = ref(false);
const planError = ref("");
const planErrorTitle = ref("");
const planCreating = ref(false);
const planName = ref("");
const selectedAgentIds = ref<string[]>([]);
const maxUnavailable = ref(1);
const minimumEligiblePerRoute = ref(1);
const canaryCount = ref(1);
const waveSize = ref(1);
const healthyDwellSeconds = ref(120);
const preview = ref<AgentUpdatePreviewAgent[]>([]);
const previewFingerprint = ref("");
const previewLoading = ref(false);
const planBusy = computed(() => previewLoading.value || planCreating.value || isBusy.value);
const campaignBusy = computed(() => isBusy.value || loading.value);
const setupModal = ref<InstanceType<typeof AgentSetupModal> | null>(null);

const agents = computed(() => overview.value?.agents ?? []);
const updaterFreshnessWindowMs = 2 * 60 * 1000;
const trustedTarget = computed(() => overview.value?.trustedTargets[0] ?? null);
const managementAuthority = computed(() => overview.value?.managementAuthority ?? null);
const managementAuthorityWarning = computed(() => overview.value?.managementAuthorityWarning?.trim() ?? "");
const serverVersion = computed(() => dashboard?.value?.status?.version || "unreported");
const unsupportedAgents = computed(() => agents.value.filter((agent) => !agent.updaterEnrolled).length);
const enrolledAgents = computed(() => agents.value.filter((agent) => agent.updaterEnrolled));
const onlineManagedAgents = computed(() => enrolledAgents.value.filter((agent) => agent.connected && updaterIsFresh(agent)).length);
const activeCampaigns = computed(() => campaigns.value.filter((campaign) => campaign.state === AgentUpdateCampaignState.RUNNING || campaign.state === AgentUpdateCampaignState.PAUSED));
const readinessPercent = computed(() => agents.value.length ? Math.round((enrolledAgents.value.length / agents.value.length) * 100) : 0);
const selectedTargetDigest = computed(() => trustedTarget.value?.manifestSha256 ?? "");
const previewBlocked = computed(() => preview.value.filter((agent) => !agent.eligible));
const currentPlanFingerprint = computed(() => [
  selectedTargetDigest.value,
  planName.value.trim(),
  [...selectedAgentIds.value].sort().join(","),
  maxUnavailable.value,
  minimumEligiblePerRoute.value,
  canaryCount.value,
  waveSize.value,
  healthyDwellSeconds.value,
  selectedAgentIds.value.map((id) => {
    const agent = agents.value.find((item) => item.agentId.toString() === id);
    return agent ? [id, agent.updaterEnrolled, agent.connected, agent.activeAssignmentId, agent.updaterVersion, updaterIsFresh(agent), agent.tunnelVersion, agent.tunnelCommit].join(":") : `${id}:missing`;
  }).sort().join(","),
].join("\u0000"));
const previewIsCurrent = computed(() => Boolean(previewFingerprint.value) && previewFingerprint.value === currentPlanFingerprint.value);
const canCreate = computed(() => Boolean(
  !operationError.value && trustedTarget.value && managementAuthority.value && planName.value.trim() && selectedAgentIds.value.length > 0 &&
  preview.value.length === selectedAgentIds.value.length && previewBlocked.value.length === 0 && previewIsCurrent.value,
));

async function selectSection(value: string | number) {
  const section = sections.find((candidate) => candidate.key === value);
  if (section && section.key !== "updates") await router.push(section.path);
}

const polling = createAgentUpdatePolling({
  scope: () => `${selectedEnvironmentId.value}\u0000${selectedEnvironmentBlocked.value}`,
  canPoll: () => !planBusy.value && !selectedEnvironmentBlocked.value && document.visibilityState !== "hidden",
  load: async (signal) => {
    if (selectedEnvironmentBlocked.value) throw new Error(selectedEnvironmentBlocked.value);
    const options = { signal, timeoutMs: 15000 };
    const [nextOverview, campaignList] = await Promise.all([
      managementClient.getAgentUpdateOverview({}, options),
      managementClient.listAgentUpdateCampaigns({ limit: 50n }, options),
    ]);
    return { nextOverview, campaignList };
  },
  apply: ({ nextOverview, campaignList }) => {
    overview.value = nextOverview;
    campaigns.value = campaignList.campaigns;
    operationError.value = "";
  },
  error: (error) => { operationError.value = messageFromError(error); },
  loading: (value) => { loading.value = value; },
});

function refresh() {
  return polling.refresh();
}

function openPlan() {
  planError.value = "";
  planName.value = trustedTarget.value ? `Fleet to ${trustedTarget.value.version}` : "Fleet update";
  selectedAgentIds.value = agents.value.filter((agent) => !agentUnavailableForPlan(agent)).map((agent) => agent.agentId.toString());
  preview.value = [];
  previewFingerprint.value = "";
  planOpen.value = true;
}

function agentUnavailableForPlan(agent: AgentUpdateOverviewAgent): boolean {
  return !agent.updaterEnrolled || !agent.connected || Boolean(agent.activeAssignmentId) || agentIsAlreadyOnUpdateTarget(agent, trustedTarget.value);
}

function policyRequest() {
  return {
    maxUnavailable: BigInt(maxUnavailable.value),
    minimumEligibleAgentsPerRoute: BigInt(minimumEligiblePerRoute.value),
    canaryCount: BigInt(canaryCount.value),
    waveSize: BigInt(waveSize.value),
    healthyDwellMillis: BigInt(healthyDwellSeconds.value * 1000),
  };
}

async function previewPlan() {
  if (planBusy.value || !trustedTarget.value || selectedAgentIds.value.length === 0) return;
  const fingerprint = currentPlanFingerprint.value;
  previewLoading.value = true;
  planError.value = "";
  preview.value = [];
  previewFingerprint.value = "";
  try {
    const response = await managementClient.previewAgentUpdateCampaign({
      agentIds: selectedAgentIds.value.map(BigInt),
      target: trustedTarget.value,
      policy: policyRequest(),
    });
    preview.value = response.agents;
    previewFingerprint.value = fingerprint;
  } catch (error) {
    planErrorTitle.value = "Safety preview failed";
    planError.value = messageFromError(error);
  } finally {
    previewLoading.value = false;
  }
}

async function createPlan() {
  if (planBusy.value || !canCreate.value || !trustedTarget.value) return;
  const target = trustedTarget.value;
  planCreating.value = true;
  planError.value = "";
  const onError = (error: unknown) => {
    planErrorTitle.value = "Unable to start campaign";
    planError.value = messageFromError(error);
  };
  const action = async () => {
    await managementClient.createAgentUpdateCampaign({
      name: planName.value.trim(),
      agentIds: selectedAgentIds.value.map(BigInt),
      target,
      policy: policyRequest(),
    });
    planOpen.value = false;
    await refresh();
  };
  try {
    if (runManagementAction) await runManagementAction(action, "Update campaign started", { onError });
    else await action();
  } catch (error) {
    onError(error);
  } finally {
    planCreating.value = false;
  }
}

async function changeCampaign(campaign: AgentUpdateCampaign, action: "pause" | "resume" | "cancel") {
  if (campaignBusy.value || (action === "resume" && !canResumeUpdateCampaign(campaign))) return;
  const request = { campaignId: campaign.id, expectedGeneration: campaign.generation };
  await runCampaignAction(campaign, async () => {
    if (action === "pause") await managementClient.pauseAgentUpdateCampaign(request);
    else if (action === "resume") await managementClient.resumeAgentUpdateCampaign(request);
    else await managementClient.cancelAgentUpdateCampaign(request);
  }, action === "cancel" ? "Campaign cancelled" : `Campaign ${action}d`);
}

async function retryFailed(campaign: AgentUpdateCampaign, recovery: boolean) {
  if (campaignBusy.value) return;
  const failed = recovery ? recoverableUpdateAssignments(campaign) : retryableStageAssignments(campaign);
  if (!failed.length) return;
  await runCampaignAction(campaign, async () => {
    await managementClient.retryAgentUpdateAssignments({
      campaignId: campaign.id,
      assignmentIds: failed.map((assignment) => assignment.id),
      expectedCampaignGeneration: campaign.generation,
    });
  }, recovery ? "Rollback recovery requested" : "Failed assignments queued again");
}

async function runCampaignAction(campaign: AgentUpdateCampaign, action: () => Promise<unknown>, successMessage: string) {
  const key = campaign.id.toString();
  delete campaignErrors.value[key];
  const onError = (error: unknown) => { campaignErrors.value[key] = messageFromError(error); };
  const execute = async () => {
    try { await action(); }
    finally { await refresh(); }
  };
  try {
    if (runManagementAction) await runManagementAction(execute, successMessage, { onError });
    else await execute();
  } catch (error) { onError(error); }
}

function enableManagedUpdates(agent: AgentUpdateOverviewAgent) {
  void setupModal.value?.openExisting({ id: agent.agentId, publicId: agent.agentPublicId, name: agent.name, version: agent.tunnelVersion, commit: agent.tunnelCommit }, "enable-updates");
}

function repairUpdater(agent: AgentUpdateOverviewAgent) {
  void setupModal.value?.openExisting({ id: agent.agentId, publicId: agent.agentPublicId, name: agent.name, version: agent.tunnelVersion, commit: agent.tunnelCommit }, "reinstall");
}

function toggleAgent(agent: AgentUpdateOverviewAgent, checked: boolean) {
  const id = agent.agentId.toString();
  selectedAgentIds.value = checked
    ? Array.from(new Set([...selectedAgentIds.value, id]))
    : selectedAgentIds.value.filter((candidate) => candidate !== id);
  preview.value = [];
  previewFingerprint.value = "";
}

/** Narrow screens stack each enrollment row into one cell instead of scrolling under a pinned column. */
const ENROLLMENT_COLUMN_WIDTHS = { agent: 200, updater: 230, tunnel: 170, traffic: 96, actions: 196 } as const;
const compactLayout = useNarrowScreen();

const enrollmentColumns = computed<DataTableColumns<AgentUpdateOverviewAgent>>(() => compactLayout.value
  ? [
      {
        title: "Agent",
        key: "agent",
        render: (agent) => h("div", { class: "update-cell update-cell--stacked" }, [
          renderEnrollmentIdentity(agent),
          h("div", { class: "update-cell" }, [h("span", { class: "update-meta" }, "Pinned updater"), renderEnrollmentUpdater(agent)]),
          h("div", { class: "update-cell" }, [h("span", { class: "update-meta" }, "Live tunnel"), renderEnrollmentTunnel(agent)]),
          h("p", { class: "update-meta" }, agent.cordoned ? "Traffic cordoned" : "Traffic eligible"),
          renderEnrollmentActions(agent),
        ]),
      },
    ]
  : [
      { title: "Agent", key: "agent", minWidth: ENROLLMENT_COLUMN_WIDTHS.agent, render: (agent) => renderEnrollmentIdentity(agent) },
      { title: "Pinned updater", key: "updater", width: ENROLLMENT_COLUMN_WIDTHS.updater, render: (agent) => renderEnrollmentUpdater(agent) },
      { title: "Live tunnel", key: "tunnel", width: ENROLLMENT_COLUMN_WIDTHS.tunnel, render: (agent) => renderEnrollmentTunnel(agent) },
      {
        title: "Traffic",
        key: "traffic",
        width: ENROLLMENT_COLUMN_WIDTHS.traffic,
        render: (agent) => h("span", { class: agent.cordoned ? "update-value warning-text" : "update-value" }, agent.cordoned ? "Cordoned" : "Eligible"),
      },
      {
        title: "Actions",
        key: "actions",
        width: ENROLLMENT_COLUMN_WIDTHS.actions,
        align: "right",
        fixed: "right",
        render: (agent) => renderEnrollmentActions(agent),
      },
    ]);
const enrollmentScrollX = computed(() => compactLayout.value
  ? undefined
  : Object.values(ENROLLMENT_COLUMN_WIDTHS).reduce((sum, width) => sum + width, 0));

function renderEnrollmentIdentity(agent: AgentUpdateOverviewAgent) {
  return h("div", { class: "update-identity" }, [
    h("span", { class: ["update-state-dot", { "update-state-dot--online": agent.connected }], "aria-hidden": "true" }),
    h("div", { class: "update-cell" }, [
      h("bdi", { class: "update-name", dir: "ltr", title: diagnosticInspectionText(agent.name) }, diagnosticExcerpt(agent.name, 64).text),
      h("p", { class: "update-id mono-text", title: diagnosticInspectionText(agent.agentPublicId) }, [
        h("bdi", { dir: "ltr" }, diagnosticInspectionText(agent.agentPublicId)),
      ]),
    ]),
  ]);
}

function renderEnrollmentUpdater(agent: AgentUpdateOverviewAgent) {
  if (!agent.updaterEnrolled) {
    return h("div", { class: "update-cell" }, [
      h("span", { class: "update-value muted-text" }, "Not enrolled"),
      h("p", { class: "update-meta" }, "One-time host setup required"),
    ]);
  }
  const fresh = updaterIsFresh(agent);
  return h("div", { class: "update-cell" }, [
    h("p", { class: "update-inline" }, [
      h("code", { class: "update-value mono-text", title: diagnosticInspectionText(agent.updaterVersion || "enrolled") }, [
        h("bdi", { dir: "ltr" }, agent.updaterVersion ? diagnosticExcerpt(agent.updaterVersion, 32).text : "Enrolled"),
      ]),
      h(NTag, { size: "small", bordered: false, type: fresh ? "success" : "warning" }, { default: () => fresh ? "Managed" : "Worker stale" }),
    ]),
    h("p", { class: "update-meta" }, sentenceCase(updaterLastSeenLabel(agent))),
  ]);
}

function renderEnrollmentTunnel(agent: AgentUpdateOverviewAgent) {
  return h("div", { class: "update-cell" }, [
    h("code", { class: "update-value mono-text", title: diagnosticInspectionText(agent.tunnelVersion || "unreported") }, [
      h("bdi", { dir: "ltr" }, agent.tunnelVersion ? diagnosticExcerpt(agent.tunnelVersion, 32).text : "Unreported"),
    ]),
    agent.tunnelCommit
      ? h("p", { class: "update-meta mono-text", title: diagnosticInspectionText(agent.tunnelCommit) }, [
          h("bdi", { dir: "ltr" }, shortDigest(diagnosticInspectionText(agent.tunnelCommit))),
        ])
      : null,
  ]);
}

function renderEnrollmentActions(agent: AgentUpdateOverviewAgent) {
  if (!agent.updaterEnrolled) {
    return h("div", { class: "update-actions" }, [
      h(NButton, {
        secondary: true,
        size: "small",
        disabled: isBusy.value || !agent.tunnelVersion || !agent.tunnelCommit,
        onClick: () => enableManagedUpdates(agent),
      }, { default: () => "Enable managed updates" }),
    ]);
  }
  return h("div", { class: "update-actions" }, [
    h("span", {
      title: agent.activeAssignmentId ? "Recovery must finish before replacing updater enrollment." : "Refresh the pinned updater while preserving the live tunnel binary.",
    }, [
      h(NButton, {
        secondary: true,
        size: "small",
        disabled: campaignBusy.value || Boolean(agent.activeAssignmentId) || !trustedTarget.value || !managementAuthority.value,
        onClick: () => repairUpdater(agent),
      }, { default: () => "Repair updater" }),
    ]),
    agent.activeAssignmentId ? h("p", { class: "update-meta" }, "Finish campaign recovery before repair.") : null,
  ]);
}

function enrollmentRowKey(agent: AgentUpdateOverviewAgent): string {
  return agent.agentPublicId;
}

function sentenceCase(value: string): string {
  return value ? `${value.charAt(0).toLocaleUpperCase()}${value.slice(1)}` : value;
}

function shortDigest(value: string): string {
  return value ? `${value.slice(0, 10)}…${value.slice(-8)}` : "unavailable";
}

function campaignLabel(state: AgentUpdateCampaignState): string {
  return AgentUpdateCampaignState[state]?.toLowerCase().replaceAll("_", " ") ?? "unknown";
}

function campaignTag(state: AgentUpdateCampaignState): "success" | "warning" | "error" | "default" {
  if (state === AgentUpdateCampaignState.COMPLETED) return "success";
  if (state === AgentUpdateCampaignState.PAUSED) return "warning";
  if (state === AgentUpdateCampaignState.CANCELLED) return "error";
  return "default";
}

function assignmentProgress(campaign: AgentUpdateCampaign): string {
  const complete = campaign.assignments.filter((assignment) => assignment.state === AgentUpdateAssignmentState.SUCCEEDED).length;
  return `${complete}/${campaign.assignments.length}`;
}

function enumLabel(value: string): string {
  return value.toLowerCase().replaceAll("_", " ");
}

function assignmentStateLabel(assignment: AgentUpdateAssignment): string {
  if (assignment.cordoned && assignment.desiredAction === AgentUpdateDesiredAction.ROLLBACK) return "rollback pending";
  return enumLabel(AgentUpdateAssignmentState[assignment.state] ?? "unknown");
}

function assignmentActionLabel(action: AgentUpdateDesiredAction): string {
  return enumLabel(AgentUpdateDesiredAction[action] ?? "none");
}

function assignmentTag(state: AgentUpdateAssignmentState): "success" | "warning" | "error" | "info" | "default" {
  if (state === AgentUpdateAssignmentState.SUCCEEDED) return "success";
  if (state === AgentUpdateAssignmentState.FAILED || state === AgentUpdateAssignmentState.BLOCKED) return "error";
  if (state === AgentUpdateAssignmentState.CANCELLED) return "warning";
  if (
    state === AgentUpdateAssignmentState.STAGING ||
    state === AgentUpdateAssignmentState.STAGED ||
    state === AgentUpdateAssignmentState.ACTIVATING ||
    state === AgentUpdateAssignmentState.AWAITING_TUNNEL ||
    state === AgentUpdateAssignmentState.HEALTHY_DWELL
  ) return "info";
  return "default";
}

function assignmentEvidence(assignment: AgentUpdateAssignment): string {
  if (assignment.failureCode) return `${assignment.failureCode}${assignment.failureDetail ? ` · ${assignment.failureDetail}` : ""}`;
  if (assignment.observedVersion) return `Observed ${assignment.observedVersion}${assignment.observedCommit ? ` · ${assignment.observedCommit.slice(0, 10)}` : ""}`;
  if (assignment.freshTunnelAtUnixMillis) return "Fresh tunnel observed; proving health";
  if (assignment.activatedAtUnixMillis) return "Activation attested; awaiting fresh tunnel";
  if (assignment.attestedBinarySha256) return `Binary ${shortDigest(assignment.attestedBinarySha256)}`;
  return "No execution evidence reported yet";
}

function previewBlockerLabel(blocker: string): string {
  return agentUpdatePreviewBlockerLabel(blocker, trustedTarget.value?.minimumUpdaterVersion);
}

function formatTimestamp(value: bigint): string {
  return value ? new Date(Number(value)).toLocaleString() : "—";
}

function updaterIsFresh(agent: AgentUpdateOverviewAgent): boolean {
  if (!agent.updaterEnrolled || !agent.updaterLastSeenAtUnixMillis) return false;
  const age = Date.now() - Number(agent.updaterLastSeenAtUnixMillis);
  return age >= 0 && age <= updaterFreshnessWindowMs;
}

function updaterLastSeenLabel(agent: AgentUpdateOverviewAgent): string {
  if (!agent.updaterLastSeenAtUnixMillis) return "worker has never checked in";
  const observed = new Date(Number(agent.updaterLastSeenAtUnixMillis));
  return updaterIsFresh(agent)
    ? `worker checked in ${observed.toLocaleTimeString()}`
    : `worker stale since ${observed.toLocaleString()}`;
}

watch([selectedEnvironmentId, selectedEnvironmentBlocked], () => {
  overview.value = null;
  campaigns.value = [];
  campaignErrors.value = {};
  planOpen.value = false;
  preview.value = [];
  previewFingerprint.value = "";
  void polling.scopeChanged();
}, { flush: "sync" });
onMounted(() => {
  void polling.start();
});
onUnmounted(() => {
  polling.stop();
});
</script>

<template>
  <div class="agent-updates">
    <PageHeader title="Agents" description="Trusted releases, rollout safety, and managed agent update campaigns.">
      <template #actions>
        <NButton secondary size="small" :loading="loading" :disabled="isBusy" @click="refresh">
          <template #icon><RefreshIcon class="icon-sm" /></template>
          Refresh status
        </NButton>
        <NButton type="primary" size="small" :disabled="!trustedTarget || !managementAuthority || !enrolledAgents.length || isBusy" @click="openPlan">
          Plan rollout
        </NButton>
      </template>
    </PageHeader>

    <NTabs class="agent-updates__tabs" type="line" size="small" value="updates" @update:value="selectSection">
      <NTab v-for="section in sections" :key="section.key" :name="section.key" :tab="section.label" />
    </NTabs>

    <NAlert v-if="operationError" type="error" :bordered="false" closable @close="operationError = ''">
      <bdi dir="ltr">{{ diagnosticInspectionText(operationError) }}</bdi>
    </NAlert>
    <NAlert v-if="managementAuthorityWarning" type="warning" :bordered="false">
      Managed update control is fail-closed: <bdi dir="ltr">{{ diagnosticInspectionText(managementAuthorityWarning) }}</bdi>. Public proxy traffic is unaffected.
    </NAlert>

    <NSpin :show="loading && !overview" content-class="agent-updates__content">
      <MetricStrip label="Fleet update readiness">
        <MetricCard
          label="Managed hosts"
          :value="agents.length ? `${enrolledAgents.length}/${agents.length}` : '—'"
          :detail="agents.length ? `${readinessPercent}% have a pinned rescue updater` : 'No agents registered'"
        />
        <MetricCard label="Online managed" :value="String(onlineManagedAgents)" detail="Connected with a recent updater check-in" />
        <MetricCard
          label="Setup required"
          :value="String(unsupportedAgents)"
          :tone="unsupportedAgents ? 'warning' : 'default'"
          detail="One-time host enrollment; no token rotation"
        />
        <MetricCard label="Unavailable budget" :value="String(activeCampaigns.length)" detail="Running or paused campaigns" />
      </MetricStrip>

      <SectionCard title="Management release" class="agent-release">
        <template #badge>
          <NTag size="small" :bordered="false" :type="trustedTarget && managementAuthority ? 'success' : 'warning'">
            {{ trustedTarget && managementAuthority ? "Trusted" : "Unavailable" }}
          </NTag>
        </template>
        <dl class="agent-release__grid">
          <div class="agent-release__item">
            <dt>Target release</dt>
            <dd class="agent-release__value mono-text">
              <bdi dir="ltr">{{ diagnosticExcerpt(trustedTarget?.version || serverVersion, 48).text }}</bdi>
            </dd>
            <dd class="agent-release__detail">{{ trustedTarget ? `Trusted target · sequence ${trustedTarget.releaseSequence}` : "No trusted release target is currently available." }}</dd>
          </div>
          <div class="agent-release__item">
            <dt>Release identity</dt>
            <dd class="agent-release__value mono-text" :title="selectedTargetDigest || undefined">{{ selectedTargetDigest ? shortDigest(selectedTargetDigest) : "—" }}</dd>
            <dd class="agent-release__detail">
              <NTag size="small" :bordered="false" :type="trustedTarget ? 'success' : 'warning'">{{ trustedTarget ? "Verified" : "Unavailable" }}</NTag>
            </dd>
          </div>
          <div class="agent-release__item">
            <dt>Command authority</dt>
            <dd class="agent-release__value mono-text" :title="managementAuthority?.keyId || undefined">{{ managementAuthority ? shortDigest(managementAuthority.keyId) : "—" }}</dd>
            <dd class="agent-release__detail">
              <NTag size="small" :bordered="false" :type="managementAuthority ? 'success' : 'warning'">{{ managementAuthority ? "Pinned" : "Unavailable" }}</NTag>
              <span v-if="managementAuthority">Epoch {{ managementAuthority.epoch }}</span>
            </dd>
          </div>
        </dl>
      </SectionCard>

      <div class="agent-update-grid">
        <SectionCard title="Safety gates before activation" description="Every rollout follows the same desired-state contract.">
          <template #badge><NTag size="small" :bordered="false">Desired state</NTag></template>
          <ol class="agent-rollout-steps">
            <li class="agent-rollout-step agent-rollout-step--ready">
              <span class="agent-rollout-step__marker"><CheckIcon aria-hidden="true" /></span>
              <div><strong>Verify</strong><small>Signer, digest, compatibility</small></div>
            </li>
            <li class="agent-rollout-step">
              <span class="agent-rollout-step__marker">2</span>
              <div><strong>Stage</strong><small>Download while traffic remains live</small></div>
            </li>
            <li class="agent-rollout-step">
              <span class="agent-rollout-step__marker">3</span>
              <div><strong>Drain</strong><small>Cordon and preserve route quorum</small></div>
            </li>
            <li class="agent-rollout-step">
              <span class="agent-rollout-step__marker">4</span>
              <div><strong>Prove</strong><small>Fresh tunnel and healthy dwell</small></div>
            </li>
          </ol>
        </SectionCard>

        <SectionCard :title="trustedTarget && managementAuthority ? 'Route-aware activation ready' : 'Update control is fail-closed'">
          <template #badge>
            <NTag size="small" :bordered="false" :type="trustedTarget && managementAuthority ? 'success' : 'warning'">{{ trustedTarget && managementAuthority ? "Enforced" : "Unavailable" }}</NTag>
          </template>
          <p class="agent-update-policy__notice">
            <WarningIcon aria-hidden="true" />
            <span>Activation never proceeds unless the route retains its configured eligible-agent quorum, the privileged helper attests the exact artifact, and the new tunnel survives its health dwell.</span>
          </p>
          <dl class="agent-update-policy">
            <div><dt>Target</dt><dd><bdi dir="ltr">{{ trustedTarget ? diagnosticExcerpt(trustedTarget.version, 48).text : "No trusted release" }}</bdi></dd></div>
            <div><dt>Canary</dt><dd>1 agent · 2 minute dwell</dd></div>
            <div><dt>Wave</dt><dd>Explicit bounded size · route-aware</dd></div>
            <div><dt>Force</dt><dd>Never automatic</dd></div>
          </dl>
        </SectionCard>
      </div>

      <SectionCard title="Updater enrollment" flush class="agent-update-table">
        <template #description>
          <template v-if="trustedTarget">
            This target requires pinned updater <bdi dir="ltr">{{ diagnosticExcerpt(trustedTarget.minimumUpdaterVersion, 32).text }}</bdi> or newer. Repair updates the pinned updater and keeps the live tunnel binary.
          </template>
          <template v-else>Hosts with a pinned rescue updater can be updated from this page.</template>
        </template>
        <template #badge><NTag size="small" :bordered="false">{{ enrolledAgents.length }}/{{ agents.length }} enrolled</NTag></template>
        <NDataTable
          v-if="agents.length"
          :columns="enrollmentColumns"
          :data="agents"
          :row-key="enrollmentRowKey"
          :pagination="false"
          :bordered="false"
         
          :scroll-x="enrollmentScrollX"
          size="small"
        />
        <p v-else class="agent-update-empty">No agents are registered.</p>
      </SectionCard>

      <SectionCard title="Update campaigns" description="Execution history · refreshes every 5 seconds." flush class="agent-update-campaigns">
        <template v-if="campaigns.length" #badge><NTag size="small" :bordered="false">{{ campaigns.length }}</NTag></template>
        <div v-if="campaigns.length" class="agent-update-campaigns__rows">
          <article v-for="campaign in campaigns" :key="campaign.id.toString()" class="agent-update-campaign">
            <div class="agent-update-campaign__summary">
              <div class="agent-update-campaign__identity">
                <NTag size="small" :bordered="false" :type="campaignTag(campaign.state)">{{ sentenceCase(campaignLabel(campaign.state)) }}</NTag>
                <bdi dir="ltr" class="agent-update-campaign__name" :title="diagnosticInspectionText(campaign.name)">{{ diagnosticExcerpt(campaign.name, 96).text }}</bdi>
                <small><bdi dir="ltr">{{ diagnosticExcerpt(campaign.target?.version ?? "", 48).text }}</bdi> · generation {{ campaign.generation }}</small>
              </div>
              <div class="agent-update-campaign__metric agent-update-campaign__healthy"><span>Proven healthy</span><strong>{{ assignmentProgress(campaign) }}</strong></div>
              <div class="agent-update-campaign__metric agent-update-campaign__wave"><span>Wave / unavailable</span><strong>{{ campaign.policy?.waveSize || 0n }} / {{ campaign.policy?.maxUnavailable || 0n }}</strong></div>
              <div class="agent-update-campaign__actions">
                <NButton v-if="campaign.state === AgentUpdateCampaignState.RUNNING" secondary size="tiny" :disabled="campaignBusy" @click="changeCampaign(campaign, 'pause')"><template #icon><PauseIcon /></template>Pause</NButton>
                <NButton v-if="campaign.state === AgentUpdateCampaignState.PAUSED" secondary size="tiny" :disabled="campaignBusy || !canResumeUpdateCampaign(campaign)" :title="canResumeUpdateCampaign(campaign) ? undefined : 'Recover reserved hosts or retry failed assignments before resuming.'" @click="changeCampaign(campaign, 'resume')"><template #icon><PlayIcon /></template>Resume</NButton>
                <NButton v-if="recoverableUpdateAssignments(campaign).length" secondary size="tiny" :disabled="campaignBusy" @click="retryFailed(campaign, true)"><template #icon><RetryIcon /></template>Recover agents</NButton>
                <NButton v-if="retryableStageAssignments(campaign).length" secondary size="tiny" :disabled="campaignBusy" @click="retryFailed(campaign, false)"><template #icon><RetryIcon /></template>Retry failed</NButton>
                <NButton v-if="campaign.state === AgentUpdateCampaignState.RUNNING || campaign.state === AgentUpdateCampaignState.PAUSED" secondary size="tiny" type="error" :disabled="campaignBusy" @click="changeCampaign(campaign, 'cancel')">Cancel</NButton>
              </div>
            </div>
            <NAlert v-if="campaignErrors[campaign.id.toString()]" type="error" :bordered="false" class="agent-update-campaign__error" closable @close="delete campaignErrors[campaign.id.toString()]">
              <bdi dir="ltr">{{ diagnosticInspectionText(campaignErrors[campaign.id.toString()]) }}</bdi>
            </NAlert>
            <p v-if="updateRecoveryMessage(campaign)" class="agent-update-campaign__recovery" role="status">{{ updateRecoveryMessage(campaign) }}</p>
            <details class="agent-update-campaign__details">
              <summary>Inspect {{ campaign.assignments.length }} agent assignment{{ campaign.assignments.length === 1 ? "" : "s" }}</summary>
              <div class="agent-update-assignment-list">
                <div v-for="assignment in campaign.assignments" :key="assignment.id.toString()" class="agent-update-assignment">
                  <div class="agent-update-assignment__identity">
                    <span class="update-state-dot" :class="{ 'update-state-dot--online': assignment.state === AgentUpdateAssignmentState.SUCCEEDED }" aria-hidden="true" />
                    <div>
                      <bdi dir="ltr" class="agent-update-assignment__name" :title="diagnosticInspectionText(assignment.agentName || assignment.agentPublicId)">{{ diagnosticExcerpt(assignment.agentName || assignment.agentPublicId, 64).text }}</bdi>
                      <small class="mono-text" :title="diagnosticInspectionText(assignment.agentPublicId)"><bdi dir="ltr">{{ diagnosticInspectionText(assignment.agentPublicId) }}</bdi></small>
                    </div>
                  </div>
                  <div class="agent-update-assignment__state">
                    <span>State</span>
                    <NTag size="small" :bordered="false" :type="assignment.desiredAction === AgentUpdateDesiredAction.ROLLBACK ? 'warning' : assignmentTag(assignment.state)">{{ sentenceCase(assignmentStateLabel(assignment)) }}</NTag>
                  </div>
                  <div class="agent-update-assignment__action"><span>Desired action</span><strong>{{ sentenceCase(assignmentActionLabel(assignment.desiredAction)) }}</strong></div>
                  <div class="agent-update-assignment__evidence"><span>Evidence</span><strong><bdi dir="ltr" :title="diagnosticInspectionText(assignmentEvidence(assignment))">{{ diagnosticExcerpt(assignmentEvidence(assignment), 160).text }}</bdi></strong></div>
                  <div class="agent-update-assignment__updated"><span>Updated</span><strong>{{ formatTimestamp(assignment.updatedAtUnixMillis) }}</strong></div>
                </div>
              </div>
            </details>
          </article>
        </div>
        <p v-else class="agent-update-empty">No managed update campaigns yet.</p>
      </SectionCard>
    </NSpin>

    <NModal v-model:show="planOpen" preset="card" title="Plan Agent Rollout" class="agent-update-modal"
      :style="modalCardStyle('760px')" :content-style="modalScrollableContentStyle()" :bordered="false"
      :closable="!planBusy" :mask-closable="false" :close-on-esc="!planBusy">
      <div class="agent-update-modal__body">
        <NAlert type="info" :bordered="false">Preview is authoritative: the server re-evaluates route quorum, connectivity, enrollment, and active assignments before creating the campaign.</NAlert>
        <label class="agent-update-field"><span>Campaign name</span><NInput v-model:value="planName" maxlength="128" :disabled="planBusy" /></label>
        <div class="agent-update-form-grid">
          <label class="agent-update-field"><span>Max unavailable</span><NInputNumber v-model:value="maxUnavailable" :min="1" :max="100" :disabled="planBusy" /></label>
          <label class="agent-update-field"><span>Other agents required per route</span><NInputNumber v-model:value="minimumEligiblePerRoute" :min="1" :max="100" :disabled="planBusy" /></label>
          <label class="agent-update-field"><span>Canary agents</span><NInputNumber v-model:value="canaryCount" :min="1" :max="100" :disabled="planBusy" /></label>
          <label class="agent-update-field"><span>Wave size</span><NInputNumber v-model:value="waveSize" :min="1" :max="100" :disabled="planBusy" /></label>
          <label class="agent-update-field"><span>Healthy dwell seconds</span><NInputNumber v-model:value="healthyDwellSeconds" :min="10" :max="86400" :disabled="planBusy" /></label>
        </div>
        <fieldset class="agent-update-agent-picker">
          <legend>Agents</legend>
          <label v-for="agent in agents" :key="agent.agentPublicId" :class="{ 'agent-update-agent-picker__blocked': agentUnavailableForPlan(agent) }">
            <NCheckbox
              :checked="selectedAgentIds.includes(agent.agentId.toString())"
              :disabled="planBusy || (!selectedAgentIds.includes(agent.agentId.toString()) && agentUnavailableForPlan(agent))"
              @update:checked="toggleAgent(agent, $event)"
            />
            <span>
              <bdi dir="ltr" class="agent-update-agent-picker__name" :title="diagnosticInspectionText(agent.name)">{{ diagnosticExcerpt(agent.name, 64).text }}</bdi>
              <small>{{ agentRolloutStatus(agent, campaigns, trustedTarget) }}</small>
            </span>
          </label>
        </fieldset>
        <ul v-if="preview.length" class="agent-update-preview" aria-label="Safety preview">
          <li v-for="agent in preview" :key="agent.agentPublicId" :class="{ 'agent-update-preview--blocked': !agent.eligible }">
            <CheckIcon v-if="agent.eligible" aria-hidden="true" /><WarningIcon v-else aria-hidden="true" />
            <span>
              <bdi dir="ltr" class="agent-update-preview__name" :title="diagnosticInspectionText(agent.name)">{{ diagnosticExcerpt(agent.name, 64).text }}</bdi>
              <small>{{ agent.eligible ? "Eligible" : agent.blockers.map(previewBlockerLabel).join("; ") }}</small>
            </span>
          </li>
        </ul>
        <NAlert v-if="preview.length && !previewIsCurrent" type="warning" :bordered="false">
          Rollout settings changed after the last safety preview. Preview again before starting.
        </NAlert>
      </div>
      <template #footer>
        <div class="agent-update-modal__footer">
          <NAlert v-if="planError" type="error" :title="planErrorTitle" :bordered="false" class="agent-update-modal__error">
            <bdi dir="ltr">{{ diagnosticInspectionText(planError) }}</bdi>
          </NAlert>
          <NAlert v-else-if="operationError" type="error" title="Unable to refresh update status" :bordered="false" class="agent-update-modal__error">
            <bdi dir="ltr">{{ diagnosticInspectionText(operationError) }}</bdi>
          </NAlert>
          <div class="agent-update-modal__actions">
            <NButton :disabled="planBusy" @click="planOpen = false">Cancel</NButton>
            <NButton secondary :loading="previewLoading" :disabled="planBusy || !trustedTarget || !selectedAgentIds.length" @click="previewPlan">Preview safety</NButton>
            <NButton type="primary" :loading="planCreating" :disabled="!canCreate || planBusy" @click="createPlan">Start campaign</NButton>
          </div>
        </div>
      </template>
    </NModal>

    <AgentSetupModal ref="setupModal" />
  </div>
</template>

<style scoped>
.agent-updates {
  display: grid;
  min-width: 0;
  gap: 1.25rem;
}

.agent-updates__tabs {
  min-width: 0;
  margin-top: -0.25rem;
}

.agent-updates :deep(.agent-updates__content) {
  display: grid;
  min-width: 0;
  gap: 1.25rem;
}

.agent-updates :deep(.metric-card__detail) {
  white-space: normal;
}

/* Release identity */
.agent-release__grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  margin: 0;
}

.agent-release__item {
  display: grid;
  min-width: 0;
  align-content: start;
  gap: 0.25rem;
  padding: 0 1rem;
  border-left: 1px solid var(--app-border-subtle);
}

.agent-release__item:first-child {
  padding-left: 0;
  border-left: 0;
}

.agent-release__item dt {
  color: var(--app-text-muted);
  font-size: 0.8125rem;
}

.agent-release__item dd {
  margin: 0;
}

.agent-release__value {
  overflow: hidden;
  color: var(--app-text);
  font-size: 1rem;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-release__detail {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.375rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

/* Rollout contract and policy */
.agent-update-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.35fr) minmax(0, 1fr);
  align-items: start;
  gap: 1.25rem;
}

.agent-rollout-steps {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 1rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.agent-rollout-step {
  display: grid;
  min-width: 0;
  align-content: start;
  gap: 0.5rem;
}

.agent-rollout-step__marker {
  display: grid;
  width: 1.5rem;
  height: 1.5rem;
  place-items: center;
  border-radius: 50%;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.agent-rollout-step--ready .agent-rollout-step__marker {
  color: var(--app-success);
}

.agent-rollout-step__marker svg {
  width: 0.875rem;
  height: 0.875rem;
}

.agent-rollout-step strong,
.agent-rollout-step small {
  display: block;
}

.agent-rollout-step strong {
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 600;
}

.agent-rollout-step small {
  margin-top: 0.125rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.4;
}

.agent-update-policy__notice {
  display: flex;
  align-items: flex-start;
  gap: 0.625rem;
  margin: 0;
  border-radius: 8px;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  line-height: 1.5;
  padding: 0.75rem;
}

.agent-update-policy__notice svg {
  width: 1rem;
  height: 1rem;
  flex: 0 0 auto;
  margin-top: 0.125rem;
  color: var(--app-warning);
}

.agent-update-policy {
  margin: 0.75rem 0 0;
}

.agent-update-policy div {
  display: flex;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.5rem 0;
  font-size: 0.8125rem;
}

.agent-update-policy div + div {
  border-top: 1px solid var(--app-border-subtle);
}

.agent-update-policy dt {
  color: var(--app-text-muted);
}

.agent-update-policy dd {
  min-width: 0;
  margin: 0;
  color: var(--app-text);
  overflow-wrap: anywhere;
  text-align: right;
}

/* Updater enrollment table (cells are rendered by NDataTable, so styles reach in with :deep) */
.agent-update-table :deep(.n-data-table-th),
.agent-update-table :deep(.n-data-table-td) {
  padding: 0.75rem;
  vertical-align: top;
}

.agent-update-table :deep(.n-data-table-th:first-child),
.agent-update-table :deep(.n-data-table-td:first-child) {
  padding-left: 1.25rem;
}

.agent-update-table :deep(.n-data-table-th:last-child),
.agent-update-table :deep(.n-data-table-td:last-child) {
  padding-right: 1.25rem;
}

.agent-update-table :deep(.update-cell) {
  display: grid;
  min-width: 0;
  gap: 0.125rem;
}

.agent-update-table :deep(.update-cell--stacked) {
  gap: 0.5rem;
}

.agent-update-table :deep(.update-identity) {
  display: flex;
  min-width: 0;
  align-items: flex-start;
  gap: 0.625rem;
}

.agent-update-table :deep(.update-name) {
  display: block;
  overflow: hidden;
  color: var(--app-text);
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-update-table :deep(.update-id) {
  overflow: hidden;
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-update-table :deep(.update-inline) {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.375rem 0.5rem;
  margin: 0;
}

.agent-update-table :deep(.update-value) {
  min-width: 0;
  max-width: 100%;
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.8125rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-update-table :deep(code.update-value) {
  font-size: 0.75rem;
}

.agent-update-table :deep(.update-value.warning-text) {
  color: var(--app-warning);
}

.agent-update-table :deep(.update-meta) {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
  line-height: 1.45;
  overflow-wrap: anywhere;
}

.agent-update-table :deep(.update-actions) {
  display: grid;
  justify-items: end;
  gap: 0.375rem;
  text-align: right;
}

.agent-update-table :deep(.update-cell--stacked .update-actions) {
  justify-items: start;
  text-align: left;
}

.agent-updates :deep(.update-state-dot),
.update-state-dot {
  width: 0.5rem;
  height: 0.5rem;
  flex: 0 0 auto;
  margin-top: 0.4rem;
  border-radius: 50%;
  background: var(--app-text-muted);
}

.agent-updates :deep(.update-state-dot--online),
.update-state-dot--online {
  background: var(--app-success);
}

.agent-update-empty {
  margin: 0;
  padding: 2rem 1.25rem;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  text-align: center;
}

/* Campaigns */
.agent-update-campaigns {
  container: update-campaigns / inline-size;
}

.agent-update-campaigns__rows {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
}

.agent-update-campaign + .agent-update-campaign {
  border-top: 1px solid var(--app-border-subtle);
}

.agent-update-campaign__summary {
  display: grid;
  grid-template-columns: minmax(0, 1.4fr) minmax(0, 0.5fr) minmax(0, 0.6fr) auto;
  grid-template-areas: "identity healthy wave actions";
  align-items: center;
  gap: 1rem;
  padding: 0.875rem 1.25rem;
}

.agent-update-campaign__summary > div {
  min-width: 0;
}

.agent-update-campaign__identity {
  grid-area: identity;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: center;
  gap: 0.25rem 0.5rem;
}

.agent-update-campaign__name {
  color: var(--app-text);
  font-weight: 600;
  overflow-wrap: anywhere;
  unicode-bidi: isolate;
}

.agent-update-campaign__identity small {
  grid-column: 1 / -1;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  overflow-wrap: anywhere;
}

.agent-update-campaign__healthy { grid-area: healthy; }
.agent-update-campaign__wave { grid-area: wave; }

.agent-update-campaign__metric span,
.agent-update-assignment > div > span {
  display: block;
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.agent-update-campaign__metric strong {
  display: block;
  margin-top: 0.125rem;
  color: var(--app-text);
  font-size: 0.875rem;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.agent-update-campaign__actions {
  grid-area: actions;
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 0.375rem;
}

.agent-update-campaign__actions svg {
  width: 0.8rem;
  height: 0.8rem;
}

.agent-update-campaign__recovery {
  margin: 0;
  padding: 0 1.25rem 0.875rem;
  color: var(--app-warning);
  font-size: 0.8125rem;
  line-height: 1.5;
  overflow-wrap: anywhere;
}

.agent-update-campaign__error {
  margin: 0 1.25rem 0.875rem;
  overflow-wrap: anywhere;
}

.agent-update-campaign__details {
  border-top: 1px solid var(--app-border-subtle);
}

.agent-update-campaign__details summary {
  width: fit-content;
  border-radius: 4px;
  color: var(--app-accent);
  cursor: pointer;
  font-size: 0.8125rem;
  font-weight: 500;
  margin: 0.25rem 1rem;
  padding: 0.375rem 0.25rem;
}

.agent-update-campaign__details summary:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: 1px;
}

.agent-update-assignment-list {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  background: var(--app-panel-muted);
}

.agent-update-assignment {
  display: grid;
  grid-template-columns: minmax(0, 1.1fr) minmax(0, 0.7fr) minmax(0, 0.55fr) minmax(0, 1.5fr) minmax(0, 0.75fr);
  grid-template-areas: "identity state action evidence updated";
  align-items: start;
  gap: 0.875rem;
  padding: 0.75rem 1.25rem;
}

.agent-update-assignment + .agent-update-assignment {
  border-top: 1px solid var(--app-border-subtle);
}

.agent-update-assignment > div {
  min-width: 0;
}

.agent-update-assignment > div > span {
  margin-bottom: 0.125rem;
}

.agent-update-assignment > div > strong {
  display: block;
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 500;
  overflow-wrap: anywhere;
}

.agent-update-assignment__identity {
  grid-area: identity;
  display: flex;
  align-items: flex-start;
  gap: 0.625rem;
}

.agent-update-assignment__identity > div {
  min-width: 0;
}

.agent-update-assignment__name,
.agent-update-assignment__identity small {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-update-assignment__name {
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 600;
}

.agent-update-assignment__identity small {
  margin-top: 0.125rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.agent-update-assignment__state { grid-area: state; }
.agent-update-assignment__action { grid-area: action; }
.agent-update-assignment__evidence { grid-area: evidence; }
.agent-update-assignment__updated { grid-area: updated; }

@container update-campaigns (max-width: 60rem) {
  .agent-update-campaign__summary {
    grid-template-columns: repeat(2, minmax(0, 1fr)) auto;
    grid-template-areas: "identity identity actions" "healthy wave actions";
  }

  .agent-update-assignment {
    grid-template-columns: minmax(0, 0.8fr) minmax(0, 0.8fr) minmax(0, 1.5fr);
    grid-template-areas: "identity identity updated" "state action evidence";
  }
}

@container update-campaigns (max-width: 40rem) {
  .agent-update-campaign__summary {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    grid-template-areas: "identity identity" "healthy wave" "actions actions";
    align-items: start;
  }

  .agent-update-campaign__actions {
    justify-content: flex-start;
  }

  .agent-update-assignment {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    grid-template-areas: "identity identity" "state action" "evidence evidence" "updated updated";
  }

  .agent-update-assignment__name,
  .agent-update-assignment__identity small {
    white-space: normal;
    overflow-wrap: anywhere;
  }
}

/* Plan rollout modal (teleported; classes are applied directly in this template) */
.agent-update-modal__body {
  display: grid;
  gap: 1rem;
}

.agent-update-field {
  display: grid;
  gap: 0.375rem;
}

.agent-update-field > span {
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  font-weight: 500;
}

.agent-update-field :deep(.n-input-number) {
  width: 100%;
}

.agent-update-form-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0.75rem;
}

.agent-update-agent-picker {
  min-width: 0;
  margin: 0;
  border: 0;
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 0.25rem 0.875rem;
}

.agent-update-agent-picker legend {
  float: left;
  width: 100%;
  padding: 0.5rem 0;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  font-weight: 500;
}

.agent-update-agent-picker > label {
  display: flex;
  clear: both;
  align-items: center;
  gap: 0.625rem;
  border-top: 1px solid var(--app-border-subtle);
  padding: 0.5rem 0;
}

.agent-update-agent-picker > label > span {
  min-width: 0;
}

.agent-update-agent-picker__name,
.agent-update-agent-picker small,
.agent-update-preview__name,
.agent-update-preview small {
  display: block;
  min-width: 0;
}

.agent-update-agent-picker__name,
.agent-update-preview__name {
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.agent-update-agent-picker small,
.agent-update-preview small {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  overflow-wrap: anywhere;
}

.agent-update-agent-picker__blocked {
  opacity: 0.55;
}

.agent-update-preview {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.5rem 1rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.agent-update-preview > li {
  display: flex;
  min-width: 0;
  align-items: flex-start;
  gap: 0.5rem;
}

.agent-update-preview > li > span {
  min-width: 0;
}

.agent-update-preview > li > svg {
  width: 0.9rem;
  height: 0.9rem;
  flex: 0 0 auto;
  margin-top: 0.15rem;
  color: var(--app-success);
}

.agent-update-preview > .agent-update-preview--blocked > svg {
  color: var(--app-warning);
}

.agent-update-modal__actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 0.5rem;
}

.agent-update-modal__footer {
  display: grid;
  gap: 1rem;
}

.agent-update-modal__error {
  max-height: min(12rem, 25dvh);
  overflow-y: auto;
  overflow-wrap: anywhere;
}

@media (max-width: 900px) {
  .agent-update-grid {
    grid-template-columns: minmax(0, 1fr);
  }

  .agent-update-form-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 639px) {
  .agent-release__grid {
    grid-template-columns: minmax(0, 1fr);
    gap: 0.75rem;
  }

  .agent-release__item {
    padding: 0.75rem 0 0;
    border-top: 1px solid var(--app-border-subtle);
    border-left: 0;
  }

  .agent-release__item:first-child {
    padding-top: 0;
    border-top: 0;
  }

  .agent-rollout-steps {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .agent-update-table :deep(.n-data-table-th:first-child),
  .agent-update-table :deep(.n-data-table-td:first-child) {
    padding-left: 1rem;
  }

  .agent-update-table :deep(.n-data-table-th:last-child),
  .agent-update-table :deep(.n-data-table-td:last-child) {
    padding-right: 1rem;
  }

  .agent-update-form-grid,
  .agent-update-preview {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
