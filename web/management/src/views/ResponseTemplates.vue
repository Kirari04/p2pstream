<script setup lang="ts">
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { computed, ref } from "vue";
import { NButton, NTag } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import PublicResponseTemplateEditorModal from "@/components/editors/PublicResponseTemplateEditorModal.vue";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementContext } from "@/composables/useManagementContext";
import { naiveTagType } from "@/lib/naiveUi";
import {
  PublicResponseBodyMode,
  PublicResponseTemplateKind,
  type PublicResponseTemplate,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

const { publicProxyConfig, isBusy, runManagementAction } = useManagementContext();
const editor = ref<InstanceType<typeof PublicResponseTemplateEditorModal> | null>(null);
const { confirm } = useConfirmDialog();

const templates = computed(() => [...(publicProxyConfig.value?.responseTemplates ?? [])].sort((a, b) => {
  const kindOrder = kindRank(a.kind) - kindRank(b.kind);
  if (kindOrder !== 0) return kindOrder;
  return a.name.localeCompare(b.name);
}));
const summaryCards = computed(() => [
  { label: "Templates", value: templates.value.length.toString(), detail: "central response bodies" },
  { label: "Generic", value: templates.value.filter((template) => template.kind === PublicResponseTemplateKind.GENERIC_BODY).length.toString(), detail: "static, rate-limit, WAF block" },
  { label: "Captcha", value: templates.value.filter((template) => template.kind === PublicResponseTemplateKind.WAF_CAPTCHA_PAGE).length.toString(), detail: "{{ .captcha_element_html }} required" },
  { label: "Waiting Room", value: templates.value.filter((template) => template.kind === PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE).length.toString(), detail: "queue placeholders required" },
  { label: "Sign-in", value: templates.value.filter((template) => template.kind === PublicResponseTemplateKind.LOCAL_ACCESS_LOGIN_PAGE).length.toString(), detail: "local access pages" },
]);
const templateUsageCounts = computed(() => {
  const counts = new Map<string, number>();
  const config = publicProxyConfig.value;
  if (!config) return counts;
  const increment = (id: bigint) => {
    const key = id.toString();
    counts.set(key, (counts.get(key) ?? 0) + 1);
  };
  for (const target of config.routeTargets) {
    if (target.staticResponseBodyMode === PublicResponseBodyMode.TEMPLATE) increment(target.staticResponseTemplateId);
  }
  for (const rule of config.rateLimitRules) {
    if (rule.responseBodyMode === PublicResponseBodyMode.TEMPLATE) increment(rule.responseBodyTemplateId);
  }
  for (const rule of config.wafRules) {
    if (rule.blockResponseBodyMode === PublicResponseBodyMode.TEMPLATE) increment(rule.blockResponseTemplateId);
    if (rule.captchaPageTemplateId) increment(rule.captchaPageTemplateId);
    if (rule.waitingRoomPageTemplateId) increment(rule.waitingRoomPageTemplateId);
  }
  for (const provider of config.accessProviders) {
    if (provider.localAuthLoginTemplateId > 0n) increment(provider.localAuthLoginTemplateId);
  }
  return counts;
});

function kindRank(kind: PublicResponseTemplateKind): number {
  switch (kind) {
    case PublicResponseTemplateKind.GENERIC_BODY:
      return 1;
    case PublicResponseTemplateKind.WAF_CAPTCHA_PAGE:
      return 2;
    case PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE:
      return 3;
    case PublicResponseTemplateKind.LOCAL_ACCESS_LOGIN_PAGE:
      return 4;
    default:
      return 9;
  }
}

function kindLabel(kind: PublicResponseTemplateKind): string {
  switch (kind) {
    case PublicResponseTemplateKind.GENERIC_BODY:
      return "Generic body";
    case PublicResponseTemplateKind.WAF_CAPTCHA_PAGE:
      return "WAF captcha";
    case PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE:
      return "Waiting room";
    case PublicResponseTemplateKind.LOCAL_ACCESS_LOGIN_PAGE:
      return "Local sign-in";
    default:
      return "Unknown";
  }
}

function requiredPlaceholderLabel(kind: PublicResponseTemplateKind): string {
  switch (kind) {
    case PublicResponseTemplateKind.WAF_CAPTCHA_PAGE:
      return "{{ .captcha_element_html }}";
    case PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE:
      return "{{ .queue_position }}, {{ .retry_after_seconds }}";
    case PublicResponseTemplateKind.LOCAL_ACCESS_LOGIN_PAGE:
      return "action, CSRF, username, password";
    default:
      return "none";
  }
}

function usageCount(template: PublicResponseTemplate): number {
  return templateUsageCounts.value.get(template.id.toString()) ?? 0;
}

function formatUpdatedAt(template: PublicResponseTemplate): string {
  const millis = Number(template.updatedAtUnixMillis || template.createdAtUnixMillis || 0n);
  if (!millis) return "never";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(millis));
}

