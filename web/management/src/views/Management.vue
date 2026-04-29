<script setup lang="ts">
import { computed, inject, reactive, ref, watch } from "vue";
import type { ComputedRef } from "vue";
import BanIcon from "@primevue/icons/ban";
import CheckIcon from "@primevue/icons/check";
import PencilIcon from "@primevue/icons/pencil";
import PlusIcon from "@primevue/icons/plus";
import RefreshIcon from "@primevue/icons/refresh";
import TimesIcon from "@primevue/icons/times";
import TrashIcon from "@primevue/icons/trash";
import { managementClient } from "@/api/managementClient";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import Tag from "@/volt/Tag.vue";
import Modal from "@/volt/Modal.vue";
import {
  ProxyState,
  PublicBackendType,
  PublicListenerProtocol,
  type GetDashboardResponse,
  type GetPublicProxyConfigResponse,
  type PublicBackend,
  type PublicListener,
  type PublicListenerStatus,
  type PublicTlsCertificate,
} from "@/gen/proto/p2pstream/v1/management_pb";

type Runner = (action: () => Promise<void>) => Promise<boolean>;
type StaticHeaderForm = { name: string; value: string };
type TlsFileField = "cert" | "key";

const dashboard = inject<ComputedRef<GetDashboardResponse | null>>("dashboard");
const publicProxyConfig = inject<ComputedRef<GetPublicProxyConfigResponse | null>>("publicProxyConfig");
const setProxyRunning = inject<(shouldRun: boolean) => Promise<void>>("setProxyRunning");
const runManagementAction = inject<Runner>("runManagementAction");
const logout = inject<() => void>("logout");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const status = computed(() => dashboard?.value?.status ?? null);
const config = computed(() => publicProxyConfig?.value ?? null);
const proxyState = computed(() => status.value?.proxy?.state ?? ProxyState.UNSPECIFIED);
const proxyIsRunning = computed(() => proxyState.value === ProxyState.RUNNING || status.value?.proxyRunning === true);
const proxyError = computed(() => status.value?.proxy?.lastError || status.value?.proxyLastError || "");
const listeners = computed(() => config.value?.listeners ?? []);
const backends = computed(() => config.value?.backends ?? []);
const routes = computed(() => config.value?.routes ?? []);
const tlsCertificates = computed(() => config.value?.tlsCertificates ?? []);
const listenerStatuses = computed(() => config.value?.proxy?.listeners ?? status.value?.proxy?.listeners ?? []);
const httpsListeners = computed(() => listeners.value.filter((listener) => listener.protocol === PublicListenerProtocol.HTTPS));

const isListenerModalOpen = ref(false);
const isBackendModalOpen = ref(false);
const isRouteModalOpen = ref(false);
const isTlsModalOpen = ref(false);

const backendForm = reactive({
  id: "" as string,
  name: "",
  backendType: PublicBackendType.PROXY_FORWARD,
  targetOrigin: "",
  tlsVerify: true,
  staticStatusCode: 200,
  staticResponseHeaders: [] as StaticHeaderForm[],
  staticResponseBody: "",
  enabled: true,
});

const listenerForm = reactive({
  id: "" as string,
  name: "",
  bindAddress: "",
  port: 80,
  protocol: PublicListenerProtocol.HTTP,
  enabled: true,
  defaultBackendId: "",
});

const routeForm = reactive({
  id: "" as string,
  listenerId: "",
  priority: 100,
  hostPattern: "",
  pathPrefix: "",
  backendId: "",
  enabled: true,
});

const tlsForm = reactive({
  id: "" as string,
  listenerId: "",
  hostnamePattern: "",
  certPem: null as Uint8Array | null,
  keyPem: null as Uint8Array | null,
  certFileName: "",
  keyFileName: "",
  enabled: true,
});
const tlsUploadError = ref("");

const proxySeverity = computed(() => severityForState(proxyState.value));
const tlsHasPartialUpload = computed(() => Boolean(tlsForm.certPem) !== Boolean(tlsForm.keyPem));
const tlsSubmitDisabled = computed(() => {
  if (isBusy?.value || !httpsListeners.value.length) return true;
  if (!tlsForm.id && (!tlsForm.certPem || !tlsForm.keyPem)) return true;
  return tlsHasPartialUpload.value;
});

