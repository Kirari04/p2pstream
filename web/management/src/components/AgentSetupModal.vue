<script setup lang="ts">
import { computed, inject, onUnmounted, ref, watch } from "vue";
import { NAlert, NButton, NCheckbox, NInput, NModal, NSpin, NTab, NTabs } from "naive-ui";
import { dashboardKey, environmentsKey, selectedEnvironmentIdKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { agentSetupManagementUrl, agentSetupReleaseVersion, cliSnippet, dockerComposeSnippet, dockerImageForRepository, FALLBACK_RELEASE_REPOSITORY, isLocalManagementUrl, linuxInstallSnippet, linuxManagedUpdaterBootstrapSnippet, linuxRotateAgentTokenSnippet, normalizeManagementUrl } from "@/lib/agentSetupSnippets";
import { prepareExistingAgentSetup, type AgentSetupMode, type AgentSetupSubject, type ManagedAgentSetup } from "@/lib/agentSetupFlow";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";
import { messageFromError } from "@/lib/errors";
import { modalCardStyle, modalScrollableContentStyle } from "@/lib/naiveUi";
import type { CreatedAgentSetup } from "@/types/agentSetup";

const client = useManagementClient();
const dashboard = inject(dashboardKey, computed(() => null));
const environments = inject(environmentsKey, computed(() => []));
const selectedEnvironmentId = inject(selectedEnvironmentIdKey, computed(() => "0"));
const discardDialog = useConfirmDialog();
const session = ref<{ agent: AgentSetupSubject; mode: AgentSetupMode; token: string; remote: boolean } | null>(null);
const managed = ref<ManagedAgentSetup | null>(null);
const alreadyEnrolled = ref(false);
const managedUpdates = ref(false);
const managementUrl = ref("");
const releaseVersion = ref("");
const repository = ref("");
const installerPath = ref("");
const binaryPath = ref("");
const allowTargets = ref("");
const allowAnyTarget = ref(false);
const caBase64 = ref("");
const caFile = ref("");
const certRequired = ref(false);
const certFile = ref("/etc/p2pstream/agent.crt.pem");
const keyFile = ref("/etc/p2pstream/agent.key.pem");
const allowInsecure = ref(false);
const dockerImage = ref("");
const dockerImageTouched = ref(false);
const tab = ref<"install" | "docker" | "cli">("install");
const loading = ref(false);
const preparationError = ref("");
const notice = ref("");
const copied = ref(false);
const copyError = ref("");
const advancedOpen = ref(false);
let generation = 0;

const mode = computed(() => session.value?.mode ?? "create");
const titles: Record<AgentSetupMode, string> = { create: "Install Agent", reinstall: "Reinstall / Repair Agent", "enable-updates": "Enable Managed Updates", rotate: "Apply Rotated Token" };
const descriptions: Record<AgentSetupMode, string> = {
  create: "Install this agent with one command. Managed updates are included when available.",
  reinstall: "Reuse the token stored on this host, refresh its configuration, and restart the agent. Managed updates are included when available.",
  "enable-updates": "Enable future updates from this UI. This one-time setup keeps the current tunnel running and preserves its token.",
  rotate: "Apply the new token on the existing host and restart its connection. The installed software and other settings are preserved.",
};
const usesTLS = computed(() => normalizeManagementUrl(managementUrl.value).startsWith("https://"));
const connectionOptionsVisible = computed(() => mode.value !== "rotate" || tab.value !== "install");
const fullInstallOptionsVisible = computed(() => mode.value === "create" || mode.value === "reinstall");
const canSelectFormat = computed(() => mode.value === "create" || mode.value === "rotate");
const copyLabel = computed(() => copied.value ? "Copied" : mode.value === "create" && tab.value === "install" ? "Copy install command" : "Copy command");

function begin(agent: AgentSetupSubject, nextMode: AgentSetupMode, token = "") {
  generation += 1;
  const environment = environments.value.find((item) => item.id.toString() === selectedEnvironmentId.value);
  const security = dashboard.value?.managementSecurity;
  session.value = { agent: { ...agent }, mode: nextMode, token, remote: Boolean(environment) };
  managementUrl.value = agentSetupManagementUrl(security?.defaultManagementUrl, environment?.managementUrl, window.location.origin);
  releaseVersion.value = agentSetupReleaseVersion(dashboard.value?.status?.version, import.meta.env.VITE_RELEASE_REF);
  repository.value = import.meta.env.VITE_RELEASE_REPOSITORY?.trim() || FALLBACK_RELEASE_REPOSITORY;
  managed.value = null;
  managedUpdates.value = false;
  alreadyEnrolled.value = false;
  installerPath.value = "";
  binaryPath.value = "";
  allowTargets.value = "";
  allowAnyTarget.value = false;
  caBase64.value = security?.managementCaPem ? btoa(security.managementCaPem) : "";
  caFile.value = "";
  certRequired.value = Boolean(security?.agentClientCertificateRequired);
  certFile.value = "/etc/p2pstream/agent.crt.pem";
  keyFile.value = "/etc/p2pstream/agent.key.pem";
  allowInsecure.value = false;
  tab.value = "install";
  dockerImageTouched.value = false;
  dockerImage.value = dockerImageForRepository(repository.value, releaseVersion.value);
  preparationError.value = "";
  notice.value = "";
  copied.value = false;
  copyError.value = "";
  loading.value = false;
  advancedOpen.value = certRequired.value || !usesTLS.value;
  return generation;
}

function useManagedSetup(value: ManagedAgentSetup | null) {
  managed.value = value;
  managedUpdates.value = Boolean(value?.updaterEnrollmentToken);
  if (value?.updaterPinnedRepository) repository.value = value.updaterPinnedRepository;
}

function openCreated(payload: CreatedAgentSetup) {
  if (!payload.agent) return;
  begin(payload.agent, "create", payload.token);
  useManagedSetup(payload.updaterEnrollmentToken ? payload : null);
}

function openRotated(agent: AgentSetupSubject, token: string) { begin(agent, "rotate", token); }

async function openExisting(agent: AgentSetupSubject, nextMode: "reinstall" | "enable-updates") {
  const request = begin(agent, nextMode);
  loading.value = true;
  try {
    const prepared = await prepareExistingAgentSetup(client, agent, nextMode, () => request === generation && Boolean(session.value));
    if (request !== generation || !session.value) return;
    useManagedSetup(prepared.managed);
    alreadyEnrolled.value = prepared.enrolled;
    notice.value = prepared.notice;
    if (prepared.version) releaseVersion.value = prepared.version;
    if (prepared.liveVersion) session.value.agent.version = prepared.liveVersion;
    if (prepared.liveCommit) session.value.agent.commit = prepared.liveCommit;
  } catch (error) {
    if (request === generation) preparationError.value = messageFromError(error);
  } finally {
    if (request === generation) loading.value = false;
  }
}

const commandResult = computed(() => {
  if (!session.value || loading.value) return { command: "", error: "" };
  if (preparationError.value) return { command: "", error: preparationError.value };
  const { agent, token } = session.value;
  try {
    if (mode.value === "rotate" && tab.value === "install") return { command: linuxRotateAgentTokenSnippet({agentId: agent.publicId, agentToken: token}), error: "" };
    const url = new URL(managementUrl.value.trim());
    if (!["https:", "http:"].includes(url.protocol) || url.username || url.password || url.search || url.hash) throw new Error("Enter a management HTTP(S) URL without credentials, a query, or a fragment.");
    if (managedUpdates.value && tab.value === "install" && (url.protocol !== "https:" || url.pathname !== "/")) throw new Error("Managed updates require an HTTPS origin without a path.");
    const input = {
      managementUrl: url.toString(), agentId: agent.publicId, agentToken: token,
      reuseExistingToken: mode.value === "reinstall",
      enableManagedUpdates: managedUpdates.value && tab.value === "install",
      updaterEnrollmentToken: managed.value?.updaterEnrollmentToken,
      agentUpdateAuthorityPublicKeyBase64: managed.value?.updaterManagementAuthorityPublicKeyBase64,
      agentUpdateAuthorityKeyId: managed.value?.updaterManagementAuthorityKeyId,
      agentUpdateAuthorityEpoch: managed.value?.updaterManagementAuthorityEpoch,
      repository: repository.value, version: releaseVersion.value, installerPath: installerPath.value, agentBinaryPath: binaryPath.value,
      dockerImage: dockerImage.value,
      allowTargets: allowAnyTarget.value ? [] : allowTargets.value.split(/[\s,]+/).filter(Boolean), allowAnyTarget: allowAnyTarget.value,
      tls: { enabled: usesTLS.value, managementCAPEMBase64: usesTLS.value ? caBase64.value : "", managementCAFile: caFile.value, agentTLSCertFile: certRequired.value ? certFile.value : "", agentTLSKeyFile: certRequired.value ? keyFile.value : "", allowInsecureManagement: allowInsecure.value },
    };
    let command: string;
    if (mode.value === "enable-updates") command = linuxManagedUpdaterBootstrapSnippet({ ...input, updaterEnrollmentToken: input.updaterEnrollmentToken ?? "", agentUpdateAuthorityPublicKeyBase64: input.agentUpdateAuthorityPublicKeyBase64 ?? "", agentUpdateAuthorityKeyId: input.agentUpdateAuthorityKeyId ?? "", agentUpdateAuthorityEpoch: input.agentUpdateAuthorityEpoch ?? 0n, currentTunnelVersion: agent.version ?? "", currentTunnelCommit: agent.commit ?? "" });
    else if (tab.value === "docker") command = dockerComposeSnippet(input);
    else if (tab.value === "cli") command = cliSnippet(input);
    else command = linuxInstallSnippet(input);
    return { command, error: "" };
  } catch (error) { return { command: "", error: messageFromError(error) }; }
});

watch([repository, releaseVersion], () => {
  if (!dockerImageTouched.value) {
    try { dockerImage.value = dockerImageForRepository(repository.value, releaseVersion.value); } catch { /* The command error describes the invalid input. */ }
  }
});
watch(() => commandResult.value.command, () => { copied.value = false; copyError.value = ""; });

async function copyCommand() {
  if (!commandResult.value.command) return;
  try { await navigator.clipboard.writeText(commandResult.value.command); copied.value = true; }
  catch { copyError.value = "Clipboard access failed. Select and copy the command below."; }
}

async function close() {
  if (!copied.value && (session.value?.token || managed.value?.updaterEnrollmentToken)) {
    if (!await discardDialog.confirm("Close Without Copying?", "Closing now discards the one-time credentials in this command.", "Discard Credentials")) return;
  }
  generation += 1;
  session.value = null;
  managed.value = null;
  loading.value = false;
}

onUnmounted(() => { generation += 1; });
defineExpose({ openCreated, openExisting, openRotated });
</script>

<template>
  <NModal :show="Boolean(session)" preset="card" :title="titles[mode]" :style="modalCardStyle('48rem')" :content-style="modalScrollableContentStyle()" :bordered="false" :mask-closable="false" :close-on-esc="false" @update:show="(show) => { if (!show) void close(); }">
    <div v-if="session" class="agent-setup-dialog layout-grid space-lg">
      <div class="agent-setup-identity">
        <strong><bdi dir="ltr" :title="diagnosticInspectionText(session.agent.name)">{{ diagnosticExcerpt(session.agent.name, 72).text }}</bdi></strong>
        <code><bdi dir="ltr">{{ diagnosticInspectionText(session.agent.publicId) }}</bdi></code>
      </div>
      <p class="copy-sm muted-text">{{ descriptions[mode] }}</p>
      <NSpin v-if="loading" size="small"><span class="copy-sm">Preparing setup for this environment…</span></NSpin>
      <NAlert v-if="notice" type="info" :bordered="false">{{ notice }}</NAlert>
      <NAlert v-if="copied" type="success" :bordered="false">Command copied. Run it on this agent's host.</NAlert>
      <label v-if="connectionOptionsVisible" class="agent-setup-field">
        <span>Management URL</span>
        <NInput v-model:value="managementUrl" :input-props="{ 'aria-label': 'Management URL' }" :disabled="loading" placeholder="https://management.example.com:8081" />
        <small>Use an address reachable from the agent host.</small>
      </label>
      <NAlert v-if="connectionOptionsVisible && session.remote && isLocalManagementUrl(managementUrl)" type="warning" :bordered="false">This address points to the agent host itself. Enter the remote management server's reachable address.</NAlert>
      <NCheckbox v-if="managed && fullInstallOptionsVisible" v-model:checked="managedUpdates" :disabled="alreadyEnrolled || loading">Enable managed updates with this installation</NCheckbox>
      <NTabs v-if="canSelectFormat" v-model:value="tab" class="agent-setup-tabs" type="segment" size="small">
        <NTab name="install" :tab="mode === 'rotate' ? 'Linux token change' : 'Linux install'" />
        <NTab name="docker" tab="Docker Compose" />
        <NTab name="cli" tab="CLI" />
      </NTabs>
      <details v-if="connectionOptionsVisible" class="agent-advanced-options" :open="advancedOpen" @toggle="advancedOpen = ($event.target as HTMLDetailsElement).open">
        <summary>Advanced setup options<small>Release, TLS, and optional local files</small></summary>
        <div class="agent-advanced-options__body">
          <div class="layout-grid space-md mq-md-cols-two">
            <label class="agent-setup-field">GitHub Repository<NInput v-model:value="repository" size="small" :disabled="loading || Boolean(managed && managedUpdates)" /></label>
            <label class="agent-setup-field">Release Version<NInput v-model:value="releaseVersion" size="small" :disabled="loading || Boolean(managed && managedUpdates)" placeholder="latest or vX.Y.Z" /></label>
            <label v-if="tab === 'install'" class="agent-setup-field">Installer file (optional)<NInput v-model:value="installerPath" size="small" placeholder="Download automatically" /></label>
            <label v-if="tab === 'install'" class="agent-setup-field">Agent binary (optional)<NInput v-model:value="binaryPath" size="small" placeholder="Download automatically" /></label>
            <label v-if="tab === 'docker'" class="agent-setup-field">Docker image<NInput v-model:value="dockerImage" size="small" @update:value="dockerImageTouched = true" /></label>
          </div>
          <div v-if="fullInstallOptionsVisible" class="layout-grid space-md">
            <label class="agent-setup-field">Agent destination allowlist<NInput v-model:value="allowTargets" size="small" :disabled="allowAnyTarget" placeholder="app.internal:443, 10.0.5.0/24:8080" /><small>Blank keeps the existing host policy on reinstall and uses loopback-only defaults on a new host.</small></label>
            <NCheckbox v-model:checked="allowAnyTarget">Allow any destination reachable by this agent</NCheckbox>
          </div>
          <p v-if="mode === 'enable-updates'" class="copy-xs muted-text">Uses the management CA already installed on this host.</p>
          <div v-else-if="usesTLS" class="layout-grid space-md mq-md-cols-two">
            <div v-if="caBase64" class="copy-xs muted-text">The management CA is included in this command.</div>
            <label v-else class="agent-setup-field">Management CA file<NInput v-model:value="caFile" size="small" placeholder="/etc/p2pstream/management-ca.pem" /></label>
            <label v-if="certRequired" class="agent-setup-field">Agent certificate<NInput v-model:value="certFile" size="small" /></label>
            <label v-if="certRequired" class="agent-setup-field">Agent key<NInput v-model:value="keyFile" size="small" /></label>
          </div>
          <NCheckbox v-else v-model:checked="allowInsecure">Allow insecure HTTP for local development</NCheckbox>
        </div>
      </details>
      <details v-if="session.token || managed?.updaterEnrollmentToken" class="agent-setup-credentials">
        <summary>Credentials included in the command</summary>
        <div v-if="session.token"><span>Agent token</span><code>{{ session.token }}</code></div>
        <div v-if="managed?.updaterEnrollmentToken"><span>Updater enrollment token</span><code>{{ managed.updaterEnrollmentToken }}</code><small v-if="managed.updaterEnrollmentExpiresAtUnixMillis">Expires {{ new Date(Number(managed.updaterEnrollmentExpiresAtUnixMillis)).toLocaleString() }}</small></div>
      </details>
      <p v-if="mode !== 'rotate'" class="copy-xs muted-text">The command downloads the matching release and verifies its SHA-256 checksums before installation. Copy it and run it on the agent host.</p>
      <NAlert v-if="commandResult.error || copyError" type="error" :bordered="false">{{ commandResult.error || copyError }}</NAlert>
      <pre v-if="commandResult.command" class="agent-setup-command"><code>{{ commandResult.command }}</code></pre>
      <div class="layout-row mq-sm-end space-md">
        <NButton secondary @click="close">Done</NButton>
        <NButton type="primary" :disabled="!commandResult.command || loading" @click="copyCommand">{{ copyLabel }}</NButton>
      </div>
    </div>
  </NModal>
</template>

<style scoped>
.agent-setup-identity { display: grid; gap: .25rem; min-width: 0; }
.agent-setup-identity code { color: var(--app-text-muted); font-size: .75rem; overflow-wrap: anywhere; }
.agent-setup-field { display: grid; gap: .4rem; min-width: 0; font-size: .8125rem; font-weight: 500; }
.agent-setup-field small { color: var(--app-text-muted); font-size: .75rem; font-weight: 400; }
.agent-advanced-options { border: 1px solid var(--app-border); border-radius: 6px; overflow: hidden; }
.agent-advanced-options summary, .agent-setup-credentials summary { cursor: pointer; padding: .8rem 1rem; font-size: .8125rem; }
.agent-advanced-options summary small { display: block; margin-top: .2rem; color: var(--app-text-muted); font-size: .75rem; }
.agent-advanced-options__body { display: grid; gap: 1rem; padding: 1rem; border-top: 1px solid var(--app-border); }
.agent-setup-credentials { background: var(--app-panel-muted); border-radius: 6px; }
.agent-setup-credentials div { display: grid; gap: .3rem; padding: .6rem 1rem; font-size: .75rem; }
.agent-setup-credentials code { overflow-wrap: anywhere; }
.agent-setup-command { max-height: 14rem; overflow: auto; padding: 1rem; border: 1px solid var(--app-border); border-radius: 6px; background: var(--app-panel-muted); font-size: .75rem; }
</style>