function openCreate(kind = PublicResponseTemplateKind.GENERIC_BODY) {
  editor.value?.openCreate(kind);
}

function openEdit(template: PublicResponseTemplate) {
  editor.value?.openEdit(template);
}

async function deleteTemplate(template: PublicResponseTemplate) {
  const uses = usageCount(template);
  if (uses > 0) return;
  if (!await confirm("Delete Response Template", `Delete ${template.name}? This cannot be undone.`)) return;
  if (!runManagementAction) return;
  await runManagementAction(async () => {
    await managementClient.deletePublicResponseTemplate({ id: template.id });
  }, "Response template deleted");
}
</script>

<template>
  <div class="stack-xl">
    <div class="layout-row layout-column space-lg mq-md-row mq-md-align-end mq-md-spread">
      <div>
        <h3 class="margin-bottom-sm copy-xl weight-bold">Response Templates</h3>
        <p class="copy-sm muted-text">Reusable response bodies, security pages, and local-access sign-in forms.</p>
      </div>
      <NButton type="primary" @click="openCreate()">
        <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
        Add Template
      </NButton>
    </div>

    <section class="layout-grid space-lg mq-sm-cols-two mq-xl-cols-five">
      <div v-for="card in summaryCards" :key="card.label" class="surface-card pad-lg">
        <p class="copy-xs weight-semibold label-case letter-widest muted-text">{{ card.label }}</p>
        <p class="margin-top-sm copy-2xl weight-semibold base-text">{{ card.value }}</p>
        <p class="margin-top-xs copy-xs muted-text">{{ card.detail }}</p>
      </div>
    </section>

    <section class="surface-card template-table-shell">
      <div class="template-table-toolbar">
        <div>
          <h4 class="copy-sm weight-semibold base-text">Templates</h4>
          <p class="copy-xs muted-text">Reusable bodies and the configured objects that reference them.</p>
        </div>
        <span class="template-table-count copy-xs muted-text">{{ templates.length }} total</span>
      </div>
      <div
        v-if="templates.length"
        class="template-table"
        role="table"
        aria-label="Response templates"
        :aria-rowcount="templates.length + 1"
        aria-colcount="6"
      >
        <div class="template-table-header" role="row">
          <span role="columnheader">Template</span>
          <span role="columnheader">Kind</span>
          <span role="columnheader">Usage</span>
          <span role="columnheader">Required placeholders</span>
          <span role="columnheader">Updated</span>
          <span role="columnheader" class="template-actions-heading">Actions</span>
        </div>
        <div
          v-for="template in templates"
          :key="template.id.toString()"
          :data-testid="`template-row-${template.id.toString()}`"
          class="template-table-row"
          role="row"
        >
          <div class="template-name-cell" role="cell">
            <p class="template-primary-text copy-sm weight-medium base-text">{{ template.name }}</p>
            <p class="template-secondary-text copy-xs muted-text">{{ template.description || "No description" }}</p>
          </div>
          <div class="template-kind-cell" role="cell">
            <span class="template-cell-label copy-xs muted-text">Kind</span>
            <NTag size="small" :bordered="false" type="info">{{ kindLabel(template.kind) }}</NTag>
            <span class="template-content-type copy-xs muted-text">{{ template.contentType || "Default content type" }}</span>
          </div>
          <div class="template-usage-cell" role="cell">
            <span class="template-cell-label copy-xs muted-text">Usage</span>
            <NTag size="small" :bordered="false" :type="naiveTagType(usageCount(template) ? 'warn' : 'info')">
              {{ usageCount(template).toString() }} {{ usageCount(template) === 1 ? "reference" : "references" }}
            </NTag>
          </div>
          <div class="template-required-cell" role="cell">
            <span class="template-cell-label copy-xs muted-text">Required</span>
            <code class="template-placeholder copy-xs">{{ requiredPlaceholderLabel(template.kind) }}</code>
          </div>
          <div class="template-updated-cell" role="cell">
            <span class="template-cell-label copy-xs muted-text">Updated</span>
            <span class="copy-xs muted-text">{{ formatUpdatedAt(template) }}</span>
          </div>
          <div class="template-actions" role="cell">
            <NButton secondary size="small" aria-label="Edit template" title="Edit template" @click="openEdit(template)">
              <template #icon><PencilIcon class="icon-sm icon-sm" /></template>
            </NButton>
            <DisabledHint :disabled="usageCount(template) > 0 || isBusy" :reason="usageCount(template) > 0 ? 'Remove all references before deleting this template.' : ''">
              <NButton
                type="error"
                size="small"
                aria-label="Delete template"
                title="Delete template"
                :disabled="usageCount(template) > 0 || isBusy"
                @click="deleteTemplate(template)"
              >
                <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
              </NButton>
            </DisabledHint>
          </div>
        </div>
      </div>
      <EmptyState
        v-else
        title="No response templates"
        description="Create reusable bodies for static targets, rate limits, WAF pages, and local-access sign-in forms."
        action-label="Add Template"
        @action="openCreate()"
      />
    </section>

    <section class="layout-grid space-md mq-sm-cols-two mq-xl-cols-four">
      <NButton secondary @click="openCreate(PublicResponseTemplateKind.GENERIC_BODY)">New Generic Body</NButton>
      <NButton secondary @click="openCreate(PublicResponseTemplateKind.WAF_CAPTCHA_PAGE)">New Captcha Page</NButton>
      <NButton secondary @click="openCreate(PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE)">New Waiting Room</NButton>
      <NButton secondary @click="openCreate(PublicResponseTemplateKind.LOCAL_ACCESS_LOGIN_PAGE)">New Sign-in Page</NButton>
    </section>

    <PublicResponseTemplateEditorModal ref="editor" />
  </div>
