<script setup lang="ts">
import { computed, inject, reactive, ref, watch } from "vue";
import {
  Cloud as CloudIcon,
  Database as DatabaseIcon,
  KeyRound as KeyIcon,
  Network as NetworkIcon,
  Pencil as PencilIcon,
  Plus as PlusIcon,
  RefreshCw as RefreshIcon,
  ShieldCheck as ShieldIcon,
  Trash2 as TrashIcon,
} from "@lucide/vue";
import { NAlert, NButton, NCheckbox, NDrawer, NDrawerContent, NInput, NSelect, NTag } from "naive-ui";
import DisabledHint from "@/components/DisabledHint.vue";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementClient } from "@/composables/useManagementClient";
import { BUSY_REASON } from "@/lib/disabledReasons";
import {
  customTrustedProxyFormFromProto,
  customTrustedProxyPayloadFromForm,
  customTrustedProxyValidationReason,
  defaultCustomTrustedProxyForm,
  geoIpSettingsFormFromProto,
  geoIpSettingsPayloadFromForm,
  geoIpSettingsValidationReason,
  type CustomTrustedProxyForm,
  type GeoIpSettingsForm,
} from "@/lib/publicVisitorIdentityForm";
import {
  PublicTrustedProxyHeaderMode,
  PublicTrustedProxyProvider,
  type GetPublicProxyConfigResponse,
  type PublicTrustedProxySource,
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

const geoIpSettings = computed(() => props.config?.geoIpSettings);
const geoIpStatus = computed(() => geoIpSettings.value?.databaseStatus);
const sources = computed(() => props.config?.trustedProxySources ?? []);
const builtInSources = computed(() => sources.value.filter((source) => source.builtIn));
const customSources = computed(() => sources.value.filter((source) => !source.builtIn));
const enabledSources = computed(() => sources.value.filter((source) => source.enabled));
const refreshStaleAfterMillis = 24 * 60 * 60 * 1000;
const geoDatabaseStale = computed(() => Boolean(
  geoIpSettings.value?.enabled
  && geoIpStatus.value?.ready
  && isRefreshStale(geoIpStatus.value.lastUpdateSuccessAtUnixMillis),
));
const trustSummary = computed(() => enabledSources.value.length
  ? `${enabledSources.value.length.toString()} trusted ${enabledSources.value.length === 1 ? "source" : "sources"}`
  : "Direct peers only");

const geoForm = reactive(geoIpSettingsFormFromProto());
const geoFormDirty = ref(false);
let geoFormSyncing = false;
const proxyForm = reactive<CustomTrustedProxyForm>(defaultCustomTrustedProxyForm());
const proxyEditorOpen = ref(false);

const geoSaveDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  return geoIpSettingsValidationReason(geoForm);
});
const proxySaveDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  return customTrustedProxyValidationReason(proxyForm);
});
const geoRefreshDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  if (geoFormDirty.value) return "Save or discard the pending GeoIP changes first.";
  if (!geoIpSettings.value?.maxmindAccountId || !geoIpSettings.value.maxmindLicenseKeySet) {
    return "Save valid MaxMind credentials first.";
  }
  return "";
});
const headerModeOptions = [
  { label: "Single IP", value: PublicTrustedProxyHeaderMode.SINGLE_IP },
  { label: "Trusted chain", value: PublicTrustedProxyHeaderMode.TRUSTED_CHAIN },
];

watch(geoIpSettings, (settings) => {
  if (!geoFormDirty.value) syncGeoForm(geoIpSettingsFormFromProto(settings));
}, { immediate: true });
watch(geoForm, () => {
  if (!geoFormSyncing) geoFormDirty.value = true;
}, { deep: true, flush: "sync" });

function syncGeoForm(value: GeoIpSettingsForm) {
  geoFormSyncing = true;
  Object.assign(geoForm, value);
  geoFormDirty.value = false;
  geoFormSyncing = false;
}

function discardGeoIpChanges() {
  syncGeoForm(geoIpSettingsFormFromProto(geoIpSettings.value));
}

function openCreateProxy() {
  Object.assign(proxyForm, defaultCustomTrustedProxyForm());
  proxyEditorOpen.value = true;
}

