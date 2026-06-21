<script setup lang="ts">
import { computed, inject, reactive, ref, watch } from "vue";
import type { InputHTMLAttributes } from "vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NCheckbox, NInput, NInputNumber, NModal, NSelect } from "naive-ui";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import {
  AGENT_ID_SYSTEM_LABEL_KEY,
  agentMatchesSelector,
  selectorRowsFromLabels,
  selectorRowsToRecord,
  validateSelectorRows,
  type SelectorLabelRow,
} from "@/lib/agentLabels";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { modalCardStyle } from "@/lib/naiveUi";
import {
  PublicRouteTargetLoadBalancing,
  PublicResponseBodyMode,
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
  tlsSkipVerify: boolean;
  responseHeaderTimeoutMillis: number;
  staticStatusCode: number;
  staticResponseBody: string;
};

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));

const isOpen = ref(false);
const routeFormMode = ref<RouteFormMode>("create");
const listeners = computed(() => props.config?.listeners ?? []);
const routes = computed(() => props.config?.routes ?? []);
const agents = computed(() => props.config?.agents ?? []);
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
  { label: "Weighted round-robin", value: PublicRouteTargetLoadBalancing.WEIGHTED_ROUND_ROBIN },
  { label: "Random", value: PublicRouteTargetLoadBalancing.RANDOM },
  { label: "Weighted random", value: PublicRouteTargetLoadBalancing.WEIGHTED_RANDOM },
  { label: "Least active", value: PublicRouteTargetLoadBalancing.LEAST_ACTIVE_REQUESTS },
  { label: "Weighted least active", value: PublicRouteTargetLoadBalancing.WEIGHTED_LEAST_ACTIVE_REQUESTS },
];
const redirectTargetModeOptions = [
  { label: "Same host path", value: PublicRouteRedirectTargetMode.SAME_HOST_PATH },
  { label: "External origin", value: PublicRouteRedirectTargetMode.EXTERNAL_ORIGIN_KEEP_PATH },
  { label: "Absolute URL", value: PublicRouteRedirectTargetMode.ABSOLUTE_URL },
];
const pathSecurityModeOptions = [
  { label: "Strict", value: PublicRoutePathSecurityMode.STRICT },
  { label: "Allow encoded separators", value: PublicRoutePathSecurityMode.ALLOW_ENCODED_SEPARATORS },
];
const targetTransportOptions = [
  { label: "Direct", value: PublicRouteTargetTransport.DIRECT },
  { label: "Agent", value: PublicRouteTargetTransport.AGENT },
];
const targetTypeOptions = [
  { label: "Proxy", value: PublicRouteTargetType.PROXY },
  { label: "Static", value: PublicRouteTargetType.STATIC },
];
const targetSelectorKeyInputProps = { "data-testid": "target-selector-key" } as unknown as InputHTMLAttributes;
const targetSelectorValueInputProps = { "data-testid": "target-selector-value" } as unknown as InputHTMLAttributes;
const exactAgentOptions = computed(() => [
  { label: "Choose agent", value: "" },
  ...agents.value.map((agent) => ({
    label: `${agent.name} (${agent.publicId})`,
    value: agent.publicId,
  })),
]);

const routeForm = reactive({
  id: "",
  listenerId: "",
  action: PublicRouteAction.FORWARD,
  priority: 100,
  hostPattern: "",
  pathPrefix: "",
  pathSecurityMode: PublicRoutePathSecurityMode.STRICT,
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

const routeIsRedirect = computed(() => routeForm.action === PublicRouteAction.REDIRECT);
const modalTitle = computed(() => (
  routeFormMode.value === "edit" ? "Edit Route" :
    routeFormMode.value === "clone" ? "Clone Route" :
      "Add Route"
));
const submitLabel = computed(() => (
  routeFormMode.value === "edit" ? "Save Changes" :
    routeFormMode.value === "clone" ? "Create Clone" :
      "Create Route"
));
const routeSubmitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!listeners.value.length) return "Create a listener before creating a route.";
  if (routeIsRedirect.value && routeForm.redirectTarget.trim() === "") return "Enter a redirect target.";
  if (!routeIsRedirect.value && !routeForm.targets.length) return "Add at least one target.";
  const targetError = routeForm.targets.map(targetValidationReason).find(Boolean);
  return targetError || "";
});
const routeSubmitDisabled = computed(() => Boolean(routeSubmitDisabledReason.value));

