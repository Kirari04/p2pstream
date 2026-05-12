<script setup lang="ts">
import { computed, inject, reactive, ref, watch } from "vue";
import type { ComputedRef } from "vue";
import TrashIcon from "@primevue/icons/trash";
import { managementClient } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import {
  PublicBackendLoadBalancing,
  PublicRouteAction,
  PublicRouteRedirectTargetMode,
  type GetPublicProxyConfigResponse,
  type PublicRoute,
} from "@/gen/proto/p2pstream/v1/management_pb";

type Runner = (action: () => Promise<void>) => Promise<boolean>;
type RouteBackendAssignmentForm = { backendId: string; weight: number; enabled: boolean };

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject<Runner>("runManagementAction");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const isOpen = ref(false);
const listeners = computed(() => props.config?.listeners ?? []);
const backends = computed(() => props.config?.backends ?? []);
const routeBackends = computed(() => props.config?.routeBackends ?? []);
const routes = computed(() => props.config?.routes ?? []);

const routeForm = reactive({
  id: "",
  listenerId: "",
  action: PublicRouteAction.FORWARD,
  priority: 100,
  hostPattern: "",
  pathPrefix: "",
  backendId: "",
  loadBalancing: PublicBackendLoadBalancing.ROUND_ROBIN,
  backendAssignments: [] as RouteBackendAssignmentForm[],
  fallbackBackendId: "",
  redirectTargetMode: PublicRouteRedirectTargetMode.SAME_HOST_PATH,
  redirectTarget: "",
  redirectStatusCode: 302,
  redirectPreservePathSuffix: true,
  redirectPreserveQuery: true,
  enabled: true,
});

const routeIsRedirect = computed(() => routeForm.action === PublicRouteAction.REDIRECT);
const routeSubmitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!listeners.value.length) return "Create a listener before creating a route.";
  if (routeIsRedirect.value && routeForm.redirectTarget.trim() === "") return "Enter a redirect target.";
  if (!routeIsRedirect.value && !backends.value.length) return "Create a backend before creating a forwarding route.";
  if (!routeIsRedirect.value && !routeForm.backendAssignments.length) return "Select at least one backend.";
  return "";
});
const routeSubmitDisabled = computed(() => Boolean(routeSubmitDisabledReason.value));
const addRouteBackendDisabledReason = computed(() => {
  if (!backends.value.length) return "Create a backend before assigning one to this route.";
  if (routeForm.backendAssignments.length >= backends.value.length) return "All backends are already assigned.";
  return "";
});

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

function resetForm() {
  routeForm.id = "";
  routeForm.listenerId = listeners.value[0]?.id.toString() ?? "";
  routeForm.action = PublicRouteAction.FORWARD;
  routeForm.priority = 100;
  routeForm.hostPattern = "";
  routeForm.pathPrefix = "";
  routeForm.backendId = backends.value[0]?.id.toString() ?? "";
  routeForm.loadBalancing = PublicBackendLoadBalancing.ROUND_ROBIN;
  routeForm.backendAssignments = backends.value[0] ? [{ backendId: backends.value[0].id.toString(), weight: 100, enabled: true }] : [];
  routeForm.fallbackBackendId = "";
  routeForm.redirectTargetMode = PublicRouteRedirectTargetMode.SAME_HOST_PATH;
  routeForm.redirectTarget = "";
  routeForm.redirectStatusCode = 302;
  routeForm.redirectPreservePathSuffix = true;
  routeForm.redirectPreserveQuery = true;
  routeForm.enabled = true;
}

function assignmentsForRoute(route: PublicRoute): RouteBackendAssignmentForm[] {
  const assignments = route.backendAssignments.length
    ? route.backendAssignments
    : routeBackends.value.filter((assignment) => assignment.routeId === route.id);
  if (!assignments.length && route.backendId > 0n) {
    return [{ backendId: route.backendId.toString(), weight: 100, enabled: true }];
  }
  return assignments.map((assignment) => ({
    backendId: assignment.backendId.toString(),
    weight: Number(assignment.weight || 100n),
    enabled: assignment.enabled,
  }));
}

