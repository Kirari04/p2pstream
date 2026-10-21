<script setup lang="ts">
import { Pencil as PencilIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { computed, h, ref } from "vue";
import { NButton, NDataTable, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import PublicResponseTemplateEditorModal from "@/components/editors/PublicResponseTemplateEditorModal.vue";
import MetricCard from "@/components/ui/MetricCard.vue";
import MetricStrip from "@/components/ui/MetricStrip.vue";
import PageHeader from "@/components/ui/PageHeader.vue";
import SectionCard from "@/components/ui/SectionCard.vue";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementContext } from "@/composables/useManagementContext";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";
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
  { label: "Templates", value: templates.value.length.toString(), detail: "Central response bodies" },
  { label: "Generic", value: templates.value.filter((template) => template.kind === PublicResponseTemplateKind.GENERIC_BODY).length.toString(), detail: "Static, rate-limit, WAF block" },
  { label: "Captcha", value: templates.value.filter((template) => template.kind === PublicResponseTemplateKind.WAF_CAPTCHA_PAGE).length.toString(), detail: "{{ .captcha_element_html }} required" },
  { label: "Waiting room", value: templates.value.filter((template) => template.kind === PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE).length.toString(), detail: "Queue placeholders required" },
  { label: "Sign-in", value: templates.value.filter((template) => template.kind === PublicResponseTemplateKind.LOCAL_ACCESS_LOGIN_PAGE).length.toString(), detail: "Local access pages" },
]);
const presetActions = [
  { label: "New Generic Body", kind: PublicResponseTemplateKind.GENERIC_BODY },
  { label: "New Captcha Page", kind: PublicResponseTemplateKind.WAF_CAPTCHA_PAGE },
  { label: "New Waiting Room", kind: PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE },
  { label: "New Sign-in Page", kind: PublicResponseTemplateKind.LOCAL_ACCESS_LOGIN_PAGE },
];
const deleteBlockedReason = "Remove all references before deleting this template.";
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

const templateColumns = computed<DataTableColumns<PublicResponseTemplate>>(() => [
  {
    title: "Template",
    key: "name",
    minWidth: 180,
    render: (template) => h("div", { class: "template-cell" }, [
      h("bdi", {
        class: "clip-text copy-sm weight-medium base-text",
        dir: "ltr",
        title: diagnosticInspectionText(template.name),
      }, diagnosticExcerpt(template.name, 64).text),
      template.description
        ? h("bdi", {
          class: "clip-text copy-xs muted-text",
          dir: "ltr",
          title: diagnosticInspectionText(template.description),
        }, diagnosticExcerpt(template.description, 96).text)
        : h("span", { class: "copy-xs muted-text" }, "No description"),
    ]),
  },
  {
    title: "Kind",
    key: "kind",
    width: 150,
    render: (template) => h("div", { class: "template-cell" }, [
      h(NTag, { size: "small", bordered: false, class: "fit-width" }, { default: () => kindLabel(template.kind) }),
      template.contentType
        ? h("bdi", {
          class: "clip-text mono-text copy-xs muted-text",
          dir: "ltr",
          title: diagnosticInspectionText(template.contentType),
        }, diagnosticExcerpt(template.contentType, 48).text)
        : h("span", { class: "copy-xs muted-text" }, "Default content type"),
    ]),
  },
  {
    title: "Usage",
    key: "usage",
    width: 110,
    render: (template) => h("span", {
      class: ["copy-xs tabular-copy", usageCount(template) ? "base-text" : "muted-text"],
    }, `${usageCount(template).toString()} ${usageCount(template) === 1 ? "reference" : "references"}`),
  },
  {
    title: "Required placeholders",
    key: "required",
    width: 180,
    render: (template) => requiredPlaceholderLabel(template.kind) === "none"
      ? h("span", { class: "copy-xs muted-text" }, "None")
      : h("code", {
        class: "template-placeholder clip-text",
        title: requiredPlaceholderLabel(template.kind),
      }, requiredPlaceholderLabel(template.kind)),
  },
  {
    title: "Updated",
    key: "updated",
    width: 150,
    render: (template) => h("span", { class: "clip-text copy-xs muted-text tabular-copy template-block", title: formatUpdatedAt(template) }, formatUpdatedAt(template)),
  },
  {
    title: "Actions",
    key: "actions",
    width: 190,
    align: "right",
    fixed: "right",
    render: (template) => h("div", { class: "template-actions" }, [
      h(NButton, {
        secondary: true,
        size: "small",
        "aria-label": "Edit template",
        title: "Edit template",
        onClick: () => openEdit(template),
      }, {
        icon: () => h(PencilIcon, { class: "icon-sm" }),
        default: () => "Edit",
      }),
      h(DisabledHint, {
        disabled: usageCount(template) > 0 || isBusy.value,
        reason: usageCount(template) > 0 ? deleteBlockedReason : "",
      }, {
        default: () => h(NButton, {
          type: "error",
          secondary: true,
          size: "small",
          "aria-label": "Delete template",
          title: "Delete template",
          disabled: usageCount(template) > 0 || isBusy.value,
          onClick: () => void deleteTemplate(template),
        }, {
          icon: () => h(TrashIcon, { class: "icon-sm" }),
          default: () => "Delete",
        }),
      }),
    ]),
  },
]);

