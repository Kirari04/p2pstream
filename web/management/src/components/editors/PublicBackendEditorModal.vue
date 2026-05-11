<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
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
  PublicBackendForwardMode,
  PublicBackendLoadBalancing,
  PublicBackendType,
  type GetPublicProxyConfigResponse,
  type PublicBackend,
} from "@/gen/proto/p2pstream/v1/management_pb";

type Runner = (action: () => Promise<void>) => Promise<boolean>;
type StaticHeaderForm = { name: string; value: string };
type UpstreamHeaderForm = { id: string; name: string; value: string; sensitive: boolean };
type BackendAgentForm = { agentId: string; weight: number; enabled: boolean };

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject<Runner>("runManagementAction");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const isOpen = ref(false);
const backends = computed(() => props.config?.backends ?? []);
const agents = computed(() => props.config?.agents ?? []);
const backendAgents = computed(() => props.config?.backendAgents ?? []);

const backendForm = reactive({
  id: "",
  name: "",
  backendType: PublicBackendType.PROXY_FORWARD,
  forwardMode: PublicBackendForwardMode.DIRECT,
  loadBalancing: PublicBackendLoadBalancing.ROUND_ROBIN,
  targetOrigin: "",
  tlsVerify: true,
  upstreamBasicAuthEnabled: false,
  upstreamBasicAuthUsername: "",
  upstreamBasicAuthPassword: "",
  upstreamBasicAuthPasswordSaved: false,
  upstreamRequestHeaders: [] as UpstreamHeaderForm[],
  agentAssignments: [] as BackendAgentForm[],
  staticStatusCode: 200,
  staticResponseHeaders: [] as StaticHeaderForm[],
  staticResponseBody: "",
  enabled: true,
});

const backendSubmitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (backendForm.backendType !== PublicBackendType.PROXY_FORWARD) return "";
  if (backendForm.forwardMode !== PublicBackendForwardMode.AGENT_POOL) return "";
  return backendForm.agentAssignments.some((assignment) => assignment.enabled && assignment.agentId)
    ? ""
    : "Assign at least one enabled agent.";
});
const backendSubmitDisabled = computed(() => Boolean(backendSubmitDisabledReason.value));
const addBackendAgentDisabledReason = computed(() => {
  if (!agents.value.length) return "Create an agent before assigning one to this backend.";
  if (backendForm.agentAssignments.length >= agents.value.length) return "All registered agents are already assigned.";
  return "";
});

function assignmentsForBackend(backend: PublicBackend) {
  if (backend.agentAssignments.length) return backend.agentAssignments;
  return backendAgents.value.filter((assignment) => assignment.backendId === backend.id);
}

function resetForm() {
  backendForm.id = "";
  backendForm.name = "";
  backendForm.backendType = PublicBackendType.PROXY_FORWARD;
  backendForm.forwardMode = PublicBackendForwardMode.DIRECT;
  backendForm.loadBalancing = PublicBackendLoadBalancing.ROUND_ROBIN;
  backendForm.targetOrigin = "";
  backendForm.tlsVerify = true;
  backendForm.upstreamBasicAuthEnabled = false;
  backendForm.upstreamBasicAuthUsername = "";
  backendForm.upstreamBasicAuthPassword = "";
  backendForm.upstreamBasicAuthPasswordSaved = false;
  backendForm.upstreamRequestHeaders = [];
  backendForm.agentAssignments = [];
  backendForm.staticStatusCode = 200;
  backendForm.staticResponseHeaders = [];
  backendForm.staticResponseBody = "";
  backendForm.enabled = true;
}

function openCreate() {
  resetForm();
  isOpen.value = true;
}

