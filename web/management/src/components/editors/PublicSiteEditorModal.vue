<script setup lang="ts">
import { computed, inject, nextTick, reactive, ref } from "vue";
import {
  AlertTriangle as AlertIcon,
  ArrowDown as MoveDownIcon,
  ArrowUp as MoveUpIcon,
  ArrowUpRight as ArrowIcon,
  CheckCircle2 as CheckIcon,
  ChevronDown as ChevronIcon,
  Copy as CopyIcon,
  Globe2 as GlobeIcon,
  Pencil as PencilIcon,
  Plus as PlusIcon,
  Route as RouteIcon,
  Trash2 as TrashIcon,
} from "@lucide/vue";
import {
  NButton,
  NCheckbox,
  NDrawer,
  NDrawerContent,
  NInput,
  NTag,
} from "naive-ui";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicRouteEditorModal from "@/components/editors/PublicRouteEditorModal.vue";
import PublicListenerEditorModal from "@/components/editors/PublicListenerEditorModal.vue";
import PublicTlsCertificateEditorModal from "@/components/editors/PublicTlsCertificateEditorModal.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import {
  isBusyKey,
  runManagementActionKey,
} from "@/composables/managementContextKeys";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementClient } from "@/composables/useManagementClient";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { editorDrawerWidth, naiveTagType } from "@/lib/naiveUi";
import {
  protocolLabel,
  routeDestinationLabel,
  routeTargetSummary,
} from "@/lib/publicProxyLabels";
import {
  publicSiteLifecycleLabel,
  publicSiteReadinessLabel,
  publicSiteValidationReason,
  type SiteAliasForm,
  type SiteListenerBindingForm,
} from "@/lib/publicSites";
import {
  PublicListenerProtocol,
  PublicRouteAction,
  PublicSiteHostBehavior,
  PublicSiteListenerBehavior,
  PublicSiteReadinessSeverity,
  type GetPublicProxyConfigResponse,
  type PublicRoute,
  type PublicSite,
} from "@/gen/proto/p2pstream/v1/management_pb";

type HostnameForm = SiteAliasForm;
const props = defineProps<{ config: GetPublicProxyConfigResponse | null }>();
const emit = defineEmits<{ (event: "saved"): void }>();
const managementClient = useManagementClient();
const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(
  isBusyKey,
  computed(() => false),
);
const { confirm } = useConfirmDialog();
const isOpen = ref(false);
const routeEditor = ref<InstanceType<typeof PublicRouteEditorModal> | null>(
  null,
);
const listenerEditor = ref<InstanceType<
  typeof PublicListenerEditorModal
> | null>(null);
const tlsEditor = ref<InstanceType<
  typeof PublicTlsCertificateEditorModal
> | null>(null);
const bindingsSection = ref<HTMLElement | null>(null);
const hostnamesSection = ref<HTMLElement | null>(null);
const initialSnapshot = ref("");
const routeSaveNotice = ref("");
const readinessExpanded = ref(false);
let returnFocusElement: HTMLElement | null = null;
let nextRowID = 1;

const form = reactive({
  id: "",
  name: "",
  enabled: true,
  defaultSite: false,
  canonicalHostname: "",
  hostnames: [] as HostnameForm[],
  listenerBindings: [] as SiteListenerBindingForm[],
});
const listeners = computed(() => props.config?.listeners ?? []);
const sites = computed(() => props.config?.sites ?? []);
const routes = computed(() =>
  (props.config?.routes ?? [])
    .filter((route) => route.siteId.toString() === form.id)
    .sort((a, b) =>
      a.isDefault !== b.isDefault
        ? Number(a.isDefault) - Number(b.isDefault)
        : a.priority === b.priority
          ? Number(a.id - b.id)
          : Number(a.priority - b.priority),
    ),
);
const currentSite = computed(() =>
  sites.value.find((site) => site.id.toString() === form.id),
);
const editing = computed(() => Boolean(form.id));
const listenerOptions = computed(() =>
  listeners.value.map((listener) => ({
    label: `${listener.name} · ${protocolLabel(listener.protocol)} :${listener.port.toString()}`,
    value: listener.id.toString(),
  })),
);
const httpsListenerOptions = computed(() =>
  listeners.value
    .filter((listener) => listener.protocol === PublicListenerProtocol.HTTPS)
    .map((listener) => ({
      label: `${listener.name} · HTTPS :${listener.port.toString()}`,
      value: listener.id.toString(),
    })),
);
const behaviorOptions = [
  { label: "Serve Site", value: PublicSiteListenerBehavior.SERVE },
  {
    label: "Redirect to HTTPS",
    value: PublicSiteListenerBehavior.REDIRECT_HTTPS,
  },
];
const hostnameBehaviorOptions = [
  { label: "Serve routes", value: PublicSiteHostBehavior.SERVE },
  {
    label: "Redirect to canonical · 308",
    value: PublicSiteHostBehavior.REDIRECT,
  },
];
const validationReason = computed(() =>
  publicSiteValidationReason({
    name: form.name,
    primaryHostname: "",
    aliases: form.hostnames,
    defaultSite: form.defaultSite,
    canonicalHostname: form.canonicalHostname,
    listenerBindings: form.listenerBindings,
  }),
);
const submitDisabledReason = computed(() =>
  isBusy.value ? BUSY_REASON : validationReason.value,
);
const snapshot = computed(() => JSON.stringify(form));
const dirty = computed(
  () =>
    isOpen.value &&
    Boolean(initialSnapshot.value) &&
    snapshot.value !== initialSnapshot.value,
);
const readinessItems = computed(() => currentSite.value?.readiness ?? []);
const blockerCount = computed(
  () =>
    readinessItems.value.filter(
      (item) => item.severity === PublicSiteReadinessSeverity.BLOCKER,
    ).length,
);
const publishDisabledReason = computed(() => {
  if (!editing.value || currentSite.value?.published) return "";
  if (dirty.value) return "Save Site settings before publishing.";
  if (isBusy.value) return BUSY_REASON;
  return (
    currentSite.value?.readiness.find(
      (item) => item.severity === PublicSiteReadinessSeverity.BLOCKER,
    )?.message ?? ""
  );
});

