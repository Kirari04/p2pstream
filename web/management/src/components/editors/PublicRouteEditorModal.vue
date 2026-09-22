<script setup lang="ts">
import { computed, inject, nextTick, reactive, ref, watch } from "vue";
import type { InputHTMLAttributes } from "vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import {
  NButton,
  NCheckbox,
  NDrawer,
  NDrawerContent,
  NInput,
  NInputNumber,
} from "naive-ui";
import {
  isBusyKey,
  runManagementActionKey,
} from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import {
  AGENT_ID_SYSTEM_LABEL_KEY,
  agentMatchesSelector,
  selectorRowsFromLabels,
  selectorRowsToRecord,
  validateSelectorRows,
  type SelectorLabelRow,
} from "@/lib/agentLabels";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { editorDrawerWidth } from "@/lib/naiveUi";
import {
  publicRouteDefaultScopeLabel,
  publicRouteSiteSelectionState,
} from "@/lib/publicSites";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import {
  PublicRouteTargetLoadBalancing,
  PublicResponseBodyMode,
  PublicResponseTemplateKind,
  PublicRouteAction,
  PublicRoutePathSecurityMode,
  PublicRouteRedirectTargetMode,
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  type GetPublicProxyConfigResponse,
  type PublicRoute,
  type PublicRouteTarget,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

type RouteFormMode = "create" | "edit" | "clone";
type UpstreamHeaderForm = {
  id: string;
  targetId: string;
  name: string;
  value: string;
  sensitive: boolean;
  valueSet: boolean;
  position: number;
};
type BasicAuthForm = {
  enabled: boolean;
  username: string;
  password: string;
  passwordSet: boolean;
};
type HealthCheckForm = {
  enabled: boolean;
  method: string;
  path: string;
  intervalMillis: number;
  timeoutMillis: number;
  healthyThreshold: number;
  unhealthyThreshold: number;
  expectedStatusMin: number;
  expectedStatusMax: number;
};
type ResponseHeaderForm = {
  name: string;
  value: string;
};
type TargetForm = {
  id: string;
  name: string;
  enabled: boolean;
  targetType: PublicRouteTargetType;
  url: string;
  transport: PublicRouteTargetTransport;
  selectorLabels: SelectorLabelRow[];
  priorityGroup: number;
  weight: number;
  agentLoadBalancing: PublicRouteTargetLoadBalancing;
  tlsSkipVerify: boolean;
  responseHeaderTimeoutMillis: number;
  upstreamRequestHeaders: UpstreamHeaderForm[];
  upstreamBasicAuth: BasicAuthForm;
  healthCheck: HealthCheckForm;
  staticStatusCode: number;
  staticResponseHeaders: ResponseHeaderForm[];
  staticResponseBody: string;
  staticResponseBodyMode: PublicResponseBodyMode;
  staticResponseTemplateId: string;
};

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(
  isBusyKey,
  computed(() => false),
);

const isOpen = ref(false);
const lockedSiteId = ref("");
const initialSnapshot = ref("");
const cloneSecretWarning = ref(false);
const routeSubmitError = ref("");
let returnFocusElement: HTMLElement | null = null;
const { confirm } = useConfirmDialog();
const routeFormMode = ref<RouteFormMode>("create");
const listeners = computed(() => props.config?.listeners ?? []);
const routes = computed(() => props.config?.routes ?? []);
const sites = computed(() => props.config?.sites ?? []);
const agents = computed(() => props.config?.agents ?? []);
const accessPolicies = computed(() => props.config?.accessPolicies ?? []);
const listenerOptions = computed(() =>
  listeners.value.map((listener) => ({
    label: listener.name,
    value: listener.id.toString(),
  })),
);
const routeActionOptions = [
  { label: "Forward", value: PublicRouteAction.FORWARD },
  { label: "Redirect", value: PublicRouteAction.REDIRECT },
];
const targetLoadBalancingOptions = [
  { label: "Round-robin", value: PublicRouteTargetLoadBalancing.ROUND_ROBIN },
  {
    label: "Weighted round-robin",
    value: PublicRouteTargetLoadBalancing.WEIGHTED_ROUND_ROBIN,
  },
  { label: "Random", value: PublicRouteTargetLoadBalancing.RANDOM },
  {
    label: "Weighted random",
    value: PublicRouteTargetLoadBalancing.WEIGHTED_RANDOM,
  },
  {
    label: "Least active",
    value: PublicRouteTargetLoadBalancing.LEAST_ACTIVE_REQUESTS,
  },
  {
    label: "Weighted least active",
    value: PublicRouteTargetLoadBalancing.WEIGHTED_LEAST_ACTIVE_REQUESTS,
  },
];
const redirectTargetModeOptions = [
  {
    label: "Same host path",
    value: PublicRouteRedirectTargetMode.SAME_HOST_PATH,
  },
  {
    label: "External origin",
    value: PublicRouteRedirectTargetMode.EXTERNAL_ORIGIN_KEEP_PATH,
  },
  { label: "Absolute URL", value: PublicRouteRedirectTargetMode.ABSOLUTE_URL },
];
const pathSecurityModeOptions = [
  { label: "Strict", value: PublicRoutePathSecurityMode.STRICT },
  {
    label: "Allow encoded separators",
    value: PublicRoutePathSecurityMode.ALLOW_ENCODED_SEPARATORS,
  },
];
const accessPolicyOptions = computed(() => [
  { label: "Public · no identity check", value: "0" },
  ...accessPolicies.value.map((policy) => ({
    label: `${policy.name}${policy.enabled ? "" : " · disabled"}`,
    value: policy.id.toString(),
  })),
]);
const targetTransportOptions = [
  { label: "Direct", value: PublicRouteTargetTransport.DIRECT },
  { label: "Agent", value: PublicRouteTargetTransport.AGENT },
];
const targetTypeOptions = [
  { label: "Proxy", value: PublicRouteTargetType.PROXY },
  { label: "Static", value: PublicRouteTargetType.STATIC },
];
const targetSelectorKeyInputProps = {
  "data-testid": "target-selector-key",
} as unknown as InputHTMLAttributes;
const targetSelectorValueInputProps = {
  "data-testid": "target-selector-value",
} as unknown as InputHTMLAttributes;
const exactAgentOptions = computed(() => [
  { label: "Choose agent", value: "" },
  ...agents.value.map((agent) => ({
    label: `${agent.name} (${agent.publicId})`,
    value: agent.publicId,
  })),
]);
const responseTemplateOptions = computed(() => [
  { label: "Choose template", value: "0" },
  ...(props.config?.responseTemplates ?? [])
    .filter(
      (template) => template.kind === PublicResponseTemplateKind.GENERIC_BODY,
    )
    .map((template) => ({
      label: template.name,
      value: template.id.toString(),
    })),
]);
const responseBodyModeOptions = [
  { label: "Inline body", value: PublicResponseBodyMode.INLINE },
  { label: "Response template", value: PublicResponseBodyMode.TEMPLATE },
];

function defaultBasicAuthForm(): BasicAuthForm {
  return { enabled: false, username: "", password: "", passwordSet: false };
}

function defaultHealthCheckForm(): HealthCheckForm {
  return {
    enabled: false,
    method: "GET",
    path: "/",
    intervalMillis: 10000,
    timeoutMillis: 2000,
    healthyThreshold: 2,
    unhealthyThreshold: 2,
    expectedStatusMin: 200,
    expectedStatusMax: 399,
  };
}

const routeForm = reactive({
  id: "",
  listenerId: "",
  siteId: "0",
  action: PublicRouteAction.FORWARD,
  priority: 100,
  hostPattern: "",
  pathPrefix: "",
  pathSecurityMode: PublicRoutePathSecurityMode.STRICT,
  accessPolicyId: "0",
  targetLoadBalancing: PublicRouteTargetLoadBalancing.ROUND_ROBIN,
  isDefault: false,
  targets: [] as TargetForm[],
  redirectTargetMode: PublicRouteRedirectTargetMode.SAME_HOST_PATH,
  redirectTarget: "",
  redirectStatusCode: 302,
  redirectPreservePathSuffix: true,
  redirectPreserveQuery: true,
  enabled: true,
});
let nextSelectorRowID = 1;

const routeScopeOptions = computed(() => [
  { label: "Listener-wide (no Site)", value: "0" },
  ...sites.value
    .filter((site) => site.listenerId.toString() === routeForm.listenerId)
    .map((site) => ({
      label: `${site.name}${site.enabled ? "" : " · disabled"}`,
      value: site.id.toString(),
    })),
  ...(routeForm.siteId !== "0" &&
  !sites.value.some(
    (site) =>
      site.id.toString() === routeForm.siteId &&
      site.listenerId.toString() === routeForm.listenerId,
  )
    ? [
        {
          label: `Unavailable Site #${routeForm.siteId} · choose another scope`,
          value: routeForm.siteId,
          disabled: true,
        },
      ]
    : []),
]);
const selectedSite = computed(() =>
  sites.value.find(
    (site) =>
      site.id.toString() === routeForm.siteId &&
      (Boolean(lockedSiteId.value) ||
        site.listenerId.toString() === routeForm.listenerId),
  ),
);
const selectedSiteListenerNames = computed(() => {
  if (!selectedSite.value) return "Not attached to a listener";
  const ids = selectedSite.value.listenerBindings.length
    ? selectedSite.value.listenerBindings.map((binding) => binding.listenerId)
    : selectedSite.value.listenerId > 0n
      ? [selectedSite.value.listenerId]
      : [];
  const names = ids.map(
    (id) =>
      listeners.value.find((listener) => listener.id === id)?.name ??
      `Listener #${id.toString()}`,
  );
  return names.length ? names.join(" · ") : "Not attached to a listener";
});
const routeSiteSelectionState = computed(() =>
  lockedSiteId.value
    ? "site"
    : publicRouteSiteSelectionState(
        routeForm.siteId,
        routeForm.listenerId,
        sites.value,
      ),
);
const isWorkspaceRoute = computed(() => Boolean(lockedSiteId.value));
const formSnapshot = computed(() => JSON.stringify(routeForm));
const isDirty = computed(
  () =>
    isOpen.value &&
    initialSnapshot.value !== "" &&
    formSnapshot.value !== initialSnapshot.value,
);

const routeIsRedirect = computed(
  () => routeForm.action === PublicRouteAction.REDIRECT,
);
const modalTitle = computed(() =>
  routeFormMode.value === "edit"
    ? "Edit Route"
    : routeFormMode.value === "clone"
      ? "Clone Route"
      : "Add Route",
);
const submitLabel = computed(() =>
  routeFormMode.value === "edit"
    ? "Save Changes"
    : routeFormMode.value === "clone"
      ? "Create Clone"
      : "Create Route",
);
const routeSubmitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!isWorkspaceRoute.value && !listeners.value.length)
    return "Create a listener before creating a route.";
  if (routeSiteSelectionState.value === "unavailable")
    return "Choose a Site on this listener or select Listener-wide (no Site).";
  if (routeIsRedirect.value && routeForm.redirectTarget.trim() === "")
    return "Enter a redirect target.";
  if (!routeIsRedirect.value && !routeForm.targets.length)
    return "Add at least one target.";
  const targetError = routeForm.targets
    .map(targetValidationReason)
    .find(Boolean);
  return targetError || "";
});
const routeSubmitDisabled = computed(() =>
  Boolean(routeSubmitDisabledReason.value),
);

