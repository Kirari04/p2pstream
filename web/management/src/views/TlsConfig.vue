<script setup lang="ts">
import { computed, h, reactive, ref, watch } from "vue";
import { ChevronRight as ChevronRightIcon } from "@lucide/vue";
import { MoreHorizontal as MoreIcon } from "@lucide/vue";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NAlert, NButton, NCheckbox, NDataTable, NDrawer, NDrawerContent, NDropdown, NInput, NInputNumber, NTag, NUpload } from "naive-ui";
import type { DataTableColumns, DropdownOption, UploadFileInfo } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import PublicTlsCertificateFacts from "@/components/public-tls/PublicTlsCertificateFacts.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import MetricCard from "@/components/ui/MetricCard.vue";
import MetricStrip from "@/components/ui/MetricStrip.vue";
import PageHeader from "@/components/ui/PageHeader.vue";
import SectionCard from "@/components/ui/SectionCard.vue";
import SegmentedControl from "@/components/ui/SegmentedControl.vue";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementContext } from "@/composables/useManagementContext";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";
import { displayHostnamePattern } from "@/lib/idnHostnames";
import {
  acmeChallengeTypeForMethod,
  dnsCredentialName,
  formatUnixMillis,
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
  publicTlsAttentionReason,
  publicTlsAttentionText,
  publicTlsExpiryStatus,
  publicTlsNeedsRenewal,
} from "@/lib/publicTlsPresentation";
import {
  PublicAcmeCa,
  PublicAcmeChallengeType,
  PublicDnsProvider,
  PublicListenerProtocol,
  PublicTlsCertificateSource,
  PublicTlsCertificateStatus,
  type PublicTlsCertificate,
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
  { label: "HTTPS listeners", value: httpsListeners.value.length.toString(), detail: "Certificate targets", tone: "default" as const },
  { label: "Certificates", value: tlsCertificates.value.length.toString(), detail: "Configured mappings", tone: "default" as const },
  { label: "Certificate errors", value: certificateErrors.value.toString(), detail: certificateErrors.value ? "Needs attention" : "None reported", tone: certificateErrors.value ? "error" as const : "default" as const },
  { label: "DNS credentials", value: tlsDnsCredentials.value.length.toString(), detail: "For DNS-01 challenges", tone: "default" as const },
]);

type TlsTableRow =
  | { kind: "certificate"; key: string; cert: PublicTlsCertificate }
  | { kind: "fallback"; key: string; listenerName: string };

const activeCertificateMenuId = ref("");
const tlsTableRows = computed<TlsTableRow[]>(() => {
  if (tlsCertificates.value.length) {
    return tlsCertificates.value.map((cert) => ({ kind: "certificate", key: cert.id.toString(), cert }));
  }
  if (httpsListeners.value.length) {
    return [{ kind: "fallback", key: "fallback", listenerName: httpsListeners.value[0]?.name ?? "HTTPS listener" }];
  }
  return [];
});
const certificateIssues = computed(() => tlsCertificates.value
  .filter((cert) => cert.lastError || tlsCertificateHostnameGuidance(cert))
  .map((cert) => ({ cert, guidance: tlsCertificateHostnameGuidance(cert) })));
const hasRenewalAttention = computed(() => tlsCertificates.value.some(certificateNeedsRenewal));
const certificateActionsWidth = computed(() => hasRenewalAttention.value ? 260 : 190);
const CERTIFICATE_EXPAND_WIDTH = 40;
const certificateTableWidth = computed(() => CERTIFICATE_EXPAND_WIDTH + 170 + 170 + 140 + 170 + certificateActionsWidth.value);

// Lifetime-relative thresholds live in publicTlsPresentation so short-lived
// Let's Encrypt certificates are not flagged for most of their life.
function certificateNeedsRenewal(cert: PublicTlsCertificate): boolean {
  return publicTlsNeedsRenewal(cert);
}

function safeHostname(cert: PublicTlsCertificate, limit = 64): string {
  return diagnosticExcerpt(displayHostnamePattern(cert.hostnamePattern), limit).text;
}

