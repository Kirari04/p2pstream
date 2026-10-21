<script setup lang="ts">
import { ArrowRight as ArrowRightIcon, Copy as CopyIcon, RefreshCw as RefreshIcon, ShieldCheck as ShieldIcon } from "@lucide/vue";
import { NButton, NTag } from "naive-ui";
import { computed, ref } from "vue";
import { useRouter } from "vue-router";
import ManagementTlsAgentTable from "@/components/management-tls/ManagementTlsAgentTable.vue";
import ManagementTlsAlerts from "@/components/management-tls/ManagementTlsAlerts.vue";
import ManagementTlsCertificateCard from "@/components/management-tls/ManagementTlsCertificateCard.vue";
import PageHeader from "@/components/ui/PageHeader.vue";
import { useManagementTlsRotation } from "@/composables/useManagementTlsRotation";
import { ManagementTlsRotationPhase } from "@/gen/proto/p2pstream/v1/management_pb";
import { DEFAULT_LOCAL_INSTALLER_PATH } from "@/lib/agentSetupSnippets";
import { managementTlsPhaseLabel, managementTlsRotationStep, managementTlsRotationSteps } from "@/lib/managementTlsPresentation";

const router = useRouter();
const { rotation, loading, operation, operationError, pollError, lastUpdatedAt, phase, remoteSelected, fleetSummary, blockingAgentCount, cleanupTitle, refresh } = useManagementTlsRotation();
const copied = ref(false);
const copyError = ref("");
const showFullRepairCommand = ref(false);

const rotationInProgress = computed(() => phase.value !== ManagementTlsRotationPhase.IDLE && phase.value !== ManagementTlsRotationPhase.UNSPECIFIED);
const phaseLabel = computed(() => managementTlsPhaseLabel(phase.value, cleanupTitle.value));
const rotationStep = computed(() => managementTlsRotationStep(phase.value));
const showFleet = computed(() => !rotationInProgress.value && Boolean(rotation.value?.trustManagementActive));
const repairCommand = computed(() => {
  const pem = rotation.value?.repairCaBundlePem ?? "";
  if (!pem) return "";
  return repairCommandFor(btoa(pem));
});
const repairCommandPreview = computed(() => {
  const pem = rotation.value?.repairCaBundlePem ?? "";
  if (!pem) return "";
  return repairCommandFor(`${btoa(pem).slice(0, 16)}…`);
});

function repairCommandFor(encodedCaBundle: string): string {
  return `sudo env P2PSTREAM_REPAIR_TRUST=true MANAGEMENT_CA_PEM_BASE64='${encodedCaBundle}' bash '${DEFAULT_LOCAL_INSTALLER_PATH}'`;
}

function openRotation() {
  void router.push("/settings/management-tls/rotation");
}

async function copyRepairCommand() {
  copyError.value = "";
  try {
    await navigator.clipboard.writeText(repairCommand.value);
  } catch {
    showFullRepairCommand.value = true;
    copyError.value = "Clipboard access failed. Select and copy the full command below.";
    return;
  }
  copied.value = true;
  window.setTimeout(() => { copied.value = false; }, 1800);
}
</script>