function routeAction(route: PublicRoute): PublicRouteAction {
  return route.action === PublicRouteAction.REDIRECT
    ? PublicRouteAction.REDIRECT
    : PublicRouteAction.FORWARD;
}

function redirectTargetPlaceholder(
  mode: PublicRouteRedirectTargetMode,
): string {
  switch (mode) {
    case PublicRouteRedirectTargetMode.EXTERNAL_ORIGIN_KEEP_PATH:
      return "https://new.example.com";
    case PublicRouteRedirectTargetMode.ABSOLUTE_URL:
      return "https://example.com/new-page";
    default:
      return "/new-path";
  }
}

function defaultTarget(index = routeForm.targets.length): TargetForm {
  return {
    id: "",
    name: `target-${index + 1}`,
    enabled: true,
    targetType: PublicRouteTargetType.PROXY,
    url: "http://127.0.0.1:9000",
    transport: PublicRouteTargetTransport.DIRECT,
    selectorLabels: [newSelectorRow()],
    priorityGroup: 0,
    weight: 100,
    agentLoadBalancing: PublicRouteTargetLoadBalancing.ROUND_ROBIN,
    tlsSkipVerify: false,
    responseHeaderTimeoutMillis: 60000,
    upstreamRequestHeaders: [],
    upstreamBasicAuth: defaultBasicAuthForm(),
    healthCheck: defaultHealthCheckForm(),
    staticStatusCode: 200,
    staticResponseHeaders: [],
    staticResponseBody: "",
    staticResponseBodyMode: PublicResponseBodyMode.INLINE,
    staticResponseTemplateId: "0",
  };
}

function resetForm() {
  cloneSecretWarning.value = false;
  routeForm.id = "";
  routeForm.listenerId = listeners.value[0]?.id.toString() ?? "";
  routeForm.siteId = "0";
  routeForm.action = PublicRouteAction.FORWARD;
  routeForm.priority = 100;
  routeForm.hostPattern = "";
  routeForm.pathPrefix = "";
  routeForm.pathSecurityMode = PublicRoutePathSecurityMode.STRICT;
  routeForm.accessPolicyId = "0";
  routeForm.targetLoadBalancing = PublicRouteTargetLoadBalancing.ROUND_ROBIN;
  routeForm.isDefault = false;
  routeForm.targets = [defaultTarget(0)];
  routeForm.redirectTargetMode = PublicRouteRedirectTargetMode.SAME_HOST_PATH;
  routeForm.redirectTarget = "";
  routeForm.redirectStatusCode = 302;
  routeForm.redirectPreservePathSuffix = true;
  routeForm.redirectPreserveQuery = true;
  routeForm.enabled = true;
}

