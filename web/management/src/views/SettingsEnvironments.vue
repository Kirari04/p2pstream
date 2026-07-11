<script setup lang="ts">
import { Check as CheckIcon } from "@lucide/vue";
import { MoreHorizontal as MoreIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { NButton, NButtonGroup, NCheckbox, NDataTable, NDrawer, NDrawerContent, NDropdown, NInput, NInputNumber, NModal, NTag, useNotification } from "naive-ui";
import type { DataTableColumns, DropdownOption } from "naive-ui";
import { computed, h, inject, onMounted, reactive, ref } from "vue";
import { localManagementClient } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import { environmentsKey, isBusyKey, reloadEnvironmentsKey } from "@/composables/managementContextKeys";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";
import { editorDrawerWidth, modalCardStyle, naiveTagType } from "@/lib/naiveUi";
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
const activeEnvironmentMenuId = ref("");
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
    minWidth: 300,
    render: (environment) => h("div", { class: "layout-grid space-xs min-width-zero" }, [
      h("bdi", {
        class: "clip-text weight-medium base-text",
        dir: "ltr",
        title: diagnosticInspectionText(environment.name),
      }, diagnosticExcerpt(environment.name, 56).text),
      h("bdi", {
        class: "clip-text mono-text copy-xs muted-text",
        dir: "ltr",
        title: diagnosticInspectionText(environment.managementUrl),
      }, diagnosticExcerpt(environment.managementUrl, 88).text),
      h("div", { class: "layout-row wrap-items space-sm" }, [
        h(NTag, { size: "small", bordered: false, type: "default" }, { default: () => transportLabel(environment.transport) }),
        !environment.enabled
          ? h(NTag, { size: "small", bordered: false, type: "warning" }, { default: () => "Disabled" })
          : null,
        environment.accessTokenConfigured
          ? h(NTag, { size: "small", bordered: false, type: "default" }, { default: () => "Token" })
          : null,
      ]),
      environment.transport === EnvironmentTransport.AGENT
        ? h("p", { class: "clip-text mono-text copy-xs muted-text" }, [
          diagnosticExcerpt(environment.agentName || `agent #${environment.agentId.toString()}`, 60).text,
          " ",
          h("span", { class: environment.agentConnected ? "success-text" : "warning-text" }, environment.agentConnected ? "connected" : "offline"),
        ])
        : null,
    ]),
  },
  {
    title: "Trust & certificate",
    key: "trust",
    minWidth: 320,
    render: (environment) => h("div", { class: "layout-grid min-width-zero space-xs" }, [
      h(NTag, { size: "small", bordered: false, type: naiveTagType(trustSeverity(environment.trustState)), class: "fit-width" }, {
        default: () => trustLabel(environment.trustState),
      }),
      h("p", { class: "clip-text copy-xs base-text", title: certificateSubject(certificateForEnvironment(environment)) }, certificateSubject(certificateForEnvironment(environment))),
      h(
        "code",
        {
          class: "inline-row max-full round-sm framed frame-standard muted-bg pad-x-sm pad-y-xs mono-text copy-micro label-case letter-wide base-text",
          title: certificateFingerprintForEnvironment(environment) || "No certificate discovered",
        },
        [h("span", { class: "clip-text" }, formatFingerprint(certificateFingerprintForEnvironment(environment)))],
      ),
      certificateForEnvironment(environment)
        ? h("p", { class: "copy-xs muted-text" }, `Expires ${formatDate(certificateForEnvironment(environment)?.notAfterUnixMillis)}`)
        : null,
    ]),
  },
  {
    title: "Reachability",
    key: "testResult",
    minWidth: 230,
    render: (environment) => h("div", { class: "layout-grid min-width-zero space-xs", "aria-live": "polite" }, [
      h(
        NTag,
        { size: "small", bordered: false, type: naiveTagType(testResultSeverity(environment)), class: "fit-width" },
        { default: () => testResultLabel(environment) },
      ),
      testResultCheckedAt(environment)
        ? h("p", { class: "mono-text copy-xs base-text" }, formatDate(testResultCheckedAt(environment)))
        : null,
      testResultMessage(environment)
        ? h(
          "bdi",
          {
            class: ["clip-text copy-xs", testResultState(environment) === "error" ? "error-text" : "muted-text"],
            dir: "ltr",
            title: diagnosticInspectionText(testResultMessage(environment)),
          },
          diagnosticExcerpt(testResultMessage(environment), 76).text,
        )
        : null,
    ]),
  },
  {
    title: "Actions",
    key: "actions",
    width: 290,
    align: "right",
    render: (environment) => h("div", { class: "layout-row align-center align-end-row space-sm" }, [
      h(NButton, {
        secondary: true,
        size: "small",
        "aria-label": `${environmentCertificateActionLabel(environment)} for ${safeEnvironmentName(environment)}`,
        disabled: Boolean(environmentCertificateActionDisabledReason(environment)),
        onClick: () => runEnvironmentCertificateAction(environment),
      }, {
        icon: () => environmentCertificateAction(environment) === "trust"
          ? h(CheckIcon, { class: "icon-sm" })
          : h(RefreshIcon, { class: "icon-sm" }),
        default: () => environmentCertificateActionLabel(environment),
      }),
      h(
        DisabledHint,
        { disabled: Boolean(testEnvironmentDisabledReason(environment)), reason: testEnvironmentDisabledReason(environment) },
        {
          default: () => h(NButton, {
            secondary: true,
            size: "small",
            "aria-label": `${isEnvironmentTesting(environment) ? "Testing" : "Test"} ${safeEnvironmentName(environment)}`,
            loading: isEnvironmentTesting(environment),
            disabled: Boolean(testEnvironmentDisabledReason(environment)),
            onClick: () => void testEnvironment(environment),
          }, { default: () => isEnvironmentTesting(environment) ? "Testing" : "Test" }),
        },
      ),
      h(NDropdown, {
        trigger: "click",
        options: environmentMenuOptions(),
        show: activeEnvironmentMenuId.value === environmentKey(environment),
        menuProps: () => ({
          id: environmentMenuId(environment),
          role: "menu",
          "aria-label": `Actions for ${safeEnvironmentName(environment)}`,
        }),
        "onUpdate:show": (show: boolean) => {
          activeEnvironmentMenuId.value = show ? environmentKey(environment) : "";
        },
        onSelect: (key: string | number) => handleEnvironmentMenuSelect(environment, String(key)),
      }, {
        default: () => h(NButton, {
          secondary: true,
          size: "small",
          "aria-label": `More actions for ${safeEnvironmentName(environment)}`,
          "aria-controls": environmentMenuId(environment),
          "aria-expanded": activeEnvironmentMenuId.value === environmentKey(environment),
          "aria-haspopup": "menu",
          disabled: Boolean(busyDisabledReason.value),
        }, {
          icon: () => h(MoreIcon, { class: "icon-sm" }),
          default: () => "More",
        }),
      }),
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

function safeEnvironmentName(environment: Environment): string {
  return diagnosticExcerpt(environment.name, 48).text;
}

function environmentCertificateAction(environment: Environment): "discover" | "trust" {
  if (
    environment.trustState !== EnvironmentTrustState.TRUSTED
    && Boolean(environment.observedCertificate?.sha256Fingerprint)
  ) {
    return "trust";
  }
  return "discover";
}

function environmentCertificateActionLabel(environment: Environment): string {
  if (environmentCertificateAction(environment) === "trust") return "Review trust";
  return certificateForEnvironment(environment) ? "Refresh certificate" : "Discover certificate";
}

function environmentCertificateActionDisabledReason(environment: Environment): string {
  if (busyDisabledReason.value) return busyDisabledReason.value;
  if (environmentCertificateAction(environment) === "trust" && !environment.observedCertificate?.sha256Fingerprint) {
    return "Discover this environment's certificate first.";
  }
  return "";
}

function runEnvironmentCertificateAction(environment: Environment) {
  if (environmentCertificateActionDisabledReason(environment)) return;
  if (environmentCertificateAction(environment) === "trust") {
    void trustCertificate(environment);
    return;
  }
  void discoverCertificate(environment);
}

function environmentMenuOptions(): DropdownOption[] {
  const disabled = Boolean(busyDisabledReason.value);
  return [
    {
      label: "Edit environment",
      key: "edit",
      disabled,
      props: { role: "menuitem", "aria-disabled": disabled ? "true" : "false" },
    },
    {
      label: "Delete environment",
      key: "delete",
      disabled,
      props: { role: "menuitem", "aria-disabled": disabled ? "true" : "false" },
    },
  ];
}

function environmentMenuId(environment: Environment): string {
  return `environment-actions-${environmentKey(environment)}`;
}

function handleEnvironmentMenuSelect(environment: Environment, key: string) {
  if (key === "edit") {
    openEditEnvironment(environment);
    return;
  }
  if (key === "delete") void deleteEnvironment(environment);
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
    `Delete "${diagnosticInspectionText(environment.name)}"? This removes the saved endpoint and access token from this instance.`,
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
      content: `${diagnosticInspectionText(environment.name)} responded successfully.`,
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
      content: diagnosticInspectionText(message),
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
  return diagnosticInspectionText(cert.subject || cert.dnsNames[0] || cert.ipAddresses[0] || "Unknown subject");
}

function certificateSanSummary(cert: EnvironmentCertificate | undefined): string {
  if (!cert) return "";
  const names = [...cert.dnsNames, ...cert.ipAddresses];
  if (!names.length) return "";
  const safeNames = names.map(diagnosticInspectionText);
  if (safeNames.length <= 2) return safeNames.join(", ");
  return `${safeNames.slice(0, 2).join(", ")} +${safeNames.length - 2}`;
}

function formatFingerprint(value: string): string {
  const normalized = value.replaceAll(":", "").trim().toUpperCase();
  if (!normalized) return "-";
  if (normalized.length <= 28) return normalized;
  return `${normalized.slice(0, 12)}...${normalized.slice(-12)}`;
}

function formatFullFingerprint(value: string): string {
  const normalized = value.replaceAll(":", "").trim().toUpperCase();
  if (!normalized) return "-";
  return normalized.match(/.{1,2}/g)?.join(":") ?? normalized;
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
        <NButton type="primary" size="small" :disabled="Boolean(busyDisabledReason)" @click="openCreateEnvironment">
          <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
          Add Environment
        </NButton>
      </DisabledHint>
    </div>

    <div v-if="operationError" class="round-md framed error-border panel-bg pad-lg copy-sm error-text">
      <bdi dir="ltr">{{ diagnosticInspectionText(operationError) }}</bdi>
    </div>

    <section class="surface-card hide-overflow">
      <div class="divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-md">
        <div>
          <h5 class="copy-sm weight-semibold base-text">Registered environments</h5>
          <p class="margin-top-xs copy-xs muted-text">Trust and reachability are checked independently for every endpoint.</p>
        </div>
        <NTag size="small" :bordered="false">{{ environments.length }} total</NTag>
      </div>
      <NDataTable
        v-if="environments.length || isLoading"
        :columns="environmentColumns"
        :data="environments"
        :row-key="environmentRowKey"
        :pagination="false"
        :bordered="false"
        :single-line="false"
        :scroll-x="1140"
        :loading="isLoading"
        size="small"
      />
      <EmptyState
        v-else
        title="No remote environments"
        description="Add an environment to operate another p2pstream instance from this management panel."
        action-label="Add Environment"
        @action="openCreateEnvironment"
      />
    </section>

    <NDrawer
      v-model:show="isEnvironmentModalOpen"
      placement="right"
      :width="editorDrawerWidth('42rem')"
      :aria-label="environmentForm.id ? 'Edit Environment' : 'Add Environment'"
      class="editor-drawer"
    >
      <NDrawerContent :title="environmentForm.id ? 'Edit Environment' : 'Add Environment'" closable>
      <form class="editor-drawer-form layout-grid space-lg" @submit.prevent="submitEnvironment">
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
          <AccessibleSelect v-model:value="environmentForm.agentId" accessible-label="Local agent" size="small" :options="localAgentOptions" required />
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
          <NInputNumber :show-button="false" v-model:value="environmentForm.responseHeaderTimeoutMillis" size="small" :min="1000" :max="300000" required />
        </label>
        <NCheckbox v-model:checked="environmentForm.enabled">
          Enabled
        </NCheckbox>
        <div class="editor-drawer-actions margin-top-lg layout-row align-end-row space-md">
          <NButton secondary attr-type="button" @click="isEnvironmentModalOpen = false">Cancel</NButton>
          <NButton type="primary" attr-type="submit" :disabled="Boolean(busyDisabledReason)">
            {{ environmentForm.id ? 'Save Changes' : 'Create Environment' }}
          </NButton>
        </div>
      </form>
      </NDrawerContent>
    </NDrawer>
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
              <p class="clip-text copy-sm base-text" :title="diagnosticInspectionText(certificateTrustEnvironment?.name ?? '')">
                {{ diagnosticInspectionText(certificateTrustEnvironment?.name ?? "") }}
              </p>
            </div>
            <div class="layout-grid space-2xs">
              <p class="copy-xs weight-medium label-case letter-wide muted-text">SHA-256 Fingerprint</p>
              <code
                class="flow-box max-full clip-text round-md framed frame-standard panel-bg pad-x-md pad-y-sm mono-text copy-xs label-case letter-wide base-text"
                :title="certificateTrustFingerprint"
              >
                {{ formatFullFingerprint(certificateTrustFingerprint) }}
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
              <p class="clip-text copy-sm base-text" :title="diagnosticInspectionText(certificateTrustCertificate.issuer)">
                {{ diagnosticInspectionText(certificateTrustCertificate.issuer) }}
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
