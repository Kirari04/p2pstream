<script setup lang="ts">
import { ArrowLeft as ArrowLeftIcon, KeyRound as KeyIcon, RefreshCw as RefreshIcon, Upload as UploadIcon } from "@lucide/vue";
import { NAlert, NButton, NDrawer, NDrawerContent, NInput, NStep, NSteps, NTag } from "naive-ui";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { RouterLink } from "vue-router";
import ManagementTlsAgentTable from "@/components/management-tls/ManagementTlsAgentTable.vue";
import ManagementTlsAlerts from "@/components/management-tls/ManagementTlsAlerts.vue";
import ManagementTlsCertificateCard from "@/components/management-tls/ManagementTlsCertificateCard.vue";
import { useManagementTlsRotation } from "@/composables/useManagementTlsRotation";
import { ManagementTlsRotationPhase } from "@/gen/proto/p2pstream/v1/management_pb";
import { editorDrawerWidth } from "@/lib/naiveUi";
import { managementTlsPhaseLabel, managementTlsRotationStep, managementTlsRotationSteps } from "@/lib/managementTlsPresentation";

const {
  rotation,
  loading,
  operation,
  operationError,
  pollError,
  lastUpdatedAt,
  custom,
  phase,
  remoteSelected,
  canOperate,
  fleetSummary,
  blockingAgentCount,
  cleanupTitle,
  refresh,
  regenerate,
  stageCustom,
  activate,
  cancelRotation,
  rollback,
  beginRetirement,
  finalizeRetirement,
  finalizeCleanup,
} = useManagementTlsRotation();
const customMaterialOpen = ref(false);
const verticalSteps = ref(false);
let narrowQuery: MediaQueryList | undefined;

function syncStepLayout(query: MediaQueryList | MediaQueryListEvent) {
  verticalSteps.value = query.matches;
}

onMounted(() => {
  narrowQuery = window.matchMedia("(max-width: 640px)");
  syncStepLayout(narrowQuery);
  narrowQuery.addEventListener("change", syncStepLayout);
});

onBeforeUnmount(() => {
  narrowQuery?.removeEventListener("change", syncStepLayout);
});

const isIdle = computed(() => phase.value === ManagementTlsRotationPhase.IDLE);
const isCleanup = computed(() => phase.value === ManagementTlsRotationPhase.CLEANING_UP);
const phaseLabel = computed(() => managementTlsPhaseLabel(phase.value, cleanupTitle.value));
/** Idle shows the first step as the next action; cleanup is off the forward path. */
const stepperCurrent = computed(() => isIdle.value ? 1 : managementTlsRotationStep(phase.value));
const blocked = computed(() => blockingAgentCount.value > 0);

async function submitCustom() {
  if (await stageCustom()) customMaterialOpen.value = false;
}
</script>