function proxyStateLabel(state: ProxyState): string {
  switch (state) {
    case ProxyState.STOPPED: return "Stopped";
    case ProxyState.STARTING: return "Starting";
    case ProxyState.RUNNING: return "Running";
    case ProxyState.STOPPING: return "Stopping";
    case ProxyState.ERROR: return "Error";
    default: return status.value?.proxyRunning ? "Running" : "Unknown";
  }
}

function severityForState(state: ProxyState): string {
  if (state === ProxyState.RUNNING) return "success";
  if (state === ProxyState.STARTING || state === ProxyState.STOPPING) return "warn";
  return "danger";
}

function listenerStatus(listener: PublicListener): PublicListenerStatus | undefined {
  return listenerStatuses.value.find((item) => item.listenerId === listener.id);
}

function listenerState(listener: PublicListener): ProxyState {
  if (!listener.enabled) return ProxyState.STOPPED;
  return listenerStatus(listener)?.state ?? ProxyState.STOPPED;
}

function listenerStateLabel(listener: PublicListener): string {
  if (!listener.enabled || listenerStatus(listener)?.disabled) return "Disabled";
  return proxyStateLabel(listenerState(listener));
}

function backendName(id: bigint): string {
  return backends.value.find((backend) => backend.id === id)?.name ?? `#${id.toString()}`;
}

function listenerName(id: bigint): string {
  return listeners.value.find((listener) => listener.id === id)?.name ?? `#${id.toString()}`;
}

function bindLabel(listener: PublicListener): string {
  return `${listener.bindAddress || "*"}:${listener.port.toString()}`;
}

function protocolLabel(protocol: PublicListenerProtocol): string {
  return protocol === PublicListenerProtocol.HTTPS ? "HTTPS" : "HTTP";
}

function backendTypeLabel(type: PublicBackendType): string {
  return type === PublicBackendType.STATIC ? "Static" : "Proxy forward";
}

function backendSummary(backend: PublicBackend): string {
  if (backend.backendType === PublicBackendType.STATIC) {
    const body = backend.staticResponseBody.trim();
    const suffix = body ? ` - ${body.slice(0, 72)}` : "";
    return `${backend.staticStatusCode.toString()}${suffix}`;
  }
  return backend.targetOrigin;
}

function isDefaultSelfSignedCertificate(cert: PublicTlsCertificate): boolean {
  return cert.hostnamePattern === "p2pstream.local";
}

function tlsCertificateSummary(cert: PublicTlsCertificate): string {
  return isDefaultSelfSignedCertificate(cert) ? "Default self-signed certificate" : "Uploaded certificate";
}

function openAddBackendModal() {
  resetBackendForm();
  isBackendModalOpen.value = true;
}

function resetBackendForm() {
  backendForm.id = "";
  backendForm.name = "";
  backendForm.backendType = PublicBackendType.PROXY_FORWARD;
  backendForm.targetOrigin = "";
  backendForm.tlsVerify = true;
  backendForm.staticStatusCode = 200;
  backendForm.staticResponseHeaders = [];
  backendForm.staticResponseBody = "";
  backendForm.enabled = true;
}

function editBackend(backend: PublicBackend) {
  backendForm.id = backend.id.toString();
  backendForm.name = backend.name;
  backendForm.backendType = backend.backendType || PublicBackendType.PROXY_FORWARD;
  backendForm.targetOrigin = backend.targetOrigin;
  backendForm.tlsVerify = !backend.tlsSkipVerify;
  backendForm.staticStatusCode = Number(backend.staticStatusCode || 200n);
  backendForm.staticResponseHeaders = backend.staticResponseHeaders.map((header) => ({
    name: header.name,
    value: header.value,
  }));
  backendForm.staticResponseBody = backend.staticResponseBody;
  backendForm.enabled = backend.enabled;
  isBackendModalOpen.value = true;
}

function addStaticHeader() {
  backendForm.staticResponseHeaders.push({ name: "", value: "" });
}

function removeStaticHeader(index: number) {
  backendForm.staticResponseHeaders.splice(index, 1);
}

function openAddListenerModal() {
  resetListenerForm();
  isListenerModalOpen.value = true;
}

