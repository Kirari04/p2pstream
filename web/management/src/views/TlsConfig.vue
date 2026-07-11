<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NButtonGroup, NCheckbox, NDrawer, NDrawerContent, NInput, NInputNumber, NTag, NUpload } from "naive-ui";
import type { UploadFileInfo } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementContext } from "@/composables/useManagementContext";
import { BUSY_REASON } from "@/lib/disabledReasons";
import {
  acmeChallengeTypeForMethod,
  dnsCredentialName,
  isDefaultSelfSignedCertificate,
  listenerName,
  tlsCertificateLastAttemptSummary,
  tlsCertificateSummary,
  tlsCertificateRenewalSummary,
  tlsCertificateValiditySummary,
  tlsMethodForCertificate,
  tlsSourceForMethod,
  tlsSourceLabel,
  tlsStatusLabel,
  tlsStatusSeverity,
  type TlsMethod,
} from "@/lib/publicProxyLabels";
import { editorDrawerWidth, naiveTagType } from "@/lib/naiveUi";
import {
  PublicAcmeCa,
  PublicAcmeChallengeType,
  PublicDnsProvider,
  PublicListenerProtocol,
  PublicTlsCertificateSource,
  PublicTlsCertificateStatus,
  type PublicTlsDnsCredential,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

type TlsFileField = "cert" | "key";
type ManualTlsMaterialMode = "generate" | "upload";

const tlsMethodOptions: Array<{ value: TlsMethod; label: string }> = [
  { value: "manual", label: "Manual" },
  { value: "http_01", label: "HTTP-01" },
  { value: "tls_alpn_01", label: "TLS-ALPN" },
  { value: "dns_01", label: "DNS-01" },
];
const manualTlsMaterialOptions: Array<{ value: ManualTlsMaterialMode; label: string }> = [
  { value: "generate", label: "Generate self-signed" },
  { value: "upload", label: "Upload PEM" },
];
const acmeCaOptions = [
  { label: "Let's Encrypt production", value: PublicAcmeCa.LETS_ENCRYPT_PRODUCTION },
  { label: "Let's Encrypt staging", value: PublicAcmeCa.LETS_ENCRYPT_STAGING },
];

const {
  dashboard,
  publicProxyConfig,
  isBusy,
  runManagementAction,
} = useManagementContext();

const config = computed(() => publicProxyConfig.value ?? null);
const listeners = computed(() => config.value?.listeners ?? []);
const httpsListeners = computed(() => listeners.value.filter((listener) => listener.protocol === PublicListenerProtocol.HTTPS));
const tlsCertificates = computed(() => config.value?.tlsCertificates ?? []);
const tlsDnsCredentials = computed(() => config.value?.tlsDnsCredentials ?? []);
const httpsListenerOptions = computed(() =>
  httpsListeners.value.map((listener) => ({
    label: listener.name,
    value: listener.id.toString(),
  })),
);
const dnsCredentialOptions = computed(() => [
  { label: "Select credential", value: "" },
  ...tlsDnsCredentials.value.map((credential) => ({
    label: credential.name,
    value: credential.id.toString(),
  })),
]);
const certificateErrors = computed(() => tlsCertificates.value.filter((cert) => cert.status === PublicTlsCertificateStatus.ERROR || Boolean(cert.lastError)).length);
const busyDisabledReason = computed(() => isBusy.value ? BUSY_REASON : "");

const summaryCards = computed(() => [
  { label: "HTTPS Listeners", value: httpsListeners.value.length.toString(), detail: "certificate targets" },
  { label: "Certificates", value: tlsCertificates.value.length.toString(), detail: "configured mappings" },
  { label: "Certificate Errors", value: certificateErrors.value.toString(), detail: certificateErrors.value ? "needs attention" : "none reported" },
  { label: "DNS Credentials", value: tlsDnsCredentials.value.length.toString(), detail: "for DNS-01 challenges" },
]);

const isTlsModalOpen = ref(false);
const isTlsCredentialModalOpen = ref(false);
const tlsUploadError = ref("");
const tlsCredentialError = ref("");
const { confirm } = useConfirmDialog();

const tlsForm = reactive({
  id: "",
  listenerId: "",
  hostnamePattern: "",
  method: "manual" as TlsMethod,
  manualMode: "generate" as ManualTlsMaterialMode,
  selfSignedValidityDays: 3650,
  acmeEmail: "",
  acmeCa: PublicAcmeCa.LETS_ENCRYPT_PRODUCTION,
  dnsCredentialId: "",
  certPem: null as Uint8Array | null,
  keyPem: null as Uint8Array | null,
  certFileName: "",
  keyFileName: "",
  enabled: true,
});

const tlsCredentialForm = reactive({
  id: "",
  name: "",
  cloudflareZoneId: "",
  apiToken: "",
  apiTokenSaved: false,
  enabled: true,
});

const tlsHasPartialUpload = computed(() => Boolean(tlsForm.certPem) !== Boolean(tlsForm.keyPem));
const tlsSubmitDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  if (!httpsListeners.value.length) return "Create an HTTPS listener before adding a TLS mapping.";
  if (tlsForm.method === "manual") {
    if (tlsForm.manualMode === "generate") {
      if (!Number.isInteger(tlsForm.selfSignedValidityDays) || tlsForm.selfSignedValidityDays < 1 || tlsForm.selfSignedValidityDays > 3650) {
        return "Enter certificate validity between 1 and 3650 days.";
      }
      return "";
    }
    if (!tlsForm.id && (!tlsForm.certPem || !tlsForm.keyPem)) return "Upload both the certificate and private key files.";
    if (tlsHasPartialUpload.value) return "Upload both files to replace the certificate.";
    return "";
  }
  if (!tlsForm.acmeEmail.trim()) return "Enter the ACME account email.";
  if (tlsForm.hostnamePattern.trim().startsWith("*.") && tlsForm.method !== "dns_01") return "Wildcard certificates require DNS-01.";
  if (tlsForm.method === "dns_01" && !tlsForm.dnsCredentialId) return "Select a Cloudflare DNS credential.";
  return "";
});
const tlsSubmitDisabled = computed(() => Boolean(tlsSubmitDisabledReason.value));
const tlsCredentialSubmitDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  if (!tlsCredentialForm.name.trim()) return "Enter a credential name.";
  if (!tlsCredentialForm.cloudflareZoneId.trim()) return "Enter the Cloudflare zone ID.";
  if (!tlsCredentialForm.id && !tlsCredentialForm.apiToken.trim()) return "Enter the Cloudflare API token.";
  return "";
});

