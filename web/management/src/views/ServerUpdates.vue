<script setup lang="ts">
import { computed, inject, onMounted, onUnmounted, ref, watch } from "vue";
import { NAlert, NButton, NCard, NInput, NModal, NTag } from "naive-ui";
import { Check, Copy, Download, RefreshCw, ShieldCheck, Wifi, WifiOff } from "@lucide/vue";
import { selectedEnvironmentIdKey, selectedEnvironmentLabelKey, selectedEnvironmentBlockedKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import { createAgentUpdatePolling } from "@/lib/agentUpdatePolling";
import { serverSetupCommand } from "@/lib/serverSetup";
import { messageFromError } from "@/lib/errors";
import { parsePendingServerUpdate, serverUpdateDuration, serverUpdateTerminal, type PendingServerUpdate } from "@/lib/serverUpdates";
import ServerUpdateProgress from "@/components/updates/ServerUpdateProgress.vue";
import type { GetServerUpdateOverviewResponse, PreviewServerUpdateResponse } from "@/gen/proto/p2pstream/v1/management_pb";
import PageHeader from "@/components/ui/PageHeader.vue";
import { modalCardStyle, modalScrollableContentStyle } from "@/lib/naiveUi";

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
const connectionError = ref("");
const lastCheckedAt = ref(0);
const copied = ref(false);
const pending = ref<PendingServerUpdate | null>(null);
const now = ref(Date.now());
let actionRevision = 0;
let disposed = false;
const scope = () => `${environmentId.value}\u0000${blocked.value}`;
const storageKey = () => `p2pstream:server-update:${environmentId.value}`;
const operation = computed(() => overview.value?.operation);
const stale = computed(() => Boolean(connectionError.value || (overview.value?.executorConfigured && !overview.value.executorAvailable)));
const checkedAgo = computed(() => lastCheckedAt.value ? serverUpdateDuration(Math.max(0, now.value - lastCheckedAt.value)) : "");
const running = computed(() => Boolean(operation.value && !serverUpdateTerminal(operation.value.phase)));
const planExpired = computed(() => !plan.value || Number(plan.value.expiresAtUnixMillis) <= now.value);
const composeProject = ref("");
const composeFiles = ref("");
const composeEnvFiles = ref("");
const composeDirectory = ref("");
const setupCommand = computed(() => serverSetupCommand(overview.value?.setupCommand ?? "", { project: composeProject.value, files: composeFiles.value, envFiles: composeEnvFiles.value, directory: composeDirectory.value }));
const singleLineSetup = computed(() => Boolean(setupCommand.value && !/[\r\n]/.test(setupCommand.value)));
const deploymentMigration = computed(() => Boolean(overview.value?.executorConfigured && (overview.value.setupCommand || overview.value.setupUnavailable)));
const showSetup = computed(() => Boolean(overview.value && !running.value && !pending.value && !blocked.value && (!overview.value.executorConfigured || deploymentMigration.value)));
// Legacy host migration uses its own deployment validation, not enrollment
// preflight or the executor's software-update readiness checks.
const setupBlockers = computed(() => deploymentMigration.value ? [] : overview.value?.blockers ?? []);
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
  // Idle reads may resolve and verify the release catalog. A previous failure
  // must not keep canceling that work with the shorter operation-status budget.
  load: (signal) => client.getServerUpdateOverview({}, { signal, timeoutMs: running.value || pending.value ? 10000 : 90000 }),
  apply: (response) => {
    // An unavailable executor cannot contradict the last accepted operation.
    // Keep its progress visible while the independent updater reconnects.
    if (response.executorConfigured && !response.executorAvailable && !response.operation && response.instanceId === overview.value?.instanceId) {
      response.operation = overview.value.operation;
    }
    overview.value = response;
    now.value = Date.now();
    lastCheckedAt.value = now.value;
    connectionError.value = "";
    if (!targetVersion.value && response.target) targetVersion.value = response.target.version;
    if (pending.value && response.operation?.id === pending.value.operationId) { savePending(null); error.value = ""; }
  },
  error: (cause) => { connectionError.value = messageFromError(cause); },
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
  if (!setupCommand.value || busy.value || running.value || pending.value || blocked.value || setupBlockers.value.length || connectionError.value) return;
  const revision = actionRevision;
  const requestScope = scope();
  const migration = deploymentMigration.value;
  try {
    await navigator.clipboard.writeText(setupCommand.value);
    if (actionIsCurrent(revision, requestScope)) copied.value = true;
  } catch {
    if (actionIsCurrent(revision, requestScope)) error.value = migration ? "Select and copy the migration command below." : "Select and copy the setup command below.";
  }
}

