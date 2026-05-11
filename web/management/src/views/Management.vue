<script setup lang="ts">
import { computed, inject, reactive, ref, watch } from "vue";
import type { ComputedRef } from "vue";
import BanIcon from "@primevue/icons/ban";
import CheckIcon from "@primevue/icons/check";
import PencilIcon from "@primevue/icons/pencil";
import PlusIcon from "@primevue/icons/plus";
import RefreshIcon from "@primevue/icons/refresh";
import TimesIcon from "@primevue/icons/times";
import TrashIcon from "@primevue/icons/trash";
import { managementClient } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import PublicProxyEditorHost from "@/components/editors/PublicProxyEditorHost.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import Tag from "@/volt/Tag.vue";
import Modal from "@/volt/Modal.vue";
import {
  ProxyState,
  PublicBackendForwardMode,
  PublicBackendLoadBalancing,
  PublicBackendType,
  PublicListenerProtocol,
  PublicRateLimitAlgorithm,
  PublicRateLimitKeySource,
  PublicTrafficShaperBudgetScope,
  PublicAcmeCa,
  PublicAcmeChallengeType,
  PublicDnsProvider,
  PublicRouteAction,
  PublicRouteRedirectTargetMode,
  PublicTlsCertificateSource,
  PublicTlsCertificateStatus,
  type GetDashboardResponse,
  type GetPublicProxyConfigResponse,
  type PublicBackend,
  type PublicBackendAgent,
  type PublicListener,
  type PublicListenerStatus,
  type PublicRateLimitRule,
  type PublicTrafficShaperRule,
  type PublicRoute,
  type PublicTlsCertificate,
  type PublicTlsDnsCredential,
} from "@/gen/proto/p2pstream/v1/management_pb";

type Runner = (action: () => Promise<void>) => Promise<boolean>;
type TlsFileField = "cert" | "key";
type TlsMethod = "manual" | "http_01" | "tls_alpn_01" | "dns_01";

const tlsMethodOptions: Array<{ value: TlsMethod; label: string }> = [
  { value: "manual", label: "Manual" },
  { value: "http_01", label: "HTTP-01" },
  { value: "tls_alpn_01", label: "TLS-ALPN" },
  { value: "dns_01", label: "DNS-01" },
];

const dashboard = inject<ComputedRef<GetDashboardResponse | null>>("dashboard");
const publicProxyConfig = inject<ComputedRef<GetPublicProxyConfigResponse | null>>("publicProxyConfig");
const setProxyRunning = inject<(shouldRun: boolean) => Promise<void>>("setProxyRunning");
const runManagementAction = inject<Runner>("runManagementAction");
const logout = inject<() => void>("logout");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const status = computed(() => dashboard?.value?.status ?? null);
const config = computed(() => publicProxyConfig?.value ?? null);
const proxyState = computed(() => status.value?.proxy?.state ?? ProxyState.UNSPECIFIED);
const proxyIsRunning = computed(() => proxyState.value === ProxyState.RUNNING || status.value?.proxyRunning === true);
const proxyError = computed(() => status.value?.proxy?.lastError || status.value?.proxyLastError || "");
const listeners = computed(() => config.value?.listeners ?? []);
const backends = computed(() => config.value?.backends ?? []);
const agents = computed(() => config.value?.agents ?? []);
const backendAgents = computed(() => config.value?.backendAgents ?? []);
const routes = computed(() => config.value?.routes ?? []);
const rateLimitRules = computed(() => config.value?.rateLimitRules ?? []);
const trafficShaperRules = computed(() => config.value?.trafficShaperRules ?? []);
const tlsCertificates = computed(() => config.value?.tlsCertificates ?? []);
const tlsDnsCredentials = computed(() => config.value?.tlsDnsCredentials ?? []);
const listenerStatuses = computed(() => config.value?.proxy?.listeners ?? status.value?.proxy?.listeners ?? []);
const httpsListeners = computed(() => listeners.value.filter((listener) => listener.protocol === PublicListenerProtocol.HTTPS));

const isTlsModalOpen = ref(false);
const isTlsCredentialModalOpen = ref(false);
const editorHost = ref<InstanceType<typeof PublicProxyEditorHost> | null>(null);

const tlsForm = reactive({
  id: "" as string,
  listenerId: "",
  hostnamePattern: "",
  method: "manual" as TlsMethod,
  acmeEmail: "",
  acmeCa: PublicAcmeCa.LETS_ENCRYPT_PRODUCTION,
  dnsCredentialId: "",
  certPem: null as Uint8Array | null,
  keyPem: null as Uint8Array | null,
  certFileName: "",
  keyFileName: "",
  enabled: true,
});
const tlsUploadError = ref("");
const tlsCredentialForm = reactive({
  id: "",
  name: "",
  cloudflareZoneId: "",
  apiToken: "",
  apiTokenSaved: false,
  enabled: true,
});
const tlsCredentialError = ref("");

const proxySeverity = computed(() => severityForState(proxyState.value));
const tlsHasPartialUpload = computed(() => Boolean(tlsForm.certPem) !== Boolean(tlsForm.keyPem));
const busyDisabledReason = computed(() => isBusy?.value ? BUSY_REASON : "");
const tlsSubmitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!httpsListeners.value.length) return "Create an HTTPS listener before adding a TLS mapping.";
  if (tlsForm.method === "manual") {
    if (!tlsForm.id && (!tlsForm.certPem || !tlsForm.keyPem)) return "Upload both the certificate and private key files.";
    if (tlsHasPartialUpload.value) return "Upload both files to replace the certificate.";
    return "";
  }
  if (!tlsForm.acmeEmail.trim()) return "Enter the ACME account email.";
  if (tlsForm.hostnamePattern.trim().startsWith("*.") && tlsForm.method !== "dns_01") return "Wildcard certificates require DNS-01.";
  if (tlsForm.method === "dns_01" && !tlsForm.dnsCredentialId) return "Select a Cloudflare DNS credential.";
  return "";
});
const tlsSubmitDisabled = computed(() => Boolean(tlsSubmitDisabledReason.value));
const tlsCredentialSubmitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!tlsCredentialForm.name.trim()) return "Enter a credential name.";
  if (!tlsCredentialForm.cloudflareZoneId.trim()) return "Enter the Cloudflare zone ID.";
  if (!tlsCredentialForm.id && !tlsCredentialForm.apiToken.trim()) return "Enter the Cloudflare API token.";
  return "";
});

function proxyStateLabel(state: ProxyState): string {
  switch (state) {
    case ProxyState.STOPPED: return "Stopped";
    case ProxyState.STARTING: return "Starting";
    case ProxyState.RUNNING: return "Running";
    case ProxyState.STOPPING: return "Stopping";
    case ProxyState.ERROR: return "Error";
    default: return status.value?.proxyRunning ? "Running" : "Unknown";
  }
}