function openCreate() {
  resetForm();
  isOpen.value = true;
}

function openEdit(routeId: bigint | string) {
  const id = routeId.toString();
  const route = routes.value.find((item) => item.id.toString() === id);
  if (!route) return;
  routeForm.id = route.id.toString();
  routeForm.listenerId = route.listenerId.toString();
  routeForm.action = routeAction(route);
  routeForm.priority = Number(route.priority);
  routeForm.hostPattern = route.hostPattern;
  routeForm.pathPrefix = route.pathPrefix;
  routeForm.backendId = route.backendId > 0n ? route.backendId.toString() : backends.value[0]?.id.toString() ?? "";
  routeForm.loadBalancing = route.loadBalancing || PublicBackendLoadBalancing.ROUND_ROBIN;
  routeForm.backendAssignments = assignmentsForRoute(route);
  routeForm.fallbackBackendId = route.fallbackBackendId > 0n ? route.fallbackBackendId.toString() : "";
  routeForm.redirectTargetMode = route.redirectTargetMode || PublicRouteRedirectTargetMode.SAME_HOST_PATH;
  routeForm.redirectTarget = route.redirectTarget;
  routeForm.redirectStatusCode = Number(route.redirectStatusCode || 302);
  routeForm.redirectPreservePathSuffix = route.redirectPreservePathSuffix;
  routeForm.redirectPreserveQuery = route.redirectPreserveQuery;
  routeForm.enabled = route.enabled;
  isOpen.value = true;
}

function addRouteBackendAssignment() {
  const selected = new Set(routeForm.backendAssignments.map((assignment) => assignment.backendId));
  const next = backends.value.find((backend) => !selected.has(backend.id.toString()));
  if (!next) return;
  routeForm.backendAssignments.push({ backendId: next.id.toString(), weight: 100, enabled: true });
}

function removeRouteBackendAssignment(index: number) {
  routeForm.backendAssignments.splice(index, 1);
}

function close() {
  isOpen.value = false;
}

async function run(action: () => Promise<void>): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action);
}

