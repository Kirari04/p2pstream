<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import {
  ArrowRight as ArrowIcon,
  KeyRound as KeyIcon,
  Pencil as PencilIcon,
  Plus as PlusIcon,
  Route as RouteIcon,
  ShieldCheck as ShieldIcon,
  Trash2 as TrashIcon,
  Users as UsersIcon,
} from "@lucide/vue";
import { NAlert, NButton, NCheckbox, NDrawer, NDrawerContent, NInput, NInputNumber, NSelect, NTag } from "naive-ui";
import DisabledHint from "@/components/DisabledHint.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementClient } from "@/composables/useManagementClient";
import { BUSY_REASON } from "@/lib/disabledReasons";
import {
  PublicAccessCookieSameSite,
  PublicAccessGroupMatch,
  PublicAccessLocalAuthMode,
  PublicAccessProviderType,
  PublicResponseTemplateKind,
  type GetPublicProxyConfigResponse,
  type PublicAccessPolicy,
  type PublicAccessProvider,
  type PublicAccessUser,
} from "@/gen/proto/p2pstream/v1/management_pb";

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  changed: [];
}>();

const managementClient = useManagementClient();
const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));
const { confirm } = useConfirmDialog();

const providers = computed(() => props.config?.accessProviders ?? []);
const users = computed(() => props.config?.accessUsers ?? []);
const policies = computed(() => props.config?.accessPolicies ?? []);
const localAccessLoginTemplates = computed(() => (props.config?.responseTemplates ?? [])
  .filter((template) => template.kind === PublicResponseTemplateKind.LOCAL_ACCESS_LOGIN_PAGE)
  .sort((left, right) => left.name.localeCompare(right.name)));
const localAccessLoginTemplateOptions = computed(() => localAccessLoginTemplates.value.map((template) => ({
  label: template.name,
  value: template.id.toString(),
})));
const defaultLocalAccessLoginTemplateId = computed(() => (
  localAccessLoginTemplates.value.find((template) => template.name === "local-access-login-default")
    ?? localAccessLoginTemplates.value[0]
)?.id.toString() ?? "");
const protectedRoutes = computed(() => (props.config?.routes ?? []).filter((route) => route.accessPolicyId > 0n));
const enabledPolicies = computed(() => policies.value.filter((policy) => policy.enabled).length);
const providerOptions = computed(() => providers.value.map((provider) => ({
  label: `${provider.name}${provider.enabled ? "" : " · disabled"}`,
  value: provider.id.toString(),
})));

const providerEditorOpen = ref(false);
const policyEditorOpen = ref(false);
const usersDrawerOpen = ref(false);
const userEditorOpen = ref(false);
const managedProviderId = ref("");
const providerForm = reactive({
  id: "",
  name: "",
  enabled: true,
  providerType: PublicAccessProviderType.LOCAL,
  forwardAuthUrl: "",
  timeoutMillis: 5000,
  tlsSkipVerify: false,
  subjectHeader: "X-Auth-Request-Preferred-Username",
  userHeader: "X-Auth-Request-User",
  emailHeader: "X-Auth-Request-Email",
  groupsHeader: "X-Auth-Request-Groups",
  forwardedHeadersText: "X-Auth-Request-User\nX-Auth-Request-Email\nX-Auth-Request-Groups\nX-Auth-Request-Preferred-Username",
  localAuthMode: PublicAccessLocalAuthMode.FORM,
  localAuthSessionHours: 168,
  localAuthRealm: "Restricted",
  localAuthLoginTemplateId: "",
  localAuthAllowedHosts: [] as string[],
  localAuthCookieSameSite: PublicAccessCookieSameSite.LAX,
  localAuthCookieDomain: "",
  localAuthCookieSecure: false,
  localAuthCookieName: "p2pstream_local_auth",
  localAuthLoginUsernameMaxFailures: 5,
  localAuthLoginClientMaxFailures: 25,
  localAuthLoginWindowMinutes: 15,
  localAuthLoginBlockMinutes: 5,
});
const userForm = reactive({
  id: "",
  username: "",
  password: "",
  passwordSet: false,
  enabled: true,
  groups: [] as string[],
});
const policyForm = reactive({
  id: "",
  name: "",
  providerId: "",
  enabled: true,
  requiredGroups: [] as string[],
  groupMatch: PublicAccessGroupMatch.ANY,
});

const groupMatchOptions = [
  { label: "Any listed group", value: PublicAccessGroupMatch.ANY },
  { label: "Every listed group", value: PublicAccessGroupMatch.ALL },
];

const providerTypeOptions = [
  { label: "Local users", value: PublicAccessProviderType.LOCAL },
  { label: "Forward auth", value: PublicAccessProviderType.FORWARD_AUTH },
];

const localAuthModeOptions = [
  { label: "Sign-in form", value: PublicAccessLocalAuthMode.FORM },
  { label: "Sign-in form + HTTP Basic", value: PublicAccessLocalAuthMode.FORM_AND_BASIC },
  { label: "HTTP Basic only", value: PublicAccessLocalAuthMode.BASIC },
];

const localAuthCookieSameSiteOptions = [
  { label: "Lax · recommended", value: PublicAccessCookieSameSite.LAX },
  { label: "Strict · same-site only", value: PublicAccessCookieSameSite.STRICT },
  { label: "None · cross-site", value: PublicAccessCookieSameSite.NONE },
];

const managedProvider = computed(() => providers.value.find((provider) => provider.id.toString() === managedProviderId.value) ?? null);
const managedUsers = computed(() => users.value.filter((user) => user.providerId.toString() === managedProviderId.value));
const suggestedUserGroups = computed(() => normalizedGroupValues(managedUsers.value.flatMap((user) => user.groups)));
const userGroupOptions = computed(() => normalizedGroupValues([
  ...suggestedUserGroups.value,
  ...userForm.groups,
]).map((group) => ({ label: group, value: group })));
const userGroupHint = computed(() => suggestedUserGroups.value.length
  ? `Search ${suggestedUserGroups.value.length.toString()} ${suggestedUserGroups.value.length === 1 ? "group" : "groups"} already used by this provider, or type a new exact group and press Enter.`
  : "No groups exist for this provider yet. Type a new exact group and press Enter.");
const policyProvider = computed(() => providers.value.find((provider) => provider.id.toString() === policyForm.providerId) ?? null);
const suggestedPolicyGroups = computed(() => {
  if (policyProvider.value?.providerType !== PublicAccessProviderType.LOCAL) return [];
  return normalizedGroupValues(users.value
    .filter((user) => user.providerId === policyProvider.value?.id)
    .flatMap((user) => user.groups));
});
const policyGroupOptions = computed(() => normalizedGroupValues([
  ...suggestedPolicyGroups.value,
  ...policyForm.requiredGroups,
]).map((group) => ({ label: group, value: group })));
const policyGroupHint = computed(() => {
  if (policyProvider.value?.providerType !== PublicAccessProviderType.LOCAL) {
    return "Search existing values or type an exact forward-auth group and press Enter. Leave empty to require authentication only.";
  }
  if (!suggestedPolicyGroups.value.length) {
    return "No groups are assigned to this provider's users yet. Type an exact group and press Enter, or leave empty to require authentication only.";
  }
  return `Search ${suggestedPolicyGroups.value.length.toString()} ${suggestedPolicyGroups.value.length === 1 ? "group" : "groups"} assigned to this provider's users, or type a new exact group and press Enter.`;
});
const localAuthHostOptions = computed(() => normalizedHostValues([
  ...(props.config?.routes ?? []).map((route) => route.hostPattern),
  ...providerForm.localAuthAllowedHosts,
]).map((host) => ({ label: host, value: host })));

const providerSaveDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  if (!providerForm.name.trim()) return "Enter a provider name.";
  if (providerForm.providerType === PublicAccessProviderType.FORWARD_AUTH) {
    if (!validForwardAuthURL(providerForm.forwardAuthUrl)) return "Enter an HTTP(S) forward-auth URL without credentials or a fragment.";
    if (providerForm.timeoutMillis < 100 || providerForm.timeoutMillis > 30000) return "Timeout must be between 100 and 30,000 milliseconds.";
    const identityHeaders = [providerForm.subjectHeader, providerForm.userHeader, providerForm.emailHeader, providerForm.groupsHeader];
    if (identityHeaders.some((header) => !validHeaderName(header))) return "Every identity header must be a valid HTTP header name.";
    const forwarded = lineValues(providerForm.forwardedHeadersText);
    if (forwarded.length > 16) return "At most 16 identity headers can be forwarded.";
    if (forwarded.some((header) => !validHeaderName(header) || forbiddenIdentityHeader(header))) {
      return "Forwarded identity headers cannot be authorization, cookie, hop-by-hop, or proxy forwarding headers.";
    }
  } else {
    if (providerForm.localAuthSessionHours < 1 || providerForm.localAuthSessionHours > 720) return "Session lifetime must be between 1 and 720 hours.";
    if (!providerForm.localAuthRealm.trim() || utf8Length(providerForm.localAuthRealm.trim()) > 128) return "The login realm must be 1–128 bytes.";
    if (!providerForm.localAuthLoginTemplateId) return "Choose a sign-in page template.";
    const allowedHosts = normalizedHostValues(providerForm.localAuthAllowedHosts);
    if (allowedHosts.length > 64) return "At most 64 allowed hosts can be configured.";
    if (allowedHosts.some((host) => !validAllowedHost(host))) return "Allowed hosts must be exact hostnames, IP addresses, or *.example.com wildcards without ports.";
    const cookieDomain = normalizeCookieDomain(providerForm.localAuthCookieDomain);
    if (providerForm.localAuthCookieDomain.trim() && !validCookieDomain(cookieDomain)) return "Cookie domain must be a multi-label DNS hostname such as example.com.";
    if (cookieDomain && !allowedHosts.length) return "Set an explicit allowed-host list before sharing cookies across a domain.";
    if (cookieDomain && allowedHosts.some((host) => !hostWithinCookieDomain(host, cookieDomain))) return "Every allowed host must be inside the configured cookie domain.";
    if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,64}$/.test(providerForm.localAuthCookieName.trim())) return "Cookie name must be 1–64 HTTP token characters.";
    if (providerForm.localAuthCookieName.startsWith("__Host-") && (cookieDomain || !providerForm.localAuthCookieSecure)) return "__Host- cookie names require host-only, always-Secure cookies.";
    if (providerForm.localAuthCookieName.startsWith("__Secure-") && !providerForm.localAuthCookieSecure) return "__Secure- cookie names require always-Secure cookies.";
    if (providerForm.localAuthLoginUsernameMaxFailures < 1 || providerForm.localAuthLoginUsernameMaxFailures > 100) return "Account failure limit must be between 1 and 100.";
    if (providerForm.localAuthLoginClientMaxFailures < providerForm.localAuthLoginUsernameMaxFailures || providerForm.localAuthLoginClientMaxFailures > 1000) return "Client failure limit must be between the account limit and 1000.";
    if (providerForm.localAuthLoginWindowMinutes < 1 || providerForm.localAuthLoginWindowMinutes > 1440) return "Failure window must be between 1 minute and 24 hours.";
    if (providerForm.localAuthLoginBlockMinutes < 1 || providerForm.localAuthLoginBlockMinutes > 10080) return "Block duration must be between 1 minute and 7 days.";
  }
  return "";
});

const userSaveDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  const username = userForm.username.trim().toLowerCase();
  if (!/^[a-z0-9_-]{3,64}$/.test(username)) return "Username must be 3–64 lowercase letters, numbers, underscores, or hyphens.";
  if (!userForm.password && !userForm.passwordSet) return "Enter a password.";
  if (userForm.password && utf8Length(userForm.password) < 12) return "Password must be at least 12 bytes.";
  if (utf8Length(userForm.password) > 72) return "Password must be at most 72 bytes.";
  const groups = normalizedGroupValues(userForm.groups);
  if (groups.length > 64) return "At most 64 groups can be assigned.";
  if (groups.some((group) => group.includes(",") || group.includes("\r") || group.includes("\n") || utf8Length(group) > 128)) {
    return "Group names must be 128 bytes or fewer and cannot contain commas or line breaks.";
  }
  return "";
});

const policySaveDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  if (!policyForm.name.trim()) return "Enter a policy name.";
  if (!policyForm.providerId) return "Choose an identity provider.";
  const groups = normalizedGroupValues(policyForm.requiredGroups);
  if (groups.length > 64) return "At most 64 groups can be required.";
  if (groups.some((group) => group.includes(",") || group.includes("\r") || group.includes("\n") || utf8Length(group) > 128)) {
    return "Group names must be 128 bytes or fewer and cannot contain commas or line breaks.";
  }
  return "";
});

const providerUsesInsecureHTTP = computed(() => providerForm.forwardAuthUrl.trim().toLowerCase().startsWith("http://"));

function utf8Length(value: string): number {
  return new TextEncoder().encode(value).length;
}

function lineValues(value: string): string[] {
  return [...new Set(value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean))];
}

function normalizedGroupValues(values: readonly string[]): string[] {
  return [...new Set(values.map((item) => item.trim()).filter(Boolean))]
    .sort((left, right) => left.localeCompare(right));
}

function normalizedHostValues(values: readonly string[]): string[] {
  return [...new Set(values.map((item) => item.trim().toLowerCase().replace(/\.$/, "")).filter(Boolean))]
    .sort((left, right) => left.localeCompare(right));
}