async function run(action: () => Promise<void>) {
  if (!runManagementAction) return;
  await runManagementAction(action);
}

function openAddTlsModal() {
  resetTlsForm();
  isTlsModalOpen.value = true;
}

function resetTlsForm() {
  tlsForm.id = "";
  tlsForm.listenerId = httpsListeners.value[0]?.id.toString() ?? "";
  tlsForm.hostnamePattern = "";
  tlsForm.method = "manual";
  tlsForm.manualMode = "generate";
  tlsForm.selfSignedValidityDays = 3650;
  tlsForm.acmeEmail = "";
  tlsForm.acmeCa = PublicAcmeCa.LETS_ENCRYPT_PRODUCTION;
  tlsForm.dnsCredentialId = tlsDnsCredentials.value[0]?.id.toString() ?? "";
  tlsForm.certPem = null;
  tlsForm.keyPem = null;
  tlsForm.certFileName = "";
  tlsForm.keyFileName = "";
  tlsForm.enabled = true;
  tlsUploadError.value = "";
}

function editTlsCertificate(certId: bigint) {
  const cert = tlsCertificates.value.find((item) => item.id === certId);
  if (!cert) return;
  tlsForm.id = cert.id.toString();
  tlsForm.listenerId = cert.listenerId.toString();
  tlsForm.hostnamePattern = cert.hostnamePattern;
  tlsForm.method = tlsMethodForCertificate(cert);
  tlsForm.manualMode = tlsForm.method === "manual" ? "upload" : "generate";
  tlsForm.selfSignedValidityDays = 3650;
  tlsForm.acmeEmail = cert.acmeEmail;
  tlsForm.acmeCa = cert.acmeCa || PublicAcmeCa.LETS_ENCRYPT_PRODUCTION;
  tlsForm.dnsCredentialId = cert.dnsCredentialId ? cert.dnsCredentialId.toString() : (tlsDnsCredentials.value[0]?.id.toString() ?? "");
  tlsForm.certPem = null;
  tlsForm.keyPem = null;
  tlsForm.certFileName = "";
  tlsForm.keyFileName = "";
  tlsForm.enabled = cert.enabled;
  tlsUploadError.value = "";
  isTlsModalOpen.value = true;
}

function openAddTlsCredentialModal() {
  resetTlsCredentialForm();
  isTlsCredentialModalOpen.value = true;
}