function freshID(prefix: string) {
  return `${prefix}-${nextRowID++}`;
}
function resetForm() {
  Object.assign(form, {
    id: "",
    name: "",
    enabled: true,
    defaultSite: false,
    canonicalHostname: "",
    hostnames: [],
    listenerBindings: [],
  });
  routeSaveNotice.value = "";
}
function populateForm(site: PublicSite) {
  form.id = site.id.toString();
  form.name = site.name;
  form.enabled = site.enabled;
  form.defaultSite = site.defaultSite;
  form.canonicalHostname = site.canonicalHostname;
  form.hostnames = site.hosts.map((host) => ({
    id: host.id.toString(),
    hostnamePattern: host.hostnamePattern,
    behavior: host.behavior || PublicSiteHostBehavior.SERVE,
  }));
  const bindings = site.listenerBindings.length
    ? site.listenerBindings
    : site.listenerId > 0n
      ? [
          {
            listenerId: site.listenerId,
            behavior: PublicSiteListenerBehavior.SERVE,
            redirectListenerId: 0n,
            redirectHostname: "",
          },
        ]
      : [];
  form.listenerBindings = bindings.map((binding) => ({
    id: freshID("binding"),
    listenerId: binding.listenerId.toString(),
    behavior: binding.behavior || PublicSiteListenerBehavior.SERVE,
    redirectListenerId: binding.redirectListenerId.toString(),
    redirectHostname: binding.redirectHostname,
  }));
  routeSaveNotice.value = "";
}
function rememberOpenState() {
  returnFocusElement =
    document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
  readinessExpanded.value = false;
  isOpen.value = true;
  void nextTick(() => {
    initialSnapshot.value = snapshot.value;
  });
}
function openCreate() {
  resetForm();
  rememberOpenState();
}
function openEdit(siteId: bigint | string) {
  const site = sites.value.find(
    (item) => item.id.toString() === siteId.toString(),
  );
  if (!site) return;
  populateForm(site);
  rememberOpenState();
}
function addHostname() {
  if (form.hostnames.length >= 64) return;
  form.hostnames.push({
    id: freshID("hostname"),
    hostnamePattern: "",
    behavior: PublicSiteHostBehavior.SERVE,
  });
}
function removeHostname(index: number) {
  form.hostnames.splice(index, 1);
}
function addBinding() {
  const assigned = new Set(
    form.listenerBindings.map((item) => item.listenerId),
  );
  const listener = listeners.value.find(
    (item) => !assigned.has(item.id.toString()),
  );
  form.listenerBindings.push({
    id: freshID("binding"),
    listenerId: listener?.id.toString() ?? "",
    behavior: PublicSiteListenerBehavior.SERVE,
    redirectListenerId: "0",
    redirectHostname: "",
  });
}
function createListener() {
  listenerEditor.value?.openCreate();
}
function removeBinding(index: number) {
  form.listenerBindings.splice(index, 1);
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
      "Discard Site changes?",
      "This Site has unsaved settings. Its persisted routes are unaffected.",
      "Discard changes",
    ))
  )
    return;
  forceClose();
}
function handleDrawerVisibility(show: boolean) {
  if (!show) void close();
}
function sitePayload() {
  const canonical = form.defaultSite
    ? ""
    : form.canonicalHostname.trim().toLocaleLowerCase();
  return {
    listenerId: 0n,
    name: form.name.trim(),
    enabled: form.enabled,
    defaultSite: form.defaultSite,
    canonicalHostname: canonical,
    hosts: form.defaultSite
      ? []
      : form.hostnames.map((host) => ({
          hostnamePattern: host.hostnamePattern.trim(),
          primary:
            Boolean(canonical) &&
            host.hostnamePattern.trim().toLocaleLowerCase() === canonical,
          behavior: host.behavior,
        })),
    listenerBindings: form.listenerBindings.map((binding) => ({
      listenerId: BigInt(binding.listenerId || "0"),
      behavior: binding.behavior,
      redirectListenerId:
        binding.behavior === PublicSiteListenerBehavior.REDIRECT_HTTPS
          ? BigInt(binding.redirectListenerId || "0")
          : 0n,
      redirectHostname:
        binding.behavior === PublicSiteListenerBehavior.REDIRECT_HTTPS
          ? binding.redirectHostname.trim()
          : "",
    })),
  };
}
async function submitSite() {
  if (!runManagementAction || submitDisabledReason.value) return;
  let savedSite: PublicSite | undefined;
  const ok = await runManagementAction(async () => {
    const response = form.id
      ? await managementClient.updatePublicSite({
          id: BigInt(form.id),
          ...sitePayload(),
        })
      : await managementClient.createPublicSite(sitePayload());
    savedSite = response.site;
  });
  if (!ok || !savedSite) return;
  populateForm(savedSite);
  initialSnapshot.value = snapshot.value;
  emit("saved");
}
async function publishSite() {
  if (!runManagementAction || !form.id || publishDisabledReason.value) return;
  let site: PublicSite | undefined;
  const ok = await runManagementAction(async () => {
    site = (await managementClient.publishPublicSite({ id: BigInt(form.id) }))
      .site;
  });
  if (!ok || !site) return;
  populateForm(site);
  initialSnapshot.value = snapshot.value;
  emit("saved");
}
function addRoute() {
  if (form.id) routeEditor.value?.openCreateForSite(form.id);
}
function editRoute(route: PublicRoute) {
  routeEditor.value?.openEditForSite(route.id, form.id);
}
function cloneRoute(route: PublicRoute) {
  routeEditor.value?.openCloneForSite(route.id, form.id);
}
async function deleteRoute(route: PublicRoute) {
  if (
    !runManagementAction ||
    !(await confirm(
      "Delete route?",
      `Delete ${route.pathPrefix || "the default path"} and all of its targets?`,
      "Delete route",
    ))
  )
    return;
  const ok = await runManagementAction(async () => {
    await managementClient.deletePublicRoute({ id: route.id });
  });
  if (ok) {
    routeSaveNotice.value = "Route deleted.";
    emit("saved");
  }
}
function routePayload(
  route: PublicRoute,
  changes: Partial<Pick<PublicRoute, "enabled" | "priority">> = {},
) {
  return {
    id: route.id,
    listenerId: 0n,
    siteId: BigInt(form.id),
    priority: changes.priority ?? route.priority,
    hostPattern: "",
    pathPrefix: route.pathPrefix,
    pathSecurityMode: route.pathSecurityMode,
    accessPolicyId: route.accessPolicyId,
    action: route.action,
    targetLoadBalancing: route.targetLoadBalancing,
    isDefault: route.isDefault,
    targets: route.targets,
    redirectTargetMode: route.redirectTargetMode,
    redirectTarget: route.redirectTarget,
    redirectStatusCode: route.redirectStatusCode,
    redirectPreservePathSuffix: route.redirectPreservePathSuffix,
    redirectPreserveQuery: route.redirectPreserveQuery,
    enabled: changes.enabled ?? route.enabled,
  };
}
async function updateRoute(
  route: PublicRoute,
  changes: Partial<Pick<PublicRoute, "enabled" | "priority">>,
  notice: string,
) {
  if (!runManagementAction) return;
  const ok = await runManagementAction(async () => {
    await managementClient.updatePublicRoute(routePayload(route, changes));
  });
  if (ok) {
    routeSaveNotice.value = notice;
    emit("saved");
  }
}
function toggleRoute(route: PublicRoute) {
  void updateRoute(
    route,
    { enabled: !route.enabled },
    `Route ${route.enabled ? "disabled" : "enabled"}.`,
  );
}
function moveRoute(route: PublicRoute, direction: -1 | 1) {
  if (route.isDefault) return;
  const index = routes.value.findIndex((item) => item.id === route.id);
  const adjacent = routes.value[index + direction];
  if (!adjacent || adjacent.isDefault) return;
  const nextPriority = adjacent.priority + BigInt(direction < 0 ? -1 : 1);
  void updateRoute(
    route,
    { priority: nextPriority },
    `Route moved ${direction < 0 ? "earlier" : "later"}.`,
  );
}
function handleRouteSaved() {
  routeSaveNotice.value =
    "Route saved. Your unsaved Site settings are still here.";
  emit("saved");
}
function readinessTagType(severity: PublicSiteReadinessSeverity) {
  if (severity === PublicSiteReadinessSeverity.BLOCKER)
    return naiveTagType("error");
  if (severity === PublicSiteReadinessSeverity.WARNING)
    return naiveTagType("warning");
  if (severity === PublicSiteReadinessSeverity.INFO)
    return naiveTagType("info");
  return "default" as const;
}
function readinessSeverityLabel(severity: PublicSiteReadinessSeverity) {
  if (severity === PublicSiteReadinessSeverity.BLOCKER) return "Blocker";
  if (severity === PublicSiteReadinessSeverity.WARNING) return "Warning";
  if (severity === PublicSiteReadinessSeverity.INFO) return "Info";
  return "Unknown";
}
function readinessActionLabel(action: string, code: string) {
  const value = action.toLocaleLowerCase();
  if (value.includes("hostname") || value.includes("host"))
    return "Edit hostnames";
  if (value.includes("listener") || value.includes("choose_listener"))
    return code === "site.listener.disabled"
      ? "Edit listener"
      : "Fix assignment";
  if (value.includes("route")) return "Edit route";
  if (value.includes("certificate") || value.includes("tls"))
    return "Configure TLS";
  return action || "Review";
}
function readinessContext(listenerId: bigint, routeId: bigint): string {
  const listener = listeners.value.find((item) => item.id === listenerId);
  const route = routes.value.find((item) => item.id === routeId);
  return [
    listener
      ? `${listener.name} · ${protocolLabel(listener.protocol)} :${listener.port.toString()}`
      : listenerId > 0n
        ? `Listener #${listenerId.toString()}`
        : "",
    route
      ? `Route ${route.pathPrefix || "/"}`
      : routeId > 0n
        ? `Route #${routeId.toString()}`
        : "",
  ]
    .filter(Boolean)
    .join(" · ");
}
function focusBinding(listenerId: bigint, code: string) {
  let index = form.listenerBindings.findIndex(
    (binding) => binding.listenerId === listenerId.toString(),
  );
  if (index < 0 && listeners.value.length > form.listenerBindings.length) {
    addBinding();
    index = form.listenerBindings.length - 1;
  }
  void nextTick(() => {
    const row = bindingsSection.value?.querySelector<HTMLElement>(
      `[data-binding-index="${index}"]`,
    );
    row?.scrollIntoView({ behavior: "smooth", block: "center" });
    const controls = row?.querySelectorAll<HTMLElement>(
      '[role="combobox"], input',
    );
    const targetIndex = code.includes("hostname")
      ? controls && controls.length - 1
      : code === "site.redirect.source_http"
        ? 1
        : code.startsWith("site.redirect.")
          ? 2
          : 0;
    controls?.[Math.max(0, targetIndex ?? 0)]?.focus();
  });
}
function handleReadinessAction(
  action: string,
  code: string,
  listenerId: bigint,
  routeId: bigint,
  hostname: string,
) {
  const value = action.toLocaleLowerCase();
  if (value.includes("certificate") || value.includes("tls")) {
    tlsEditor.value?.openFor(
      listenerId,
      hostname ||
        form.canonicalHostname ||
        form.hostnames.find(
          (host) => host.behavior === PublicSiteHostBehavior.SERVE,
        )?.hostnamePattern ||
        "",
    );
    return;
  }
  if (value.includes("hostname") || value.includes("host")) {
    form.defaultSite = false;
    void nextTick(() =>
      hostnamesSection.value?.scrollIntoView({
        behavior: "smooth",
        block: "start",
      }),
    );
    return;
  }
  if (routeId > 0n || value.includes("route")) {
    routeId > 0n
      ? routeEditor.value?.openEditForSite(routeId, form.id)
      : addRoute();
    return;
  }
  if (value.includes("listener") || value.includes("choose_listener")) {
    if (code === "site.listener.disabled" && listenerId > 0n)
      listenerEditor.value?.openEdit(listenerId);
    else if (listeners.value.length === 0) createListener();
    else focusBinding(listenerId, code);
  }
}
defineExpose({ openCreate, openEdit, close });
</script>