function severityForState(state: ProxyState): string {
  if (state === ProxyState.RUNNING) return "success";
  if (state === ProxyState.STARTING || state === ProxyState.STOPPING) return "warn";
  return "danger";
}

function listenerStatus(listener: PublicListener): PublicListenerStatus | undefined {
  return listenerStatuses.value.find((item) => item.listenerId === listener.id);
}

function listenerState(listener: PublicListener): ProxyState {
  if (!listener.enabled) return ProxyState.STOPPED;
  return listenerStatus(listener)?.state ?? ProxyState.STOPPED;
}

function listenerStateLabel(listener: PublicListener): string {
  if (!listener.enabled || listenerStatus(listener)?.disabled) return "Disabled";
  return proxyStateLabel(listenerState(listener));
}

function listenerRunningDisabledReason(listener: PublicListener): string {
  if (isBusy?.value) return BUSY_REASON;
  if (!listener.enabled) return "Enable this listener before starting it.";
  return "";
}

function backendName(id: bigint): string {
  if (id === 0n) return "None";
  return backends.value.find((backend) => backend.id === id)?.name ?? `#${id.toString()}`;
}

function agentName(id: bigint): string {
  const agent = agents.value.find((item) => item.id === id);
  return agent ? `${agent.name} (${agent.publicId})` : `#${id.toString()}`;
}

function listenerName(id: bigint): string {
  return listeners.value.find((listener) => listener.id === id)?.name ?? `#${id.toString()}`;
}

function bindLabel(listener: PublicListener): string {
  return `${listener.bindAddress || "*"}:${listener.port.toString()}`;
}

function protocolLabel(protocol: PublicListenerProtocol): string {
  return protocol === PublicListenerProtocol.HTTPS ? "HTTPS" : "HTTP";
}

function backendTypeLabel(type: PublicBackendType): string {
  return type === PublicBackendType.STATIC ? "Static" : "Proxy forward";
}

function forwardModeLabel(mode: PublicBackendForwardMode): string {
  return mode === PublicBackendForwardMode.AGENT_POOL ? "Agents" : "Direct";
}

function routeAction(route: PublicRoute): PublicRouteAction {
  return route.action === PublicRouteAction.REDIRECT ? PublicRouteAction.REDIRECT : PublicRouteAction.FORWARD;
}

function routeDestinationLabel(route: PublicRoute): string {
  if (routeAction(route) === PublicRouteAction.REDIRECT) {
    return `Redirect ${route.redirectStatusCode || 302}`;
  }
  return backendName(route.backendId);
}

function routeTargetSummary(route: PublicRoute): string {
  if (routeAction(route) !== PublicRouteAction.REDIRECT) {
    return backendName(route.backendId);
  }
  const target = route.redirectTarget || redirectModeLabel(route.redirectTargetMode);
  return `${redirectModeLabel(route.redirectTargetMode)} -> ${target}`;
}

function redirectModeLabel(mode: PublicRouteRedirectTargetMode): string {
  switch (mode) {
    case PublicRouteRedirectTargetMode.EXTERNAL_ORIGIN_KEEP_PATH:
      return "External origin";
    case PublicRouteRedirectTargetMode.ABSOLUTE_URL:
      return "Absolute URL";
    case PublicRouteRedirectTargetMode.SAME_HOST_PATH:
      return "Same host";
    default:
      return "Redirect";
  }
}

function loadBalancingLabel(algorithm: PublicBackendLoadBalancing): string {
  switch (algorithm) {
    case PublicBackendLoadBalancing.WEIGHTED_ROUND_ROBIN: return "Weighted round-robin";
    case PublicBackendLoadBalancing.RANDOM: return "Random";
    case PublicBackendLoadBalancing.WEIGHTED_RANDOM: return "Weighted random";
    case PublicBackendLoadBalancing.LEAST_ACTIVE_REQUESTS: return "Least active";
    case PublicBackendLoadBalancing.WEIGHTED_LEAST_ACTIVE_REQUESTS: return "Weighted least active";
    default: return "Round-robin";
  }
}

function rateLimitAlgorithmLabel(algorithm: PublicRateLimitAlgorithm): string {
  switch (algorithm) {
    case PublicRateLimitAlgorithm.SLIDING_WINDOW: return "Sliding window";
    case PublicRateLimitAlgorithm.TOKEN_BUCKET: return "Token bucket";
    case PublicRateLimitAlgorithm.LEAKY_BUCKET: return "Leaky bucket";
    default: return "Fixed window";
  }
}

function rateLimitWindowLabel(windowMillis: bigint): string {
  const seconds = Math.max(1, Math.round(Number(windowMillis || 0n) / 1000));
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  return `${Math.round(seconds / 3600)}h`;
}

function rateLimitRuleSummary(rule: PublicRateLimitRule): string {
  const burst = rule.algorithm === PublicRateLimitAlgorithm.TOKEN_BUCKET || rule.algorithm === PublicRateLimitAlgorithm.LEAKY_BUCKET
    ? `, burst ${Number(rule.burst || rule.limit).toString()}`
    : "";
  return `${Number(rule.limit).toString()} / ${rateLimitWindowLabel(rule.windowMillis)}${burst}`;
}

function rateLimitMatchSummary(rule: PublicRateLimitRule): string {
  const match = rule.match;
  if (!match) return "Any request";
  const parts: string[] = [];
  if (match.methods.length) parts.push(match.methods.join(","));
  if (match.protocols.length) parts.push(match.protocols.map(protocolLabel).join(","));
  if (match.hostPatterns.length) parts.push(match.hostPatterns.join(", "));
  if (match.pathPrefixes.length) parts.push(match.pathPrefixes.join(", "));
  const matcherCount = match.headers.length + match.cookies.length + match.queryParams.length;
  if (matcherCount) parts.push(`${matcherCount} value matcher${matcherCount === 1 ? "" : "s"}`);
  return parts.length ? parts.join(" / ") : "Any request";
}