function resetTlsCredentialForm() {
  tlsCredentialForm.id = "";
  tlsCredentialForm.name = "";
  tlsCredentialForm.cloudflareZoneId = "";
  tlsCredentialForm.apiToken = "";
  tlsCredentialForm.apiTokenSaved = false;
  tlsCredentialForm.enabled = true;
  tlsCredentialError.value = "";
}

function editTlsCredential(credential: PublicTlsDnsCredential) {
  tlsCredentialForm.id = credential.id.toString();
  tlsCredentialForm.name = credential.name;
  tlsCredentialForm.cloudflareZoneId = credential.cloudflareZoneId;
  tlsCredentialForm.apiToken = "";
  tlsCredentialForm.apiTokenSaved = credential.apiTokenSet;
  tlsCredentialForm.enabled = credential.enabled;
  tlsCredentialError.value = "";
  isTlsCredentialModalOpen.value = true;
}

async function handleTlsUploadChange(field: TlsFileField, options: { fileList: UploadFileInfo[] }) {
  tlsUploadError.value = "";
  const file = options.fileList.at(-1)?.file ?? null;
  if (!file) {
    if (field === "cert") {
      tlsForm.certPem = null;
      tlsForm.certFileName = "";
    } else {
      tlsForm.keyPem = null;
      tlsForm.keyFileName = "";
    }
    return;
  }

  const bytes = new Uint8Array(await file.arrayBuffer());
  if (field === "cert") {
    tlsForm.certPem = bytes;
    tlsForm.certFileName = file.name;
    return;
  }
  tlsForm.keyPem = bytes;
  tlsForm.keyFileName = file.name;
}

async function submitTlsCertificate() {
  tlsUploadError.value = "";
  const isManualUpload = tlsForm.method === "manual" && tlsForm.manualMode === "upload";
  const isGeneratedSelfSigned = tlsForm.method === "manual" && tlsForm.manualMode === "generate";
  if (isManualUpload && !tlsForm.id && (!tlsForm.certPem || !tlsForm.keyPem)) {
    tlsUploadError.value = "Upload both the certificate and private key.";
    return;
  }
  if (isManualUpload && tlsHasPartialUpload.value) {
    tlsUploadError.value = "Upload both files to replace the certificate.";
    return;
  }
  if (isGeneratedSelfSigned && (!Number.isInteger(tlsForm.selfSignedValidityDays) || tlsForm.selfSignedValidityDays < 1 || tlsForm.selfSignedValidityDays > 3650)) {
    tlsUploadError.value = "Enter certificate validity between 1 and 3650 days.";
    return;
  }
  if (tlsForm.method !== "manual" && tlsForm.method !== "dns_01" && tlsForm.hostnamePattern.trim().startsWith("*.")) {
    tlsUploadError.value = "Wildcard certificates require DNS-01.";
    return;
  }

  await run(async () => {
    const isManual = tlsForm.method === "manual";
    const payload = {
      listenerId: BigInt(tlsForm.listenerId || "0"),
      hostnamePattern: tlsForm.hostnamePattern,
      enabled: tlsForm.enabled,
      certPem: isManualUpload ? (tlsForm.certPem ?? new Uint8Array()) : new Uint8Array(),
      keyPem: isManualUpload ? (tlsForm.keyPem ?? new Uint8Array()) : new Uint8Array(),
      source: tlsSourceForMethod(tlsForm.method),
      acmeChallengeType: isManual ? PublicAcmeChallengeType.UNSPECIFIED : acmeChallengeTypeForMethod(tlsForm.method),
      acmeCa: isManual ? PublicAcmeCa.UNSPECIFIED : tlsForm.acmeCa,
      acmeEmail: isManual ? "" : tlsForm.acmeEmail,
      dnsCredentialId: !isManual && tlsForm.method === "dns_01" ? BigInt(tlsForm.dnsCredentialId || "0") : 0n,
      generateSelfSigned: isGeneratedSelfSigned,
      selfSignedValidityDays: isGeneratedSelfSigned ? BigInt(tlsForm.selfSignedValidityDays) : 0n,
    };
    if (tlsForm.id) {
      await managementClient.updatePublicTlsCertificate({ id: BigInt(tlsForm.id), ...payload });
    } else {
      await managementClient.createPublicTlsCertificate(payload);
    }
    isTlsModalOpen.value = false;
  });
}

async function renewTlsCertificate(id: bigint) {
  await run(async () => {
    await managementClient.renewPublicTlsCertificate({ id });
  });
}

