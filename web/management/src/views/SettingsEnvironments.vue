<script setup lang="ts">
import { Check as CheckIcon } from "@lucide/vue";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NButtonGroup, NCheckbox, NDataTable, NInput, NInputNumber, NModal, NSelect, NTag, useNotification } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { computed, h, inject, onMounted, reactive, ref } from "vue";
import { localManagementClient } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import { environmentsKey, isBusyKey, reloadEnvironmentsKey } from "@/composables/managementContextKeys";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { modalCardStyle, naiveTagType } from "@/lib/naiveUi";
import type {
  Agent,
  Environment,
  EnvironmentCertificate,
  TestEnvironmentResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  EnvironmentTransport,
  EnvironmentTrustState,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { messageFromError } from "@/lib/errors";

const environments = inject(environmentsKey, computed(() => []));
const reloadEnvironments = inject(reloadEnvironmentsKey, undefined);
const isBusy = inject(isBusyKey, computed(() => false));
const confirmDialog = useConfirmDialog();
const notification = useNotification();

type EnvironmentTestState = "testing" | "success" | "error";

interface EnvironmentTestResult {
  state: EnvironmentTestState;
  checkedAtUnixMillis: bigint;
  message: string;
}

const localAgents = ref<Agent[]>([]);
const isLoading = ref(false);
const testingEnvironmentId = ref("");
const isEnvironmentModalOpen = ref(false);
const certificateTrustEnvironment = ref<Environment | null>(null);
const operationError = ref("");
const environmentTestResults = reactive<Record<string, EnvironmentTestResult>>({});

const environmentForm = reactive({
  id: "",
  name: "",
  managementUrl: "",
  transport: EnvironmentTransport.DIRECT,
  agentId: "",
  accessToken: "",
  responseHeaderTimeoutMillis: 10000 as number | null,
  enabled: true,
});
const environmentTransportOptions = [
  { label: "Direct", value: EnvironmentTransport.DIRECT },
  { label: "Agent", value: EnvironmentTransport.AGENT },
];

const busyDisabledReason = computed(() => isBusy.value || isLoading.value || testingEnvironmentId.value ? BUSY_REASON : "");
const enabledLocalAgents = computed(() => localAgents.value.filter((agent) => agent.enabled));
const localAgentOptions = computed(() => [
  { label: "Select agent", value: "", disabled: true },
  ...enabledLocalAgents.value.map((agent) => ({
    label: `${agent.name}${agent.connected ? "" : " (offline)"}`,
    value: agent.id.toString(),
  })),
]);
const certificateTrustCertificate = computed(() => certificateTrustEnvironment.value?.observedCertificate);
const certificateTrustFingerprint = computed(() => certificateTrustCertificate.value?.sha256Fingerprint ?? "");
const environmentColumns = computed<DataTableColumns<Environment>>(() => [
  {
    title: "Environment",
    key: "environment",
    minWidth: 260,
    render: (environment) => h("div", [
      h("p", { class: "weight-medium base-text" }, environment.name),
      h("p", { class: "max-auth-width clip-text mono-text copy-xs muted-text" }, environment.managementUrl),
      h("div", { class: "margin-top-sm layout-row space-sm" }, [
        !environment.enabled
          ? h(NTag, { size: "small", bordered: false, type: "warning" }, { default: () => "Disabled" })
          : null,
        environment.accessTokenConfigured
          ? h(NTag, { size: "small", bordered: false, type: "default" }, { default: () => "Token" })
          : null,
      ]),
    ]),
  },
  {
    title: "Transport",
    key: "transport",
    minWidth: 180,
    render: (environment) => h("div", [
      h("p", { class: "base-text" }, transportLabel(environment.transport)),
      environment.transport === EnvironmentTransport.AGENT
        ? h("p", { class: "mono-text copy-xs muted-text" }, [
          environment.agentName || `agent #${environment.agentId.toString()}`,
          " ",
          h("span", { class: environment.agentConnected ? "success-text" : "warning-text" }, environment.agentConnected ? "connected" : "offline"),
        ])
        : null,
    ]),
  },
  {
    title: "Trust",
    key: "trust",
    width: 120,
    render: (environment) => h(
      NTag,
      { size: "small", bordered: false, type: naiveTagType(trustSeverity(environment.trustState)) },
      { default: () => trustLabel(environment.trustState) },
    ),
  },
  {
    title: "Certificate",
    key: "certificate",
    minWidth: 260,
    render: (environment) => h("div", { class: "layout-grid max-token-width space-xs" }, [
      h("p", { class: "clip-text copy-xs base-text", title: certificateSubject(certificateForEnvironment(environment)) }, certificateSubject(certificateForEnvironment(environment))),
      h(
        "code",
        {
          class: "inline-row max-full round-sm framed frame-standard muted-bg pad-x-sm pad-y-xs mono-text copy-micro label-case letter-wide base-text",
          title: certificateFingerprintForEnvironment(environment) || "No certificate discovered",
        },
        [h("span", { class: "clip-text" }, formatFingerprint(certificateFingerprintForEnvironment(environment)))],
      ),
      h("p", { class: "copy-xs muted-text" }, `Expires ${formatDate(certificateForEnvironment(environment)?.notAfterUnixMillis)}`),
    ]),
  },
  {
    title: "Test Result",
    key: "testResult",
    minWidth: 220,
    render: (environment) => h("div", { class: "layout-grid max-content-sm space-xs", "aria-live": "polite" }, [
      h(
        NTag,
        { size: "small", bordered: false, type: naiveTagType(testResultSeverity(environment)) },
        { default: () => testResultLabel(environment) },
      ),
      h("p", { class: "mono-text copy-xs base-text" }, formatDate(testResultCheckedAt(environment))),
      testResultMessage(environment)
        ? h(
          "p",
          {
            class: ["clip-text copy-xs", testResultState(environment) === "error" ? "error-text" : "muted-text"],
            title: testResultMessage(environment),
          },
          testResultMessage(environment),
        )
        : null,
    ]),
  },
  {
    title: "Actions",
    key: "actions",
    width: 300,
    align: "right",
    render: (environment) => h("div", { class: "layout-row align-end-row space-sm" }, [
      h(NButton, {
        secondary: true,
        size: "small",
        "aria-label": "Discover certificate",
        title: "Discover certificate",
        disabled: Boolean(busyDisabledReason.value),
        onClick: () => void discoverCertificate(environment),
      }, { icon: () => h(RefreshIcon, { class: "icon-sm" }) }),
      h(NButton, {
        secondary: true,
        size: "small",
        "aria-label": "Trust certificate",
        title: "Trust certificate",
        disabled: Boolean(busyDisabledReason.value) || !environment.observedCertificate?.sha256Fingerprint,
        onClick: () => trustCertificate(environment),
      }, { icon: () => h(CheckIcon, { class: "icon-sm" }) }),
      h(
        DisabledHint,
        { disabled: Boolean(testEnvironmentDisabledReason(environment)), reason: testEnvironmentDisabledReason(environment) },
        {
          default: () => h(NButton, {
            secondary: true,
            size: "small",
            loading: isEnvironmentTesting(environment),
            disabled: Boolean(testEnvironmentDisabledReason(environment)),
            onClick: () => void testEnvironment(environment),
          }, { default: () => isEnvironmentTesting(environment) ? "Testing" : "Test" }),
        },
      ),
      h(NButton, {
        secondary: true,
        size: "small",
        "aria-label": "Edit environment",
        title: "Edit environment",
        disabled: Boolean(busyDisabledReason.value),
        onClick: () => openEditEnvironment(environment),
      }, { icon: () => h(PencilIcon, { class: "icon-sm" }) }),
      h(NButton, {
        type: "error",
        size: "small",
        "aria-label": "Delete environment",
        title: "Delete environment",
        disabled: Boolean(busyDisabledReason.value),
        onClick: () => void deleteEnvironment(environment),
      }, { icon: () => h(TrashIcon, { class: "icon-sm" }) }),
    ]),
  },
]);

onMounted(() => {
  void refreshLocalData();
});

async function refreshLocalData() {
  isLoading.value = true;
  operationError.value = "";
  try {
    await Promise.all([reloadEnvironments?.(), loadLocalAgents()]);
  } catch (err) {
    operationError.value = messageFromError(err);
  } finally {
    isLoading.value = false;
  }
}

async function loadLocalAgents() {
  const resp = await localManagementClient.getPublicProxyConfig({});
  localAgents.value = resp.agents;
}

function openCreateEnvironment() {
  environmentForm.id = "";
  environmentForm.name = "";
  environmentForm.managementUrl = "";
  environmentForm.transport = EnvironmentTransport.DIRECT;
  environmentForm.agentId = "";
  environmentForm.accessToken = "";
  environmentForm.responseHeaderTimeoutMillis = 10000;
  environmentForm.enabled = true;
  operationError.value = "";
  isEnvironmentModalOpen.value = true;
}

function openEditEnvironment(environment: Environment) {
  environmentForm.id = environment.id.toString();
  environmentForm.name = environment.name;
  environmentForm.managementUrl = environment.managementUrl;
  environmentForm.transport = environment.transport || EnvironmentTransport.DIRECT;
  environmentForm.agentId = environment.agentId ? environment.agentId.toString() : "";
  environmentForm.accessToken = "";
  environmentForm.responseHeaderTimeoutMillis = Number(environment.responseHeaderTimeoutMillis || 10000n);
  environmentForm.enabled = environment.enabled;
  operationError.value = "";
  isEnvironmentModalOpen.value = true;
}

async function submitEnvironment() {
  await runLocalAction(async () => {
    const timeout = environmentForm.responseHeaderTimeoutMillis;
    if (timeout === null || !Number.isInteger(timeout) || timeout < 1000 || timeout > 300000) {
      throw new Error("Response header timeout must be between 1000 and 300000 ms.");
    }
    if (environmentForm.transport === EnvironmentTransport.AGENT && !environmentForm.agentId) {
      throw new Error("Select a local agent.");
    }
    const payload = {
      name: environmentForm.name,
      managementUrl: environmentForm.managementUrl,
      transport: environmentForm.transport,
      agentId: BigInt(environmentForm.transport === EnvironmentTransport.AGENT ? environmentForm.agentId : "0"),
      accessToken: environmentForm.accessToken,
      responseHeaderTimeoutMillis: BigInt(timeout),
      enabled: environmentForm.enabled,
    };
    if (environmentForm.id) {
      await localManagementClient.updateEnvironment({ id: BigInt(environmentForm.id), ...payload });
    } else {
      await localManagementClient.createEnvironment(payload);
    }
    isEnvironmentModalOpen.value = false;
    await reloadEnvironments?.();
  });
}

async function deleteEnvironment(environment: Environment) {
  const confirmed = await confirmDialog.confirm(
    "Delete Environment",
    `Delete "${environment.name}"?`,
    "Delete",
  );
  if (!confirmed) return;
  await runLocalAction(async () => {
    await localManagementClient.deleteEnvironment({ id: environment.id });
    await reloadEnvironments?.();
  });
}

async function discoverCertificate(environment: Environment) {
  if (isLoading.value) return;
  isLoading.value = true;
  operationError.value = "";
  try {
    await localManagementClient.discoverEnvironmentCertificate({ id: environment.id });
  } catch (err) {
    operationError.value = messageFromError(err);
  } finally {
    try {
      await reloadEnvironments?.();
    } catch (err) {
      if (!operationError.value) {
        operationError.value = messageFromError(err);
      }
    }
    isLoading.value = false;
  }
}

async function trustCertificate(environment: Environment) {
  const fingerprint = environment.observedCertificate?.sha256Fingerprint ?? "";
  if (!fingerprint) return;
  certificateTrustEnvironment.value = environment;
}

function closeTrustCertificateModal() {
  certificateTrustEnvironment.value = null;
}

async function confirmTrustCertificate() {
  const environment = certificateTrustEnvironment.value;
  const fingerprint = certificateTrustFingerprint.value;
  if (!environment || !fingerprint) return;
  await runLocalAction(async () => {
    await localManagementClient.trustEnvironmentCertificate({ id: environment.id, sha256Fingerprint: fingerprint });
    closeTrustCertificateModal();
    await reloadEnvironments?.();
  });
}

async function testEnvironment(environment: Environment) {
  const key = environmentKey(environment);
  if (testingEnvironmentId.value || testEnvironmentDisabledReason(environment)) return;

  testingEnvironmentId.value = key;
  operationError.value = "";
  environmentTestResults[key] = {
    state: "testing",
    checkedAtUnixMillis: BigInt(Date.now()),
    message: "Testing connection...",
  };

  try {
    const resp = await localManagementClient.testEnvironment({ id: environment.id });
    const checkedAt = resp.environment?.lastCheckedAtUnixMillis || BigInt(Date.now());
    environmentTestResults[key] = {
      state: "success",
      checkedAtUnixMillis: checkedAt,
      message: environmentStatusMessage(resp.status),
    };
    notification.success({
      title: "Environment reachable",
      content: `${environment.name} responded successfully.`,
      duration: 3000,
    });
  } catch (err) {
    const message = messageFromError(err);
    environmentTestResults[key] = {
      state: "error",
      checkedAtUnixMillis: BigInt(Date.now()),
      message,
    };
    notification.error({
      title: "Environment test failed",
      content: message,
      duration: 5000,
    });
  } finally {
    testingEnvironmentId.value = "";
    try {
      await reloadEnvironments?.();
    } catch (err) {
      operationError.value = messageFromError(err);
    }
  }
}

async function runLocalAction(action: () => Promise<void>) {
  if (isLoading.value) return;
  isLoading.value = true;
  operationError.value = "";
  try {
    await action();
  } catch (err) {
    operationError.value = messageFromError(err);
  } finally {
    isLoading.value = false;
  }
}

function trustLabel(state: EnvironmentTrustState): string {
  switch (state) {
    case EnvironmentTrustState.TRUSTED:
      return "Trusted";
    case EnvironmentTrustState.CHANGED:
      return "Changed";
    case EnvironmentTrustState.EXPIRED:
      return "Expired";
    default:
      return "Untrusted";
  }
}

function trustSeverity(state: EnvironmentTrustState): "success" | "warn" | "danger" {
  if (state === EnvironmentTrustState.TRUSTED) return "success";
  if (state === EnvironmentTrustState.CHANGED || state === EnvironmentTrustState.EXPIRED) return "danger";
  return "warn";
}

function transportLabel(transport: EnvironmentTransport): string {
  return transport === EnvironmentTransport.AGENT ? "Agent" : "Direct";
}

function environmentKey(environment: Environment): string {
  return environment.id.toString();
}

function isEnvironmentTesting(environment: Environment): boolean {
  return testingEnvironmentId.value === environmentKey(environment);
}

function testEnvironmentDisabledReason(environment: Environment): string {
  if (isBusy.value || isLoading.value) return BUSY_REASON;
  if (testingEnvironmentId.value && !isEnvironmentTesting(environment)) return BUSY_REASON;
  if (environment.trustState !== EnvironmentTrustState.TRUSTED) return "Trust this environment before testing.";
  return "";
}

function testResultState(environment: Environment): "idle" | "testing" | "success" | "error" {
  const result = environmentTestResults[environmentKey(environment)];
  if (result?.state === "testing") return "testing";
  if (result?.state === "success") return "success";
  if (result?.state === "error") return "error";
  if (!environment.lastCheckedAtUnixMillis || environment.lastCheckedAtUnixMillis === 0n) return "idle";
  return environment.lastError ? "error" : "success";
}

function testResultLabel(environment: Environment): string {
  switch (testResultState(environment)) {
    case "testing":
      return "Testing";
    case "success":
      return "Reachable";
    case "error":
      return "Failed";
    default:
      return "Not tested";
  }
}

function testResultSeverity(environment: Environment): "success" | "warn" | "danger" | "secondary" {
  switch (testResultState(environment)) {
    case "testing":
      return "warn";
    case "success":
      return "success";
    case "error":
      return "danger";
    default:
      return "secondary";
  }
}

function testResultCheckedAt(environment: Environment): bigint {
  return environmentTestResults[environmentKey(environment)]?.checkedAtUnixMillis
    ?? environment.lastCheckedAtUnixMillis;
}

function testResultMessage(environment: Environment): string {
  const result = environmentTestResults[environmentKey(environment)];
  if (result?.message) return result.message;
  if (environment.lastError) return environment.lastError;
  if (environment.lastCheckedAtUnixMillis && environment.lastCheckedAtUnixMillis !== 0n) return "Remote responded.";
  return "";
}

function environmentStatusMessage(status: TestEnvironmentResponse["status"]): string {
  if (!status) return "Remote responded.";
  if (status.proxyLastError) return `Remote responded. Proxy error: ${status.proxyLastError}`;
  if (status.proxy?.lastError) return `Remote responded. Proxy error: ${status.proxy.lastError}`;
  return status.proxyRunning ? "Remote responded. Proxy running." : "Remote responded. Proxy stopped.";
}

function certificateForEnvironment(environment: Environment): EnvironmentCertificate | undefined {
  return environment.observedCertificate ?? environment.trustedCertificate;
}

function certificateFingerprintForEnvironment(environment: Environment): string {
  return certificateForEnvironment(environment)?.sha256Fingerprint ?? "";
}

function certificateSubject(cert: EnvironmentCertificate | undefined): string {
  if (!cert) return "No certificate discovered";
  return cert.subject || cert.dnsNames[0] || cert.ipAddresses[0] || "Unknown subject";
}

function certificateSanSummary(cert: EnvironmentCertificate | undefined): string {
  if (!cert) return "";
  const names = [...cert.dnsNames, ...cert.ipAddresses];
  if (!names.length) return "";
  if (names.length <= 2) return names.join(", ");
  return `${names.slice(0, 2).join(", ")} +${names.length - 2}`;
}

function formatFingerprint(value: string): string {
  const normalized = value.replaceAll(":", "").trim().toUpperCase();
  if (!normalized) return "-";
  if (normalized.length <= 28) return normalized;
  return `${normalized.slice(0, 12)}...${normalized.slice(-12)}`;
}

function formatDate(value: bigint | undefined): string {
  if (!value || value === 0n) return "Never";
  return new Date(Number(value)).toLocaleString();
}


function environmentRowKey(environment: Environment): string {
  return environment.id.toString();
}

function handleTrustModalUpdate(show: boolean) {
  if (!show) closeTrustCertificateModal();
}
</script>

<template>
  <div class="stack-xl">
    <div class="layout-row layout-column space-lg mq-md-row mq-md-align-end mq-md-spread">
      <div>
        <h4 class="margin-bottom-sm copy-lg weight-semibold base-text">Environments</h4>
        <p class="copy-sm muted-text">Remote management endpoints, routing, and certificate trust.</p>
      </div>
      <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
        <NButton secondary size="small" :disabled="Boolean(busyDisabledReason)" @click="openCreateEnvironment">
          <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
          Add Environment
        </NButton>
      </DisabledHint>
    </div>

    <div v-if="operationError" class="round-md framed error-border panel-bg pad-lg copy-sm error-text">
      {{ operationError }}
    </div>

    <section class="surface-card hide-overflow">
      <div class="divider-bottom frame-standard pad-x-xl pad-y-lg">
        <h5 class="copy-sm weight-semibold label-case letter-widest muted-text">Registered Environments</h5>
      </div>
      <NDataTable
        :columns="environmentColumns"
        :data="environments"
        :row-key="environmentRowKey"
        :pagination="false"
        :bordered="false"
        :single-line="false"
        :scroll-x="1120"
        size="small"
      />
    </section>

    <NModal
      v-model:show="isEnvironmentModalOpen"
      preset="card"
      :title="environmentForm.id ? 'Edit Environment' : 'Add Environment'"
      :style="modalCardStyle('42rem')"
      :bordered="false"
    >
      <form class="layout-grid max-modal-height space-lg scroll-y pad-right-xs" @submit.prevent="submitEnvironment">
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Name
          <NInput v-model:value="environmentForm.name" size="small" required />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Management URL
          <NInput v-model:value="environmentForm.managementUrl" size="small" placeholder="https://proxy.example.com:8081" required />
        </label>
        <div class="layout-grid space-sm copy-xs weight-medium label-case letter-wide muted-text">
          Transport
          <NButtonGroup class="fit-width" size="small">
            <NButton
              v-for="option in environmentTransportOptions"
              :key="option.value"
              attr-type="button"
              :type="environmentForm.transport === option.value ? 'primary' : 'default'"
              @click="environmentForm.transport = option.value"
            >
              {{ option.label }}
            </NButton>
          </NButtonGroup>
        </div>
        <label v-if="environmentForm.transport === EnvironmentTransport.AGENT" class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Local Agent
          <NSelect v-model:value="environmentForm.agentId" size="small" :options="localAgentOptions" required />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Access Token
          <NInput
            v-model:value="environmentForm.accessToken"
            size="small"
            :placeholder="environmentForm.id ? 'Leave blank to keep existing token' : 'p2pat_...'"
            :required="!environmentForm.id"
          />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Response Header Timeout
          <NInputNumber v-model:value="environmentForm.responseHeaderTimeoutMillis" size="small" :min="1000" :max="300000" required />
        </label>
        <NCheckbox v-model:checked="environmentForm.enabled">
          Enabled
        </NCheckbox>
        <div class="margin-top-lg layout-row align-end-row space-md">
          <NButton secondary attr-type="button" @click="isEnvironmentModalOpen = false">Cancel</NButton>
          <NButton type="primary" attr-type="submit" :disabled="Boolean(busyDisabledReason)">
            {{ environmentForm.id ? 'Save Changes' : 'Create Environment' }}
          </NButton>
        </div>
      </form>
    </NModal>
    <NModal
      :show="Boolean(certificateTrustEnvironment)"
      preset="card"
      title="Trust Certificate"
      :style="modalCardStyle('34rem')"
      :bordered="false"
      @update:show="handleTrustModalUpdate"
    >
      <div class="layout-grid space-xl">
        <div class="round-md framed frame-standard muted-bg pad-lg">
          <div class="layout-grid space-lg">
            <div class="layout-grid space-2xs">
              <p class="copy-xs weight-medium label-case letter-wide muted-text">Environment</p>
              <p class="clip-text copy-sm base-text" :title="certificateTrustEnvironment?.name">
                {{ certificateTrustEnvironment?.name }}
              </p>
            </div>
            <div class="layout-grid space-2xs">
              <p class="copy-xs weight-medium label-case letter-wide muted-text">SHA-256 Fingerprint</p>
              <code
                class="flow-box max-full clip-text round-md framed frame-standard panel-bg pad-x-md pad-y-sm mono-text copy-xs label-case letter-wide base-text"
                :title="certificateTrustFingerprint"
              >
                {{ formatFingerprint(certificateTrustFingerprint) }}
              </code>
            </div>
            <div class="layout-grid space-2xs">
              <p class="copy-xs weight-medium label-case letter-wide muted-text">Subject</p>
              <p class="clip-text copy-sm base-text" :title="certificateSubject(certificateTrustCertificate)">
                {{ certificateSubject(certificateTrustCertificate) }}
              </p>
            </div>
            <div v-if="certificateTrustCertificate?.issuer" class="layout-grid space-2xs">
              <p class="copy-xs weight-medium label-case letter-wide muted-text">Issuer</p>
              <p class="clip-text copy-sm base-text" :title="certificateTrustCertificate.issuer">
                {{ certificateTrustCertificate.issuer }}
              </p>
            </div>
            <div v-if="certificateSanSummary(certificateTrustCertificate)" class="layout-grid space-2xs">
              <p class="copy-xs weight-medium label-case letter-wide muted-text">Names</p>
              <p class="clip-text copy-sm base-text" :title="certificateSanSummary(certificateTrustCertificate)">
                {{ certificateSanSummary(certificateTrustCertificate) }}
              </p>
            </div>
            <div class="layout-grid space-2xs">
              <p class="copy-xs weight-medium label-case letter-wide muted-text">Valid Until</p>
              <p class="mono-text copy-xs base-text">
                {{ formatDate(certificateTrustCertificate?.notAfterUnixMillis) }}
              </p>
            </div>
          </div>
        </div>
        <div class="layout-row align-end-row space-md">
          <NButton secondary attr-type="button" @click="closeTrustCertificateModal">Cancel</NButton>
          <NButton
            type="primary"
            attr-type="button"
            :disabled="Boolean(busyDisabledReason) || !certificateTrustFingerprint"
            @click="confirmTrustCertificate"
          >
            Trust Certificate
          </NButton>
        </div>
      </div>
    </NModal>
  </div>
</template>