function certificateMenuOptions(cert: PublicTlsCertificate): DropdownOption[] {
  const props = (disabled: boolean) => ({ role: "menuitem", "aria-disabled": disabled ? "true" as const : "false" as const });
  const options: DropdownOption[] = [];
  if (cert.source === PublicTlsCertificateSource.ACME && !certificateNeedsRenewal(cert)) {
    const disabled = Boolean(busyDisabledReason.value);
    options.push({ label: "Renew certificate", key: "renew", disabled, props: props(disabled) });
  }
  options.push({ label: "Delete mapping", key: "delete", props: props(false) });
  return options;
}

function handleCertificateMenuSelect(cert: PublicTlsCertificate, key: string) {
  activeCertificateMenuId.value = "";
  if (key === "renew") {
    void renewTlsCertificate(cert.id);
    return;
  }
  if (key === "delete") void deleteTlsCertificate(cert.id);
}

function muted(text: string, extra = "") {
  return h("span", { class: `clip-text copy-xs muted-text ${extra}`, title: text }, text);
}

function renderCertificateExpandIcon({ expanded, rowData }: { expanded: boolean; rowData: unknown }) {
  const row = rowData as TlsTableRow;
  const host = row.kind === "certificate" ? safeHostname(row.cert, 48) : "";
  return h("button", {
    type: "button",
    class: "tls-expand-button",
    "aria-expanded": expanded ? "true" : "false",
    "aria-label": `${expanded ? "Hide" : "Show"} certificate details for ${host}`,
    title: expanded ? "Hide certificate details" : "Show certificate details",
  }, [h(ChevronRightIcon, { class: "icon-sm tls-expand-icon", "aria-hidden": "true" })]);
}