async function deleteTlsCertificate(id: bigint) {
  if (!await confirm("Delete TLS Certificate", "This certificate will be permanently removed. HTTPS connections using it will fall back to the default self-signed certificate.")) return;
  await run(async () => {
    await managementClient.deletePublicTlsCertificate({ id });
  });
}

async function submitTlsCredential() {
  tlsCredentialError.value = "";
  if (!tlsCredentialForm.id && !tlsCredentialForm.apiToken.trim()) {
    tlsCredentialError.value = "Enter the Cloudflare API token.";
    return;
  }
  await run(async () => {
    const payload = {
      name: tlsCredentialForm.name,
      provider: PublicDnsProvider.CLOUDFLARE,
      cloudflareZoneId: tlsCredentialForm.cloudflareZoneId,
      apiToken: tlsCredentialForm.apiToken,
      apiTokenSet: tlsCredentialForm.apiToken.trim() !== "",
      enabled: tlsCredentialForm.enabled,
    };
    if (tlsCredentialForm.id) {
      await managementClient.updatePublicTlsDnsCredential({ id: BigInt(tlsCredentialForm.id), ...payload });
    } else {
      await managementClient.createPublicTlsDnsCredential(payload);
    }
    isTlsCredentialModalOpen.value = false;
  });
}

async function deleteTlsCredential(id: bigint) {
  if (!await confirm("Delete DNS Credential", "This credential will be permanently removed. Certificates using it will no longer be able to renew.")) return;
  await run(async () => {
    await managementClient.deletePublicTlsDnsCredential({ id });
  });
}

watch(httpsListeners, () => {
  if (!tlsForm.listenerId && httpsListeners.value[0]) {
    tlsForm.listenerId = httpsListeners.value[0].id.toString();
  }
}, { immediate: true });

watch(tlsDnsCredentials, () => {
  if (!tlsForm.dnsCredentialId && tlsDnsCredentials.value[0]) {
    tlsForm.dnsCredentialId = tlsDnsCredentials.value[0].id.toString();
  }
}, { immediate: true });
</script>