function resetListenerForm() {
  listenerForm.id = "";
  listenerForm.name = "";
  listenerForm.bindAddress = "";
  listenerForm.port = 80;
  listenerForm.protocol = PublicListenerProtocol.HTTP;
  listenerForm.enabled = true;
  listenerForm.defaultBackendId = backends.value[0]?.id.toString() ?? "";
}

function editListener(listener: PublicListener) {
  listenerForm.id = listener.id.toString();
  listenerForm.name = listener.name;
  listenerForm.bindAddress = listener.bindAddress;
  listenerForm.port = Number(listener.port);
  listenerForm.protocol = listener.protocol;
  listenerForm.enabled = listener.enabled;
  listenerForm.defaultBackendId = listener.defaultBackendId.toString();
  isListenerModalOpen.value = true;
}

function openAddRouteModal() {
  resetRouteForm();
  isRouteModalOpen.value = true;
}

function resetRouteForm() {
  routeForm.id = "";
  routeForm.listenerId = listeners.value[0]?.id.toString() ?? "";
  routeForm.priority = 100;
  routeForm.hostPattern = "";
  routeForm.pathPrefix = "";
  routeForm.backendId = backends.value[0]?.id.toString() ?? "";
  routeForm.enabled = true;
}

function editRoute(routeId: bigint) {
  const route = routes.value.find((item) => item.id === routeId);
  if (!route) return;
  routeForm.id = route.id.toString();
  routeForm.listenerId = route.listenerId.toString();
  routeForm.priority = Number(route.priority);
  routeForm.hostPattern = route.hostPattern;
  routeForm.pathPrefix = route.pathPrefix;
  routeForm.backendId = route.backendId.toString();
  routeForm.enabled = route.enabled;
  isRouteModalOpen.value = true;
}

function openAddTlsModal() {
  resetTlsForm();
  isTlsModalOpen.value = true;
}

