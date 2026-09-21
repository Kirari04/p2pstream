<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import { Plus as PlusIcon, Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NCheckbox, NDrawer, NDrawerContent, NInput, NTag } from "naive-ui";
import DisabledHint from "@/components/DisabledHint.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { editorDrawerWidth, naiveTagType } from "@/lib/naiveUi";
import { protocolLabel } from "@/lib/publicProxyLabels";
import {
  publicSiteAliases,
  publicSitePrimary,
  publicSiteTLSLabel,
  publicSiteValidationReason,
  type SiteAliasForm,
} from "@/lib/publicSites";
import {
  PublicSiteHostBehavior,
  PublicSiteTlsCoverage,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const props = defineProps<{ config: GetPublicProxyConfigResponse | null }>();
const emit = defineEmits<{ (event: "saved"): void }>();

const managementClient = useManagementClient();
const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));
const isOpen = ref(false);
const nextAliasID = ref(1);

const form = reactive({
  id: "",
  listenerId: "",
  name: "",
  primaryHostname: "",
  aliases: [] as SiteAliasForm[],
  enabled: true,
});

const editing = computed(() => Boolean(form.id));
const listeners = computed(() => props.config?.listeners ?? []);
const sites = computed(() => props.config?.sites ?? []);
const currentSite = computed(() => sites.value.find((site) => site.id.toString() === form.id));
const listenerOptions = computed(() => listeners.value.map((listener) => ({
  label: `${listener.name} · ${protocolLabel(listener.protocol)} :${listener.port.toString()}`,
  value: listener.id.toString(),
})));
const selectedListenerLabel = computed(() => (
  listenerOptions.value.find((option) => option.value === form.listenerId)?.label ?? `Listener ${form.listenerId}`
));
const behaviorOptions = [
  { label: "Serve site", value: PublicSiteHostBehavior.SERVE },
  { label: "Redirect to primary · 308", value: PublicSiteHostBehavior.REDIRECT },
];
const validationReason = computed(() => publicSiteValidationReason(form));
const submitDisabledReason = computed(() => isBusy.value ? BUSY_REASON : validationReason.value);

function resetForm() {
  form.id = "";
  form.listenerId = listeners.value[0]?.id.toString() ?? "";
  form.name = "";
  form.primaryHostname = "";
  form.aliases = [];
  form.enabled = true;
}

function openCreate() {
  resetForm();
  isOpen.value = true;
}

function openEdit(siteId: bigint | string) {
  const site = sites.value.find((item) => item.id.toString() === siteId.toString());
  if (!site) return;
  const primary = publicSitePrimary(site);
  form.id = site.id.toString();
  form.listenerId = site.listenerId.toString();
  form.name = site.name;
  form.primaryHostname = primary?.hostnamePattern ?? "";
  form.aliases = publicSiteAliases(site).map((host) => ({
    id: host.id.toString(),
    hostnamePattern: host.hostnamePattern,
    behavior: host.behavior === PublicSiteHostBehavior.REDIRECT ? PublicSiteHostBehavior.REDIRECT : PublicSiteHostBehavior.SERVE,
  }));
  form.enabled = site.enabled;
  isOpen.value = true;
}

function addAlias() {
  form.aliases.push({ id: `new-${nextAliasID.value++}`, hostnamePattern: "", behavior: PublicSiteHostBehavior.SERVE });
}

function removeAlias(index: number) {
  form.aliases.splice(index, 1);
}

function close() {
  isOpen.value = false;
}

function coverageTagType(coverage: PublicSiteTlsCoverage) {
  if (coverage === PublicSiteTlsCoverage.COVERED || coverage === PublicSiteTlsCoverage.NOT_APPLICABLE) return naiveTagType("ok");
  if (coverage === PublicSiteTlsCoverage.MISSING) return naiveTagType("warn");
  if (coverage === PublicSiteTlsCoverage.INVALID) return naiveTagType("error");
  return "default" as const;
}