const certificateColumns = computed<DataTableColumns<TlsTableRow>>(() => [
  {
    type: "expand",
    width: CERTIFICATE_EXPAND_WIDTH,
    expandable: (row) => row.kind === "certificate",
    renderExpand: (row) => row.kind === "certificate"
      ? h(PublicTlsCertificateFacts, { certificate: row.cert, hostnameLabel: safeHostname(row.cert, 48) })
      : null,
  },
  {
    title: "Hostname",
    key: "hostname",
    minWidth: 170,
    render: (row) => {
      if (row.kind === "fallback") {
        return h("div", { class: "tls-cell" }, [
          h("span", { class: "clip-text mono-text copy-sm weight-medium base-text" }, "p2pstream.local"),
          h("bdi", { class: "clip-text copy-xs muted-text", dir: "ltr" }, diagnosticExcerpt(row.listenerName, 48).text),
        ]);
      }
      const listener = listenerName(row.cert.listenerId, listeners.value);
      return h("div", { class: "tls-cell" }, [
        h("bdi", {
          class: "clip-text mono-text copy-sm weight-medium base-text",
          dir: "ltr",
          title: diagnosticInspectionText(row.cert.hostnamePattern),
        }, safeHostname(row.cert)),
        h("bdi", {
          class: "clip-text copy-xs muted-text",
          dir: "ltr",
          title: diagnosticInspectionText(listener),
        }, diagnosticExcerpt(listener, 48).text),
      ]);
    },
  },
  {
    title: "Status",
    key: "status",
    width: 170,
    render: (row) => {
      if (row.kind === "fallback") {
        return h("div", { class: "tls-cell" }, [
          h("div", { class: "tls-tags" }, [
            h(NTag, { size: "small", bordered: false, type: "success" }, { default: () => "Active" }),
            h(NTag, { size: "small", bordered: false }, { default: () => "Self-signed" }),
          ]),
          muted("Runtime fallback certificate"),
        ]);
      }
      const cert = row.cert;
      return h("div", { class: "tls-cell" }, [
        h("div", { class: "tls-tags" }, [
          h(NTag, { size: "small", bordered: false, type: naiveTagType(tlsStatusSeverity(cert)) }, { default: () => tlsStatusLabel(cert) }),
          h(NTag, { size: "small", bordered: false }, {
            default: () => isDefaultSelfSignedCertificate(cert) ? "Self-signed" : tlsSourceLabel(cert),
          }),
        ]),
        certificateStatusDetail(cert),
      ]);
    },
  },
  {
    title: "Expires",
    key: "expires",
    width: 140,
    render: (row) => {
      if (row.kind === "fallback") return muted("Managed automatically");
      const cert = row.cert;
      if (cert.expiresAtUnixMillis === 0n) {
        return h("div", { class: "tls-cell" }, [
          h("span", { class: "copy-sm muted-text" }, "—"),
          muted(cert.source === PublicTlsCertificateSource.ACME ? "Not issued yet" : "Expiry unknown"),
        ]);
      }
      const expiry = publicTlsExpiryStatus(cert);
      const validity = tlsCertificateValiditySummary(cert);
      return h("div", { class: "tls-cell" }, [
        h(NTag, { size: "small", bordered: false, type: expiry.type, class: "fit-width" }, { default: () => expiry.label }),
        h("span", { class: "clip-text copy-xs muted-text tls-tabular", title: validity }, formatUnixMillis(cert.expiresAtUnixMillis)),
      ]);
    },
  },
  {
    title: "Renewal",
    key: "renewal",
    width: 170,
    render: (row) => {
      if (row.kind === "fallback") return muted("No renewal needed");
      const cert = row.cert;
      const renewal = tlsCertificateRenewalSummary(cert);
      const lastAttempt = tlsCertificateLastAttemptSummary(cert);
      const lines = [];
      if (renewal) lines.push(h("span", { class: "clip-text copy-xs base-text", title: renewal }, renewal));
      if (lastAttempt) lines.push(muted(lastAttempt));
      if (cert.source === PublicTlsCertificateSource.ACME && cert.dnsCredentialId) {
        const credential = dnsCredentialName(cert.dnsCredentialId, tlsDnsCredentials.value);
        lines.push(h("bdi", {
          class: "clip-text copy-xs muted-text",
          dir: "ltr",
          title: diagnosticInspectionText(credential),
        }, `Cloudflare · ${diagnosticExcerpt(credential, 40).text}`));
      }
      if (!renewal && !lastAttempt) lines.push(muted("Managed manually"));
      return h("div", { class: "tls-cell" }, lines);
    },
  },
  {
    title: "Actions",
    key: "actions",
    width: certificateActionsWidth.value,
    align: "right",
    fixed: "right",
    render: (row) => {
      if (row.kind === "fallback") return h("span", { class: "copy-xs muted-text" }, "System managed");
      const cert = row.cert;
      const menuId = `tls-certificate-actions-${cert.id.toString()}`;
      return h("div", { class: "tls-actions" }, [
        certificateNeedsRenewal(cert)
          ? h(DisabledHint, { disabled: Boolean(busyDisabledReason.value), reason: busyDisabledReason.value }, {
            default: () => h(NButton, {
              secondary: true,
              size: "small",
              "aria-label": "Renew TLS certificate",
              title: "Renew TLS certificate",
              disabled: Boolean(busyDisabledReason.value),
              onClick: () => void renewTlsCertificate(cert.id),
            }, {
              icon: () => h(RefreshIcon, { class: "icon-sm" }),
              default: () => "Renew",
            }),
          })
          : null,
        h(NButton, {
          secondary: true,
          size: "small",
          "aria-label": "Edit TLS mapping",
          title: "Edit TLS mapping",
          onClick: () => editTlsCertificate(cert.id),
        }, {
          icon: () => h(PencilIcon, { class: "icon-sm" }),
          default: () => "Edit",
        }),
        h(NDropdown, {
          trigger: "click",
          options: certificateMenuOptions(cert),
          show: activeCertificateMenuId.value === cert.id.toString(),
          menuProps: () => ({ id: menuId, role: "menu", "aria-label": `Actions for ${safeHostname(cert, 48)}` }),
          "onUpdate:show": (show: boolean) => {
            activeCertificateMenuId.value = show ? cert.id.toString() : "";
          },
          onSelect: (key: string | number) => handleCertificateMenuSelect(cert, String(key)),
        }, {
          default: () => h(NButton, {
            secondary: true,
            size: "small",
            "aria-label": `More actions for ${safeHostname(cert, 48)}`,
            "aria-controls": menuId,
            "aria-expanded": activeCertificateMenuId.value === cert.id.toString(),
            "aria-haspopup": "menu",
          }, {
            icon: () => h(MoreIcon, { class: "icon-sm" }),
            default: () => "More",
          }),
        }),
      ]);
    },
  },
]);