function rateLimitKeySummary(rule: PublicRateLimitRule): string {
  const parts = rule.keyParts.length ? rule.keyParts : [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  return parts.map((part) => {
    const label = rateLimitKeySourceLabel(part.source);
    return part.name ? `${label}:${part.name}` : label;
  }).join(" + ");
}

function rateLimitKeySourceLabel(source: PublicRateLimitKeySource): string {
  switch (source) {
    case PublicRateLimitKeySource.HOST: return "host";
    case PublicRateLimitKeySource.METHOD: return "method";
    case PublicRateLimitKeySource.PATH: return "path";
    case PublicRateLimitKeySource.PROTOCOL: return "protocol";
    case PublicRateLimitKeySource.HEADER: return "header";
    case PublicRateLimitKeySource.COOKIE: return "cookie";
    case PublicRateLimitKeySource.QUERY_PARAM: return "query";
    default: return "ip";
  }
}

function trafficShaperScopeLabel(scope: PublicTrafficShaperBudgetScope): string {
  return scope === PublicTrafficShaperBudgetScope.PER_REQUEST ? "Per request" : "Per key";
}

function trafficShaperBytesLabel(bytes: bigint): string {
  const value = Number(bytes || 0n);
  if (value <= 0) return "unlimited";
  const kib = value / 1024;
  if (kib < 1024) return `${Math.round(kib).toString()} KiB/s`;
  return `${(kib / 1024).toFixed(1)} MiB/s`;
}

function trafficShaperKibLabel(bytes: bigint): string {
  const value = Number(bytes || 0n);
  if (value <= 0) return "0 KiB";
  const kib = value / 1024;
  if (kib < 1024) return `${Math.round(kib).toString()} KiB`;
  return `${(kib / 1024).toFixed(1)} MiB`;
}

function trafficShaperRuleSummary(rule: PublicTrafficShaperRule): string {
  return `up ${trafficShaperBytesLabel(rule.uploadBytesPerSecond)} / down ${trafficShaperBytesLabel(rule.downloadBytesPerSecond)}`;
}

function trafficShaperBudgetSummary(rule: PublicTrafficShaperRule): string {
  const burst = trafficShaperKibLabel(rule.burstBytes);
  const requestFree = trafficShaperKibLabel(rule.requestExemptBytes);
  const responseFree = trafficShaperKibLabel(rule.responseExemptBytes);
  return `burst ${burst} / free req ${requestFree}, res ${responseFree}`;
}

function trafficShaperMatchSummary(rule: PublicTrafficShaperRule): string {
  const match = rule.match;
  if (!match) return "Any request";
  const parts: string[] = [];
  if (match.methods.length) parts.push(match.methods.join(","));
  if (match.protocols.length) parts.push(match.protocols.map(protocolLabel).join(","));
  if (match.hostPatterns.length) parts.push(match.hostPatterns.join(", "));
  if (match.pathPrefixes.length) parts.push(match.pathPrefixes.join(", "));
  const matcherCount = match.headers.length + match.cookies.length + match.queryParams.length;
  if (matcherCount) parts.push(`${matcherCount} value matcher${matcherCount === 1 ? "" : "s"}`);
  return parts.length ? parts.join(" / ") : "Any request";
}

function trafficShaperKeySummary(rule: PublicTrafficShaperRule): string {
  if (rule.budgetScope === PublicTrafficShaperBudgetScope.PER_REQUEST) return "per request";
  const parts = rule.keyParts.length ? rule.keyParts : [{ source: PublicRateLimitKeySource.REMOTE_IP, name: "" }];
  return parts.map((part) => {
    const label = rateLimitKeySourceLabel(part.source);
    return part.name ? `${label}:${part.name}` : label;
  }).join(" + ");
}

function backendSummary(backend: PublicBackend): string {
  if (backend.backendType === PublicBackendType.STATIC) {
    const body = backend.staticResponseBody.trim();
    const suffix = body ? ` - ${body.slice(0, 72)}` : "";
    return `${backend.staticStatusCode.toString()}${suffix}`;
  }
  return backend.targetOrigin;
}

function assignmentsForBackend(backend: PublicBackend): PublicBackendAgent[] {
  if (backend.agentAssignments.length) return backend.agentAssignments;
  return backendAgents.value.filter((assignment) => assignment.backendId === backend.id);
}

function backendAgentSummary(backend: PublicBackend): string {
  if (backend.backendType !== PublicBackendType.PROXY_FORWARD || backend.forwardMode !== PublicBackendForwardMode.AGENT_POOL) {
    return "";
  }
  const assignments = assignmentsForBackend(backend).filter((assignment) => assignment.enabled);
  if (!assignments.length) return "No enabled agents";
  return assignments.map((assignment) => `${agentName(assignment.agentId)} x${assignment.weight.toString()}`).join(", ");
}

function upstreamHeaderCount(backend: PublicBackend): number {
  return backend.upstreamRequestHeaders.length;
}

function isDefaultSelfSignedCertificate(cert: PublicTlsCertificate): boolean {
  return cert.hostnamePattern === "p2pstream.local";
}

function tlsCertificateSummary(cert: PublicTlsCertificate): string {
  if (isDefaultSelfSignedCertificate(cert)) return "Default self-signed certificate";
  if (cert.source === PublicTlsCertificateSource.ACME) {
    const expiry = formatUnixMillis(cert.expiresAtUnixMillis);
    const renewal = formatUnixMillis(cert.nextRenewalAtUnixMillis);
    if (expiry && renewal) return `Expires ${expiry} / renews ${renewal}`;
    if (expiry) return `Expires ${expiry}`;
    return "Managed by Let's Encrypt";
  }
  return "Uploaded certificate";
}

function tlsMethodForCertificate(cert: PublicTlsCertificate): TlsMethod {
  if (cert.source !== PublicTlsCertificateSource.ACME) return "manual";
  if (cert.acmeChallengeType === PublicAcmeChallengeType.TLS_ALPN_01) return "tls_alpn_01";
  if (cert.acmeChallengeType === PublicAcmeChallengeType.DNS_01) return "dns_01";
  return "http_01";
}

function tlsSourceLabel(cert: PublicTlsCertificate): string {
  switch (tlsMethodForCertificate(cert)) {
    case "http_01": return "HTTP-01";
    case "tls_alpn_01": return "TLS-ALPN-01";
    case "dns_01": return "DNS-01";
    default: return "Manual";
  }
}

function tlsStatusLabel(cert: PublicTlsCertificate): string {
  if (!cert.enabled) return "Disabled";
  switch (cert.status) {
    case PublicTlsCertificateStatus.PENDING: return "Pending";
    case PublicTlsCertificateStatus.RENEWING: return "Renewing";
    case PublicTlsCertificateStatus.ERROR: return "Error";
    case PublicTlsCertificateStatus.READY: return "Ready";
    default: return cert.source === PublicTlsCertificateSource.ACME ? "Pending" : "Ready";
  }
}

function tlsStatusSeverity(cert: PublicTlsCertificate): string {
  if (!cert.enabled) return "warn";
  if (cert.status === PublicTlsCertificateStatus.ERROR) return "danger";
  if (cert.status === PublicTlsCertificateStatus.PENDING || cert.status === PublicTlsCertificateStatus.RENEWING) return "warn";
  return "success";
}

function acmeChallengeTypeForMethod(method: TlsMethod): PublicAcmeChallengeType {
  if (method === "tls_alpn_01") return PublicAcmeChallengeType.TLS_ALPN_01;
  if (method === "dns_01") return PublicAcmeChallengeType.DNS_01;
  return PublicAcmeChallengeType.HTTP_01;
}

function tlsSourceForMethod(method: TlsMethod): PublicTlsCertificateSource {
  return method === "manual" ? PublicTlsCertificateSource.MANUAL : PublicTlsCertificateSource.ACME;
}

function dnsCredentialName(id: bigint): string {
  if (id === 0n) return "None";
  return tlsDnsCredentials.value.find((credential) => credential.id === id)?.name ?? `#${id.toString()}`;
}

function formatUnixMillis(value: bigint): string {
  if (!value || value === 0n) return "";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium" }).format(new Date(Number(value)));
}

function editBackend(backend: PublicBackend) {
  editorHost.value?.openBackend(backend.id);
}

function openAddListenerModal() {
  editorHost.value?.openCreateListener();
}

function editListener(listener: PublicListener) {
  editorHost.value?.openListener(listener.id);
}

function openAddBackendModal() {
  editorHost.value?.openCreateBackend();
}

function openAddRouteModal() {
  editorHost.value?.openCreateRoute();
}

function editRoute(routeId: bigint) {
  editorHost.value?.openRoute(routeId);
}

function openAddRateLimitRuleModal() {
  editorHost.value?.openCreateRateLimitRule();
}

function editRateLimitRule(id: bigint) {
  editorHost.value?.openRateLimitRule(id);
}

function openAddTrafficShaperRuleModal() {
  editorHost.value?.openCreateTrafficShaperRule();
}

function editTrafficShaperRule(id: bigint) {
  editorHost.value?.openTrafficShaperRule(id);
}

function openAddTlsModal() {
  resetTlsForm();
  isTlsModalOpen.value = true;
}

function resetTlsForm() {
  tlsForm.id = "";
  tlsForm.listenerId = httpsListeners.value[0]?.id.toString() ?? "";
  tlsForm.hostnamePattern = "";
  tlsForm.method = "manual";
  tlsForm.acmeEmail = "";
  tlsForm.acmeCa = PublicAcmeCa.LETS_ENCRYPT_PRODUCTION;
  tlsForm.dnsCredentialId = tlsDnsCredentials.value[0]?.id.toString() ?? "";
  tlsForm.certPem = null;
  tlsForm.keyPem = null;
  tlsForm.certFileName = "";
  tlsForm.keyFileName = "";
  tlsForm.enabled = true;
  tlsUploadError.value = "";
}

function editTlsCertificate(certId: bigint) {
  const cert = tlsCertificates.value.find((item) => item.id === certId);
  if (!cert) return;
  tlsForm.id = cert.id.toString();
  tlsForm.listenerId = cert.listenerId.toString();
  tlsForm.hostnamePattern = cert.hostnamePattern;
  tlsForm.method = tlsMethodForCertificate(cert);
  tlsForm.acmeEmail = cert.acmeEmail;
  tlsForm.acmeCa = cert.acmeCa || PublicAcmeCa.LETS_ENCRYPT_PRODUCTION;
  tlsForm.dnsCredentialId = cert.dnsCredentialId ? cert.dnsCredentialId.toString() : (tlsDnsCredentials.value[0]?.id.toString() ?? "");
  tlsForm.certPem = null;
  tlsForm.keyPem = null;
  tlsForm.certFileName = "";
  tlsForm.keyFileName = "";
  tlsForm.enabled = cert.enabled;
  tlsUploadError.value = "";
  isTlsModalOpen.value = true;
}

function openAddTlsCredentialModal() {
  resetTlsCredentialForm();
  isTlsCredentialModalOpen.value = true;
}

function resetTlsCredentialForm() {
  tlsCredentialForm.id = "";
  tlsCredentialForm.name = "";
  tlsCredentialForm.cloudflareZoneId = "";
  tlsCredentialForm.apiToken = "";
  tlsCredentialForm.apiTokenSaved = false;
  tlsCredentialForm.enabled = true;
  tlsCredentialError.value = "";
}

function editTlsCredential(credential: PublicTlsDnsCredential) {
  tlsCredentialForm.id = credential.id.toString();
  tlsCredentialForm.name = credential.name;
  tlsCredentialForm.cloudflareZoneId = credential.cloudflareZoneId;
  tlsCredentialForm.apiToken = "";
  tlsCredentialForm.apiTokenSaved = credential.apiTokenSet;
  tlsCredentialForm.enabled = credential.enabled;
  tlsCredentialError.value = "";
  isTlsCredentialModalOpen.value = true;
}

async function handleTlsFileChange(field: TlsFileField, event: Event) {
  tlsUploadError.value = "";
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) {
    if (field === "cert") {
      tlsForm.certPem = null;
      tlsForm.certFileName = "";
    } else {
      tlsForm.keyPem = null;
      tlsForm.keyFileName = "";
    }
    return;
  }

  const bytes = new Uint8Array(await file.arrayBuffer());
  if (field === "cert") {
    tlsForm.certPem = bytes;
    tlsForm.certFileName = file.name;
    return;
  }
  tlsForm.keyPem = bytes;
  tlsForm.keyFileName = file.name;
}