<template>
  <div v-if="dashboard" class="stack-xl">
    <div class="layout-row layout-column space-lg mq-md-row mq-md-align-end mq-md-spread">
      <div>
        <h3 class="margin-bottom-sm copy-xl weight-bold">TLS</h3>
        <p class="copy-sm muted-text">HTTPS certificate mappings and DNS credentials.</p>
      </div>
      <div class="layout-row wrap-items space-sm">
        <NButton secondary size="small" @click="openAddTlsCredentialModal">
          <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
          Add DNS Credential
        </NButton>
        <NButton secondary size="small" @click="openAddTlsModal">
          <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
          Add Certificate
        </NButton>
      </div>
    </div>

    <section class="layout-grid space-lg mq-sm-cols-two mq-xl-cols-four">
      <div v-for="card in summaryCards" :key="card.label" class="surface-card pad-lg">
        <p class="copy-xs weight-semibold label-case letter-widest muted-text">{{ card.label }}</p>
        <p class="margin-top-sm copy-2xl weight-semibold base-text">{{ card.value }}</p>
        <p class="margin-top-xs copy-xs muted-text">{{ card.detail }}</p>
      </div>
    </section>

    <section class="surface-card hide-overflow">
      <div class="divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
        <div>
          <h4 class="copy-sm weight-semibold label-case letter-widest muted-text">TLS Certificates</h4>
          <p class="margin-top-2xs copy-xs muted-text normal-text letter-normal">Certificates for HTTPS listeners.</p>
        </div>
        <NButton secondary size="small" @click="openAddTlsModal">
          <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
          Add Certificate
        </NButton>
      </div>
      <div class="tls-table" role="table" aria-label="TLS certificate mappings">
        <div v-if="tlsCertificates.length || httpsListeners.length" class="tls-table__header tls-table__certificate-grid" role="row">
          <span role="columnheader">Mapping</span>
          <span role="columnheader">Certificate</span>
          <span role="columnheader">Status</span>
          <span role="columnheader">Lifecycle</span>
          <span class="visually-hidden" role="columnheader">Actions</span>
        </div>
        <div
          v-for="cert in tlsCertificates"
          :key="cert.id.toString()"
          :data-testid="`tls-row-${cert.id.toString()}`"
          class="tls-table__row tls-table__certificate-grid"
          role="row"
        >
          <div class="tls-table__cell tls-table__identity" data-label="Mapping" role="cell">
            <strong>{{ cert.hostnamePattern }}</strong>
            <span>{{ listenerName(cert.listenerId, listeners) }}</span>
          </div>
          <div class="tls-table__cell" data-label="Certificate" role="cell">
            <NTag v-if="isDefaultSelfSignedCertificate(cert)" size="small" :bordered="false" type="info">Self-signed</NTag>
            <NTag v-else size="small" :bordered="false" type="info">{{ tlsSourceLabel(cert) }}</NTag>
            <span class="tls-table__meta">{{ tlsCertificateSummary(cert) }}</span>
          </div>
          <div class="tls-table__cell" data-label="Status" role="cell">
            <NTag size="small" :bordered="false" :type="naiveTagType(tlsStatusSeverity(cert))">{{ tlsStatusLabel(cert) }}</NTag>
            <span v-if="tlsCertificateValiditySummary(cert)" class="tls-table__meta">{{ tlsCertificateValiditySummary(cert) }}</span>
          </div>
          <div class="tls-table__cell" data-label="Lifecycle" role="cell">
            <span v-if="tlsCertificateRenewalSummary(cert)" class="tls-table__meta">{{ tlsCertificateRenewalSummary(cert) }}</span>
            <span v-if="tlsCertificateLastAttemptSummary(cert)" class="tls-table__meta">{{ tlsCertificateLastAttemptSummary(cert) }}</span>
            <span v-if="cert.source === PublicTlsCertificateSource.ACME && cert.dnsCredentialId" class="tls-table__meta">
              Cloudflare · {{ dnsCredentialName(cert.dnsCredentialId, tlsDnsCredentials) }}
            </span>
            <span v-if="!tlsCertificateRenewalSummary(cert) && !tlsCertificateLastAttemptSummary(cert)" class="tls-table__meta">Managed manually</span>
          </div>
          <div class="tls-table__actions" data-label="Actions" role="cell">
            <div class="tls-table__action-buttons">
              <DisabledHint
                v-if="cert.source === PublicTlsCertificateSource.ACME"
                :disabled="Boolean(busyDisabledReason)"
                :reason="busyDisabledReason"
              >
                <NButton
                  secondary
                  size="small"
                  aria-label="Renew TLS certificate"
                  title="Renew TLS certificate"
                  :disabled="Boolean(busyDisabledReason)"
                  @click="renewTlsCertificate(cert.id)"
                >
                  <template #icon><RefreshIcon class="icon-sm icon-sm" /></template>
                </NButton>
              </DisabledHint>
              <NButton secondary size="small" aria-label="Edit TLS mapping" title="Edit TLS mapping" @click="editTlsCertificate(cert.id)">
                <template #icon><PencilIcon class="icon-sm icon-sm" /></template>
              </NButton>
              <NButton type="error" size="small" aria-label="Delete TLS mapping" title="Delete TLS mapping" @click="deleteTlsCertificate(cert.id)">
                <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
              </NButton>
            </div>
          </div>
          <p v-if="cert.lastError" class="tls-table__row-note error-text" role="cell">Last error: {{ cert.lastError }}</p>
        </div>
        <div v-if="httpsListeners.length && !tlsCertificates.length" class="tls-table__row tls-table__certificate-grid" role="row">
          <div class="tls-table__cell tls-table__identity" data-label="Mapping" role="cell">
            <strong>p2pstream.local</strong>
            <span>{{ httpsListeners[0]?.name ?? "HTTPS listener" }}</span>
          </div>
          <div class="tls-table__cell" data-label="Certificate" role="cell">
            <NTag size="small" :bordered="false" type="info">Self-signed</NTag>
            <span class="tls-table__meta">Runtime fallback certificate</span>
          </div>
          <div class="tls-table__cell" data-label="Status" role="cell"><NTag size="small" :bordered="false" type="success">Active</NTag></div>
          <div class="tls-table__cell" data-label="Lifecycle" role="cell"><span class="tls-table__meta">Managed automatically</span></div>
          <div class="tls-table__actions tls-table__actions--note" data-label="Actions" role="cell">System managed</div>
        </div>
        <EmptyState
          v-if="!httpsListeners.length"
          title="No HTTPS listeners configured"
          description="Create an HTTPS listener on the Proxy page before adding certificate mappings."
        />
      </div>
    </section>

    <section class="surface-card hide-overflow">
      <div class="divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
        <div>
          <h4 class="copy-sm weight-semibold label-case letter-widest muted-text">DNS Credentials</h4>
          <p class="margin-top-2xs copy-xs muted-text normal-text letter-normal">Credentials used for DNS-01 certificate challenges.</p>
        </div>
        <NButton secondary size="small" @click="openAddTlsCredentialModal">
          <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
          Add DNS Credential
        </NButton>
      </div>
      <div class="tls-table tls-table--credentials" role="table" aria-label="DNS credentials">
        <div v-if="tlsDnsCredentials.length" class="tls-table__header tls-table__credential-grid" role="row">
          <span role="columnheader">Credential</span>
          <span role="columnheader">Provider</span>
          <span role="columnheader">Zone ID</span>
          <span role="columnheader">State</span>
          <span class="visually-hidden" role="columnheader">Actions</span>
        </div>
        <div v-for="credential in tlsDnsCredentials" :key="credential.id.toString()" class="tls-table__row tls-table__credential-grid" role="row">
          <div class="tls-table__cell tls-table__identity" data-label="Credential" role="cell">
            <strong>{{ credential.name }}</strong>
            <span>DNS-01 validation</span>
          </div>
          <div class="tls-table__cell" data-label="Provider" role="cell">
            <NTag size="small" :bordered="false" type="info">Cloudflare</NTag>
          </div>
          <div class="tls-table__cell" data-label="Zone ID" role="cell">
            <code class="tls-table__zone">{{ credential.cloudflareZoneId }}</code>
          </div>
          <div class="tls-table__cell" data-label="State" role="cell">
            <NTag size="small" :bordered="false" :type="credential.enabled ? 'success' : 'warning'">{{ credential.enabled ? "Enabled" : "Disabled" }}</NTag>
            <span class="tls-table__meta">{{ credential.apiTokenSet ? "Secret saved" : "Secret missing" }}</span>
          </div>
          <div class="tls-table__actions" data-label="Actions" role="cell">
            <div class="tls-table__action-buttons">
              <NButton secondary size="small" aria-label="Edit DNS credential" title="Edit DNS credential" @click="editTlsCredential(credential)">
                <template #icon><PencilIcon class="icon-sm icon-sm" /></template>
              </NButton>
              <NButton type="error" size="small" aria-label="Delete DNS credential" title="Delete DNS credential" @click="deleteTlsCredential(credential.id)">
                <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
              </NButton>
            </div>
          </div>
        </div>
        <EmptyState
          v-if="!tlsDnsCredentials.length"
          title="No DNS credentials configured"
          description="Add a Cloudflare DNS credential when you need DNS-01 validation or wildcard certificates."
          action-label="Add DNS Credential"
          @action="openAddTlsCredentialModal"
        />
      </div>
    </section>

    <NDrawer
      v-model:show="isTlsModalOpen"
      placement="right"
      :width="editorDrawerWidth('36rem')"
      :aria-label="tlsForm.id ? 'Edit TLS Mapping' : 'Add TLS Mapping'"
      class="editor-drawer"
    >
      <NDrawerContent :title="tlsForm.id ? 'Edit TLS Mapping' : 'Add TLS Mapping'" closable>
      <form class="editor-drawer-form layout-grid space-lg" @submit.prevent="submitTlsCertificate">
        <div class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Method
          <NButtonGroup class="layout-grid cols-two space-sm mq-sm-cols-four" size="small" role="group" aria-label="Certificate method">
            <NButton
              v-for="method in tlsMethodOptions"
              :key="method.value"
              attr-type="button"
              :type="tlsForm.method === method.value ? 'primary' : 'default'"
              :aria-pressed="tlsForm.method === method.value"
              @click="tlsForm.method = method.value"
            >
              {{ method.label }}
            </NButton>
          </NButtonGroup>
        </div>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          HTTPS listener
          <AccessibleSelect v-model:value="tlsForm.listenerId" accessible-label="HTTPS listener" size="small" :options="httpsListenerOptions" required />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Hostname pattern
          <NInput v-model:value="tlsForm.hostnamePattern" size="small" placeholder="app.example.com" required />
          <p class="copy-xs weight-normal normal-text letter-normal muted-text">Exact domain or wildcard prefix (*.example.com).</p>
        </label>
        <div v-if="tlsForm.method !== 'manual'" class="layout-grid space-md">
          <div class="layout-grid space-md mq-sm-cols-two">
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              ACME email
              <NInput v-model:value="tlsForm.acmeEmail" size="small" type="text" placeholder="admin@example.com" required />
              <p class="copy-xs weight-normal normal-text letter-normal muted-text">Used for certificate expiration notices from Let's Encrypt.</p>
            </label>
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              CA environment
              <AccessibleSelect v-model:value="tlsForm.acmeCa" accessible-label="CA environment" size="small" :options="acmeCaOptions" />
            </label>
          </div>
          <label v-if="tlsForm.method === 'dns_01'" class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Cloudflare credential
            <AccessibleSelect v-model:value="tlsForm.dnsCredentialId" accessible-label="Cloudflare credential" size="small" :options="dnsCredentialOptions" required />
          </label>
        </div>
        <div v-if="tlsForm.method === 'manual'" class="layout-grid space-md">
          <div class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Certificate material
            <NButtonGroup class="layout-grid cols-two space-sm" size="small" role="group" aria-label="Certificate material source">
              <NButton
                v-for="option in manualTlsMaterialOptions"
                :key="option.value"
                attr-type="button"
                :type="tlsForm.manualMode === option.value ? 'primary' : 'default'"
                :aria-pressed="tlsForm.manualMode === option.value"
                @click="tlsForm.manualMode = option.value"
              >
                {{ option.label }}
              </NButton>
            </NButtonGroup>
          </div>
          <label v-if="tlsForm.manualMode === 'generate'" class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Validity days
            <NInputNumber :show-button="false" v-model:value="tlsForm.selfSignedValidityDays" size="small" :min="1" :max="3650" :step="1" required />
          </label>
          <div v-else class="layout-grid space-md mq-sm-cols-two">
            <div class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Certificate file
              <NUpload
                :default-upload="false"
                :max="1"
                accept=".pem,.crt,.cer"
                @change="handleTlsUploadChange('cert', $event)"
              >
                <NButton secondary size="small" attr-type="button">Choose certificate</NButton>
              </NUpload>
              <span v-if="tlsForm.certFileName" class="clip-text copy-xs normal-text letter-normal base-text">{{ tlsForm.certFileName }}</span>
            </div>
            <div class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Private key file
              <NUpload
                :default-upload="false"
                :max="1"
                accept=".pem,.key"
                @change="handleTlsUploadChange('key', $event)"
              >
                <NButton secondary size="small" attr-type="button">Choose private key</NButton>
              </NUpload>
              <span v-if="tlsForm.keyFileName" class="clip-text copy-xs normal-text letter-normal base-text">{{ tlsForm.keyFileName }}</span>
            </div>
          </div>
        </div>
        <p v-if="tlsForm.id && tlsForm.method === 'manual' && tlsForm.manualMode === 'upload'" class="round-md framed frame-standard muted-bg pad-x-md pad-y-sm copy-xs muted-text">
          Current certificate is stored in the app config directory.
        </p>
        <p v-if="tlsUploadError" class="round-md framed error-border error-surface pad-x-md pad-y-sm copy-sm error-text">
          {{ tlsUploadError }}
        </p>
        <NCheckbox v-model:checked="tlsForm.enabled" class="margin-top-sm">
          Enabled
        </NCheckbox>
        <div class="editor-drawer-actions margin-top-lg layout-row align-end-row space-md">
          <NButton secondary attr-type="button" @click="isTlsModalOpen = false">Cancel</NButton>
          <DisabledHint :disabled="Boolean(tlsSubmitDisabledReason)" :reason="tlsSubmitDisabledReason">
            <NButton type="primary" attr-type="submit" :disabled="tlsSubmitDisabled">
              {{ tlsForm.id ? 'Save Changes' : 'Create TLS Mapping' }}
            </NButton>
          </DisabledHint>
        </div>
      </form>
      </NDrawerContent>
    </NDrawer>

    <NDrawer
      v-model:show="isTlsCredentialModalOpen"
      placement="right"
      :width="editorDrawerWidth('32rem')"
      :aria-label="tlsCredentialForm.id ? 'Edit DNS Credential' : 'Add DNS Credential'"
      class="editor-drawer"
    >
      <NDrawerContent :title="tlsCredentialForm.id ? 'Edit DNS Credential' : 'Add DNS Credential'" closable>
      <form class="editor-drawer-form layout-grid space-lg" @submit.prevent="submitTlsCredential">
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Name
          <NInput v-model:value="tlsCredentialForm.name" size="small" placeholder="cloudflare-prod" required />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Cloudflare zone ID
          <NInput v-model:value="tlsCredentialForm.cloudflareZoneId" size="small" class="mono-text" required />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          API token
          <NInput
            v-model:value="tlsCredentialForm.apiToken"
            size="small"
            type="password"
            :placeholder="tlsCredentialForm.apiTokenSaved ? 'Saved token' : 'Cloudflare API token'"
            :required="!tlsCredentialForm.id"
          />
        </label>
        <p v-if="tlsCredentialError" class="round-md framed error-border error-surface pad-x-md pad-y-sm copy-sm error-text">
          {{ tlsCredentialError }}
        </p>
        <NCheckbox v-model:checked="tlsCredentialForm.enabled" class="margin-top-sm">
          Enabled
        </NCheckbox>
        <div class="editor-drawer-actions margin-top-lg layout-row align-end-row space-md">
          <NButton secondary attr-type="button" @click="isTlsCredentialModalOpen = false">Cancel</NButton>
          <DisabledHint :disabled="Boolean(tlsCredentialSubmitDisabledReason)" :reason="tlsCredentialSubmitDisabledReason">
            <NButton type="primary" attr-type="submit" :disabled="Boolean(tlsCredentialSubmitDisabledReason)">
              {{ tlsCredentialForm.id ? 'Save Credential' : 'Create Credential' }}
            </NButton>
          </DisabledHint>
        </div>
      </form>
      </NDrawerContent>
    </NDrawer>
  </div>