function certificateStatusDetail(cert: PublicTlsCertificate) {
  if (cert.lastError) return h("span", { class: "clip-text copy-xs error-text" }, "Error details below");
  const reason = publicTlsAttentionReason(cert);
  if (reason && reason !== "error") {
    const text = publicTlsAttentionText(reason);
    return h("span", { class: "clip-text copy-xs warning-text", title: text }, text);
  }
  return muted(tlsCertificateSummary(cert));
}

function tlsRowKey(row: TlsTableRow): string {
  return row.key;
}

function tlsRowProps(row: TlsTableRow): Record<string, string> {
  return row.kind === "certificate" ? { "data-testid": `tls-row-${row.key}` } : {};
}

const credentialColumns = computed<DataTableColumns<PublicTlsDnsCredential>>(() => [
  {
    title: "Credential",
    key: "name",
    minWidth: 200,
    render: (credential) => h("div", { class: "tls-cell" }, [
      h("bdi", {
        class: "clip-text copy-sm weight-medium base-text",
        dir: "ltr",
        title: diagnosticInspectionText(credential.name),
      }, diagnosticExcerpt(credential.name, 64).text),
      muted("DNS-01 validation"),
    ]),
  },
  {
    title: "Provider",
    key: "provider",
    width: 120,
    render: () => h(NTag, { size: "small", bordered: false }, { default: () => "Cloudflare" }),
  },
  {
    title: "Zone ID",
    key: "zone",
    width: 260,
    render: (credential) => h("bdi", {
      class: "clip-text mono-text copy-xs base-text tls-block",
      dir: "ltr",
      title: diagnosticInspectionText(credential.cloudflareZoneId),
    }, diagnosticExcerpt(credential.cloudflareZoneId, 48).text),
  },
  {
    title: "State",
    key: "state",
    width: 150,
    render: (credential) => h("div", { class: "tls-cell" }, [
      h(NTag, { size: "small", bordered: false, type: credential.enabled ? "success" : "warning", class: "fit-width" }, {
        default: () => credential.enabled ? "Enabled" : "Disabled",
      }),
      muted(credential.apiTokenSet ? "Secret saved" : "Secret missing"),
    ]),
  },
  {
    title: "Actions",
    key: "actions",
    width: 190,
    align: "right",
    fixed: "right",
    render: (credential) => h("div", { class: "tls-actions" }, [
      h(NButton, {
        secondary: true,
        size: "small",
        "aria-label": "Edit DNS credential",
        title: "Edit DNS credential",
        onClick: () => editTlsCredential(credential),
      }, {
        icon: () => h(PencilIcon, { class: "icon-sm" }),
        default: () => "Edit",
      }),
      h(NButton, {
        type: "error",
        secondary: true,
        size: "small",
        "aria-label": "Delete DNS credential",
        title: "Delete DNS credential",
        onClick: () => void deleteTlsCredential(credential.id),
      }, {
        icon: () => h(TrashIcon, { class: "icon-sm" }),
        default: () => "Delete",
      }),
    ]),
  },
]);

