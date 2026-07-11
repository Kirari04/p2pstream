<script setup lang="ts">
import { computed, h, ref, type HTMLAttributes } from "vue";
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
type ProxySummaryFact = ProxySectionSummary | {
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
const summaryFacts = computed<ProxySummaryFact[]>(() => [
  { key: "proxy", label: "Proxy", value: proxyStateLabel(proxyState.value, status.value?.proxyRunning), detail: proxyIsRunning.value ? "accepting traffic" : "not running" },
  proxySections.value[1],
  proxySections.value[0],
  { key: "targets", label: "Targets", value: routeTargets.value.length.toString(), detail: "proxy and static destinations" },
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

function proxyTabProps(section: ProxySectionKey, label: string): HTMLAttributes {
  return {
    id: `proxy-tab-${section}`,
    role: "tab",
    tabindex: 0,
    "aria-label": label,
    "aria-selected": activeProxySection.value === section,
    onKeydown: (event: KeyboardEvent) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      void selectProxySection(section);
    },
  };
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
    <header class="page-toolbar">
      <div class="page-toolbar__body">
        <h1 class="margin-bottom-sm copy-2xl weight-bold">Proxy</h1>
        <p id="proxy-page-description" class="copy-sm muted-text">{{ activeProxyMeta.description }}</p>
      </div>
      <div class="page-toolbar__actions" aria-live="polite">
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
            <template #icon><PlusIcon class="icon-md" /></template>
            Start proxy
          </NButton>
        </DisabledHint>
        <DisabledHint v-else :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
          <NButton
            type="error"
            :loading="isBusy && proxyIsRunning"
            :disabled="Boolean(busyDisabledReason)"
            @click="setProxyRunning?.(false)"
          >
            <template #icon><BanIcon class="icon-md" /></template>
            Stop proxy
          </NButton>
        </DisabledHint>
      </div>
    </header>

    <p v-if="proxyError" role="alert" class="proxy-error round-md framed error-border error-surface pad-x-lg pad-y-md copy-sm error-text">
      {{ proxyError }}
    </p>

    <dl class="summary-grid summary-grid--four proxy-summary-grid" aria-label="Proxy configuration summary">
      <div
        v-for="fact in summaryFacts"
        :key="fact.key"
        class="proxy-summary-card"
        :class="{ 'proxy-summary-card--active': fact.key === activeProxySection }"
      >
        <dt>{{ fact.label }}</dt>
        <dd>
          <strong class="base-text">{{ fact.value }}</strong>
          <small>{{ fact.detail }}</small>
        </dd>
      </div>
    </dl>

    <NTabs
      class="proxy-tabs"
      type="line"
      animated
      role="group"
      aria-label="Proxy configuration sections"
      aria-describedby="proxy-page-description"
      :value="activeProxySection"
      @update:value="selectProxySection"
    >
      <NTabPane
        id="proxy-panel-routes"
        name="routes"
        role="tabpanel"
        aria-labelledby="proxy-tab-routes"
        :tab="`Routes · ${routes.length}`"
        :tab-props="proxyTabProps('routes', `${routes.length.toString()} routes configured`)"
      >
        <section class="surface-card hide-overflow">
          <div class="workbench-section-header divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
            <div>
              <h2 class="copy-base weight-semibold">Routes</h2>
              <p class="margin-top-xs copy-sm muted-text">Rules that match incoming requests to route targets.</p>
            </div>
            <NButton type="primary" size="small" @click="openAddRouteModal">
              <template #icon><PlusIcon class="icon-sm" /></template>
              Add Route
            </NButton>
          </div>
          <div
            v-if="routes.length"
            class="route-table"
            role="table"
            aria-label="Configured proxy routes"
            :aria-rowcount="routes.length + 1"
          >
            <div class="route-table__header" role="row">
              <span role="columnheader">Route and match</span>
              <span role="columnheader">Action and targets</span>
              <span role="columnheader">Priority and state</span>
              <span class="route-table__actions-heading" role="columnheader">Actions</span>
            </div>
            <div
              v-for="route in routes"
              :key="route.id.toString()"
              :data-testid="`route-row-${route.id.toString()}`"
              class="route-table__row"
              role="row"
            >
              <div class="route-table__identity" role="cell">
                <div class="route-table__primary-line">
                  <span class="route-table__listener">{{ listenerName(route.listenerId, listeners) }}</span>
                  <span class="route-table__id mono-text">#{{ route.id.toString() }}</span>
                  <span v-if="route.isDefault" class="route-table__default">Default</span>
                </div>
                <p class="route-table__technical mono-text" dir="auto">
                  {{ route.hostPattern || "*" }}{{ route.pathPrefix || "/" }}
                </p>
              </div>

              <div class="route-table__destination" role="cell">
                <div class="route-table__primary-line">
                  <span
                    class="route-table__action"
                    :class="{ 'route-table__action--redirect': routeAction(route) === PublicRouteAction.REDIRECT }"
                  >
                    {{ routeAction(route) === PublicRouteAction.REDIRECT ? "Redirect" : "Forward" }}
                  </span>
                  <span class="route-table__destination-label">{{ routeDestinationLabel(route) }}</span>
                </div>
                <p class="route-table__technical mono-text" dir="auto">{{ routeTargetSummary(route) }}</p>
              </div>

              <div class="route-table__status" role="cell">
                <span class="route-table__priority mono-text">{{ route.priority.toString() }}</span>
                <span class="route-table__state" :class="{ 'route-table__state--disabled': !route.enabled }">
                  <span class="route-table__state-dot" aria-hidden="true"></span>
                  {{ route.enabled ? "Enabled" : "Disabled" }}
                </span>
              </div>

              <div class="route-table__actions" role="cell">
                <NButton secondary size="small" aria-label="Edit route" title="Edit route" @click="editRoute(route.id)">
                  <template #icon><PencilIcon class="icon-sm" /></template>
                </NButton>
                <NButton secondary size="small" aria-label="Clone route" title="Clone route" @click="cloneRoute(route.id)">
                  <template #icon><WindowMaximizeIcon class="icon-sm" /></template>
                </NButton>
                <NButton type="error" size="small" aria-label="Delete route" title="Delete route" @click="deleteRoute(route.id)">
                  <template #icon><TrashIcon class="icon-sm" /></template>
                </NButton>
              </div>
            </div>
          </div>
          <EmptyState
            v-else
            title="No routes configured"
            description="Routes match hosts and paths before forwarding, redirecting, or using listener defaults."
            action-label="Add Route"
            @action="openAddRouteModal"
          />
        </section>
      </NTabPane>

      <NTabPane
        id="proxy-panel-listeners"
        name="listeners"
        role="tabpanel"
        aria-labelledby="proxy-tab-listeners"
        :tab="`Public Listeners · ${listeners.length}`"
        :tab-props="proxyTabProps('listeners', `${listeners.length.toString()} public listeners configured`)"
      >
        <section class="surface-card hide-overflow">
          <div class="workbench-section-header divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-lg">
            <div>
              <h2 class="copy-base weight-semibold">Public Listeners</h2>
              <p class="margin-top-xs copy-sm muted-text">Incoming endpoints where the proxy accepts connections.</p>
            </div>
            <NButton type="primary" size="small" @click="openAddListenerModal">
              <template #icon><PlusIcon class="icon-sm" /></template>
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
              :scroll-x="980"
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
.proxy-summary-grid {
  margin: 0;
}

.proxy-summary-card {
  display: grid;
  align-content: center;
  gap: 0.25rem;
}

.proxy-summary-card dt,
.proxy-summary-card small {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.45;
}

.proxy-summary-card dt {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 600;
}

.proxy-summary-card small {
  overflow-wrap: anywhere;
}

.proxy-summary-card dd {
  display: grid;
  gap: 0.125rem;
  min-width: 0;
  margin: 0;
}

.proxy-summary-card strong {
  overflow: hidden;
  font-size: 1rem;
  font-variant-numeric: tabular-nums;
  font-weight: 650;
  line-height: 1.4;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.proxy-error {
  overflow-wrap: anywhere;
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

.workbench-section-header {
  flex-direction: column;
  align-items: stretch;
}

.workbench-section-header > :deep(.n-button) {
  width: 100%;
}

.route-table {
  min-width: 0;
}

.route-table__header,
.route-table__row {
  display: grid;
  grid-template-columns:
    minmax(13rem, 1.15fr)
    minmax(15rem, 1.4fr)
    minmax(7.75rem, 0.45fr)
    7.5rem;
  column-gap: 1rem;
  align-items: center;
  padding-inline: 1.25rem;
}

.route-table__header {
  min-height: 2.25rem;
  border-bottom: 1px solid var(--app-border-subtle);
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-weight: 600;
  line-height: 1.25;
}

.route-table__actions-heading {
  text-align: right;
}

.route-table__row {
  min-height: 3.75rem;
  padding-block: 0.5rem;
  transition: background-color 160ms ease-out;
}

.route-table__row + .route-table__row {
  border-top: 1px solid var(--app-border-subtle);
}

.route-table__row:hover,
.route-table__row:focus-within {
  background: var(--app-hover);
}

.route-table__identity,
.route-table__destination,
.route-table__status {
  min-width: 0;
}

.route-table__primary-line {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.5rem;
  line-height: 1.35;
}

.route-table__listener,
.route-table__destination-label {
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
  text-overflow: ellipsis;
  unicode-bidi: plaintext;
  white-space: nowrap;
}

.route-table__id {
  flex: 0 0 auto;
  color: var(--app-text-muted);
  font-size: 0.6875rem;
}

.route-table__default,
.route-table__action {
  flex: 0 0 auto;
  border-radius: 4px;
  padding: 0.0625rem 0.375rem;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.6875rem;
  font-weight: 600;
  line-height: 1.5;
}

.route-table__action--redirect {
  background: var(--app-accent-soft);
  color: var(--app-accent);
}

.route-table__technical {
  overflow: hidden;
  margin: 0.125rem 0 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.35;
  text-overflow: ellipsis;
  unicode-bidi: plaintext;
  white-space: nowrap;
}

.route-table__status {
  display: grid;
  grid-template-columns: minmax(2.25rem, auto) 1fr;
  align-items: center;
  gap: 0.625rem;
}

.route-table__priority {
  color: var(--app-text);
  font-size: 0.8125rem;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.route-table__state {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  gap: 0.375rem;
  color: var(--app-success);
  font-size: 0.75rem;
  font-weight: 500;
  white-space: nowrap;
}

.route-table__state-dot {
  width: 0.4375rem;
  height: 0.4375rem;
  flex: 0 0 auto;
  border-radius: 50%;
  background: currentColor;
}

.route-table__state--disabled {
  color: var(--app-text-muted);
}

.route-table__actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 0.375rem;
}

.route-table__actions :deep(.n-button) {
  width: 2.25rem;
  min-width: 2.25rem;
  padding-inline: 0;
}

@media (max-width: 860px) {
  .route-table__header {
    display: none;
  }

  .route-table__row {
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas:
      "identity actions"
      "destination destination"
      "status status";
    gap: 0.5rem 0.75rem;
    min-height: 0;
    padding-block: 0.75rem;
  }

  .route-table__identity {
    grid-area: identity;
  }

  .route-table__destination {
    grid-area: destination;
  }

  .route-table__status {
    grid-area: status;
    display: flex;
  }

  .route-table__priority::before {
    content: "Priority ";
    color: var(--app-text-muted);
    font-family: var(--font-body);
    font-weight: 400;
  }

  .route-table__actions {
    grid-area: actions;
  }
}

@media (max-width: 520px) {
  .route-table__row {
    padding-inline: 1rem;
  }

  .route-table__id {
    display: none;
  }

  .route-table__actions {
    gap: 0.25rem;
  }

  .route-table__actions :deep(.n-button) {
    width: 2rem;
    min-width: 2rem;
  }
}

@media (pointer: coarse) {
  .route-table__actions :deep(.n-button) {
    width: 2.75rem;
    min-width: 2.75rem;
    min-height: 2.75rem;
  }
}

@media (min-width: 640px) {
  .workbench-section-header {
    flex-direction: row;
    align-items: center;
  }

  .workbench-section-header > :deep(.n-button) {
    width: auto;
  }
}
</style>