async function submitRoute() {
  const ok = await run(async () => {
    const isRedirect = routeForm.action === PublicRouteAction.REDIRECT;
    const assignments = routeForm.backendAssignments.map((assignment, index) => ({
      routeId: BigInt(routeForm.id || "0"),
      backendId: BigInt(assignment.backendId || "0"),
      position: BigInt(index),
      weight: BigInt(assignment.weight || 100),
      enabled: assignment.enabled,
    }));
    const payload = {
      listenerId: BigInt(routeForm.listenerId || "0"),
      priority: BigInt(routeForm.priority),
      hostPattern: routeForm.hostPattern,
      pathPrefix: routeForm.pathPrefix,
      backendId: isRedirect ? 0n : (assignments[0]?.backendId ?? BigInt(routeForm.backendId || "0")),
      loadBalancing: isRedirect ? PublicBackendLoadBalancing.ROUND_ROBIN : routeForm.loadBalancing,
      backendAssignments: isRedirect ? [] : assignments,
      fallbackBackendId: isRedirect ? 0n : BigInt(routeForm.fallbackBackendId || "0"),
      action: routeForm.action,
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

watch(backends, () => {
  if (!routeForm.backendId && backends.value[0]) {
    routeForm.backendId = backends.value[0].id.toString();
  }
  if (!routeForm.backendAssignments.length && backends.value[0]) {
    routeForm.backendAssignments = [{ backendId: backends.value[0].id.toString(), weight: 100, enabled: true }];
  }
}, { immediate: true });

watch(listeners, () => {
  if (!routeForm.listenerId && listeners.value[0]) {
    routeForm.listenerId = listeners.value[0].id.toString();
  }
}, { immediate: true });

watch(
  () => routeForm.action,
  (action) => {
    if (action === PublicRouteAction.FORWARD && !routeForm.backendId && backends.value[0]) {
      routeForm.backendId = backends.value[0].id.toString();
    }
    if (action === PublicRouteAction.FORWARD && !routeForm.backendAssignments.length && backends.value[0]) {
      routeForm.backendAssignments = [{ backendId: backends.value[0].id.toString(), weight: 100, enabled: true }];
    }
  },
);

watch(
  () => routeForm.redirectTargetMode,
  (mode) => {
    if (mode === PublicRouteRedirectTargetMode.EXTERNAL_ORIGIN_KEEP_PATH) {
      routeForm.redirectPreservePathSuffix = false;
    }
  },
);

defineExpose({ openCreate, openEdit, close });
</script>

<template>
  <Modal v-model="isOpen" :title="routeForm.id ? 'Edit Route' : 'Add Route'" max-width="42rem">
    <form @submit.prevent="submitRoute" class="grid gap-4 sm:grid-cols-2">
      <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
        Action
        <div class="grid grid-cols-2 rounded-md border border-[#333] bg-[#0b0b0b] p-1">
          <button
            type="button"
            class="rounded px-3 py-2 text-sm font-medium normal-case tracking-normal transition"
            :class="routeForm.action === PublicRouteAction.FORWARD ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
            @click="routeForm.action = PublicRouteAction.FORWARD"
          >
            Forward
          </button>
          <button
            type="button"
            class="rounded px-3 py-2 text-sm font-medium normal-case tracking-normal transition"
            :class="routeForm.action === PublicRouteAction.REDIRECT ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
            @click="routeForm.action = PublicRouteAction.REDIRECT"
          >
            Redirect
          </button>
        </div>
      </div>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Listener
        <select v-model="routeForm.listenerId" class="vercel-input text-sm normal-case tracking-normal" required>
          <option v-for="listener in listeners" :key="listener.id.toString()" :value="listener.id.toString()">{{ listener.name }}</option>
        </select>
      </label>
      <label v-if="!routeIsRedirect" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Load balancing
        <select v-model="routeForm.loadBalancing" class="vercel-input text-sm normal-case tracking-normal" required>
          <option :value="PublicBackendLoadBalancing.ROUND_ROBIN">Round-robin</option>
          <option :value="PublicBackendLoadBalancing.WEIGHTED_ROUND_ROBIN">Weighted round-robin</option>
          <option :value="PublicBackendLoadBalancing.RANDOM">Random</option>
          <option :value="PublicBackendLoadBalancing.WEIGHTED_RANDOM">Weighted random</option>
          <option :value="PublicBackendLoadBalancing.LEAST_ACTIVE_REQUESTS">Least active</option>
          <option :value="PublicBackendLoadBalancing.WEIGHTED_LEAST_ACTIVE_REQUESTS">Weighted least active</option>
        </select>
      </label>
      <label v-else class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Status
        <select v-model.number="routeForm.redirectStatusCode" class="vercel-input text-sm normal-case tracking-normal" required>
          <option :value="302">302 Found</option>
          <option :value="301">301 Moved</option>
          <option :value="307">307 Temporary</option>
          <option :value="308">308 Permanent</option>
        </select>
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Priority
        <input v-model.number="routeForm.priority" type="number" class="vercel-input text-sm normal-case tracking-normal" required />
        <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Lower values are evaluated first.</p>
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Host pattern
        <input v-model="routeForm.hostPattern" class="vercel-input text-sm normal-case tracking-normal" placeholder="*.example.com" />
        <p class="text-xs font-normal normal-case tracking-normal text-[#666]">Supports wildcards, e.g. *.example.com</p>
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
        Path prefix
        <input v-model="routeForm.pathPrefix" class="vercel-input text-sm normal-case tracking-normal" placeholder="/api" />
      </label>
      <template v-if="!routeIsRedirect">
        <div class="grid gap-3 rounded-md border border-[#333] bg-[#0b0b0b] p-3 sm:col-span-2">
          <div class="flex items-center justify-between gap-3">
            <p class="text-xs font-medium uppercase tracking-wider text-[#888]">Backends</p>
            <DisabledHint :disabled="Boolean(addRouteBackendDisabledReason)" :reason="addRouteBackendDisabledReason">
              <SecondaryButton
                size="small"
                label="Add Backend"
                type="button"
                :disabled="Boolean(addRouteBackendDisabledReason)"
                @click="addRouteBackendAssignment"
              />
            </DisabledHint>
          </div>
          <div v-if="!backends.length" class="rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-2 text-xs text-[#888]">
            Create a backend before using route forwarding.
          </div>
          <div v-for="(assignment, index) in routeForm.backendAssignments" :key="index" class="grid gap-2 sm:grid-cols-[1fr_6rem_5rem_auto]">
            <select v-model="assignment.backendId" class="vercel-input text-sm normal-case tracking-normal" required>
              <option v-for="backend in backends" :key="backend.id.toString()" :value="backend.id.toString()">{{ backend.name }}</option>
            </select>
            <input v-model.number="assignment.weight" type="number" min="1" max="1000" class="vercel-input text-sm normal-case tracking-normal" />
            <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
              <input v-model="assignment.enabled" type="checkbox" />
              On
            </label>
            <DangerButton size="small" aria-label="Remove route backend" title="Remove route backend" type="button" @click="removeRouteBackendAssignment(index)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
          Fallback backend
          <select v-model="routeForm.fallbackBackendId" class="vercel-input text-sm normal-case tracking-normal">
            <option value="">No fallback</option>
            <option v-for="backend in backends" :key="backend.id.toString()" :value="backend.id.toString()">{{ backend.name }}</option>
          </select>
        </label>
      </template>
      <template v-if="routeIsRedirect">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
          Redirect mode
          <select v-model="routeForm.redirectTargetMode" class="vercel-input text-sm normal-case tracking-normal" required>
            <option :value="PublicRouteRedirectTargetMode.SAME_HOST_PATH">Same host path</option>
            <option :value="PublicRouteRedirectTargetMode.EXTERNAL_ORIGIN_KEEP_PATH">External origin, keep path</option>
            <option :value="PublicRouteRedirectTargetMode.ABSOLUTE_URL">Absolute URL</option>
          </select>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
          Redirect target
          <input
            v-model="routeForm.redirectTarget"
            class="vercel-input text-sm normal-case tracking-normal"
            :placeholder="redirectTargetPlaceholder(routeForm.redirectTargetMode)"
            required
          />
        </label>
        <div class="grid gap-3 rounded-md border border-[#333] bg-[#0b0b0b] p-3 sm:col-span-2">
          <label
            v-if="routeForm.redirectTargetMode !== PublicRouteRedirectTargetMode.EXTERNAL_ORIGIN_KEEP_PATH"
            class="flex items-center gap-2 text-sm text-[#d4d4d8]"
          >
            <input v-model="routeForm.redirectPreservePathSuffix" type="checkbox" />
            Append matched path suffix
          </label>
          <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
            <input v-model="routeForm.redirectPreserveQuery" type="checkbox" />
            Preserve query string
          </label>
        </div>
      </template>
      <label class="flex items-center gap-2 text-sm text-[#d4d4d8] sm:col-span-2 mt-2">
        <input v-model="routeForm.enabled" type="checkbox" />
        Enabled
      </label>
      <div class="sm:col-span-2 mt-4 flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="close" />
        <DisabledHint :disabled="Boolean(routeSubmitDisabledReason)" :reason="routeSubmitDisabledReason">
          <Button :label="routeForm.id ? 'Save Changes' : 'Create Route'" type="submit" :disabled="routeSubmitDisabled" />
        </DisabledHint>
      </div>
    </form>
  </Modal>
</template>