function openEdit(backendId: bigint | string) {
  const id = backendId.toString();
  const backend = backends.value.find((item) => item.id.toString() === id);
  if (!backend) return;
  backendForm.id = backend.id.toString();
  backendForm.name = backend.name;
  backendForm.backendType = backend.backendType || PublicBackendType.PROXY_FORWARD;
  backendForm.forwardMode = backend.forwardMode || PublicBackendForwardMode.DIRECT;
  backendForm.loadBalancing = backend.loadBalancing || PublicBackendLoadBalancing.ROUND_ROBIN;
  backendForm.targetOrigin = backend.targetOrigin;
  backendForm.tlsVerify = !backend.tlsSkipVerify;
  backendForm.upstreamBasicAuthEnabled = backend.upstreamBasicAuth?.enabled ?? false;
  backendForm.upstreamBasicAuthUsername = backend.upstreamBasicAuth?.username ?? "";
  backendForm.upstreamBasicAuthPassword = "";
  backendForm.upstreamBasicAuthPasswordSaved = backend.upstreamBasicAuth?.passwordSet ?? false;
  backendForm.upstreamRequestHeaders = backend.upstreamRequestHeaders.map((header) => ({
    id: header.id.toString(),
    name: header.name,
    value: header.sensitive ? "" : header.value,
    sensitive: header.sensitive,
  }));
  backendForm.agentAssignments = assignmentsForBackend(backend).map((assignment) => ({
    agentId: assignment.agentId.toString(),
    weight: Number(assignment.weight || 100n),
    enabled: assignment.enabled,
  }));
  backendForm.staticStatusCode = Number(backend.staticStatusCode || 200n);
  backendForm.staticResponseHeaders = backend.staticResponseHeaders.map((header) => ({
    name: header.name,
    value: header.value,
  }));
  backendForm.staticResponseBody = backend.staticResponseBody;
  backendForm.enabled = backend.enabled;
  isOpen.value = true;
}

function close() {
  isOpen.value = false;
}

function addStaticHeader() {
  backendForm.staticResponseHeaders.push({ name: "", value: "" });
}

function removeStaticHeader(index: number) {
  backendForm.staticResponseHeaders.splice(index, 1);
}

function addUpstreamHeader() {
  backendForm.upstreamRequestHeaders.push({ id: "", name: "", value: "", sensitive: false });
}

function removeUpstreamHeader(index: number) {
  backendForm.upstreamRequestHeaders.splice(index, 1);
}

function addBackendAgentAssignment() {
  const selected = new Set(backendForm.agentAssignments.map((assignment) => assignment.agentId));
  const next = agents.value.find((agent) => !selected.has(agent.id.toString()));
  if (!next) return;
  backendForm.agentAssignments.push({ agentId: next.id.toString(), weight: 100, enabled: true });
}

function removeBackendAgentAssignment(index: number) {
  backendForm.agentAssignments.splice(index, 1);
}

async function run(action: () => Promise<void>): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action);
}

async function submitBackend() {
  const ok = await run(async () => {
    const isStatic = backendForm.backendType === PublicBackendType.STATIC;
    const usesAgents = !isStatic && backendForm.forwardMode === PublicBackendForwardMode.AGENT_POOL;
    const payload = {
      name: backendForm.name,
      targetOrigin: isStatic ? "" : backendForm.targetOrigin,
      enabled: backendForm.enabled,
      backendType: backendForm.backendType,
      forwardMode: isStatic ? PublicBackendForwardMode.DIRECT : backendForm.forwardMode,
      loadBalancing: usesAgents ? backendForm.loadBalancing : PublicBackendLoadBalancing.ROUND_ROBIN,
      agentAssignments: usesAgents
        ? backendForm.agentAssignments.map((assignment, index) => ({
          backendId: 0n,
          agentId: BigInt(assignment.agentId || "0"),
          position: BigInt(index),
          weight: BigInt(assignment.weight || 100),
          enabled: assignment.enabled,
        }))
        : [],
      tlsSkipVerify: !isStatic && !backendForm.tlsVerify,
      upstreamRequestHeaders: isStatic
        ? []
        : backendForm.upstreamRequestHeaders.map((header, index) => ({
          id: BigInt(header.id || "0"),
          backendId: BigInt(backendForm.id || "0"),
          name: header.name,
          value: header.value,
          sensitive: header.sensitive,
          valueSet: !header.sensitive || header.value !== "" || !header.id,
          position: BigInt(index),
        })),
      upstreamBasicAuth: isStatic
        ? { enabled: false, username: "", password: "", passwordSet: false }
        : {
          enabled: backendForm.upstreamBasicAuthEnabled,
          username: backendForm.upstreamBasicAuthUsername,
          password: backendForm.upstreamBasicAuthPassword,
          passwordSet: backendForm.upstreamBasicAuthEnabled && (backendForm.upstreamBasicAuthPassword !== "" || !backendForm.upstreamBasicAuthPasswordSaved),
        },
      staticStatusCode: BigInt(isStatic ? backendForm.staticStatusCode || 200 : 200),
      staticResponseHeaders: isStatic
        ? backendForm.staticResponseHeaders.map((header) => ({ name: header.name, value: header.value }))
        : [],
      staticResponseBody: isStatic ? backendForm.staticResponseBody : "",
    };
    if (backendForm.id) {
      await managementClient.updatePublicBackend({
        id: BigInt(backendForm.id),
        ...payload,
      });
    } else {
      await managementClient.createPublicBackend(payload);
    }
  });
  if (ok) {
    isOpen.value = false;
    emit("saved");
  }
}