function upstreamHeadersFromProto(
  target: PublicRouteTarget,
  mode: "edit" | "clone",
): UpstreamHeaderForm[] {
  const preserveSecretReferences = mode === "edit";
  return target.upstreamRequestHeaders
    .filter(
      (header) =>
        preserveSecretReferences || header.valueSet || header.value !== "",
    )
    .map((header, index) => ({
      id: preserveSecretReferences ? header.id.toString() : "",
      targetId: preserveSecretReferences ? header.targetId.toString() : "",
      name: header.name,
      value: header.value,
      sensitive: header.sensitive,
      valueSet: preserveSecretReferences
        ? header.valueSet
        : header.value !== "",
      position: Number(header.position || BigInt(index)),
    }));
}

function basicAuthFromProto(
  target: PublicRouteTarget,
  mode: "edit" | "clone",
): BasicAuthForm {
  const auth = target.upstreamBasicAuth;
  if (!auth?.enabled) return defaultBasicAuthForm();
  if (mode === "clone" && auth.password === "" && auth.passwordSet) {
    return {
      enabled: true,
      username: auth.username,
      password: "",
      passwordSet: false,
    };
  }
  return {
    enabled: auth.enabled,
    username: auth.username,
    password: auth.password,
    passwordSet: auth.passwordSet || auth.password !== "",
  };
}

function healthCheckFromProto(target: PublicRouteTarget): HealthCheckForm {
  const check = target.healthCheck;
  if (!check) return defaultHealthCheckForm();
  return {
    enabled: check.enabled,
    method: check.method || "GET",
    path: check.path || "/",
    intervalMillis: Number(check.intervalMillis || 10000n),
    timeoutMillis: Number(check.timeoutMillis || 2000n),
    healthyThreshold: Number(check.healthyThreshold || 2n),
    unhealthyThreshold: Number(check.unhealthyThreshold || 2n),
    expectedStatusMin: Number(check.expectedStatusMin || 200n),
    expectedStatusMax: Number(check.expectedStatusMax || 399n),
  };
}

function targetFormFromProto(
  target: PublicRouteTarget,
  mode: "edit" | "clone",
): TargetForm {
  const labels = target.agentSelector?.matchLabels ?? {};
  const selectorLabels = selectorRowsFromLabels(labels);
  return {
    id: mode === "clone" ? "" : target.id.toString(),
    name: target.name,
    enabled: target.enabled,
    targetType: target.targetType || PublicRouteTargetType.PROXY,
    url: target.url,
    transport: target.transport || PublicRouteTargetTransport.DIRECT,
    selectorLabels: selectorLabels.length
      ? selectorLabels.map(cloneSelectorRow)
      : [newSelectorRow()],
    priorityGroup: Number(target.priorityGroup || 0n),
    weight: Number(target.weight || 100n),
    agentLoadBalancing:
      target.agentLoadBalancing || PublicRouteTargetLoadBalancing.ROUND_ROBIN,
    tlsSkipVerify: target.tlsSkipVerify,
    responseHeaderTimeoutMillis: Number(
      target.upstreamResponseHeaderTimeoutMillis || 60000n,
    ),
    upstreamRequestHeaders: upstreamHeadersFromProto(target, mode),
    upstreamBasicAuth: basicAuthFromProto(target, mode),
    healthCheck: healthCheckFromProto(target),
    staticStatusCode: Number(target.staticStatusCode || 200n),
    staticResponseHeaders: target.staticResponseHeaders.map((header) => ({
      name: header.name,
      value: header.value,
    })),
    staticResponseBody: target.staticResponseBody,
    staticResponseBodyMode:
      target.staticResponseBodyMode || PublicResponseBodyMode.INLINE,
    staticResponseTemplateId: (
      target.staticResponseTemplateId || 0n
    ).toString(),
  };
}

function populateRouteForm(route: PublicRoute, mode: "edit" | "clone") {
  resetForm();
  routeFormMode.value = mode;
  const action = routeAction(route);
  routeForm.id = mode === "clone" ? "" : route.id.toString();
  routeForm.listenerId = route.listenerId.toString();
  routeForm.siteId = route.siteId.toString();
  routeForm.action = action;
  routeForm.priority = Number(route.priority);
  routeForm.hostPattern = route.hostPattern;
  routeForm.pathPrefix = route.pathPrefix;
  routeForm.pathSecurityMode =
    route.pathSecurityMode || PublicRoutePathSecurityMode.STRICT;
  routeForm.accessPolicyId = route.accessPolicyId.toString();
  routeForm.targetLoadBalancing =
    route.targetLoadBalancing || PublicRouteTargetLoadBalancing.ROUND_ROBIN;
  routeForm.isDefault = mode === "clone" ? false : route.isDefault;
  cloneSecretWarning.value =
    mode === "clone" &&
    route.targets.some(
      (target) =>
        target.upstreamRequestHeaders.some(
          (header) =>
            header.sensitive && header.value === "" && header.valueSet,
        ) ||
        Boolean(
          target.upstreamBasicAuth?.enabled &&
            target.upstreamBasicAuth.password === "" &&
            target.upstreamBasicAuth.passwordSet,
        ),
    );
  routeForm.targets =
    action === PublicRouteAction.REDIRECT
      ? []
      : route.targets.map((target) => targetFormFromProto(target, mode));
  if (action !== PublicRouteAction.REDIRECT && !routeForm.targets.length)
    routeForm.targets = [defaultTarget(0)];
  routeForm.redirectTargetMode =
    route.redirectTargetMode || PublicRouteRedirectTargetMode.SAME_HOST_PATH;
  routeForm.redirectTarget = route.redirectTarget;
  routeForm.redirectStatusCode = Number(route.redirectStatusCode || 302n);
  routeForm.redirectPreservePathSuffix = route.redirectPreservePathSuffix;
  routeForm.redirectPreserveQuery = route.redirectPreserveQuery;
  routeForm.enabled = route.enabled;
  openDrawer();
}

function openCreate() {
  lockedSiteId.value = "";
  resetForm();
  routeFormMode.value = "create";
  openDrawer();
}

function openCreateForSite(siteId: bigint | string) {
  lockedSiteId.value = siteId.toString();
  resetForm();
  routeForm.listenerId = "0";
  routeForm.siteId = lockedSiteId.value;
  routeFormMode.value = "create";
  openDrawer();
}

function openEdit(routeId: bigint | string) {
  lockedSiteId.value = "";
  const id = routeId.toString();
  const route = routes.value.find((item) => item.id.toString() === id);
  if (!route) return;
  populateRouteForm(route, "edit");
}

function openEditForSite(routeId: bigint | string, siteId: bigint | string) {
  const route = routes.value.find(
    (item) =>
      item.id.toString() === routeId.toString() &&
      item.siteId.toString() === siteId.toString(),
  );
  if (!route) return;
  lockedSiteId.value = siteId.toString();
  populateRouteForm(route, "edit");
}

