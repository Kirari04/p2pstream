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
} from "@lucide/vue";
import { NAlert, NButton, NCheckbox, NDrawer, NDrawerContent, NInput, NInputNumber, NSelect, NTag } from "naive-ui";
import DisabledHint from "@/components/DisabledHint.vue";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementClient } from "@/composables/useManagementClient";
import { BUSY_REASON } from "@/lib/disabledReasons";
import {
  PublicAccessGroupMatch,
  PublicAccessProviderType,
  type GetPublicProxyConfigResponse,
  type PublicAccessPolicy,
  type PublicAccessProvider,
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
const policies = computed(() => props.config?.accessPolicies ?? []);
const protectedRoutes = computed(() => (props.config?.routes ?? []).filter((route) => route.accessPolicyId > 0n));
const enabledPolicies = computed(() => policies.value.filter((policy) => policy.enabled).length);
const providerOptions = computed(() => providers.value.map((provider) => ({
  label: `${provider.name}${provider.enabled ? "" : " · disabled"}`,
  value: provider.id.toString(),
})));

const providerEditorOpen = ref(false);
const policyEditorOpen = ref(false);
const providerForm = reactive({
  id: "",
  name: "",
  enabled: true,
  forwardAuthUrl: "",
  timeoutMillis: 5000,
  tlsSkipVerify: false,
  subjectHeader: "X-Auth-Request-Preferred-Username",
  userHeader: "X-Auth-Request-User",
  emailHeader: "X-Auth-Request-Email",
  groupsHeader: "X-Auth-Request-Groups",
  forwardedHeadersText: "X-Auth-Request-User\nX-Auth-Request-Email\nX-Auth-Request-Groups\nX-Auth-Request-Preferred-Username",
});
const policyForm = reactive({
  id: "",
  name: "",
  providerId: "",
  enabled: true,
  requiredGroupsText: "",
  groupMatch: PublicAccessGroupMatch.ANY,
});

const groupMatchOptions = [
  { label: "Any listed group", value: PublicAccessGroupMatch.ANY },
  { label: "Every listed group", value: PublicAccessGroupMatch.ALL },
];

const providerSaveDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  if (!providerForm.name.trim()) return "Enter a provider name.";
  if (!validForwardAuthURL(providerForm.forwardAuthUrl)) return "Enter an HTTP(S) forward-auth URL without credentials or a fragment.";
  if (providerForm.timeoutMillis < 100 || providerForm.timeoutMillis > 30000) return "Timeout must be between 100 and 30,000 milliseconds.";
  const identityHeaders = [providerForm.subjectHeader, providerForm.userHeader, providerForm.emailHeader, providerForm.groupsHeader];
  if (identityHeaders.some((header) => !validHeaderName(header))) return "Every identity header must be a valid HTTP header name.";
  const forwarded = lineValues(providerForm.forwardedHeadersText);
  if (forwarded.length > 16) return "At most 16 identity headers can be forwarded.";
  if (forwarded.some((header) => !validHeaderName(header) || forbiddenIdentityHeader(header))) {
    return "Forwarded identity headers cannot be authorization, cookie, hop-by-hop, or proxy forwarding headers.";
  }
  return "";
});

const policySaveDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  if (!policyForm.name.trim()) return "Enter a policy name.";
  if (!policyForm.providerId) return "Choose an identity provider.";
  const groups = lineValues(policyForm.requiredGroupsText);
  if (groups.length > 64) return "At most 64 groups can be required.";
  if (groups.some((group) => group.includes(",") || group.length > 128)) return "Group names must be 128 characters or fewer and cannot contain commas.";
  return "";
});

const providerUsesInsecureHTTP = computed(() => providerForm.forwardAuthUrl.trim().toLowerCase().startsWith("http://"));

function lineValues(value: string): string[] {
  return [...new Set(value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean))];
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
    forwardAuthUrl: "",
    timeoutMillis: 5000,
    tlsSkipVerify: false,
    subjectHeader: "X-Auth-Request-Preferred-Username",
    userHeader: "X-Auth-Request-User",
    emailHeader: "X-Auth-Request-Email",
    groupsHeader: "X-Auth-Request-Groups",
    forwardedHeadersText: "X-Auth-Request-User\nX-Auth-Request-Email\nX-Auth-Request-Groups\nX-Auth-Request-Preferred-Username",
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
    forwardAuthUrl: provider.forwardAuthUrl,
    timeoutMillis: Number(provider.timeoutMillis || 5000n),
    tlsSkipVerify: provider.tlsSkipVerify,
    subjectHeader: provider.subjectHeader,
    userHeader: provider.userHeader,
    emailHeader: provider.emailHeader,
    groupsHeader: provider.groupsHeader,
    forwardedHeadersText: provider.forwardedHeaders.join("\n"),
  });
  providerEditorOpen.value = true;
}