async function run(action: () => Promise<void>) {
  if (!runManagementAction) return;
  await runManagementAction(action);
}

async function deleteBackend(id: bigint) {
  if (!window.confirm("Delete this backend?")) return;
  await run(async () => {
    await managementClient.deletePublicBackend({ id });
  });
}

async function deleteListener(id: bigint) {
  if (!window.confirm("Delete this listener?")) return;
  await run(async () => {
    await managementClient.deletePublicListener({ id });
  });
}

async function setListenerEnabled(listener: PublicListener, enabled: boolean) {
  await run(async () => {
    if (enabled) {
      await managementClient.enablePublicListener({ id: listener.id });
    } else {
      await managementClient.disablePublicListener({ id: listener.id });
    }
  });
}

async function setListenerRunning(listener: PublicListener, running: boolean) {
  await run(async () => {
    if (running) {
      await managementClient.startPublicListener({ id: listener.id });
    } else {
      await managementClient.stopPublicListener({ id: listener.id });
    }
  });
}

async function deleteRoute(id: bigint) {
  if (!window.confirm("Delete this route?")) return;
  await run(async () => {
    await managementClient.deletePublicRoute({ id });
  });
}

async function deleteRateLimitRule(id: bigint) {
  if (!window.confirm("Delete this rate-limit rule?")) return;
  await run(async () => {
    await managementClient.deletePublicRateLimitRule({ id });
  });
}

async function deleteTrafficShaperRule(id: bigint) {
  if (!window.confirm("Delete this traffic-shaper rule?")) return;
  await run(async () => {
    await managementClient.deletePublicTrafficShaperRule({ id });
  });
}

