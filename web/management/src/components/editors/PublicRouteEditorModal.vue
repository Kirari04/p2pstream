<script setup lang="ts">
import { computed, inject, reactive, ref, watch } from "vue";
import type { ComputedRef } from "vue";
import { managementClient } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import {
  PublicRouteAction,
  PublicRouteRedirectTargetMode,
  type GetPublicProxyConfigResponse,
  type PublicRoute,
} from "@/gen/proto/p2pstream/v1/management_pb";

type Runner = (action: () => Promise<void>) => Promise<boolean>;

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
const routes = computed(() => props.config?.routes ?? []);

const routeForm = reactive({
  id: "",
  listenerId: "",
  action: PublicRouteAction.FORWARD,
  priority: 100,
  hostPattern: "",
  pathPrefix: "",
  backendId: "",
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
  if (!routeIsRedirect.value && !routeForm.backendId) return "Select a backend.";
  return "";
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

function resetForm() {
  routeForm.id = "";
  routeForm.listenerId = listeners.value[0]?.id.toString() ?? "";
  routeForm.action = PublicRouteAction.FORWARD;
  routeForm.priority = 100;
  routeForm.hostPattern = "";
  routeForm.pathPrefix = "";
  routeForm.backendId = backends.value[0]?.id.toString() ?? "";
  routeForm.redirectTargetMode = PublicRouteRedirectTargetMode.SAME_HOST_PATH;
  routeForm.redirectTarget = "";
  routeForm.redirectStatusCode = 302;
  routeForm.redirectPreservePathSuffix = true;
  routeForm.redirectPreserveQuery = true;
  routeForm.enabled = true;
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
  routeForm.redirectTargetMode = route.redirectTargetMode || PublicRouteRedirectTargetMode.SAME_HOST_PATH;
  routeForm.redirectTarget = route.redirectTarget;
  routeForm.redirectStatusCode = Number(route.redirectStatusCode || 302);
  routeForm.redirectPreservePathSuffix = route.redirectPreservePathSuffix;
  routeForm.redirectPreserveQuery = route.redirectPreserveQuery;
  routeForm.enabled = route.enabled;
  isOpen.value = true;
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
    const payload = {
      listenerId: BigInt(routeForm.listenerId || "0"),
      priority: BigInt(routeForm.priority),
      hostPattern: routeForm.hostPattern,
      pathPrefix: routeForm.pathPrefix,
      backendId: isRedirect ? 0n : BigInt(routeForm.backendId || "0"),
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
  <Modal v-model="isOpen" :title="routeForm.id ? 'Edit Route' : 'Add Route'" max-width="36rem">
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
        Backend
        <select v-model="routeForm.backendId" class="vercel-input text-sm normal-case tracking-normal" required>
          <option v-for="backend in backends" :key="backend.id.toString()" :value="backend.id.toString()">{{ backend.name }}</option>
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
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Host pattern
        <input v-model="routeForm.hostPattern" class="vercel-input text-sm normal-case tracking-normal" placeholder="*.example.com" />
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
        Path prefix
        <input v-model="routeForm.pathPrefix" class="vercel-input text-sm normal-case tracking-normal" placeholder="/api" />
      </label>
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
            <input v-model="routeForm.redirectPreservePathSuffix" type="checkbox" class="h-4 w-4 accent-white" />
            Append matched path suffix
          </label>
          <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
            <input v-model="routeForm.redirectPreserveQuery" type="checkbox" class="h-4 w-4 accent-white" />
            Preserve query string
          </label>
        </div>
      </template>
      <label class="flex items-center gap-2 text-sm text-[#d4d4d8] sm:col-span-2 mt-2">
        <input v-model="routeForm.enabled" type="checkbox" class="h-4 w-4 accent-white" />
        Enabled
      </label>
      <div class="sm:col-span-2 mt-4 flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="close" />
        <DisabledHint :disabled="Boolean(routeSubmitDisabledReason)" :reason="routeSubmitDisabledReason">
          <Button class="!bg-white !text-black !border-white" :label="routeForm.id ? 'Save Changes' : 'Create Route'" type="submit" :disabled="routeSubmitDisabled" />
        </DisabledHint>
      </div>
    </form>
  </Modal>
</template>