function openClone(routeId: bigint | string) {
  lockedSiteId.value = "";
  const id = routeId.toString();
  const route = routes.value.find((item) => item.id.toString() === id);
  if (!route) return;
  populateRouteForm(route, "clone");
}

function openCloneForSite(routeId: bigint | string, siteId: bigint | string) {
  const route = routes.value.find(
    (item) =>
      item.id.toString() === routeId.toString() &&
      item.siteId.toString() === siteId.toString(),
  );
  if (!route) return;
  lockedSiteId.value = siteId.toString();
  populateRouteForm(route, "clone");
}

function addTarget() {
  routeForm.targets.push(defaultTarget(routeForm.targets.length));
}

function addUpstreamHeader(target: TargetForm) {
  target.upstreamRequestHeaders.push({
    id: "",
    targetId: "",
    name: "",
    value: "",
    sensitive: false,
    valueSet: false,
    position: target.upstreamRequestHeaders.length,
  });
}

function removeUpstreamHeader(target: TargetForm, index: number) {
  target.upstreamRequestHeaders.splice(index, 1);
}

function addResponseHeader(target: TargetForm) {
  target.staticResponseHeaders.push({ name: "", value: "" });
}

function removeResponseHeader(target: TargetForm, index: number) {
  target.staticResponseHeaders.splice(index, 1);
}

function removeTarget(index: number) {
  routeForm.targets.splice(index, 1);
}

function openDrawer() {
  routeSubmitError.value = "";
  returnFocusElement =
    document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
  isOpen.value = true;
  nextTick(() => {
    initialSnapshot.value = formSnapshot.value;
  });
}

function forceClose() {
  isOpen.value = false;
  initialSnapshot.value = "";
  const focusTarget = returnFocusElement;
  returnFocusElement = null;
  void nextTick(() => focusTarget?.focus());
}

async function close() {
  if (
    isDirty.value &&
    !(await confirm(
      "Discard route changes?",
      "This route has unsaved changes. Discard them and return to the Site workspace?",
      "Discard changes",
    ))
  )
    return;
  forceClose();
}

function handleDrawerVisibility(show: boolean) {
  if (!show) void close();
}

async function run(action: () => Promise<void>): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action);
}

function selectorPayload(target: TargetForm): {
  matchLabels: Record<string, string>;
} {
  if (
    target.targetType !== PublicRouteTargetType.PROXY ||
    target.transport !== PublicRouteTargetTransport.AGENT
  ) {
    return { matchLabels: {} };
  }
  return { matchLabels: selectorRowsToRecord(target.selectorLabels) };
}

function upstreamHeaderPayload(header: UpstreamHeaderForm, index: number) {
  return {
    id: BigInt(header.id || "0"),
    targetId: BigInt(header.targetId || "0"),
    name: header.name,
    value: header.value,
    sensitive: header.sensitive,
    valueSet: header.valueSet || header.value !== "",
    position: BigInt(index),
  };
}

function basicAuthPayload(auth: BasicAuthForm) {
  return {
    enabled: auth.enabled,
    username: auth.username,
    password: auth.password,
    passwordSet: auth.passwordSet || auth.password !== "",
  };
}

function healthCheckPayload(check: HealthCheckForm) {
  return {
    enabled: check.enabled,
    method: check.method || "GET",
    path: check.path || "/",
    intervalMillis: BigInt(Math.max(1, check.intervalMillis || 10000)),
    timeoutMillis: BigInt(Math.max(1, check.timeoutMillis || 2000)),
    healthyThreshold: BigInt(Math.max(1, check.healthyThreshold || 2)),
    unhealthyThreshold: BigInt(Math.max(1, check.unhealthyThreshold || 2)),
    expectedStatusMin: BigInt(Math.max(100, check.expectedStatusMin || 200)),
    expectedStatusMax: BigInt(Math.max(100, check.expectedStatusMax || 399)),
  };
}

function targetPayload(target: TargetForm, index: number) {
  const isStatic = target.targetType === PublicRouteTargetType.STATIC;
  return {
    id: BigInt(target.id || "0"),
    name: target.name.trim() || `target-${index + 1}`,
    position: BigInt(index),
    priorityGroup: BigInt(Math.max(0, target.priorityGroup || 0)),
    weight: BigInt(Math.max(1, target.weight || 1)),
    enabled: target.enabled,
    targetType: target.targetType,
    url: isStatic ? "" : target.url.trim(),
    transport: isStatic ? PublicRouteTargetTransport.DIRECT : target.transport,
    agentSelector: selectorPayload(target),
    agentLoadBalancing: isStatic
      ? PublicRouteTargetLoadBalancing.ROUND_ROBIN
      : target.agentLoadBalancing,
    tlsSkipVerify: !isStatic && target.tlsSkipVerify,
    upstreamResponseHeaderTimeoutMillis: BigInt(
      Math.max(1, target.responseHeaderTimeoutMillis || 60000),
    ),
    upstreamRequestHeaders: isStatic
      ? []
      : target.upstreamRequestHeaders.map(upstreamHeaderPayload),
    upstreamBasicAuth: isStatic
      ? defaultBasicAuthForm()
      : basicAuthPayload(target.upstreamBasicAuth),
    healthCheck: isStatic
      ? healthCheckPayload(defaultHealthCheckForm())
      : healthCheckPayload(target.healthCheck),
    staticStatusCode: BigInt(isStatic ? target.staticStatusCode || 200 : 200),
    staticResponseHeaders: isStatic
      ? target.staticResponseHeaders.map((header) => ({
          name: header.name,
          value: header.value,
        }))
      : [],
    staticResponseBody: isStatic ? target.staticResponseBody : "",
    staticResponseBodyMode: isStatic
      ? target.staticResponseBodyMode
      : PublicResponseBodyMode.INLINE,
    staticResponseTemplateId: BigInt(
      isStatic ? target.staticResponseTemplateId || "0" : "0",
    ),
  };
}

function targetValidationReason(target: TargetForm): string {
  if (!target.name.trim()) return "Every target needs a name.";
  if (target.targetType === PublicRouteTargetType.PROXY && !target.url.trim())
    return "Proxy targets need an origin URL.";
  if (
    target.targetType === PublicRouteTargetType.PROXY &&
    !isProxyTargetOrigin(target.url)
  ) {
    return "Proxy target must be an HTTP(S) origin only, such as https://upstream:8443, without credentials, a path, query, or fragment.";
  }
  if (
    target.targetType === PublicRouteTargetType.PROXY &&
    target.transport === PublicRouteTargetTransport.AGENT
  ) {
    const selectorError = validateSelectorRows(target.selectorLabels);
    if (selectorError) return selectorError;
  }
  if (target.weight < 1) return "Target weight must be at least 1.";
  if (target.responseHeaderTimeoutMillis < 1)
    return "Response-header timeout must be positive.";
  if (
    target.targetType === PublicRouteTargetType.PROXY &&
    target.upstreamBasicAuth.enabled &&
    !target.upstreamBasicAuth.password &&
    !target.upstreamBasicAuth.passwordSet
  )
    return "Enter the upstream basic-auth password.";
  if (
    target.targetType === PublicRouteTargetType.PROXY &&
    target.upstreamRequestHeaders.some((header) => !header.name.trim())
  )
    return "Every upstream request header needs a name.";
  if (
    target.targetType === PublicRouteTargetType.PROXY &&
    target.upstreamRequestHeaders.some(
      (header) => header.sensitive && !header.value && !header.valueSet,
    )
  )
    return "Re-enter each sensitive header value before saving this clone.";
  if (
    target.targetType === PublicRouteTargetType.STATIC &&
    target.staticResponseHeaders.some((header) => !header.name.trim())
  )
    return "Every response header needs a name.";
  if (
    target.targetType === PublicRouteTargetType.STATIC &&
    target.staticResponseBodyMode === PublicResponseBodyMode.TEMPLATE &&
    target.staticResponseTemplateId === "0"
  )
    return "Choose a response template.";
  return "";
}