async function submitTlsCertificate() {
  tlsUploadError.value = "";
  if (tlsForm.method === "manual" && !tlsForm.id && (!tlsForm.certPem || !tlsForm.keyPem)) {
    tlsUploadError.value = "Upload both the certificate and private key.";
    return;
  }
  if (tlsForm.method === "manual" && tlsHasPartialUpload.value) {
    tlsUploadError.value = "Upload both files to replace the certificate.";
    return;
  }
  if (tlsForm.method !== "manual" && tlsForm.method !== "dns_01" && tlsForm.hostnamePattern.trim().startsWith("*.")) {
    tlsUploadError.value = "Wildcard certificates require DNS-01.";
    return;
  }

  await run(async () => {
    const isManual = tlsForm.method === "manual";
    const payload = {
      listenerId: BigInt(tlsForm.listenerId || "0"),
      hostnamePattern: tlsForm.hostnamePattern,
      enabled: tlsForm.enabled,
      certPem: isManual ? (tlsForm.certPem ?? new Uint8Array()) : new Uint8Array(),
      keyPem: isManual ? (tlsForm.keyPem ?? new Uint8Array()) : new Uint8Array(),
      source: tlsSourceForMethod(tlsForm.method),
      acmeChallengeType: isManual ? PublicAcmeChallengeType.UNSPECIFIED : acmeChallengeTypeForMethod(tlsForm.method),
      acmeCa: isManual ? PublicAcmeCa.UNSPECIFIED : tlsForm.acmeCa,
      acmeEmail: isManual ? "" : tlsForm.acmeEmail,
      dnsCredentialId: !isManual && tlsForm.method === "dns_01" ? BigInt(tlsForm.dnsCredentialId || "0") : 0n,
    };
    if (tlsForm.id) {
      await managementClient.updatePublicTlsCertificate({ id: BigInt(tlsForm.id), ...payload });
    } else {
      await managementClient.createPublicTlsCertificate(payload);
    }
    isTlsModalOpen.value = false;
  });
}

async function renewTlsCertificate(id: bigint) {
  await run(async () => {
    await managementClient.renewPublicTlsCertificate({ id });
  });
}

async function deleteTlsCertificate(id: bigint) {
  if (!window.confirm("Delete this TLS certificate?")) return;
  await run(async () => {
    await managementClient.deletePublicTlsCertificate({ id });
  });
}

async function submitTlsCredential() {
  tlsCredentialError.value = "";
  if (!tlsCredentialForm.id && !tlsCredentialForm.apiToken.trim()) {
    tlsCredentialError.value = "Enter the Cloudflare API token.";
    return;
  }
  await run(async () => {
    const payload = {
      name: tlsCredentialForm.name,
      provider: PublicDnsProvider.CLOUDFLARE,
      cloudflareZoneId: tlsCredentialForm.cloudflareZoneId,
      apiToken: tlsCredentialForm.apiToken,
      apiTokenSet: tlsCredentialForm.apiToken.trim() !== "",
      enabled: tlsCredentialForm.enabled,
    };
    if (tlsCredentialForm.id) {
      await managementClient.updatePublicTlsDnsCredential({ id: BigInt(tlsCredentialForm.id), ...payload });
    } else {
      await managementClient.createPublicTlsDnsCredential(payload);
    }
    isTlsCredentialModalOpen.value = false;
  });
}

async function deleteTlsCredential(id: bigint) {
  if (!window.confirm("Delete this DNS credential?")) return;
  await run(async () => {
    await managementClient.deletePublicTlsDnsCredential({ id });
  });
}

watch(httpsListeners, () => {
  if (!tlsForm.listenerId && httpsListeners.value[0]) {
    tlsForm.listenerId = httpsListeners.value[0].id.toString();
  }
}, { immediate: true });

watch(tlsDnsCredentials, () => {
  if (!tlsForm.dnsCredentialId && tlsDnsCredentials.value[0]) {
    tlsForm.dnsCredentialId = tlsDnsCredentials.value[0].id.toString();
  }
}, { immediate: true });
</script>