watch(setupCommand, () => { copied.value = false; });
watch(() => [environmentId.value, blocked.value], () => {
  actionRevision += 1;
  composeProject.value = ""; composeFiles.value = ""; composeEnvFiles.value = ""; composeDirectory.value = "";
  busy.value = false; overview.value = null; plan.value = null; targetVersion.value = ""; error.value = ""; connectionError.value = ""; lastCheckedAt.value = 0; copied.value = false;
  loadPending(); void polling.scopeChanged();
});
let clock: ReturnType<typeof setInterval>;
onMounted(() => { loadPending(); void polling.start(); clock = setInterval(() => { now.value = Date.now(); }, 1000); });
onUnmounted(() => { disposed = true; actionRevision += 1; polling.stop(); clearInterval(clock); });
</script>

<template>
  <div class="server-updates">
    <PageHeader title="Server updates">
      <template #description>Verified releases for <bdi dir="ltr">{{ environmentLabel }}</bdi>, with a data backup and recovery to the previous version.</template>
      <template #actions>
        <NButton secondary size="small" :loading="loading" :disabled="busy || Boolean(blocked)" @click="polling.refresh()"><template #icon><RefreshCw :size="14" /></template>Refresh status</NButton>
      </template>
    </PageHeader>

    <NAlert v-if="blocked" type="warning">{{ blocked }}</NAlert>
    <NAlert v-if="error" :type="running || pending ? 'info' : 'error'" :title="pending ? 'Update request needs confirmation' : 'Request could not complete'">{{ error }}</NAlert>
    <NAlert v-if="overview?.warning" type="warning">{{ overview.warning }}</NAlert>

    <div v-if="overview || connectionError" class="update-connection" :class="{ 'update-connection--waiting': connectionError }" aria-label="Management connection">
      <div><WifiOff v-if="connectionError" :size="16" /><Wifi v-else :size="16" /><strong>{{ connectionError ? 'Waiting for management' : 'Management responding' }}</strong><span v-if="lastCheckedAt">Last response {{ checkedAgo }} ago</span></div>
      <p v-if="connectionError">The host updater runs independently. This connection status does not confirm an update outcome. {{ connectionError }}</p>
      <p v-else>The management connection is separate from the host update outcome and public route health.</p>
    </div>

    <ServerUpdateProgress v-if="operation" :key="`${environmentId}:${operation.id}`" :operation="operation" :now="now" :environment="environmentLabel" :instance-id="overview?.instanceId || ''" :stale="stale" />

    <div class="update-summary">
      <div class="update-version">
        <span class="update-eyebrow">Installed on {{ environmentLabel }}</span>
        <strong>{{ overview?.version || 'Checking…' }}</strong>
        <div class="update-meta"><NTag size="small" :bordered="false">{{ overview?.channel || 'Unknown channel' }}</NTag><span>{{ overview?.commit.slice(0, 12) }}</span></div>
      </div>
      <div class="update-policy"><ShieldCheck :size="22" /><div><strong>You choose when to update</strong><p>Unattended updates are off. The installed image stays pinned until you select another release.</p></div></div>
    </div>

    <NCard v-if="overview && showSetup" :title="deploymentMigration ? 'Upgrade host updater' : 'Enable server updates'" :class="{ 'update-migration': deploymentMigration }">
      <template v-if="deploymentMigration" #header-extra><NTag size="small" :bordered="false" type="info">One-time migration</NTag></template>
      <NAlert v-if="setupBlockers.length" type="warning" title="Resolve before setup" class="update-blocker" aria-label="Setup prerequisites">
        <ul><li v-for="reason in setupBlockers" :key="reason">{{ reason }}</li></ul>
        <p>Resolve these conditions, then refresh status. The setup command becomes available when this server reports that it is ready.</p>
      </NAlert>
      <NAlert v-else-if="connectionError" type="info" title="Waiting to confirm setup readiness">Reconnect to {{ environmentLabel }} and refresh status before running setup.</NAlert>
      <template v-else>
      <template v-if="deploymentMigration">
        <p>Upgrade <strong>{{ environmentLabel }}</strong>’s pinned host updater and connect its existing Compose deployment to the managed integration. Run the {{ singleLineSetup ? 'migration command' : 'complete migration block' }} on its <strong>Docker host</strong>, from the directory containing the running deployment.</p>
        <div class="update-migration-facts" aria-label="Migration preserves deployment"><div><span>Current image</span><strong>Preserved</strong></div><div><span>Data volume</span><strong>Preserved</strong></div><div><span>Existing token</span><strong>Preserved</strong></div></div>
        <NAlert type="warning" class="update-blocker">Migration restarts the server. Restoring a disconnected updater may require an additional restart. Public requests and agent tunnels are interrupted during these restarts.</NAlert>
        <p class="update-migration-copy">After migration, the original Compose files become bridges to the managed deployment, so ordinary <code>docker compose</code> commands use that deployment. The installer prints the saved editable paths.</p>
      </template>
      <template v-else>
        <p>Run the {{ singleLineSetup ? 'setup command' : 'complete setup block' }} on <strong>{{ environmentLabel }}</strong>’s <strong>Docker host</strong>, from the directory containing its running Compose deployment and <code>.env</code>. It verifies this installation and release, installs the independent updater, and restarts the server once.</p>
        <NAlert type="warning" class="update-blocker">The updater receives host-level Docker authority. Enable it only on the intended host. Public traffic and agent tunnels reconnect after the restart.</NAlert>
      </template>
      <template v-if="setupCommand">
        <details class="update-context"><summary>Custom Compose project or files</summary>
          <p>Use the same options as the running deployment. Paths are relative to the directory where you run the command. Leave these blank for the default Compose context.</p>
          <label>Project name<NInput v-model:value="composeProject" aria-label="Compose project name" placeholder="production" /></label>
          <label>Compose files, in order (one per line)<NInput v-model:value="composeFiles" type="textarea" aria-label="Compose files" placeholder="compose.yaml&#10;production.yaml" :autosize="{ minRows: 2, maxRows: 5 }" /></label>
          <label>Environment files (one per line)<NInput v-model:value="composeEnvFiles" type="textarea" aria-label="Compose environment files" placeholder=".env" :autosize="{ minRows: 1, maxRows: 3 }" /></label>
          <label>Project directory override<NInput v-model:value="composeDirectory" aria-label="Compose project directory" placeholder="Leave blank to use the deployment directory" /></label>
        </details>
        <div class="update-command" :class="{ 'update-command--multiline': !singleLineSetup }">
          <div class="update-command-toolbar"><span>{{ singleLineSetup ? 'One-line command' : 'Complete setup block' }}</span><NButton size="small" :disabled="busy" :aria-label="copied ? 'Command copied' : deploymentMigration ? 'Copy migration command' : 'Copy setup command'" @click="copySetup"><template #icon><Check v-if="copied" :size="14" /><Copy v-else :size="14" /></template>{{ copied ? 'Copied' : 'Copy command' }}</NButton></div>
          <pre tabindex="0" aria-label="Host updater command"><code>{{ setupCommand }}</code></pre>
        </div>
        <p class="muted-text">Release {{ overview.version }} · Linux {{ overview.architecture }} · Installation {{ overview.installationId.slice(0, 8) }}. Requires Bash, curl, Python 3.9+, sudo, and local rootful Docker with Compose. GitHub and the publisher are the download trust source; checksums bind the downloaded content.</p>
        <div v-if="deploymentMigration" class="update-migration-settings"><strong>Settings after migration</strong><p>Edit the saved Compose inputs named <code>*.p2pstream-input-*</code> and the recorded environment files using the paths printed by the installer. Apply settings with <code>sudo /etc/p2pstream-server-updater/manage apply</code>.</p></div>
        <p class="muted-text">Keep this page open: it refreshes after the restart and confirms updater availability. Use the editable input paths reported by the installer when changing settings, then run <code>sudo /etc/p2pstream-server-updater/manage apply</code> to preserve the pinned release.</p>
      </template>
      <NAlert v-else-if="deploymentMigration" type="warning" title="Migration command unavailable">{{ overview.setupUnavailable || 'This server did not provide a supported verified migration command.' }} Resolve the reported prerequisite, then refresh. Continue using the existing host controller until migration completes.</NAlert>
      <NAlert v-else type="info" title="Manual release upgrade required">{{ overview.setupUnavailable || 'This server does not provide a verified installation bundle and installation identity. Manually deploy a published release with Docker installation support, then refresh this page.' }}</NAlert>
      <p class="muted-text">Supported: one Linux amd64/arm64 Compose server with a writable named <code>/data</code> volume. Native, rootless, Swarm, custom entrypoints, deployment hooks, external databases, and unsupported data layouts require host deployment procedures. Copied commands are rejected if this server’s identity, release, or deployment changed.</p>
      </template>
    </NCard>

    <NAlert v-if="overview?.executorConfigured" :type="connectionError || !overview.executorAvailable ? 'warning' : operation && operation.phase !== 'succeeded' ? 'info' : 'success'" :title="connectionError ? 'Host updater · last confirmed status' : overview.executorAvailable ? (operation ? 'Host updater connected · operation recorded' : 'Updates enabled · updater verified') : 'Host updater unavailable'">
      <template v-if="connectionError">Waiting for a fresh response from {{ environmentLabel }}. The operation panel retains its last confirmed state.</template>
      <template v-else-if="!overview.executorAvailable"><span v-if="operation">The updater connection is unavailable. The operation panel preserves the last confirmed host state.</span><span v-else>Setup or the private updater connection could not be verified.</span> On {{ environmentLabel }}’s Docker host run <code>sudo /etc/p2pstream-server-updater/manage status</code>, then <code>sudo /etc/p2pstream-server-updater/manage repair</code> if setup was interrupted. Refresh after recovery.</template>
      <template v-else-if="operation?.phase === 'recovery_required'">Resolve the host recovery above before applying settings or starting another update.</template>
      <template v-else-if="running">Wait for the confirmed host outcome above before applying settings. The updater continues independently of this page.</template>
      <template v-else-if="operation && operation.phase !== 'succeeded'">Review the recorded failure above and resolve its cause before previewing a release or applying deployment changes.</template>
      <template v-else-if="deploymentMigration">Complete the one-time host updater migration above to enable ordinary Compose recreation. Until then, apply settings through your existing host controller.</template>
      <template v-else>Use the editable input paths recorded by your host controller when changing settings. Apply changes with <code>sudo /etc/p2pstream-server-updater/manage apply</code>. Later software updates remain your choice.</template>
      <p class="update-host-policy">Server updates leave the pinned host updater unchanged. It does not self-update. Rerunning regular setup checks its health without refreshing it. <template v-if="deploymentMigration">The one-time migration upgrades this legacy host updater.</template><template v-else>Future host updater upgrades require a separate host procedure; this page does not provide one.</template></p>
    </NAlert>

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

    <NModal :show="Boolean(plan)" preset="card" title="Update the selected server" :style="modalCardStyle('35rem')" :content-style="modalScrollableContentStyle()" :mask-closable="!busy" @update:show="(show) => { if (!show && !busy) plan = null; }">
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
.server-updates :deep(.n-card-content){display:grid;gap:.75rem}.server-updates :deep(.n-card-content)>.update-command,.server-updates :deep(.n-card-content)>.update-context,.server-updates :deep(.n-card-content)>.update-migration-facts,.server-updates :deep(.n-card-content)>.update-migration-settings,.server-updates :deep(.n-card-content)>.update-blocker{margin:0}.update-context[open]>p{margin:.6rem 0 0}
.update-eyebrow{font-size:.8125rem;font-weight:500;color:var(--app-text-muted)}
.update-summary{display:grid;grid-template-columns:1fr 1fr;border:1px solid var(--app-border-subtle);border-radius:10px;overflow:hidden}.update-version{padding:1.5rem;display:grid;gap:.65rem;min-width:0}.update-version>strong{font-family:'IBM Plex Mono',monospace;font-size:1.5rem;overflow-wrap:anywhere}.update-meta{display:flex;gap:.8rem;align-items:center;font-family:'IBM Plex Mono',monospace;font-size:.75rem}
.update-policy{padding:1.5rem;display:flex;gap:.8rem;align-items:flex-start;background:var(--app-panel-muted);border-left:1px solid var(--app-border-subtle)}.update-policy>svg{flex-shrink:0;color:var(--app-accent)}.update-policy p{font-size:.82rem;margin:.4rem 0 0;color:var(--app-text-muted)}
.update-connection{padding:.8rem 1rem;border:1px solid var(--app-border-subtle);border-radius:6px;background:var(--app-panel-muted)}.update-connection>div{display:flex;align-items:center;gap:.6rem;flex-wrap:wrap;font-size:.75rem}.update-connection svg{color:var(--app-success);flex-shrink:0}.update-connection strong{font-weight:500}.update-connection span{color:var(--app-text-muted);font-family:var(--font-mono);font-size:.65rem;margin-left:auto}.update-connection p{color:var(--app-text-muted);font-size:.72rem;line-height:1.6;margin:.4rem 0 0;overflow-wrap:anywhere}.update-connection--waiting svg{color:var(--app-warning)}
.update-migration{border-top:3px solid var(--app-accent)}.update-migration-facts{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));margin:1rem 0;border:1px solid var(--app-border-subtle);border-radius:6px;background:var(--app-panel-muted)}.update-migration-facts>div{display:grid;gap:.3rem;padding:.8rem 1rem}.update-migration-facts>div+div{border-left:1px solid var(--app-border-subtle)}.update-migration-facts span{font-size:.7rem;color:var(--app-text-muted)}.update-migration-facts strong{font-family:var(--font-mono);font-size:.75rem;font-weight:500}.update-migration-copy{font-size:.82rem;line-height:1.65}.update-migration-settings{padding:.8rem 1rem;border-left:2px solid var(--app-accent);background:var(--app-panel-muted);margin:1rem 0}.update-migration-settings>strong{font-size:.8rem}.update-migration-settings p{font-size:.78rem;line-height:1.7;margin:.4rem 0 0}.update-migration-settings code{overflow-wrap:anywhere}
.update-actions,.update-operation{display:flex;align-items:center;gap:.75rem}.update-context{margin:1rem 0}.update-context label{display:grid;gap:.4rem;margin:.8rem 0}
.update-command{margin:1rem 0;border:1px solid var(--app-border-subtle);border-radius:6px;overflow:hidden;background:var(--app-panel-muted);min-width:0}.update-command-toolbar{display:flex;align-items:center;justify-content:space-between;gap:.75rem;padding:.5rem .75rem;border-bottom:1px solid var(--app-border-subtle)}.update-command-toolbar>span{font-size:.75rem;font-weight:500;color:var(--app-text-muted)}.update-command pre{margin:0;padding:1rem;max-height:14rem;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-family:var(--font-mono);font-size:.72rem;line-height:1.85;tab-size:2}.update-command pre:focus-visible{outline:2px solid var(--app-accent);outline-offset:-2px}.update-command code{font:inherit}.update-command--multiline pre{max-height:18rem}.update-host-policy{font-size:.76rem;line-height:1.7;margin:.75rem 0 0;padding-top:.75rem;border-top:1px solid var(--app-border-subtle)}
.update-actions .n-input{max-width:380px}.update-operation{flex-wrap:wrap;margin-bottom:.75rem}.update-blocker{margin-bottom:.75rem}.update-details{margin:1rem 0}.update-details code{display:block;overflow-wrap:anywhere;font-size:.7rem;margin-top:.75rem}.update-modal-actions{display:flex;justify-content:flex-end;gap:.75rem;margin-top:1.25rem}.muted-text{font-size:.8rem}
@media(max-width:640px){.update-summary{grid-template-columns:1fr}.update-policy{border-left:0;border-top:1px solid var(--app-border-subtle)}.update-actions{align-items:stretch;flex-direction:column}.update-actions .n-input{max-width:none}.update-command pre{padding:.8rem;font-size:.68rem}.update-command-toolbar{padding:.5rem .65rem}}
@media(max-width:420px){.update-migration-facts{grid-template-columns:1fr}.update-migration-facts>div{display:flex;justify-content:space-between;align-items:center;padding:.6rem .8rem}.update-migration-facts>div+div{border-left:0;border-top:1px solid var(--app-border-subtle)}}
</style>