<template>
  <div class="stack-xl management-tls-rotation">
    <div class="layout-grid space-sm">
      <RouterLink to="/settings/management-tls" class="back-link copy-xs weight-medium muted-text">
        <ArrowLeftIcon class="icon-sm" />
        Management TLS
      </RouterLink>
      <div class="layout-row layout-column space-lg mq-md-row mq-md-align-end mq-md-spread">
        <div>
          <h4 class="margin-bottom-sm copy-lg weight-semibold base-text">Certificate rotation</h4>
          <p class="copy-sm muted-text">Stage a new certificate, distribute trust to agents, then activate it and retire the old CA.</p>
        </div>
        <NButton secondary size="small" :loading="loading && !operation" aria-label="Refresh rotation status" @click="refresh()">
          <template #icon><RefreshIcon class="icon-sm" /></template>
          Refresh
        </NButton>
      </div>
    </div>

    <ManagementTlsAlerts :rotation="rotation" :remote-selected="remoteSelected" :operation-error="operationError" :poll-error="pollError" :last-updated-at="lastUpdatedAt" />

    <section class="round-lg framed frame-standard panel-bg pad-xl stack-lg">
      <NSteps v-if="stepperCurrent" :current="stepperCurrent" :vertical="verticalSteps" size="small" aria-label="Rotation progress">
        <NStep v-for="step in managementTlsRotationSteps" :key="step" :title="step" />
      </NSteps>
      <NAlert v-else-if="isCleanup" type="info" :bordered="false" :title="cleanupTitle">
        The rotation was cancelled or rolled back. Agents are removing the abandoned CA; the active certificate is unchanged.
      </NAlert>

      <template v-if="isIdle">
        <div class="layout-grid space-2xs">
          <h3 class="copy-md weight-semibold">Start a rotation</h3>
          <p class="copy-xs muted-text">Either option only stages the certificate. Agents receive the new trust first; the active certificate changes only when you activate it.</p>
        </div>
        <div class="start-grid">
          <article class="start-option">
            <span class="start-option__icon"><KeyIcon class="icon-md" /></span>
            <div class="layout-grid space-2xs">
              <h4 class="copy-sm weight-semibold">Generate a new CA</h4>
              <p class="copy-xs muted-text">Creates a private CA and server certificate on this server. Use this unless you need a certificate from your own PKI.</p>
            </div>
            <NButton type="primary" :disabled="!canOperate" :loading="operation === 'generate'" @click="regenerate">Generate and stage</NButton>
          </article>
          <article class="start-option">
            <span class="start-option__icon"><UploadIcon class="icon-md" /></span>
            <div class="layout-grid space-2xs">
              <h4 class="copy-sm weight-semibold">Install a custom certificate</h4>
              <p class="copy-xs muted-text">Provide your own certificate chain, private key, and the CA bundle agents should trust.</p>
            </div>
            <NButton secondary :disabled="!canOperate" @click="customMaterialOpen = true">Provide certificate</NButton>
          </article>
        </div>
        <ManagementTlsCertificateCard :label="`Active · generation ${rotation?.activeGeneration ?? 0n}`" :certificate="rotation?.activeCertificate" show-expiry />
      </template>

      <template v-else>
        <div class="cert-grid">
          <ManagementTlsCertificateCard :label="`Active · generation ${rotation?.activeGeneration ?? 0n}`" :certificate="rotation?.activeCertificate" show-expiry />
          <ManagementTlsCertificateCard label="Staged" :certificate="rotation?.stagedCertificate" empty-text="No certificate is staged." staged />
        </div>

        <div class="layout-row layout-column space-md mq-md-row mq-md-spread mq-md-align-end">
          <div class="layout-grid space-2xs">
            <p class="copy-xs label-case letter-wide muted-text">{{ phaseLabel }} · generation {{ rotation?.rolloutGeneration ?? 0n }}</p>
            <h3 class="copy-md weight-semibold">{{ fleetSummary.readyParticipants }} of {{ fleetSummary.participants }} participating agents ready</h3>
            <p class="copy-xs muted-text">{{ fleetSummary.rolloutPercent }}% acknowledged after durable write, reload, and digest readback. Disabled agents are excluded; incompatible agents cannot have installed an abandoned bundle and do not block cleanup.</p>
          </div>
          <NTag :type="blocked ? 'warning' : 'success'" :bordered="false">{{ blockingAgentCount }} blocking</NTag>
        </div>
        <div class="rollout-progress" role="progressbar" :aria-valuenow="fleetSummary.rolloutPercent" aria-valuemin="0" aria-valuemax="100" :aria-label="`${fleetSummary.rolloutPercent}% of trust rollout acknowledged`">
          <span :style="{ width: `${fleetSummary.rolloutPercent}%` }" />
        </div>

        <ManagementTlsAgentTable :agents="rotation?.agents ?? []" :phase="phase" />

        <div class="layout-row layout-column space-sm mq-sm-row">
          <template v-if="phase === ManagementTlsRotationPhase.DISTRIBUTING">
            <NButton type="primary" :disabled="!canOperate || blocked" :loading="operation === 'activate'" @click="activate(false)">Activate certificate</NButton>
            <NButton v-if="blocked" type="error" ghost :disabled="!canOperate" :loading="operation === 'force-activate'" @click="activate(true)">Force activation</NButton>
            <NButton secondary :disabled="!canOperate" :loading="operation === 'cancel'" @click="cancelRotation">Cancel</NButton>
          </template>
          <template v-else-if="phase === ManagementTlsRotationPhase.ACTIVE">
            <NButton type="primary" :disabled="!canOperate || blocked" :loading="operation === 'retire'" @click="beginRetirement">{{ (rotation?.rolloutGeneration ?? 0n) === 0n ? 'Finish rotation' : 'Retire old trust' }}</NButton>
            <NButton secondary :disabled="!canOperate" :loading="operation === 'rollback'" @click="rollback">Roll back certificate</NButton>
          </template>
          <template v-else-if="phase === ManagementTlsRotationPhase.RETIRING">
            <NButton type="primary" :disabled="!canOperate || blocked" :loading="operation === 'finalize'" @click="finalizeRetirement(false)">Finalize retirement</NButton>
            <NButton v-if="blocked" type="error" ghost :disabled="!canOperate" :loading="operation === 'force-finalize'" @click="finalizeRetirement(true)">Force retirement</NButton>
          </template>
          <template v-else-if="isCleanup">
            <NButton type="primary" :disabled="!canOperate || blocked" :loading="operation === 'cleanup'" @click="finalizeCleanup(false)">Finish cleanup</NButton>
            <NButton v-if="blocked" type="error" ghost :disabled="!canOperate" :loading="operation === 'force-cleanup'" @click="finalizeCleanup(true)">Force cleanup</NButton>
          </template>
        </div>
      </template>
    </section>

    <NDrawer
      v-model:show="customMaterialOpen"
      placement="right"
      :width="editorDrawerWidth('42rem')"
      aria-label="Install custom certificate"
      class="editor-drawer"
    >
      <NDrawerContent title="Install custom certificate" closable>
        <form class="editor-drawer-form layout-grid space-lg" @submit.prevent="submitCustom">
          <NAlert type="info" :bordered="false">The private key is validated and stored only on this server. Agents receive the CA bundle and public certificate, never the private key.</NAlert>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">Server certificate chain (PEM)<NInput v-model:value="custom.certificatePem" type="textarea" :autosize="{ minRows: 5, maxRows: 12 }" :input-props="{ spellcheck: false, autocomplete: 'off' }" placeholder="-----BEGIN CERTIFICATE-----" /></label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">Private key (PEM)<NInput v-model:value="custom.privateKeyPem" type="textarea" :autosize="{ minRows: 4, maxRows: 10 }" :input-props="{ spellcheck: false, autocomplete: 'off' }" placeholder="-----BEGIN PRIVATE KEY-----" /></label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">Agent CA bundle (PEM)<NInput v-model:value="custom.caBundlePem" type="textarea" :autosize="{ minRows: 5, maxRows: 12 }" :input-props="{ spellcheck: false, autocomplete: 'off' }" placeholder="-----BEGIN CERTIFICATE-----" /></label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">Current agent CA bundle (PEM, only if requested)<NInput v-model:value="custom.currentCaBundlePem" type="textarea" :autosize="{ minRows: 3, maxRows: 10 }" :input-props="{ spellcheck: false, autocomplete: 'off' }" placeholder="Needed only when the current provided certificate did not include its issuer chain" /><span class="copy-xs muted-text">This is verified against the active certificate before it can be added to the dual-trust rollout.</span></label>
          <div class="editor-drawer-actions margin-top-lg layout-row align-end-row space-md">
            <NButton secondary attr-type="button" @click="customMaterialOpen = false">Cancel</NButton>
            <NButton type="primary" attr-type="submit" :disabled="!canOperate || !custom.certificatePem || !custom.privateKeyPem || !custom.caBundlePem" :loading="operation === 'stage'">Validate and stage</NButton>
          </div>
        </form>
      </NDrawerContent>
    </NDrawer>
  </div>
