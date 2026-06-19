<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NButtonGroup, NCheckbox, NInput, NInputNumber, NModal, NSelect, NTag, NUpload } from "naive-ui";
import type { UploadFileInfo } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementContext } from "@/composables/useManagementContext";
import { BUSY_REASON } from "@/lib/disabledReasons";
import {
  acmeChallengeTypeForMethod,
  dnsCredentialName,
  isDefaultSelfSignedCertificate,
  listenerName,
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
import { modalCardStyle, naiveTagType } from "@/lib/naiveUi";
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
  <div v-if="dashboard" class="space-y-8">
    <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
      <div>
        <h3 class="mb-2 text-xl font-bold">TLS</h3>
        <p class="text-sm text-[#888]">HTTPS certificate mappings and DNS credentials.</p>
      </div>
      <div class="flex flex-wrap gap-2">
        <NButton secondary size="small" @click="openAddTlsCredentialModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          Add DNS Credential
        </NButton>
        <NButton secondary size="small" @click="openAddTlsModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          Add Certificate
        </NButton>
      </div>
    </div>

    <section class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div v-for="card in summaryCards" :key="card.label" class="app-card p-4">
        <p class="text-xs font-semibold uppercase tracking-widest text-[#666]">{{ card.label }}</p>
        <p class="mt-2 text-2xl font-semibold text-white">{{ card.value }}</p>
        <p class="mt-1 text-xs text-[#777]">{{ card.detail }}</p>
      </div>
    </section>

    <section class="app-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">TLS Certificates</h4>
          <p class="mt-0.5 text-xs text-[#666] normal-case tracking-normal">Certificates for HTTPS listeners.</p>
        </div>
        <NButton secondary size="small" @click="openAddTlsModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          Add Certificate
        </NButton>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div
          v-for="cert in tlsCertificates"
          :key="cert.id.toString()"
          :data-testid="`tls-row-${cert.id.toString()}`"
          class="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]"
        >
          <div class="min-w-0">
            <div class="flex min-w-0 items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ listenerName(cert.listenerId, listeners) }} / {{ cert.hostnamePattern }}</p>
              <NTag v-if="isDefaultSelfSignedCertificate(cert)" size="small" :bordered="false" type="info">Self-signed</NTag>
              <NTag v-else size="small" :bordered="false" type="info">{{ tlsSourceLabel(cert) }}</NTag>
              <NTag size="small" :bordered="false" :type="naiveTagType(tlsStatusSeverity(cert))">{{ tlsStatusLabel(cert) }}</NTag>
            </div>
            <p class="truncate text-xs text-[#888]">{{ tlsCertificateSummary(cert) }}</p>
            <p v-if="tlsCertificateValiditySummary(cert)" class="truncate text-xs text-[#777]">{{ tlsCertificateValiditySummary(cert) }}</p>
            <p v-if="tlsCertificateRenewalSummary(cert)" class="truncate text-xs text-[#666]">{{ tlsCertificateRenewalSummary(cert) }}</p>
            <p v-if="cert.source === PublicTlsCertificateSource.ACME && cert.dnsCredentialId" class="truncate text-xs text-[#666]">
              Cloudflare / {{ dnsCredentialName(cert.dnsCredentialId, tlsDnsCredentials) }}
            </p>
            <p v-if="cert.lastError" class="truncate text-xs text-red-400">{{ cert.lastError }}</p>
          </div>
          <div class="flex gap-2">
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
                <template #icon><RefreshIcon class="h-3.5 w-3.5" /></template>
              </NButton>
            </DisabledHint>
            <NButton secondary size="small" aria-label="Edit TLS mapping" title="Edit TLS mapping" @click="editTlsCertificate(cert.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete TLS mapping" title="Delete TLS mapping" @click="deleteTlsCertificate(cert.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </NButton>
          </div>
        </div>
        <div v-if="httpsListeners.length && !tlsCertificates.length" class="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ httpsListeners[0]?.name ?? "HTTPS listener" }} / p2pstream.local</p>
              <NTag size="small" :bordered="false" type="info">Self-signed</NTag>
            </div>
            <p class="truncate text-xs text-[#888]">Runtime fallback certificate</p>
          </div>
        </div>
        <EmptyState
          v-if="!httpsListeners.length"
          title="No HTTPS listeners configured"
          description="Create an HTTPS listener on the Proxy page before adding certificate mappings."
        />
      </div>
    </section>

    <section class="app-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">DNS Credentials</h4>
          <p class="mt-0.5 text-xs text-[#666] normal-case tracking-normal">Credentials used for DNS-01 certificate challenges.</p>
        </div>
        <NButton secondary size="small" @click="openAddTlsCredentialModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          Add DNS Credential
        </NButton>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div v-for="credential in tlsDnsCredentials" :key="credential.id.toString()" class="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ credential.name }}</p>
              <NTag size="small" :bordered="false" type="info">Cloudflare</NTag>
              <NTag v-if="!credential.enabled" size="small" :bordered="false" type="warning">Disabled</NTag>
            </div>
            <p class="truncate font-mono text-xs text-[#888]">{{ credential.cloudflareZoneId }}</p>
          </div>
          <div class="flex gap-2">
            <NButton secondary size="small" aria-label="Edit DNS credential" title="Edit DNS credential" @click="editTlsCredential(credential)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete DNS credential" title="Delete DNS credential" @click="deleteTlsCredential(credential.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </NButton>
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

    <NModal
      v-model:show="isTlsModalOpen"
      preset="card"
      :title="tlsForm.id ? 'Edit TLS Mapping' : 'Add TLS Mapping'"
      :style="modalCardStyle('36rem')"
      :bordered="false"
    >
      <form class="grid max-h-[calc(100vh-9rem)] gap-4 overflow-y-auto pr-1" @submit.prevent="submitTlsCertificate">
        <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Method
          <NButtonGroup class="grid grid-cols-2 gap-2 sm:grid-cols-4" size="small">
            <NButton
              v-for="method in tlsMethodOptions"
              :key="method.value"
              attr-type="button"
              :type="tlsForm.method === method.value ? 'primary' : 'default'"
              @click="tlsForm.method = method.value"
            >
              {{ method.label }}
            </NButton>
          </NButtonGroup>
        </div>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          HTTPS listener
          <NSelect v-model:value="tlsForm.listenerId" size="small" :options="httpsListenerOptions" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Hostname pattern
          <NInput v-model:value="tlsForm.hostnamePattern" size="small" placeholder="app.example.com" required />
          <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Exact domain or wildcard prefix (*.example.com).</p>
        </label>
        <div v-if="tlsForm.method !== 'manual'" class="grid gap-3">
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              ACME email
              <NInput v-model:value="tlsForm.acmeEmail" size="small" type="text" placeholder="admin@example.com" required />
              <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Used for certificate expiration notices from Let's Encrypt.</p>
            </label>
            <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              CA environment
              <NSelect v-model:value="tlsForm.acmeCa" size="small" :options="acmeCaOptions" />
            </label>
          </div>
          <label v-if="tlsForm.method === 'dns_01'" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Cloudflare credential
            <NSelect v-model:value="tlsForm.dnsCredentialId" size="small" :options="dnsCredentialOptions" required />
          </label>
        </div>
        <div v-if="tlsForm.method === 'manual'" class="grid gap-3">
          <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Certificate material
            <NButtonGroup class="grid grid-cols-2 gap-2" size="small">
              <NButton
                v-for="option in manualTlsMaterialOptions"
                :key="option.value"
                attr-type="button"
                :type="tlsForm.manualMode === option.value ? 'primary' : 'default'"
                @click="tlsForm.manualMode = option.value"
              >
                {{ option.label }}
              </NButton>
            </NButtonGroup>
          </div>
          <label v-if="tlsForm.manualMode === 'generate'" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Validity days
            <NInputNumber v-model:value="tlsForm.selfSignedValidityDays" size="small" :min="1" :max="3650" :step="1" required />
          </label>
          <div v-else class="grid gap-3 sm:grid-cols-2">
            <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              Certificate file
              <NUpload
                :default-upload="false"
                :max="1"
                accept=".pem,.crt,.cer"
                @change="handleTlsUploadChange('cert', $event)"
              >
                <NButton secondary size="small" attr-type="button">Choose certificate</NButton>
              </NUpload>
              <span v-if="tlsForm.certFileName" class="truncate text-xs normal-case tracking-normal text-[#d4d4d8]">{{ tlsForm.certFileName }}</span>
            </div>
            <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              Private key file
              <NUpload
                :default-upload="false"
                :max="1"
                accept=".pem,.key"
                @change="handleTlsUploadChange('key', $event)"
              >
                <NButton secondary size="small" attr-type="button">Choose private key</NButton>
              </NUpload>
              <span v-if="tlsForm.keyFileName" class="truncate text-xs normal-case tracking-normal text-[#d4d4d8]">{{ tlsForm.keyFileName }}</span>
            </div>
          </div>
        </div>
        <p v-if="tlsForm.id && tlsForm.method === 'manual' && tlsForm.manualMode === 'upload'" class="rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-2 text-xs text-[#888]">
          Current certificate is stored in the app config directory.
        </p>
        <p v-if="tlsUploadError" class="rounded-md border border-red-900/50 bg-red-950/20 px-3 py-2 text-sm text-red-400">
          {{ tlsUploadError }}
        </p>
        <NCheckbox v-model:checked="tlsForm.enabled" class="mt-2">
          Enabled
        </NCheckbox>
        <div class="mt-4 flex justify-end gap-3">
          <NButton secondary attr-type="button" @click="isTlsModalOpen = false">Cancel</NButton>
          <DisabledHint :disabled="Boolean(tlsSubmitDisabledReason)" :reason="tlsSubmitDisabledReason">
            <NButton type="primary" attr-type="submit" :disabled="tlsSubmitDisabled">
              {{ tlsForm.id ? 'Save Changes' : 'Create TLS Mapping' }}
            </NButton>
          </DisabledHint>
        </div>
      </form>
    </NModal>

    <NModal
      v-model:show="isTlsCredentialModalOpen"
      preset="card"
      :title="tlsCredentialForm.id ? 'Edit DNS Credential' : 'Add DNS Credential'"
      :style="modalCardStyle('32rem')"
      :bordered="false"
    >
      <form class="grid max-h-[calc(100vh-9rem)] gap-4 overflow-y-auto pr-1" @submit.prevent="submitTlsCredential">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Name
          <NInput v-model:value="tlsCredentialForm.name" size="small" placeholder="cloudflare-prod" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Cloudflare zone ID
          <NInput v-model:value="tlsCredentialForm.cloudflareZoneId" size="small" class="font-mono" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          API token
          <NInput
            v-model:value="tlsCredentialForm.apiToken"
            size="small"
            type="password"
            :placeholder="tlsCredentialForm.apiTokenSaved ? 'Saved token' : 'Cloudflare API token'"
            :required="!tlsCredentialForm.id"
          />
        </label>
        <p v-if="tlsCredentialError" class="rounded-md border border-red-900/50 bg-red-950/20 px-3 py-2 text-sm text-red-400">
          {{ tlsCredentialError }}
        </p>
        <NCheckbox v-model:checked="tlsCredentialForm.enabled" class="mt-2">
          Enabled
        </NCheckbox>
        <div class="mt-4 flex justify-end gap-3">
          <NButton secondary attr-type="button" @click="isTlsCredentialModalOpen = false">Cancel</NButton>
          <DisabledHint :disabled="Boolean(tlsCredentialSubmitDisabledReason)" :reason="tlsCredentialSubmitDisabledReason">
            <NButton type="primary" attr-type="submit" :disabled="Boolean(tlsCredentialSubmitDisabledReason)">
              {{ tlsCredentialForm.id ? 'Save Credential' : 'Create Credential' }}
            </NButton>
          </DisabledHint>
        </div>
      </form>
    </NModal>
  </div>
</template>
