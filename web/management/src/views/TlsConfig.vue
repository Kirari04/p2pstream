<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import PencilIcon from "@primevue/icons/pencil";
import PlusIcon from "@primevue/icons/plus";
import RefreshIcon from "@primevue/icons/refresh";
import TrashIcon from "@primevue/icons/trash";
import { useManagementClient } from "@/composables/useManagementClient";
import ConfirmDialog from "@/components/ConfirmDialog.vue";
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
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import Tag from "@/volt/Tag.vue";
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
const { state: confirmState, confirm, handleConfirm: onConfirm, handleCancel: onCancel } = useConfirmDialog();

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

async function handleTlsFileChange(field: TlsFileField, event: Event) {
  tlsUploadError.value = "";
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
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
        <SecondaryButton size="small" label="Add DNS Credential" @click="openAddTlsCredentialModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
        <SecondaryButton size="small" label="Add Certificate" @click="openAddTlsModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </div>
    </div>

    <section class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div v-for="card in summaryCards" :key="card.label" class="vercel-card p-4">
        <p class="text-xs font-semibold uppercase tracking-widest text-[#666]">{{ card.label }}</p>
        <p class="mt-2 text-2xl font-semibold text-white">{{ card.value }}</p>
        <p class="mt-1 text-xs text-[#777]">{{ card.detail }}</p>
      </div>
    </section>

    <section class="vercel-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">TLS Certificates</h4>
          <p class="mt-0.5 text-xs text-[#666] normal-case tracking-normal">Certificates for HTTPS listeners.</p>
        </div>
        <SecondaryButton size="small" label="Add Certificate" @click="openAddTlsModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div v-for="cert in tlsCertificates" :key="cert.id.toString()" class="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ listenerName(cert.listenerId, listeners) }} / {{ cert.hostnamePattern }}</p>
              <Tag v-if="isDefaultSelfSignedCertificate(cert)" value="Self-signed" severity="info" />
              <Tag v-else :value="tlsSourceLabel(cert)" severity="info" />
              <Tag :value="tlsStatusLabel(cert)" :severity="tlsStatusSeverity(cert)" />
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
              <SecondaryButton
                size="small"
                aria-label="Renew TLS certificate"
                title="Renew TLS certificate"
                :disabled="Boolean(busyDisabledReason)"
                @click="renewTlsCertificate(cert.id)"
              >
                <template #icon><RefreshIcon class="h-3.5 w-3.5" /></template>
              </SecondaryButton>
            </DisabledHint>
            <SecondaryButton size="small" aria-label="Edit TLS mapping" title="Edit TLS mapping" @click="editTlsCertificate(cert.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <DangerButton size="small" aria-label="Delete TLS mapping" title="Delete TLS mapping" @click="deleteTlsCertificate(cert.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
        <div v-if="httpsListeners.length && !tlsCertificates.length" class="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ httpsListeners[0]?.name ?? "HTTPS listener" }} / p2pstream.local</p>
              <Tag value="Self-signed" severity="info" />
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

    <section class="vercel-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">DNS Credentials</h4>
          <p class="mt-0.5 text-xs text-[#666] normal-case tracking-normal">Credentials used for DNS-01 certificate challenges.</p>
        </div>
        <SecondaryButton size="small" label="Add DNS Credential" @click="openAddTlsCredentialModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div v-for="credential in tlsDnsCredentials" :key="credential.id.toString()" class="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ credential.name }}</p>
              <Tag value="Cloudflare" severity="info" />
              <Tag v-if="!credential.enabled" value="Disabled" severity="warn" />
            </div>
            <p class="truncate font-mono text-xs text-[#888]">{{ credential.cloudflareZoneId }}</p>
          </div>
          <div class="flex gap-2">
            <SecondaryButton size="small" aria-label="Edit DNS credential" title="Edit DNS credential" @click="editTlsCredential(credential)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <DangerButton size="small" aria-label="Delete DNS credential" title="Delete DNS credential" @click="deleteTlsCredential(credential.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
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

    <Modal v-model="isTlsModalOpen" :title="tlsForm.id ? 'Edit TLS Mapping' : 'Add TLS Mapping'" max-width="36rem">
      <form @submit.prevent="submitTlsCertificate" class="grid gap-4">
        <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Method
          <div class="grid grid-cols-2 gap-2 sm:grid-cols-4">
            <button
              v-for="method in tlsMethodOptions"
              :key="method.value"
              type="button"
              class="rounded-md border px-2.5 py-2 text-xs font-semibold normal-case tracking-normal transition"
              :class="tlsForm.method === method.value ? 'border-white bg-white text-black' : 'border-[#333] bg-[#0b0b0b] text-[#d4d4d8] hover:border-[#555]'"
              @click="tlsForm.method = method.value"
            >
              {{ method.label }}
            </button>
          </div>
        </div>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          HTTPS listener
          <select v-model="tlsForm.listenerId" class="vercel-input text-sm normal-case tracking-normal" required>
            <option v-for="listener in httpsListeners" :key="listener.id.toString()" :value="listener.id.toString()">{{ listener.name }}</option>
          </select>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Hostname pattern
          <input v-model="tlsForm.hostnamePattern" class="vercel-input text-sm normal-case tracking-normal" placeholder="app.example.com" required />
          <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Exact domain or wildcard prefix (*.example.com).</p>
        </label>
        <div v-if="tlsForm.method !== 'manual'" class="grid gap-3">
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              ACME email
              <input v-model="tlsForm.acmeEmail" class="vercel-input text-sm normal-case tracking-normal" type="email" placeholder="admin@example.com" required />
              <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Used for certificate expiration notices from Let's Encrypt.</p>
            </label>
            <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              CA environment
              <select v-model="tlsForm.acmeCa" class="vercel-input text-sm normal-case tracking-normal">
                <option :value="PublicAcmeCa.LETS_ENCRYPT_PRODUCTION">Let's Encrypt production</option>
                <option :value="PublicAcmeCa.LETS_ENCRYPT_STAGING">Let's Encrypt staging</option>
              </select>
            </label>
          </div>
          <label v-if="tlsForm.method === 'dns_01'" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Cloudflare credential
            <select v-model="tlsForm.dnsCredentialId" class="vercel-input text-sm normal-case tracking-normal" required>
              <option value="">Select credential</option>
              <option v-for="credential in tlsDnsCredentials" :key="credential.id.toString()" :value="credential.id.toString()">{{ credential.name }}</option>
            </select>
          </label>
        </div>
        <div v-if="tlsForm.method === 'manual'" class="grid gap-3">
          <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Certificate material
            <div class="grid grid-cols-2 gap-2">
              <button
                v-for="option in manualTlsMaterialOptions"
                :key="option.value"
                type="button"
                class="rounded-md border px-2.5 py-2 text-xs font-semibold normal-case tracking-normal transition"
                :class="tlsForm.manualMode === option.value ? 'border-white bg-white text-black' : 'border-[#333] bg-[#0b0b0b] text-[#d4d4d8] hover:border-[#555]'"
                @click="tlsForm.manualMode = option.value"
              >
                {{ option.label }}
              </button>
            </div>
          </div>
          <label v-if="tlsForm.manualMode === 'generate'" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Validity days
            <input v-model.number="tlsForm.selfSignedValidityDays" class="vercel-input text-sm normal-case tracking-normal" type="number" min="1" max="3650" step="1" required />
          </label>
          <div v-else class="grid gap-3 sm:grid-cols-2">
            <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              Certificate file
              <input
                class="vercel-input cursor-pointer text-sm normal-case tracking-normal file:mr-3 file:rounded file:border-0 file:bg-white file:px-3 file:py-1.5 file:text-xs file:font-medium file:text-black"
                type="file"
                accept=".pem,.crt,.cer"
                :required="!tlsForm.id"
                @change="handleTlsFileChange('cert', $event)"
              />
              <span v-if="tlsForm.certFileName" class="truncate text-xs normal-case tracking-normal text-[#d4d4d8]">{{ tlsForm.certFileName }}</span>
            </label>
            <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              Private key file
              <input
                class="vercel-input cursor-pointer text-sm normal-case tracking-normal file:mr-3 file:rounded file:border-0 file:bg-white file:px-3 file:py-1.5 file:text-xs file:font-medium file:text-black"
                type="file"
                accept=".pem,.key"
                :required="!tlsForm.id"
                @change="handleTlsFileChange('key', $event)"
              />
              <span v-if="tlsForm.keyFileName" class="truncate text-xs normal-case tracking-normal text-[#d4d4d8]">{{ tlsForm.keyFileName }}</span>
            </label>
          </div>
        </div>
        <p v-if="tlsForm.id && tlsForm.method === 'manual' && tlsForm.manualMode === 'upload'" class="rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-2 text-xs text-[#888]">
          Current certificate is stored in the app config directory.
        </p>
        <p v-if="tlsUploadError" class="rounded-md border border-red-900/50 bg-red-950/20 px-3 py-2 text-sm text-red-400">
          {{ tlsUploadError }}
        </p>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8] mt-2">
          <input v-model="tlsForm.enabled" type="checkbox" />
          Enabled
        </label>
        <div class="mt-4 flex justify-end gap-3">
          <SecondaryButton type="button" label="Cancel" @click="isTlsModalOpen = false" />
          <DisabledHint :disabled="Boolean(tlsSubmitDisabledReason)" :reason="tlsSubmitDisabledReason">
            <Button :label="tlsForm.id ? 'Save Changes' : 'Create TLS Mapping'" type="submit" :disabled="tlsSubmitDisabled" />
          </DisabledHint>
        </div>
      </form>
    </Modal>

    <Modal v-model="isTlsCredentialModalOpen" :title="tlsCredentialForm.id ? 'Edit DNS Credential' : 'Add DNS Credential'" max-width="32rem">
      <form @submit.prevent="submitTlsCredential" class="grid gap-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Name
          <input v-model="tlsCredentialForm.name" class="vercel-input text-sm normal-case tracking-normal" placeholder="cloudflare-prod" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Cloudflare zone ID
          <input v-model="tlsCredentialForm.cloudflareZoneId" class="vercel-input font-mono text-sm normal-case tracking-normal" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          API token
          <input
            v-model="tlsCredentialForm.apiToken"
            class="vercel-input text-sm normal-case tracking-normal"
            type="password"
            autocomplete="new-password"
            :placeholder="tlsCredentialForm.apiTokenSaved ? 'Saved token' : 'Cloudflare API token'"
            :required="!tlsCredentialForm.id"
          />
        </label>
        <p v-if="tlsCredentialError" class="rounded-md border border-red-900/50 bg-red-950/20 px-3 py-2 text-sm text-red-400">
          {{ tlsCredentialError }}
        </p>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8] mt-2">
          <input v-model="tlsCredentialForm.enabled" type="checkbox" />
          Enabled
        </label>
        <div class="mt-4 flex justify-end gap-3">
          <SecondaryButton type="button" label="Cancel" @click="isTlsCredentialModalOpen = false" />
          <DisabledHint :disabled="Boolean(tlsCredentialSubmitDisabledReason)" :reason="tlsCredentialSubmitDisabledReason">
            <Button :label="tlsCredentialForm.id ? 'Save Credential' : 'Create Credential'" type="submit" :disabled="Boolean(tlsCredentialSubmitDisabledReason)" />
          </DisabledHint>
        </div>
      </form>
    </Modal>

    <ConfirmDialog :state="confirmState" @confirm="onConfirm" @cancel="onCancel" />
  </div>
</template>
