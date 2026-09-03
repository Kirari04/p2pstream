<script setup lang="ts">
import { computed, inject, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { NAlert, NButton, NCheckbox, NInput, NInputNumber, NModal, NSpin, NTab, NTabs, NTag } from "naive-ui";
import {
  ArrowRight as ArrowRightIcon,
  CheckCircle2 as CheckIcon,
  CirclePause as PauseIcon,
  CirclePlay as PlayIcon,
  Copy as CopyIcon,
  Fingerprint as FingerprintIcon,
  PackageCheck as PackageIcon,
  RefreshCw as RefreshIcon,
  RotateCcw as RetryIcon,
  ShieldCheck as ShieldIcon,
  TriangleAlert as WarningIcon,
} from "@lucide/vue";
import { dashboardKey, isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import { messageFromError } from "@/lib/errors";
import { DEFAULT_LOCAL_AGENT_BINARY_PATH, DEFAULT_LOCAL_INSTALLER_PATH, linuxManagedUpdaterBootstrapSnippet } from "@/lib/agentSetupSnippets";
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

const sections = [
  { key: "fleet", label: "Fleet", path: "/agent" },
  { key: "activity", label: "Activity", path: "/agent/activity" },
  { key: "updates", label: "Updates", path: "/agent/updates" },
] as const;

const overview = ref<GetAgentUpdateOverviewResponse | null>(null);
const campaigns = ref<AgentUpdateCampaign[]>([]);
const loading = ref(false);
const operationError = ref("");
const planOpen = ref(false);
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
const bootstrapAgent = ref<AgentUpdateOverviewAgent | null>(null);
const bootstrapToken = ref("");
const bootstrapRootBase64 = ref("");
const bootstrapRootSHA256 = ref("");
const bootstrapRootVersion = ref(0n);
const bootstrapRepository = ref("");
const bootstrapAuthorityPublicKeyBase64 = ref("");
const bootstrapAuthorityKeyId = ref("");
const bootstrapAuthorityEpoch = ref(0n);
const bootstrapExpiresAt = ref(0n);
const bootstrapCopied = ref(false);
const bootstrapTokenCopied = ref(false);
const bootstrapInstallerPath = ref(DEFAULT_LOCAL_INSTALLER_PATH);
const bootstrapAgentBinaryPath = ref(DEFAULT_LOCAL_AGENT_BINARY_PATH);

function bytesToBase64(value: Uint8Array): string {
  let binary = "";
  for (const byte of value) binary += String.fromCharCode(byte);
  return btoa(binary);
}

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
].join("\u0000"));
const previewIsCurrent = computed(() => Boolean(previewFingerprint.value) && previewFingerprint.value === currentPlanFingerprint.value);
const canCreate = computed(() => Boolean(
  trustedTarget.value && managementAuthority.value && planName.value.trim() && selectedAgentIds.value.length > 0 &&
  preview.value.length === selectedAgentIds.value.length && previewBlocked.value.length === 0 && previewIsCurrent.value,
));

async function selectSection(value: string | number) {
  const section = sections.find((candidate) => candidate.key === value);
  if (section && section.key !== "updates") await router.push(section.path);
}

async function refresh() {
  loading.value = true;
  operationError.value = "";
  try {
    const [nextOverview, campaignList] = await Promise.all([
      managementClient.getAgentUpdateOverview({}),
      managementClient.listAgentUpdateCampaigns({ limit: 50n }),
    ]);
    overview.value = nextOverview;
    campaigns.value = campaignList.campaigns;
  } catch (error) {
    operationError.value = messageFromError(error);
  } finally {
    loading.value = false;
  }
}

function openPlan() {
  planName.value = trustedTarget.value ? `Fleet to ${trustedTarget.value.version}` : "Fleet update";
  selectedAgentIds.value = enrolledAgents.value.filter((agent) => agent.connected && !agent.activeAssignmentId).map((agent) => agent.agentId.toString());
  preview.value = [];
  previewFingerprint.value = "";
  planOpen.value = true;
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
  if (!trustedTarget.value || selectedAgentIds.value.length === 0) return;
  previewLoading.value = true;
  operationError.value = "";
  try {
    const response = await managementClient.previewAgentUpdateCampaign({
      agentIds: selectedAgentIds.value.map(BigInt),
      target: trustedTarget.value,
      policy: policyRequest(),
    });
    preview.value = response.agents;
    previewFingerprint.value = currentPlanFingerprint.value;
  } catch (error) {
    operationError.value = messageFromError(error);
  } finally {
    previewLoading.value = false;
  }
}

async function createPlan() {
  if (!canCreate.value || !trustedTarget.value) return;
  const target = trustedTarget.value;
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
  if (runManagementAction) await runManagementAction(action, "Update campaign started");
  else await action();
}

async function changeCampaign(campaign: AgentUpdateCampaign, action: "pause" | "resume" | "cancel") {
  const request = { campaignId: campaign.id, expectedGeneration: campaign.generation };
  const execute = async () => {
    if (action === "pause") await managementClient.pauseAgentUpdateCampaign(request);
    else if (action === "resume") await managementClient.resumeAgentUpdateCampaign(request);
    else await managementClient.cancelAgentUpdateCampaign(request);
    await refresh();
  };
  if (runManagementAction) await runManagementAction(execute, `Campaign ${action}d`);
  else await execute();
}

async function retryFailed(campaign: AgentUpdateCampaign) {
  const failed = campaign.assignments.filter((assignment) => assignment.state === AgentUpdateAssignmentState.FAILED || assignment.state === AgentUpdateAssignmentState.BLOCKED);
  if (!failed.length) return;
  const execute = async () => {
    await managementClient.retryAgentUpdateAssignments({
      campaignId: campaign.id,
      assignmentIds: failed.map((assignment) => assignment.id),
      expectedCampaignGeneration: campaign.generation,
    });
    await refresh();
  };
  if (runManagementAction) await runManagementAction(execute, "Failed assignments queued again");
  else await execute();
}