function routeAction(route: PublicRoute): PublicRouteAction {
  return route.action === PublicRouteAction.REDIRECT ? PublicRouteAction.REDIRECT : PublicRouteAction.FORWARD;
}

function redirectTargetPlaceholder(mode: PublicRouteRedirectTargetMode): string {
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
    tlsSkipVerify: false,
    responseHeaderTimeoutMillis: 60000,
    staticStatusCode: 200,
    staticResponseBody: "",
  };
}

function resetForm() {
  routeForm.id = "";
  routeForm.listenerId = listeners.value[0]?.id.toString() ?? "";
  routeForm.action = PublicRouteAction.FORWARD;
  routeForm.priority = 100;
  routeForm.hostPattern = "";
  routeForm.pathPrefix = "";
  routeForm.pathSecurityMode = PublicRoutePathSecurityMode.STRICT;
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

function targetFormFromProto(target: PublicRouteTarget): TargetForm {
  const labels = target.agentSelector?.matchLabels ?? {};
  const selectorLabels = selectorRowsFromLabels(labels);
  return {
    id: target.id.toString(),
    name: target.name,
    enabled: target.enabled,
    targetType: target.targetType || PublicRouteTargetType.PROXY,
    url: target.url,
    transport: target.transport || PublicRouteTargetTransport.DIRECT,
    selectorLabels: selectorLabels.length ? selectorLabels.map(cloneSelectorRow) : [newSelectorRow()],
    priorityGroup: Number(target.priorityGroup || 0n),
    weight: Number(target.weight || 100n),
    tlsSkipVerify: target.tlsSkipVerify,
    responseHeaderTimeoutMillis: Number(target.upstreamResponseHeaderTimeoutMillis || 60000n),
    staticStatusCode: Number(target.staticStatusCode || 200n),
    staticResponseBody: target.staticResponseBody,
  };
}

function populateRouteForm(route: PublicRoute, mode: "edit" | "clone") {
  resetForm();
  routeFormMode.value = mode;
  const action = routeAction(route);
  routeForm.id = mode === "clone" ? "" : route.id.toString();
  routeForm.listenerId = route.listenerId.toString();
  routeForm.action = action;
  routeForm.priority = Number(route.priority);
  routeForm.hostPattern = route.hostPattern;
  routeForm.pathPrefix = route.pathPrefix;
  routeForm.pathSecurityMode = route.pathSecurityMode || PublicRoutePathSecurityMode.STRICT;
  routeForm.targetLoadBalancing = route.targetLoadBalancing || PublicRouteTargetLoadBalancing.ROUND_ROBIN;
  routeForm.isDefault = route.isDefault;
  routeForm.targets = action === PublicRouteAction.REDIRECT ? [] : route.targets.map(targetFormFromProto);
  if (action !== PublicRouteAction.REDIRECT && !routeForm.targets.length) routeForm.targets = [defaultTarget(0)];
  routeForm.redirectTargetMode = route.redirectTargetMode || PublicRouteRedirectTargetMode.SAME_HOST_PATH;
  routeForm.redirectTarget = route.redirectTarget;
  routeForm.redirectStatusCode = Number(route.redirectStatusCode || 302n);
  routeForm.redirectPreservePathSuffix = route.redirectPreservePathSuffix;
  routeForm.redirectPreserveQuery = route.redirectPreserveQuery;
  routeForm.enabled = route.enabled;
  isOpen.value = true;
}

function openCreate() {
  resetForm();
  routeFormMode.value = "create";
  isOpen.value = true;
}

function openEdit(routeId: bigint | string) {
  const id = routeId.toString();
  const route = routes.value.find((item) => item.id.toString() === id);
  if (!route) return;
  populateRouteForm(route, "edit");
}

function openClone(routeId: bigint | string) {
  const id = routeId.toString();
  const route = routes.value.find((item) => item.id.toString() === id);
  if (!route) return;
  populateRouteForm(route, "clone");
}

function addTarget() {
  routeForm.targets.push(defaultTarget(routeForm.targets.length));
}

function removeTarget(index: number) {
  routeForm.targets.splice(index, 1);
}

function close() {
  isOpen.value = false;
}

async function run(action: () => Promise<void>): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action);
}