function openEditProxy(source: PublicTrustedProxySource) {
  Object.assign(proxyForm, customTrustedProxyFormFromProto(source));
  proxyEditorOpen.value = true;
}

function closeProxyEditor() {
  proxyEditorOpen.value = false;
}

async function run(action: () => Promise<void>, successMessage?: string): Promise<boolean> {
  if (!runManagementAction) return false;
  const ok = await runManagementAction(action, successMessage);
  if (ok) emit("changed");
  return ok;
}

async function saveGeoIpSettings() {
  if (geoSaveDisabledReason.value) return;
  const payload = geoIpSettingsPayloadFromForm(geoForm);
  const ok = await run(async () => {
    await managementClient.updatePublicGeoIpSettings(payload);
  }, payload.enabled ? "GeoIP settings saved" : "GeoIP disabled");
  if (ok) {
    syncGeoForm({
      enabled: payload.enabled,
      maxmindAccountId: payload.maxmindAccountId,
      maxmindLicenseKey: "",
      licenseKeySaved: payload.clearLicenseKey
        ? false
        : geoForm.licenseKeySaved || Boolean(payload.maxmindLicenseKey),
      clearLicenseKey: false,
    });
  }
}

async function refreshGeoIpDatabase() {
  if (geoRefreshDisabledReason.value) return;
  await run(async () => {
    await managementClient.refreshPublicGeoIpDatabase({});
  }, "GeoIP database refreshed");
}

async function setSourceEnabled(source: PublicTrustedProxySource, enabled: boolean) {
  await run(async () => {
    await managementClient.updatePublicTrustedProxySource({
      id: source.id,
      name: source.name,
      enabled,
      cidrs: source.builtIn ? [] : source.cidrs,
      headerName: source.headerName,
      headerMode: source.headerMode,
    });
  }, enabled ? `${source.name} trusted` : `${source.name} trust disabled`);
}

async function refreshSource(source: PublicTrustedProxySource) {
  await run(async () => {
    await managementClient.refreshPublicTrustedProxySource({ id: source.id });
  }, `${source.name} ranges refreshed`);
}

async function saveProxy() {
  if (proxySaveDisabledReason.value) return;
  const payload = customTrustedProxyPayloadFromForm(proxyForm);
  const ok = await run(async () => {
    if (proxyForm.id) {
      await managementClient.updatePublicTrustedProxySource({ id: BigInt(proxyForm.id), ...payload });
    } else {
      await managementClient.createPublicTrustedProxySource(payload);
    }
  }, proxyForm.id ? "Trusted proxy updated" : "Trusted proxy created");
  if (ok) proxyEditorOpen.value = false;
}

async function deleteProxy(source: PublicTrustedProxySource) {
  if (!await confirm(
    "Delete Trusted Proxy Source",
    `${source.name} will no longer be allowed to supply visitor identity. Requests from its peers will use their network address instead.`,
  )) return;
  await run(async () => {
    await managementClient.deletePublicTrustedProxySource({ id: source.id });
  }, "Trusted proxy deleted");
}

function providerLabel(provider: PublicTrustedProxyProvider): string {
  switch (provider) {
    case PublicTrustedProxyProvider.CLOUDFLARE: return "Cloudflare";
    case PublicTrustedProxyProvider.BUNNY: return "Bunny CDN";
    case PublicTrustedProxyProvider.CLOUDFRONT: return "Amazon CloudFront";
    default: return "Custom proxy";
  }
}

function providerDescription(provider: PublicTrustedProxyProvider): string {
  switch (provider) {
    case PublicTrustedProxyProvider.CLOUDFLARE:
      return "Uses managed Cloudflare edge ranges and strict CF-Connecting-IP parsing.";
    case PublicTrustedProxyProvider.BUNNY:
      return "Uses managed Bunny edge ranges and strict X-Real-IP parsing.";
    case PublicTrustedProxyProvider.CLOUDFRONT:
      return "Uses AWS CloudFront ranges and resolves a trusted X-Forwarded-For chain.";
    default:
      return "Uses only the peer networks and client-IP header configured here.";
  }
}

