<script setup lang="ts">
import { computed, h, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { NButton, NDataTable, NTabPane, NTabs, NTag } from "naive-ui";
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

const proxySectionKeys = ["routes", "listeners"] as const;
type ProxySectionKey = typeof proxySectionKeys[number];
type ProxySectionSummary = {
  key: ProxySectionKey;
  label: string;
  value: string;
  detail: string;
  description: string;
};
type ProxySummaryCard = ProxySectionSummary | {
  key: "targets" | "proxy";
  label: string;
  value: string;
  detail: string;
};

const managementClient = useManagementClient();
const route = useRoute();
const router = useRouter();

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
    render: (listener) => h("span", { class: "mono-text copy-xs" }, bindLabel(listener)),
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
    render: (listener) => h("div", { class: "layout-row layout-column space-2xs" }, [
      h(
        NTag,
        {
          size: "small",
          bordered: false,
          type: naiveTagType(listener.enabled ? severityForState(listenerRuntimeState(listener, listenerStatus(listener))) : "warn"),
          class: "fit-width",
        },
        { default: () => listenerStateLabel(listener, listenerStatus(listener)) },
      ),
      listenerStatus(listener)?.lastError
        ? h("span", { class: "max-token-width clip-text copy-xs error-text" }, listenerStatus(listener)?.lastError)
        : null,
    ]),
  },
  {
    title: "Actions",
    key: "actions",
    width: 220,
    align: "right",
    render: (listener) => h("div", { class: "layout-row align-end-row space-sm" }, [
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
            { icon: () => listener.enabled ? h(BanIcon, { class: "icon-sm" }) : h(CheckIcon, { class: "icon-sm" }) },
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
            { icon: () => listenerStatus(listener)?.running ? h(TimesIcon, { class: "icon-sm" }) : h(RefreshIcon, { class: "icon-sm" }) },
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
            { icon: () => h(PencilIcon, { class: "icon-sm" }) },
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
            { icon: () => h(TrashIcon, { class: "icon-sm" }) },
          ),
        },
      ),
    ]),
  },
]);

const editorHost = ref<InstanceType<typeof PublicProxyEditorHost> | null>(null);
const { confirm } = useConfirmDialog();

const proxySections = computed<ProxySectionSummary[]>(() => [
  {
    key: "routes",
    label: "Routes",
    value: routes.value.length.toString(),
    detail: `${routeTargets.value.length.toString()} route targets`,
    description: "Rules that match incoming requests to route targets.",
  },
  {
    key: "listeners",
    label: "Public Listeners",
    value: listeners.value.length.toString(),
    detail: `${runningListeners.value.toString()} running`,
    description: "Incoming endpoints where the proxy accepts connections.",
  },
]);
const summaryCards = computed<ProxySummaryCard[]>(() => [
  ...proxySections.value,
  { key: "targets", label: "Targets", value: routeTargets.value.length.toString(), detail: "proxy and static destinations" },
  { key: "proxy", label: "Proxy", value: proxyStateLabel(proxyState.value, status.value?.proxyRunning), detail: proxyIsRunning.value ? "accepting traffic" : "not running" },
]);
const activeProxySection = computed<ProxySectionKey>(() => normalizeProxySection(route.params.section));
const activeProxyMeta = computed(() => (
  proxySections.value.find((section) => section.key === activeProxySection.value) ?? proxySections.value[0]
));

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

function normalizeProxySection(value: unknown): ProxySectionKey {
  const section = Array.isArray(value) ? value[0] : value;
  return proxySectionKeys.includes(section as ProxySectionKey) ? section as ProxySectionKey : "routes";
}

