<script setup lang="ts">
import { computed, inject, reactive, ref, watch } from "vue";
import type { ComputedRef } from "vue";
import PlusIcon from "@primevue/icons/plus";
import TrashIcon from "@primevue/icons/trash";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import {
  AGENT_ID_SYSTEM_LABEL_KEY,
  agentLabelKeySuggestions,
  agentLabelValueSuggestions,
  agentMatchesSelector,
  selectorRowsFromLabels,
  selectorRowsToRecord,
  validateSelectorRows,
  type SelectorLabelRow,
} from "@/lib/agentLabels";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import {
  PublicRouteTargetLoadBalancing,
  PublicResponseBodyMode,
  PublicRouteAction,
  PublicRouteRedirectTargetMode,
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  type GetPublicProxyConfigResponse,
  type PublicRoute,
  type PublicRouteTarget,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

type Runner = (action: () => Promise<void>) => Promise<boolean>;
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

const runManagementAction = inject<Runner>("runManagementAction");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const isOpen = ref(false);
const routeFormMode = ref<RouteFormMode>("create");
const listeners = computed(() => props.config?.listeners ?? []);
const routes = computed(() => props.config?.routes ?? []);
const agents = computed(() => props.config?.agents ?? []);
const selectorKeySuggestions = computed(() => agentLabelKeySuggestions(agents.value));

const routeForm = reactive({
  id: "",
  listenerId: "",
  action: PublicRouteAction.FORWARD,
  priority: 100,
  hostPattern: "",
  pathPrefix: "",
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

function selectorValueSuggestions(key: string): string[] {
  return agentLabelValueSuggestions(agents.value, key);
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

function eventValue(event: Event): string {
  return event.target instanceof HTMLSelectElement ? event.target.value : "";
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
  <Modal v-model="isOpen" :title="modalTitle" max-width="72rem">
    <form @submit.prevent="submitRoute" class="grid gap-5">
      <section class="grid gap-4 sm:grid-cols-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Listener
          <select v-model="routeForm.listenerId" class="vercel-input text-sm normal-case tracking-normal" required>
            <option v-for="listener in listeners" :key="listener.id.toString()" :value="listener.id.toString()">{{ listener.name }}</option>
          </select>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Action
          <select v-model="routeForm.action" class="vercel-input text-sm normal-case tracking-normal">
            <option :value="PublicRouteAction.FORWARD">Forward</option>
            <option :value="PublicRouteAction.REDIRECT">Redirect</option>
          </select>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Priority
          <input v-model.number="routeForm.priority" type="number" class="vercel-input text-sm normal-case tracking-normal" required />
        </label>
        <label class="flex items-center gap-2 self-end text-sm text-[#d4d4d8]">
          <input v-model="routeForm.enabled" type="checkbox" />
          Enabled
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Host pattern
          <input v-model="routeForm.hostPattern" class="vercel-input text-sm normal-case tracking-normal" placeholder="*.example.com" />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Path prefix
          <input v-model="routeForm.pathPrefix" class="vercel-input text-sm normal-case tracking-normal" placeholder="/" />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Target balancing
          <select v-model="routeForm.targetLoadBalancing" class="vercel-input text-sm normal-case tracking-normal" :disabled="routeIsRedirect">
            <option :value="PublicRouteTargetLoadBalancing.ROUND_ROBIN">Round-robin</option>
            <option :value="PublicRouteTargetLoadBalancing.WEIGHTED_ROUND_ROBIN">Weighted round-robin</option>
            <option :value="PublicRouteTargetLoadBalancing.RANDOM">Random</option>
            <option :value="PublicRouteTargetLoadBalancing.WEIGHTED_RANDOM">Weighted random</option>
            <option :value="PublicRouteTargetLoadBalancing.LEAST_ACTIVE_REQUESTS">Least active</option>
            <option :value="PublicRouteTargetLoadBalancing.WEIGHTED_LEAST_ACTIVE_REQUESTS">Weighted least active</option>
          </select>
        </label>
        <label class="flex items-center gap-2 self-end text-sm text-[#d4d4d8]">
          <input v-model="routeForm.isDefault" type="checkbox" />
          Default route
        </label>
      </section>

      <section v-if="routeIsRedirect" class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4 sm:grid-cols-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Mode
          <select v-model="routeForm.redirectTargetMode" class="vercel-input text-sm normal-case tracking-normal">
            <option :value="PublicRouteRedirectTargetMode.SAME_HOST_PATH">Same host path</option>
            <option :value="PublicRouteRedirectTargetMode.EXTERNAL_ORIGIN_KEEP_PATH">External origin</option>
            <option :value="PublicRouteRedirectTargetMode.ABSOLUTE_URL">Absolute URL</option>
          </select>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
          Target
          <input v-model="routeForm.redirectTarget" class="vercel-input text-sm normal-case tracking-normal" :placeholder="redirectTargetPlaceholder(routeForm.redirectTargetMode)" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Status
          <input v-model.number="routeForm.redirectStatusCode" type="number" min="300" max="399" class="vercel-input text-sm normal-case tracking-normal" />
        </label>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
          <input v-model="routeForm.redirectPreservePathSuffix" type="checkbox" />
          Preserve path suffix
        </label>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
          <input v-model="routeForm.redirectPreserveQuery" type="checkbox" />
          Preserve query
        </label>
      </section>

      <section v-else class="grid gap-4">
        <div class="flex items-center justify-between gap-3">
          <h4 class="text-sm font-semibold text-white">Targets</h4>
          <SecondaryButton type="button" size="small" label="Add Target" @click="addTarget">
            <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          </SecondaryButton>
        </div>

        <div v-for="(target, index) in routeForm.targets" :key="`${target.id || 'new'}-${index}`" data-testid="route-target-row" class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
          <div class="flex items-start justify-between gap-3">
            <div class="grid flex-1 gap-4 sm:grid-cols-4">
              <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
                Name
                <input v-model="target.name" class="vercel-input text-sm normal-case tracking-normal" required />
              </label>
              <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
                Type
                <select v-model="target.targetType" class="vercel-input text-sm normal-case tracking-normal">
                  <option :value="PublicRouteTargetType.PROXY">Proxy</option>
                  <option :value="PublicRouteTargetType.STATIC">Static</option>
                </select>
              </label>
              <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
                Priority group
                <input v-model.number="target.priorityGroup" type="number" min="0" class="vercel-input text-sm normal-case tracking-normal" />
              </label>
              <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
                Weight
                <input v-model.number="target.weight" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
              </label>
              <label v-if="target.targetType === PublicRouteTargetType.PROXY" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
                URL
                <input v-model="target.url" class="vercel-input text-sm normal-case tracking-normal" placeholder="http://upstream:9000" required />
              </label>
              <label v-if="target.targetType === PublicRouteTargetType.PROXY" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
                Transport
                <select v-model="target.transport" class="vercel-input text-sm normal-case tracking-normal">
                  <option :value="PublicRouteTargetTransport.DIRECT">Direct</option>
                  <option :value="PublicRouteTargetTransport.AGENT">Agent</option>
                </select>
              </label>
              <label v-if="target.targetType === PublicRouteTargetType.PROXY" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
                Header timeout ms
                <input v-model.number="target.responseHeaderTimeoutMillis" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
              </label>
              <div v-if="target.targetType === PublicRouteTargetType.PROXY && target.transport === PublicRouteTargetTransport.AGENT" class="grid gap-3 rounded-md border border-[#1f1f1f] bg-[#080808] p-3 sm:col-span-4">
                <div class="flex flex-wrap items-start justify-between gap-3">
                  <div>
                    <p class="text-xs font-medium uppercase tracking-wider text-[#888]">Agent selector</p>
                    <p class="mt-1 text-xs leading-5 text-[#777]">All selector labels must match the same enabled agent.</p>
                  </div>
                  <div class="grid gap-1.5">
                    <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Match exact agent</span>
                    <select :value="exactSelectorValue(target)" data-testid="exact-agent-selector" class="vercel-input min-w-[14rem] text-sm normal-case tracking-normal" @change="setExactAgent(target, eventValue($event))">
                      <option value="">Choose agent</option>
                      <option v-for="agent in agents" :key="agent.id.toString()" :value="agent.publicId">{{ agent.name }} ({{ agent.publicId }})</option>
                    </select>
                  </div>
                </div>
                <div class="grid gap-2">
                  <div v-for="(selector, selectorIndex) in target.selectorLabels" :key="selector.id" data-testid="target-selector-row" class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
                    <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
                      Selector key
                      <input
                        v-model="selector.key"
                        data-testid="target-selector-key"
                        class="vercel-input text-sm normal-case tracking-normal"
                        placeholder="site"
                        :list="`selector-key-options-${index}-${selectorIndex}`"
                        required
                      />
                      <datalist :id="`selector-key-options-${index}-${selectorIndex}`">
                        <option v-for="key in selectorKeySuggestions" :key="key" :value="key" />
                      </datalist>
                    </label>
                    <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
                      Selector value
                      <input
                        v-model="selector.value"
                        data-testid="target-selector-value"
                        class="vercel-input text-sm normal-case tracking-normal"
                        placeholder="home-lab"
                        :list="`selector-value-options-${index}-${selectorIndex}`"
                      />
                      <datalist :id="`selector-value-options-${index}-${selectorIndex}`">
                        <option v-for="value in selectorValueSuggestions(selector.key)" :key="value" :value="value" />
                      </datalist>
                    </label>
                    <DangerButton type="button" size="small" aria-label="Remove selector label" title="Remove selector label" class="self-end" @click="removeSelectorLabel(target, selectorIndex)">
                      <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
                    </DangerButton>
                  </div>
                </div>
                <div class="flex flex-wrap items-center justify-between gap-3">
                  <SecondaryButton type="button" size="small" label="Add Selector" @click="addSelectorLabel(target)">
                    <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
                  </SecondaryButton>
                  <div data-testid="selector-match-preview" class="text-right text-xs leading-5">
                    <p :class="matchingAgents(target).length ? 'text-[#d4d4d8]' : 'text-[#f5c28b]'">
                      Matches {{ matchingAgents(target).length }} enabled agents; {{ connectedMatchingAgents(target).length }} connected.
                    </p>
                    <p v-if="matchingAgents(target).length" class="text-[#777]">
                      {{ matchingAgents(target).slice(0, 3).map((agent) => agentDisplayName(agent.id)).join(", ") }}
                      <span v-if="matchingAgents(target).length > 3">+{{ matchingAgents(target).length - 3 }} more</span>
                    </p>
                    <p v-else class="text-[#9d6b35]">No enabled agents currently match this selector.</p>
                  </div>
                </div>
              </div>
              <label v-if="target.targetType === PublicRouteTargetType.PROXY" class="flex items-center gap-2 text-sm text-[#d4d4d8]">
                <input v-model="target.tlsSkipVerify" type="checkbox" />
                Skip TLS verify
              </label>
              <label v-if="target.targetType === PublicRouteTargetType.STATIC" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
                Status
                <input v-model.number="target.staticStatusCode" type="number" min="100" max="599" class="vercel-input text-sm normal-case tracking-normal" />
              </label>
              <label v-if="target.targetType === PublicRouteTargetType.STATIC" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-3">
                Body
                <textarea v-model="target.staticResponseBody" rows="3" class="vercel-input text-sm normal-case tracking-normal" />
              </label>
              <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
                <input v-model="target.enabled" type="checkbox" />
                Enabled
              </label>
            </div>
            <DangerButton type="button" size="small" aria-label="Remove target" title="Remove target" @click="removeTarget(index)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
      </section>

      <div class="mt-2 flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="close" />
        <DisabledHint :disabled="routeSubmitDisabled" :reason="routeSubmitDisabledReason">
          <Button :label="submitLabel" type="submit" :disabled="routeSubmitDisabled" />
        </DisabledHint>
      </div>
    </form>
  </Modal>
</template>