function selectorPayload(target: TargetForm): { matchLabels: Record<string, string> } {
  if (target.targetType !== PublicRouteTargetType.PROXY || target.transport !== PublicRouteTargetTransport.AGENT) {
    return { matchLabels: {} };
  }
  return { matchLabels: selectorRowsToRecord(target.selectorLabels) };
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
    agentLoadBalancing: PublicRouteTargetLoadBalancing.ROUND_ROBIN,
    tlsSkipVerify: !isStatic && target.tlsSkipVerify,
    upstreamResponseHeaderTimeoutMillis: BigInt(Math.max(1, target.responseHeaderTimeoutMillis || 60000)),
    upstreamRequestHeaders: [],
    upstreamBasicAuth: { enabled: false, username: "", password: "", passwordSet: false },
    healthCheck: {
      enabled: false,
      method: "GET",
      path: "/",
      intervalMillis: 10000n,
      timeoutMillis: 2000n,
      healthyThreshold: 2n,
      unhealthyThreshold: 2n,
      expectedStatusMin: 200n,
      expectedStatusMax: 399n,
    },
    staticStatusCode: BigInt(isStatic ? target.staticStatusCode || 200 : 200),
    staticResponseHeaders: [],
    staticResponseBody: isStatic ? target.staticResponseBody : "",
    staticResponseBodyMode: PublicResponseBodyMode.INLINE,
    staticResponseTemplateId: 0n,
  };
}

function targetValidationReason(target: TargetForm): string {
  if (!target.name.trim()) return "Every target needs a name.";
  if (target.targetType === PublicRouteTargetType.PROXY && !target.url.trim()) return "Proxy targets need a URL.";
  if (target.targetType === PublicRouteTargetType.PROXY && target.transport === PublicRouteTargetTransport.AGENT) {
    const selectorError = validateSelectorRows(target.selectorLabels);
    if (selectorError) return selectorError;
  }
  if (target.weight < 1) return "Target weight must be at least 1.";
  if (target.responseHeaderTimeoutMillis < 1) return "Response-header timeout must be positive.";
  return "";
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
  return agents.value.filter((agent) => agent.enabled && agentMatchesSelector(agent, selector));
}

function connectedMatchingAgents(target: TargetForm) {
  return matchingAgents(target).filter((agent) => agent.connected);
}

function exactSelectorValue(target: TargetForm): string {
  return selectorRowsToRecord(target.selectorLabels)[AGENT_ID_SYSTEM_LABEL_KEY] ?? "";
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
  const agent = agents.value.find((item) => item.id.toString() === agentID.toString());
  return agent ? `${agent.name} (${agent.publicId})` : agentID.toString();
}

async function submitRoute() {
  const ok = await run(async () => {
    const isRedirect = routeForm.action === PublicRouteAction.REDIRECT;
    const payload = {
      listenerId: BigInt(routeForm.listenerId || "0"),
      priority: BigInt(routeForm.priority),
      hostPattern: routeForm.hostPattern,
      pathPrefix: routeForm.pathPrefix,
      pathSecurityMode: routeForm.pathSecurityMode,
      action: routeForm.action,
      targetLoadBalancing: isRedirect ? PublicRouteTargetLoadBalancing.ROUND_ROBIN : routeForm.targetLoadBalancing,
      isDefault: routeForm.isDefault,
      targets: isRedirect ? [] : routeForm.targets.map(targetPayload),
      redirectTargetMode: isRedirect ? routeForm.redirectTargetMode : PublicRouteRedirectTargetMode.UNSPECIFIED,
      redirectTarget: isRedirect ? routeForm.redirectTarget : "",
      redirectStatusCode: BigInt(isRedirect ? routeForm.redirectStatusCode || 302 : 302),
      redirectPreservePathSuffix: isRedirect ? routeForm.redirectPreservePathSuffix : true,
      redirectPreserveQuery: isRedirect ? routeForm.redirectPreserveQuery : true,
      enabled: routeForm.enabled,
    };
    if (routeForm.id) {
      await managementClient.updatePublicRoute({ id: BigInt(routeForm.id), ...payload });
    } else {
      await managementClient.createPublicRoute(payload);
    }
  });
  if (ok) {
    isOpen.value = false;
    emit("saved");
  }
}

watch(listeners, () => {
  if (!routeForm.listenerId && listeners.value[0]) {
    routeForm.listenerId = listeners.value[0].id.toString();
  }
}, { immediate: true });

