<script setup lang="ts">
import { computed, inject, nextTick, reactive, ref } from "vue";
import {
  NButton,
  NButtonGroup,
  NCheckbox,
  NDrawer,
  NDrawerContent,
  NInput,
  NInputNumber,
  NUpload,
} from "naive-ui";
import type { UploadFileInfo } from "naive-ui";
import DisabledHint from "@/components/DisabledHint.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import {
  isBusyKey,
  runManagementActionKey,
} from "@/composables/managementContextKeys";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementClient } from "@/composables/useManagementClient";
import { BUSY_REASON } from "@/lib/disabledReasons";
import {
  asciiHostnamePattern,
  displayHostnamePattern,
} from "@/lib/idnHostnames";
import {
  acmeChallengeTypeForMethod,
  tlsMethodForCertificate,
  tlsSourceForMethod,
  type TlsMethod,
} from "@/lib/publicProxyLabels";
import { editorDrawerWidth } from "@/lib/naiveUi";
import {
  PublicAcmeCa,
  PublicAcmeChallengeType,
  PublicListenerProtocol,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

type ManualMode = "generate" | "upload";
const props = defineProps<{ config: GetPublicProxyConfigResponse | null }>();
const emit = defineEmits<{ (event: "saved"): void }>();
const client = useManagementClient();
const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(
  isBusyKey,
  computed(() => false),
);
const { confirm } = useConfirmDialog();
const isOpen = ref(false);
const initialSnapshot = ref("");
let returnFocusElement: HTMLElement | null = null;
const form = reactive({
  id: "",
  listenerId: "",
  hostnamePattern: "",
  method: "manual" as TlsMethod,
  manualMode: "generate" as ManualMode,
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
const methods: Array<{ label: string; value: TlsMethod }> = [
  { label: "Manual", value: "manual" },
  { label: "HTTP-01", value: "http_01" },
  { label: "TLS-ALPN", value: "tls_alpn_01" },
  { label: "DNS-01", value: "dns_01" },
];
const manualModes: Array<{ label: string; value: ManualMode }> = [
  { label: "Generate self-signed", value: "generate" },
  { label: "Upload PEM", value: "upload" },
];
const caOptions = [
  {
    label: "Let's Encrypt production",
    value: PublicAcmeCa.LETS_ENCRYPT_PRODUCTION,
  },
  { label: "Let's Encrypt staging", value: PublicAcmeCa.LETS_ENCRYPT_STAGING },
];
const httpsListeners = computed(() =>
  (props.config?.listeners ?? []).filter(
    (listener) => listener.protocol === PublicListenerProtocol.HTTPS,
  ),
);
const listenerOptions = computed(() =>
  httpsListeners.value.map((listener) => ({
    label: `${listener.name} · :${listener.port.toString()}`,
    value: listener.id.toString(),
  })),
);
const credentialOptions = computed(() => [
  { label: "Select credential", value: "" },
  ...(props.config?.tlsDnsCredentials ?? []).map((credential) => ({
    label: credential.name,
    value: credential.id.toString(),
  })),
]);
const snapshot = computed(() => JSON.stringify(form));
const dirty = computed(
  () =>
    isOpen.value &&
    initialSnapshot.value !== "" &&
    snapshot.value !== initialSnapshot.value,
);
const partialUpload = computed(
  () => Boolean(form.certPem) !== Boolean(form.keyPem),
);
const submitDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  if (!form.listenerId) return "Choose an HTTPS listener.";
  if (!form.hostnamePattern.trim())
    return "Enter the hostname this certificate covers.";
  if (
    form.method === "manual" &&
    form.manualMode === "generate" &&
    (!Number.isInteger(form.selfSignedValidityDays) ||
      form.selfSignedValidityDays < 1 ||
      form.selfSignedValidityDays > 3650)
  )
    return "Enter certificate validity between 1 and 3650 days.";
  if (
    form.method === "manual" &&
    form.manualMode === "upload" &&
    ((!form.id && (!form.certPem || !form.keyPem)) || partialUpload.value)
  )
    return "Upload both the certificate and private key files.";
  if (form.method !== "manual" && !form.acmeEmail.trim())
    return "Enter the ACME account email.";
  if (
    form.hostnamePattern.trim().startsWith("*.") &&
    form.method !== "dns_01" &&
    form.method !== "manual"
  )
    return "Wildcard certificates require DNS-01.";
  if (form.method === "dns_01" && !form.dnsCredentialId)
    return "Choose a Cloudflare DNS credential.";
  return "";
});

function hostnameMatches(pattern: string, hostname: string): boolean {
  const candidate = asciiHostnamePattern(pattern.trim().toLocaleLowerCase());
  const requested = asciiHostnamePattern(hostname.trim().toLocaleLowerCase());
  if (candidate === requested) return true;
  if (!candidate.startsWith("*.")) return false;
  const suffix = candidate.slice(2);
  return (
    requested.endsWith(`.${suffix}`) &&
    requested.split(".").length === suffix.split(".").length + 1
  );
}
function openFor(listenerId: bigint | string = "", hostnamePattern = "") {
  const requestedListener = listenerId.toString();
  const certificate = (props.config?.tlsCertificates ?? [])
    .filter(
      (item) =>
        item.listenerId.toString() === requestedListener &&
        hostnameMatches(item.hostnamePattern, hostnamePattern),
    )
    .sort(
      (left, right) =>
        Number(
          asciiHostnamePattern(left.hostnamePattern) !==
            asciiHostnamePattern(hostnamePattern),
        ) -
        Number(
          asciiHostnamePattern(right.hostnamePattern) !==
            asciiHostnamePattern(hostnamePattern),
        ),
    )[0];
  form.id = certificate?.id.toString() ?? "";
  form.listenerId = httpsListeners.value.some(
    (listener) => listener.id.toString() === requestedListener,
  )
    ? requestedListener
    : (httpsListeners.value[0]?.id.toString() ?? "");
  form.hostnamePattern = displayHostnamePattern(certificate?.hostnamePattern ?? hostnamePattern);
  form.method = certificate
    ? tlsMethodForCertificate(certificate)
    : hostnamePattern.startsWith("*.")
      ? "dns_01"
      : "manual";
  form.manualMode = certificate ? "upload" : "generate";
  form.selfSignedValidityDays = 3650;
  form.acmeEmail = certificate?.acmeEmail ?? "";
  form.acmeCa = certificate?.acmeCa || PublicAcmeCa.LETS_ENCRYPT_PRODUCTION;
  form.dnsCredentialId =
    certificate?.dnsCredentialId.toString() ||
    props.config?.tlsDnsCredentials[0]?.id.toString() ||
    "";
  form.certPem = null;
  form.keyPem = null;
  form.certFileName = "";
  form.keyFileName = "";
  form.enabled = certificate?.enabled ?? true;
  returnFocusElement =
    document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
  isOpen.value = true;
  void nextTick(() => {
    initialSnapshot.value = snapshot.value;
  });
}
async function handleUpload(
  field: "cert" | "key",
  options: { fileList: UploadFileInfo[] },
) {
  const file = options.fileList.at(-1)?.file ?? null;
  const bytes = file ? new Uint8Array(await file.arrayBuffer()) : null;
  if (field === "cert") {
    form.certPem = bytes;
    form.certFileName = file?.name ?? "";
  } else {
    form.keyPem = bytes;
    form.keyFileName = file?.name ?? "";
  }
}
function forceClose() {
  isOpen.value = false;
  initialSnapshot.value = "";
  const target = returnFocusElement;
  returnFocusElement = null;
  void nextTick(() => target?.focus());
}
async function close() {
  if (
    dirty.value &&
    !(await confirm(
      "Discard TLS mapping?",
      "This certificate mapping has unsaved changes.",
      "Discard changes",
    ))
  )
    return;
  forceClose();
}
function handleDrawerVisibility(show: boolean) {
  if (!show) void close();
}
async function submit() {
  if (!runManagementAction || submitDisabledReason.value) return;
  const ok = await runManagementAction(async () => {
    const manual = form.method === "manual";
    const generateSelfSigned = manual && form.manualMode === "generate";
    const payload = {
      listenerId: BigInt(form.listenerId),
      hostnamePattern: form.hostnamePattern.trim().toLocaleLowerCase(),
      enabled: form.enabled,
      certPem:
        manual && form.manualMode === "upload"
          ? (form.certPem ?? new Uint8Array())
          : new Uint8Array(),
      keyPem:
        manual && form.manualMode === "upload"
          ? (form.keyPem ?? new Uint8Array())
          : new Uint8Array(),
      source: tlsSourceForMethod(form.method),
      acmeChallengeType: manual
        ? PublicAcmeChallengeType.UNSPECIFIED
        : acmeChallengeTypeForMethod(form.method),
      acmeCa: manual ? PublicAcmeCa.UNSPECIFIED : form.acmeCa,
      acmeEmail: manual ? "" : form.acmeEmail.trim(),
      dnsCredentialId:
        form.method === "dns_01" ? BigInt(form.dnsCredentialId) : 0n,
      generateSelfSigned,
      selfSignedValidityDays: generateSelfSigned
        ? BigInt(form.selfSignedValidityDays)
        : 0n,
    };
    if (form.id)
      await client.updatePublicTlsCertificate({
        id: BigInt(form.id),
        ...payload,
      });
    else await client.createPublicTlsCertificate(payload);
  });
  if (!ok) return;
  forceClose();
  emit("saved");
}
defineExpose({ openFor, close });
</script>

<template>
  <NDrawer
    :show="isOpen"
    to="body"
    placement="right"
    :width="editorDrawerWidth('38rem')"
    :aria-label="form.id ? 'Edit TLS for Site' : 'Configure TLS for Site'"
    class="editor-drawer"
    @update:show="handleDrawerVisibility"
  >
    <NDrawerContent
      :title="form.id ? 'Edit TLS for Site' : 'Configure TLS for Site'"
      closable
    >
      <form
        class="editor-drawer-form layout-grid space-lg"
        @submit.prevent="submit"
      >
        <p
          class="round-md framed frame-standard muted-bg pad-md copy-xs line-normal muted-text"
        >
          {{
            form.id
              ? "Repair this certificate mapping without leaving the Site workspace."
              : "Create a certificate mapping without leaving the Site workspace."
          }}
          Readiness refreshes after it is saved.
        </p>
        <div
          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
        >
          Method<NButtonGroup
            class="layout-grid cols-two space-sm mq-sm-cols-four"
            size="small"
            role="group"
            aria-label="Certificate method"
            ><NButton
              v-for="method in methods"
              :key="method.value"
              attr-type="button"
              :type="form.method === method.value ? 'primary' : 'default'"
              :aria-pressed="form.method === method.value"
              @click="form.method = method.value"
              >{{ method.label }}</NButton
            ></NButtonGroup
          >
        </div>
        <label
          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >HTTPS listener<AccessibleSelect
            v-model:value="form.listenerId"
            accessible-label="HTTPS listener"
            size="small"
            :options="listenerOptions"
            required
        /></label>
        <label
          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >Hostname pattern<NInput
            v-model:value="form.hostnamePattern"
            size="small"
            placeholder="app.example.com"
            required
          /><span class="normal-text letter-normal"
            >Exact hostname or one-label wildcard.</span
          ></label
        >
        <template v-if="form.method === 'manual'">
          <div
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >
            Certificate material<NButtonGroup
              class="layout-grid cols-two space-sm"
              size="small"
              role="group"
              aria-label="Certificate material source"
              ><NButton
                v-for="option in manualModes"
                :key="option.value"
                attr-type="button"
                :type="form.manualMode === option.value ? 'primary' : 'default'"
                :aria-pressed="form.manualMode === option.value"
                @click="form.manualMode = option.value"
                >{{ option.label }}</NButton
              ></NButtonGroup
            >
          </div>
          <label
            v-if="form.manualMode === 'generate'"
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
            >Self-signed validity days<NInputNumber
              v-model:value="form.selfSignedValidityDays"
              :show-button="false"
              size="small"
              :min="1"
              :max="3650"
              required
            /><span class="normal-text letter-normal"
              >Useful for internal and test environments. Use ACME for public
              production traffic.</span
            ></label
          >
          <div v-else class="layout-grid space-lg mq-sm-cols-two">
            <div
              class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
            >
              Certificate file<NUpload
                :default-upload="false"
                :max="1"
                accept=".pem,.crt,.cer"
                @change="handleUpload('cert', $event)"
                ><NButton secondary size="small" attr-type="button"
                  >Choose certificate</NButton
                ></NUpload
              ><span
                v-if="form.certFileName"
                class="clip-text normal-text letter-normal"
                >{{ form.certFileName }}</span
              >
            </div>
            <div
              class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
            >
              Private key file<NUpload
                :default-upload="false"
                :max="1"
                accept=".pem,.key"
                @change="handleUpload('key', $event)"
                ><NButton secondary size="small" attr-type="button"
                  >Choose private key</NButton
                ></NUpload
              ><span
                v-if="form.keyFileName"
                class="clip-text normal-text letter-normal"
                >{{ form.keyFileName }}</span
              >
            </div>
          </div>
          <p
            v-if="form.id && form.manualMode === 'upload'"
            class="round-md framed frame-standard muted-bg pad-md copy-xs muted-text"
          >
            Leave both files empty to keep the currently stored certificate
            material.
          </p>
        </template>
        <template v-else
          ><div class="layout-grid space-lg mq-sm-cols-two">
            <label
              class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
              >ACME email<NInput
                v-model:value="form.acmeEmail"
                size="small"
                placeholder="admin@example.com"
                required /></label
            ><label
              class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
              >CA environment<AccessibleSelect
                v-model:value="form.acmeCa"
                accessible-label="CA environment"
                size="small"
                :options="caOptions"
            /></label>
          </div>
          <label
            v-if="form.method === 'dns_01'"
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
            >Cloudflare credential<AccessibleSelect
              v-model:value="form.dnsCredentialId"
              accessible-label="Cloudflare credential"
              size="small"
              :options="credentialOptions"
              required
            /><span class="normal-text letter-normal"
              >DNS credentials are managed on the TLS page.</span
            ></label
          ></template
        >
        <NCheckbox v-model:checked="form.enabled">Enabled</NCheckbox>
        <p
          v-if="submitDisabledReason && !isBusy"
          class="copy-xs warning-text"
          role="alert"
        >
          {{ submitDisabledReason }}
        </p>
        <div
          class="editor-drawer-actions margin-top-lg layout-row align-end-row space-md"
        >
          <NButton secondary attr-type="button" @click="close">Cancel</NButton
          ><DisabledHint
            :disabled="Boolean(submitDisabledReason)"
            :reason="submitDisabledReason"
            ><NButton
              type="primary"
              attr-type="submit"
              :disabled="Boolean(submitDisabledReason)"
              >{{
                form.id ? "Save TLS Mapping" : "Create TLS Mapping"
              }}</NButton
            ></DisabledHint
          >
        </div>
      </form>
    </NDrawerContent>
  </NDrawer>
</template>
