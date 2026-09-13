<script setup lang="ts">
import { computed, inject, onMounted, onUnmounted, ref, watch } from "vue";
import { NAlert, NButton, NCard, NInput, NModal, NTag } from "naive-ui";
import { ArrowUpRight, Check, Copy, Download, RefreshCw, ShieldCheck } from "@lucide/vue";
import { selectedEnvironmentIdKey, selectedEnvironmentLabelKey, selectedEnvironmentBlockedKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import { createAgentUpdatePolling } from "@/lib/agentUpdatePolling";
import { messageFromError } from "@/lib/errors";
import { parsePendingServerUpdate, serverUpdatePhaseLabel, serverUpdateTerminal, type PendingServerUpdate } from "@/lib/serverUpdates";
import type { GetServerUpdateOverviewResponse, PreviewServerUpdateResponse } from "@/gen/proto/p2pstream/v1/management_pb";

const client = useManagementClient();
const environmentId = inject(selectedEnvironmentIdKey, computed(() => "0"));
const environmentLabel = inject(selectedEnvironmentLabelKey, computed(() => "Local"));
const blocked = inject(selectedEnvironmentBlockedKey, computed(() => ""));
const overview = ref<GetServerUpdateOverviewResponse | null>(null);
const plan = ref<PreviewServerUpdateResponse | null>(null);
const planScope = ref("");
const targetVersion = ref("");
const busy = ref(false);
const loading = ref(false);
const error = ref("");
const copied = ref(false);
const pending = ref<PendingServerUpdate | null>(null);
const now = ref(Date.now());
let actionRevision = 0;
let disposed = false;
const scope = () => `${environmentId.value}\u0000${blocked.value}`;
const storageKey = () => `p2pstream:server-update:${environmentId.value}`;
const operation = computed(() => overview.value?.operation);
const running = computed(() => Boolean(operation.value && !serverUpdateTerminal(operation.value.phase)));
const planExpired = computed(() => !plan.value || Number(plan.value.expiresAtUnixMillis) <= now.value);
const setupCommand = "sudo ./scripts/install-server-updater.sh";
const actionIsCurrent = (revision: number, requestScope: string) => !disposed && revision === actionRevision && requestScope === scope();

function savePending(value: PendingServerUpdate | null) {
  pending.value = value;
  try { if (value) window.sessionStorage.setItem(storageKey(), JSON.stringify(value)); else window.sessionStorage.removeItem(storageKey()); } catch { /* Storage is optional; executor state remains authoritative. */ }
}
function loadPending() {
  try { pending.value = parsePendingServerUpdate(window.sessionStorage.getItem(storageKey())); } catch { pending.value = null; }
}

const polling = createAgentUpdatePolling({
  scope,
  canPoll: () => !busy.value && !blocked.value && document.visibilityState !== "hidden",
  load: (signal) => client.getServerUpdateOverview({}, { signal, timeoutMs: 90000 }),
  apply: (response) => {
    // An unavailable executor cannot contradict the last accepted operation.
    // Keep its progress visible while the independent updater reconnects.
    if (response.executorConfigured && !response.executorAvailable && !response.operation && response.instanceId === overview.value?.instanceId) {
      response.operation = overview.value.operation;
    }
    overview.value = response;
    now.value = Date.now();
    if (!targetVersion.value && response.target) targetVersion.value = response.target.version;
    if (pending.value && response.operation?.id === pending.value.operationId) savePending(null);
    error.value = "";
  },
  error: (cause) => { error.value = messageFromError(cause); },
  loading: (value) => { loading.value = value; },
});

async function preview() {
  if (!overview.value || busy.value || blocked.value) return;
  const requestScope = scope();
  const requestRevision = actionRevision;
  const instanceId = overview.value.instanceId;
  const previewUpdate = client.previewServerUpdate;
  busy.value = true;
  error.value = "";
  try {
    const result = await previewUpdate({ instanceId, targetVersion: targetVersion.value.trim() }, { timeoutMs: 90000 });
    if (!actionIsCurrent(requestRevision, requestScope)) return;
    plan.value = result;
    planScope.value = requestScope;
    now.value = Date.now();
  } catch (cause) { if (actionIsCurrent(requestRevision, requestScope)) error.value = messageFromError(cause); }
  finally { if (actionIsCurrent(requestRevision, requestScope)) busy.value = false; }
}

async function start(retry = false) {
  if (busy.value || blocked.value) return;
  let request = pending.value;
  if (!retry) {
    if (!plan.value || planExpired.value || planScope.value !== scope()) return;
    request = { instanceId: plan.value.instanceId, operationId: crypto.randomUUID(), planToken: plan.value.planToken };
    savePending(request);
  }
  if (!request) return;
  const requestScope = scope();
  const requestRevision = actionRevision;
  const startUpdate = client.startServerUpdate;
  busy.value = true;
  error.value = "";
  try {
    const result = await startUpdate(request, { timeoutMs: 90000 });
    if (!actionIsCurrent(requestRevision, requestScope)) return;
    if (overview.value && result.operation) overview.value.operation = result.operation;
    savePending(null);
    plan.value = null;
  } catch (cause) {
    if (actionIsCurrent(requestRevision, requestScope)) {
      plan.value = null;
      error.value = `The update request could not be confirmed. Check its status or retry the same request. ${messageFromError(cause)}`;
    }
  } finally {
    if (actionIsCurrent(requestRevision, requestScope)) {
      busy.value = false;
      // Discard reads begun before/during this mutation. Their snapshots may
      // predate the accepted operation even if they arrive after its response.
      void polling.scopeChanged();
    }
  }
}

async function copySetup() {
  try { await navigator.clipboard.writeText(setupCommand); copied.value = true; } catch { error.value = "Select and copy the setup command below."; }
}

watch(() => [environmentId.value, blocked.value], () => {
  actionRevision += 1;
  busy.value = false; overview.value = null; plan.value = null; targetVersion.value = ""; error.value = ""; copied.value = false;
  loadPending(); void polling.scopeChanged();
});
let clock: ReturnType<typeof setInterval>;
onMounted(() => { loadPending(); void polling.start(); clock = setInterval(() => { now.value = Date.now(); }, 1000); });
onUnmounted(() => { disposed = true; actionRevision += 1; polling.stop(); clearInterval(clock); });
</script>

<template>
  <div class="server-updates">
    <div class="update-heading">
      <div>
        <div class="update-eyebrow">SERVER SOFTWARE · {{ environmentLabel }}</div>
        <h2>Keep your server current</h2>
        <p>Verified releases, a data backup, and recovery to the previous version.</p>
      </div>
      <NButton :loading="loading" :disabled="busy || Boolean(blocked)" @click="polling.refresh()"><template #icon><RefreshCw :size="16" /></template>Refresh status</NButton>
    </div>

    <NAlert v-if="blocked" type="warning">{{ blocked }}</NAlert>
    <NAlert v-if="error" :type="running || pending ? 'info' : 'error'" :title="running || pending ? 'Reconnecting to the selected server' : 'Unable to load server updates'">{{ error }}</NAlert>
    <NAlert v-if="overview?.warning" type="warning">{{ overview.warning }}</NAlert>

    <div class="update-summary">
      <div class="update-version">
        <span class="update-eyebrow">INSTALLED ON {{ environmentLabel }}</span>
        <strong>{{ overview?.version || 'Checking…' }}</strong>
        <div class="update-meta"><NTag size="small" :bordered="false">{{ overview?.channel || 'Unknown channel' }}</NTag><span>{{ overview?.commit.slice(0, 12) }}</span></div>
      </div>
      <div class="update-policy"><ShieldCheck :size="22" /><div><strong>You choose when to update</strong><p>Unattended updates are off. The installed image stays pinned until you select another release.</p></div></div>
    </div>

    <NCard v-if="overview && !overview.executorConfigured" title="Enable one-click updates">
      <p>Run this once on <strong>{{ environmentLabel }}</strong>'s Docker host, from its Compose directory, using the scripts shipped with the installed release. Setup installs the independent updater and restarts the server once.</p>
      <div class="update-command"><code>{{ setupCommand }}</code><NButton size="small" @click="copySetup"><template #icon><Check v-if="copied" :size="16" /><Copy v-else :size="16" /></template>{{ copied ? 'Copied' : 'Copy' }}</NButton></div>
      <p class="muted-text">The updater receives Docker access. The management container keeps only a private update connection. For native or custom deployments, use the pinned release and your host deployment procedure.</p>
    </NCard>

    <NCard v-if="operation" :title="serverUpdatePhaseLabel(operation.phase)">
      <div class="update-operation"><span>{{ operation.previousVersion }}</span><ArrowUpRight :size="18" /><strong>{{ operation.targetVersion }}</strong><NTag :type="operation.phase === 'succeeded' ? 'success' : operation.phase === 'recovery_required' ? 'error' : 'default'" size="small">{{ operation.phase.replaceAll('_', ' ') }}</NTag></div>
      <p v-if="running && operation.phase !== 'recovery_required'">The updater continues on the remote host even if this page disconnects. Public traffic and agent tunnels reconnect after the server returns.</p>
      <p v-if="operation.phase === 'recovery_required'">Recovery is paused. Check the updater on the selected server’s host before retrying recovery.</p>
      <NAlert v-if="operation.detail" :type="operation.phase === 'rolled_back' ? 'warning' : 'error'">{{ operation.detail }}</NAlert>
      <small class="muted-text">Operation {{ operation.id }}</small>
    </NCard>

    <NAlert v-if="pending" type="info" title="Update request awaiting confirmation">
      Retrying uses the same operation ID, so it cannot create a second update.
      <p class="muted-text">Clearing this saved request only removes the browser’s retry record; an accepted update continues on the host.</p>
      <div class="update-modal-actions"><NButton :disabled="busy" @click="savePending(null)">Clear saved request</NButton><NButton :loading="busy" @click="start(true)">Retry same request</NButton></div>
    </NAlert>

    <NCard v-if="overview?.executorConfigured" title="Choose a release">
      <NAlert v-for="reason in overview.blockers" :key="reason" type="warning" class="update-blocker">{{ reason }}</NAlert>
      <p v-if="overview.target">A verified {{ overview.target.channel }} release is available.</p>
      <p v-else>No newer eligible release is currently reported. You can preview an exact release in this server's channel.</p>
      <div class="update-actions"><NInput v-model:value="targetVersion" aria-label="Exact server release version" placeholder="vX.Y.Z or vX.Y.Z-staging.N" :disabled="busy || running || Boolean(pending)" /><NButton type="primary" :loading="busy" :disabled="!overview.executorAvailable || !targetVersion.trim() || running || Boolean(pending) || overview.blockers.length > 0 || Boolean(blocked)" @click="preview"><template #icon><Download :size="16" /></template>Preview update</NButton></div>
      <p class="muted-text">The preview checks this server, release compatibility, and the saved deployment. Updating this server also restarts its public proxy and agent tunnels.</p>
    </NCard>

    <NModal :show="Boolean(plan)" preset="card" title="Update the selected server" :style="{ width: 'min(560px, calc(100vw - 32px))' }" :mask-closable="!busy" @update:show="(show) => { if (!show && !busy) plan = null; }">
      <template v-if="plan">
        <p><strong>{{ environmentLabel }}</strong> will update from <strong>{{ plan.currentVersion }}</strong> to <strong>{{ plan.target?.version }}</strong>.</p>
        <p>The updater will download the verified image, stop the server, back up its data, and check the replacement. If validation fails, it will restore the previous version and backup.</p>
        <NAlert type="warning">Public requests and agent tunnels will be interrupted. Long-running connections may need to be restarted.</NAlert>
        <details class="update-details"><summary>Verified release identity</summary><code>{{ plan.target?.image }}</code><code>{{ plan.target?.manifestSha256 }}</code></details>
        <NAlert v-if="planExpired" type="warning">This preview expired. Close it and preview the release again.</NAlert>
        <div class="update-modal-actions"><NButton :disabled="busy" @click="plan = null">Cancel</NButton><NButton type="primary" :loading="busy" :disabled="planExpired || planScope !== scope()" @click="start()">Update {{ environmentLabel }}</NButton></div>
      </template>
    </NModal>
  </div>
</template>

<style scoped>
.server-updates{display:grid;gap:1.25rem;max-width:1040px}
.update-heading{display:flex;justify-content:space-between;align-items:flex-start;gap:1.5rem}.update-heading h2{font-size:1.5rem;margin:.35rem 0}.update-heading p{margin:0;color:var(--app-text-muted)}
.update-eyebrow{font-size:.68rem;letter-spacing:.09em;font-weight:600;color:var(--app-text-muted)}
.update-summary{display:grid;grid-template-columns:1fr 1fr;border:1px solid var(--app-border);border-radius:8px;overflow:hidden}.update-version{padding:1.5rem;display:grid;gap:.65rem;min-width:0}.update-version>strong{font-family:'IBM Plex Mono',monospace;font-size:1.5rem;overflow-wrap:anywhere}.update-meta{display:flex;gap:.8rem;align-items:center;font-family:'IBM Plex Mono',monospace;font-size:.75rem}
.update-policy{padding:1.5rem;display:flex;gap:.8rem;align-items:flex-start;background:var(--app-panel-muted);border-left:1px solid var(--app-border)}.update-policy>svg{flex-shrink:0;color:var(--app-accent)}.update-policy p{font-size:.82rem;margin:.4rem 0 0;color:var(--app-text-muted)}
.update-command,.update-actions,.update-operation{display:flex;align-items:center;gap:.75rem}.update-command{padding:.75rem 1rem;border:1px solid var(--app-border);border-radius:6px;justify-content:space-between;margin:1rem 0}.update-command code{overflow-wrap:anywhere;min-width:0}.update-actions .n-input{max-width:380px}.update-operation{flex-wrap:wrap;margin-bottom:.75rem}.update-blocker{margin-bottom:.75rem}.update-details{margin:1rem 0}.update-details code{display:block;overflow-wrap:anywhere;font-size:.7rem;margin-top:.75rem}.update-modal-actions{display:flex;justify-content:flex-end;gap:.75rem;margin-top:1.25rem}.muted-text{font-size:.8rem}
@media(max-width:640px){.update-heading{flex-direction:column}.update-summary{grid-template-columns:1fr}.update-policy{border-left:0;border-top:1px solid var(--app-border)}.update-actions{align-items:stretch;flex-direction:column}.update-actions .n-input{max-width:none}.update-command{flex-direction:column;align-items:flex-start}}
</style>