function headerModeLabel(mode: PublicTrustedProxyHeaderMode): string {
  return mode === PublicTrustedProxyHeaderMode.TRUSTED_CHAIN ? "Trusted chain" : "Single IP";
}

function formatTimestamp(value?: bigint): string {
  if (!value || value <= 0n) return "Never";
  const date = new Date(Number(value));
  return Number.isNaN(date.getTime()) ? "Unknown" : date.toLocaleString();
}

function isRefreshStale(value?: bigint): boolean {
  if (!value || value <= 0n) return true;
  return Date.now() - Number(value) > refreshStaleAfterMillis;
}

function sourceRangesStale(source: PublicTrustedProxySource): boolean {
  return source.enabled && source.cidrCount > 0n && isRefreshStale(source.lastRefreshSuccessAtUnixMillis);
}
</script>

<template>
  <section class="visitor-identity surface-card" aria-labelledby="visitor-identity-heading">
    <div class="visitor-identity__header surface-card__header">
      <div class="layout-grid space-xs min-width-zero">
        <div class="layout-row align-center space-sm">
          <ShieldIcon class="icon-sm accent-text" aria-hidden="true" />
          <h2 id="visitor-identity-heading" class="copy-base weight-semibold">Visitor identity &amp; GeoIP</h2>
        </div>
        <p class="copy-sm line-normal muted-text">
          Resolve the visitor address before WAF, rate limits, traffic shaping, captcha verification, and upstream forwarding.
        </p>
      </div>
      <NTag size="small" :bordered="false" :type="enabledSources.length ? 'info' : 'default'">{{ trustSummary }}</NTag>
    </div>

    <div class="visitor-identity__notice pad-x-xl pad-y-lg divider-bottom">
      <NAlert type="info" :show-icon="false">
        <strong>No proxy or CDN is trusted by default.</strong>
        Client-IP headers are ignored unless the network peer belongs to an enabled source below. Conflicting or malformed trusted headers resolve to an unknown visitor.
      </NAlert>
    </div>

    <div class="visitor-identity__section layout-grid space-lg pad-x-xl pad-y-xl divider-bottom">
      <div class="layout-row wrap-items align-start spread-items space-lg">
        <div class="layout-grid space-xs">
          <div class="layout-row align-center space-sm">
            <DatabaseIcon class="icon-sm muted-text" aria-hidden="true" />
            <h3 class="copy-sm weight-semibold">GeoLite2 Country</h3>
            <NTag
              size="small"
              :bordered="false"
              :type="geoIpSettings?.enabled && geoIpStatus?.ready ? 'success' : geoIpSettings?.enabled ? 'warning' : 'default'"
            >
              {{ !geoIpSettings?.enabled ? 'Disabled' : geoIpStatus?.ready ? 'Ready' : 'Not ready' }}
            </NTag>
          </div>
          <p class="copy-xs line-normal muted-text">
            Lookups stay local. MaxMind credentials are used only to maintain the country database; the license key is never returned by the API.
          </p>
        </div>
        <DisabledHint :disabled="Boolean(geoRefreshDisabledReason)" :reason="geoRefreshDisabledReason">
          <NButton secondary size="small" :disabled="Boolean(geoRefreshDisabledReason)" @click="refreshGeoIpDatabase">
            <template #icon><RefreshIcon class="icon-sm" /></template>
            Refresh database
          </NButton>
        </DisabledHint>
      </div>

      <NAlert v-if="geoIpStatus?.lastUpdateError" type="warning" :show-icon="false" class="wrap-anywhere">
        <strong>Last refresh failed.</strong> {{ geoIpStatus.lastUpdateError }}
        <span v-if="geoIpStatus.ready"> The last valid database remains active.</span>
      </NAlert>
      <NAlert v-if="geoDatabaseStale" type="warning" :show-icon="false">
        <strong>The country database has not refreshed successfully in more than 24 hours.</strong>
        The last valid database remains active; check credentials and outbound access if the next automatic refresh also fails.
      </NAlert>
      <p v-if="geoRefreshDisabledReason" class="copy-xs line-normal muted-text" role="status">
        Database refresh unavailable: {{ geoRefreshDisabledReason }}
      </p>

      <dl class="visitor-identity__facts" aria-label="GeoIP database status">
        <div>
          <dt>Database</dt>
          <dd>{{ geoIpStatus?.databaseType || 'No database loaded' }}</dd>
        </div>
        <div>
          <dt>Build date</dt>
          <dd>{{ formatTimestamp(geoIpStatus?.buildAtUnixMillis) }}</dd>
        </div>
        <div>
          <dt>Last success</dt>
          <dd>{{ formatTimestamp(geoIpStatus?.lastUpdateSuccessAtUnixMillis) }}</dd>
        </div>
        <div>
          <dt>Last attempt</dt>
          <dd>{{ formatTimestamp(geoIpStatus?.lastUpdateAttemptAtUnixMillis) }}</dd>
        </div>
      </dl>

      <form class="layout-grid space-lg" @submit.prevent="saveGeoIpSettings">
        <div class="layout-grid space-lg mq-sm-cols-two">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            MaxMind account ID
            <NInput v-model:value="geoForm.maxmindAccountId" size="small" :maxlength="64" autocomplete="off" placeholder="Account ID" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            MaxMind license key
            <NInput
              v-model:value="geoForm.maxmindLicenseKey"
              size="small"
              type="password"
              :maxlength="256"
              autocomplete="new-password"
              :disabled="geoForm.clearLicenseKey"
              :placeholder="geoForm.licenseKeySaved ? 'Saved — leave blank to retain' : 'License key'"
            >
              <template #prefix><KeyIcon class="icon-sm muted-text" aria-hidden="true" /></template>
            </NInput>
          </label>
        </div>
        <p v-if="geoSaveDisabledReason" class="copy-xs line-normal muted-text" role="status">
          GeoIP settings cannot be saved: {{ geoSaveDisabledReason }}
        </p>
        <div class="layout-row wrap-items align-center spread-items space-lg">
          <div class="layout-row wrap-items align-center space-lg">
            <NCheckbox v-model:checked="geoForm.enabled">Enable GeoIP country lookups</NCheckbox>
            <NCheckbox v-if="geoForm.licenseKeySaved" v-model:checked="geoForm.clearLicenseKey">
              Clear saved license key
            </NCheckbox>
          </div>
          <div class="layout-row wrap-items align-center space-sm">
            <NButton v-if="geoFormDirty" secondary size="small" attr-type="button" :disabled="isBusy" @click="discardGeoIpChanges">
              Discard changes
            </NButton>
            <DisabledHint :disabled="Boolean(geoSaveDisabledReason)" :reason="geoSaveDisabledReason">
              <NButton type="primary" size="small" attr-type="submit" :disabled="Boolean(geoSaveDisabledReason)">
                Save GeoIP settings
              </NButton>
            </DisabledHint>
          </div>
        </div>
      </form>
    </div>

    <div class="visitor-identity__section layout-grid space-lg pad-x-xl pad-y-xl">
      <div class="layout-row wrap-items align-start spread-items space-lg">
        <div class="layout-grid space-xs">
          <div class="layout-row align-center space-sm">
            <NetworkIcon class="icon-sm muted-text" aria-hidden="true" />
            <h3 class="copy-sm weight-semibold">Trusted proxies &amp; CDNs</h3>
          </div>
          <p class="copy-xs line-normal muted-text">
            Trust establishes visitor identity only. Restrict direct access to the origin separately when traffic must pass through a CDN.
          </p>
        </div>
        <NButton secondary size="small" :disabled="isBusy" @click="openCreateProxy">
          <template #icon><PlusIcon class="icon-sm" /></template>
          Add custom source
        </NButton>
      </div>

      <div v-if="builtInSources.length" class="visitor-identity__sources divided-list" aria-label="Built-in trusted proxy sources">
        <article v-for="source in builtInSources" :key="source.id.toString()" class="visitor-identity__source">
          <div class="layout-grid space-xs min-width-zero">
            <div class="layout-row wrap-items align-center space-sm">
              <CloudIcon class="icon-sm muted-text" aria-hidden="true" />
              <h4 class="copy-sm weight-semibold">{{ providerLabel(source.provider) }}</h4>
              <NTag size="small" :bordered="false" :type="source.enabled ? 'success' : 'default'">
                {{ source.enabled ? 'Trusted' : 'Not trusted' }}
              </NTag>
              <NTag v-if="source.lastRefreshError" size="small" :bordered="false" type="warning">Refresh issue</NTag>
              <NTag v-else-if="sourceRangesStale(source)" size="small" :bordered="false" type="warning">Stale ranges</NTag>
            </div>
            <p class="copy-xs line-normal muted-text">{{ providerDescription(source.provider) }}</p>
            <p class="copy-xs mono-text wrap-anywhere muted-text">
              {{ source.headerName }} · {{ headerModeLabel(source.headerMode) }} · {{ source.cidrCount.toString() }} peer ranges
            </p>
            <p class="copy-xs line-normal muted-text">
              Last success: {{ formatTimestamp(source.lastRefreshSuccessAtUnixMillis) }} ·
              Last attempt: {{ formatTimestamp(source.lastRefreshAttemptAtUnixMillis) }}
            </p>
            <p v-if="source.lastRefreshError" class="copy-xs line-normal warning-text wrap-anywhere">
              {{ source.lastRefreshError }}<span v-if="source.cidrCount > 0n"> Last valid ranges remain active.</span>
            </p>
            <p v-else-if="sourceRangesStale(source)" class="copy-xs line-normal warning-text">
              These managed ranges have not refreshed successfully in more than 24 hours. The last valid ranges remain active.
            </p>
          </div>
          <div class="visitor-identity__source-actions">
            <NButton
              secondary
              size="small"
              :disabled="isBusy"
              :aria-label="`Refresh ${providerLabel(source.provider)} ranges`"
              @click="refreshSource(source)"
            >
              <template #icon><RefreshIcon class="icon-sm" /></template>
              Refresh
            </NButton>
            <NButton
              size="small"
              :type="source.enabled ? 'default' : 'primary'"
              :secondary="source.enabled"
              :disabled="isBusy"
              :aria-label="`${source.enabled ? 'Disable' : 'Enable'} trust for ${providerLabel(source.provider)}`"
              @click="setSourceEnabled(source, !source.enabled)"
            >
              {{ source.enabled ? 'Disable trust' : 'Enable trust' }}
            </NButton>
          </div>
        </article>
      </div>

      <div class="layout-grid space-sm">
        <div class="layout-row align-center spread-items space-md">
          <h4 class="copy-xs weight-semibold label-case letter-wide muted-text">Custom sources</h4>
          <span class="copy-xs muted-text">{{ customSources.length.toString() }}</span>
        </div>
        <div v-if="customSources.length" class="visitor-identity__sources divided-list" aria-label="Custom trusted proxy sources">
          <article v-for="source in customSources" :key="source.id.toString()" class="visitor-identity__source">
            <div class="layout-grid space-xs min-width-zero">
              <div class="layout-row wrap-items align-center space-sm">
                <h5 class="copy-sm weight-semibold wrap-anywhere">{{ source.name }}</h5>
                <NTag size="small" :bordered="false" :type="source.enabled ? 'success' : 'default'">
                  {{ source.enabled ? 'Trusted' : 'Not trusted' }}
                </NTag>
              </div>
              <p class="copy-xs mono-text wrap-anywhere muted-text">
                {{ source.headerName }} · {{ headerModeLabel(source.headerMode) }} · {{ source.cidrs.length.toString() }} peer CIDRs
              </p>
            </div>
            <div class="visitor-identity__source-actions">
              <NButton secondary size="small" :disabled="isBusy" :aria-label="`Edit trusted proxy source ${source.name}`" @click="openEditProxy(source)">
                <template #icon><PencilIcon class="icon-sm" /></template>
              </NButton>
              <NButton type="error" size="small" :disabled="isBusy" :aria-label="`Delete trusted proxy source ${source.name}`" @click="deleteProxy(source)">
                <template #icon><TrashIcon class="icon-sm" /></template>
              </NButton>
            </div>
          </article>
        </div>
        <p v-else class="visitor-identity__empty copy-xs line-normal muted-text">
          No custom proxy source is trusted. Add one only for a proxy whose peer networks and header behavior you control.
        </p>
      </div>
    </div>
  </section>

  <NDrawer
    v-model:show="proxyEditorOpen"
    placement="right"
    width="min(100vw, 42rem)"
    :aria-label="proxyForm.id ? 'Edit Trusted Proxy Source' : 'Add Trusted Proxy Source'"
    class="editor-drawer"
  >
    <NDrawerContent :title="proxyForm.id ? 'Edit Trusted Proxy Source' : 'Add Trusted Proxy Source'" closable>
      <form class="editor-drawer-form layout-grid space-xl" @submit.prevent="saveProxy">
        <NAlert type="warning" :show-icon="false">
          Trust only peers you administer. A matching peer can control the configured client-IP header and therefore the visitor identity used by security policy.
        </NAlert>

        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Name
          <NInput v-model:value="proxyForm.name" size="small" :maxlength="64" placeholder="office-ingress" />
        </label>

        <div class="layout-grid space-lg mq-sm-cols-two">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Client-IP header
            <NInput v-model:value="proxyForm.headerName" size="small" class="mono-text" :maxlength="256" placeholder="X-Forwarded-For" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Header parser
            <NSelect
              v-model:value="proxyForm.headerMode"
              :input-props="{ 'aria-label': 'Trusted proxy header parser' }"
              size="small"
              :options="headerModeOptions"
            />
          </label>
        </div>

        <p class="copy-xs line-normal muted-text">
          <template v-if="proxyForm.headerMode === PublicTrustedProxyHeaderMode.SINGLE_IP">
            Single IP requires exactly one valid address. Use it only when this proxy overwrites a dedicated header.
          </template>
          <template v-else>
            Trusted chain parses an IP-only comma-separated chain from right to left and strips only enabled trusted-chain peers configured for this same header.
          </template>
        </p>

        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Trusted peer CIDRs
          <NInput
            v-model:value="proxyForm.cidrsText"
            type="textarea"
            class="mono-text"
            :autosize="{ minRows: 5, maxRows: 12 }"
            placeholder="192.0.2.0/24&#10;2001:db8::/32"
          />
          <span class="normal-text letter-normal weight-normal line-normal muted-text">
            One IPv4 or IPv6 network per line. These networks identify proxy peers, not visitors.
          </span>
        </label>

        <NCheckbox v-model:checked="proxyForm.enabled">Trust this source immediately after save</NCheckbox>

        <p v-if="proxySaveDisabledReason" class="copy-xs line-normal muted-text" role="status">
          Trusted proxy source cannot be saved: {{ proxySaveDisabledReason }}
        </p>

        <div class="editor-drawer-actions layout-row align-end-row space-md">
          <NButton secondary attr-type="button" @click="closeProxyEditor">Cancel</NButton>
          <DisabledHint :disabled="Boolean(proxySaveDisabledReason)" :reason="proxySaveDisabledReason">
            <NButton type="primary" attr-type="submit" :disabled="Boolean(proxySaveDisabledReason)">
              {{ proxyForm.id ? 'Save changes' : 'Create source' }}
            </NButton>
          </DisabledHint>
        </div>
      </form>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.visitor-identity__header {
  align-items: center;
}

.visitor-identity__notice {
  background: var(--app-panel-muted);
}

.visitor-identity__facts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr));
  gap: 1px;
  overflow: hidden;
  margin: 0;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-border);
}

.visitor-identity__facts > div {
  min-width: 0;
  background: var(--app-panel-muted);
  padding: 0.75rem;
}

.visitor-identity__facts dt {
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.visitor-identity__facts dd {
  margin: 0.25rem 0 0;
  overflow-wrap: anywhere;
  color: var(--app-text);
  font-size: 0.75rem;
  font-weight: 500;
}

.visitor-identity__sources {
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-panel);
}

.visitor-identity__source {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 1rem;
}

.visitor-identity__source-actions {
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 0.5rem;
}

.visitor-identity__empty {
  border: 1px dashed var(--app-border);
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 1rem;
}

@media (max-width: 47.99rem) {
  .visitor-identity__header,
  .visitor-identity__source {
    align-items: flex-start;
    flex-direction: column;
  }

  .visitor-identity__source-actions {
    width: 100%;
    justify-content: flex-start;
  }
}
</style>
