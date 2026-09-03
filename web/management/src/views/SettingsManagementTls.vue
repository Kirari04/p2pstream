<script setup lang="ts">
import { Copy as CopyIcon, RefreshCw as RefreshIcon, ShieldCheck as ShieldIcon, Upload as UploadIcon } from "@lucide/vue";
import { NAlert, NButton, NDataTable, NInput, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { computed, h, inject, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import { localManagementClient } from "@/api/managementClient";
import { selectedEnvironmentIdKey } from "@/composables/managementContextKeys";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import {
  ManagementTlsAgentRolloutState,
  ManagementTlsCleanupReason,
  ManagementTlsRotationPhase,
  type ManagementTlsAgentRollout,
  type ManagementTlsCertificateSummary,
  type ManagementTlsRotation,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { messageFromError } from "@/lib/errors";
import { DEFAULT_LOCAL_INSTALLER_PATH } from "@/lib/agentSetupSnippets";
import { diagnosticExcerpt } from "@/lib/diagnosticText";
import { managementCertificateExpiryWarning, managementTlsAgentDetail, summarizeManagementTlsFleet } from "@/lib/managementTlsPresentation";

const selectedEnvironmentId = inject(selectedEnvironmentIdKey, computed(() => "0"));
const confirmDialog = useConfirmDialog();
const rotation = ref<ManagementTlsRotation>();
const loading = ref(false);
const operation = ref("");
const operationError = ref("");
const pollError = ref("");
const lastUpdatedAt = ref(0);
const copied = ref(false);
const customMaterialOpen = ref(false);
const custom = reactive({ certificatePem: "", privateKeyPem: "", caBundlePem: "", currentCaBundlePem: "" });
let pollTimer: number | undefined;

const phase = computed(() => rotation.value?.phase ?? ManagementTlsRotationPhase.UNSPECIFIED);
const remoteSelected = computed(() => selectedEnvironmentId.value !== "0");
const canOperate = computed(() => Boolean(rotation.value?.managedRotationAvailable) && !loading.value && !remoteSelected.value);
const fleetSummary = computed(() => summarizeManagementTlsFleet(rotation.value?.agents ?? []));
const enabledAgentCount = computed(() => fleetSummary.value.enabled);
const rolloutParticipantCount = computed(() => fleetSummary.value.participants);
const readyAgentCount = computed(() => fleetSummary.value.readyParticipants);
const rolloutPercent = computed(() => fleetSummary.value.rolloutPercent);
const showFleet = computed(() => phase.value !== ManagementTlsRotationPhase.IDLE || Boolean(rotation.value?.trustManagementActive));
const cleanupTitle = computed(() => rotation.value?.cleanupReason === ManagementTlsCleanupReason.ROLLED_BACK_CERTIFICATE ? "Rollback trust cleanup" : "Cancelled rotation cleanup");
const activeExpiryWarning = computed(() => managementCertificateExpiryWarning(rotation.value?.activeCertificate?.notAfterUnixMillis ?? 0n));
const repairCommand = computed(() => {
  const pem = rotation.value?.repairCaBundlePem ?? "";
  if (!pem) return "";
  const encoded = btoa(pem);
  return `sudo env P2PSTREAM_REPAIR_TRUST=true MANAGEMENT_CA_PEM_BASE64='${encoded}' bash '${DEFAULT_LOCAL_INSTALLER_PATH}'`;
});

const agentColumns: DataTableColumns<ManagementTlsAgentRollout> = [
  {
    title: "Agent",
    key: "agent",
    minWidth: 180,
    render: (agent) => h("div", { class: "layout-grid space-xxs" }, [
      h("span", { class: "weight-medium" }, agent.agentName || agent.agentPublicId),
      h("code", { class: "copy-2xs muted-text" }, agent.agentPublicId),
    ]),
  },
  {
    title: "Connection",
    key: "connected",
    width: 120,
    render: (agent) => h(NTag, { size: "small", bordered: false, type: agent.connected ? "success" : "default" }, { default: () => agent.enabled ? (agent.connected ? "Online" : "Offline") : "Disabled" }),
  },
  {
    title: "Trust state",
    key: "state",
    width: 180,
    render: (agent) => h("div", { class: "layout-row align-center space-xs" }, [
      h(NTag, { size: "small", bordered: false, type: rolloutTagType(agent.state) }, { default: () => rolloutLabel(agent.state) }),
      agent.needsTrustAttention ? h(NTag, { size: "small", bordered: false, type: "warning" }, { default: () => "Attention" }) : null,
    ]),
  },
  {
    title: "Version",
    key: "version",
    width: 120,
    render: (agent) => h("code", { class: "copy-xs" }, agent.agentVersion ? diagnosticExcerpt(agent.agentVersion, 48).text : "Unknown"),
  },
  {
    title: "Last report",
    key: "reported",
    width: 170,
    render: (agent) => h("span", { class: "copy-xs muted-text" }, formatTime(agent.reportedAtUnixMillis)),
  },
  {
    title: "Detail",
    key: "detail",
    minWidth: 220,
    render: (agent) => h("span", { class: "copy-xs muted-text wrap-anywhere" }, agent.errorDetail ? diagnosticExcerpt(agent.errorDetail, 160).text : rolloutDetail(agent)),
  },
];

onMounted(() => {
  void refresh();
  pollTimer = window.setInterval(() => void refresh(true), 5000);
});

onBeforeUnmount(() => {
  if (pollTimer !== undefined) window.clearInterval(pollTimer);
  confirmDialog.handleCancel();
});

async function refresh(quiet = false) {
  if (loading.value && quiet) return;
  if (!quiet) loading.value = true;
  try {
    const response = await localManagementClient.getManagementTlsRotation({});
    rotation.value = response.rotation;
    lastUpdatedAt.value = Date.now();
    pollError.value = "";
    if (!quiet) operationError.value = "";
  } catch (error) {
    if (quiet) pollError.value = messageFromError(error);
    else operationError.value = messageFromError(error);
  } finally {
    if (!quiet) loading.value = false;
  }
}

async function runAction(name: string, action: () => Promise<{ rotation?: ManagementTlsRotation }>) {
  operation.value = name;
  loading.value = true;
  operationError.value = "";
  try {
    const response = await action();
    if (response.rotation) rotation.value = response.rotation;
  } catch (error) {
    operationError.value = messageFromError(error);
  } finally {
    loading.value = false;
    operation.value = "";
  }
}

async function regenerate() {
  if (!await confirmDialog.confirm("Generate a new management CA?", "A new private CA and server certificate will be staged. The active certificate does not change until every enabled compatible agent acknowledges durable trust, or you explicitly force activation.", "Generate and stage")) return;
  await runAction("generate", () => localManagementClient.generateManagementTlsRotation({}));
}

async function stageCustom() {
  await runAction("stage", () => localManagementClient.stageManagementTlsRotation(custom));
  if (!operationError.value) {
    custom.certificatePem = "";
    custom.privateKeyPem = "";
    custom.caBundlePem = "";
    custom.currentCaBundlePem = "";
    customMaterialOpen.value = false;
  }
}

async function activate(force: boolean) {
  const description = force
    ? `Force activation can strand ${rotation.value?.blockingAgentCount ?? 0} enabled agent(s). They will need the repair command or a full reinstall before reconnecting.`
    : "New TLS handshakes will begin using the staged certificate. The previous certificate and dual trust bundle remain available for rollback.";
  if (!await confirmDialog.confirm(force ? "Force certificate activation?" : "Activate staged certificate?", description, force ? "Force activation" : "Activate")) return;
  await runAction(force ? "force-activate" : "activate", () => localManagementClient.activateManagementTlsRotation({ force }));
}

async function cancelRotation() {
  if (!await confirmDialog.confirm("Cancel staged rotation?", "The active certificate remains unchanged. A cleanup rollout will remove the staged CA from every compatible agent that may have installed the dual bundle.", "Cancel and clean up")) return;
  await runAction("cancel", () => localManagementClient.cancelManagementTlsRotation({}));
}

async function rollback() {
  if (!await confirmDialog.confirm("Roll back the active certificate?", "The retained previous certificate becomes active immediately, then agents acknowledge removal of the abandoned CA. Rollback is unavailable after old trust is retired.", "Roll back and clean up")) return;
  await runAction("rollback", () => localManagementClient.rollbackManagementTlsRotation({}));
}

async function beginRetirement() {
  const leafOnly = (rotation.value?.rolloutGeneration ?? 0n) === 0n;
  if (!await confirmDialog.confirm(leafOnly ? "Finish leaf-certificate rotation?" : "Remove trust in the old CA?", leafOnly ? "The CA did not change, so no agent trust cleanup is required. This closes the rollback window." : "Agents will receive a new bundle containing only the active CA. The previous certificate remains available until every enabled agent acknowledges this cleanup.", leafOnly ? "Finish rotation" : "Begin retirement")) return;
  await runAction("retire", () => localManagementClient.beginManagementTlsTrustRetirement({}));
}

async function finalizeRetirement(force: boolean) {
  if (!await confirmDialog.confirm(force ? "Force trust retirement?" : "Finalize old trust retirement?", force ? "Agents that did not acknowledge will retain old trust. Review and repair them manually." : "The rollback certificate will be retired after all enabled agents have acknowledged the active-only bundle.", force ? "Force retirement" : "Finalize")) return;
  await runAction(force ? "force-finalize" : "finalize", () => localManagementClient.finalizeManagementTlsTrustRetirement({ force }));
}

async function finalizeCleanup(force: boolean) {
  const description = force
    ? "Compatible agents that have not acknowledged may continue trusting the abandoned CA. The server will keep offering the active-only repair bundle when they return."
    : "The cancelled or rolled-back certificate files are already retired. This closes cleanup after every participating agent has durably removed the abandoned CA.";
  if (!await confirmDialog.confirm(force ? "Force trust cleanup?" : "Finish trust cleanup?", description, force ? "Force cleanup" : "Finish cleanup")) return;
  await runAction(force ? "force-cleanup" : "cleanup", () => localManagementClient.finalizeManagementTlsTrustCleanup({ force }));
}

async function copyRepairCommand() {
  await navigator.clipboard.writeText(repairCommand.value);
  copied.value = true;
  window.setTimeout(() => { copied.value = false; }, 1800);
}

function rolloutLabel(state: ManagementTlsAgentRolloutState): string {
  return ({
    [ManagementTlsAgentRolloutState.DISABLED]: "Excluded",
    [ManagementTlsAgentRolloutState.INCOMPATIBLE]: "Incompatible",
    [ManagementTlsAgentRolloutState.PENDING]: "Pending",
    [ManagementTlsAgentRolloutState.READY]: "Ready",
    [ManagementTlsAgentRolloutState.FAILED]: "Failed",
    [ManagementTlsAgentRolloutState.STRANDED]: "Stranded",
  } as Record<number, string>)[state] ?? "Unknown";
}

function rolloutTagType(state: ManagementTlsAgentRolloutState): "default" | "success" | "warning" | "error" {
  if (state === ManagementTlsAgentRolloutState.READY) return "success";
  if (state === ManagementTlsAgentRolloutState.FAILED || state === ManagementTlsAgentRolloutState.STRANDED) return "error";
  if (state === ManagementTlsAgentRolloutState.PENDING || state === ManagementTlsAgentRolloutState.INCOMPATIBLE) return "warning";
  return "default";
}

function rolloutDetail(agent: ManagementTlsAgentRollout): string {
  return managementTlsAgentDetail(agent, phase.value);
}

function formatTime(value: bigint): string {
  if (value === 0n) return "Never";
  return new Date(Number(value)).toLocaleString();
}

function formatCertificateDate(value: bigint): string {
  return value === 0n ? "Unknown" : new Date(Number(value)).toLocaleString();
}

function shortDigest(value: string): string {
  return value ? `${value.slice(0, 12)}…${value.slice(-8)}` : "Unavailable";
}

function certificateNames(cert?: ManagementTlsCertificateSummary): string {
  if (!cert) return "None";
  return [...cert.dnsNames, ...cert.ipAddresses].join(", ") || "No SANs";
}
</script>

<template>
  <div class="stack-xl management-tls-page">
    <NAlert v-if="remoteSelected" type="warning" :bordered="false">
      Management TLS changes are intentionally local-only. Select the Local environment to operate on the certificate serving this UI.
    </NAlert>
    <NAlert v-if="operationError" type="error" :bordered="false">{{ operationError }}</NAlert>
    <NAlert v-if="pollError" type="warning" :bordered="false">Live refresh failed: {{ pollError }}. Showing the last confirmed status from {{ lastUpdatedAt ? new Date(lastUpdatedAt).toLocaleTimeString() : 'startup' }}.</NAlert>
    <NAlert v-if="rotation?.statusMessage" type="warning" :bordered="false">{{ rotation.statusMessage }}</NAlert>
    <NAlert v-if="activeExpiryWarning" type="error" :bordered="false">{{ activeExpiryWarning }}</NAlert>
    <NAlert v-if="rotation?.secretCleanupPending" type="warning" :bordered="false">
      Retired certificate material could not yet be removed from disk. Cleanup is retried automatically; check server filesystem permissions.
    </NAlert>
    <NAlert v-if="rotation?.forcedActivation" type="error" :bordered="false">
      Certificate activation was forced. {{ (rotation?.trustAttentionAgentCount ?? 0) > 0 ? 'Repair every stranded agent before retiring old trust.' : 'Every known agent has reconciled; old-trust retirement may now proceed.' }}
    </NAlert>
    <NAlert v-if="rotation?.forcedRetirement" type="warning" :bordered="false">
      Old-CA retirement was forced. {{ (rotation?.trustAttentionAgentCount ?? 0) > 0 ? 'Agents marked Attention have not confirmed the active-only trust bundle yet.' : 'Every known agent has since reconciled to the active trust bundle.' }}
    </NAlert>
    <NAlert v-if="rotation?.forcedCleanup" type="warning" :bordered="false">
      Cleanup was forced. {{ (rotation?.trustAttentionAgentCount ?? 0) > 0 ? 'Agents marked Attention may still trust the abandoned CA; they will be reconciled when they report again.' : 'Every known agent has since confirmed cleanup.' }}
    </NAlert>

    <section class="round-lg framed frame-standard panel-bg pad-xl stack-lg">
      <div class="layout-row layout-column space-md mq-md-row mq-md-spread mq-md-align-center">
        <div class="layout-row align-center space-md">
          <span class="tls-mark"><ShieldIcon class="icon-md" /></span>
          <div>
            <p class="copy-xs label-case letter-wide muted-text">Shared management endpoint</p>
            <h2 class="copy-lg weight-semibold">Certificate &amp; agent trust</h2>
          </div>
        </div>
        <div class="layout-row align-center space-sm">
          <NTag v-if="phase !== ManagementTlsRotationPhase.IDLE" type="info" :bordered="false">{{ phase === ManagementTlsRotationPhase.CLEANING_UP ? cleanupTitle : phase === ManagementTlsRotationPhase.DISTRIBUTING ? 'Distributing trust' : phase === ManagementTlsRotationPhase.ACTIVE ? 'Certificate active' : 'Retiring old trust' }}</NTag>
          <NTag :type="rotation?.tlsEnabled ? 'success' : 'error'" :bordered="false">{{ rotation?.tlsEnabled ? 'TLS enabled' : 'TLS disabled' }}</NTag>
          <NButton quaternary :loading="loading && !operation" aria-label="Refresh certificate status" @click="refresh()"><template #icon><RefreshIcon class="icon-sm" /></template></NButton>
        </div>
      </div>

      <div class="cert-grid">
        <article class="cert-card">
          <p class="copy-xs label-case letter-wide muted-text">Active · generation {{ rotation?.activeGeneration ?? 0n }}</p>
          <p class="copy-sm weight-semibold wrap-anywhere">{{ rotation?.activeCertificate?.subject || 'Unavailable' }}</p>
          <dl class="cert-facts">
            <div><dt>Expires</dt><dd>{{ formatCertificateDate(rotation?.activeCertificate?.notAfterUnixMillis ?? 0n) }}</dd></div>
            <div><dt>Issuer</dt><dd>{{ rotation?.activeCertificate?.issuer || 'Unavailable' }}</dd></div>
            <div><dt>Names</dt><dd>{{ certificateNames(rotation?.activeCertificate) }}</dd></div>
            <div><dt>SHA-256</dt><dd><code>{{ shortDigest(rotation?.activeCertificate?.sha256 ?? '') }}</code></dd></div>
          </dl>
        </article>
        <article class="cert-card cert-card--staged">
          <p class="copy-xs label-case letter-wide muted-text">Staged</p>
          <template v-if="rotation?.stagedCertificate">
            <p class="copy-sm weight-semibold wrap-anywhere">{{ rotation.stagedCertificate.subject }}</p>
            <dl class="cert-facts">
              <div><dt>Expires</dt><dd>{{ formatCertificateDate(rotation.stagedCertificate.notAfterUnixMillis) }}</dd></div>
              <div><dt>Issuer</dt><dd>{{ rotation.stagedCertificate.issuer || 'Unavailable' }}</dd></div>
              <div><dt>Names</dt><dd>{{ certificateNames(rotation.stagedCertificate) }}</dd></div>
              <div><dt>SHA-256</dt><dd><code>{{ shortDigest(rotation.stagedCertificate.sha256) }}</code></dd></div>
            </dl>
          </template>
          <p v-else class="copy-sm muted-text">No certificate is staged. The active certificate remains untouched until activation.</p>
        </article>
      </div>

      <div v-if="phase === ManagementTlsRotationPhase.IDLE" class="layout-row layout-column space-md mq-sm-row">
        <NButton type="primary" :disabled="!canOperate" :loading="operation === 'generate'" @click="regenerate"><template #icon><RefreshIcon class="icon-sm" /></template>Generate new certificate</NButton>
        <NButton secondary :disabled="!canOperate" @click="customMaterialOpen = !customMaterialOpen"><template #icon><UploadIcon class="icon-sm" /></template>Install custom certificate</NButton>
      </div>

      <form v-if="customMaterialOpen && phase === ManagementTlsRotationPhase.IDLE" class="custom-material stack-md" @submit.prevent="stageCustom">
        <NAlert type="info" :bordered="false">The private key is validated and stored only on this server. Agents receive the CA bundle and public certificate, never the private key.</NAlert>
        <label class="layout-grid space-xs copy-xs weight-medium muted-text">Server certificate chain (PEM)<NInput v-model:value="custom.certificatePem" type="textarea" :autosize="{ minRows: 5, maxRows: 12 }" :input-props="{ spellcheck: false, autocomplete: 'off' }" placeholder="-----BEGIN CERTIFICATE-----" /></label>
        <label class="layout-grid space-xs copy-xs weight-medium muted-text">Private key (PEM)<NInput v-model:value="custom.privateKeyPem" type="textarea" :autosize="{ minRows: 4, maxRows: 10 }" :input-props="{ spellcheck: false, autocomplete: 'off' }" placeholder="-----BEGIN PRIVATE KEY-----" /></label>
        <label class="layout-grid space-xs copy-xs weight-medium muted-text">Agent CA bundle (PEM)<NInput v-model:value="custom.caBundlePem" type="textarea" :autosize="{ minRows: 5, maxRows: 12 }" :input-props="{ spellcheck: false, autocomplete: 'off' }" placeholder="-----BEGIN CERTIFICATE-----" /></label>
        <label class="layout-grid space-xs copy-xs weight-medium muted-text">Current agent CA bundle (PEM, only if requested)<NInput v-model:value="custom.currentCaBundlePem" type="textarea" :autosize="{ minRows: 3, maxRows: 10 }" :input-props="{ spellcheck: false, autocomplete: 'off' }" placeholder="Needed only when the current provided certificate did not include its issuer chain" /><span class="copy-xs muted-text">This is verified against the active certificate before it can be added to the dual-trust rollout.</span></label>
        <div><NButton type="primary" attr-type="submit" :disabled="!canOperate || !custom.certificatePem || !custom.privateKeyPem || !custom.caBundlePem" :loading="operation === 'stage'">Validate and stage</NButton></div>
      </form>
    </section>

    <section v-if="showFleet" class="round-lg framed frame-standard panel-bg pad-xl stack-lg">
      <div class="layout-row layout-column space-md mq-md-row mq-md-spread mq-md-align-end">
        <div>
          <template v-if="phase === ManagementTlsRotationPhase.IDLE">
            <p class="copy-xs label-case letter-wide muted-text">Durable fleet trust · generation {{ rotation?.desiredTrustGeneration ?? 0n }}</p>
            <h3 class="copy-md weight-semibold">{{ enabledAgentCount }} enabled · {{ rotation?.trustAttentionAgentCount ?? 0 }} needing attention</h3>
            <p class="copy-xs muted-text">Idle health remains tracked after rotation, including disabled agents that may require repair before re-enabling.</p>
          </template>
          <template v-else>
            <p class="copy-xs label-case letter-wide muted-text">{{ phase === ManagementTlsRotationPhase.CLEANING_UP ? cleanupTitle : 'Fleet rollout' }} · generation {{ rotation?.rolloutGeneration ?? 0n }}</p>
            <h3 class="copy-md weight-semibold">{{ readyAgentCount }} of {{ rolloutParticipantCount }} participating agents ready</h3>
            <p class="copy-xs muted-text">{{ rolloutPercent }}% acknowledged after durable write, reload, and digest readback. Disabled agents are excluded; incompatible agents cannot have installed an abandoned bundle and do not block cleanup.</p>
          </template>
        </div>
        <NTag v-if="phase !== ManagementTlsRotationPhase.IDLE" :type="(rotation?.blockingAgentCount ?? 0) === 0 ? 'success' : 'warning'" :bordered="false">{{ rotation?.blockingAgentCount ?? 0 }} blocking</NTag>
        <NTag v-else :type="(rotation?.trustAttentionAgentCount ?? 0) === 0 ? 'success' : 'warning'" :bordered="false">{{ (rotation?.trustAttentionAgentCount ?? 0) === 0 ? 'Fleet trust current' : `${rotation?.trustAttentionAgentCount ?? 0} attention` }}</NTag>
      </div>

      <div v-if="phase !== ManagementTlsRotationPhase.IDLE" class="rollout-progress" role="progressbar" :aria-valuenow="rolloutPercent" aria-valuemin="0" aria-valuemax="100" :aria-label="`${rolloutPercent}% of trust rollout acknowledged`">
        <span :style="{ width: `${rolloutPercent}%` }" />
      </div>

      <NDataTable :columns="agentColumns" :data="rotation?.agents ?? []" :row-key="(agent: ManagementTlsAgentRollout) => agent.agentId.toString()" :pagination="false" :bordered="false" :single-line="false" :scroll-x="980" size="small" />

      <div v-if="phase !== ManagementTlsRotationPhase.IDLE" class="layout-row layout-column space-sm mq-sm-row">
        <template v-if="phase === ManagementTlsRotationPhase.DISTRIBUTING">
          <NButton type="primary" :disabled="!canOperate || (rotation?.blockingAgentCount ?? 0) > 0" :loading="operation === 'activate'" @click="activate(false)">Activate certificate</NButton>
          <NButton v-if="(rotation?.blockingAgentCount ?? 0) > 0" type="error" ghost :disabled="!canOperate" :loading="operation === 'force-activate'" @click="activate(true)">Force activation</NButton>
          <NButton secondary :disabled="!canOperate" :loading="operation === 'cancel'" @click="cancelRotation">Cancel</NButton>
        </template>
        <template v-else-if="phase === ManagementTlsRotationPhase.ACTIVE">
          <NButton type="primary" :disabled="!canOperate || (rotation?.blockingAgentCount ?? 0) > 0" :loading="operation === 'retire'" @click="beginRetirement">{{ (rotation?.rolloutGeneration ?? 0n) === 0n ? 'Finish rotation' : 'Retire old trust' }}</NButton>
          <NButton secondary :disabled="!canOperate" :loading="operation === 'rollback'" @click="rollback">Roll back certificate</NButton>
        </template>
        <template v-else-if="phase === ManagementTlsRotationPhase.RETIRING">
          <NButton type="primary" :disabled="!canOperate || (rotation?.blockingAgentCount ?? 0) > 0" :loading="operation === 'finalize'" @click="finalizeRetirement(false)">Finalize retirement</NButton>
          <NButton v-if="(rotation?.blockingAgentCount ?? 0) > 0" type="error" ghost :disabled="!canOperate" :loading="operation === 'force-finalize'" @click="finalizeRetirement(true)">Force retirement</NButton>
        </template>
        <template v-else-if="phase === ManagementTlsRotationPhase.CLEANING_UP">
          <NButton type="primary" :disabled="!canOperate || (rotation?.blockingAgentCount ?? 0) > 0" :loading="operation === 'cleanup'" @click="finalizeCleanup(false)">Finish cleanup</NButton>
          <NButton v-if="(rotation?.blockingAgentCount ?? 0) > 0" type="error" ghost :disabled="!canOperate" :loading="operation === 'force-cleanup'" @click="finalizeCleanup(true)">Force cleanup</NButton>
        </template>
      </div>
    </section>

    <section v-if="repairCommand" class="round-lg framed frame-standard muted-bg pad-xl stack-md">
      <div>
        <p class="copy-xs label-case letter-wide muted-text">Recovery</p>
        <h3 class="copy-md weight-semibold">Repair an agent that cannot reconnect</h3>
        <p class="copy-sm muted-text">Run this on a current compatible installation to restore management trust. For an incompatible or damaged agent, rerun its full install command from the Agents page so the identity and token are installed again.</p>
      </div>
      <code class="repair-command">{{ repairCommand }}</code>
      <div><NButton secondary @click="copyRepairCommand"><template #icon><CopyIcon class="icon-sm" /></template>{{ copied ? 'Copied' : 'Copy repair command' }}</NButton></div>
    </section>
  </div>
</template>

<style scoped>
.management-tls-page { gap: 1.25rem; }
.tls-mark { display: inline-flex; align-items: center; justify-content: center; width: 2.5rem; height: 2.5rem; border-radius: 0.75rem; color: var(--app-accent); background: var(--app-accent-soft); }
.cert-grid { display: grid; gap: 0.875rem; grid-template-columns: repeat(2, minmax(0, 1fr)); }
.cert-card { padding: 1rem; border: 1px solid var(--app-border); border-radius: 0.75rem; background: var(--app-panel-muted); display: grid; gap: 0.75rem; min-width: 0; }
.cert-card--staged { border-style: dashed; }
.cert-facts { display: grid; gap: 0.45rem; margin: 0; }
.cert-facts div { display: grid; grid-template-columns: 4.5rem minmax(0, 1fr); gap: 0.75rem; font-size: 0.75rem; }
.cert-facts dt { color: var(--app-text-muted); }
.cert-facts dd { margin: 0; overflow-wrap: anywhere; }
.custom-material { padding-top: 1rem; border-top: 1px solid var(--app-border); }
.rollout-progress { height: 0.42rem; overflow: hidden; border-radius: 999px; background: var(--app-border); }
.rollout-progress span { display: block; height: 100%; border-radius: inherit; background: var(--app-accent); transition: width 280ms ease; }
.repair-command { display: block; max-height: 9rem; overflow: auto; padding: 0.875rem; border: 1px solid var(--app-border); border-radius: 0.625rem; background: var(--app-panel); font-size: 0.72rem; line-height: 1.55; overflow-wrap: anywhere; user-select: all; }
@media (max-width: 720px) { .cert-grid { grid-template-columns: 1fr; } }
</style>