function resetPolicyForm() {
  Object.assign(policyForm, {
    id: "",
    name: "",
    providerId: providers.value[0]?.id.toString() ?? "",
    enabled: true,
    requiredGroupsText: "",
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
    requiredGroupsText: policy.requiredGroups.join("\n"),
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
    providerType: PublicAccessProviderType.FORWARD_AUTH,
    enabled: providerForm.enabled,
    forwardAuthUrl: providerForm.forwardAuthUrl.trim(),
    timeoutMillis: BigInt(providerForm.timeoutMillis),
    tlsSkipVerify: providerForm.tlsSkipVerify,
    subjectHeader: providerForm.subjectHeader.trim(),
    userHeader: providerForm.userHeader.trim(),
    emailHeader: providerForm.emailHeader.trim(),
    groupsHeader: providerForm.groupsHeader.trim(),
    forwardedHeaders: lineValues(providerForm.forwardedHeadersText),
  };
  const ok = await run(async () => {
    if (providerForm.id) {
      await managementClient.updatePublicAccessProvider({ id: BigInt(providerForm.id), ...payload });
    } else {
      await managementClient.createPublicAccessProvider(payload);
    }
  }, providerForm.id ? "Identity provider updated" : "Identity provider created");
  if (ok) providerEditorOpen.value = false;
}

async function savePolicy() {
  if (policySaveDisabledReason.value) return;
  const payload = {
    name: policyForm.name.trim(),
    providerId: BigInt(policyForm.providerId),
    enabled: policyForm.enabled,
    requiredGroups: lineValues(policyForm.requiredGroupsText),
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
          Ask an external identity service to authenticate each protected request, then enforce optional group membership before routing.
        </p>
      </div>
      <NTag size="small" :bordered="false" :type="protectedRoutes.length ? 'success' : 'default'">
        {{ protectedRoutes.length.toString() }} protected {{ protectedRoutes.length === 1 ? 'route' : 'routes' }}
      </NTag>
    </div>

    <div class="access-control__flow pad-x-xl pad-y-lg divider-bottom" aria-label="Access-control request flow">
      <div class="access-control__flow-node">
        <KeyIcon class="icon-sm" aria-hidden="true" />
        <span><strong>Forward auth</strong><small>verifies the browser session</small></span>
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
        Access checks fail closed. The proxy strips configured identity headers supplied by the client and injects only values returned by the successful auth service. Protected responses bypass shared cache storage.
      </NAlert>
    </div>

    <div class="access-control__section layout-grid space-lg pad-x-xl pad-y-xl divider-bottom">
      <div class="layout-row wrap-items align-start spread-items space-lg">
        <div class="layout-grid space-xs">
          <h3 class="copy-sm weight-semibold">Identity providers</h3>
          <p class="copy-xs line-normal muted-text">Compatible with forward-auth endpoints from oauth2-proxy, Authelia, Authentik, or your own service.</p>
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
              <NTag size="small" :bordered="false" type="info">Forward auth</NTag>
              <NTag size="small" :bordered="false" :type="provider.enabled ? 'success' : 'warning'">
                {{ provider.enabled ? 'Enabled' : 'Disabled' }}
              </NTag>
              <NTag v-if="provider.tlsSkipVerify" size="small" :bordered="false" type="error">TLS verification off</NTag>
            </div>
            <p class="copy-xs mono-text wrap-anywhere muted-text">{{ provider.forwardAuthUrl }}</p>
            <p class="copy-xs muted-text">{{ provider.timeoutMillis.toString() }} ms timeout · {{ provider.forwardedHeaders.length.toString() }} trusted headers</p>
          </div>
          <div class="access-control__actions">
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
        No identity provider configured. Add a forward-auth endpoint before creating an access policy.
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
        <NAlert type="info" :show-icon="false">
          The proxy sends a bodyless GET with the browser Authorization and Cookie headers plus trustworthy X-Forwarded-* request context. Redirect responses are returned to the browser; only 2xx responses grant access.
        </NAlert>

        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Name
          <NInput v-model:value="providerForm.name" size="small" :maxlength="64" placeholder="company-sso" />
        </label>

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
            <NCheckbox v-model:checked="providerForm.enabled">Enable provider</NCheckbox>
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
          <NInput v-model:value="policyForm.requiredGroupsText" type="textarea" class="mono-text" :autosize="{ minRows: 4, maxRows: 12 }" placeholder="engineering&#10;operators" />
          <span class="normal-text letter-normal weight-normal line-normal muted-text">One exact, case-sensitive group per line. Leave empty to require authentication without group restrictions.</span>
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Group requirement
          <NSelect v-model:value="policyForm.groupMatch" size="small" :options="groupMatchOptions" :disabled="!lineValues(policyForm.requiredGroupsText).length" :input-props="{ 'aria-label': 'Access policy group requirement' }" />
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