async function selectProxySection(value: string | number) {
  const section = normalizeProxySection(value);
  if (section === activeProxySection.value) return;
  await router.push(`/proxy/${section}`);
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
  <div v-if="dashboard" class="stack-xl">
    <div class="layout-row layout-column space-lg mq-md-row mq-md-align-end mq-md-spread">
      <div>
        <h3 class="margin-bottom-sm copy-xl weight-bold">Proxy</h3>
        <p class="copy-sm muted-text">{{ activeProxyMeta.description }}</p>
      </div>
      <div class="layout-row align-center space-md">
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
            <template #icon><PlusIcon class="icon-md icon-md" /></template>
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
            <template #icon><BanIcon class="icon-md icon-md" /></template>
            Stop Proxy
          </NButton>
        </DisabledHint>
      </div>
    </div>

    <p v-if="proxyError" class="round-md framed error-border error-surface pad-x-lg pad-y-md copy-sm error-text">
      {{ proxyError }}
    </p>

    <section class="summary-grid summary-grid--four proxy-summary-grid" aria-label="Proxy configuration summary">
      <div
        v-for="card in summaryCards"
        :key="card.key"
        class="surface-card pad-lg proxy-summary-card"
        :class="{ 'proxy-summary-card--active': card.key === activeProxySection }"
      >
        <p class="copy-xs weight-semibold label-case letter-widest muted-text">{{ card.label }}</p>
        <p class="margin-top-sm copy-2xl weight-semibold base-text">{{ card.value }}</p>
        <p class="margin-top-xs copy-xs muted-text">{{ card.detail }}</p>
      </div>
    </section>

    <NTabs class="proxy-tabs" type="line" animated :value="activeProxySection" @update:value="selectProxySection">
      <NTabPane name="routes" :tab="`Routes (${routes.length})`">
        <section class="surface-card hide-overflow">
          <div class="divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
            <div>
              <h4 class="copy-sm weight-semibold label-case letter-widest muted-text">Routes</h4>
              <p class="margin-top-2xs copy-xs muted-text normal-text letter-normal">Rules that match incoming requests to route targets.</p>
            </div>
            <NButton secondary size="small" @click="openAddRouteModal">
              <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
              Add Route
            </NButton>
          </div>
          <div class="divided-list">
            <div
              v-for="route in routes"
              :key="route.id.toString()"
              :data-testid="`route-row-${route.id.toString()}`"
              class="layout-grid space-md pad-x-xl pad-y-lg mq-sm-one-auto"
            >
              <div class="min-width-zero">
                <div class="layout-row min-width-zero align-center space-sm">
                  <p class="clip-text copy-sm weight-medium base-text">{{ listenerName(route.listenerId, listeners) }} -> {{ routeDestinationLabel(route) }}</p>
                  <span
                    v-if="routeAction(route) === PublicRouteAction.REDIRECT"
                    class="no-shrink round-sm framed accent-frame pad-x-xs pad-y-2xs copy-3xs weight-semibold label-case letter-wide accent-text"
                  >
                    Redirect
                  </span>
                </div>
                <p class="clip-text mono-text copy-xs muted-text">
                  {{ route.priority.toString() }} / {{ route.hostPattern || "*" }}{{ route.pathPrefix || "/" }}
                </p>
                <p class="clip-text mono-text copy-xs muted-text">
                  {{ routeTargetSummary(route) }}
                </p>
              </div>
              <div class="layout-row space-sm">
                <NButton secondary size="small" aria-label="Edit route" title="Edit route" @click="editRoute(route.id)">
                  <template #icon><PencilIcon class="icon-sm icon-sm" /></template>
                </NButton>
                <NButton secondary size="small" aria-label="Clone route" title="Clone route" @click="cloneRoute(route.id)">
                  <template #icon><WindowMaximizeIcon class="icon-sm icon-sm" /></template>
                </NButton>
                <NButton type="error" size="small" aria-label="Delete route" title="Delete route" @click="deleteRoute(route.id)">
                  <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
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
      </NTabPane>

      <NTabPane name="listeners" :tab="`Public Listeners (${listeners.length})`">
        <section class="surface-card hide-overflow">
          <div class="divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
            <div>
              <h4 class="copy-sm weight-semibold label-case letter-widest muted-text">Public Listeners</h4>
              <p class="margin-top-2xs copy-xs muted-text normal-text letter-normal">Incoming endpoints where the proxy accepts connections.</p>
            </div>
            <NButton secondary size="small" @click="openAddListenerModal">
              <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
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
      </NTabPane>
    </NTabs>

    <PublicProxyEditorHost ref="editorHost" :config="config" />
  </div>
</template>

<style scoped>
.proxy-summary-card {
  min-height: 7rem;
  padding: 0.875rem;
  transition: border-color 160ms ease, box-shadow 160ms ease;
}

.proxy-summary-grid {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.proxy-summary-card--active {
  border-color: var(--app-accent);
  box-shadow: inset 0 0 0 1px var(--app-accent-soft);
}

.proxy-summary-card--active .base-text {
  color: var(--app-accent);
}

.proxy-tabs {
  min-width: 0;
}

.proxy-tabs :deep(.n-tabs-nav) {
  margin-bottom: 1rem;
}

.proxy-tabs :deep(.n-tab-pane) {
  padding-top: 0.25rem;
}

@media (min-width: 900px) {
  .proxy-summary-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }

  .proxy-summary-card {
    min-height: 0;
    padding: 1rem;
  }
}
</style>