function validAllowedHost(value: string): boolean {
  const host = value.startsWith("*.") ? value.slice(2) : value;
  if (!host || host.length > 253 || /[/?#@]/.test(host)) return false;
  if (host.includes(":")) return !value.startsWith("*.") && /^\[?[0-9a-f:]+\]?$/.test(host);
  return host.split(".").every((label) => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label));
}

function normalizeCookieDomain(value: string): string {
  return value.trim().toLowerCase().replace(/^\./, "").replace(/\.$/, "");
}

function validCookieDomain(value: string): boolean {
  return value.includes(".") && validAllowedHost(value) && !value.startsWith("*.") && !value.includes(":");
}

function hostWithinCookieDomain(value: string, domain: string): boolean {
  const host = value.replace(/^\*\./, "").replace(/^\[/, "").replace(/\]$/, "");
  return host === domain || host.endsWith(`.${domain}`);
}

function validForwardAuthURL(value: string): boolean {
  try {
    const parsed = new URL(value.trim());
    return (parsed.protocol === "http:" || parsed.protocol === "https:")
      && Boolean(parsed.host)
      && parsed.username === ""
      && parsed.password === ""
      && parsed.hash === "";
  } catch {
    return false;
  }
}

function validHeaderName(value: string): boolean {
  return /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(value.trim()) && value.trim().length <= 128;
}

function forbiddenIdentityHeader(value: string): boolean {
  const name = value.trim().toLowerCase();
  return name.startsWith("x-forwarded-") || [
    "authorization", "cookie", "set-cookie", "forwarded", "host", "connection", "content-length", "expect", "keep-alive",
    "proxy-authenticate", "proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade", "x-real-ip",
  ].includes(name);
}

function resetProviderForm() {
  Object.assign(providerForm, {
    id: "",
    name: "",
    enabled: true,
    providerType: PublicAccessProviderType.LOCAL,
    forwardAuthUrl: "",
    timeoutMillis: 5000,
    tlsSkipVerify: false,
    subjectHeader: "X-Auth-Request-Preferred-Username",
    userHeader: "X-Auth-Request-User",
    emailHeader: "X-Auth-Request-Email",
    groupsHeader: "X-Auth-Request-Groups",
    forwardedHeadersText: "X-Auth-Request-User\nX-Auth-Request-Email\nX-Auth-Request-Groups\nX-Auth-Request-Preferred-Username",
    localAuthMode: PublicAccessLocalAuthMode.FORM,
    localAuthSessionHours: 168,
    localAuthRealm: "Restricted",
    localAuthLoginTemplateId: defaultLocalAccessLoginTemplateId.value,
    localAuthAllowedHosts: [],
    localAuthCookieSameSite: PublicAccessCookieSameSite.LAX,
    localAuthCookieDomain: "",
    localAuthCookieSecure: false,
    localAuthCookieName: "p2pstream_local_auth",
    localAuthLoginUsernameMaxFailures: 5,
    localAuthLoginClientMaxFailures: 25,
    localAuthLoginWindowMinutes: 15,
    localAuthLoginBlockMinutes: 5,
  });
}

function openCreateProvider() {
  resetProviderForm();
  providerEditorOpen.value = true;
}

function openEditProvider(provider: PublicAccessProvider) {
  Object.assign(providerForm, {
    id: provider.id.toString(),
    name: provider.name,
    enabled: provider.enabled,
    providerType: provider.providerType || PublicAccessProviderType.FORWARD_AUTH,
    forwardAuthUrl: provider.forwardAuthUrl,
    timeoutMillis: Number(provider.timeoutMillis || 5000n),
    tlsSkipVerify: provider.tlsSkipVerify,
    subjectHeader: provider.subjectHeader,
    userHeader: provider.userHeader,
    emailHeader: provider.emailHeader,
    groupsHeader: provider.groupsHeader,
    forwardedHeadersText: provider.forwardedHeaders.join("\n"),
    localAuthMode: provider.localAuthMode || PublicAccessLocalAuthMode.FORM,
    localAuthSessionHours: Number(provider.localAuthSessionDurationMillis || 604800000n) / 3_600_000,
    localAuthRealm: provider.localAuthRealm || "Restricted",
    localAuthLoginTemplateId: provider.localAuthLoginTemplateId > 0n
      ? provider.localAuthLoginTemplateId.toString()
      : defaultLocalAccessLoginTemplateId.value,
    localAuthAllowedHosts: [...provider.localAuthAllowedHosts],
    localAuthCookieSameSite: provider.localAuthCookieSameSite || PublicAccessCookieSameSite.LAX,
    localAuthCookieDomain: provider.localAuthCookieDomain,
    localAuthCookieSecure: provider.localAuthCookieSecure,
    localAuthCookieName: provider.localAuthCookieName || "p2pstream_local_auth",
    localAuthLoginUsernameMaxFailures: Number(provider.localAuthLoginUsernameMaxFailures || 5n),
    localAuthLoginClientMaxFailures: Number(provider.localAuthLoginClientMaxFailures || 25n),
    localAuthLoginWindowMinutes: Number(provider.localAuthLoginWindowMillis || 900000n) / 60_000,
    localAuthLoginBlockMinutes: Number(provider.localAuthLoginBlockMillis || 300000n) / 60_000,
  });
  providerEditorOpen.value = true;
}

function resetUserForm() {
  Object.assign(userForm, {
    id: "",
    username: "",
    password: "",
    passwordSet: false,
    enabled: true,
    groups: [],
  });
}

function openManageUsers(provider: PublicAccessProvider) {
  managedProviderId.value = provider.id.toString();
  usersDrawerOpen.value = true;
}

function openCreateUser() {
  resetUserForm();
  userEditorOpen.value = true;
}

function openEditUser(user: PublicAccessUser) {
  Object.assign(userForm, {
    id: user.id.toString(),
    username: user.username,
    password: "",
    passwordSet: user.passwordSet,
    enabled: user.enabled,
    groups: [...user.groups],
  });
  userEditorOpen.value = true;
}

function resetPolicyForm() {
  Object.assign(policyForm, {
    id: "",
    name: "",
    providerId: providers.value[0]?.id.toString() ?? "",
    enabled: true,
    requiredGroups: [],
    groupMatch: PublicAccessGroupMatch.ANY,
  });
}

function openCreatePolicy() {
  resetPolicyForm();
  policyEditorOpen.value = true;
}

function openEditPolicy(policy: PublicAccessPolicy) {
  Object.assign(policyForm, {
    id: policy.id.toString(),
    name: policy.name,
    providerId: policy.providerId.toString(),
    enabled: policy.enabled,
    requiredGroups: [...policy.requiredGroups],
    groupMatch: policy.groupMatch || PublicAccessGroupMatch.ANY,
  });
  policyEditorOpen.value = true;
}

async function run(action: () => Promise<void>, message: string): Promise<boolean> {
  if (!runManagementAction) return false;
  const ok = await runManagementAction(action, message);
  if (ok) emit("changed");
  return ok;
}

async function saveProvider() {
  if (providerSaveDisabledReason.value) return;
  const payload = {
    name: providerForm.name.trim(),
    providerType: providerForm.providerType,
    enabled: providerForm.enabled,
    forwardAuthUrl: providerForm.forwardAuthUrl.trim(),
    timeoutMillis: BigInt(providerForm.timeoutMillis),
    tlsSkipVerify: providerForm.tlsSkipVerify,
    subjectHeader: providerForm.subjectHeader.trim(),
    userHeader: providerForm.userHeader.trim(),
    emailHeader: providerForm.emailHeader.trim(),
    groupsHeader: providerForm.groupsHeader.trim(),
    forwardedHeaders: lineValues(providerForm.forwardedHeadersText),
    localAuthMode: providerForm.localAuthMode,
    localAuthSessionDurationMillis: BigInt(Math.round(providerForm.localAuthSessionHours * 3_600_000)),
    localAuthRealm: providerForm.localAuthRealm.trim(),
    localAuthLoginTemplateId: providerForm.localAuthLoginTemplateId ? BigInt(providerForm.localAuthLoginTemplateId) : 0n,
    localAuthAllowedHosts: normalizedHostValues(providerForm.localAuthAllowedHosts),
    localAuthCookieSameSite: providerForm.localAuthCookieSameSite,
    localAuthCookieDomain: normalizeCookieDomain(providerForm.localAuthCookieDomain),
    localAuthCookieSecure: providerForm.localAuthCookieSecure || providerForm.localAuthCookieSameSite === PublicAccessCookieSameSite.NONE,
    localAuthCookieName: providerForm.localAuthCookieName.trim(),
    localAuthLoginUsernameMaxFailures: BigInt(Math.round(providerForm.localAuthLoginUsernameMaxFailures)),
    localAuthLoginClientMaxFailures: BigInt(Math.round(providerForm.localAuthLoginClientMaxFailures)),
    localAuthLoginWindowMillis: BigInt(Math.round(providerForm.localAuthLoginWindowMinutes * 60_000)),
    localAuthLoginBlockMillis: BigInt(Math.round(providerForm.localAuthLoginBlockMinutes * 60_000)),
  };
  const ok = await run(async () => {
    if (providerForm.id) {
      await managementClient.updatePublicAccessProvider({
        id: BigInt(providerForm.id),
        ...payload,
        localAuthSecuritySettingsPresent: true,
      });
    } else {
      await managementClient.createPublicAccessProvider(payload);
    }
  }, providerForm.id ? "Identity provider updated" : "Identity provider created");
  if (ok) providerEditorOpen.value = false;
}

async function saveUser() {
  if (userSaveDisabledReason.value || !managedProvider.value) return;
  const payload = {
    username: userForm.username.trim().toLowerCase(),
    password: userForm.password,
    enabled: userForm.enabled,
    groups: normalizedGroupValues(userForm.groups),
  };
  const ok = await run(async () => {
    if (userForm.id) {
      await managementClient.updatePublicAccessUser({ id: BigInt(userForm.id), ...payload });
    } else {
      await managementClient.createPublicAccessUser({ providerId: managedProvider.value!.id, ...payload });
    }
  }, userForm.id ? "Local user updated and sessions revoked" : "Local user created");
  if (ok) {
    userForm.password = "";
    userEditorOpen.value = false;
  }
}

async function deleteUser(user: PublicAccessUser) {
  if (!await confirm("Delete Local User", `${user.username} will lose access immediately and all sessions will be deleted.`)) return;
  await run(async () => {
    await managementClient.deletePublicAccessUser({ id: user.id });
  }, "Local user deleted");
}

async function savePolicy() {
  if (policySaveDisabledReason.value) return;
  const payload = {
    name: policyForm.name.trim(),
    providerId: BigInt(policyForm.providerId),
    enabled: policyForm.enabled,
    requiredGroups: normalizedGroupValues(policyForm.requiredGroups),
    groupMatch: policyForm.groupMatch,
  };
  const ok = await run(async () => {
    if (policyForm.id) {
      await managementClient.updatePublicAccessPolicy({ id: BigInt(policyForm.id), ...payload });
    } else {
      await managementClient.createPublicAccessPolicy(payload);
    }
  }, policyForm.id ? "Access policy updated" : "Access policy created");
  if (ok) policyEditorOpen.value = false;
}

async function deleteProvider(provider: PublicAccessProvider) {
  const usedBy = policies.value.filter((policy) => policy.providerId === provider.id).length;
  if (!await confirm(
    "Delete Identity Provider",
    usedBy
      ? `${provider.name} is used by ${usedBy.toString()} access ${usedBy === 1 ? "policy" : "policies"}. Reassign or delete those policies first.`
      : `${provider.name} will no longer be available for access checks.`,
  )) return;
  await run(async () => {
    await managementClient.deletePublicAccessProvider({ id: provider.id });
  }, "Identity provider deleted");
}

async function deletePolicy(policy: PublicAccessPolicy) {
  const usedBy = routeCountForPolicy(policy.id);
  if (!await confirm(
    "Delete Access Policy",
    usedBy
      ? `${policy.name} protects ${usedBy.toString()} ${usedBy === 1 ? "route" : "routes"}. Remove those route assignments first.`
      : `${policy.name} will be permanently removed.`,
  )) return;
  await run(async () => {
    await managementClient.deletePublicAccessPolicy({ id: policy.id });
  }, "Access policy deleted");
}

function providerName(providerId: bigint): string {
  return providers.value.find((provider) => provider.id === providerId)?.name ?? `Provider #${providerId.toString()}`;
}

function isLocalProvider(provider: PublicAccessProvider): boolean {
  return provider.providerType === PublicAccessProviderType.LOCAL;
}

function localUserCount(providerId: bigint): number {
  return users.value.filter((user) => user.providerId === providerId).length;
}

function localAuthModeLabel(mode: PublicAccessLocalAuthMode): string {
  if (mode === PublicAccessLocalAuthMode.BASIC) return "HTTP Basic";
  if (mode === PublicAccessLocalAuthMode.FORM_AND_BASIC) return "Form + Basic";
  return "Sign-in form";
}

function cookieSameSiteLabel(value: PublicAccessCookieSameSite): string {
  if (value === PublicAccessCookieSameSite.STRICT) return "Strict";
  if (value === PublicAccessCookieSameSite.NONE) return "None";
  return "Lax";
}

function localAccessLoginTemplateName(templateId: bigint): string {
  return localAccessLoginTemplates.value.find((template) => template.id === templateId)?.name ?? "Sign-in template unavailable";
}

function routeCountForPolicy(policyId: bigint): number {
  return protectedRoutes.value.filter((route) => route.accessPolicyId === policyId).length;
}

function providerDeleteDisabledReason(provider: PublicAccessProvider): string {
  if (isBusy.value) return BUSY_REASON;
  const usedBy = policies.value.filter((policy) => policy.providerId === provider.id).length;
  return usedBy ? `Reassign or delete the ${usedBy.toString()} dependent ${usedBy === 1 ? "policy" : "policies"} first.` : "";
}

function policyDeleteDisabledReason(policy: PublicAccessPolicy): string {
  if (isBusy.value) return BUSY_REASON;
  const usedBy = routeCountForPolicy(policy.id);
  return usedBy ? `Remove this policy from ${usedBy.toString()} protected ${usedBy === 1 ? "route" : "routes"} first.` : "";
}

function groupSummary(policy: PublicAccessPolicy): string {
  if (!policy.requiredGroups.length) return "Authenticated identity";
  const joiner = policy.groupMatch === PublicAccessGroupMatch.ALL ? " + " : " or ";
  return policy.requiredGroups.join(joiner);
}
</script>

<template>
  <section class="access-control surface-card" aria-labelledby="access-control-heading">
    <div class="access-control__header surface-card__header">
      <div class="layout-grid space-xs min-width-zero">
        <div class="layout-row align-center space-sm">
          <ShieldIcon class="icon-sm accent-text" aria-hidden="true" />
          <h2 id="access-control-heading" class="copy-base weight-semibold">Identity-aware access</h2>
        </div>
        <p class="copy-sm line-normal muted-text">
          Protect routes with built-in local users or an external identity service, then enforce optional group membership before routing.
        </p>
      </div>
      <NTag size="small" :bordered="false" :type="protectedRoutes.length ? 'success' : 'default'">
        {{ protectedRoutes.length.toString() }} protected {{ protectedRoutes.length === 1 ? 'route' : 'routes' }}
      </NTag>
    </div>

    <div class="access-control__flow pad-x-xl pad-y-lg divider-bottom" aria-label="Access-control request flow">
      <div class="access-control__flow-node">
        <KeyIcon class="icon-sm" aria-hidden="true" />
        <span><strong>Provider</strong><small>verifies the visitor</small></span>
      </div>
      <ArrowIcon class="icon-sm muted-text" aria-hidden="true" />
      <div class="access-control__flow-node">
        <ShieldIcon class="icon-sm" aria-hidden="true" />
        <span><strong>Policy</strong><small>checks required groups</small></span>
      </div>
      <ArrowIcon class="icon-sm muted-text" aria-hidden="true" />
      <div class="access-control__flow-node">
        <RouteIcon class="icon-sm" aria-hidden="true" />
        <span><strong>Route</strong><small>receives trusted identity headers</small></span>
      </div>
    </div>

    <div class="access-control__notice pad-x-xl pad-y-lg divider-bottom">
      <NAlert type="info" :show-icon="false">
        Access checks fail closed. Client-supplied identity headers are removed, local and external identities are injected only after successful authentication, and protected responses bypass shared cache storage.
      </NAlert>
    </div>

    <div class="access-control__section layout-grid space-lg pad-x-xl pad-y-xl divider-bottom">
      <div class="layout-row wrap-items align-start spread-items space-lg">
        <div class="layout-grid space-xs">
          <h3 class="copy-sm weight-semibold">Identity providers</h3>
          <p class="copy-xs line-normal muted-text">Use local accounts for a self-contained setup, or connect oauth2-proxy, Authelia, Authentik, or your own forward-auth service.</p>
        </div>
        <NButton secondary size="small" :disabled="isBusy" @click="openCreateProvider">
          <template #icon><PlusIcon class="icon-sm" /></template>
          Add provider
        </NButton>
      </div>

      <div v-if="providers.length" class="access-control__list divided-list">
        <article v-for="provider in providers" :key="provider.id.toString()" class="access-control__row">
          <div class="layout-grid space-xs min-width-zero">
            <div class="layout-row wrap-items align-center space-sm">
              <h4 class="copy-sm weight-semibold wrap-anywhere">{{ provider.name }}</h4>
              <NTag size="small" :bordered="false" type="info">{{ isLocalProvider(provider) ? 'Local users' : 'Forward auth' }}</NTag>
              <NTag size="small" :bordered="false" :type="provider.enabled ? 'success' : 'warning'">
                {{ provider.enabled ? 'Enabled' : 'Disabled' }}
              </NTag>
              <NTag v-if="provider.tlsSkipVerify" size="small" :bordered="false" type="error">TLS verification off</NTag>
            </div>
            <template v-if="isLocalProvider(provider)">
              <p class="copy-xs muted-text">{{ localAuthModeLabel(provider.localAuthMode) }} · {{ localUserCount(provider.id).toString() }} {{ localUserCount(provider.id) === 1 ? 'user' : 'users' }} · {{ Math.round(Number(provider.localAuthSessionDurationMillis) / 3_600_000).toString() }} hour sessions</p>
              <p class="copy-xs muted-text">Sign-in page · {{ localAccessLoginTemplateName(provider.localAuthLoginTemplateId) }}</p>
              <p class="copy-xs muted-text">Login protection · {{ (provider.localAuthLoginUsernameMaxFailures || 5n).toString() }}/account · {{ (provider.localAuthLoginClientMaxFailures || 25n).toString() }}/client · {{ Math.round(Number(provider.localAuthLoginBlockMillis || 300000n) / 60_000).toString() }} min block</p>
              <p class="copy-xs muted-text">{{ provider.localAuthAllowedHosts.length ? provider.localAuthAllowedHosts.join(' · ') : 'Any routed host' }} · SameSite {{ cookieSameSiteLabel(provider.localAuthCookieSameSite) }}{{ provider.localAuthCookieDomain ? ` · Domain ${provider.localAuthCookieDomain}` : '' }} · {{ provider.localAuthCookieName }}</p>
            </template>
            <template v-else>
              <p class="copy-xs mono-text wrap-anywhere muted-text">{{ provider.forwardAuthUrl }}</p>
              <p class="copy-xs muted-text">{{ provider.timeoutMillis.toString() }} ms timeout · {{ provider.forwardedHeaders.length.toString() }} trusted headers</p>
            </template>
          </div>
          <div class="access-control__actions">
            <NButton v-if="isLocalProvider(provider)" secondary size="small" :disabled="isBusy" :aria-label="`Manage local users for ${provider.name}`" @click="openManageUsers(provider)">
              <template #icon><UsersIcon class="icon-sm" /></template>
              Users
            </NButton>
            <NButton secondary size="small" :disabled="isBusy" :aria-label="`Edit identity provider ${provider.name}`" @click="openEditProvider(provider)">
              <template #icon><PencilIcon class="icon-sm" /></template>
            </NButton>
            <DisabledHint :disabled="Boolean(providerDeleteDisabledReason(provider))" :reason="providerDeleteDisabledReason(provider)">
              <NButton type="error" size="small" :disabled="Boolean(providerDeleteDisabledReason(provider))" :aria-label="`Delete identity provider ${provider.name}`" @click="deleteProvider(provider)">
                <template #icon><TrashIcon class="icon-sm" /></template>
              </NButton>
            </DisabledHint>
          </div>
        </article>
      </div>
      <p v-else class="access-control__empty copy-xs line-normal muted-text">
        No identity provider configured. Add local users or a forward-auth endpoint before creating an access policy.
      </p>
    </div>

    <div class="access-control__section layout-grid space-lg pad-x-xl pad-y-xl">
      <div class="layout-row wrap-items align-start spread-items space-lg">
        <div class="layout-grid space-xs">
          <div class="layout-row align-center space-sm">
            <h3 class="copy-sm weight-semibold">Access policies</h3>
            <NTag size="small" :bordered="false">{{ enabledPolicies.toString() }}/{{ policies.length.toString() }} active</NTag>
          </div>
          <p class="copy-xs line-normal muted-text">Policies are reusable. Assign one from a route editor to protect that route.</p>
        </div>
        <DisabledHint :disabled="!providers.length || isBusy" :reason="!providers.length ? 'Create an identity provider first.' : BUSY_REASON">
          <NButton type="primary" size="small" :disabled="!providers.length || isBusy" @click="openCreatePolicy">
            <template #icon><PlusIcon class="icon-sm" /></template>
            Add policy
          </NButton>
        </DisabledHint>
      </div>

      <div v-if="policies.length" class="access-control__list divided-list">
        <article v-for="policy in policies" :key="policy.id.toString()" class="access-control__row">
          <div class="layout-grid space-xs min-width-zero">
            <div class="layout-row wrap-items align-center space-sm">
              <h4 class="copy-sm weight-semibold wrap-anywhere">{{ policy.name }}</h4>
              <NTag size="small" :bordered="false" :type="policy.enabled ? 'success' : 'warning'">
                {{ policy.enabled ? 'Enabled' : 'Disabled' }}
              </NTag>
              <NTag size="small" :bordered="false" type="info">
                {{ routeCountForPolicy(policy.id).toString() }} {{ routeCountForPolicy(policy.id) === 1 ? 'route' : 'routes' }}
              </NTag>
            </div>
            <p class="copy-xs muted-text">{{ providerName(policy.providerId) }} · {{ groupSummary(policy) }}</p>
          </div>
          <div class="access-control__actions">
            <NButton secondary size="small" :disabled="isBusy" :aria-label="`Edit access policy ${policy.name}`" @click="openEditPolicy(policy)">
              <template #icon><PencilIcon class="icon-sm" /></template>
            </NButton>
            <DisabledHint :disabled="Boolean(policyDeleteDisabledReason(policy))" :reason="policyDeleteDisabledReason(policy)">
              <NButton type="error" size="small" :disabled="Boolean(policyDeleteDisabledReason(policy))" :aria-label="`Delete access policy ${policy.name}`" @click="deletePolicy(policy)">
                <template #icon><TrashIcon class="icon-sm" /></template>
              </NButton>
            </DisabledHint>
          </div>
        </article>
      </div>
      <p v-else class="access-control__empty copy-xs line-normal muted-text">
        No access policies yet. A policy can require authentication only, any listed group, or every listed group.
      </p>
    </div>
  </section>

  <NDrawer v-model:show="providerEditorOpen" placement="right" width="min(100vw, 46rem)" class="editor-drawer" :aria-label="providerForm.id ? 'Edit Identity Provider' : 'Add Identity Provider'">
    <NDrawerContent :title="providerForm.id ? 'Edit Identity Provider' : 'Add Identity Provider'" closable>
      <form class="editor-drawer-form layout-grid space-xl" @submit.prevent="saveProvider">
        <NAlert v-if="providerForm.providerType === PublicAccessProviderType.FORWARD_AUTH" type="info" :show-icon="false">
          The proxy sends a bodyless GET with the browser Authorization and Cookie headers plus trustworthy X-Forwarded-* request context. Redirect responses are returned to the browser; only 2xx responses grant access.
        </NAlert>
        <NAlert v-else type="info" :show-icon="false">
          Local users are stored here with bcrypt-hashed passwords. Form login creates revocable, HTTP-only sessions; HTTP Basic uses the same accounts for clients that need it.
        </NAlert>

        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Name
          <NInput v-model:value="providerForm.name" size="small" :maxlength="64" placeholder="company-sso" />
        </label>

        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Provider type
          <NSelect v-model:value="providerForm.providerType" size="small" :options="providerTypeOptions" :disabled="Boolean(providerForm.id)" />
          <span class="normal-text letter-normal weight-normal line-normal muted-text">Provider type cannot be changed after creation; create a new provider to migrate authentication methods.</span>
        </label>

        <template v-if="providerForm.providerType === PublicAccessProviderType.FORWARD_AUTH">
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Forward-auth URL
          <NInput v-model:value="providerForm.forwardAuthUrl" size="small" class="mono-text" placeholder="https://auth.example.com/oauth2/auth" />
        </label>

        <NAlert v-if="providerUsesInsecureHTTP" type="warning" :show-icon="false">
          HTTP exposes browser credentials and identity headers on the network. Use it only across a trusted local transport.
        </NAlert>

        <div class="layout-grid space-lg mq-sm-cols-two">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Timeout (ms)
            <NInputNumber v-model:value="providerForm.timeoutMillis" :show-button="false" size="small" :min="100" :max="30000" />
          </label>
          <div class="layout-grid space-sm self-align-end">
            <NCheckbox v-model:checked="providerForm.tlsSkipVerify">Skip TLS certificate verification</NCheckbox>
          </div>
        </div>

        <section class="layout-grid space-lg round-md framed frame-standard muted-bg pad-lg">
          <div>
            <h3 class="copy-sm weight-semibold">Identity response headers</h3>
            <p class="margin-top-xs copy-xs line-normal muted-text">Header names read from a successful auth response. Groups are comma-separated.</p>
          </div>
          <div class="layout-grid space-lg mq-sm-cols-two">
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Subject
              <NInput v-model:value="providerForm.subjectHeader" size="small" class="mono-text" />
            </label>
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Username
              <NInput v-model:value="providerForm.userHeader" size="small" class="mono-text" />
            </label>
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Email
              <NInput v-model:value="providerForm.emailHeader" size="small" class="mono-text" />
            </label>
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Groups
              <NInput v-model:value="providerForm.groupsHeader" size="small" class="mono-text" />
            </label>
          </div>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Headers forwarded to the protected upstream
            <NInput v-model:value="providerForm.forwardedHeadersText" type="textarea" class="mono-text" :autosize="{ minRows: 4, maxRows: 12 }" />
            <span class="normal-text letter-normal weight-normal line-normal muted-text">One header per line. Client values are removed before these trusted auth-response values are injected.</span>
          </label>
        </section>
        </template>

        <template v-else>
          <section class="layout-grid space-lg round-md framed frame-standard muted-bg pad-lg">
            <div>
              <h3 class="copy-sm weight-semibold">Login experience</h3>
              <p class="margin-top-xs copy-xs line-normal muted-text">The sign-in form is the easiest browser experience. Enable Basic as well only when API clients need it.</p>
            </div>
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Login mode
              <NSelect v-model:value="providerForm.localAuthMode" size="small" :options="localAuthModeOptions" />
            </label>
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Sign-in page
              <AccessibleSelect
                :value="providerForm.localAuthLoginTemplateId"
                accessible-label="Local access sign-in page template"
                size="small"
                filterable
                :options="localAccessLoginTemplateOptions"
                :disabled="!localAccessLoginTemplateOptions.length"
                @update:value="providerForm.localAuthLoginTemplateId = String($event ?? '')"
              />
              <span class="normal-text letter-normal weight-normal line-normal muted-text">
                Customize sign-in and authentication-error pages under <RouterLink to="/templates">Templates</RouterLink>.
              </span>
            </label>
            <div class="layout-grid space-lg mq-sm-cols-two">
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Session lifetime (hours)
                <NInputNumber v-model:value="providerForm.localAuthSessionHours" :show-button="false" size="small" :min="1" :max="720" />
              </label>
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                HTTP Basic realm
                <NInput v-model:value="providerForm.localAuthRealm" size="small" :maxlength="128" placeholder="Restricted" />
              </label>
            </div>
          </section>
          <section class="layout-grid space-lg round-md framed frame-standard muted-bg pad-lg">
            <div>
              <h3 class="copy-sm weight-semibold">Login protection</h3>
              <p class="margin-top-xs copy-xs line-normal muted-text">Bound password guessing by account and client address. These limits always apply to both the sign-in form and HTTP Basic.</p>
            </div>
            <div class="layout-grid space-lg mq-sm-cols-two">
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Failures per account
                <NInputNumber v-model:value="providerForm.localAuthLoginUsernameMaxFailures" :show-button="false" size="small" :min="1" :max="100" />
                <span class="normal-text letter-normal weight-normal line-normal muted-text">Per username and client address.</span>
              </label>
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Failures per client
                <NInputNumber v-model:value="providerForm.localAuthLoginClientMaxFailures" :show-button="false" size="small" :min="providerForm.localAuthLoginUsernameMaxFailures" :max="1000" />
                <span class="normal-text letter-normal weight-normal line-normal muted-text">Across every username attempted by one client.</span>
              </label>
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Failure window (minutes)
                <NInputNumber v-model:value="providerForm.localAuthLoginWindowMinutes" :show-button="false" size="small" :min="1" :max="1440" />
                <span class="normal-text letter-normal weight-normal line-normal muted-text">The failure count resets after this window ends.</span>
              </label>
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Block duration (minutes)
                <NInputNumber v-model:value="providerForm.localAuthLoginBlockMinutes" :show-button="false" size="small" :min="1" :max="10080" />
                <span class="normal-text letter-normal weight-normal line-normal muted-text">Returned through Retry-After when the limit is reached.</span>
              </label>
            </div>
            <div class="layout-row wrap-items space-sm" aria-label="Fixed login protections">
              <NTag size="small" :bordered="false" type="success">Account + client buckets</NTag>
              <NTag size="small" :bordered="false" type="success">Concurrent attempts counted</NTag>
              <NTag size="small" :bordered="false" type="success">Cannot be disabled</NTag>
            </div>
          </section>
          <section class="layout-grid space-lg round-md framed frame-standard muted-bg pad-lg">
            <div>
              <h3 class="copy-sm weight-semibold">Browser boundary</h3>
              <p class="margin-top-xs copy-xs line-normal muted-text">Limit where this provider can authenticate and control how its short-lived CSRF and session cookies travel.</p>
            </div>
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Allowed hosts
              <AccessibleSelect
                v-model:value="providerForm.localAuthAllowedHosts"
                accessible-label="Allowed local authentication hosts"
                :options="localAuthHostOptions"
                multiple
                filterable
                tag
                clearable
                max-tag-count="responsive"
                size="small"
                placeholder="Search or add hosts"
              />
              <span class="normal-text letter-normal weight-normal line-normal muted-text">Exact hosts and leading wildcards are supported, for example <span class="mono-text">localhost</span> or <span class="mono-text">*.example.com</span>. Empty allows every host already matched by an assigned route.</span>
            </label>
            <div class="layout-grid space-lg mq-sm-cols-two">
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                SameSite policy
                <NSelect v-model:value="providerForm.localAuthCookieSameSite" size="small" :options="localAuthCookieSameSiteOptions" />
              </label>
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Cookie domain
                <NInput v-model:value="providerForm.localAuthCookieDomain" size="small" class="mono-text" placeholder="Host-only (recommended)" />
              </label>
            </div>
            <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
              Cookie name prefix
              <NInput v-model:value="providerForm.localAuthCookieName" size="small" class="mono-text" :maxlength="64" placeholder="p2pstream_local_auth" />
              <span class="normal-text letter-normal weight-normal line-normal muted-text">Rotate this value to recover from stale or conflicting browser cookies. Provider and CSRF suffixes are added automatically.</span>
            </label>
            <NCheckbox v-model:checked="providerForm.localAuthCookieSecure" :disabled="providerForm.localAuthCookieSameSite === PublicAccessCookieSameSite.NONE">
              Always mark cookies Secure
            </NCheckbox>
            <p class="copy-xs line-normal muted-text">HTTPS listeners enable Secure automatically. Force it when an external TLS layer makes the browser-facing request HTTPS.</p>
            <NAlert v-if="providerForm.localAuthCookieSameSite === PublicAccessCookieSameSite.NONE" type="warning" :show-icon="false">
              SameSite=None is only accepted by browsers with Secure cookies and therefore requires HTTPS. Browser-source validation and the one-time form nonce still apply.
            </NAlert>
            <NAlert v-else-if="providerForm.localAuthCookieDomain" type="warning" :show-icon="false">
              A cookie domain shares the session with every matching subdomain. Keep the allowed-host list narrow and prefer host-only cookies unless cross-subdomain sign-in is intentional.
            </NAlert>
            <div class="layout-row wrap-items space-sm" aria-label="Fixed cookie protections">
              <NTag size="small" :bordered="false" type="success">HttpOnly always on</NTag>
              <NTag size="small" :bordered="false" type="success">Browser source checked</NTag>
              <NTag size="small" :bordered="false" type="success">One-time form nonce</NTag>
            </div>
          </section>
          <NAlert type="warning" :show-icon="false">
            Serve protected routes over HTTPS. Form and Basic credentials are otherwise visible to anyone who can observe the network.
          </NAlert>
        </template>

        <NCheckbox v-model:checked="providerForm.enabled">Enable provider</NCheckbox>

        <p v-if="providerSaveDisabledReason" class="copy-xs line-normal muted-text" role="status">Provider cannot be saved: {{ providerSaveDisabledReason }}</p>
        <div class="editor-drawer-actions layout-row align-end-row space-md">
          <NButton secondary attr-type="button" @click="providerEditorOpen = false">Cancel</NButton>
          <DisabledHint :disabled="Boolean(providerSaveDisabledReason)" :reason="providerSaveDisabledReason">
            <NButton type="primary" attr-type="submit" :disabled="Boolean(providerSaveDisabledReason)">
              {{ providerForm.id ? 'Save changes' : 'Create provider' }}
            </NButton>
          </DisabledHint>
        </div>
      </form>
    </NDrawerContent>
  </NDrawer>

  <NDrawer v-model:show="usersDrawerOpen" placement="right" width="min(100vw, 46rem)" class="editor-drawer" :aria-label="managedProvider ? `Local users for ${managedProvider.name}` : 'Local users'">
    <NDrawerContent :title="managedProvider ? `${managedProvider.name} · Local users` : 'Local users'" closable>
      <div class="editor-drawer-form layout-grid space-xl">
        <NAlert type="info" :show-icon="false">
          Users and groups belong to this provider. Passwords are write-only, and editing or disabling a user revokes every active form session immediately.
        </NAlert>
        <div class="layout-row wrap-items align-start spread-items space-lg">
          <div class="layout-grid space-xs">
            <h3 class="copy-sm weight-semibold">Accounts</h3>
            <p class="copy-xs muted-text">{{ managedUsers.length.toString() }} configured {{ managedUsers.length === 1 ? 'user' : 'users' }}</p>
          </div>
          <NButton type="primary" size="small" :disabled="isBusy" @click="openCreateUser">
            <template #icon><PlusIcon class="icon-sm" /></template>
            Add user
          </NButton>
        </div>

        <div v-if="managedUsers.length" class="access-control__list divided-list">
          <article v-for="user in managedUsers" :key="user.id.toString()" class="access-control__row">
            <div class="layout-grid space-xs min-width-zero">
              <div class="layout-row wrap-items align-center space-sm">
                <h4 class="copy-sm weight-semibold mono-text">{{ user.username }}</h4>
                <NTag size="small" :bordered="false" :type="user.enabled ? 'success' : 'warning'">{{ user.enabled ? 'Enabled' : 'Disabled' }}</NTag>
              </div>
              <p class="copy-xs muted-text">{{ user.groups.length ? user.groups.join(' · ') : 'No groups' }}</p>
            </div>
            <div class="access-control__actions">
              <NButton secondary size="small" :disabled="isBusy" :aria-label="`Edit local user ${user.username}`" @click="openEditUser(user)">
                <template #icon><PencilIcon class="icon-sm" /></template>
              </NButton>
              <NButton type="error" size="small" :disabled="isBusy" :aria-label="`Delete local user ${user.username}`" @click="deleteUser(user)">
                <template #icon><TrashIcon class="icon-sm" /></template>
              </NButton>
            </div>
          </article>
        </div>
        <p v-else class="access-control__empty copy-xs line-normal muted-text">No local users yet. Protected routes fail closed until at least one enabled account exists.</p>
      </div>
    </NDrawerContent>
  </NDrawer>

  <NDrawer v-model:show="userEditorOpen" placement="right" width="min(100vw, 38rem)" class="editor-drawer" :aria-label="userForm.id ? 'Edit Local User' : 'Add Local User'">
    <NDrawerContent :title="userForm.id ? 'Edit Local User' : 'Add Local User'" closable>
      <form class="editor-drawer-form layout-grid space-xl" @submit.prevent="saveUser">
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Username
          <NInput v-model:value="userForm.username" size="small" :maxlength="64" autocomplete="off" placeholder="alice" />
          <span class="normal-text letter-normal weight-normal line-normal muted-text">Lowercase letters, numbers, underscores, and hyphens.</span>
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          {{ userForm.passwordSet ? 'New password' : 'Password' }}
          <NInput v-model:value="userForm.password" type="password" size="small" :maxlength="72" autocomplete="new-password" :placeholder="userForm.passwordSet ? 'Leave blank to keep the current password' : 'At least 12 characters'" />
          <span class="normal-text letter-normal weight-normal line-normal muted-text">{{ userForm.passwordSet ? 'Setting a new password revokes all existing sessions.' : 'Use 12–72 bytes.' }}</span>
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Groups
          <AccessibleSelect
            v-model:value="userForm.groups"
            accessible-label="Local user groups"
            :options="userGroupOptions"
            multiple
            filterable
            tag
            clearable
            max-tag-count="responsive"
            size="small"
            placeholder="Search or add groups"
          />
          <span class="normal-text letter-normal weight-normal line-normal muted-text">{{ userGroupHint }} Policies match these values exactly and case-sensitively.</span>
        </label>
        <NCheckbox v-model:checked="userForm.enabled">Enable user</NCheckbox>
        <p v-if="userSaveDisabledReason" class="copy-xs line-normal muted-text" role="status">User cannot be saved: {{ userSaveDisabledReason }}</p>
        <div class="editor-drawer-actions layout-row align-end-row space-md">
          <NButton secondary attr-type="button" @click="userEditorOpen = false">Cancel</NButton>
          <DisabledHint :disabled="Boolean(userSaveDisabledReason)" :reason="userSaveDisabledReason">
            <NButton type="primary" attr-type="submit" :disabled="Boolean(userSaveDisabledReason)">{{ userForm.id ? 'Save and revoke sessions' : 'Create user' }}</NButton>
          </DisabledHint>
        </div>
      </form>
    </NDrawerContent>
  </NDrawer>

  <NDrawer v-model:show="policyEditorOpen" placement="right" width="min(100vw, 40rem)" class="editor-drawer" :aria-label="policyForm.id ? 'Edit Access Policy' : 'Add Access Policy'">
    <NDrawerContent :title="policyForm.id ? 'Edit Access Policy' : 'Add Access Policy'" closable>
      <form class="editor-drawer-form layout-grid space-xl" @submit.prevent="savePolicy">
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Name
          <NInput v-model:value="policyForm.name" size="small" :maxlength="64" placeholder="staff-only" />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Identity provider
          <NSelect v-model:value="policyForm.providerId" size="small" :options="providerOptions" :input-props="{ 'aria-label': 'Access policy identity provider' }" />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Required groups
          <AccessibleSelect
            v-model:value="policyForm.requiredGroups"
            accessible-label="Required access groups"
            :options="policyGroupOptions"
            multiple
            filterable
            tag
            clearable
            max-tag-count="responsive"
            size="small"
            placeholder="Search or add groups"
          />
          <span class="normal-text letter-normal weight-normal line-normal muted-text">{{ policyGroupHint }}</span>
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Group requirement
          <NSelect v-model:value="policyForm.groupMatch" size="small" :options="groupMatchOptions" :disabled="!policyForm.requiredGroups.length" :input-props="{ 'aria-label': 'Access policy group requirement' }" />
        </label>
        <NCheckbox v-model:checked="policyForm.enabled">Enable policy</NCheckbox>

        <NAlert type="warning" :show-icon="false">
          Disabling a policy or its provider denies requests to every assigned route with 503. Remove the assignment from a route to make that route public.
        </NAlert>

        <p v-if="policySaveDisabledReason" class="copy-xs line-normal muted-text" role="status">Policy cannot be saved: {{ policySaveDisabledReason }}</p>
        <div class="editor-drawer-actions layout-row align-end-row space-md">
          <NButton secondary attr-type="button" @click="policyEditorOpen = false">Cancel</NButton>
          <DisabledHint :disabled="Boolean(policySaveDisabledReason)" :reason="policySaveDisabledReason">
            <NButton type="primary" attr-type="submit" :disabled="Boolean(policySaveDisabledReason)">
              {{ policyForm.id ? 'Save changes' : 'Create policy' }}
            </NButton>
          </DisabledHint>
        </div>
      </form>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.access-control__header {
  align-items: center;
}

.access-control__flow {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 0.75rem;
  background: var(--app-panel-muted);
}

.access-control__flow-node {
  display: flex;
  align-items: center;
  gap: 0.65rem;
  min-width: 0;
  padding: 0.7rem 0.9rem;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-panel);
}

.access-control__flow-node span {
  display: grid;
  gap: 0.1rem;
}

.access-control__flow-node small {
  color: var(--app-text-muted);
  font-size: 0.7rem;
}

.access-control__notice {
  background: var(--app-panel);
}

.access-control__list {
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-panel);
}

.access-control__row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 1rem;
}

.access-control__actions {
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 0.5rem;
}

.access-control__empty {
  padding: 1rem;
  border: 1px dashed var(--app-border);
  border-radius: 8px;
  background: var(--app-panel-muted);
}

@media (max-width: 47.99rem) {
  .access-control__header,
  .access-control__row {
    align-items: flex-start;
    flex-direction: column;
  }

  .access-control__flow {
    align-items: stretch;
    flex-direction: column;
  }

  .access-control__flow > svg {
    align-self: center;
    transform: rotate(90deg);
  }
}
</style>