</template>

<style scoped>
.tls-table {
  min-width: 0;
  container-type: inline-size;
}

.tls-table__header,
.tls-table__row {
  display: grid;
  align-items: center;
  column-gap: 1rem;
}

.tls-table__certificate-grid {
  grid-template-columns: minmax(13rem, 1.35fr) minmax(9rem, 0.85fr) minmax(11rem, 0.9fr) minmax(14rem, 1.25fr) auto;
}

.tls-table__credential-grid {
  grid-template-columns: minmax(13rem, 1.15fr) minmax(8rem, 0.65fr) minmax(13rem, 1fr) minmax(10rem, 0.75fr) auto;
}

.tls-table__header {
  min-height: 2.25rem;
  border-bottom: 1px solid var(--app-border-subtle);
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.6875rem;
  font-weight: 600;
  padding-inline: 1.25rem;
}

.tls-table__row {
  min-height: 4rem;
  border-bottom: 1px solid var(--app-border-subtle);
  padding: 0.625rem 1.25rem;
}

.tls-table__row:last-of-type {
  border-bottom: 0;
}

.tls-table__cell,
.tls-table__identity {
  display: flex;
  min-width: 0;
  align-items: flex-start;
  flex-direction: column;
  gap: 0.25rem;
}

.tls-table__identity strong,
.tls-table__identity span,
.tls-table__meta,
.tls-table__zone {
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  unicode-bidi: plaintext;
  white-space: nowrap;
}