function managementOrigin(): string {
  const configured = dashboard?.value?.managementSecurity?.defaultManagementUrl?.trim();
  if (configured) return configured.replace(/\/+$/, "");
  const url = new URL(window.location.origin);
  if (url.port === "5173") url.port = "8081";
  if (url.protocol !== "https:") url.protocol = "https:";
  return url.toString().replace(/\/$/, "");
}

const bootstrapCommand = computed(() => {
  if (!bootstrapAgent.value || !bootstrapAgent.value.tunnelVersion || !bootstrapAgent.value.tunnelCommit || !bootstrapToken.value || !bootstrapRootBase64.value || !bootstrapAuthorityPublicKeyBase64.value || !bootstrapAuthorityKeyId.value || bootstrapAuthorityEpoch.value <= 0n) return "";
  try {
    return linuxManagedUpdaterBootstrapSnippet({
      managementUrl: managementOrigin(),
      agentId: bootstrapAgent.value.agentPublicId,
      updaterEnrollmentToken: bootstrapToken.value,
      agentUpdateRootBase64: bootstrapRootBase64.value,
      agentUpdateAuthorityPublicKeyBase64: bootstrapAuthorityPublicKeyBase64.value,
      agentUpdateAuthorityKeyId: bootstrapAuthorityKeyId.value,
      agentUpdateAuthorityEpoch: bootstrapAuthorityEpoch.value,
      currentTunnelVersion: bootstrapAgent.value.tunnelVersion,
      currentTunnelCommit: bootstrapAgent.value.tunnelCommit,
      repository: bootstrapRepository.value,
      version: trustedTarget.value?.version,
      installerPath: bootstrapInstallerPath.value,
      agentBinaryPath: bootstrapAgentBinaryPath.value,
    });
  } catch (error) {
    operationError.value = messageFromError(error);
    return "";
  }
});
const bootstrapReadyToClose = computed(() => bootstrapCopied.value && bootstrapTokenCopied.value);

async function openBootstrap(agent: AgentUpdateOverviewAgent) {
  operationError.value = "";
  try {
    const response = await managementClient.generateAgentUpdaterEnrollmentToken({
      agentId: agent.agentId,
      ttlMillis: 10n * 60n * 1000n,
    });
    bootstrapAgent.value = agent;
    bootstrapToken.value = response.token;
    bootstrapRootBase64.value = response.trustedRootMetadataBase64;
    bootstrapRootSHA256.value = response.trustedRootSha256;
    bootstrapRootVersion.value = response.trustedRootVersion;
    bootstrapRepository.value = response.pinnedRepository;
    bootstrapAuthorityPublicKeyBase64.value = bytesToBase64(response.managementAuthority?.publicKey ?? new Uint8Array());
    bootstrapAuthorityKeyId.value = response.managementAuthority?.keyId ?? "";
    bootstrapAuthorityEpoch.value = response.managementAuthority?.epoch ?? 0n;
    bootstrapExpiresAt.value = response.expiresAtUnixMillis;
    bootstrapCopied.value = false;
    bootstrapTokenCopied.value = false;
    bootstrapInstallerPath.value = DEFAULT_LOCAL_INSTALLER_PATH;
    bootstrapAgentBinaryPath.value = DEFAULT_LOCAL_AGENT_BINARY_PATH;
  } catch (error) {
    operationError.value = messageFromError(error);
  }
}

function closeBootstrap() {
  bootstrapAgent.value = null;
  bootstrapToken.value = "";
  bootstrapRootBase64.value = "";
  bootstrapRootSHA256.value = "";
  bootstrapRootVersion.value = 0n;
  bootstrapRepository.value = "";
  bootstrapAuthorityPublicKeyBase64.value = "";
  bootstrapAuthorityKeyId.value = "";
  bootstrapAuthorityEpoch.value = 0n;
  bootstrapExpiresAt.value = 0n;
  bootstrapCopied.value = false;
  bootstrapTokenCopied.value = false;
}

async function copyBootstrap() {
  if (!bootstrapCommand.value) return;
  try {
    await navigator.clipboard.writeText(bootstrapCommand.value);
    bootstrapCopied.value = true;
  } catch (error) {
    operationError.value = messageFromError(error);
  }
}

async function copyBootstrapToken() {
  if (!bootstrapToken.value) return;
  try {
    await navigator.clipboard.writeText(bootstrapToken.value);
    bootstrapTokenCopied.value = true;
  } catch (error) {
    operationError.value = messageFromError(error);
  }
}

function formatExpiry(value: bigint): string {
  if (!value) return "unknown";
  return new Date(Number(value)).toLocaleString();
}