async function submitSite() {
  if (!runManagementAction || submitDisabledReason.value) return;
  const payload = {
    listenerId: BigInt(form.listenerId),
    name: form.name.trim(),
    enabled: form.enabled,
    hosts: [
      { hostnamePattern: form.primaryHostname, primary: true, behavior: PublicSiteHostBehavior.SERVE },
      ...form.aliases.map((alias) => ({
        hostnamePattern: alias.hostnamePattern,
        primary: false,
        behavior: alias.behavior,
      })),
    ],
  };
  const ok = await runManagementAction(async () => {
    if (form.id) await managementClient.updatePublicSite({ id: BigInt(form.id), ...payload });
    else await managementClient.createPublicSite(payload);
  });
  if (ok) {
    close();
    emit("saved");
  }
}

defineExpose({ openCreate, openEdit, close });
</script>

<template>
  <NDrawer
    v-model:show="isOpen"
    placement="right"
    :width="editorDrawerWidth('48rem')"
    :aria-label="editing ? 'Edit Site' : 'Add Site'"
    class="editor-drawer"
  >
    <NDrawerContent :title="editing ? 'Edit Site' : 'Add Site'" closable>
      <form class="editor-drawer-form layout-grid space-xl" @submit.prevent="submitSite">
        <section class="site-editor__identity layout-grid space-lg mq-sm-cols-two">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Site name
            <NInput v-model:value="form.name" size="small" maxlength="64" placeholder="customer-portal" required />
            <span class="normal-text letter-normal">An operator label; it is not sent to clients.</span>
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Listener
            <NInput
              v-if="editing"
              :value="selectedListenerLabel"
              size="small"
              readonly
              :input-props="{ 'aria-label': 'Site listener' }"
            />
            <AccessibleSelect
              v-else
              v-model:value="form.listenerId"
              accessible-label="Site listener"
              size="small"
              :options="listenerOptions"
              required
            />
            <span class="normal-text letter-normal">{{ editing ? 'Listener ownership cannot be changed after creation.' : 'All site hostnames are claimed on this listener.' }}</span>
          </label>
          <NCheckbox v-model:checked="form.enabled">Site enabled</NCheckbox>
        </section>

        <section class="site-editor__primary layout-grid space-md round-md framed frame-standard pad-lg">
          <div class="layout-row align-start spread-items space-md">
            <div>
              <p class="copy-xs weight-semibold label-case letter-wide base-text">Primary hostname</p>
              <p class="margin-top-xs copy-xs line-normal muted-text">The canonical exact hostname. Alias redirects always return 308 to this value.</p>
            </div>
            <NTag size="small" :bordered="false">Canonical</NTag>
          </div>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Exact hostname
            <NInput v-model:value="form.primaryHostname" size="small" placeholder="app.example.com" required />
            <span class="normal-text letter-normal">No scheme, path, port, or wildcard. Unicode names are stored in canonical IDNA form.</span>
          </label>
        </section>

        <p v-if="!editing" role="note" class="round-md framed warning-border warning-surface pad-md copy-xs line-normal warning-text">
          Saving claims every hostname immediately, even when the Site is disabled. The primary and Serve aliases return 404 until a route matches, with no Legacy fallthrough. On an enabled Site, Redirect aliases still return 308 to the primary.
        </p>

        <section class="layout-grid space-md">
          <div class="layout-row align-start spread-items space-md">
            <div>
              <h3 class="copy-sm weight-semibold base-text">Aliases</h3>
              <p class="margin-top-xs copy-xs line-normal muted-text">Serve the same route tree or redirect permanently to the primary while preserving path and query.</p>
            </div>
            <NButton secondary size="small" attr-type="button" @click="addAlias">
              <template #icon><PlusIcon class="icon-sm" /></template>
              Add alias
            </NButton>
          </div>

          <div v-if="form.aliases.length" class="site-editor__aliases">
            <div v-for="(alias, index) in form.aliases" :key="alias.id" class="site-editor__alias-row">
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Alias {{ index + 1 }} hostname
                <NInput v-model:value="alias.hostnamePattern" size="small" placeholder="www.example.com or *.example.com" required />
              </label>
              <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
                Behavior
                <AccessibleSelect
                  v-model:value="alias.behavior"
                  :accessible-label="`Alias ${index + 1} behavior`"
                  size="small"
                  :options="behaviorOptions"
                />
              </label>
              <NButton
                type="error"
                size="small"
                attr-type="button"
                :aria-label="`Remove alias ${index + 1}`"
                title="Remove alias"
                @click="removeAlias(index)"
              >
                <template #icon><TrashIcon class="icon-sm" /></template>
              </NButton>
            </div>
          </div>
          <div v-else class="site-editor__empty round-md framed frame-standard muted-bg pad-lg copy-xs muted-text">
            No aliases. Only the exact primary hostname will claim this site.
          </div>
          <p class="copy-xs line-normal muted-text">Wildcard aliases match exactly one label: <span class="mono-text">*.example.com</span> matches <span class="mono-text">a.example.com</span>, never the apex or <span class="mono-text">a.b.example.com</span>.</p>
        </section>

        <section v-if="currentSite" class="layout-grid space-md divider-top pad-top-lg">
          <div class="layout-row align-center spread-items space-md">
            <h3 class="copy-sm weight-semibold base-text">TLS coverage</h3>
            <router-link to="/tls" class="copy-xs">Configure TLS</router-link>
          </div>
          <div class="site-editor__coverage-list">
            <div v-for="host in currentSite.hosts" :key="host.id.toString()" class="site-editor__coverage-row">
              <span class="mono-text copy-xs clip-text" :title="host.hostnamePattern">{{ host.hostnamePattern }}</span>
              <NTag size="small" :bordered="false" :type="coverageTagType(host.tlsCoverage)">{{ publicSiteTLSLabel(host.tlsCoverage) }}</NTag>
              <span class="copy-xs muted-text">{{ host.tlsDetail }}</span>
            </div>
          </div>
          <p class="copy-xs line-normal muted-text">Certificate selection follows HTTPS SNI. IP-literal sites cannot select a certificate mapping; use a DNS hostname for HTTPS.</p>
        </section>

        <p v-if="validationReason" role="alert" class="copy-xs warning-text">{{ validationReason }}</p>

        <div class="editor-drawer-actions layout-row align-end-row space-md">
          <NButton secondary attr-type="button" @click="close">Cancel</NButton>
          <DisabledHint :disabled="Boolean(submitDisabledReason)" :reason="submitDisabledReason">
            <NButton type="primary" attr-type="submit" :disabled="Boolean(submitDisabledReason)">
              {{ editing ? 'Save changes' : 'Create site' }}
            </NButton>
          </DisabledHint>
        </div>
      </form>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.site-editor__primary {
  border-left: 3px solid var(--app-accent);
  background: var(--app-accent-soft);
}

.site-editor__aliases {
  overflow: hidden;
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
}

.site-editor__alias-row {
  display: grid;
  grid-template-columns: minmax(12rem, 1fr) minmax(12rem, 0.65fr) auto;
  gap: 0.75rem;
  align-items: end;
  padding: 0.75rem;
}

.site-editor__alias-row + .site-editor__alias-row,
.site-editor__coverage-row + .site-editor__coverage-row {
  border-top: 1px solid var(--app-border-subtle);
}

.site-editor__alias-row > :deep(.n-button) {
  width: 2.125rem;
  min-width: 2.125rem;
  padding-inline: 0;
}

.site-editor__coverage-list {
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
}

.site-editor__coverage-row {
  display: grid;
  grid-template-columns: minmax(10rem, 0.8fr) auto minmax(12rem, 1fr);
  gap: 0.75rem;
  align-items: center;
  padding: 0.625rem 0.75rem;
}

@media (max-width: 640px) {
  .site-editor__alias-row,
  .site-editor__coverage-row {
    grid-template-columns: minmax(0, 1fr);
  }

  .site-editor__alias-row > :deep(.n-button) {
    width: 100%;
  }
}
</style>