<template>
  <NDrawer
    :show="isOpen"
    to="body"
    placement="right"
    :width="editorDrawerWidth('76rem')"
    :aria-label="editing ? 'Site workspace' : 'Create Site workspace'"
    class="editor-drawer site-workspace"
    @update:show="handleDrawerVisibility"
  >
    <NDrawerContent closable>
      <template #header
        ><div class="site-workspace__header min-width-zero">
          <div class="site-workspace__mark" aria-hidden="true">
            <GlobeIcon class="icon-md" />
          </div>
          <div class="min-width-zero">
            <p
              class="copy-xs weight-semibold label-case letter-wide muted-text"
            >
              Site workspace
            </p>
            <h2 class="copy-lg weight-semibold clip-text">
              {{ editing ? form.name || "Untitled Site" : "Create a Site" }}
            </h2>
          </div>
          <NTag
            v-if="currentSite"
            size="small"
            :bordered="false"
            :type="
              currentSite.published
                ? currentSite.enabled
                  ? 'success'
                  : 'warning'
                : 'default'
            "
            >{{ publicSiteLifecycleLabel(currentSite) }}</NTag
          >
        </div></template
      >
      <form
        class="editor-drawer-form site-workspace__form layout-grid space-xl"
        @submit.prevent="submitSite"
      >
        <section class="site-workspace__intro">
          <div>
            <h3 class="copy-base weight-semibold base-text">
              Identity &amp; hostname mode
            </h3>
            <p class="margin-top-xs copy-xs line-normal muted-text">
              Draft Sites are persisted but do not claim hostnames or receive
              traffic until you publish.
            </p>
          </div>
          <div
            v-if="currentSite && !currentSite.published"
            class="layout-grid justify-end space-xs"
          >
            <DisabledHint
              :disabled="Boolean(publishDisabledReason)"
              :reason="publishDisabledReason"
              ><NButton
                type="primary"
                attr-type="button"
                :disabled="Boolean(publishDisabledReason)"
                @click="publishSite"
                ><template #icon><ArrowIcon class="icon-sm" /></template>Publish
                Site</NButton
              ></DisabledHint
            ><span class="copy-xs muted-text align-right-text"
              >Publication revalidates every listener and hostname.</span
            >
          </div>
        </section>
        <section class="layout-grid space-lg mq-sm-cols-two">
          <label
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
            >Site name<NInput
              v-model:value="form.name"
              size="small"
              maxlength="64"
              placeholder="customer-portal"
              required
            /><span class="normal-text letter-normal"
              >An operator label; it is never sent to visitors.</span
            ></label
          >
          <div class="layout-grid space-xs">
            <span
              class="copy-xs weight-medium label-case letter-wide muted-text"
              >Hostname mode</span
            >
            <div
              class="site-mode-switch"
              role="group"
              aria-label="Hostname mode"
            >
              <button
                type="button"
                :class="{ 'is-active': !form.defaultSite }"
                @click="form.defaultSite = false"
              >
                Specific hostnames</button
              ><button
                type="button"
                :class="{ 'is-active': form.defaultSite }"
                @click="form.defaultSite = true"
              >
                Any unmatched hostname
              </button>
            </div>
            <span class="copy-xs line-normal muted-text"
              >A Default Site handles hostnames that no published named Site
              claims on the listener.</span
            >
          </div>
          <NCheckbox v-model:checked="form.enabled">Site enabled</NCheckbox>
        </section>
        <section
          v-if="!form.defaultSite"
          ref="hostnamesSection"
          class="site-workspace__section layout-grid space-lg"
          aria-labelledby="site-hostnames-heading"
        >
          <div class="site-workspace__section-heading">
            <div>
              <h3
                id="site-hostnames-heading"
                class="copy-sm weight-semibold base-text"
              >
                Hostnames
              </h3>
              <p class="margin-top-xs copy-xs line-normal muted-text">
                Exact and one-label wildcard hostnames share this Site’s routes.
              </p>
            </div>
            <NButton
              secondary
              size="small"
              attr-type="button"
              :disabled="form.hostnames.length >= 64"
              @click="addHostname"
              ><template #icon><PlusIcon class="icon-sm" /></template>Add
              hostname</NButton
            >
          </div>
          <div v-if="form.hostnames.length" class="site-host-list">
            <div
              v-for="(host, index) in form.hostnames"
              :key="host.id"
              class="site-host-row"
            >
              <label
                class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                >Hostname {{ index + 1
                }}<NInput
                  v-model:value="host.hostnamePattern"
                  size="small"
                  placeholder="app.example.com or *.example.com"
                  required /></label
              ><label
                class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                >Behavior<AccessibleSelect
                  v-model:value="host.behavior"
                  :accessible-label="`Hostname ${index + 1} behavior`"
                  size="small"
                  :options="hostnameBehaviorOptions" /></label
              ><NButton
                type="error"
                size="small"
                attr-type="button"
                :aria-label="`Remove hostname ${index + 1}`"
                @click="removeHostname(index)"
                ><template #icon><TrashIcon class="icon-sm" /></template
              ></NButton>
            </div>
          </div>
          <button
            v-else
            type="button"
            class="site-empty-action"
            :disabled="form.hostnames.length >= 64"
            @click="addHostname"
          >
            <GlobeIcon class="icon-lg" /><span
              ><strong>Add the first hostname</strong
              ><small
                >Use an exact name or a wildcard such as *.example.com.</small
              ></span
            >
          </button>
          <label
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
            >Canonical hostname
            <span class="normal-text letter-normal">Optional</span
            ><NInput
              v-model:value="form.canonicalHostname"
              size="small"
              placeholder="app.example.com"
            /><span class="normal-text letter-normal"
              >An exact preferred hostname for redirects and links.
              Wildcard-only Sites can leave this empty.</span
            ></label
          >
        </section>
        <section
          ref="bindingsSection"
          class="site-workspace__section layout-grid space-lg"
          aria-labelledby="site-listeners-heading"
        >
          <div class="site-workspace__section-heading">
            <div>
              <h3
                id="site-listeners-heading"
                class="copy-sm weight-semibold base-text"
              >
                Listener assignments
              </h3>
              <p class="margin-top-xs copy-xs line-normal muted-text">
                Attach any number of listeners. An unassigned Site remains
                available but serves no traffic.
              </p>
            </div>
            <NButton
              v-if="listeners.length"
              secondary
              size="small"
              attr-type="button"
              :disabled="form.listenerBindings.length >= listeners.length"
              @click="addBinding"
              ><template #icon><PlusIcon class="icon-sm" /></template>Add
              listener</NButton
            >
            <NButton
              v-else
              secondary
              size="small"
              attr-type="button"
              @click="createListener"
              ><template #icon><PlusIcon class="icon-sm" /></template>Create
              listener</NButton
            >
          </div>
          <div v-if="form.listenerBindings.length" class="site-binding-list">
            <div
              v-for="(binding, index) in form.listenerBindings"
              :key="binding.id"
              class="site-binding-row"
              :data-binding-index="index"
            >
              <label
                class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                >Listener<AccessibleSelect
                  v-model:value="binding.listenerId"
                  :accessible-label="`Listener assignment ${index + 1}`"
                  size="small"
                  :options="listenerOptions"
                  required /></label
              ><label
                class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                >Behavior<AccessibleSelect
                  v-model:value="binding.behavior"
                  :accessible-label="`Listener assignment ${index + 1} behavior`"
                  size="small"
                  :options="behaviorOptions" /></label
              ><template
                v-if="
                  binding.behavior === PublicSiteListenerBehavior.REDIRECT_HTTPS
                "
                ><label
                  class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                  >HTTPS destination<AccessibleSelect
                    v-model:value="binding.redirectListenerId"
                    :accessible-label="`Listener assignment ${index + 1} HTTPS destination`"
                    size="small"
                    :options="httpsListenerOptions"
                    required /></label
                ><label
                  class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                  >Redirect hostname {{ form.defaultSite ? "" : "· optional"
                  }}<NInput
                    v-model:value="binding.redirectHostname"
                    size="small"
                    placeholder="secure.example.com" /></label></template
              ><NButton
                type="error"
                size="small"
                attr-type="button"
                :aria-label="`Remove listener assignment ${index + 1}`"
                @click="removeBinding(index)"
                ><template #icon><TrashIcon class="icon-sm" /></template
              ></NButton>
              <p class="site-binding-row__note copy-xs line-normal muted-text">
                Removing this assignment releases hostname ownership on that
                listener after save; the Site and its routes remain intact.
              </p>
            </div>
          </div>
          <div v-else class="site-empty-note">
            <strong>Not attached</strong
            ><span
              >This Site is retained as inactive configuration and can be
              attached later.</span
            >
          </div>
        </section>
        <section
          v-if="currentSite"
          class="site-readiness"
          aria-labelledby="site-readiness-heading"
        >
          <button
            type="button"
            class="site-readiness__summary"
            :aria-expanded="readinessExpanded"
            aria-controls="site-readiness-items"
            @click="readinessExpanded = !readinessExpanded"
          >
            <span
              class="site-readiness__icon"
              :class="{ 'has-blocker': blockerCount }"
              aria-hidden="true"
              ><AlertIcon v-if="blockerCount" class="icon-md" /><CheckIcon
                v-else
                class="icon-md" /></span
            ><span class="site-readiness__copy"
              ><strong
                id="site-readiness-heading"
                class="copy-sm weight-semibold base-text"
                >{{ publicSiteReadinessLabel(currentSite) }}</strong
              ><span class="margin-top-xs copy-xs line-normal muted-text"
                >Persisted readiness · {{ readinessItems.length }}
                {{ readinessItems.length === 1 ? "check" : "checks" }}</span
              ></span
            ><NTag
              size="small"
              :bordered="false"
              :type="blockerCount ? 'error' : 'success'"
              >{{
                blockerCount ? `${blockerCount} blocking` : "No blockers"
              }}</NTag
            ><ChevronIcon
              class="site-readiness__chevron icon-sm"
              :class="{ 'is-open': readinessExpanded }"
              aria-hidden="true"
            />
          </button>
          <div v-if="readinessExpanded" id="site-readiness-items">
            <div v-if="readinessItems.length" class="site-readiness__items">
              <div
                v-for="item in readinessItems"
                :key="`${item.code}-${item.listenerId}-${item.routeId}-${item.hostname}`"
                class="site-readiness__item"
              >
                <NTag
                  size="small"
                  :bordered="false"
                  :type="readinessTagType(item.severity)"
                  >{{ readinessSeverityLabel(item.severity) }}</NTag
                >
                <div class="site-readiness__copy min-width-zero">
                  <p class="copy-xs line-normal base-text">
                    {{ item.message }}
                  </p>
                  <p
                    v-if="readinessContext(item.listenerId, item.routeId)"
                    class="margin-top-xs copy-xs muted-text"
                  >
                    {{ readinessContext(item.listenerId, item.routeId) }}
                  </p>
                  <p
                    v-if="item.hostname"
                    class="margin-top-xs mono-text copy-xs muted-text"
                  >
                    {{ item.hostname }}
                  </p>
                </div>
                <NButton
                  v-if="item.action"
                  text
                  size="small"
                  attr-type="button"
                  @click="
                    handleReadinessAction(
                      item.action,
                      item.code,
                      item.listenerId,
                      item.routeId,
                      item.hostname,
                    )
                  "
                  >{{ readinessActionLabel(item.action, item.code) }}</NButton
                >
              </div>
            </div>
            <p v-else class="site-readiness__empty copy-xs muted-text">
              No readiness issues were reported for the persisted configuration.
            </p>
          </div>
        </section>
        <section
          v-if="editing"
          class="site-workspace__section site-routes layout-grid space-lg"
          aria-labelledby="site-routes-heading"
        >
          <div class="site-workspace__section-heading">
            <div>
              <h3
                id="site-routes-heading"
                class="copy-sm weight-semibold base-text"
              >
                Routes
              </h3>
              <p class="margin-top-xs copy-xs line-normal muted-text">
                Paths stay inside this Site. A missing path never falls through
                to another Site.
              </p>
            </div>
            <NButton
              type="primary"
              size="small"
              attr-type="button"
              @click="addRoute"
              ><template #icon><PlusIcon class="icon-sm" /></template>Add
              route</NButton
            >
          </div>
          <p v-if="dirty" class="copy-xs line-normal warning-text">
            Routes use this Site’s saved bindings. Your unsaved Site settings
            remain in the workspace when a route is saved.
          </p>
          <p v-if="routeSaveNotice" class="copy-xs success-text" role="status">
            {{ routeSaveNotice }}
          </p>
          <div
            v-if="routes.length"
            class="site-route-list"
            role="list"
            aria-label="Site routes"
          >
            <article
              v-for="(route, index) in routes"
              :key="route.id.toString()"
              class="site-route-row"
              role="listitem"
              :data-testid="`site-route-${route.id.toString()}`"
            >
              <div class="site-route-row__match">
                <div class="layout-row align-center space-sm">
                  <span class="mono-text copy-sm weight-semibold base-text">{{
                    route.pathPrefix || "/"
                  }}</span
                  ><NTag v-if="route.isDefault" size="tiny" :bordered="false"
                    >Default path</NTag
                  >
                </div>
                <span class="copy-xs muted-text"
                  >Priority {{ route.priority.toString() }}</span
                >
              </div>
              <div class="site-route-row__destination">
                <span
                  class="copy-xs weight-semibold"
                  :class="
                    route.action === PublicRouteAction.REDIRECT
                      ? 'warning-text'
                      : 'base-text'
                  "
                  >{{ routeDestinationLabel(route) }}</span
                ><span
                  class="copy-xs mono-text muted-text clip-text"
                  :title="routeTargetSummary(route)"
                  >{{ routeTargetSummary(route) }}</span
                >
              </div>
              <button
                type="button"
                class="site-route-row__state"
                :class="{ 'is-disabled': !route.enabled }"
                :aria-label="`${route.enabled ? 'Disable' : 'Enable'} route ${route.pathPrefix || '/'}`"
                @click="toggleRoute(route)"
              >
                <i aria-hidden="true"></i
                >{{ route.enabled ? "Enabled" : "Disabled" }}
              </button>
              <div class="site-route-row__actions">
                <NButton
                  secondary
                  size="small"
                  attr-type="button"
                  :disabled="route.isDefault || index === 0"
                  aria-label="Move route earlier"
                  @click="moveRoute(route, -1)"
                  ><template #icon
                    ><MoveUpIcon class="icon-sm" /></template></NButton
                ><NButton
                  secondary
                  size="small"
                  attr-type="button"
                  :disabled="
                    route.isDefault ||
                    index === routes.length - 1 ||
                    routes[index + 1]?.isDefault
                  "
                  aria-label="Move route later"
                  @click="moveRoute(route, 1)"
                  ><template #icon
                    ><MoveDownIcon class="icon-sm" /></template></NButton
                ><NButton
                  secondary
                  size="small"
                  attr-type="button"
                  aria-label="Edit route"
                  @click="editRoute(route)"
                  ><template #icon
                    ><PencilIcon class="icon-sm" /></template></NButton
                ><NButton
                  secondary
                  size="small"
                  attr-type="button"
                  aria-label="Clone route"
                  @click="cloneRoute(route)"
                  ><template #icon
                    ><CopyIcon class="icon-sm" /></template></NButton
                ><NButton
                  type="error"
                  size="small"
                  attr-type="button"
                  aria-label="Delete route"
                  @click="deleteRoute(route)"
                  ><template #icon><TrashIcon class="icon-sm" /></template
                ></NButton>
              </div>
            </article>
          </div>
          <button
            v-else
            type="button"
            class="site-empty-action"
            @click="addRoute"
          >
            <RouteIcon class="icon-lg" /><span
              ><strong>Add the first route</strong
              ><small
                >Configure a path, action, and full target pool without leaving
                this Site.</small
              ></span
            >
          </button>
        </section>
        <p v-if="validationReason" role="alert" class="copy-xs warning-text">
          {{ validationReason }}
        </p>
        <div
          class="editor-drawer-actions site-workspace__actions layout-row align-center spread-items space-md"
        >
          <span class="copy-xs muted-text">{{
            editing
              ? dirty
                ? "Unsaved Site settings"
                : "Site settings saved"
              : "Create the draft to start adding routes"
          }}</span>
          <div class="layout-row align-center space-md">
            <NButton secondary attr-type="button" @click="close">Close</NButton
            ><DisabledHint
              :disabled="Boolean(submitDisabledReason)"
              :reason="submitDisabledReason"
              ><NButton
                type="primary"
                attr-type="submit"
                :disabled="Boolean(submitDisabledReason)"
                >{{
                  editing ? "Save Site settings" : "Create draft Site"
                }}</NButton
              ></DisabledHint
            >
          </div>
        </div>
      </form>
    </NDrawerContent>
  </NDrawer>
  <PublicRouteEditorModal
    ref="routeEditor"
    :config="config"
    @saved="handleRouteSaved"
  />
  <PublicListenerEditorModal
    ref="listenerEditor"
    :config="config"
    @saved="emit('saved')"
  />
  <PublicTlsCertificateEditorModal
    ref="tlsEditor"
    :config="config"
    @saved="emit('saved')"
  />