function credentialRowKey(credential: PublicTlsDnsCredential): string {
  return credential.id.toString();
}

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
function canonicalTlsHostnamePattern(value: string) {
  const trimmed = value.trim().toLowerCase().replace(/\.$/, "");
  if (!trimmed) return "";
  const wildcard = trimmed.startsWith("*.");
  const hostname = wildcard ? trimmed.slice(2) : trimmed;
  try {
    const canonical = new URL(`https://${hostname}`).hostname;
    return wildcard ? `*.${canonical}` : canonical;
  } catch {
    return "";
  }
}
const canonicalTlsHostname = computed(() => canonicalTlsHostnamePattern(tlsForm.hostnamePattern));
const tlsHostnameWillNormalize = computed(() =>
  Boolean(canonicalTlsHostname.value) &&
  canonicalTlsHostname.value !== tlsForm.hostnamePattern.trim().toLowerCase().replace(/\.$/, ""),
);
function tlsCertificateHostnameGuidance(cert: PublicTlsCertificate) {
  const canonical = canonicalTlsHostnamePattern(cert.hostnamePattern);
  if (!canonical || canonical === cert.hostnamePattern) return "";
  return `This internationalized hostname must be requested as ${canonical}. Retry issuance and the mapping will be repaired automatically.`;
}
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
  tlsForm.hostnamePattern = displayHostnamePattern(cert.hostnamePattern);
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
    <PageHeader title="TLS" description="HTTPS certificate mappings and DNS credentials.">
      <template #actions>
        <NButton secondary size="small" @click="openAddTlsCredentialModal">
          <template #icon><PlusIcon class="icon-sm" /></template>
          Add DNS Credential
        </NButton>
        <NButton type="primary" size="small" @click="openAddTlsModal">
          <template #icon><PlusIcon class="icon-sm" /></template>
          Add Certificate
        </NButton>
      </template>
    </PageHeader>

    <MetricStrip label="TLS summary">
      <MetricCard
        v-for="card in summaryCards"
        :key="card.label"
        :label="card.label"
        :value="card.value"
        :detail="card.detail"
        :tone="card.tone"
      />
    </MetricStrip>

    <SectionCard title="TLS certificates" description="Certificates served by HTTPS listeners, matched by hostname." flush>
      <template #badge><NTag size="small" :bordered="false">{{ tlsCertificates.length }}</NTag></template>
      <NDataTable
        v-if="tlsTableRows.length"
        :columns="certificateColumns"
        :data="tlsTableRows"
        :row-key="tlsRowKey"
        :row-props="tlsRowProps"
        :render-expand-icon="renderCertificateExpandIcon"
        sticky-expanded-rows
        :pagination="false"
        :bordered="false"
       
        :scroll-x="certificateTableWidth"
        table-layout="fixed"
        size="small"
        aria-label="TLS certificate mappings"
      />
      <EmptyState
        v-if="!httpsListeners.length"
        title="No HTTPS listeners configured"
        description="Create an HTTPS listener on the Proxy page before adding certificate mappings."
      />
      <template v-if="certificateIssues.length" #footer>
        <div class="tls-issues" role="list" aria-label="Certificate issues">
          <div v-for="issue in certificateIssues" :key="issue.cert.id.toString()" class="tls-issue" role="listitem">
            <bdi dir="ltr" class="clip-text mono-text copy-xs weight-medium base-text" :title="diagnosticInspectionText(issue.cert.hostnamePattern)">
              {{ safeHostname(issue.cert) }}
            </bdi>
            <p v-if="issue.cert.lastError" class="tls-issue__text error-text">
              Last error: <bdi dir="ltr">{{ diagnosticInspectionText(issue.cert.lastError) }}</bdi>
            </p>
            <p v-if="issue.guidance" class="tls-issue__text base-text">
              <bdi dir="ltr">{{ diagnosticInspectionText(issue.guidance) }}</bdi>
            </p>
          </div>
        </div>
      </template>
    </SectionCard>

    <SectionCard title="DNS credentials" description="Credentials used for DNS-01 certificate challenges." flush>
      <template #badge><NTag size="small" :bordered="false">{{ tlsDnsCredentials.length }}</NTag></template>
      <NDataTable
        v-if="tlsDnsCredentials.length"
        :columns="credentialColumns"
        :data="tlsDnsCredentials"
        :row-key="credentialRowKey"
        :pagination="false"
        :bordered="false"
       
        :scroll-x="900"
        table-layout="fixed"
        size="small"
        aria-label="DNS credentials"
      />
      <EmptyState
        v-else
        title="No DNS credentials configured"
        description="Add a Cloudflare DNS credential when you need DNS-01 validation or wildcard certificates."
        action-label="Add DNS Credential"
        @action="openAddTlsCredentialModal"
      />
    </SectionCard>

    <NDrawer
      v-model:show="isTlsModalOpen"
      placement="right"
      :width="editorDrawerWidth('36rem')"
      :aria-label="tlsForm.id ? 'Edit TLS Mapping' : 'Add TLS Mapping'"
      class="editor-drawer"
    >
      <NDrawerContent :title="tlsForm.id ? 'Edit TLS Mapping' : 'Add TLS Mapping'" closable>
      <form class="editor-drawer-form layout-grid space-xl" @submit.prevent="submitTlsCertificate">
        <section class="tls-editor-group" aria-labelledby="tls-editor-mapping-heading">
          <h3 id="tls-editor-mapping-heading" class="tls-editor-group__title">Mapping</h3>
          <div class="layout-grid space-xs copy-xs weight-medium muted-text">
            Method
            <SegmentedControl class="tls-segmented" v-model="tlsForm.method" :options="tlsMethodOptions" label="Certificate method" />
          </div>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            HTTPS listener
            <AccessibleSelect v-model:value="tlsForm.listenerId" accessible-label="HTTPS listener" size="small" :options="httpsListenerOptions" required />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Hostname pattern
            <NInput v-model:value="tlsForm.hostnamePattern" size="small" placeholder="app.example.com" required />
            <span class="tls-help">Exact domain or wildcard prefix (*.example.com).</span>
            <span v-if="tlsHostnameWillNormalize" class="tls-hostname-preview">
              Certificate identifier:
              <code><bdi dir="ltr">{{ diagnosticInspectionText(canonicalTlsHostname) }}</bdi></code>
            </span>
          </label>
        </section>

        <section v-if="tlsForm.method !== 'manual'" class="tls-editor-group" aria-labelledby="tls-editor-acme-heading">
          <h3 id="tls-editor-acme-heading" class="tls-editor-group__title">ACME issuance</h3>
          <div class="layout-grid space-lg mq-sm-cols-two">
            <label class="layout-grid space-xs copy-xs weight-medium muted-text">
              ACME email
              <NInput v-model:value="tlsForm.acmeEmail" size="small" type="text" placeholder="admin@example.com" required />
              <span class="tls-help">Used for certificate expiration notices from Let's Encrypt.</span>
            </label>
            <label class="layout-grid space-xs copy-xs weight-medium muted-text">
              CA environment
              <AccessibleSelect v-model:value="tlsForm.acmeCa" accessible-label="CA environment" size="small" :options="acmeCaOptions" />
            </label>
          </div>
          <label v-if="tlsForm.method === 'dns_01'" class="layout-grid space-xs copy-xs weight-medium muted-text">
            Cloudflare credential
            <AccessibleSelect v-model:value="tlsForm.dnsCredentialId" accessible-label="Cloudflare credential" size="small" :options="dnsCredentialOptions" required />
          </label>
        </section>

        <section v-else class="tls-editor-group" aria-labelledby="tls-editor-material-heading">
          <h3 id="tls-editor-material-heading" class="tls-editor-group__title">Certificate material</h3>
          <div class="layout-grid space-xs copy-xs weight-medium muted-text">
            Source
            <SegmentedControl class="tls-segmented" v-model="tlsForm.manualMode" :options="manualTlsMaterialOptions" label="Certificate material source" />
          </div>
          <label v-if="tlsForm.manualMode === 'generate'" class="layout-grid space-xs copy-xs weight-medium muted-text">
            Validity days
            <NInputNumber :show-button="false" v-model:value="tlsForm.selfSignedValidityDays" size="small" :min="1" :max="3650" :step="1" required />
          </label>
          <template v-else>
            <div class="layout-grid space-lg mq-sm-cols-two">
              <div class="layout-grid space-xs copy-xs weight-medium muted-text">
                Certificate file
                <NUpload
                  :default-upload="false"
                  :max="1"
                  accept=".pem,.crt,.cer"
                  @change="handleTlsUploadChange('cert', $event)"
                >
                  <NButton secondary size="small" attr-type="button">Choose certificate</NButton>
                </NUpload>
                <bdi v-if="tlsForm.certFileName" dir="ltr" class="clip-text copy-xs weight-normal base-text">{{ diagnosticInspectionText(tlsForm.certFileName) }}</bdi>
              </div>
              <div class="layout-grid space-xs copy-xs weight-medium muted-text">
                Private key file
                <NUpload
                  :default-upload="false"
                  :max="1"
                  accept=".pem,.key"
                  @change="handleTlsUploadChange('key', $event)"
                >
                  <NButton secondary size="small" attr-type="button">Choose private key</NButton>
                </NUpload>
                <bdi v-if="tlsForm.keyFileName" dir="ltr" class="clip-text copy-xs weight-normal base-text">{{ diagnosticInspectionText(tlsForm.keyFileName) }}</bdi>
              </div>
            </div>
            <p v-if="tlsForm.id" class="tls-help">
              Current certificate is stored in the app config directory.
            </p>
          </template>
        </section>

        <NAlert v-if="tlsUploadError" type="error" :bordered="false">
          {{ tlsUploadError }}
        </NAlert>
        <NCheckbox v-model:checked="tlsForm.enabled">
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
        <label class="layout-grid space-xs copy-xs weight-medium muted-text">
          Name
          <NInput v-model:value="tlsCredentialForm.name" size="small" placeholder="cloudflare-prod" required />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium muted-text">
          Cloudflare zone ID
          <NInput v-model:value="tlsCredentialForm.cloudflareZoneId" size="small" class="mono-text" placeholder="32-character zone ID from the Cloudflare dashboard" required />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium muted-text">
          API token
          <NInput
            v-model:value="tlsCredentialForm.apiToken"
            size="small"
            type="password"
            :placeholder="tlsCredentialForm.apiTokenSaved ? 'Saved token' : 'Cloudflare API token'"
            :required="!tlsCredentialForm.id"
          />
        </label>
        <NAlert v-if="tlsCredentialError" type="error" :bordered="false">
          {{ tlsCredentialError }}
        </NAlert>
        <NCheckbox v-model:checked="tlsCredentialForm.enabled">
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
.tls-segmented {
  justify-self: start;
}