function templateRowKey(template: PublicResponseTemplate): string {
  return template.id.toString();
}

function templateRowProps(template: PublicResponseTemplate): Record<string, string> {
  return { "data-testid": `template-row-${template.id.toString()}` };
}

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
    <PageHeader title="Response Templates" description="Reusable response bodies, security pages, and local-access sign-in forms.">
      <template #actions>
        <NButton type="primary" size="small" @click="openCreate()">
          <template #icon><PlusIcon class="icon-sm" /></template>
          Add Template
        </NButton>
      </template>
    </PageHeader>

    <MetricStrip label="Response template summary">
      <MetricCard v-for="card in summaryCards" :key="card.label" :label="card.label" :value="card.value" :detail="card.detail" />
    </MetricStrip>

    <SectionCard title="Templates" description="Reusable bodies and the configured objects that reference them." flush>
      <template #badge><NTag size="small" :bordered="false">{{ templates.length }}</NTag></template>
      <NDataTable
        v-if="templates.length"
        :columns="templateColumns"
        :data="templates"
        :row-key="templateRowKey"
        :row-props="templateRowProps"
        :pagination="false"
        :bordered="false"
       
        :scroll-x="960"
        table-layout="fixed"
        size="small"
        aria-label="Response templates"
      />
      <EmptyState
        v-else
        title="No response templates"
        description="Create reusable bodies for static targets, rate limits, WAF pages, and local-access sign-in forms."
        action-label="Add Template"
        @action="openCreate()"
      />
      <template #footer>
        <div class="template-presets">
          <span class="copy-xs muted-text">Start from a preset</span>
          <div class="template-presets__actions">
            <NButton v-for="preset in presetActions" :key="preset.kind" quaternary size="small" @click="openCreate(preset.kind)">
              {{ preset.label }}
            </NButton>
          </div>
        </div>
      </template>
    </SectionCard>

    <PublicResponseTemplateEditorModal ref="editor" />
  </div>
</template>

<style scoped>
:deep(.template-cell) {
  display: grid;
  min-width: 0;
  justify-items: start;
  gap: 0.125rem;
}

:deep(.template-cell > *) {
  max-width: 100%;
}

:deep(.template-placeholder) {
  display: block;
  max-width: 100%;
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.75rem;
}

:deep(.template-block) {
  display: block;
  max-width: 100%;
}

:deep(.tabular-copy) {
  font-variant-numeric: tabular-nums;
}

:deep(.template-actions) {
  display: flex;
  justify-content: flex-end;
  gap: 0.375rem;
}

.template-presets {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.25rem 0.75rem;
}

.template-presets__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.25rem;
}

@media (pointer: coarse) {
  :deep(.template-actions .n-button) {
    min-height: 2.75rem;
  }
}
</style>