<template>
  <div v-if="dashboard" class="space-y-8">
    <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
      <div>
        <h3 class="mb-2 text-xl font-bold">Proxy Control</h3>
        <p class="text-sm text-[#888]">Public listeners, backends, routes, and TLS mappings.</p>
      </div>
      <div class="flex items-center gap-3">
        <Tag :severity="proxySeverity" :value="proxyStateLabel(proxyState)" class="!bg-[#111] !border-[#333] !text-white" />
        <DisabledHint v-if="!proxyIsRunning" :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
          <Button
            label="Start Proxy"
            class="!bg-white !text-black !border-white"
            :loading="isBusy && !proxyIsRunning"
            :disabled="Boolean(busyDisabledReason)"
            @click="setProxyRunning?.(true)"
          >
            <template #icon><PlusIcon class="h-4 w-4" /></template>
          </Button>
        </DisabledHint>
        <DisabledHint v-else :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
          <DangerButton
            label="Stop Proxy"
            :loading="isBusy && proxyIsRunning"
            :disabled="Boolean(busyDisabledReason)"
            @click="setProxyRunning?.(false)"
          >
            <template #icon><BanIcon class="h-4 w-4" /></template>
          </DangerButton>
        </DisabledHint>
      </div>
    </div>

    <p v-if="proxyError" class="rounded-md border border-red-900/50 bg-red-950/20 px-4 py-3 text-sm text-red-400">
      {{ proxyError }}
    </p>

    <!-- Public Listeners List -->
    <section class="vercel-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Public Listeners</h4>
        <SecondaryButton size="small" label="Add Listener" @click="openAddListenerModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </div>
      <div class="overflow-x-auto">
        <table class="w-full min-w-[900px] text-sm">
          <thead class="border-b border-[#333] text-left text-xs uppercase tracking-wider text-[#888]">
            <tr>
              <th class="px-5 py-3">Name</th>
              <th class="px-5 py-3">Bind</th>
              <th class="px-5 py-3">Protocol</th>
              <th class="px-5 py-3">Backend</th>
              <th class="px-5 py-3">State</th>
              <th class="px-5 py-3 text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="listener in listeners" :key="listener.id.toString()" class="border-b border-[#1f1f1f] last:border-0">
              <td class="px-5 py-4 font-medium text-white">{{ listener.name }}</td>
              <td class="px-5 py-4 font-mono text-xs text-[#d4d4d8]">{{ bindLabel(listener) }}</td>
              <td class="px-5 py-4">{{ protocolLabel(listener.protocol) }}</td>
              <td class="px-5 py-4 text-[#d4d4d8]">{{ backendName(listener.defaultBackendId) }}</td>
              <td class="px-5 py-4">
                <div class="flex flex-col gap-1">
                  <Tag
                    :severity="listener.enabled ? severityForState(listenerState(listener)) : 'warn'"
                    :value="listenerStateLabel(listener)"
                    class="w-fit !bg-[#111] !border-[#333] !text-white"
                  />
                  <span v-if="listenerStatus(listener)?.lastError" class="max-w-[280px] truncate text-xs text-red-400">
                    {{ listenerStatus(listener)?.lastError }}
                  </span>
                </div>
              </td>
              <td class="px-5 py-4">
                <div class="flex justify-end gap-2">
                  <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
                    <SecondaryButton
                      size="small"
                      :aria-label="listener.enabled ? 'Disable listener' : 'Enable listener'"
                      :title="listener.enabled ? 'Disable listener' : 'Enable listener'"
                      :disabled="Boolean(busyDisabledReason)"
                      @click="setListenerEnabled(listener, !listener.enabled)"
                    >
                      <template #icon>
                        <BanIcon v-if="listener.enabled" class="h-3.5 w-3.5" />
                        <CheckIcon v-else class="h-3.5 w-3.5" />
                      </template>
                    </SecondaryButton>
                  </DisabledHint>
                  <DisabledHint :disabled="Boolean(listenerRunningDisabledReason(listener))" :reason="listenerRunningDisabledReason(listener)">
                    <SecondaryButton
                      size="small"
                      :aria-label="listenerStatus(listener)?.running ? 'Stop listener' : 'Start listener'"
                      :title="listenerStatus(listener)?.running ? 'Stop listener' : 'Start listener'"
                      :disabled="Boolean(listenerRunningDisabledReason(listener))"
                      @click="setListenerRunning(listener, !listenerStatus(listener)?.running)"
                    >
                      <template #icon>
                        <TimesIcon v-if="listenerStatus(listener)?.running" class="h-3.5 w-3.5" />
                        <RefreshIcon v-else class="h-3.5 w-3.5" />
                      </template>
                    </SecondaryButton>
                  </DisabledHint>
                  <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
                    <SecondaryButton size="small" aria-label="Edit listener" title="Edit listener" :disabled="Boolean(busyDisabledReason)" @click="editListener(listener)">
                      <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
                    </SecondaryButton>
                  </DisabledHint>
                  <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
                    <DangerButton size="small" aria-label="Delete listener" title="Delete listener" :disabled="Boolean(busyDisabledReason)" @click="deleteListener(listener.id)">
                      <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
                    </DangerButton>
                  </DisabledHint>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- Backends List -->
    <section class="vercel-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Backends</h4>
        <SecondaryButton size="small" label="Add Backend" @click="openAddBackendModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div v-for="backend in backends" :key="backend.id.toString()" class="flex items-center justify-between gap-3 px-5 py-4">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ backend.name }}</p>
              <Tag :value="backendTypeLabel(backend.backendType)" severity="info" class="!bg-[#111] !border-[#333] !text-white" />
              <Tag
                v-if="backend.backendType === PublicBackendType.PROXY_FORWARD"
                :value="forwardModeLabel(backend.forwardMode)"
                severity="info"
                class="!bg-[#111] !border-[#333] !text-white"
              />
              <Tag
                v-if="backend.backendType === PublicBackendType.PROXY_FORWARD && backend.upstreamBasicAuth?.enabled"
                value="Basic auth"
                severity="info"
                class="!bg-[#111] !border-[#333] !text-white"
              />
              <Tag
                v-if="backend.backendType === PublicBackendType.PROXY_FORWARD && upstreamHeaderCount(backend) > 0"
                :value="`${upstreamHeaderCount(backend)} upstream headers`"
                severity="info"
                class="!bg-[#111] !border-[#333] !text-white"
              />
              <Tag v-if="!backend.enabled" value="Disabled" severity="warn" class="!bg-[#111] !border-[#333] !text-white" />
            </div>
            <p class="truncate text-xs text-[#888] mt-1">{{ backendSummary(backend) }}</p>
            <p
              v-if="backend.backendType === PublicBackendType.PROXY_FORWARD && backend.forwardMode === PublicBackendForwardMode.AGENT_POOL"
              class="truncate text-xs text-[#666] mt-1"
            >
              {{ loadBalancingLabel(backend.loadBalancing) }} / {{ backendAgentSummary(backend) }}
            </p>
          </div>
          <div class="flex gap-2">
            <SecondaryButton size="small" aria-label="Edit backend" title="Edit backend" @click="editBackend(backend)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <DangerButton size="small" aria-label="Delete backend" title="Delete backend" @click="deleteBackend(backend.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
      </div>
    </section>

    <!-- Rate Limits List -->
    <section class="vercel-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Rate Limits</h4>
        <SecondaryButton size="small" label="Add Rule" @click="openAddRateLimitRuleModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div v-for="rule in rateLimitRules" :key="rule.id.toString()" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ rule.name }}</p>
              <Tag :value="rateLimitAlgorithmLabel(rule.algorithm)" severity="info" class="!bg-[#111] !border-[#333] !text-white" />
              <Tag v-if="!rule.enabled" value="Disabled" severity="warn" class="!bg-[#111] !border-[#333] !text-white" />
              <Tag :value="`P${rule.priority.toString()}`" severity="info" class="!bg-[#111] !border-[#333] !text-white" />
            </div>
            <p class="mt-1 truncate font-mono text-xs text-[#888]">{{ rateLimitRuleSummary(rule) }} / key {{ rateLimitKeySummary(rule) }}</p>
            <p class="mt-1 truncate text-xs text-[#666]">{{ rateLimitMatchSummary(rule) }} / response {{ rule.responseStatusCode.toString() }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <SecondaryButton size="small" aria-label="Edit rate-limit rule" title="Edit rate-limit rule" @click="editRateLimitRule(rule.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <DangerButton size="small" aria-label="Delete rate-limit rule" title="Delete rate-limit rule" @click="deleteRateLimitRule(rule.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
        <div v-if="!rateLimitRules.length" class="px-5 py-8 text-center text-sm text-[#888]">
          No rate-limit rules configured.
        </div>
      </div>
    </section>

    <!-- Traffic Shaper List -->
    <section class="vercel-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
        <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Traffic Shaper</h4>
        <SecondaryButton size="small" label="Add Shaper" @click="openAddTrafficShaperRuleModal">
          <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
        </SecondaryButton>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div v-for="rule in trafficShaperRules" :key="rule.id.toString()" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ rule.name }}</p>
              <Tag :value="trafficShaperScopeLabel(rule.budgetScope)" severity="info" class="!bg-[#111] !border-[#333] !text-white" />
              <Tag v-if="!rule.enabled" value="Disabled" severity="warn" class="!bg-[#111] !border-[#333] !text-white" />
              <Tag :value="`P${rule.priority.toString()}`" severity="info" class="!bg-[#111] !border-[#333] !text-white" />
            </div>
            <p class="mt-1 truncate font-mono text-xs text-[#888]">{{ trafficShaperRuleSummary(rule) }} / {{ trafficShaperBudgetSummary(rule) }}</p>
            <p class="mt-1 truncate text-xs text-[#666]">{{ trafficShaperMatchSummary(rule) }} / key {{ trafficShaperKeySummary(rule) }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <SecondaryButton size="small" aria-label="Edit traffic-shaper rule" title="Edit traffic-shaper rule" @click="editTrafficShaperRule(rule.id)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <DangerButton size="small" aria-label="Delete traffic-shaper rule" title="Delete traffic-shaper rule" @click="deleteTrafficShaperRule(rule.id)">
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
        <div v-if="!trafficShaperRules.length" class="px-5 py-8 text-center text-sm text-[#888]">
          No traffic-shaper rules configured.
        </div>
      </div>
    </section>

    <!-- Routes and TLS Section -->
    <section class="grid gap-6 lg:grid-cols-2">
      <!-- Routes List -->
      <div class="vercel-card overflow-hidden h-fit">
        <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Routes</h4>
          <SecondaryButton size="small" label="Add Route" @click="openAddRouteModal">
            <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
          </SecondaryButton>
        </div>
        <div class="divide-y divide-[#1f1f1f]">
          <div v-for="route in routes" :key="route.id.toString()" class="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]">
            <div class="min-w-0">
              <div class="flex min-w-0 items-center gap-2">
                <p class="truncate text-sm font-medium text-white">{{ listenerName(route.listenerId) }} -> {{ routeDestinationLabel(route) }}</p>
                <span
                  v-if="routeAction(route) === PublicRouteAction.REDIRECT"
                  class="shrink-0 rounded border border-[#0f766e] px-1.5 py-0.5 text-[0.62rem] font-semibold uppercase tracking-wider text-[#5eead4]"
                >
                  Redirect
                </span>
              </div>
              <p class="truncate font-mono text-xs text-[#888]">
                {{ route.priority.toString() }} / {{ route.hostPattern || "*" }}{{ route.pathPrefix || "/" }}
              </p>
              <p v-if="routeAction(route) === PublicRouteAction.REDIRECT" class="truncate font-mono text-xs text-[#71717a]">
                {{ routeTargetSummary(route) }}
              </p>
            </div>
            <div class="flex gap-2">
              <SecondaryButton size="small" aria-label="Edit route" title="Edit route" @click="editRoute(route.id)">
                <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
              </SecondaryButton>
              <DangerButton size="small" aria-label="Delete route" title="Delete route" @click="deleteRoute(route.id)">
                <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
              </DangerButton>
            </div>
          </div>
        </div>
      </div>

      <!-- TLS Certificates List -->
      <div class="vercel-card overflow-hidden h-fit">
        <div class="border-b border-[#333] px-5 py-4 flex items-center justify-between gap-4">
          <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">TLS Certificates</h4>
          <div class="flex flex-wrap justify-end gap-2">
            <SecondaryButton size="small" label="DNS Credentials" @click="openAddTlsCredentialModal">
              <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <SecondaryButton size="small" label="Add Certificate" @click="openAddTlsModal">
              <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
          </div>
        </div>
        <div class="divide-y divide-[#1f1f1f]">
          <div v-for="cert in tlsCertificates" :key="cert.id.toString()" class="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]">
            <div class="min-w-0">
              <div class="flex min-w-0 items-center gap-2">
                <p class="truncate text-sm font-medium text-white">{{ listenerName(cert.listenerId) }} / {{ cert.hostnamePattern }}</p>
                <Tag v-if="isDefaultSelfSignedCertificate(cert)" value="Self-signed" severity="info" class="!bg-[#111] !border-[#333] !text-white" />
                <Tag v-else :value="tlsSourceLabel(cert)" severity="info" class="!bg-[#111] !border-[#333] !text-white" />
                <Tag :value="tlsStatusLabel(cert)" :severity="tlsStatusSeverity(cert)" class="!bg-[#111] !border-[#333] !text-white" />
              </div>
              <p class="truncate text-xs text-[#888]">{{ tlsCertificateSummary(cert) }}</p>
              <p v-if="cert.source === PublicTlsCertificateSource.ACME && cert.dnsCredentialId" class="truncate text-xs text-[#666]">
                Cloudflare / {{ dnsCredentialName(cert.dnsCredentialId) }}
              </p>
              <p v-if="cert.lastError" class="truncate text-xs text-red-400">{{ cert.lastError }}</p>
            </div>
            <div class="flex gap-2">
              <SecondaryButton
                v-if="cert.source === PublicTlsCertificateSource.ACME"
                size="small"
                aria-label="Renew TLS certificate"
                title="Renew TLS certificate"
                :disabled="Boolean(busyDisabledReason)"
                @click="renewTlsCertificate(cert.id)"
              >
                <template #icon><RefreshIcon class="h-3.5 w-3.5" /></template>
              </SecondaryButton>
              <SecondaryButton size="small" aria-label="Edit TLS mapping" title="Edit TLS mapping" @click="editTlsCertificate(cert.id)">
                <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
              </SecondaryButton>
              <DangerButton size="small" aria-label="Delete TLS mapping" title="Delete TLS mapping" @click="deleteTlsCertificate(cert.id)">
                <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
              </DangerButton>
            </div>
          </div>
          <div v-if="httpsListeners.length && !tlsCertificates.length" class="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]">
            <div class="min-w-0">
              <div class="flex min-w-0 items-center gap-2">
                <p class="truncate text-sm font-medium text-white">{{ httpsListeners[0]?.name ?? "HTTPS listener" }} / p2pstream.local</p>
                <Tag value="Self-signed" severity="info" class="!bg-[#111] !border-[#333] !text-white" />
              </div>
              <p class="truncate text-xs text-[#888]">Runtime fallback certificate</p>
            </div>
          </div>
          <div v-if="tlsDnsCredentials.length" class="border-t border-[#333]">
            <div class="px-5 py-3 text-xs font-semibold uppercase tracking-widest text-[#666]">DNS Credentials</div>
            <div v-for="credential in tlsDnsCredentials" :key="credential.id.toString()" class="grid gap-3 px-5 py-3 sm:grid-cols-[1fr_auto]">
              <div class="min-w-0">
                <div class="flex min-w-0 items-center gap-2">
                  <p class="truncate text-sm font-medium text-white">{{ credential.name }}</p>
                  <Tag value="Cloudflare" severity="info" class="!bg-[#111] !border-[#333] !text-white" />
                  <Tag v-if="!credential.enabled" value="Disabled" severity="warn" class="!bg-[#111] !border-[#333] !text-white" />
                </div>
                <p class="truncate font-mono text-xs text-[#888]">{{ credential.cloudflareZoneId }}</p>
              </div>
              <div class="flex gap-2">
                <SecondaryButton size="small" aria-label="Edit DNS credential" title="Edit DNS credential" @click="editTlsCredential(credential)">
                  <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
                </SecondaryButton>
                <DangerButton size="small" aria-label="Delete DNS credential" title="Delete DNS credential" @click="deleteTlsCredential(credential.id)">
                  <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
                </DangerButton>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>

    <PublicProxyEditorHost ref="editorHost" :config="config" />

    <Modal v-model="isTlsModalOpen" :title="tlsForm.id ? 'Edit TLS Mapping' : 'Add TLS Mapping'" max-width="36rem">
      <form @submit.prevent="submitTlsCertificate" class="grid gap-4">
        <div class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Method
          <div class="grid grid-cols-2 gap-2 sm:grid-cols-4">
            <button
              v-for="method in tlsMethodOptions"
              :key="method.value"
              type="button"
              class="rounded-md border px-2.5 py-2 text-xs font-semibold normal-case tracking-normal transition"
              :class="tlsForm.method === method.value ? 'border-white bg-white text-black' : 'border-[#333] bg-[#0b0b0b] text-[#d4d4d8] hover:border-[#555]'"
              @click="tlsForm.method = method.value"
            >
              {{ method.label }}
            </button>
          </div>
        </div>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          HTTPS listener
          <select v-model="tlsForm.listenerId" class="vercel-input text-sm normal-case tracking-normal" required>
            <option v-for="listener in httpsListeners" :key="listener.id.toString()" :value="listener.id.toString()">{{ listener.name }}</option>
          </select>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Hostname pattern
          <input v-model="tlsForm.hostnamePattern" class="vercel-input text-sm normal-case tracking-normal" placeholder="app.example.com" required />
        </label>
        <div v-if="tlsForm.method !== 'manual'" class="grid gap-3">
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              ACME email
              <input v-model="tlsForm.acmeEmail" class="vercel-input text-sm normal-case tracking-normal" type="email" placeholder="admin@example.com" required />
            </label>
            <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
              CA environment
              <select v-model="tlsForm.acmeCa" class="vercel-input text-sm normal-case tracking-normal">
                <option :value="PublicAcmeCa.LETS_ENCRYPT_PRODUCTION">Let's Encrypt production</option>
                <option :value="PublicAcmeCa.LETS_ENCRYPT_STAGING">Let's Encrypt staging</option>
              </select>
            </label>
          </div>
          <label v-if="tlsForm.method === 'dns_01'" class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Cloudflare credential
            <select v-model="tlsForm.dnsCredentialId" class="vercel-input text-sm normal-case tracking-normal" required>
              <option value="">Select credential</option>
              <option v-for="credential in tlsDnsCredentials" :key="credential.id.toString()" :value="credential.id.toString()">{{ credential.name }}</option>
            </select>
          </label>
        </div>
        <div v-if="tlsForm.method === 'manual'" class="grid gap-3 sm:grid-cols-2">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Certificate file
            <input
              class="vercel-input cursor-pointer text-sm normal-case tracking-normal file:mr-3 file:rounded file:border-0 file:bg-white file:px-3 file:py-1.5 file:text-xs file:font-medium file:text-black"
              type="file"
              accept=".pem,.crt,.cer"
              :required="!tlsForm.id"
              @change="handleTlsFileChange('cert', $event)"
            />
            <span v-if="tlsForm.certFileName" class="truncate text-xs normal-case tracking-normal text-[#d4d4d8]">{{ tlsForm.certFileName }}</span>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Private key file
            <input
              class="vercel-input cursor-pointer text-sm normal-case tracking-normal file:mr-3 file:rounded file:border-0 file:bg-white file:px-3 file:py-1.5 file:text-xs file:font-medium file:text-black"
              type="file"
              accept=".pem,.key"
              :required="!tlsForm.id"
              @change="handleTlsFileChange('key', $event)"
            />
            <span v-if="tlsForm.keyFileName" class="truncate text-xs normal-case tracking-normal text-[#d4d4d8]">{{ tlsForm.keyFileName }}</span>
          </label>
        </div>
        <p v-if="tlsForm.id && tlsForm.method === 'manual'" class="rounded-md border border-[#333] bg-[#0b0b0b] px-3 py-2 text-xs text-[#888]">
          Current certificate is stored in the app config directory.
        </p>
        <p v-if="tlsUploadError" class="rounded-md border border-red-900/50 bg-red-950/20 px-3 py-2 text-sm text-red-400">
          {{ tlsUploadError }}
        </p>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8] mt-2">
          <input v-model="tlsForm.enabled" type="checkbox" class="h-4 w-4 accent-white" />
          Enabled
        </label>
        <div class="mt-4 flex justify-end gap-3">
          <SecondaryButton type="button" label="Cancel" @click="isTlsModalOpen = false" />
          <DisabledHint :disabled="Boolean(tlsSubmitDisabledReason)" :reason="tlsSubmitDisabledReason">
            <Button class="!bg-white !text-black !border-white" :label="tlsForm.id ? 'Save Changes' : 'Create TLS Mapping'" type="submit" :disabled="tlsSubmitDisabled" />
          </DisabledHint>
        </div>
      </form>
    </Modal>

    <Modal v-model="isTlsCredentialModalOpen" :title="tlsCredentialForm.id ? 'Edit DNS Credential' : 'Add DNS Credential'" max-width="32rem">
      <form @submit.prevent="submitTlsCredential" class="grid gap-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Name
          <input v-model="tlsCredentialForm.name" class="vercel-input text-sm normal-case tracking-normal" placeholder="cloudflare-prod" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Cloudflare zone ID
          <input v-model="tlsCredentialForm.cloudflareZoneId" class="vercel-input font-mono text-sm normal-case tracking-normal" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          API token
          <input
            v-model="tlsCredentialForm.apiToken"
            class="vercel-input text-sm normal-case tracking-normal"
            type="password"
            autocomplete="new-password"
            :placeholder="tlsCredentialForm.apiTokenSaved ? 'Saved token' : 'Cloudflare API token'"
            :required="!tlsCredentialForm.id"
          />
        </label>
        <p v-if="tlsCredentialError" class="rounded-md border border-red-900/50 bg-red-950/20 px-3 py-2 text-sm text-red-400">
          {{ tlsCredentialError }}
        </p>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8] mt-2">
          <input v-model="tlsCredentialForm.enabled" type="checkbox" class="h-4 w-4 accent-white" />
          Enabled
        </label>
        <div class="mt-4 flex justify-end gap-3">
          <SecondaryButton type="button" label="Cancel" @click="isTlsCredentialModalOpen = false" />
          <DisabledHint :disabled="Boolean(tlsCredentialSubmitDisabledReason)" :reason="tlsCredentialSubmitDisabledReason">
            <Button class="!bg-white !text-black !border-white" :label="tlsCredentialForm.id ? 'Save Credential' : 'Create Credential'" type="submit" :disabled="Boolean(tlsCredentialSubmitDisabledReason)" />
          </DisabledHint>
        </div>
      </form>
    </Modal>

    <div class="border-t border-[#333] pt-8">
      <h4 class="mb-4 text-sm font-semibold uppercase tracking-widest text-red-500">Danger Zone</h4>
      <div class="vercel-card border-red-900/50 p-6 flex items-center justify-between gap-4">
        <div>
          <p class="font-medium">Reset Session</p>
          <p class="text-sm text-[#888]">This will log you out and clear current dashboard state.</p>
        </div>
        <SecondaryButton label="Log out" class="!border-red-900/50 !text-red-500" @click="logout?.()" />
      </div>
    </div>
  </div>
</template>