function resetTlsForm() {
  tlsForm.id = "";
  tlsForm.listenerId = httpsListeners.value[0]?.id.toString() ?? "";
  tlsForm.hostnamePattern = "";
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
  tlsForm.certPem = null;
  tlsForm.keyPem = null;
  tlsForm.certFileName = "";
  tlsForm.keyFileName = "";
  tlsForm.enabled = cert.enabled;
  tlsUploadError.value = "";
  isTlsModalOpen.value = true;
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

async function run(action: () => Promise<void>) {
  if (!runManagementAction) return;
  await runManagementAction(action);
}

async function submitBackend() {
  await run(async () => {
    const isStatic = backendForm.backendType === PublicBackendType.STATIC;
    const payload = {
      name: backendForm.name,
      targetOrigin: isStatic ? "" : backendForm.targetOrigin,
      enabled: backendForm.enabled,
      backendType: backendForm.backendType,
      tlsSkipVerify: !isStatic && !backendForm.tlsVerify,
      staticStatusCode: BigInt(isStatic ? backendForm.staticStatusCode || 200 : 200),
      staticResponseHeaders: isStatic
        ? backendForm.staticResponseHeaders.map((header) => ({ name: header.name, value: header.value }))
        : [],
      staticResponseBody: isStatic ? backendForm.staticResponseBody : "",
    };
    if (backendForm.id) {
      await managementClient.updatePublicBackend({
        id: BigInt(backendForm.id),
        ...payload,
      });
    } else {
      await managementClient.createPublicBackend(payload);
    }
    isBackendModalOpen.value = false;
  });
}

async function deleteBackend(id: bigint) {
  if (!window.confirm("Delete this backend?")) return;
  await run(async () => {
    await managementClient.deletePublicBackend({ id });
  });
}

async function submitListener() {
  await run(async () => {
    const payload = {
      name: listenerForm.name,
      bindAddress: listenerForm.bindAddress,
      port: BigInt(listenerForm.port),
      protocol: listenerForm.protocol,
      enabled: listenerForm.enabled,
      defaultBackendId: BigInt(listenerForm.defaultBackendId || "0"),
    };
    if (listenerForm.id) {
      await managementClient.updatePublicListener({ id: BigInt(listenerForm.id), ...payload });
    } else {
      await managementClient.createPublicListener(payload);
    }
    isListenerModalOpen.value = false;
  });
}

async function deleteListener(id: bigint) {
  if (!window.confirm("Delete this listener?")) return;
  await run(async () => {
    await managementClient.deletePublicListener({ id });
  });
}

async function setListenerEnabled(listener: PublicListener, enabled: boolean) {
  await run(async () => {
    if (enabled) {
      await managementClient.enablePublicListener({ id: listener.id });
    } else {
      await managementClient.disablePublicListener({ id: listener.id });
    }
  });
}

async function setListenerRunning(listener: PublicListener, running: boolean) {
  await run(async () => {
    if (running) {
      await managementClient.startPublicListener({ id: listener.id });
    } else {
      await managementClient.stopPublicListener({ id: listener.id });
    }
  });
}

async function submitRoute() {
  await run(async () => {
    const payload = {
      listenerId: BigInt(routeForm.listenerId || "0"),
      priority: BigInt(routeForm.priority),
      hostPattern: routeForm.hostPattern,
      pathPrefix: routeForm.pathPrefix,
      backendId: BigInt(routeForm.backendId || "0"),
      enabled: routeForm.enabled,
    };
    if (routeForm.id) {
      await managementClient.updatePublicRoute({ id: BigInt(routeForm.id), ...payload });
    } else {
      await managementClient.createPublicRoute(payload);
    }
    isRouteModalOpen.value = false;
  });
}

async function deleteRoute(id: bigint) {
  if (!window.confirm("Delete this route?")) return;
  await run(async () => {
    await managementClient.deletePublicRoute({ id });
  });
}

async function submitTlsCertificate() {
  tlsUploadError.value = "";
  if (!tlsForm.id && (!tlsForm.certPem || !tlsForm.keyPem)) {
    tlsUploadError.value = "Upload both the certificate and private key.";
    return;
  }
  if (tlsHasPartialUpload.value) {
    tlsUploadError.value = "Upload both files to replace the certificate.";
    return;
  }

  await run(async () => {
    const payload = {
      listenerId: BigInt(tlsForm.listenerId || "0"),
      hostnamePattern: tlsForm.hostnamePattern,
      enabled: tlsForm.enabled,
      certPem: tlsForm.certPem ?? new Uint8Array(),
      keyPem: tlsForm.keyPem ?? new Uint8Array(),
    };
    if (tlsForm.id) {
      await managementClient.updatePublicTlsCertificate({ id: BigInt(tlsForm.id), ...payload });
    } else {
      await managementClient.createPublicTlsCertificate(payload);
    }
    isTlsModalOpen.value = false;
  });
}

async function deleteTlsCertificate(id: bigint) {
  if (!window.confirm("Delete this TLS certificate?")) return;
  await run(async () => {
    await managementClient.deletePublicTlsCertificate({ id });
  });
}

watch(backends, () => {
  if (!listenerForm.defaultBackendId && backends.value[0]) {
    listenerForm.defaultBackendId = backends.value[0].id.toString();
  }
  if (!routeForm.backendId && backends.value[0]) {
    routeForm.backendId = backends.value[0].id.toString();
  }
}, { immediate: true });

watch(listeners, () => {
  if (!routeForm.listenerId && listeners.value[0]) {
    routeForm.listenerId = listeners.value[0].id.toString();
  }
}, { immediate: true });

watch(httpsListeners, () => {
  if (!tlsForm.listenerId && httpsListeners.value[0]) {
    tlsForm.listenerId = httpsListeners.value[0].id.toString();
  }
}, { immediate: true });
</script>

<template>
  <div v-if="dashboard" class="space-y-8">
    <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
      <div>
        <h3 class="mb-2 text-xl font-bold">Proxy Control</h3>
        <p class="text-sm text-[#888]">Public listeners, backends, routes, and TLS mappings.</p>
      </div>
      <div class="flex items-center gap-3">
        <Tag :severity="proxySeverity" :value="proxyStateLabel(proxyState)" class="!bg-[#111] !border-[#333] !text-white" />
        <Button
          v-if="!proxyIsRunning"
          label="Start Proxy"
          class="!bg-white !text-black !border-white"
          :loading="isBusy && !proxyIsRunning"
          :disabled="isBusy"
          @click="setProxyRunning?.(true)"
        >
          <template #icon><PlusIcon class="h-4 w-4" /></template>
        </Button>
        <DangerButton
          v-else
          label="Stop Proxy"
          :loading="isBusy && proxyIsRunning"
          :disabled="isBusy"
          @click="setProxyRunning?.(false)"
        >
          <template #icon><BanIcon class="h-4 w-4" /></template>
        </DangerButton>
      </div>
    </div>

    <p v-if="proxyError" class="rounded-md border border-red-900/50 bg-red-950/20 px-4 py-3 text-sm text-red-400">
      {{ proxyError }}
    </p>

    <!-- Public Listeners List -->
    <section class="vercel-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Public Listeners</h4>
        <SecondaryButton size="small" label="Add Listener" @click="openAddListenerModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </div>
      <div class="overflow-x-auto">
        <table class="w-full min-w-[900px] text-sm">
          <thead class="border-b border-[#333] text-left text-xs uppercase tracking-wider text-[#888]">
            <tr>
              <th class="px-5 py-3">Name</th>
              <th class="px-5 py-3">Bind</th>
              <th class="px-5 py-3">Protocol</th>
              <th class="px-5 py-3">Backend</th>
              <th class="px-5 py-3">State</th>
              <th class="px-5 py-3 text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="listener in listeners" :key="listener.id.toString()" class="border-b border-[#1f1f1f] last:border-0">
              <td class="px-5 py-4 font-medium text-white">{{ listener.name }}</td>
              <td class="px-5 py-4 font-mono text-xs text-[#d4d4d8]">{{ bindLabel(listener) }}</td>
              <td class="px-5 py-4">{{ protocolLabel(listener.protocol) }}</td>
              <td class="px-5 py-4 text-[#d4d4d8]">{{ backendName(listener.defaultBackendId) }}</td>
              <td class="px-5 py-4">
                <div class="flex flex-col gap-1">
                  <Tag
                    :severity="listener.enabled ? severityForState(listenerState(listener)) : 'warn'"
                    :value="listenerStateLabel(listener)"
                    class="w-fit !bg-[#111] !border-[#333] !text-white"
                  />
                  <span v-if="listenerStatus(listener)?.lastError" class="max-w-[280px] truncate text-xs text-red-400">
                    {{ listenerStatus(listener)?.lastError }}
                  </span>
                </div>
              </td>
              <td class="px-5 py-4">
                <div class="flex justify-end gap-2">
                  <SecondaryButton
                    size="small"
                    :aria-label="listener.enabled ? 'Disable listener' : 'Enable listener'"
                    :title="listener.enabled ? 'Disable listener' : 'Enable listener'"
                    :disabled="isBusy"
                    @click="setListenerEnabled(listener, !listener.enabled)"
                  >
                    <template #icon>
                      <BanIcon v-if="listener.enabled" class="h-3.5 w-3.5" />
                      <CheckIcon v-else class="h-3.5 w-3.5" />
                    </template>
                  </SecondaryButton>
                  <SecondaryButton
                    size="small"
                    :aria-label="listenerStatus(listener)?.running ? 'Stop listener' : 'Start listener'"
                    :title="listenerStatus(listener)?.running ? 'Stop listener' : 'Start listener'"
                    :disabled="isBusy || !listener.enabled"
                    @click="setListenerRunning(listener, !listenerStatus(listener)?.running)"
                  >
                    <template #icon>
                      <TimesIcon v-if="listenerStatus(listener)?.running" class="h-3.5 w-3.5" />
                      <RefreshIcon v-else class="h-3.5 w-3.5" />
                    </template>
                  </SecondaryButton>
                  <SecondaryButton size="small" aria-label="Edit listener" title="Edit listener" :disabled="isBusy" @click="editListener(listener)">
                    <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
                  </SecondaryButton>
                  <DangerButton size="small" aria-label="Delete listener" title="Delete listener" :disabled="isBusy" @click="deleteListener(listener.id)">
                    <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
                  </DangerButton>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- Backends List -->
    <section class="vercel-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Backends</h4>
        <SecondaryButton size="small" label="Add Backend" @click="openAddBackendModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div v-for="backend in backends" :key="backend.id.toString()" class="flex items-center justify-between gap-3 px-5 py-4">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ backend.name }}</p>
              <Tag :value="backendTypeLabel(backend.backendType)" severity="info" class="!bg-[#111] !border-[#333] !text-white" />
              <Tag v-if="!backend.enabled" value="Disabled" severity="warn" class="!bg-[#111] !border-[#333] !text-white" />
            </div>
            <p class="truncate text-xs text-[#888] mt-1">{{ backendSummary(backend) }}</p>
          </div>
          <div class="flex gap-2">
            <SecondaryButton size="small" aria-label="Edit backend" title="Edit backend" @click="editBackend(backend)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <DangerButton size="small" aria-label="Delete backend" title="Delete backend" @click="deleteBackend(backend.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
      </div>
    </section>

    <!-- Routes and TLS Section -->
    <section class="grid gap-6 lg:grid-cols-2">
      <!-- Routes List -->
      <div class="vercel-card overflow-hidden h-fit">
        <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Routes</h4>
          <SecondaryButton size="small" label="Add Route" @click="openAddRouteModal">
            <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          </SecondaryButton>
        </div>
        <div class="divide-y divide-[#1f1f1f]">
          <div v-for="route in routes" :key="route.id.toString()" class="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]">
            <div class="min-w-0">
              <p class="truncate text-sm font-medium text-white">{{ listenerName(route.listenerId) }} -> {{ backendName(route.backendId) }}</p>
              <p class="truncate font-mono text-xs text-[#888]">
                {{ route.priority.toString() }} / {{ route.hostPattern || "*" }}{{ route.pathPrefix || "/" }}
              </p>
            </div>
            <div class="flex gap-2">
              <SecondaryButton size="small" aria-label="Edit route" title="Edit route" @click="editRoute(route.id)">
                <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
              </SecondaryButton>
              <DangerButton size="small" aria-label="Delete route" title="Delete route" @click="deleteRoute(route.id)">
                <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
              </DangerButton>
            </div>
          </div>
        </div>
      </div>

      <!-- TLS Certificates List -->
      <div class="vercel-card overflow-hidden h-fit">
        <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">TLS Certificates</h4>
          <SecondaryButton size="small" label="Add TLS Mapping" @click="openAddTlsModal">
            <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          </SecondaryButton>
        </div>
        <div class="divide-y divide-[#1f1f1f]">
          <div v-for="cert in tlsCertificates" :key="cert.id.toString()" class="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]">
            <div class="min-w-0">
              <div class="flex min-w-0 items-center gap-2">
                <p class="truncate text-sm font-medium text-white">{{ listenerName(cert.listenerId) }} / {{ cert.hostnamePattern }}</p>
                <Tag v-if="isDefaultSelfSignedCertificate(cert)" value="Self-signed" severity="info" class="!bg-[#111] !border-[#333] !text-white" />
                <Tag v-if="!cert.enabled" value="Disabled" severity="warn" class="!bg-[#111] !border-[#333] !text-white" />
              </div>
              <p class="truncate text-xs text-[#888]">{{ tlsCertificateSummary(cert) }}</p>
            </div>
            <div class="flex gap-2">
              <SecondaryButton size="small" aria-label="Edit TLS mapping" title="Edit TLS mapping" @click="editTlsCertificate(cert.id)">
                <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
              </SecondaryButton>
              <DangerButton size="small" aria-label="Delete TLS mapping" title="Delete TLS mapping" @click="deleteTlsCertificate(cert.id)">
                <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
              </DangerButton>
            </div>
          </div>
          <div v-if="httpsListeners.length && !tlsCertificates.length" class="px-5 py-4 text-sm text-[#888]">
            HTTPS listeners will use the fallback self-signed certificate.
          </div>
        </div>
      </div>
    </section>

    <!-- Modals -->
    <Modal v-model="isListenerModalOpen" :title="listenerForm.id ? 'Edit Listener' : 'Add Listener'" max-width="36rem">
      <form @submit.prevent="submitListener" class="grid gap-4 sm:grid-cols-2">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Name
          <input v-model="listenerForm.name" class="vercel-input text-sm normal-case tracking-normal" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Bind address
          <input v-model="listenerForm.bindAddress" class="vercel-input text-sm normal-case tracking-normal" placeholder="0.0.0.0" />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Port
          <input v-model.number="listenerForm.port" class="vercel-input text-sm normal-case tracking-normal" type="number" min="1" max="65535" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Protocol
          <select v-model="listenerForm.protocol" class="vercel-input text-sm normal-case tracking-normal">
            <option :value="PublicListenerProtocol.HTTP">HTTP</option>
            <option :value="PublicListenerProtocol.HTTPS">HTTPS</option>
          </select>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
          Default backend
          <select v-model="listenerForm.defaultBackendId" class="vercel-input text-sm normal-case tracking-normal" required>
            <option v-for="backend in backends" :key="backend.id.toString()" :value="backend.id.toString()">
              {{ backend.name }}
            </option>
          </select>
        </label>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8] sm:col-span-2 mt-2">
          <input v-model="listenerForm.enabled" type="checkbox" class="h-4 w-4 accent-white" />
          Enabled
        </label>
        <div class="sm:col-span-2 mt-4 flex justify-end gap-3">
          <SecondaryButton type="button" label="Cancel" @click="isListenerModalOpen = false" />
          <Button class="!bg-white !text-black !border-white" :label="listenerForm.id ? 'Save Changes' : 'Create Listener'" type="submit" :disabled="isBusy || !backends.length" />
        </div>
      </form>
    </Modal>

    <Modal v-model="isBackendModalOpen" :title="backendForm.id ? 'Edit Backend' : 'Add Backend'" max-width="36rem">
      <form @submit.prevent="submitBackend" class="grid gap-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Name
          <input v-model="backendForm.name" class="vercel-input text-sm normal-case tracking-normal" required />
        </label>
        <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Type
          <div class="grid grid-cols-2 rounded-md border border-[#333] bg-[#0b0b0b] p-1">
            <button
              type="button"
              class="rounded px-3 py-2 text-sm font-medium normal-case tracking-normal transition"
              :class="backendForm.backendType === PublicBackendType.PROXY_FORWARD ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
              @click="backendForm.backendType = PublicBackendType.PROXY_FORWARD"
            >
              Proxy forward
            </button>
            <button
              type="button"
              class="rounded px-3 py-2 text-sm font-medium normal-case tracking-normal transition"
              :class="backendForm.backendType === PublicBackendType.STATIC ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
              @click="backendForm.backendType = PublicBackendType.STATIC"
            >
              Static
            </button>
          </div>
        </div>
        <template v-if="backendForm.backendType === PublicBackendType.PROXY_FORWARD">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Target origin
            <input v-model="backendForm.targetOrigin" class="vercel-input text-sm normal-case tracking-normal" placeholder="https://example.com" required />
          </label>
          <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
            <input v-model="backendForm.tlsVerify" type="checkbox" class="h-4 w-4 accent-white" />
            Verify upstream TLS certificate
          </label>
        </template>
        <template v-else>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Status
            <input v-model.number="backendForm.staticStatusCode" type="number" min="100" max="599" class="vercel-input text-sm normal-case tracking-normal" required />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Body
            <textarea v-model="backendForm.staticResponseBody" class="vercel-input min-h-28 resize-y text-sm normal-case tracking-normal" />
          </label>
          <div class="grid gap-2">
            <div class="flex items-center justify-between gap-3">
              <p class="text-xs font-medium uppercase tracking-wider text-[#888]">Headers</p>
              <SecondaryButton size="small" label="Add Header" type="button" @click="addStaticHeader" />
            </div>
            <div v-for="(header, index) in backendForm.staticResponseHeaders" :key="index" class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
              <input v-model="header.name" class="vercel-input text-sm normal-case tracking-normal" placeholder="Content-Type" />
              <input v-model="header.value" class="vercel-input text-sm normal-case tracking-normal" placeholder="text/plain" />
              <DangerButton size="small" aria-label="Remove header" title="Remove header" type="button" @click="removeStaticHeader(index)">
                <template #icon><TimesIcon class="h-3.5 w-3.5" /></template>
              </DangerButton>
            </div>
          </div>
        </template>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8] mt-2">
          <input v-model="backendForm.enabled" type="checkbox" class="h-4 w-4 accent-white" />
          Enabled
        </label>
        <div class="mt-4 flex justify-end gap-3">
          <SecondaryButton type="button" label="Cancel" @click="isBackendModalOpen = false" />
          <Button class="!bg-white !text-black !border-white" :label="backendForm.id ? 'Save Changes' : 'Create Backend'" type="submit" :disabled="isBusy" />
        </div>
      </form>
    </Modal>

    <Modal v-model="isRouteModalOpen" :title="routeForm.id ? 'Edit Route' : 'Add Route'" max-width="36rem">
      <form @submit.prevent="submitRoute" class="grid gap-4 sm:grid-cols-2">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Listener
          <select v-model="routeForm.listenerId" class="vercel-input text-sm normal-case tracking-normal" required>
            <option v-for="listener in listeners" :key="listener.id.toString()" :value="listener.id.toString()">{{ listener.name }}</option>
          </select>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Backend
          <select v-model="routeForm.backendId" class="vercel-input text-sm normal-case tracking-normal" required>
            <option v-for="backend in backends" :key="backend.id.toString()" :value="backend.id.toString()">{{ backend.name }}</option>
          </select>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Priority
          <input v-model.number="routeForm.priority" type="number" class="vercel-input text-sm normal-case tracking-normal" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Host pattern
          <input v-model="routeForm.hostPattern" class="vercel-input text-sm normal-case tracking-normal" placeholder="*.example.com" />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
          Path prefix
          <input v-model="routeForm.pathPrefix" class="vercel-input text-sm normal-case tracking-normal" placeholder="/api" />
        </label>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8] sm:col-span-2 mt-2">
          <input v-model="routeForm.enabled" type="checkbox" class="h-4 w-4 accent-white" />
          Enabled
        </label>
        <div class="sm:col-span-2 mt-4 flex justify-end gap-3">
          <SecondaryButton type="button" label="Cancel" @click="isRouteModalOpen = false" />
          <Button class="!bg-white !text-black !border-white" :label="routeForm.id ? 'Save Changes' : 'Create Route'" type="submit" :disabled="isBusy || !listeners.length || !backends.length" />
        </div>
      </form>
    </Modal>

    <Modal v-model="isTlsModalOpen" :title="tlsForm.id ? 'Edit TLS Mapping' : 'Add TLS Mapping'" max-width="36rem">
      <form @submit.prevent="submitTlsCertificate" class="grid gap-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          HTTPS listener
          <select v-model="tlsForm.listenerId" class="vercel-input text-sm normal-case tracking-normal" required>
            <option v-for="listener in httpsListeners" :key="listener.id.toString()" :value="listener.id.toString()">{{ listener.name }}</option>
          </select>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Hostname pattern
          <input v-model="tlsForm.hostnamePattern" class="vercel-input text-sm normal-case tracking-normal" placeholder="app.example.com" required />
        </label>
        <div class="grid gap-3 sm:grid-cols-2">
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
        <p v-if="tlsForm.id" class="rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-2 text-xs text-[#888]">
          Current certificate is stored in the app config directory.
        </p>
        <p v-if="tlsUploadError" class="rounded-md border border-red-900/50 bg-red-950/20 px-3 py-2 text-sm text-red-400">
          {{ tlsUploadError }}
        </p>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8] mt-2">
          <input v-model="tlsForm.enabled" type="checkbox" class="h-4 w-4 accent-white" />
          Enabled
        </label>
        <div class="mt-4 flex justify-end gap-3">
          <SecondaryButton type="button" label="Cancel" @click="isTlsModalOpen = false" />
          <Button class="!bg-white !text-black !border-white" :label="tlsForm.id ? 'Save Changes' : 'Create TLS Mapping'" type="submit" :disabled="tlsSubmitDisabled" />
        </div>
      </form>
    </Modal>

    <div class="border-t border-[#333] pt-8">
      <h4 class="mb-4 text-sm font-semibold uppercase tracking-widest text-red-500">Danger Zone</h4>
      <div class="vercel-card border-red-900/50 p-6 flex items-center justify-between gap-4">
        <div>
          <p class="font-medium">Reset Session</p>
          <p class="text-sm text-[#888]">This will log you out and clear current dashboard state.</p>
        </div>
        <SecondaryButton label="Log out" class="!border-red-900/50 !text-red-500" @click="logout?.()" />
      </div>
    </div>
  </div>
</template>