:deep(.tls-cell) {
  display: grid;
  min-width: 0;
  justify-items: start;
  gap: 0.25rem;
}

:deep(.tls-cell > *) {
  max-width: 100%;
}

:deep(.tls-block) {
  display: block;
  max-width: 100%;
}

:deep(.tls-tags) {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  gap: 0.25rem;
}

:deep(.tls-tabular) {
  font-variant-numeric: tabular-nums;
}

:deep(.n-data-table-expand-trigger) {
  width: auto;
  height: auto;
  vertical-align: middle;
}

:deep(.tls-expand-button) {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.5rem;
  height: 1.5rem;
  border: 0;
  border-radius: 6px;
  padding: 0;
  background: transparent;
  color: var(--app-text-muted);
  cursor: pointer;
}

:deep(.tls-expand-button:hover) {
  background: var(--app-hover);
  color: var(--app-text);
}

:deep(.tls-expand-button:focus-visible) {
  outline: 2px solid var(--app-accent);
  outline-offset: 1px;
}

:deep(.tls-expand-icon) {
  transition: transform 0.15s ease;
}

:deep(.tls-expand-button[aria-expanded="true"] .tls-expand-icon) {
  transform: rotate(90deg);
}

@media (prefers-reduced-motion: reduce) {
  :deep(.tls-expand-icon) {
    transition: none;
  }
}

:deep(.tls-actions) {
  display: flex;
  justify-content: flex-end;
  gap: 0.375rem;
}

.tls-issues {
  display: grid;
  gap: 0.75rem;
}

.tls-issue {
  display: grid;
  min-width: 0;
  gap: 0.25rem;
}

.tls-issue + .tls-issue {
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 0.75rem;
}

.tls-issue__text {
  max-height: 4.5rem;
  overflow: auto;
  font-size: 0.75rem;
  line-height: 1.45;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.tls-editor-group {
  display: grid;
  min-width: 0;
  gap: 1rem;
}

.tls-editor-group + .tls-editor-group {
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 1.25rem;
}

.tls-editor-group__title {
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
}

.tls-help {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-weight: 400;
  line-height: 1.45;
}

.tls-hostname-preview {
  border-radius: 8px;
  padding: 0.375rem 0.625rem;
  background: var(--app-panel-muted);
  color: var(--app-text);
  font-size: 0.75rem;
  font-weight: 400;
}

.tls-hostname-preview code {
  font-family: var(--font-mono);
  font-weight: 600;
}

@media (pointer: coarse) {
  :deep(.tls-actions .n-button) {
    min-height: 2.75rem;
  }
}
</style>