</template>

<style scoped>
.management-tls-rotation { gap: 1.25rem; }
.back-link { display: inline-flex; width: fit-content; align-items: center; gap: 0.375rem; }
.back-link:hover,
.back-link:focus-visible { color: var(--app-text); }
.start-grid { display: grid; gap: 0.875rem; grid-template-columns: repeat(2, minmax(0, 1fr)); }
.start-option { display: grid; align-content: start; justify-items: start; gap: 0.75rem; padding: 1rem; border: 1px solid var(--app-border); border-radius: 0.75rem; background: var(--app-panel-muted); min-width: 0; }
.start-option__icon { display: inline-flex; align-items: center; justify-content: center; width: 2.25rem; height: 2.25rem; border-radius: 0.625rem; color: var(--app-accent); background: var(--app-accent-soft); }
.cert-grid { display: grid; gap: 0.875rem; grid-template-columns: repeat(2, minmax(0, 1fr)); }
.rollout-progress { height: 0.42rem; overflow: hidden; border-radius: 999px; background: var(--app-border); }
.rollout-progress span { display: block; height: 100%; border-radius: inherit; background: var(--app-accent); transition: width 280ms ease; }
@media (max-width: 720px) { .start-grid, .cert-grid { grid-template-columns: 1fr; } }
</style>
