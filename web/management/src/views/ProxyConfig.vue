<script setup lang="ts">
import { computed, h, ref, type HTMLAttributes } from "vue";
import { useRoute, useRouter } from "vue-router";
import {
  NAlert,
  NButton,
  NDataTable,
  NDropdown,
  NInput,
  NTabPane,
  NTabs,
  NTag,
} from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { Ban as BanIcon } from "@lucide/vue";
import { MoreHorizontal as MoreIcon } from "@lucide/vue";
import { Pencil as PencilIcon } from "@lucide/vue";
import { Play as PlayIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Search as SearchIcon } from "@lucide/vue";
import { X as TimesIcon } from "@lucide/vue";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import PublicSiteMigrationPanel from "@/components/PublicSiteMigrationPanel.vue";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import MetricCard from "@/components/ui/MetricCard.vue";
import MetricStrip from "@/components/ui/MetricStrip.vue";
import PageHeader from "@/components/ui/PageHeader.vue";
import SectionCard from "@/components/ui/SectionCard.vue";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementContext } from "@/composables/useManagementContext";
import { BUSY_REASON } from "@/lib/disabledReasons";
import {
  diagnosticExcerpt,
  diagnosticInspectionText,
} from "@/lib/diagnosticText";
import {
  displayHostnamePattern,
  displayMigratedSiteName,
} from "@/lib/idnHostnames";
import {
  bindLabel,
  listenerRuntimeState,
  listenerStateLabel,
  protocolLabel,
  proxyStateLabel,
  severityForState,
} from "@/lib/publicProxyLabels";
import {
  publicSiteLifecycleLabel,
  publicSiteListenerIDs,
  publicSiteReadinessLabel,
} from "@/lib/publicSites";
import { naiveTagType } from "@/lib/naiveUi";
import {
  ProxyState,
  PublicSiteHostBehavior,
  type PublicListener,
  type PublicSite,
} from "@/gen/proto/p2pstream/v1/management_pb";