function isProxyTargetOrigin(value: string): boolean {
  try {
    const parsed = new URL(value.trim());
    return (
      (parsed.protocol === "http:" || parsed.protocol === "https:") &&
      parsed.username === "" &&
      parsed.password === "" &&
      (parsed.pathname === "" || parsed.pathname === "/") &&
      parsed.search === "" &&
      parsed.hash === ""
    );
  } catch {
    return false;
  }
}

function newSelectorRow(key = "", value = ""): SelectorLabelRow {
  return {
    id: `selector:${nextSelectorRowID++}`,
    key,
    value,
  };
}

function cloneSelectorRow(row: SelectorLabelRow): SelectorLabelRow {
  return newSelectorRow(row.key, row.value);
}

function addSelectorLabel(target: TargetForm) {
  target.selectorLabels.push(newSelectorRow());
}

function removeSelectorLabel(target: TargetForm, index: number) {
  target.selectorLabels.splice(index, 1);
  if (!target.selectorLabels.length) {
    target.selectorLabels.push(newSelectorRow());
  }
}

function matchingAgents(target: TargetForm) {
  const validationError = validateSelectorRows(target.selectorLabels);
  if (validationError) return [];
  const selector = selectorRowsToRecord(target.selectorLabels);
  return agents.value.filter(
    (agent) => agent.enabled && agentMatchesSelector(agent, selector),
  );
}

function connectedMatchingAgents(target: TargetForm) {
  return matchingAgents(target).filter((agent) => agent.connected);
}

function exactSelectorValue(target: TargetForm): string {
  return (
    selectorRowsToRecord(target.selectorLabels)[AGENT_ID_SYSTEM_LABEL_KEY] ?? ""
  );
}

function setExactAgent(target: TargetForm, publicID: string) {
  if (!publicID) return;
  target.selectorLabels = [
    newSelectorRow(AGENT_ID_SYSTEM_LABEL_KEY, publicID),
    ...target.selectorLabels.filter((row) => {
      const key = row.key.trim();
      return key && key !== AGENT_ID_SYSTEM_LABEL_KEY;
    }),
  ];
}

function agentDisplayName(agentID: bigint | string): string {
  const agent = agents.value.find(
    (item) => item.id.toString() === agentID.toString(),
  );
  return agent ? `${agent.name} (${agent.publicId})` : agentID.toString();
}

async function submitRoute() {
  routeSubmitError.value = "";
  const ok = await run(async () => {
    const isRedirect = routeForm.action === PublicRouteAction.REDIRECT;
    const payload = {
      listenerId: isWorkspaceRoute.value
        ? 0n
        : BigInt(routeForm.listenerId || "0"),
      siteId: BigInt(routeForm.siteId || "0"),
      priority: BigInt(routeForm.priority),
      hostPattern: routeForm.siteId === "0" ? routeForm.hostPattern : "",
      pathPrefix: routeForm.pathPrefix,
      pathSecurityMode: routeForm.pathSecurityMode,
      accessPolicyId: BigInt(routeForm.accessPolicyId || "0"),
      action: routeForm.action,
      targetLoadBalancing: isRedirect
        ? PublicRouteTargetLoadBalancing.ROUND_ROBIN
        : routeForm.targetLoadBalancing,
      isDefault: routeForm.isDefault,
      targets: isRedirect ? [] : routeForm.targets.map(targetPayload),
      redirectTargetMode: isRedirect
        ? routeForm.redirectTargetMode
        : PublicRouteRedirectTargetMode.UNSPECIFIED,
      redirectTarget: isRedirect ? routeForm.redirectTarget : "",
      redirectStatusCode: BigInt(
        isRedirect ? routeForm.redirectStatusCode || 302 : 302,
      ),
      redirectPreservePathSuffix: isRedirect
        ? routeForm.redirectPreservePathSuffix
        : true,
      redirectPreserveQuery: isRedirect
        ? routeForm.redirectPreserveQuery
        : true,
      enabled: routeForm.enabled,
    };
    try {
      if (routeForm.id) {
        await managementClient.updatePublicRoute({
          id: BigInt(routeForm.id),
          ...payload,
        });
      } else {
        await managementClient.createPublicRoute(payload);
      }
    } catch (error) {
      routeSubmitError.value =
        error instanceof Error
          ? error.message
          : "The route could not be saved.";
      throw error;
    }
  });
  if (ok) {
    forceClose();
    emit("saved");
  }
}

watch(
  listeners,
  () => {
    if (!lockedSiteId.value && !routeForm.listenerId && listeners.value[0]) {
      routeForm.listenerId = listeners.value[0].id.toString();
    }
  },
  { immediate: true },
);

watch(
  () => routeForm.action,
  (action) => {
    if (action === PublicRouteAction.FORWARD && !routeForm.targets.length) {
      routeForm.targets = [defaultTarget(0)];
    }
  },
);

defineExpose({
  openCreate,
  openCreateForSite,
  openEdit,
  openEditForSite,
  openClone,
  openCloneForSite,
  close,
});
</script>