<template>
  <div class="stack-xl management-tls-page">
    <PageHeader title="Management TLS" :level="2" hide-title description="Certificate serving this management UI and the CA bundle agents use to trust it.">
      <template #actions>
        <NButton secondary size="small" :loading="loading && !operation" aria-label="Refresh certificate status" @click="refresh()">
          <template #icon><RefreshIcon class="icon-sm" /></template>
          Refresh
        </NButton>
      </template>
    </PageHeader>

    <ManagementTlsAlerts :rotation="rotation" :remote-selected="remoteSelected" :operation-error="operationError" :poll-error="pollError" :last-updated-at="lastUpdatedAt" />

    <section class="section-surface stack-lg">
      <div class="layout-row layout-column space-md mq-md-row mq-md-spread mq-md-align-center">
        <div class="layout-row align-center space-md">
          <span class="tls-mark"><ShieldIcon class="icon-md" /></span>
          <div class="layout-grid space-2xs">
            <p class="copy-xs label-case letter-wide muted-text">Shared management endpoint</p>
            <h2 class="copy-lg weight-semibold">Certificate &amp; agent trust</h2>
          </div>
        </div>
        <NTag :type="rotation?.tlsEnabled ? 'success' : 'error'" :bordered="false">{{ rotation?.tlsEnabled ? 'TLS enabled' : 'TLS disabled' }}</NTag>
      </div>

      <div v-if="rotationInProgress" class="rotation-callout" role="status">
        <div class="layout-grid space-2xs min-width-zero">
          <p class="copy-sm weight-semibold base-text">
            Rotation in progress<template v-if="rotationStep"> · Step {{ rotationStep }} of {{ managementTlsRotationSteps.length }}</template>
          </p>
          <p class="copy-xs muted-text">{{ phaseLabel }} · {{ fleetSummary.readyParticipants }} of {{ fleetSummary.participants }} participating agents ready<template v-if="blockingAgentCount"> · {{ blockingAgentCount }} blocking</template></p>
        </div>
        <NButton type="primary" size="small" icon-placement="right" @click="openRotation">
          <template #icon><ArrowRightIcon class="icon-sm" /></template>
          Continue rotation
        </NButton>
      </div>

      <ManagementTlsCertificateCard :label="`Active · generation ${rotation?.activeGeneration ?? 0n}`" :certificate="rotation?.activeCertificate" show-expiry />

      <div v-if="!rotationInProgress" class="layout-row layout-column align-start space-md mq-sm-row mq-sm-align-center">
        <NButton secondary icon-placement="right" @click="openRotation">
          <template #icon><ArrowRightIcon class="icon-sm" /></template>
          Rotate certificate
        </NButton>
        <p class="copy-xs muted-text">Stage a generated or custom certificate. Agents receive the new trust before anything changes.</p>
      </div>
    </section>

    <section v-if="showFleet" class="section-surface stack-lg">
      <div class="layout-row layout-column space-md mq-md-row mq-md-spread mq-md-align-end">
        <div class="layout-grid space-2xs">
          <p class="copy-xs label-case letter-wide muted-text">Durable fleet trust · generation {{ rotation?.desiredTrustGeneration ?? 0n }}</p>
          <h3 class="copy-md weight-semibold">{{ fleetSummary.enabled }} enabled · {{ rotation?.trustAttentionAgentCount ?? 0 }} needing attention</h3>
          <p class="copy-xs muted-text">Idle health remains tracked after rotation, including disabled agents that may require repair before re-enabling.</p>
        </div>
        <NTag :type="(rotation?.trustAttentionAgentCount ?? 0) === 0 ? 'success' : 'warning'" :bordered="false">{{ (rotation?.trustAttentionAgentCount ?? 0) === 0 ? 'Fleet trust current' : `${rotation?.trustAttentionAgentCount ?? 0} attention` }}</NTag>
      </div>
      <ManagementTlsAgentTable :agents="rotation?.agents ?? []" :phase="phase" />
    </section>

    <section v-if="repairCommand" class="section-surface stack-md">
      <div class="layout-grid space-2xs">
        <p class="copy-xs label-case letter-wide muted-text">Recovery</p>
        <h3 class="copy-md weight-semibold">Repair an agent that cannot reconnect</h3>
        <p class="copy-sm muted-text">Run this on a current compatible installation to restore management trust. For an incompatible or damaged agent, rerun its full install command from the Agents page so the identity and token are installed again.</p>
      </div>
      <div class="repair-bar">
        <code class="repair-bar__preview" :title="repairCommandPreview">{{ repairCommandPreview }}</code>
        <NButton type="primary" size="small" @click="copyRepairCommand">
          <template #icon><CopyIcon class="icon-sm" /></template>
          {{ copied ? 'Copied' : 'Copy command' }}
        </NButton>
      </div>
      <p v-if="copyError" class="copy-xs warning-text">{{ copyError }}</p>
      <div class="layout-row align-center spread-items space-md">
        <p class="copy-xs muted-text">Embeds the current management CA bundle (public certificates only, no private keys).</p>
        <NButton text size="small" :aria-expanded="showFullRepairCommand" @click="showFullRepairCommand = !showFullRepairCommand">
          {{ showFullRepairCommand ? 'Hide full command' : 'Show full command' }}
        </NButton>
      </div>
      <code v-if="showFullRepairCommand" class="repair-command">{{ repairCommand }}</code>
    </section>
  </div>
</template>

<style scoped>
.management-tls-page { gap: 1.25rem; }
.section-surface { padding: 1.25rem; border: 1px solid var(--app-border-subtle); border-radius: 10px; background: var(--app-panel); }
.tls-mark { display: inline-flex; align-items: center; justify-content: center; width: 2.5rem; height: 2.5rem; border-radius: 0.75rem; color: var(--app-accent); background: var(--app-accent-soft); }
.rotation-callout { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 0.75rem 1rem; padding: 0.875rem 1rem; border: 1px solid var(--app-accent); border-radius: 0.75rem; background: var(--app-accent-soft); }
.repair-bar { display: flex; align-items: center; gap: 0.75rem; min-width: 0; padding: 0.375rem 0.375rem 0.375rem 0.875rem; border: 1px solid var(--app-border); border-radius: 0.625rem; background: var(--app-panel); }
.repair-bar__preview { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 0.75rem; }
.repair-command { display: block; max-height: 9rem; overflow: auto; padding: 0.875rem; border: 1px solid var(--app-border); border-radius: 0.625rem; background: var(--app-panel); font-size: 0.72rem; line-height: 1.55; overflow-wrap: anywhere; user-select: all; }
</style>