.tls-table__identity strong {
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 600;
}

.tls-table__identity span,
.tls-table__meta,
.tls-table__actions--note {
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.tls-table__zone {
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 0.75rem;
}

.tls-table__actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
}

.tls-table__action-buttons {
  display: flex;
  align-items: center;
  gap: 0.375rem;
}

.tls-table__row-note {
  grid-column: 1 / -1;
  max-height: 4rem;
  margin: 0.5rem 0 0;
  overflow: auto;
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 0.5rem;
  font-size: 0.75rem;
  line-height: 1.45;
  overflow-wrap: anywhere;
  unicode-bidi: plaintext;
  white-space: pre-wrap;
}

@container (max-width: 62rem) {
  .tls-table__header {
    display: none;
  }

  .tls-table__certificate-grid,
  .tls-table__credential-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 0.75rem 1rem;
  }

  .tls-table__row {
    padding-block: 0.875rem;
  }

  .tls-table__cell::before,
  .tls-table__actions::before {
    content: attr(data-label);
    color: var(--app-text-muted);
    font-size: 0.6875rem;
    font-weight: 600;
  }

  .tls-table__actions {
    align-items: flex-start;
    justify-content: flex-start;
    flex-direction: column;
    gap: 0.25rem;
  }

  .tls-table__row-note {
    grid-column: 1 / -1;
  }
}

@container (max-width: 38rem) {
  .tls-table__certificate-grid,
  .tls-table__credential-grid {
    grid-template-columns: minmax(0, 1fr);
  }

  .tls-table__row-note {
    grid-column: 1;
  }
}

@media (pointer: coarse) {
  .tls-table__actions :deep(.n-button) {
    min-width: 2.75rem;
    min-height: 2.75rem;
  }
}
</style>