const proxySectionKeys = ["sites", "listeners"] as const;
type ProxySectionKey = (typeof proxySectionKeys)[number];
type ProxySectionSummary = {
  key: ProxySectionKey;
  label: string;
  value: string;
  detail: string;
  description: string;
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
const proxyState = computed(
  () => status.value?.proxy?.state ?? ProxyState.UNSPECIFIED,
);
const proxyIsRunning = computed(
  () =>
    proxyState.value === ProxyState.RUNNING ||
    status.value?.proxyRunning === true,
);
const proxyError = computed(
  () => status.value?.proxy?.lastError || status.value?.proxyLastError || "",
);
const proxySeverity = computed(() => severityForState(proxyState.value));
const proxyTone = computed<"success" | "warning" | "error">(() => {
  const tag = naiveTagType(proxySeverity.value);
  if (tag === "success") return "success";
  if (tag === "warning") return "warning";
  return "error";
});
const listeners = computed(() => config.value?.listeners ?? []);
const routes = computed(() => config.value?.routes ?? []);
const sites = computed(() => config.value?.sites ?? []);
const listenerStatuses = computed(
  () => config.value?.proxy?.listeners ?? status.value?.proxy?.listeners ?? [],
);
const runningListeners = computed(
  () =>
    listeners.value.filter((listener) => listenerStatus(listener)?.running)
      .length,
);
const busyDisabledReason = computed(() => (isBusy.value ? BUSY_REASON : ""));
const listenerFilterText = ref("");
const displayedListeners = computed(() => {
  const query = listenerFilterText.value.trim().toLocaleLowerCase();
  return [...listeners.value]
    .filter((listener) => {
      if (!query) return true;
      return [
        listener.name,
        bindLabel(listener),
        protocolLabel(listener.protocol),
        listenerStateLabel(listener, listenerStatus(listener)),
      ].some((value) => value.toLocaleLowerCase().includes(query));
    })
    .sort((left, right) => {
      const rankDifference =
        listenerAttentionRank(left) - listenerAttentionRank(right);
      if (rankDifference) return rankDifference;
      return left.name.localeCompare(right.name);
    });
});
const listenerTableWidth = 810;
const listenerColumns = computed<DataTableColumns<PublicListener>>(() => [
  {
    title: "Listener",
    key: "listener",
    minWidth: 240,
    render: (listener) =>
      h("div", { class: "layout-grid min-width-zero space-xs" }, [
        h(
          "bdi",
          {
            class: "clip-text weight-medium base-text",
            dir: "ltr",
            title: diagnosticInspectionText(listener.name),
          },
          diagnosticExcerpt(listener.name, 56).text,
        ),
        h(
          "bdi",
          {
            class: "clip-text mono-text copy-xs muted-text",
            dir: "ltr",
            title: diagnosticInspectionText(bindLabel(listener)),
          },
          diagnosticExcerpt(bindLabel(listener), 72).text,
        ),
      ]),
  },
  {
    title: "Protocol & routes",
    key: "traffic",
    width: 150,
    render: (listener) =>
      h("div", { class: "layout-grid space-xs" }, [
        h("span", { class: "base-text" }, protocolLabel(listener.protocol)),
        h(
          "span",
          { class: "copy-xs muted-text" },
          `${listenerRouteCount(listener)} site routes`,
        ),
      ]),
  },
  {
    title: "State",
    key: "state",
    minWidth: 190,
    render: (listener) =>
      h("div", { class: "layout-row layout-column space-2xs" }, [
        h(
          NTag,
          {
            size: "small",
            bordered: false,
            type: naiveTagType(
              listener.enabled
                ? severityForState(
                    listenerRuntimeState(listener, listenerStatus(listener)),
                  )
                : "warn",
            ),
            class: "fit-width",
          },
          {
            default: () =>
              listenerStateLabel(listener, listenerStatus(listener)),
          },
        ),
        listenerStatus(listener)?.lastError
          ? h("details", { class: "listener-error-disclosure" }, [
              h("summary", { class: "copy-xs error-text" }, "View error"),
              h(
                "bdi",
                { class: "mono-text copy-xs error-text", dir: "ltr" },
                diagnosticInspectionText(
                  listenerStatus(listener)?.lastError ?? "",
                ),
              ),
            ])
          : null,
      ]),
  },
  {
    title: "Actions",
    key: "actions",
    width: 230,
    align: "right",
    fixed: "right",
    render: (listener) =>
      h("div", { class: "layout-row align-center align-end-row space-sm" }, [
        h(
          DisabledHint,
          {
            disabled: Boolean(listenerRunningDisabledReason(listener)),
            reason: listenerRunningDisabledReason(listener),
          },
          {
            default: () =>
              h(
                NButton,
                {
                  secondary: true,
                  size: "small",
                  "aria-label": `${listenerStatus(listener)?.running ? "Stop" : "Start"} ${safeListenerName(listener)}`,
                  disabled: Boolean(listenerRunningDisabledReason(listener)),
                  onClick: () =>
                    void setListenerRunning(
                      listener,
                      !listenerStatus(listener)?.running,
                    ),
                },
                {
                  icon: () =>
                    listenerStatus(listener)?.running
                      ? h(TimesIcon, { class: "icon-sm" })
                      : h(PlayIcon, { class: "icon-sm" }),
                  default: () =>
                    listenerStatus(listener)?.running ? "Stop" : "Start",
                },
              ),
          },
        ),
        h(
          DisabledHint,
          {
            disabled: Boolean(busyDisabledReason.value),
            reason: busyDisabledReason.value,
          },
          {
            default: () =>
              h(
                NButton,
                {
                  secondary: true,
                  size: "small",
                  "aria-label": `Edit ${safeListenerName(listener)}`,
                  disabled: Boolean(busyDisabledReason.value),
                  onClick: () => editListener(listener),
                },
                {
                  icon: () => h(PencilIcon, { class: "icon-sm" }),
                  default: () => "Edit",
                },
              ),
          },
        ),
        h(
          NDropdown,
          {
            trigger: "click",
            options: listenerMenuOptions(listener),
            onSelect: (key: string | number) =>
              handleListenerMenuSelect(listener, String(key)),
          },
          {
            default: () =>
              h(
                NButton,
                {
                  secondary: true,
                  size: "small",
                  "aria-label": `More actions for ${safeListenerName(listener)}`,
                  disabled: Boolean(busyDisabledReason.value),
                },
                {
                  icon: () => h(MoreIcon, { class: "icon-sm" }),
                  default: () => "More",
                },
              ),
          },
        ),
      ]),
  },
]);
// Column widths sum to the scroll-x below so the table fits a 1280px viewport.
const siteTableWidth = 920;
const siteColumns = computed<DataTableColumns<PublicSite>>(() => [
  {
    title: "Site and listeners",
    key: "site",
    minWidth: 200,
    render: (site) =>
      h("div", { class: "site-table__identity" }, [
        h("div", { class: "site-table__title-line" }, [
          h(
            "bdi",
            {
              class: "site-table__name",
              dir: "auto",
              title: diagnosticInspectionText(site.name),
            },
            diagnosticExcerpt(displayMigratedSiteName(site.name), 48).text,
          ),
          h(
            "span",
            { class: "mono-text copy-xs muted-text" },
            `#${site.id.toString()}`,
          ),
        ]),
        h(
          "span",
          {
            class: "clip-text copy-xs muted-text",
            title: diagnosticInspectionText(siteListenerSummary(site)),
          },
          diagnosticInspectionText(siteListenerSummary(site)),
        ),
      ]),
  },
  {
    title: "Hostname mode",
    key: "hosts",
    minWidth: 270,
    render: (site) =>
      h("div", { class: "site-table__hosts" }, [
        h("div", { class: "site-table__primary-host" }, [
          h(
            "span",
            { class: "site-table__host-role" },
            site.defaultSite ? "Default" : "Named",
          ),
          h(
            "bdi",
            {
              class: "mono-text copy-xs clip-text",
              dir: "ltr",
              title: site.canonicalHostname || undefined,
            },
            site.defaultSite
              ? "Any unmatched hostname"
              : displayHostnamePattern(site.canonicalHostname) ||
                  "No canonical hostname",
          ),
        ]),
        !site.defaultSite && site.hosts.length
          ? h("div", { class: "site-table__aliases" }, [
              ...site.hosts.slice(0, 3).map((host) =>
                h(
                  "span",
                  {
                    key: host.id.toString(),
                    class: "site-table__alias",
                    title: host.hostnamePattern,
                  },
                  [
                    h(
                      "bdi",
                      { dir: "ltr" },
                      displayHostnamePattern(host.hostnamePattern),
                    ),
                    h(
                      "span",
                      { "aria-hidden": "true" },
                      host.behavior === PublicSiteHostBehavior.REDIRECT
                        ? "redirect"
                        : "serve",
                    ),
                  ],
                ),
              ),
              site.hosts.length > 3
                ? h(
                    "span",
                    { class: "copy-xs muted-text" },
                    `+${site.hosts.length - 3} more`,
                  )
                : null,
            ])
          : !site.defaultSite
            ? h("span", { class: "copy-xs muted-text" }, "No hostnames yet")
            : null,
      ]),
  },
  {
    title: "Readiness and routes",
    key: "readiness",
    width: 170,
    render: (site) =>
      h("div", { class: "site-table__operations" }, [
        h(
          NTag,
          {
            size: "small",
            bordered: false,
            type: site.readiness.some((item) => item.severity === 1)
              ? "error"
              : "success",
          },
          { default: () => publicSiteReadinessLabel(site) },
        ),
        h(
          "span",
          { class: "copy-xs muted-text" },
          `${siteRouteCount(site)} ${siteRouteCount(site) === 1 ? "route" : "routes"}`,
        ),
        siteNeedsTls(site)
          ? h(
              NButton,
              {
                text: true,
                type: "primary",
                size: "tiny",
                onClick: () => editSite(site),
              },
              { default: () => "Configure TLS" },
            )
          : null,
      ]),
  },
  {
    title: "State",
    key: "state",
    width: 120,
    render: (site) =>
      h(
        "span",
        {
          class: [
            "site-state",
            { "site-state--inactive": !site.published || !site.enabled },
          ],
        },
        [
          h("span", { class: "site-state__dot", "aria-hidden": "true" }),
          publicSiteLifecycleLabel(site),
        ],
      ),
  },
  {
    title: "Actions",
    key: "actions",
    width: 160,
    align: "right",
    fixed: "right",
    render: (site) =>
      h("div", { class: "layout-row align-center align-end-row space-sm" }, [
        h(
          NButton,
          {
            secondary: true,
            size: "small",
            "aria-label": `Edit site ${diagnosticExcerpt(site.name, 48).text}`,
            title: "Edit site",
            onClick: () => editSite(site),
          },
          {
            icon: () => h(PencilIcon, { class: "icon-sm" }),
            default: () => "Edit",
          },
        ),
        h(
          NDropdown,
          {
            trigger: "click",
            options: siteMenuOptions(site),
            onSelect: (key: string | number) => {
              if (key === "delete") void deleteSite(site);
            },
          },
          {
            default: () =>
              h(
                NButton,
                {
                  secondary: true,
                  size: "small",
                  "aria-label": `More actions for site ${diagnosticExcerpt(site.name, 48).text}`,
                },
                {
                  icon: () => h(MoreIcon, { class: "icon-sm" }),
                  default: () => "More",
                },
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
    key: "sites",
    label: "Sites",
    value: sites.value.length.toString(),
    detail: `${routes.value.length.toString()} owned routes`,
    description:
      "Persistent workspaces for hostnames, listeners, routes, readiness, and publication.",
  },
  {
    key: "listeners",
    label: "Public listeners",
    value: listeners.value.length.toString(),
    detail: `${runningListeners.value.toString()} running`,
    description: "Incoming endpoints where the proxy accepts connections.",
  },
]);
const activeProxySection = computed<ProxySectionKey>(() =>
  normalizeProxySection(route.params.section),
);
const activeProxyMeta = computed(
  () =>
    proxySections.value.find(
      (section) => section.key === activeProxySection.value,
    ) ?? proxySections.value[0],
);

function listenerStatus(listener: PublicListener) {
  return listenerStatuses.value.find((item) => item.listenerId === listener.id);
}

function listenerRouteCount(listener: PublicListener): number {
  const siteIDs = new Set(
    sites.value
      .filter((site) => publicSiteListenerIDs(site).includes(listener.id))
      .map((site) => site.id.toString()),
  );
  return routes.value.filter(
    (route) => route.siteId > 0n && siteIDs.has(route.siteId.toString()),
  ).length;
}

function listenerRowKey(listener: PublicListener): string {
  return listener.id.toString();
}

function listenerRowProps(listener: PublicListener): Record<string, string> {
  return {
    "data-testid": `listener-row-${listener.id.toString()}`,
  };
}

function safeListenerName(listener: PublicListener): string {
  return diagnosticExcerpt(listener.name, 48).text;
}

function listenerAttentionRank(listener: PublicListener): number {
  const runtime = listenerStatus(listener);
  if (runtime?.lastError) return 0;
  if (listener.enabled && !runtime?.running) return 1;
  if (listener.enabled) return 2;
  return 3;
}

function siteMenuOptions(site: PublicSite) {
  return [
    {
      label: "Delete site",
      key: "delete",
      props: {
        title: siteRouteCount(site) ? "Review site routes before deletion" : undefined,
      },
    },
  ];
}

function listenerMenuOptions(listener: PublicListener) {
  const disabled = Boolean(busyDisabledReason.value);
  return [
    {
      label: listener.enabled ? "Disable listener" : "Enable listener",
      key: "toggle-enabled",
      disabled,
    },
    { label: "Delete listener", key: "delete", disabled },
  ];
}

function handleListenerMenuSelect(listener: PublicListener, key: string) {
  if (key === "toggle-enabled") {
    void setListenerEnabled(listener, !listener.enabled);
    return;
  }
  if (key === "delete") void deleteListener(listener);
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
  return proxySectionKeys.includes(section as ProxySectionKey)
    ? (section as ProxySectionKey)
    : "sites";
}

async function selectProxySection(value: string | number) {
  const section = normalizeProxySection(value);
  if (section === activeProxySection.value) return;
  await router.push(`/proxy/${section}`);
}

function proxyTabProps(
  section: ProxySectionKey,
  label: string,
): HTMLAttributes {
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

function openAddSiteModal() {
  editorHost.value?.openCreateSite();
}

function editSite(site: PublicSite) {
  editorHost.value?.openSite(site.id);
}

function editLegacyRoute(routeId: bigint) {
  editorHost.value?.openRoute(routeId);
}

async function deleteListener(listener: PublicListener) {
  if (
    !(await confirm(
      "Delete listener",
      `Delete "${diagnosticInspectionText(listener.name)}"? It will stop accepting connections and be permanently removed.`,
    ))
  )
    return;
  await run(async () => {
    await managementClient.deletePublicListener({ id: listener.id });
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

function siteRouteCount(site: PublicSite): number {
  return routes.value.filter((route) => route.siteId === site.id).length;
}

function siteRowKey(site: PublicSite): string {
  return site.id.toString();
}

function siteRowProps(site: PublicSite): Record<string, string> {
  return {
    "data-testid": `site-row-${site.id.toString()}`,
  };
}

function siteNeedsTls(site: PublicSite): boolean {
  return site.readiness.some((item) => {
    const action = item.action.toLocaleLowerCase();
    return action.includes("certificate") || action.includes("tls");
  });
}

function siteListenerSummary(site: PublicSite): string {
  const names = publicSiteListenerIDs(site).map(
    (id) =>
      listeners.value.find((listener) => listener.id === id)?.name ??
      `#${id.toString()}`,
  );
  if (!names.length) return "Not attached";
  if (names.length <= 2) return names.join(" · ");
  return `${names.slice(0, 2).join(" · ")} +${names.length - 2}`;
}

async function deleteSite(site: PublicSite) {
  const routeCount = siteRouteCount(site);
  if (routeCount > 0) {
    if (
      await confirm(
        "Site still owns routes",
        `“${diagnosticInspectionText(site.name)}” owns ${routeCount.toString()} ${routeCount === 1 ? "route" : "routes"}. Open its workspace to review or delete those routes first?`,
        "Open site",
      )
    )
      editSite(site);
    return;
  }
  if (
    !(await confirm(
      "Delete site",
      `Delete “${diagnosticInspectionText(site.name)}” and release its ${site.hosts.length.toString()} claimed ${site.hosts.length === 1 ? "hostname" : "hostnames"}? Routes are never reassigned automatically.`,
    ))
  )
    return;
  await run(async () => {
    await managementClient.deletePublicSite({ id: site.id });
  });
}
</script>

<template>
  <div v-if="dashboard" class="stack-xl">
    <PageHeader title="Proxy">
      <template #description>
        <span id="proxy-page-description">{{ activeProxyMeta.description }}</span>
      </template>
      <template #actions>
        <DisabledHint
          v-if="!proxyIsRunning"
          :disabled="Boolean(busyDisabledReason)"
          :reason="busyDisabledReason"
        >
          <NButton
            type="primary"
            size="small"
            :loading="isBusy && !proxyIsRunning"
            :disabled="Boolean(busyDisabledReason)"
            @click="setProxyRunning?.(true)"
          >
            <template #icon><PlayIcon class="icon-sm" /></template>
            Start proxy
          </NButton>
        </DisabledHint>
        <DisabledHint
          v-else
          :disabled="Boolean(busyDisabledReason)"
          :reason="busyDisabledReason"
        >
          <NButton
            type="error"
            secondary
            size="small"
            :loading="isBusy && proxyIsRunning"
            :disabled="Boolean(busyDisabledReason)"
            @click="setProxyRunning?.(false)"
          >
            <template #icon><BanIcon class="icon-sm" /></template>
            Stop proxy
          </NButton>
        </DisabledHint>
      </template>
    </PageHeader>

    <NAlert v-if="proxyError" type="error" :bordered="false" class="proxy-error">
      <bdi dir="ltr">{{ diagnosticInspectionText(proxyError) }}</bdi>
    </NAlert>

    <MetricStrip variant="strip" label="Proxy configuration summary">
      <MetricCard
        label="Proxy"
        :value="proxyStateLabel(proxyState, status?.proxyRunning)"
        :tone="proxyTone"
        dot
        :detail="proxyIsRunning ? 'Accepting traffic' : 'Not running'"
        aria-live="polite"
      />
      <MetricCard
        v-for="section in proxySections"
        :key="section.key"
        :label="section.label"
        :value="section.value"
        :detail="section.detail"
        :class="{ 'proxy-metric--active': section.key === activeProxySection }"
      />
    </MetricStrip>

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
        id="proxy-panel-sites"
        name="sites"
        role="tabpanel"
        aria-labelledby="proxy-tab-sites"
        :tab="`Sites · ${sites.length}`"
        :tab-props="
          proxyTabProps('sites', `${sites.length.toString()} sites configured`)
        "
      >
        <div class="stack-lg">
          <PublicSiteMigrationPanel :config="config" @edit-route="editLegacyRoute" />
          <SectionCard
            title="Sites"
            heading-id="proxy-sites-heading"
            description="Build hostnames, listener behavior, and routes in one workspace, then publish when ready."
            flush
          >
            <template #actions>
              <NButton type="primary" size="small" @click="openAddSiteModal">
                <template #icon><PlusIcon class="icon-sm" /></template>
                Add site
              </NButton>
            </template>
            <div
              v-if="sites.length"
              role="region"
              aria-label="Configured virtual host sites"
            >
              <NDataTable
                class="proxy-table"
                :columns="siteColumns"
                :data="sites"
                :row-key="siteRowKey"
                :row-props="siteRowProps"
                :pagination="false"
                :bordered="false"
               
                :scroll-x="siteTableWidth"
                size="small"
              />
            </div>
            <EmptyState
              v-else
              title="No sites configured"
              description="Create a draft site, add its listener assignments and routes, then publish it when readiness checks pass. Drafts never affect live traffic."
              action-label="Add site"
              @action="openAddSiteModal"
            />
          </SectionCard>
        </div>
      </NTabPane>

      <NTabPane
        id="proxy-panel-listeners"
        name="listeners"
        role="tabpanel"
        aria-labelledby="proxy-tab-listeners"
        :tab="`Public listeners · ${listeners.length}`"
        :tab-props="
          proxyTabProps(
            'listeners',
            `${listeners.length.toString()} public listeners configured`,
          )
        "
      >
        <SectionCard
          title="Public listeners"
          description="Incoming endpoints where the proxy accepts connections."
          flush
        >
          <template #actions>
            <NButton type="primary" size="small" @click="openAddListenerModal">
              <template #icon><PlusIcon class="icon-sm" /></template>
              Add listener
            </NButton>
          </template>
          <div v-if="listeners.length" class="listener-filter">
            <NInput
              v-model:value="listenerFilterText"
              class="listener-filter__input"
              size="small"
              clearable
              :input-props="{ 'aria-label': 'Search public listeners' }"
              placeholder="Search name, bind address, protocol, or state"
            >
              <template #prefix><SearchIcon class="icon-sm" /></template>
            </NInput>
            <span class="listener-filter__count copy-xs muted-text" aria-live="polite">
              {{ displayedListeners.length }} of {{ listeners.length }}
            </span>
          </div>
          <NDataTable
            v-if="displayedListeners.length"
            class="proxy-table"
            :columns="listenerColumns"
            :data="displayedListeners"
            :row-key="listenerRowKey"
            :row-props="listenerRowProps"
            :pagination="false"
            :bordered="false"
           
            :scroll-x="listenerTableWidth"
            size="small"
          />
          <EmptyState
            v-else-if="listeners.length"
            title="No matching listeners"
            description="Clear or adjust the listener search."
          />
          <EmptyState
            v-else
            title="No listeners configured"
            description="Listeners accept public HTTP or HTTPS traffic on published ports."
            action-label="Add listener"
            @action="openAddListenerModal"
          />
        </SectionCard>
      </NTabPane>
    </NTabs>

    <PublicProxyEditorHost ref="editorHost" :config="config" />
  </div>
</template>

<style scoped>
.proxy-error {
  overflow-wrap: anywhere;
}

.proxy-metric--active :deep(.metric-card__label) {
  color: var(--app-text);
  font-weight: 600;
}

.proxy-tabs {
  min-width: 0;
}

.proxy-tabs :deep(.n-tabs-nav) {
  margin-bottom: 1.25rem;
}

.listener-filter {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  border-bottom: 1px solid var(--app-border-subtle);
  padding: 0.75rem 1.25rem;
}

.listener-filter__input {
  max-width: 26rem;
}

.listener-filter__count {
  flex: 0 0 auto;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

/* Cell content is rendered by column functions, so it needs :deep(). */
.proxy-table :deep(.listener-error-disclosure) {
  max-width: 18rem;
}

.proxy-table :deep(.listener-error-disclosure summary) {
  width: fit-content;
  cursor: pointer;
}

.proxy-table :deep(.listener-error-disclosure bdi) {
  display: block;
  max-height: 8rem;
  margin-top: 0.375rem;
  overflow: auto;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.proxy-table :deep(.site-table__identity),
.proxy-table :deep(.site-table__hosts),
.proxy-table :deep(.site-table__operations) {
  display: grid;
  min-width: 0;
  gap: 0.25rem;
}

.proxy-table :deep(.site-table__operations) {
  justify-items: start;
}

.proxy-table :deep(.site-table__title-line),
.proxy-table :deep(.site-table__primary-host),
.proxy-table :deep(.site-table__aliases) {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.5rem;
}

.proxy-table :deep(.site-table__aliases) {
  flex-wrap: wrap;
  gap: 0.375rem;
}

.proxy-table :deep(.site-table__name) {
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.proxy-table :deep(.site-table__host-role) {
  flex: 0 0 auto;
  border-radius: 6px;
  padding: 0.0625rem 0.375rem;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.6875rem;
  font-weight: 500;
}

.proxy-table :deep(.site-table__alias) {
  display: inline-flex;
  min-width: 0;
  max-width: 12rem;
  align-items: center;
  gap: 0.25rem;
  border-radius: 6px;
  padding: 0.0625rem 0.375rem;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.6875rem;
}

.proxy-table :deep(.site-table__alias bdi) {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.proxy-table :deep(.site-table__alias span) {
  flex: 0 0 auto;
  color: var(--app-text);
  font-family: var(--font-body);
  font-size: 0.6875rem;
}

.proxy-table :deep(.site-state) {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  gap: 0.375rem;
  color: var(--app-success);
  font-size: 0.75rem;
  font-weight: 500;
  white-space: nowrap;
}

.proxy-table :deep(.site-state__dot) {
  width: 0.4375rem;
  height: 0.4375rem;
  flex: 0 0 auto;
  border-radius: 50%;
  background: currentColor;
}

.proxy-table :deep(.site-state--inactive) {
  color: var(--app-text-muted);
}

@media (max-width: 639px) {
  .listener-filter {
    padding-inline: 1rem;
  }

  .listener-filter__input {
    max-width: none;
  }
}
</style>
