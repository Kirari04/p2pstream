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
      h("p", { class: "font-medium text-[var(--app-text)]" }, environment.name),
      h("p", { class: "max-w-md truncate font-mono text-xs text-[var(--app-text-muted)]" }, environment.managementUrl),
      h("div", { class: "mt-2 flex gap-2" }, [
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
      h("p", { class: "text-[var(--app-text)]" }, transportLabel(environment.transport)),
      environment.transport === EnvironmentTransport.AGENT
        ? h("p", { class: "font-mono text-xs text-[var(--app-text-muted)]" }, [
          environment.agentName || `agent #${environment.agentId.toString()}`,
          " ",
          h("span", { class: environment.agentConnected ? "text-green-400" : "text-amber-400" }, environment.agentConnected ? "connected" : "offline"),
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
    render: (environment) => h("div", { class: "grid max-w-[18rem] gap-1.5" }, [
      h("p", { class: "truncate text-xs text-[var(--app-text)]", title: certificateSubject(certificateForEnvironment(environment)) }, certificateSubject(certificateForEnvironment(environment))),
      h(
        "code",
        {
          class: "inline-flex max-w-full rounded border border-[var(--app-border)] bg-[var(--app-panel-muted)] px-2 py-1 font-mono text-[11px] uppercase tracking-wider text-[var(--app-text)]",
          title: certificateFingerprintForEnvironment(environment) || "No certificate discovered",
        },
        [h("span", { class: "truncate" }, formatFingerprint(certificateFingerprintForEnvironment(environment)))],
      ),
      h("p", { class: "text-xs text-[var(--app-text-muted)]" }, `Expires ${formatDate(certificateForEnvironment(environment)?.notAfterUnixMillis)}`),
    ]),
  },
  {
    title: "Test Result",
    key: "testResult",
    minWidth: 220,
    render: (environment) => h("div", { class: "grid max-w-xs gap-1.5", "aria-live": "polite" }, [
      h(
        NTag,
        { size: "small", bordered: false, type: naiveTagType(testResultSeverity(environment)) },
        { default: () => testResultLabel(environment) },
      ),
      h("p", { class: "font-mono text-xs text-[var(--app-text)]" }, formatDate(testResultCheckedAt(environment))),
      testResultMessage(environment)
        ? h(
          "p",
          {
            class: ["truncate text-xs", testResultState(environment) === "error" ? "text-red-400" : "text-[var(--app-text-muted)]"],
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
    render: (environment) => h("div", { class: "flex justify-end gap-2" }, [
      h(NButton, {
        secondary: true,
        size: "small",
        "aria-label": "Discover certificate",
        title: "Discover certificate",
        disabled: Boolean(busyDisabledReason.value),
        onClick: () => void discoverCertificate(environment),
      }, { icon: () => h(RefreshIcon, { class: "h-3.5 w-3.5" }) }),
      h(NButton, {
        secondary: true,
        size: "small",
        "aria-label": "Trust certificate",
        title: "Trust certificate",
        disabled: Boolean(busyDisabledReason.value) || !environment.observedCertificate?.sha256Fingerprint,
        onClick: () => trustCertificate(environment),
      }, { icon: () => h(CheckIcon, { class: "h-3.5 w-3.5" }) }),
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
      }, { icon: () => h(PencilIcon, { class: "h-3.5 w-3.5" }) }),
      h(NButton, {
        type: "error",
        size: "small",
        "aria-label": "Delete environment",
        title: "Delete environment",
        disabled: Boolean(busyDisabledReason.value),
        onClick: () => void deleteEnvironment(environment),
      }, { icon: () => h(TrashIcon, { class: "h-3.5 w-3.5" }) }),
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
  <div class="space-y-8">
    <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
      <div>
        <h4 class="mb-2 text-lg font-semibold text-[var(--app-text)]">Environments</h4>
        <p class="text-sm text-[var(--app-text-muted)]">Remote management endpoints, routing, and certificate trust.</p>
      </div>
      <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
        <NButton secondary size="small" :disabled="Boolean(busyDisabledReason)" @click="openCreateEnvironment">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          Add Environment
        </NButton>
      </DisabledHint>
    </div>

    <div v-if="operationError" class="rounded-md border border-red-900/60 bg-[var(--app-panel)] p-4 text-sm text-red-400">
      {{ operationError }}
    </div>

    <section class="app-card overflow-hidden">
      <div class="border-b border-[var(--app-border)] px-5 py-4">
        <h5 class="text-sm font-semibold uppercase tracking-widest text-[var(--app-text-muted)]">Registered Environments</h5>
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
      <form class="grid max-h-[calc(100vh-9rem)] gap-4 overflow-y-auto pr-1" @submit.prevent="submitEnvironment">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Name
          <NInput v-model:value="environmentForm.name" size="small" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Management URL
          <NInput v-model:value="environmentForm.managementUrl" size="small" placeholder="https://proxy.example.com:8081" required />
        </label>
        <div class="grid gap-2 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Transport
          <NButtonGroup class="w-fit" size="small">
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
        <label v-if="environmentForm.transport === EnvironmentTransport.AGENT" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Local Agent
          <NSelect v-model:value="environmentForm.agentId" size="small" :options="localAgentOptions" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Access Token
          <NInput
            v-model:value="environmentForm.accessToken"
            size="small"
            :placeholder="environmentForm.id ? 'Leave blank to keep existing token' : 'p2pat_...'"
            :required="!environmentForm.id"
          />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Response Header Timeout
          <NInputNumber v-model:value="environmentForm.responseHeaderTimeoutMillis" size="small" :min="1000" :max="300000" required />
        </label>
        <NCheckbox v-model:checked="environmentForm.enabled">
          Enabled
        </NCheckbox>
        <div class="mt-4 flex justify-end gap-3">
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
      <div class="grid gap-5">
        <div class="rounded-md border border-[var(--app-border)] bg-[var(--app-panel-muted)] p-4">
          <div class="grid gap-4">
            <div class="grid gap-1">
              <p class="text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">Environment</p>
              <p class="truncate text-sm text-[var(--app-text)]" :title="certificateTrustEnvironment?.name">
                {{ certificateTrustEnvironment?.name }}
              </p>
            </div>
            <div class="grid gap-1">
              <p class="text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">SHA-256 Fingerprint</p>
              <code
                class="block max-w-full truncate rounded-md border border-[var(--app-border)] bg-[var(--app-panel)] px-3 py-2 font-mono text-xs uppercase tracking-wider text-[var(--app-text)]"
                :title="certificateTrustFingerprint"
              >
                {{ formatFingerprint(certificateTrustFingerprint) }}
              </code>
            </div>
            <div class="grid gap-1">
              <p class="text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">Subject</p>
              <p class="truncate text-sm text-[var(--app-text)]" :title="certificateSubject(certificateTrustCertificate)">
                {{ certificateSubject(certificateTrustCertificate) }}
              </p>
            </div>
            <div v-if="certificateTrustCertificate?.issuer" class="grid gap-1">
              <p class="text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">Issuer</p>
              <p class="truncate text-sm text-[var(--app-text)]" :title="certificateTrustCertificate.issuer">
                {{ certificateTrustCertificate.issuer }}
              </p>
            </div>
            <div v-if="certificateSanSummary(certificateTrustCertificate)" class="grid gap-1">
              <p class="text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">Names</p>
              <p class="truncate text-sm text-[var(--app-text)]" :title="certificateSanSummary(certificateTrustCertificate)">
                {{ certificateSanSummary(certificateTrustCertificate) }}
              </p>
            </div>
            <div class="grid gap-1">
              <p class="text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">Valid Until</p>
              <p class="font-mono text-xs text-[var(--app-text)]">
                {{ formatDate(certificateTrustCertificate?.notAfterUnixMillis) }}
              </p>
            </div>
          </div>
        </div>
        <div class="flex justify-end gap-3">
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