watch(
  () => routeForm.action,
  (action) => {
    if (action === PublicRouteAction.FORWARD && !routeForm.targets.length) {
      routeForm.targets = [defaultTarget(0)];
    }
  },
);

defineExpose({ openCreate, openEdit, openClone, close });
</script>

<template>
  <NModal
    v-model:show="isOpen"
    preset="card"
    :title="modalTitle"
    :style="modalCardStyle('72rem')"
    :bordered="false"
    size="huge"
  >
    <form class="grid max-h-[calc(100vh-9rem)] gap-5 overflow-y-auto pr-1" @submit.prevent="submitRoute">
      <section class="grid gap-4 sm:grid-cols-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Listener
          <NSelect v-model:value="routeForm.listenerId" size="small" :options="listenerOptions" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Action
          <NSelect v-model:value="routeForm.action" size="small" :options="routeActionOptions" />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Priority
          <NInputNumber v-model:value="routeForm.priority" size="small" required />
        </label>
        <NCheckbox v-model:checked="routeForm.enabled" class="self-end">
          Enabled
        </NCheckbox>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Host pattern
          <NInput v-model:value="routeForm.hostPattern" size="small" placeholder="*.example.com" />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Path prefix
          <NInput v-model:value="routeForm.pathPrefix" size="small" placeholder="/" />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Path security
          <NSelect v-model:value="routeForm.pathSecurityMode" size="small" :options="pathSecurityModeOptions" />
          <span class="normal-case tracking-normal">Compatibility mode is for upstreams that require encoded / or \ path identifiers.</span>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Target balancing
          <NSelect
            v-model:value="routeForm.targetLoadBalancing"
            size="small"
            :options="targetLoadBalancingOptions"
            :disabled="routeIsRedirect"
          />
        </label>
        <NCheckbox v-model:checked="routeForm.isDefault" class="self-end">
          Default route
        </NCheckbox>
      </section>

      <section v-if="routeIsRedirect" class="grid gap-4 rounded-md border border-[var(--app-border)] bg-[var(--app-panel-muted)] p-4 sm:grid-cols-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Mode
          <NSelect v-model:value="routeForm.redirectTargetMode" size="small" :options="redirectTargetModeOptions" />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)] sm:col-span-2">
          Target
          <NInput v-model:value="routeForm.redirectTarget" size="small" :placeholder="redirectTargetPlaceholder(routeForm.redirectTargetMode)" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
          Status
          <NInputNumber v-model:value="routeForm.redirectStatusCode" size="small" :min="300" :max="399" />
        </label>
        <NCheckbox v-model:checked="routeForm.redirectPreservePathSuffix">
          Preserve path suffix
        </NCheckbox>
        <NCheckbox v-model:checked="routeForm.redirectPreserveQuery">
          Preserve query
        </NCheckbox>
      </section>

      <section v-else class="grid gap-4">
        <div class="flex items-center justify-between gap-3">
          <h4 class="text-sm font-semibold text-[var(--app-text)]">Targets</h4>
          <NButton secondary size="small" attr-type="button" @click="addTarget">
            <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
            Add Target
          </NButton>
        </div>

        <div v-for="(target, index) in routeForm.targets" :key="`${target.id || 'new'}-${index}`" data-testid="route-target-row" class="grid gap-4 rounded-md border border-[var(--app-border)] bg-[var(--app-panel-muted)] p-4">
          <div class="flex items-start justify-between gap-3">
            <div class="grid flex-1 gap-4 sm:grid-cols-4">
              <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
                Name
                <NInput v-model:value="target.name" size="small" required />
              </label>
              <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
                Type
                <NSelect v-model:value="target.targetType" size="small" :options="targetTypeOptions" />
              </label>
              <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
                Priority group
                <NInputNumber v-model:value="target.priorityGroup" size="small" :min="0" />
              </label>
              <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
                Weight
                <NInputNumber v-model:value="target.weight" size="small" :min="1" />
              </label>
              <label v-if="target.targetType === PublicRouteTargetType.PROXY" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)] sm:col-span-2">
                URL
                <NInput v-model:value="target.url" size="small" placeholder="http://upstream:9000" required />
              </label>
              <label v-if="target.targetType === PublicRouteTargetType.PROXY" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
                Transport
                <NSelect
                  v-model:value="target.transport"
                  :options="targetTransportOptions"
                  size="small"
                  aria-label="Transport"
                />
              </label>
              <label v-if="target.targetType === PublicRouteTargetType.PROXY" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
                Header timeout ms
                <NInputNumber v-model:value="target.responseHeaderTimeoutMillis" size="small" :min="1" />
              </label>
              <div v-if="target.targetType === PublicRouteTargetType.PROXY && target.transport === PublicRouteTargetTransport.AGENT" class="grid gap-3 rounded-md border border-[var(--app-border)] bg-[var(--app-panel-muted)] p-3 sm:col-span-4">
                <div class="flex flex-wrap items-start justify-between gap-3">
                  <div>
                    <p class="text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">Agent selector</p>
                    <p class="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">All selector labels must match the same enabled agent.</p>
                  </div>
                  <div class="grid gap-1.5">
                    <span class="text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">Match exact agent</span>
                    <NSelect
                      :value="exactSelectorValue(target)"
                      :options="exactAgentOptions"
                      data-testid="exact-agent-selector"
                      class="min-w-[14rem]"
                      size="small"
                      aria-label="Match exact agent"
                      @update:value="setExactAgent(target, String($event ?? ''))"
                    />
                  </div>
                </div>
                <div class="grid gap-2">
                  <div v-for="(selector, selectorIndex) in target.selectorLabels" :key="selector.id" data-testid="target-selector-row" class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
                    <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
                      Selector key
                      <NInput
                        v-model:value="selector.key"
                        size="small"
                        placeholder="site"
                        required
                        :input-props="targetSelectorKeyInputProps"
                      />
                    </label>
                    <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
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
                      class="self-end"
                      attr-type="button"
                      @click="removeSelectorLabel(target, selectorIndex)"
                    >
                      <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
                    </NButton>
                  </div>
                </div>
                <div class="flex flex-wrap items-center justify-between gap-3">
                  <NButton secondary size="small" attr-type="button" @click="addSelectorLabel(target)">
                    <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
                    Add Selector
                  </NButton>
                  <div data-testid="selector-match-preview" class="text-right text-xs leading-5">
                    <p :class="matchingAgents(target).length ? 'text-[var(--app-text)]' : 'text-[var(--app-warning)]'">
                      Matches {{ matchingAgents(target).length }} enabled agents; {{ connectedMatchingAgents(target).length }} connected.
                    </p>
                    <p v-if="matchingAgents(target).length" class="text-[var(--app-text-muted)]">
                      {{ matchingAgents(target).slice(0, 3).map((agent) => agentDisplayName(agent.id)).join(", ") }}
                      <span v-if="matchingAgents(target).length > 3">+{{ matchingAgents(target).length - 3 }} more</span>
                    </p>
                    <p v-else class="text-[var(--app-warning)] opacity-80">No enabled agents currently match this selector.</p>
                  </div>
                </div>
              </div>
              <NCheckbox v-if="target.targetType === PublicRouteTargetType.PROXY" v-model:checked="target.tlsSkipVerify">
                Skip TLS verify
              </NCheckbox>
              <label v-if="target.targetType === PublicRouteTargetType.STATIC" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
                Status
                <NInputNumber v-model:value="target.staticStatusCode" size="small" :min="100" :max="599" />
              </label>
              <label v-if="target.targetType === PublicRouteTargetType.STATIC" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[var(--app-text-muted)] sm:col-span-3">
                Body
                <NInput v-model:value="target.staticResponseBody" type="textarea" size="small" :autosize="{ minRows: 3, maxRows: 8 }" />
              </label>
              <NCheckbox v-model:checked="target.enabled">
                Enabled
              </NCheckbox>
            </div>
            <NButton type="error" size="small" aria-label="Remove target" title="Remove target" attr-type="button" @click="removeTarget(index)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </NButton>
          </div>
        </div>
      </section>

      <div class="mt-2 flex justify-end gap-3">
        <NButton secondary attr-type="button" @click="close">Cancel</NButton>
        <DisabledHint :disabled="routeSubmitDisabled" :reason="routeSubmitDisabledReason">
          <NButton type="primary" attr-type="submit" :disabled="routeSubmitDisabled">
            {{ submitLabel }}
          </NButton>
        </DisabledHint>
      </div>
    </form>
  </NModal>
</template>