function toggleAgent(agent: AgentUpdateOverviewAgent, checked: boolean) {
  const id = agent.agentId.toString();
  selectedAgentIds.value = checked
    ? Array.from(new Set([...selectedAgentIds.value, id]))
    : selectedAgentIds.value.filter((candidate) => candidate !== id);
  preview.value = [];
  previewFingerprint.value = "";
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

function assignmentStateLabel(state: AgentUpdateAssignmentState): string {
  return enumLabel(AgentUpdateAssignmentState[state] ?? "unknown");
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

onMounted(refresh);
</script>

<template>
  <div class="agent-updates stack-xl">
    <header class="agent-updates__header">
      <div>
        <div class="agent-updates__eyebrow"><ShieldIcon aria-hidden="true" /> Release control</div>
        <h3 class="copy-xl weight-bold">Agent Updates</h3>
        <p class="muted-text copy-sm">Signed desired state, route-aware rollout waves, and verified recovery.</p>
      </div>
      <div class="agent-updates__actions">
        <NButton secondary size="small" :loading="loading" :disabled="isBusy" @click="refresh">
          <template #icon><RefreshIcon class="icon-sm" /></template>
          Refresh catalog
        </NButton>
        <NButton type="primary" size="small" :disabled="!trustedTarget || !managementAuthority || !enrolledAgents.length || isBusy" @click="openPlan">
          Plan rollout
          <template #icon><ArrowRightIcon class="icon-sm" /></template>
        </NButton>
      </div>
    </header>

    <NTabs class="agent-updates__tabs" type="line" value="updates" @update:value="selectSection">
      <NTab v-for="section in sections" :key="section.key" :name="section.key" :tab="section.label" />
    </NTabs>

    <NAlert v-if="operationError" type="error" :bordered="false" closable @close="operationError = ''">
      {{ operationError }}
    </NAlert>
    <NAlert v-if="managementAuthorityWarning" type="warning" :bordered="false">
      Managed update control is fail-closed: {{ managementAuthorityWarning }}. Public proxy traffic is unaffected.
    </NAlert>

    <NSpin :show="loading && !overview">

    <section class="agent-release-deck" aria-labelledby="release-deck-title">
      <div class="agent-release-deck__signal" aria-hidden="true">
        <span /><span /><span /><span />
      </div>
      <div class="agent-release-deck__identity">
        <div class="agent-release-deck__icon"><PackageIcon aria-hidden="true" /></div>
        <div>
          <p class="stat-label">Management release</p>
          <h4 id="release-deck-title">{{ trustedTarget?.version || serverVersion }}</h4>
          <p>{{ trustedTarget ? `Signed stable target · sequence ${trustedTarget.releaseSequence}` : "No trusted stable target is currently available." }}</p>
        </div>
      </div>
      <div class="agent-release-deck__integrity">
        <FingerprintIcon aria-hidden="true" />
        <div>
          <span>Release identity</span>
          <strong class="mono-text">{{ shortDigest(selectedTargetDigest) }}</strong>
        </div>
        <NTag size="small" :bordered="false" :type="trustedTarget ? 'success' : 'warning'">
          {{ trustedTarget ? "Verified" : "Unavailable" }}
        </NTag>
      </div>
      <div class="agent-release-deck__integrity">
        <ShieldIcon aria-hidden="true" />
        <div>
          <span>Command authority</span>
          <strong class="mono-text">{{ managementAuthority ? shortDigest(managementAuthority.keyId) : "unavailable" }}</strong>
          <small v-if="managementAuthority">epoch {{ managementAuthority.epoch }}</small>
        </div>
        <NTag size="small" :bordered="false" :type="managementAuthority ? 'success' : 'warning'">
          {{ managementAuthority ? "Pinned" : "Unavailable" }}
        </NTag>
      </div>
    </section>

    <section class="agent-update-metrics" aria-label="Fleet update readiness">
      <article>
        <span>Managed hosts</span>
        <strong>{{ enrolledAgents.length }}/{{ agents.length }}</strong>
        <small>{{ readinessPercent }}% have a pinned rescue updater</small>
      </article>
      <article>
        <span>Online managed</span>
        <strong>{{ onlineManagedAgents }}</strong>
        <small>connected hosts with a recent updater check-in</small>
      </article>
      <article>
        <span>Bootstrap required</span>
        <strong>{{ unsupportedAgents }}</strong>
        <small>need one-time host enrollment; no token rotation</small>
      </article>
      <article>
        <span>Unavailable budget</span>
        <strong>{{ activeCampaigns.length }}</strong>
        <small>running or paused campaigns</small>
      </article>
    </section>

    <div class="agent-update-grid">
      <section class="surface-card agent-rollout-rail">
        <div class="surface-card__header">
          <div>
            <p class="stat-label">Rollout contract</p>
            <h4>Safety gates before activation</h4>
          </div>
          <NTag size="small" :bordered="false" type="default">Desired state</NTag>
        </div>
        <div class="agent-rollout-rail__steps">
          <div class="agent-rollout-step agent-rollout-step--ready">
            <span><CheckIcon aria-hidden="true" /></span>
            <div><strong>Verify</strong><small>signer, digest, compatibility</small></div>
          </div>
          <div class="agent-rollout-step">
            <span>02</span>
            <div><strong>Stage</strong><small>download while traffic remains live</small></div>
          </div>
          <div class="agent-rollout-step">
            <span>03</span>
            <div><strong>Drain</strong><small>cordon and preserve route quorum</small></div>
          </div>
          <div class="agent-rollout-step">
            <span>04</span>
            <div><strong>Prove</strong><small>fresh tunnel and healthy dwell</small></div>
          </div>
        </div>
      </section>

      <section class="surface-card agent-update-policy">
        <div class="surface-card__header">
          <div>
            <p class="stat-label">Fleet policy</p>
            <h4>{{ trustedTarget && managementAuthority ? "Route-aware activation ready" : "Update control is fail-closed" }}</h4>
          </div>
          <NTag size="small" :bordered="false" :type="trustedTarget && managementAuthority ? 'success' : 'warning'">{{ trustedTarget && managementAuthority ? "Enforced" : "Unavailable" }}</NTag>
        </div>
        <div class="agent-update-policy__body">
          <div class="agent-update-policy__notice">
            <WarningIcon aria-hidden="true" />
            <p>Activation never proceeds unless the route retains its configured eligible-agent quorum, the root helper attests the exact artifact, and the new tunnel survives its health dwell.</p>
          </div>
          <dl>
            <div><dt>Target</dt><dd>{{ trustedTarget?.version || "No trusted release" }}</dd></div>
            <div><dt>Canary</dt><dd>1 agent · 2 minute dwell</dd></div>
            <div><dt>Wave</dt><dd>Explicit bounded size · route-aware</dd></div>
            <div><dt>Force</dt><dd>Never automatic</dd></div>
          </dl>
        </div>
      </section>
    </div>

    <section class="surface-card agent-update-fleet">
      <div class="surface-card__header">
        <div>
          <p class="stat-label">Host trust</p>
          <h4>Updater enrollment</h4>
        </div>
        <NTag size="small" :bordered="false">{{ enrolledAgents.length }}/{{ agents.length }} enrolled</NTag>
      </div>
      <div v-if="agents.length" class="agent-update-fleet__rows">
        <div v-for="agent in agents" :key="agent.agentPublicId" class="agent-update-fleet__row">
          <span class="agent-update-fleet__state" :class="{ 'agent-update-fleet__state--online': agent.connected }" aria-hidden="true" />
          <div class="agent-update-fleet__identity">
            <strong>{{ agent.name }}</strong>
            <small class="mono-text">{{ agent.agentPublicId }}</small>
          </div>
          <div><span>Pinned rescue</span><strong>{{ agent.updaterEnrolled ? (agent.updaterVersion || "enrolled") : "not enrolled" }}</strong><small v-if="agent.updaterEnrolled">{{ updaterLastSeenLabel(agent) }}</small></div>
          <div><span>Live tunnel</span><strong>{{ agent.tunnelVersion || "unreported" }}</strong><small v-if="agent.tunnelCommit" class="mono-text">{{ shortDigest(agent.tunnelCommit) }}</small></div>
          <div><span>Traffic</span><strong>{{ agent.cordoned ? "cordoned" : "eligible" }}</strong></div>
          <NTag v-if="agent.updaterEnrolled" size="small" :bordered="false" :type="updaterIsFresh(agent) ? 'success' : 'warning'">{{ updaterIsFresh(agent) ? "Managed" : "Worker stale" }}</NTag>
          <NButton v-else secondary size="tiny" :disabled="isBusy || !agent.tunnelVersion || !agent.tunnelCommit" @click="openBootstrap(agent)">Bootstrap</NButton>
        </div>
      </div>
      <div v-else class="agent-update-empty">No agents are registered.</div>
    </section>

    <section class="surface-card agent-update-campaigns">
      <div class="surface-card__header">
        <div>
          <p class="stat-label">Execution history</p>
          <h4>Update campaigns</h4>
        </div>
      </div>
      <div v-if="campaigns.length" class="agent-update-campaigns__rows">
        <article v-for="campaign in campaigns" :key="campaign.id.toString()" class="agent-update-campaign">
          <div class="agent-update-campaign__summary">
            <div>
              <NTag size="small" :bordered="false" :type="campaignTag(campaign.state)">{{ campaignLabel(campaign.state) }}</NTag>
              <strong>{{ campaign.name }}</strong>
              <small>{{ campaign.target?.version }} · generation {{ campaign.generation }}</small>
            </div>
            <div class="agent-update-campaign__metric"><span>Proven healthy</span><strong>{{ assignmentProgress(campaign) }}</strong></div>
            <div class="agent-update-campaign__metric"><span>Wave / unavailable</span><strong>{{ campaign.policy?.waveSize || 0n }} / {{ campaign.policy?.maxUnavailable || 0n }}</strong></div>
            <div class="agent-update-campaign__actions">
              <NButton v-if="campaign.state === AgentUpdateCampaignState.RUNNING" quaternary size="tiny" :disabled="isBusy" @click="changeCampaign(campaign, 'pause')"><template #icon><PauseIcon /></template>Pause</NButton>
              <NButton v-if="campaign.state === AgentUpdateCampaignState.PAUSED" quaternary size="tiny" :disabled="isBusy" @click="changeCampaign(campaign, 'resume')"><template #icon><PlayIcon /></template>Resume</NButton>
              <NButton v-if="campaign.assignments.some((a) => a.state === AgentUpdateAssignmentState.FAILED || a.state === AgentUpdateAssignmentState.BLOCKED)" quaternary size="tiny" :disabled="isBusy" @click="retryFailed(campaign)"><template #icon><RetryIcon /></template>Retry</NButton>
              <NButton v-if="campaign.state === AgentUpdateCampaignState.RUNNING || campaign.state === AgentUpdateCampaignState.PAUSED" quaternary size="tiny" type="error" :disabled="isBusy" @click="changeCampaign(campaign, 'cancel')">Cancel</NButton>
            </div>
          </div>
          <details class="agent-update-campaign__details">
            <summary>Inspect {{ campaign.assignments.length }} agent assignment{{ campaign.assignments.length === 1 ? "" : "s" }}</summary>
            <div class="agent-update-assignment-list">
              <div v-for="assignment in campaign.assignments" :key="assignment.id.toString()" class="agent-update-assignment">
                <div class="agent-update-assignment__identity">
                  <span class="agent-update-fleet__state" :class="{ 'agent-update-fleet__state--online': assignment.state === AgentUpdateAssignmentState.SUCCEEDED }" aria-hidden="true" />
                  <div>
                    <strong>{{ assignment.agentName || assignment.agentPublicId }}</strong>
                    <small class="mono-text">{{ assignment.agentPublicId }}</small>
                  </div>
                </div>
                <div>
                  <span>State</span>
                  <NTag size="small" :bordered="false" :type="assignmentTag(assignment.state)">{{ assignmentStateLabel(assignment.state) }}</NTag>
                </div>
                <div><span>Desired action</span><strong>{{ assignmentActionLabel(assignment.desiredAction) }}</strong></div>
                <div class="agent-update-assignment__evidence"><span>Evidence</span><strong>{{ assignmentEvidence(assignment) }}</strong></div>
                <div><span>Updated</span><strong>{{ formatTimestamp(assignment.updatedAtUnixMillis) }}</strong></div>
              </div>
            </div>
          </details>
        </article>
      </div>
      <div v-else class="agent-update-empty">No managed update campaigns yet.</div>
    </section>
    </NSpin>

    <NModal v-model:show="planOpen" preset="card" title="Plan Agent Rollout" class="agent-update-modal" :style="{ width: 'min(760px, calc(100vw - 2rem))' }">
      <div class="stack-lg">
        <NAlert type="info" :bordered="false">Preview is authoritative: the server re-evaluates route quorum, connectivity, enrollment, and active assignments before creating the campaign.</NAlert>
        <label class="agent-update-field"><span>Campaign name</span><NInput v-model:value="planName" maxlength="128" /></label>
        <div class="agent-update-form-grid">
          <label class="agent-update-field"><span>Max unavailable</span><NInputNumber v-model:value="maxUnavailable" :min="1" :max="100" /></label>
          <label class="agent-update-field"><span>Other agents required per route</span><NInputNumber v-model:value="minimumEligiblePerRoute" :min="1" :max="100" /></label>
          <label class="agent-update-field"><span>Canary agents</span><NInputNumber v-model:value="canaryCount" :min="1" :max="100" /></label>
          <label class="agent-update-field"><span>Wave size</span><NInputNumber v-model:value="waveSize" :min="1" :max="100" /></label>
          <label class="agent-update-field"><span>Healthy dwell seconds</span><NInputNumber v-model:value="healthyDwellSeconds" :min="10" :max="86400" /></label>
        </div>
        <div class="agent-update-agent-picker">
          <p class="stat-label">Agents</p>
          <label v-for="agent in agents" :key="agent.agentPublicId" :class="{ 'agent-update-agent-picker__blocked': !agent.updaterEnrolled || !agent.connected || Boolean(agent.activeAssignmentId) }">
            <NCheckbox
              :checked="selectedAgentIds.includes(agent.agentId.toString())"
              :disabled="!agent.updaterEnrolled || !agent.connected || Boolean(agent.activeAssignmentId)"
              @update:checked="toggleAgent(agent, $event)"
            />
            <span><strong>{{ agent.name }}</strong><small>{{ !agent.updaterEnrolled ? "Updater not enrolled" : !agent.connected ? "Disconnected" : agent.activeAssignmentId ? "Already assigned" : "Ready for preview" }}</small></span>
          </label>
        </div>
        <div v-if="preview.length" class="agent-update-preview">
          <div v-for="agent in preview" :key="agent.agentPublicId" :class="{ 'agent-update-preview--blocked': !agent.eligible }">
            <CheckIcon v-if="agent.eligible" /><WarningIcon v-else />
            <span><strong>{{ agent.name }}</strong><small>{{ agent.eligible ? "Eligible" : agent.blockers.join(", ") }}</small></span>
          </div>
        </div>
        <NAlert v-if="preview.length && !previewIsCurrent" type="warning" :bordered="false">
          Rollout settings changed after the last safety preview. Preview again before starting.
        </NAlert>
        <div class="agent-update-modal__actions">
          <NButton @click="planOpen = false">Cancel</NButton>
          <NButton secondary :loading="previewLoading" :disabled="!trustedTarget || !selectedAgentIds.length" @click="previewPlan">Preview safety</NButton>
          <NButton type="primary" :disabled="!canCreate || isBusy" @click="createPlan">Start campaign</NButton>
        </div>
      </div>
    </NModal>

    <NModal
      :show="Boolean(bootstrapAgent)"
      preset="card"
      title="Bootstrap Managed Updates"
      :style="{ width: 'min(760px, calc(100vw - 2rem))' }"
      :mask-closable="bootstrapReadyToClose"
      :close-on-esc="bootstrapReadyToClose"
      @update:show="(show) => { if (!show && bootstrapReadyToClose) closeBootstrap(); }"
    >
      <div v-if="bootstrapAgent" class="stack-lg">
        <NAlert :type="bootstrapReadyToClose ? 'success' : 'warning'" :bordered="false">
          {{ bootstrapReadyToClose ? "Command and one-time token copied. Paste the token only into the command's hidden prompt." : "Copy the command and enrollment token separately before closing. The command contains no secret and never rotates the tunnel token." }}
        </NAlert>
		<NAlert type="info" :bordered="false">
		  The pinned file installs the rescue updater only. The live tunnel remains on {{ bootstrapAgent.tunnelVersion }} ({{ shortDigest(bootstrapAgent.tunnelCommit) }}) until a drained campaign activates a signed target.
		</NAlert>
        <div class="agent-bootstrap-identity">
          <div><span>Agent</span><strong>{{ bootstrapAgent.name }}</strong><small class="mono-text">{{ bootstrapAgent.agentPublicId }}</small></div>
          <div><span>Pinned root</span><strong>v{{ bootstrapRootVersion }}</strong><small class="mono-text">{{ shortDigest(bootstrapRootSHA256) }}</small></div>
          <div><span>Management authority</span><strong>epoch {{ bootstrapAuthorityEpoch }}</strong><small class="mono-text">{{ shortDigest(bootstrapAuthorityKeyId) }}</small></div>
          <div><span>Expires</span><strong>{{ formatExpiry(bootstrapExpiresAt) }}</strong><small>{{ bootstrapRepository }}</small></div>
        </div>
        <label class="agent-update-field">
          <span>One-time updater enrollment token</span>
          <code class="agent-bootstrap-token">{{ bootstrapToken }}</code>
        </label>
        <div class="layout-grid space-md mq-md-cols-two">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Pinned installer file
            <NInput v-model:value="bootstrapInstallerPath" size="small" required />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
			Pinned rescue updater binary ({{ trustedTarget?.version }})
            <NInput v-model:value="bootstrapAgentBinaryPath" size="small" required />
          </label>
        </div>
        <p class="copy-xs muted-text">Download and verify both versioned files through a trusted channel before running this local-only command. Remote scripts are never piped into root.</p>
        <pre class="agent-bootstrap-command"><code>{{ bootstrapCommand }}</code></pre>
        <div class="agent-update-modal__actions">
          <NButton :disabled="!bootstrapReadyToClose" @click="closeBootstrap">Done</NButton>
          <NButton secondary :disabled="!bootstrapToken" @click="copyBootstrapToken"><template #icon><CopyIcon /></template>{{ bootstrapTokenCopied ? "Token copied" : "Copy token" }}</NButton>
          <NButton type="primary" :disabled="!bootstrapCommand" @click="copyBootstrap"><template #icon><CopyIcon /></template>{{ bootstrapCopied ? "Copied" : "Copy bootstrap command" }}</NButton>
        </div>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.agent-updates {
  --update-cyan: color-mix(in srgb, var(--app-accent) 76%, #22d3ee);
}

.agent-updates__header,
.agent-updates__actions,
.agent-release-deck__identity,
.agent-release-deck__integrity,
.agent-rollout-step,
.agent-update-policy__notice {
  display: flex;
  align-items: center;
}

.agent-updates__header {
  justify-content: space-between;
  gap: 1.5rem;
}

.agent-updates__header h3 {
  margin: 0.25rem 0;
}

.agent-updates__eyebrow {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  color: var(--app-accent);
  font-family: var(--font-mono);
  font-size: 0.68rem;
  font-weight: 700;
  letter-spacing: 0.11em;
  text-transform: uppercase;
}

.agent-updates__eyebrow svg,
.agent-release-deck__integrity > svg,
.agent-update-policy__notice svg {
  width: 1rem;
  height: 1rem;
}

.agent-updates__actions { gap: 0.65rem; }
.agent-updates__tabs { margin-top: -0.6rem; }

.agent-release-deck {
  position: relative;
  display: grid;
  grid-template-columns: minmax(0, 1.25fr) repeat(2, minmax(15rem, 0.75fr));
  overflow: hidden;
  border: 1px solid color-mix(in srgb, var(--app-accent) 34%, var(--app-border));
  border-radius: 10px;
  background:
    linear-gradient(110deg, color-mix(in srgb, var(--app-accent-soft) 54%, var(--app-panel)) 0 58%, var(--app-panel) 58%),
    var(--app-panel);
  box-shadow: 0 18px 48px color-mix(in srgb, var(--app-accent) 8%, transparent);
}

.agent-release-deck__signal {
  position: absolute;
  inset: 0 auto 0 0;
  display: flex;
  width: 4px;
  flex-direction: column;
}

.agent-release-deck__signal span { flex: 1; background: var(--app-accent); }
.agent-release-deck__signal span:nth-child(2) { background: var(--update-cyan); }
.agent-release-deck__signal span:nth-child(3) { background: var(--app-warning); }
.agent-release-deck__signal span:nth-child(4) { background: var(--app-success); }

.agent-release-deck__identity {
  gap: 1rem;
  min-height: 9.5rem;
  padding: 1.5rem 1.75rem;
}

.agent-release-deck__icon {
  display: grid;
  width: 3.4rem;
  height: 3.4rem;
  flex: 0 0 auto;
  place-items: center;
  border: 1px solid color-mix(in srgb, var(--app-accent) 34%, transparent);
  border-radius: 10px;
  background: var(--app-accent-soft);
  color: var(--app-accent);
}

.agent-release-deck__icon svg { width: 1.65rem; height: 1.65rem; }
.agent-release-deck h4 { margin: 0.2rem 0; font-family: var(--font-mono); font-size: 1.45rem; }
.agent-release-deck p { margin: 0; color: var(--app-text-muted); font-size: 0.78rem; }

.agent-release-deck__integrity {
  gap: 0.8rem;
  border-left: 1px solid var(--app-border-subtle);
  padding: 1.5rem;
}

.agent-release-deck__integrity div { min-width: 0; flex: 1; }
.agent-release-deck__integrity span,
.agent-release-deck__integrity strong { display: block; }
.agent-release-deck__integrity span { color: var(--app-text-muted); font-size: 0.68rem; }
.agent-release-deck__integrity strong { margin-top: 0.15rem; font-size: 0.82rem; }
.agent-release-deck__integrity small { display: block; margin-top: 0.15rem; color: var(--app-text-muted); font-size: 0.62rem; }

.agent-update-metrics {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-panel);
}

.agent-update-metrics article { padding: 1rem 1.15rem; }
.agent-update-metrics article + article { border-left: 1px solid var(--app-border-subtle); }
.agent-update-metrics span,
.agent-update-metrics small { display: block; color: var(--app-text-muted); }
.agent-update-metrics span { font-size: 0.68rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.055em; }
.agent-update-metrics strong { display: block; margin: 0.2rem 0; font-family: var(--font-mono); font-size: 1.35rem; }
.agent-update-metrics small { font-size: 0.67rem; line-height: 1.35; }

.agent-update-grid { display: grid; grid-template-columns: minmax(0, 1.45fr) minmax(19rem, 0.75fr); gap: 1rem; }
.agent-rollout-rail h4,
.agent-update-policy h4 { margin: 0.15rem 0 0; }
.agent-rollout-rail__steps { display: grid; grid-template-columns: repeat(4, 1fr); padding: 1.25rem; }
.agent-rollout-step { position: relative; gap: 0.65rem; min-width: 0; padding-right: 1rem; }
.agent-rollout-step:not(:last-child)::after {
  position: absolute;
  top: 1rem;
  right: 0;
  width: calc(100% - 3.3rem);
  height: 1px;
  background: var(--app-border);
  content: "";
  transform: translateX(50%);
}
.agent-rollout-step > span {
  z-index: 1;
  display: grid;
  width: 2rem;
  height: 2rem;
  flex: 0 0 auto;
  place-items: center;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-panel);
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.66rem;
}
.agent-rollout-step--ready > span { border-color: color-mix(in srgb, var(--app-success) 40%, var(--app-border)); color: var(--app-success); }
.agent-rollout-step svg { width: 1rem; height: 1rem; }
.agent-rollout-step div { min-width: 0; }
.agent-rollout-step strong,
.agent-rollout-step small { display: block; }
.agent-rollout-step strong { font-size: 0.76rem; }
.agent-rollout-step small { margin-top: 0.15rem; color: var(--app-text-muted); font-size: 0.65rem; line-height: 1.35; }

.agent-update-policy__body { padding: 1rem 1.2rem 1.2rem; }
.agent-update-policy__notice { align-items: flex-start; gap: 0.65rem; border-left: 2px solid var(--app-warning); background: color-mix(in srgb, var(--app-warning) 7%, transparent); padding: 0.75rem; }
.agent-update-policy__notice svg { flex: 0 0 auto; color: var(--app-warning); }
.agent-update-policy__notice p { margin: 0; color: var(--app-text-muted); font-size: 0.72rem; line-height: 1.5; }
.agent-update-policy dl { margin: 1rem 0 0; }
.agent-update-policy dl div { display: flex; justify-content: space-between; gap: 1rem; border-top: 1px solid var(--app-border-subtle); padding: 0.65rem 0; }
.agent-update-policy dt { color: var(--app-text-muted); font-size: 0.7rem; }
.agent-update-policy dd { margin: 0; text-align: right; font-size: 0.7rem; font-weight: 600; }

.agent-update-fleet h4,
.agent-update-campaigns h4 { margin: 0.15rem 0 0; }
.agent-update-fleet__rows,
.agent-update-campaigns__rows { display: grid; }
.agent-update-fleet__row {
  display: grid;
  grid-template-columns: auto minmax(13rem, 1.4fr) minmax(8rem, 0.7fr) minmax(7rem, 0.5fr) auto;
  align-items: center;
  gap: 1rem;
  border-top: 1px solid var(--app-border-subtle);
  padding: 0.8rem 1.2rem;
}
.agent-update-fleet__state { width: 0.5rem; height: 0.5rem; border-radius: 50%; background: var(--app-text-muted); box-shadow: 0 0 0 3px color-mix(in srgb, var(--app-text-muted) 12%, transparent); }
.agent-update-fleet__state--online { background: var(--app-success); box-shadow: 0 0 0 3px color-mix(in srgb, var(--app-success) 14%, transparent); }
.agent-update-fleet__identity,
.agent-update-fleet__row > div,
.agent-update-campaign__summary > div { min-width: 0; }
.agent-update-fleet__row strong,
.agent-update-fleet__row span,
.agent-update-fleet__row small,
.agent-update-campaign strong,
.agent-update-campaign span,
.agent-update-campaign small { display: block; }
.agent-update-fleet__row > div > span,
.agent-update-campaign__metric span { color: var(--app-text-muted); font-size: 0.64rem; text-transform: uppercase; letter-spacing: 0.045em; }
.agent-update-fleet__row strong { overflow: hidden; font-size: 0.72rem; text-overflow: ellipsis; white-space: nowrap; }
.agent-update-fleet__identity small,
.agent-update-campaign small { overflow: hidden; margin-top: 0.12rem; color: var(--app-text-muted); font-size: 0.64rem; text-overflow: ellipsis; white-space: nowrap; }
.agent-update-campaign {
  border-top: 1px solid var(--app-border-subtle);
}
.agent-update-campaign__summary {
  display: grid;
  grid-template-columns: minmax(15rem, 1.4fr) minmax(7rem, 0.5fr) minmax(8rem, 0.6fr) auto;
  align-items: center;
  gap: 1rem;
  padding: 0.9rem 1.2rem;
}
.agent-update-campaign__summary > div:first-child { display: grid; grid-template-columns: auto 1fr; align-items: center; gap: 0.45rem; }
.agent-update-campaign__summary > div:first-child small { grid-column: 1 / -1; }
.agent-update-campaign__metric strong { margin-top: 0.15rem; font-family: var(--font-mono); font-size: 0.82rem; }
.agent-update-campaign__actions { display: flex; justify-content: flex-end; gap: 0.25rem; }
.agent-update-campaign__actions svg { width: 0.8rem; height: 0.8rem; }
.agent-update-campaign__details {
  border-top: 1px solid var(--app-border-subtle);
  background: color-mix(in srgb, var(--app-panel-muted) 62%, transparent);
}
.agent-update-campaign__details summary {
  width: fit-content;
  cursor: pointer;
  padding: 0.65rem 1.2rem;
  color: var(--app-accent);
  font-size: 0.66rem;
  font-weight: 650;
  list-style-position: inside;
}
.agent-update-assignment-list { display: grid; border-top: 1px solid var(--app-border-subtle); }
.agent-update-assignment {
  display: grid;
  grid-template-columns: minmax(13rem, 1.1fr) minmax(8rem, 0.7fr) minmax(7rem, 0.55fr) minmax(16rem, 1.5fr) minmax(10rem, 0.75fr);
  align-items: center;
  gap: 0.85rem;
  padding: 0.75rem 1.2rem;
}
.agent-update-assignment + .agent-update-assignment { border-top: 1px solid var(--app-border-subtle); }
.agent-update-assignment > div { min-width: 0; }
.agent-update-assignment > div > span,
.agent-update-assignment > div > strong { display: block; }
.agent-update-assignment > div > span { margin-bottom: 0.15rem; color: var(--app-text-muted); font-size: 0.59rem; letter-spacing: 0.045em; text-transform: uppercase; }
.agent-update-assignment > div > strong { overflow: hidden; font-size: 0.68rem; text-overflow: ellipsis; white-space: nowrap; }
.agent-update-assignment__identity { display: flex; align-items: center; gap: 0.65rem; }
.agent-update-assignment__identity > div { min-width: 0; }
.agent-update-assignment__identity strong,
.agent-update-assignment__identity small { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.agent-update-assignment__identity strong { font-size: 0.7rem; }
.agent-update-assignment__identity small { margin-top: 0.1rem; font-size: 0.6rem; }
.agent-update-assignment__evidence strong { white-space: normal; overflow-wrap: anywhere; }
.agent-update-empty { border-top: 1px solid var(--app-border-subtle); padding: 1.5rem; color: var(--app-text-muted); font-size: 0.72rem; text-align: center; }
.agent-update-field { display: grid; gap: 0.35rem; }
.agent-update-field > span { color: var(--app-text-muted); font-size: 0.68rem; font-weight: 600; }
.agent-update-form-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0.8rem; }
.agent-update-field :deep(.n-input-number) { width: 100%; }
.agent-update-agent-picker { overflow: hidden; border: 1px solid var(--app-border); border-radius: 7px; }
.agent-update-agent-picker > p { margin: 0; padding: 0.65rem 0.8rem; background: var(--app-panel-muted); }
.agent-update-agent-picker > label { display: flex; align-items: center; gap: 0.65rem; border-top: 1px solid var(--app-border-subtle); padding: 0.65rem 0.8rem; }
.agent-update-agent-picker > label > span { min-width: 0; }
.agent-update-agent-picker strong,
.agent-update-agent-picker small { display: block; }
.agent-update-agent-picker strong { font-size: 0.72rem; }
.agent-update-agent-picker small { color: var(--app-text-muted); font-size: 0.64rem; }
.agent-update-agent-picker__blocked { opacity: 0.55; }
.agent-update-preview { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.5rem; }
.agent-update-preview > div { display: flex; align-items: flex-start; gap: 0.5rem; border: 1px solid color-mix(in srgb, var(--app-success) 30%, var(--app-border)); border-radius: 6px; padding: 0.6rem; }
.agent-update-preview > div > svg { width: 0.9rem; flex: 0 0 auto; color: var(--app-success); }
.agent-update-preview > div > span,
.agent-update-preview strong,
.agent-update-preview small { display: block; min-width: 0; }
.agent-update-preview strong { font-size: 0.7rem; }
.agent-update-preview small { margin-top: 0.1rem; color: var(--app-text-muted); font-size: 0.62rem; overflow-wrap: anywhere; }
.agent-update-preview > .agent-update-preview--blocked { border-color: color-mix(in srgb, var(--app-warning) 34%, var(--app-border)); }
.agent-update-preview > .agent-update-preview--blocked > svg { color: var(--app-warning); }
.agent-update-modal__actions { display: flex; justify-content: flex-end; gap: 0.6rem; }
.agent-bootstrap-identity { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); border: 1px solid var(--app-border); border-radius: 7px; }
.agent-bootstrap-identity > div { min-width: 0; padding: 0.8rem; }
.agent-bootstrap-identity > div + div { border-left: 1px solid var(--app-border-subtle); }
.agent-bootstrap-identity span,
.agent-bootstrap-identity strong,
.agent-bootstrap-identity small { display: block; }
.agent-bootstrap-identity span { color: var(--app-text-muted); font-size: 0.63rem; text-transform: uppercase; letter-spacing: 0.05em; }
.agent-bootstrap-identity strong { overflow: hidden; margin-top: 0.15rem; font-size: 0.73rem; text-overflow: ellipsis; white-space: nowrap; }
.agent-bootstrap-identity small { overflow: hidden; margin-top: 0.12rem; color: var(--app-text-muted); font-size: 0.61rem; text-overflow: ellipsis; white-space: nowrap; }
.agent-bootstrap-command { overflow: auto; max-height: 17rem; margin: 0; border: 1px solid var(--app-border); border-radius: 7px; background: var(--app-panel-muted); padding: 1rem; color: var(--app-text); font-size: 0.68rem; line-height: 1.55; white-space: pre-wrap; overflow-wrap: anywhere; }
.agent-bootstrap-token { display: block; border: 1px solid var(--app-border); border-radius: 7px; background: var(--app-panel-muted); padding: 0.75rem; color: var(--app-text); font-size: 0.68rem; overflow-wrap: anywhere; }