defineExpose({ openCreate, openEdit, close });
</script>

<template>
  <Modal v-model="isOpen" :title="backendForm.id ? 'Edit Backend' : 'Add Backend'" max-width="36rem">
    <form @submit.prevent="submitBackend" class="grid gap-4">
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Name
        <input v-model="backendForm.name" class="vercel-input text-sm normal-case tracking-normal" required />
      </label>
      <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Type
        <div class="grid grid-cols-2 rounded-md border border-[#333] bg-[#0b0b0b] p-1">
          <button
            type="button"
            class="rounded px-3 py-2 text-sm font-medium normal-case tracking-normal transition"
            :class="backendForm.backendType === PublicBackendType.PROXY_FORWARD ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
            @click="backendForm.backendType = PublicBackendType.PROXY_FORWARD"
          >
            Proxy forward
          </button>
          <button
            type="button"
            class="rounded px-3 py-2 text-sm font-medium normal-case tracking-normal transition"
            :class="backendForm.backendType === PublicBackendType.STATIC ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
            @click="backendForm.backendType = PublicBackendType.STATIC"
          >
            Static
          </button>
        </div>
      </div>
      <template v-if="backendForm.backendType === PublicBackendType.PROXY_FORWARD">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Target origin
          <input v-model="backendForm.targetOrigin" class="vercel-input text-sm normal-case tracking-normal" placeholder="https://example.com" required />
        </label>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
          <input v-model="backendForm.tlsVerify" type="checkbox" class="h-4 w-4 accent-white" />
          Verify upstream TLS certificate
        </label>
        <div class="grid gap-3 rounded-md border border-[#333] bg-[#0b0b0b] p-3">
          <div class="flex items-center justify-between gap-3">
            <p class="text-xs font-medium uppercase tracking-wider text-[#888]">Upstream request</p>
            <SecondaryButton size="small" label="Add Header" type="button" @click="addUpstreamHeader" />
          </div>
          <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
            <input v-model="backendForm.upstreamBasicAuthEnabled" type="checkbox" class="h-4 w-4 accent-white" />
            Basic Auth
          </label>
          <div v-if="backendForm.upstreamBasicAuthEnabled" class="grid gap-2 sm:grid-cols-2">
            <input v-model="backendForm.upstreamBasicAuthUsername" class="vercel-input text-sm normal-case tracking-normal" placeholder="Username" autocomplete="off" />
            <input
              v-model="backendForm.upstreamBasicAuthPassword"
              class="vercel-input text-sm normal-case tracking-normal"
              :placeholder="backendForm.upstreamBasicAuthPasswordSaved ? 'Saved password' : 'Password'"
              type="password"
              autocomplete="new-password"
            />
          </div>
          <div v-for="(header, index) in backendForm.upstreamRequestHeaders" :key="index" class="grid gap-2 sm:grid-cols-[1fr_1fr_6rem_auto]">
            <input v-model="header.name" class="vercel-input text-sm normal-case tracking-normal" placeholder="X-Upstream-Header" />
            <input
              v-model="header.value"
              class="vercel-input text-sm normal-case tracking-normal"
              :placeholder="header.sensitive && header.id ? 'Saved value' : 'Value'"
              :type="header.sensitive ? 'password' : 'text'"
              autocomplete="off"
            />
            <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
              <input v-model="header.sensitive" type="checkbox" class="h-4 w-4 accent-white" />
              Secret
            </label>
            <DangerButton size="small" aria-label="Remove upstream header" title="Remove upstream header" type="button" @click="removeUpstreamHeader(index)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
        <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Forwarding
          <div class="grid grid-cols-2 rounded-md border border-[#333] bg-[#0b0b0b] p-1">
            <button
              type="button"
              class="rounded px-3 py-2 text-sm font-medium normal-case tracking-normal transition"
              :class="backendForm.forwardMode === PublicBackendForwardMode.DIRECT ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
              @click="backendForm.forwardMode = PublicBackendForwardMode.DIRECT"
            >
              Direct
            </button>
            <button
              type="button"
              class="rounded px-3 py-2 text-sm font-medium normal-case tracking-normal transition"
              :class="backendForm.forwardMode === PublicBackendForwardMode.AGENT_POOL ? 'bg-white text-black' : 'text-[#d4d4d8] hover:bg-[#1f1f1f]'"
              @click="backendForm.forwardMode = PublicBackendForwardMode.AGENT_POOL"
            >
              Agents
            </button>
          </div>
        </div>
        <template v-if="backendForm.forwardMode === PublicBackendForwardMode.AGENT_POOL">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Load balancing
            <select v-model="backendForm.loadBalancing" class="vercel-input text-sm normal-case tracking-normal">
              <option :value="PublicBackendLoadBalancing.ROUND_ROBIN">Round-robin</option>
              <option :value="PublicBackendLoadBalancing.WEIGHTED_ROUND_ROBIN">Weighted round-robin</option>
              <option :value="PublicBackendLoadBalancing.RANDOM">Random</option>
              <option :value="PublicBackendLoadBalancing.WEIGHTED_RANDOM">Weighted random</option>
              <option :value="PublicBackendLoadBalancing.LEAST_ACTIVE_REQUESTS">Least active</option>
              <option :value="PublicBackendLoadBalancing.WEIGHTED_LEAST_ACTIVE_REQUESTS">Weighted least active</option>
            </select>
          </label>
          <div class="grid gap-2">
            <div class="flex items-center justify-between gap-3">
              <p class="text-xs font-medium uppercase tracking-wider text-[#888]">Agents</p>
              <DisabledHint :disabled="Boolean(addBackendAgentDisabledReason)" :reason="addBackendAgentDisabledReason">
                <SecondaryButton
                  size="small"
                  label="Add Agent"
                  type="button"
                  :disabled="Boolean(addBackendAgentDisabledReason)"
                  @click="addBackendAgentAssignment"
                />
              </DisabledHint>
            </div>
            <div v-if="!agents.length" class="rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-2 text-xs text-[#888]">
              Create an agent in Agent Health before using agent forwarding.
            </div>
            <div v-for="(assignment, index) in backendForm.agentAssignments" :key="index" class="grid gap-2 sm:grid-cols-[1fr_6rem_5rem_auto]">
              <select v-model="assignment.agentId" class="vercel-input text-sm normal-case tracking-normal">
                <option v-for="agent in agents" :key="agent.id.toString()" :value="agent.id.toString()">
                  {{ agent.name }} ({{ agent.publicId }})
                </option>
              </select>
              <input v-model.number="assignment.weight" type="number" min="1" max="1000" class="vercel-input text-sm normal-case tracking-normal" />
              <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
                <input v-model="assignment.enabled" type="checkbox" class="h-4 w-4 accent-white" />
                On
              </label>
              <DangerButton size="small" aria-label="Remove agent" title="Remove agent" type="button" @click="removeBackendAgentAssignment(index)">
                <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
              </DangerButton>
            </div>
          </div>
        </template>
      </template>
      <template v-else>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Status
          <input v-model.number="backendForm.staticStatusCode" type="number" min="100" max="599" class="vercel-input text-sm normal-case tracking-normal" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Body
          <textarea v-model="backendForm.staticResponseBody" class="vercel-input min-h-28 resize-y text-sm normal-case tracking-normal" />
        </label>
        <div class="grid gap-2">
          <div class="flex items-center justify-between gap-3">
            <p class="text-xs font-medium uppercase tracking-wider text-[#888]">Headers</p>
            <SecondaryButton size="small" label="Add Header" type="button" @click="addStaticHeader" />
          </div>
          <div v-for="(header, index) in backendForm.staticResponseHeaders" :key="index" class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
            <input v-model="header.name" class="vercel-input text-sm normal-case tracking-normal" placeholder="Content-Type" />
            <input v-model="header.value" class="vercel-input text-sm normal-case tracking-normal" placeholder="text/plain" />
            <DangerButton size="small" aria-label="Remove header" title="Remove header" type="button" @click="removeStaticHeader(index)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
      </template>
      <label class="flex items-center gap-2 text-sm text-[#d4d4d8] mt-2">
        <input v-model="backendForm.enabled" type="checkbox" class="h-4 w-4 accent-white" />
        Enabled
      </label>
      <div class="mt-4 flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="close" />
        <DisabledHint :disabled="Boolean(backendSubmitDisabledReason)" :reason="backendSubmitDisabledReason">
          <Button class="!bg-white !text-black !border-white" :label="backendForm.id ? 'Save Changes' : 'Create Backend'" type="submit" :disabled="backendSubmitDisabled" />
        </DisabledHint>
      </div>
    </form>
  </Modal>
</template>