<template>
  <NDrawer
    :show="isOpen"
    to="body"
    @update:show="handleDrawerVisibility"
    placement="right"
    :width="editorDrawerWidth('72rem')"
    :aria-label="modalTitle"
    class="editor-drawer"
  >
    <NDrawerContent :title="modalTitle" closable>
      <form
        class="editor-drawer-form layout-grid space-xl"
        @submit.prevent="submitRoute"
      >
        <section
          v-if="isWorkspaceRoute && selectedSite"
          class="route-site-context round-md framed frame-standard pad-md"
          role="note"
        >
          <p class="copy-xs weight-semibold label-case letter-wide base-text">
            Site route
          </p>
          <p class="margin-top-xs copy-sm base-text">{{ selectedSite.name }}</p>
          <p class="margin-top-xs copy-xs base-text">
            {{ selectedSiteListenerNames }}
          </p>
          <p class="margin-top-xs copy-xs line-normal muted-text">
            This route is locked to its Site. Saving it updates the persisted
            route only; unsaved Site settings remain unchanged.
          </p>
        </section>
        <p
          v-if="cloneSecretWarning"
          class="round-md framed warning-border warning-surface pad-md copy-xs line-normal warning-text"
          role="alert"
        >
          Secret values cannot be copied. Re-enter every marked sensitive header
          and upstream basic-auth password before creating this clone. The clone
          is also created as a non-default route.
        </p>
        <section class="layout-grid space-lg mq-sm-cols-four">
          <label
            v-if="!isWorkspaceRoute"
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >
            Listener
            <AccessibleSelect
              v-model:value="routeForm.listenerId"
              accessible-label="Route listener"
              size="small"
              :options="listenerOptions"
              required
            />
          </label>
          <label
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >
            Action
            <AccessibleSelect
              v-model:value="routeForm.action"
              accessible-label="Route action"
              size="small"
              :options="routeActionOptions"
            />
          </label>
          <label
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >
            Priority
            <NInputNumber
              :show-button="false"
              v-model:value="routeForm.priority"
              size="small"
              required
            />
          </label>
          <NCheckbox v-model:checked="routeForm.enabled" class="self-align-end">
            Enabled
          </NCheckbox>
          <label
            v-if="!isWorkspaceRoute"
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text mq-sm-span-two"
          >
            Routing scope
            <AccessibleSelect
              v-model:value="routeForm.siteId"
              accessible-label="Route Site or listener-wide scope"
              size="small"
              :options="routeScopeOptions"
            />
            <span v-if="selectedSite" class="normal-text letter-normal"
              >Hostnames come from Site “{{ selectedSite.name }}”. Requests for
              claimed hosts stay within that Site.</span
            >
            <span
              v-else-if="routeSiteSelectionState === 'unavailable'"
              class="normal-text letter-normal warning-text"
              >The saved Site is unavailable on this listener. Choose another
              Site or Listener-wide (no Site); nothing is detached
              automatically.</span
            >
            <span v-else class="normal-text letter-normal"
              >Listener-wide routes are retained for configurations that have
              not been moved into a Site.</span
            >
          </label>
          <label
            v-if="!isWorkspaceRoute && routeSiteSelectionState === 'legacy'"
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >
            Listener-wide host pattern
            <NInput
              v-model:value="routeForm.hostPattern"
              size="small"
              placeholder="*.example.com"
            />
          </label>
          <div
            v-else-if="!isWorkspaceRoute && selectedSite"
            class="layout-grid space-xs min-width-zero"
          >
            <span
              class="copy-xs weight-medium label-case letter-wide muted-text"
              >Site hostnames</span
            >
            <span
              class="mono-text copy-xs base-text clip-text"
              :title="
                selectedSite.hosts
                  .map((host) => host.hostnamePattern)
                  .join(' · ')
              "
            >
              {{
                selectedSite.hosts
                  .map((host) => host.hostnamePattern)
                  .join(" · ")
              }}
            </span>
          </div>
          <label
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >
            Path prefix
            <NInput
              v-model:value="routeForm.pathPrefix"
              size="small"
              placeholder="/"
            />
          </label>
          <label
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >
            Path security
            <AccessibleSelect
              v-model:value="routeForm.pathSecurityMode"
              accessible-label="Path security mode"
              size="small"
              :options="pathSecurityModeOptions"
            />
            <span class="normal-text letter-normal"
              >Compatibility mode is for upstreams that require encoded / or \
              path identifiers.</span
            >
          </label>
          <label
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >
            Access policy
            <AccessibleSelect
              v-model:value="routeForm.accessPolicyId"
              accessible-label="Route access policy"
              size="small"
              :options="accessPolicyOptions"
            />
            <span class="normal-text letter-normal"
              >Protected routes fail closed and bypass shared response
              caching.</span
            >
          </label>
          <label
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >
            Target balancing
            <AccessibleSelect
              v-model:value="routeForm.targetLoadBalancing"
              accessible-label="Target balancing"
              size="small"
              :options="targetLoadBalancingOptions"
              :disabled="routeIsRedirect"
            />
          </label>
          <div class="layout-grid space-xs self-align-end">
            <NCheckbox v-model:checked="routeForm.isDefault"
              >Default route</NCheckbox
            >
            <span class="copy-xs muted-text">{{
              publicRouteDefaultScopeLabel(routeSiteSelectionState)
            }}</span>
          </div>
        </section>

        <section
          v-if="routeIsRedirect"
          class="layout-grid space-lg round-md framed frame-standard muted-bg pad-lg mq-sm-cols-four"
        >
          <label
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >
            Mode
            <AccessibleSelect
              v-model:value="routeForm.redirectTargetMode"
              accessible-label="Redirect target mode"
              size="small"
              :options="redirectTargetModeOptions"
            />
          </label>
          <label
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text mq-sm-span-two"
          >
            Target
            <NInput
              v-model:value="routeForm.redirectTarget"
              size="small"
              :placeholder="
                redirectTargetPlaceholder(routeForm.redirectTargetMode)
              "
              required
            />
          </label>
          <label
            class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
          >
            Status
            <NInputNumber
              :show-button="false"
              v-model:value="routeForm.redirectStatusCode"
              size="small"
              :min="300"
              :max="399"
            />
          </label>
          <NCheckbox v-model:checked="routeForm.redirectPreservePathSuffix">
            Preserve path suffix
          </NCheckbox>
          <NCheckbox v-model:checked="routeForm.redirectPreserveQuery">
            Preserve query
          </NCheckbox>
        </section>

        <section v-else class="layout-grid space-lg">
          <div class="layout-row align-center spread-items space-md">
            <h4 class="copy-sm weight-semibold base-text">Targets</h4>
            <NButton
              secondary
              size="small"
              attr-type="button"
              @click="addTarget"
            >
              <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
              Add Target
            </NButton>
          </div>

          <div
            v-for="(target, index) in routeForm.targets"
            :key="`${target.id || 'new'}-${index}`"
            data-testid="route-target-row"
            class="layout-grid space-lg round-md framed frame-standard muted-bg pad-lg"
          >
            <div class="layout-row align-start spread-items space-md">
              <div class="layout-grid grow-fill space-lg mq-sm-cols-four">
                <label
                  class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                >
                  Name
                  <NInput v-model:value="target.name" size="small" required />
                </label>
                <label
                  class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                >
                  Type
                  <AccessibleSelect
                    v-model:value="target.targetType"
                    :accessible-label="`Target ${index + 1} type`"
                    size="small"
                    :options="targetTypeOptions"
                  />
                </label>
                <label
                  class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                >
                  Priority group
                  <NInputNumber
                    :show-button="false"
                    v-model:value="target.priorityGroup"
                    size="small"
                    :min="0"
                  />
                </label>
                <label
                  class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                >
                  Weight
                  <NInputNumber
                    :show-button="false"
                    v-model:value="target.weight"
                    size="small"
                    :min="1"
                  />
                </label>
                <label
                  v-if="target.targetType === PublicRouteTargetType.PROXY"
                  class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text mq-sm-span-two"
                >
                  URL
                  <NInput
                    v-model:value="target.url"
                    size="small"
                    placeholder="http://upstream:9000"
                    required
                  />
                </label>
                <label
                  v-if="target.targetType === PublicRouteTargetType.PROXY"
                  class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                >
                  Transport
                  <AccessibleSelect
                    v-model:value="target.transport"
                    :accessible-label="`Target ${index + 1} transport`"
                    :options="targetTransportOptions"
                    size="small"
                  />
                </label>
                <label
                  v-if="target.targetType === PublicRouteTargetType.PROXY"
                  class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                >
                  Header timeout ms
                  <NInputNumber
                    :show-button="false"
                    v-model:value="target.responseHeaderTimeoutMillis"
                    size="small"
                    :min="1"
                  />
                </label>
                <div
                  v-if="
                    target.targetType === PublicRouteTargetType.PROXY &&
                    target.transport === PublicRouteTargetTransport.AGENT
                  "
                  class="layout-grid space-md round-md framed frame-standard muted-bg pad-md mq-sm-span-four"
                >
                  <div
                    class="layout-row wrap-items align-start spread-items space-md"
                  >
                    <div>
                      <p
                        class="copy-xs weight-medium label-case letter-wide muted-text"
                      >
                        Agent selector
                      </p>
                      <p class="margin-top-xs copy-xs line-normal muted-text">
                        All selector labels must match the same enabled agent.
                      </p>
                    </div>
                    <div class="layout-grid space-xs">
                      <span
                        class="copy-xs weight-medium label-case letter-wide muted-text"
                        >Match exact agent</span
                      >
                      <AccessibleSelect
                        :value="exactSelectorValue(target)"
                        :accessible-label="`Target ${index + 1} exact agent`"
                        :options="exactAgentOptions"
                        data-testid="exact-agent-selector"
                        class="min-w-[14rem]"
                        size="small"
                        @update:value="
                          setExactAgent(target, String($event ?? ''))
                        "
                      />
                    </div>
                  </div>
                  <div class="layout-grid space-sm">
                    <div
                      v-for="(selector, selectorIndex) in target.selectorLabels"
                      :key="selector.id"
                      data-testid="target-selector-row"
                      class="layout-grid space-sm mq-sm-two-auto"
                    >
                      <label
                        class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                      >
                        Selector key
                        <NInput
                          v-model:value="selector.key"
                          size="small"
                          placeholder="site"
                          required
                          :input-props="targetSelectorKeyInputProps"
                        />
                      </label>
                      <label
                        class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                      >
                        Selector value
                        <NInput
                          v-model:value="selector.value"
                          size="small"
                          placeholder="home-lab"
                          :input-props="targetSelectorValueInputProps"
                        />
                      </label>
                      <NButton
                        type="error"
                        size="small"
                        aria-label="Remove selector label"
                        title="Remove selector label"
                        class="self-align-end"
                        attr-type="button"
                        @click="removeSelectorLabel(target, selectorIndex)"
                      >
                        <template #icon
                          ><TrashIcon class="icon-sm icon-sm"
                        /></template>
                      </NButton>
                    </div>
                  </div>
                  <div
                    class="layout-row wrap-items align-center spread-items space-md"
                  >
                    <NButton
                      secondary
                      size="small"
                      attr-type="button"
                      @click="addSelectorLabel(target)"
                    >
                      <template #icon
                        ><PlusIcon class="icon-sm icon-sm"
                      /></template>
                      Add Selector
                    </NButton>
                    <div
                      data-testid="selector-match-preview"
                      class="align-right-text copy-xs line-normal"
                    >
                      <p
                        :class="
                          matchingAgents(target).length
                            ? 'base-text'
                            : 'warning-text'
                        "
                      >
                        Matches {{ matchingAgents(target).length }} enabled
                        agents;
                        {{ connectedMatchingAgents(target).length }} connected.
                      </p>
                      <p
                        v-if="matchingAgents(target).length"
                        class="muted-text"
                      >
                        {{
                          matchingAgents(target)
                            .slice(0, 3)
                            .map((agent) => agentDisplayName(agent.id))
                            .join(", ")
                        }}
                        <span v-if="matchingAgents(target).length > 3"
                          >+{{ matchingAgents(target).length - 3 }} more</span
                        >
                      </p>
                      <p v-else class="warning-text deemphasized">
                        No enabled agents currently match this selector.
                      </p>
                    </div>
                  </div>
                </div>
                <NCheckbox
                  v-if="target.targetType === PublicRouteTargetType.PROXY"
                  v-model:checked="target.tlsSkipVerify"
                >
                  Skip TLS verify
                </NCheckbox>
                <label
                  v-if="target.targetType === PublicRouteTargetType.STATIC"
                  class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                >
                  Status
                  <NInputNumber
                    :show-button="false"
                    v-model:value="target.staticStatusCode"
                    size="small"
                    :min="100"
                    :max="599"
                  />
                </label>
                <label
                  v-if="
                    target.targetType === PublicRouteTargetType.STATIC &&
                    target.staticResponseBodyMode !==
                      PublicResponseBodyMode.TEMPLATE
                  "
                  class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text mq-sm-span-three"
                >
                  Body
                  <NInput
                    v-model:value="target.staticResponseBody"
                    type="textarea"
                    size="small"
                    :autosize="{ minRows: 3, maxRows: 8 }"
                  />
                </label>
                <NCheckbox v-model:checked="target.enabled">
                  Enabled
                </NCheckbox>
                <details
                  class="target-advanced mq-sm-span-four round-md framed frame-standard"
                >
                  <summary class="copy-xs weight-semibold base-text">
                    Advanced target settings
                  </summary>
                  <div
                    v-if="target.targetType === PublicRouteTargetType.PROXY"
                    class="layout-grid space-xl pad-lg"
                  >
                    <section class="layout-grid space-md">
                      <div
                        class="layout-row align-center spread-items space-md"
                      >
                        <div>
                          <h5
                            class="copy-xs weight-semibold label-case letter-wide base-text"
                          >
                            Request headers
                          </h5>
                          <p class="margin-top-xs copy-xs muted-text">
                            Set or replace headers sent to this upstream.
                          </p>
                        </div>
                        <NButton
                          secondary
                          size="small"
                          attr-type="button"
                          @click="addUpstreamHeader(target)"
                          ><template #icon
                            ><PlusIcon class="icon-sm" /></template
                          >Add header</NButton
                        >
                      </div>
                      <div
                        v-for="(
                          header, headerIndex
                        ) in target.upstreamRequestHeaders"
                        :key="`${header.id}-${headerIndex}`"
                        class="advanced-header-row"
                      >
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Name<NInput
                            v-model:value="header.name"
                            size="small"
                            placeholder="X-Upstream-Token"
                            required
                        /></label>
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Value<NInput
                            v-model:value="header.value"
                            :type="header.sensitive ? 'password' : 'text'"
                            size="small"
                            :placeholder="
                              header.valueSet
                                ? 'Stored value unchanged'
                                : 'Header value'
                            "
                        /></label>
                        <NCheckbox
                          v-model:checked="header.sensitive"
                          class="self-align-end"
                          >Sensitive</NCheckbox
                        >
                        <NButton
                          type="error"
                          size="small"
                          attr-type="button"
                          aria-label="Remove upstream header"
                          class="self-align-end"
                          @click="removeUpstreamHeader(target, headerIndex)"
                          ><template #icon
                            ><TrashIcon class="icon-sm" /></template
                        ></NButton>
                      </div>
                    </section>
                    <section
                      class="layout-grid space-md divider-top pad-top-lg"
                    >
                      <NCheckbox
                        v-model:checked="target.upstreamBasicAuth.enabled"
                        >Send upstream basic authentication</NCheckbox
                      >
                      <div
                        v-if="target.upstreamBasicAuth.enabled"
                        class="layout-grid space-md mq-sm-cols-two"
                      >
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Username<NInput
                            v-model:value="target.upstreamBasicAuth.username"
                            size="small"
                            autocomplete="off"
                        /></label>
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Password<NInput
                            v-model:value="target.upstreamBasicAuth.password"
                            type="password"
                            size="small"
                            autocomplete="new-password"
                            :placeholder="
                              target.upstreamBasicAuth.passwordSet
                                ? 'Stored password unchanged'
                                : 'Enter password'
                            "
                        /></label>
                      </div>
                    </section>
                    <section
                      class="layout-grid space-md divider-top pad-top-lg"
                    >
                      <NCheckbox v-model:checked="target.healthCheck.enabled"
                        >Enable active health check</NCheckbox
                      >
                      <div
                        v-if="target.healthCheck.enabled"
                        class="layout-grid space-md mq-sm-cols-four"
                      >
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Method<NInput
                            v-model:value="target.healthCheck.method"
                            size="small"
                        /></label>
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Path<NInput
                            v-model:value="target.healthCheck.path"
                            size="small"
                            placeholder="/health"
                        /></label>
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Interval ms<NInputNumber
                            v-model:value="target.healthCheck.intervalMillis"
                            :show-button="false"
                            size="small"
                            :min="1"
                        /></label>
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Timeout ms<NInputNumber
                            v-model:value="target.healthCheck.timeoutMillis"
                            :show-button="false"
                            size="small"
                            :min="1"
                        /></label>
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Healthy threshold<NInputNumber
                            v-model:value="target.healthCheck.healthyThreshold"
                            :show-button="false"
                            size="small"
                            :min="1"
                        /></label>
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Unhealthy threshold<NInputNumber
                            v-model:value="
                              target.healthCheck.unhealthyThreshold
                            "
                            :show-button="false"
                            size="small"
                            :min="1"
                        /></label>
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Status from<NInputNumber
                            v-model:value="target.healthCheck.expectedStatusMin"
                            :show-button="false"
                            size="small"
                            :min="100"
                            :max="599"
                        /></label>
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Status through<NInputNumber
                            v-model:value="target.healthCheck.expectedStatusMax"
                            :show-button="false"
                            size="small"
                            :min="100"
                            :max="599"
                        /></label>
                      </div>
                    </section>
                  </div>
                  <div v-else class="layout-grid space-xl pad-lg">
                    <section class="layout-grid space-md mq-sm-cols-two">
                      <label
                        class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                        >Response body source<AccessibleSelect
                          v-model:value="target.staticResponseBodyMode"
                          :accessible-label="`Target ${index + 1} response body source`"
                          size="small"
                          :options="responseBodyModeOptions"
                      /></label>
                      <label
                        v-if="
                          target.staticResponseBodyMode ===
                          PublicResponseBodyMode.TEMPLATE
                        "
                        class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                        >Response template<AccessibleSelect
                          v-model:value="target.staticResponseTemplateId"
                          :accessible-label="`Target ${index + 1} response template`"
                          size="small"
                          :options="responseTemplateOptions"
                      /></label>
                    </section>
                    <section
                      class="layout-grid space-md divider-top pad-top-lg"
                    >
                      <div
                        class="layout-row align-center spread-items space-md"
                      >
                        <h5
                          class="copy-xs weight-semibold label-case letter-wide base-text"
                        >
                          Response headers
                        </h5>
                        <NButton
                          secondary
                          size="small"
                          attr-type="button"
                          @click="addResponseHeader(target)"
                          ><template #icon
                            ><PlusIcon class="icon-sm" /></template
                          >Add header</NButton
                        >
                      </div>
                      <div
                        v-for="(
                          header, headerIndex
                        ) in target.staticResponseHeaders"
                        :key="headerIndex"
                        class="advanced-header-row advanced-header-row--response"
                      >
                        <label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Name<NInput
                            v-model:value="header.name"
                            size="small"
                            required /></label
                        ><label
                          class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text"
                          >Value<NInput
                            v-model:value="header.value"
                            size="small" /></label
                        ><NButton
                          type="error"
                          size="small"
                          attr-type="button"
                          aria-label="Remove response header"
                          class="self-align-end"
                          @click="removeResponseHeader(target, headerIndex)"
                          ><template #icon
                            ><TrashIcon class="icon-sm" /></template
                        ></NButton>
                      </div>
                    </section>
                  </div>
                </details>
              </div>
              <NButton
                type="error"
                size="small"
                aria-label="Remove target"
                title="Remove target"
                attr-type="button"
                @click="removeTarget(index)"
              >
                <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
              </NButton>
            </div>
          </div>
        </section>

        <p
          v-if="routeSubmitError"
          class="round-md framed error-border error-surface pad-md copy-xs line-normal error-text"
          role="alert"
        >
          {{ routeSubmitError }}
        </p>
        <div
          class="editor-drawer-actions margin-top-sm layout-row align-end-row space-md"
        >
          <NButton secondary attr-type="button" @click="close">Cancel</NButton>
          <DisabledHint
            :disabled="routeSubmitDisabled"
            :reason="routeSubmitDisabledReason"
          >
            <NButton
              type="primary"
              attr-type="submit"
              :disabled="routeSubmitDisabled"
            >
              {{ submitLabel }}
            </NButton>
          </DisabledHint>
        </div>
      </form>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.route-site-context {
  border-left: 3px solid var(--app-accent);
  background: var(--app-accent-soft);
}

.target-advanced {
  overflow: hidden;
  background: var(--app-panel);
}

.target-advanced > summary {
  padding: 0.75rem 1rem;
  cursor: pointer;
  list-style-position: inside;
}

.target-advanced[open] > summary {
  border-bottom: 1px solid var(--app-border-subtle);
}

.advanced-header-row {
  display: grid;
  grid-template-columns: minmax(10rem, 1fr) minmax(12rem, 1.5fr) auto auto;
  gap: 0.75rem;
  align-items: end;
}

.advanced-header-row--response {
  grid-template-columns: minmax(10rem, 1fr) minmax(12rem, 1.5fr) auto;
}

@media (max-width: 640px) {
  .advanced-header-row,
  .advanced-header-row--response {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