@media (max-width: 900px) {
  .agent-release-deck,
  .agent-update-grid { grid-template-columns: 1fr; }
  .agent-release-deck__integrity { border-top: 1px solid var(--app-border-subtle); border-left: 0; }
  .agent-update-metrics { grid-template-columns: repeat(2, 1fr); }
  .agent-update-metrics article:nth-child(3) { border-top: 1px solid var(--app-border-subtle); border-left: 0; }
  .agent-update-metrics article:nth-child(4) { border-top: 1px solid var(--app-border-subtle); }
  .agent-rollout-rail__steps { grid-template-columns: repeat(2, 1fr); gap: 1.25rem; }
  .agent-rollout-step::after { display: none; }
  .agent-update-fleet__row { grid-template-columns: auto 1fr auto; }
  .agent-update-fleet__row > div:not(.agent-update-fleet__identity) { display: none; }
  .agent-update-campaign__summary { grid-template-columns: 1fr auto; }
  .agent-update-campaign__metric { display: none; }
  .agent-update-assignment { grid-template-columns: minmax(12rem, 1fr) minmax(8rem, auto) minmax(12rem, 1fr); }
  .agent-update-assignment > div:nth-child(3),
  .agent-update-assignment > div:nth-child(5) { display: none; }
  .agent-update-form-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}

@media (max-width: 640px) {
  .agent-updates__header { align-items: flex-start; flex-direction: column; }
  .agent-updates__actions { width: 100%; }
  .agent-updates__actions :deep(.n-button) { flex: 1; }
  .agent-update-metrics { grid-template-columns: 1fr; }
  .agent-update-metrics article + article { border-top: 1px solid var(--app-border-subtle); border-left: 0; }
  .agent-rollout-rail__steps { grid-template-columns: 1fr; }
  .agent-update-form-grid,
  .agent-update-preview { grid-template-columns: 1fr; }
  .agent-bootstrap-identity { grid-template-columns: 1fr; }
  .agent-bootstrap-identity > div + div { border-top: 1px solid var(--app-border-subtle); border-left: 0; }
  .agent-update-campaign__summary { align-items: flex-start; grid-template-columns: 1fr; }
  .agent-update-campaign__actions { justify-content: flex-start; }
  .agent-update-assignment { grid-template-columns: 1fr; }
  .agent-update-assignment > div:nth-child(3),
  .agent-update-assignment > div:nth-child(5) { display: block; }
  .agent-update-modal__actions { flex-wrap: wrap; }
}
</style>