</template>

<style scoped>
.template-table-shell {
  container-type: inline-size;
  overflow: hidden;
}

.template-table-toolbar {
  display: flex;
  min-height: 3.5rem;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.625rem 1rem;
  border-bottom: 1px solid var(--app-border);
}

.template-table-toolbar > div {
  min-width: 0;
}

.template-table-toolbar p {
  margin-top: 0.125rem;
}

.template-table-count {
  flex: 0 0 auto;
  font-variant-numeric: tabular-nums;
}

.template-table-header,
.template-table-row {
  display: grid;
  grid-template-columns:
    minmax(13rem, 1.4fr)
    minmax(8.5rem, 0.8fr)
    minmax(7.5rem, 0.65fr)
    minmax(14rem, 1.15fr)
    minmax(9.5rem, 0.8fr)
    4.75rem;
  align-items: center;
  column-gap: 1rem;
}

.template-table-header {
  min-height: 2.25rem;
  padding: 0.375rem 1rem;
  border-bottom: 1px solid var(--app-border-subtle);
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.6875rem;
  font-weight: 600;
}

.template-table-row {
  min-height: 4rem;
  padding: 0.5rem 1rem;
  border-bottom: 1px solid var(--app-border-subtle);
  transition: background-color 0.15s ease-out;
}

.template-table-row:last-child {
  border-bottom: 0;
}

.template-table-row:hover {
  background: var(--app-panel-muted);
}

.template-table-row > *,
.template-name-cell,
.template-kind-cell,
.template-usage-cell,
.template-required-cell,
.template-updated-cell {
  min-width: 0;
}

.template-primary-text,
.template-secondary-text,
.template-content-type,
.template-placeholder {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: plaintext;
}

.template-secondary-text {
  margin-top: 0.125rem;
}

.template-kind-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  align-items: flex-start;
  gap: 0.125rem;
}

.template-content-type {
  display: block;
  max-width: 100%;
}

.template-usage-cell,
.template-required-cell,
.template-updated-cell {
  display: flex;
  align-items: center;
}

.template-placeholder {
  display: block;
  max-width: 100%;
  color: var(--app-text-muted);
  font-family: var(--font-mono);
}

.template-cell-label {
  display: none;
  font-weight: 500;
}

.template-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.375rem;
}

.template-actions-heading {
  text-align: right;
}

@container (max-width: 62rem) {
  .template-table-header {
    display: none;
  }

  .template-table-row {
    grid-template:
      "name name actions" auto
      "kind usage usage" auto
      "required required updated" auto /
      minmax(0, 1fr) minmax(0, 1fr) auto;
    gap: 0.625rem 1rem;
    min-height: 0;
    padding-block: 0.75rem;
  }

  .template-name-cell {
    grid-area: name;
  }

  .template-kind-cell {
    grid-area: kind;
  }

  .template-usage-cell {
    grid-area: usage;
  }

  .template-required-cell {
    grid-area: required;
  }

  .template-updated-cell {
    grid-area: updated;
  }

  .template-actions {
    grid-area: actions;
    align-self: start;
  }

  .template-kind-cell,
  .template-usage-cell,
  .template-required-cell,
  .template-updated-cell {
    align-items: flex-start;
    flex-direction: column;
    gap: 0.25rem;
  }

  .template-cell-label {
    display: block;
  }
}

@container (max-width: 38rem) {
  .template-table-toolbar {
    padding-inline: 0.75rem;
  }

  .template-table-row {
    grid-template:
      "name actions" auto
      "kind kind" auto
      "usage updated" auto
      "required required" auto /
      minmax(0, 1fr) auto;
    padding-inline: 0.75rem;
  }

  .template-required-cell {
    padding-top: 0.125rem;
  }
}

@media (hover: none), (pointer: coarse) {
  .template-actions :deep(.n-button) {
    min-width: 2.75rem;
    min-height: 2.75rem;
  }
}

@media (prefers-reduced-motion: reduce) {
  .template-table-row {
    transition: none;
  }
}
</style>
