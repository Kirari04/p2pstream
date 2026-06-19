<script setup lang="ts">
import { computed, h, ref } from "vue";
import { NButton, NDataTable, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { Ban as BanIcon } from "@lucide/vue";
import { Check as CheckIcon } from "@lucide/vue";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { X as TimesIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { Copy as WindowMaximizeIcon } from "@lucide/vue";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementContext } from "@/composables/useManagementContext";
import { BUSY_REASON } from "@/lib/disabledReasons";
import {
  bindLabel,
  listenerName,
  listenerRuntimeState,
  listenerStateLabel,
  protocolLabel,
  proxyStateLabel,
  routeAction,
  routeDestinationLabel,
  routeTargetSummary,
  severityForState,
} from "@/lib/publicProxyLabels";
import { naiveTagType } from "@/lib/naiveUi";
import {
  ProxyState,
  PublicRouteAction,
  type PublicListener,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

const {
  dashboard,
  publicProxyConfig,
  isBusy,
  runManagementAction,
  setProxyRunning,
} = useManagementContext();

const status = computed(() => dashboard.value?.status ?? null);
const config = computed(() => publicProxyConfig.value ?? null);
const proxyState = computed(() => status.value?.proxy?.state ?? ProxyState.UNSPECIFIED);
const proxyIsRunning = computed(() => proxyState.value === ProxyState.RUNNING || status.value?.proxyRunning === true);
const proxyError = computed(() => status.value?.proxy?.lastError || status.value?.proxyLastError || "");
const proxySeverity = computed(() => severityForState(proxyState.value));
const listeners = computed(() => config.value?.listeners ?? []);
const routeTargets = computed(() => config.value?.routeTargets ?? []);
const routes = computed(() => config.value?.routes ?? []);
const listenerStatuses = computed(() => config.value?.proxy?.listeners ?? status.value?.proxy?.listeners ?? []);
const runningListeners = computed(() => listeners.value.filter((listener) => listenerStatus(listener)?.running).length);
const busyDisabledReason = computed(() => isBusy.value ? BUSY_REASON : "");
const listenerColumns = computed<DataTableColumns<PublicListener>>(() => [
  {
    title: "Name",
    key: "name",
    minWidth: 180,
    ellipsis: { tooltip: true },
    render: (listener) => listener.name,
  },
  {
    title: "Bind",
    key: "bind",
    minWidth: 180,
    render: (listener) => h("span", { class: "font-mono text-xs" }, bindLabel(listener)),
  },
  {
    title: "Protocol",
    key: "protocol",
    width: 120,
    render: (listener) => protocolLabel(listener.protocol),
  },
  {
    title: "Routes",
    key: "routes",
    width: 100,
    render: (listener) => routes.value.filter((route) => route.listenerId === listener.id).length.toString(),
  },
  {
    title: "State",
    key: "state",
    minWidth: 180,
    render: (listener) => h("div", { class: "flex flex-col gap-1" }, [
      h(
        NTag,
        {
          size: "small",
          bordered: false,
          type: naiveTagType(listener.enabled ? severityForState(listenerRuntimeState(listener, listenerStatus(listener))) : "warn"),
          class: "w-fit",
        },
        { default: () => listenerStateLabel(listener, listenerStatus(listener)) },
      ),
      listenerStatus(listener)?.lastError
        ? h("span", { class: "max-w-[280px] truncate text-xs text-red-400" }, listenerStatus(listener)?.lastError)
        : null,
    ]),
  },
  {
    title: "Actions",
    key: "actions",
    width: 220,
    align: "right",
    render: (listener) => h("div", { class: "flex justify-end gap-2" }, [
      h(
        DisabledHint,
        { disabled: Boolean(busyDisabledReason.value), reason: busyDisabledReason.value },
        {
          default: () => h(
            NButton,
            {
              secondary: true,
              size: "small",
              "aria-label": listener.enabled ? "Disable listener" : "Enable listener",
              title: listener.enabled ? "Disable listener" : "Enable listener",
              disabled: Boolean(busyDisabledReason.value),
              onClick: () => void setListenerEnabled(listener, !listener.enabled),
            },
            { icon: () => listener.enabled ? h(BanIcon, { class: "h-3.5 w-3.5" }) : h(CheckIcon, { class: "h-3.5 w-3.5" }) },
          ),
        },
      ),
      h(
        DisabledHint,
        { disabled: Boolean(listenerRunningDisabledReason(listener)), reason: listenerRunningDisabledReason(listener) },
        {
          default: () => h(
            NButton,
            {
              secondary: true,
              size: "small",
              "aria-label": listenerStatus(listener)?.running ? "Stop listener" : "Start listener",
              title: listenerStatus(listener)?.running ? "Stop listener" : "Start listener",
              disabled: Boolean(listenerRunningDisabledReason(listener)),
              onClick: () => void setListenerRunning(listener, !listenerStatus(listener)?.running),
            },
            { icon: () => listenerStatus(listener)?.running ? h(TimesIcon, { class: "h-3.5 w-3.5" }) : h(RefreshIcon, { class: "h-3.5 w-3.5" }) },
          ),
        },
      ),
      h(
        DisabledHint,
        { disabled: Boolean(busyDisabledReason.value), reason: busyDisabledReason.value },
        {
          default: () => h(
            NButton,
            {
              secondary: true,
              size: "small",
              "aria-label": "Edit listener",
              title: "Edit listener",
              disabled: Boolean(busyDisabledReason.value),
              onClick: () => editListener(listener),
            },
            { icon: () => h(PencilIcon, { class: "h-3.5 w-3.5" }) },
          ),
        },
      ),
      h(
        DisabledHint,
        { disabled: Boolean(busyDisabledReason.value), reason: busyDisabledReason.value },
        {
          default: () => h(
            NButton,
            {
              type: "error",
              size: "small",
              "aria-label": "Delete listener",
              title: "Delete listener",
              disabled: Boolean(busyDisabledReason.value),
              onClick: () => void deleteListener(listener.id),
            },
            { icon: () => h(TrashIcon, { class: "h-3.5 w-3.5" }) },
          ),
        },
      ),
    ]),
  },
]);

const editorHost = ref<InstanceType<typeof PublicProxyEditorHost> | null>(null);
const { confirm } = useConfirmDialog();

const summaryCards = computed(() => [
  { label: "Listeners", value: listeners.value.length.toString(), detail: `${runningListeners.value.toString()} running` },
  { label: "Targets", value: routeTargets.value.length.toString(), detail: "proxy and static destinations" },
  { label: "Routes", value: routes.value.length.toString(), detail: "listener match rules" },
  { label: "Proxy", value: proxyStateLabel(proxyState.value, status.value?.proxyRunning), detail: proxyIsRunning.value ? "accepting traffic" : "not running" },
]);

function listenerStatus(listener: PublicListener) {
  return listenerStatuses.value.find((item) => item.listenerId === listener.id);
}

function listenerRowKey(listener: PublicListener): string {
  return listener.id.toString();
}

function listenerRowProps(listener: PublicListener): Record<string, string> {
  return {
    "data-testid": `listener-row-${listener.id.toString()}`,
  };
}

function listenerRunningDisabledReason(listener: PublicListener): string {
  if (isBusy.value) return BUSY_REASON;
  if (!listener.enabled) return "Enable this listener before starting it.";
  return "";
}

async function run(action: () => Promise<void>) {
  if (!runManagementAction) return;
  await runManagementAction(action);
}

function openAddListenerModal() {
  editorHost.value?.openCreateListener();
}

function editListener(listener: PublicListener) {
  editorHost.value?.openListener(listener.id);
}

function openAddRouteModal() {
  editorHost.value?.openCreateRoute();
}

function editRoute(routeId: bigint) {
  editorHost.value?.openRoute(routeId);
}

function cloneRoute(routeId: bigint) {
  editorHost.value?.openCloneRoute(routeId);
}

async function deleteListener(id: bigint) {
  if (!await confirm("Delete Listener", "This listener will stop accepting connections and be permanently removed.")) return;
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

async function deleteRoute(id: bigint) {
  if (!await confirm("Delete Route", "This route and its targets will be permanently removed. Traffic matching it will fall through to other routes or the default route.")) return;
  await run(async () => {
    await managementClient.deletePublicRoute({ id });
  });
}
</script>

<template>
  <div v-if="dashboard" class="space-y-8">
    <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
      <div>
        <h3 class="mb-2 text-xl font-bold">Proxy</h3>
        <p class="text-sm text-[var(--app-text-muted)]">Public listeners, routes, and route targets.</p>
      </div>
      <div class="flex items-center gap-3">
        <NTag size="small" :bordered="false" :type="naiveTagType(proxySeverity)">
          {{ proxyStateLabel(proxyState, status?.proxyRunning) }}
        </NTag>
        <DisabledHint v-if="!proxyIsRunning" :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
          <NButton
            type="primary"
            :loading="isBusy && !proxyIsRunning"
            :disabled="Boolean(busyDisabledReason)"
            @click="setProxyRunning?.(true)"
          >
            <template #icon><PlusIcon class="h-4 w-4" /></template>
            Start Proxy
          </NButton>
        </DisabledHint>
        <DisabledHint v-else :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
          <NButton
            type="error"
            :loading="isBusy && proxyIsRunning"
            :disabled="Boolean(busyDisabledReason)"
            @click="setProxyRunning?.(false)"
          >
            <template #icon><BanIcon class="h-4 w-4" /></template>
            Stop Proxy
          </NButton>
        </DisabledHint>
      </div>
    </div>

    <p v-if="proxyError" class="rounded-md border border-red-900/50 bg-red-950/20 px-4 py-3 text-sm text-red-400">
      {{ proxyError }}
    </p>

    <section class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div v-for="card in summaryCards" :key="card.label" class="app-card p-4">
        <p class="text-xs font-semibold uppercase tracking-widest text-[var(--app-text-muted)]">{{ card.label }}</p>
        <p class="mt-2 text-2xl font-semibold text-[var(--app-text)]">{{ card.value }}</p>
        <p class="mt-1 text-xs text-[var(--app-text-muted)]">{{ card.detail }}</p>
      </div>
    </section>

    <section class="app-card overflow-hidden">
      <div class="border-b border-[var(--app-border)] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[var(--app-text-muted)]">Public Listeners</h4>
          <p class="mt-0.5 text-xs text-[var(--app-text-muted)] normal-case tracking-normal">Incoming endpoints where the proxy accepts connections.</p>
        </div>
        <NButton secondary size="small" @click="openAddListenerModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          Add Listener
        </NButton>
      </div>
      <div>
        <NDataTable
          v-if="listeners.length"
          :columns="listenerColumns"
          :data="listeners"
          :row-key="listenerRowKey"
          :row-props="listenerRowProps"
          :pagination="false"
          :bordered="false"
          :single-line="false"
          :scroll-x="900"
          size="small"
        />
        <EmptyState
          v-else
          title="No listeners configured"
          description="Listeners accept public HTTP or HTTPS traffic on published ports."
          action-label="Add Listener"
          @action="openAddListenerModal"
        />
      </div>
    </section>

    <section class="app-card overflow-hidden">
      <div class="border-b border-[var(--app-border)] px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[var(--app-text-muted)]">Routes</h4>
          <p class="mt-0.5 text-xs text-[var(--app-text-muted)] normal-case tracking-normal">Rules that match incoming requests to route targets.</p>
        </div>
        <NButton secondary size="small" @click="openAddRouteModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          Add Route
        </NButton>
      </div>
      <div class="divide-y divide-[var(--app-border-subtle)]">
        <div
          v-for="route in routes"
          :key="route.id.toString()"
          :data-testid="`route-row-${route.id.toString()}`"
          class="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]"
        >
          <div class="min-w-0">
            <div class="flex min-w-0 items-center gap-2">
              <p class="truncate text-sm font-medium text-[var(--app-text)]">{{ listenerName(route.listenerId, listeners) }} -> {{ routeDestinationLabel(route) }}</p>
              <span
                v-if="routeAction(route) === PublicRouteAction.REDIRECT"
                class="shrink-0 rounded border border-[var(--app-accent)] px-1.5 py-0.5 text-[0.62rem] font-semibold uppercase tracking-wider text-[var(--app-accent)]"
              >
                Redirect
              </span>
            </div>
            <p class="truncate font-mono text-xs text-[var(--app-text-muted)]">
              {{ route.priority.toString() }} / {{ route.hostPattern || "*" }}{{ route.pathPrefix || "/" }}
            </p>
            <p class="truncate font-mono text-xs text-[var(--app-text-muted)]">
              {{ routeTargetSummary(route) }}
            </p>
          </div>
          <div class="flex gap-2">
            <NButton secondary size="small" aria-label="Edit route" title="Edit route" @click="editRoute(route.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </NButton>
            <NButton secondary size="small" aria-label="Clone route" title="Clone route" @click="cloneRoute(route.id)">
              <template #icon><WindowMaximizeIcon class="h-3.5 w-3.5" /></template>
            </NButton>
            <NButton type="error" size="small" aria-label="Delete route" title="Delete route" @click="deleteRoute(route.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </NButton>
          </div>
        </div>
        <EmptyState
          v-if="!routes.length"
          title="No routes configured"
          description="Routes match hosts and paths before forwarding, redirecting, or using listener defaults."
          action-label="Add Route"
          @action="openAddRouteModal"
        />
      </div>
    </section>

    <PublicProxyEditorHost ref="editorHost" :config="config" />
  </div>
</template>