</template>

<style scoped>
.site-workspace__header,
.site-workspace__intro,
.site-workspace__section-heading,
.site-readiness__summary,
.site-readiness__item {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}
.site-workspace__header {
  min-width: 0;
  width: 100%;
  max-width: 100%;
  overflow: hidden;
}
.site-workspace__header > div:nth-child(2) {
  min-width: 0;
  flex: 1 1 0;
  overflow: hidden;
}
.site-workspace__header h2 {
  display: block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.site-workspace__header > :deep(.n-tag) {
  flex: none;
}
.site-workspace__intro > div:first-child,
.site-workspace__section-heading > div:first-child {
  flex: 1;
}
.site-workspace__mark {
  display: grid;
  width: 2.25rem;
  height: 2.25rem;
  flex: none;
  place-items: center;
  border-radius: 0.5rem;
  background: var(--app-accent-soft);
  color: var(--app-accent);
}
:global(.site-workspace .n-drawer-header__main) {
  min-width: 0;
  overflow: hidden;
}
:global(.site-workspace .n-drawer-header__close) {
  flex: none;
}
.site-workspace__intro,
.site-workspace__section-heading {
  justify-content: space-between;
  align-items: flex-start;
}
.site-workspace__section {
  scroll-margin-top: 1rem;
  border: 1px solid var(--app-border-subtle);
  border-radius: 0.625rem;
  padding: 1rem;
  background: var(--app-panel);
}
.site-mode-switch {
  display: grid;
  grid-template-columns: 1fr 1fr;
  padding: 0.1875rem;
  border: 1px solid var(--app-border-subtle);
  border-radius: 0.5rem;
  background: var(--app-panel-muted);
}
.site-mode-switch button {
  border: 0;
  border-radius: 0.375rem;
  padding: 0.5rem 0.75rem;
  background: transparent;
  color: var(--app-text-muted);
  font: inherit;
  font-size: 0.75rem;
  font-weight: 600;
  cursor: pointer;
}
.site-mode-switch button.is-active {
  background: var(--app-panel);
  color: var(--app-text);
  box-shadow: 0 1px 4px rgb(0 0 0 / 12%);
}
.site-host-list,
.site-binding-list,
.site-route-list,
.site-readiness__items {
  overflow: hidden;
  border: 1px solid var(--app-border-subtle);
  border-radius: 0.5rem;
}
.site-host-row {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 0.75rem;
  align-items: end;
  padding: 0.75rem;
}
.site-host-row + .site-host-row,
.site-binding-row + .site-binding-row,
.site-route-row + .site-route-row,
.site-readiness__item + .site-readiness__item {
  border-top: 1px solid var(--app-border-subtle);
}
.site-host-row > :deep(.n-button),
.site-binding-row > :deep(.n-button),
.site-route-row__actions :deep(.n-button) {
  width: 2.125rem;
  min-width: 2.125rem;
  padding-inline: 0;
}
.site-binding-row {
  display: grid;
  grid-template-columns:
    minmax(10rem, 1fr) minmax(10rem, 0.8fr) minmax(10rem, 1fr)
    minmax(10rem, 1fr) auto;
  gap: 0.75rem;
  align-items: end;
  padding: 0.875rem;
}
.site-binding-row__note {
  grid-column: 1/-1;
}
.site-empty-note {
  display: grid;
  gap: 0.25rem;
  border-left: 3px solid var(--app-border);
  padding: 0.75rem 1rem;
  background: var(--app-panel-muted);
  font-size: 0.75rem;
}
.site-empty-note span {
  color: var(--app-text-muted);
}
.site-empty-action {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 0.875rem;
  border: 1px dashed var(--app-border);
  border-radius: 0.5rem;
  padding: 1rem;
  background: var(--app-panel-muted);
  color: var(--app-accent);
  text-align: left;
  cursor: pointer;
}
.site-empty-action:hover {
  border-color: var(--app-accent);
  background: var(--app-accent-soft);
}
.site-empty-action span {
  display: grid;
  gap: 0.25rem;
}
.site-empty-action strong {
  color: var(--app-text);
  font-size: 0.8125rem;
}
.site-empty-action small {
  color: var(--app-text-muted);
  font-size: 0.75rem;
}
.site-readiness {
  overflow: hidden;
  border: 1px solid var(--app-border-subtle);
  border-radius: 0.625rem;
  background: var(--app-panel-muted);
}
.site-readiness__summary {
  width: 100%;
  border: 0;
  padding: 0.875rem 1rem;
  background: transparent;
  color: inherit;
  text-align: left;
  cursor: pointer;
}
.site-readiness__copy {
  display: grid;
  min-width: 0;
  flex: 1;
}
.site-readiness__summary > :deep(.n-tag),
.site-readiness__item > :deep(.n-tag) {
  width: auto;
  flex: none;
  align-self: flex-start;
}
.site-readiness__chevron {
  flex: none;
  transition: transform 0.18s ease;
}
.site-readiness__chevron.is-open {
  transform: rotate(180deg);
}
.site-readiness__icon {
  display: grid;
  width: 2rem;
  height: 2rem;
  flex: none;
  place-items: center;
  border-radius: 50%;
  background: color-mix(in srgb, var(--app-success) 12%, var(--app-panel));
  color: var(--app-success);
}
.site-readiness__icon.has-blocker {
  background: color-mix(in srgb, var(--app-error) 12%, var(--app-panel));
  color: var(--app-error);
}
.site-readiness__item {
  align-items: flex-start;
  padding: 0.75rem 1rem;
  background: var(--app-panel);
}
.site-readiness__empty {
  border-top: 1px solid var(--app-border-subtle);
  padding: 0.75rem 1rem;
}
.site-route-row {
  display: grid;
  grid-template-columns: minmax(10rem, 0.8fr) minmax(14rem, 1.4fr) auto auto;
  gap: 1rem;
  align-items: center;
  padding: 0.75rem;
}
.site-route-row__match,
.site-route-row__destination {
  display: grid;
  min-width: 0;
  gap: 0.25rem;
}
.site-route-row__state {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
  border: 0;
  padding: 0.25rem;
  background: transparent;
  color: var(--app-success);
  font: inherit;
  font-size: 0.75rem;
  font-weight: 600;
  cursor: pointer;
}
.site-route-row__state i {
  width: 0.4375rem;
  height: 0.4375rem;
  border-radius: 50%;
  background: currentColor;
}
.site-route-row__state.is-disabled {
  color: var(--app-text-muted);
}
.site-route-row__actions {
  display: flex;
  gap: 0.375rem;
  justify-content: flex-end;
}
.site-workspace__actions {
  position: sticky;
  z-index: 2;
  bottom: 0;
  margin-inline: -1rem;
  border-top: 1px solid var(--app-border-subtle);
  padding: 0.875rem 1rem;
  background: color-mix(in srgb, var(--app-panel) 94%, transparent);
  backdrop-filter: blur(10px);
}
.site-host-row {
  grid-template-columns: minmax(12rem, 1.4fr) minmax(12rem, 1fr) auto;
}
@media (max-width: 880px) {
  .site-binding-row {
    grid-template-columns: 1fr 1fr;
  }
  .site-binding-row > :deep(.n-button) {
    width: 100%;
  }
  .site-route-row {
    grid-template-columns: minmax(0, 1fr) auto;
  }
  .site-route-row__destination {
    grid-column: 1/-1;
    grid-row: 2;
  }
}
@media (max-width: 640px) {
  .site-workspace__intro,
  .site-workspace__section-heading {
    flex-direction: column;
  }
  .site-workspace__section-heading > :deep(.n-button) {
    width: 100%;
  }
  .site-host-row,
  .site-binding-row,
  .site-route-row {
    grid-template-columns: minmax(0, 1fr);
  }
  .site-binding-row > :deep(.n-button) {
    width: 100%;
  }
  .site-readiness__summary {
    align-items: flex-start;
    flex-wrap: wrap;
  }
  .site-readiness__item {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
  }
  .site-readiness__item > :deep(.n-button) {
    grid-column: 2;
    justify-self: start;
  }
  .site-route-row__destination {
    grid-column: auto;
    grid-row: auto;
  }
  .site-route-row__actions {
    justify-content: stretch;
  }
  .site-route-row__actions :deep(.n-button) {
    flex: 1;
  }
  .site-workspace__actions {
    align-items: stretch;
    flex-direction: column;
  }
  .site-workspace__actions > div {
    display: grid;
    grid-template-columns: 1fr 1fr;
  }
}
</style>
