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
        <p class="copy-sm muted-text">Reusable static bodies and validated WAF HTML pages.</p>
      </div>
      <NButton type="primary" @click="openCreate()">
        <template #icon><PlusIcon class="icon-sm icon-sm" /></template>
        Add Template
      </NButton>
    </div>

    <section class="layout-grid space-lg mq-sm-cols-two mq-xl-cols-four">
      <div v-for="card in summaryCards" :key="card.label" class="surface-card pad-lg">
        <p class="copy-xs weight-semibold label-case letter-widest muted-text">{{ card.label }}</p>
        <p class="margin-top-sm copy-2xl weight-semibold base-text">{{ card.value }}</p>
        <p class="margin-top-xs copy-xs muted-text">{{ card.detail }}</p>
      </div>
    </section>

    <section class="surface-card hide-overflow">
      <div class="divider-bottom frame-standard pad-x-xl pad-y-lg">
        <h4 class="copy-sm weight-semibold label-case letter-widest muted-text">Templates</h4>
      </div>
      <div class="divided-list">
        <div
          v-for="template in templates"
          :key="template.id.toString()"
          :data-testid="`template-row-${template.id.toString()}`"
          class="layout-grid space-md pad-x-xl pad-y-lg mq-lg-one-auto"
        >
          <div class="min-width-zero">
            <div class="layout-row min-width-zero wrap-items align-center space-sm">
              <p class="clip-text copy-sm weight-medium base-text">{{ template.name }}</p>
              <NTag size="small" :bordered="false" type="info">{{ kindLabel(template.kind) }}</NTag>
              <NTag size="small" :bordered="false" :type="naiveTagType(usageCount(template) ? 'warn' : 'info')">{{ usageCount(template).toString() }} uses</NTag>
            </div>
            <p class="margin-top-xs clip-text copy-xs muted-text">{{ template.description || template.contentType || "No description" }}</p>
            <p class="margin-top-xs clip-text mono-text copy-xs muted-text">Required: {{ requiredPlaceholderLabel(template.kind) }} / updated {{ formatUpdatedAt(template) }}</p>
          </div>
          <div class="layout-row space-sm mq-lg-end">
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
        <EmptyState
          v-if="!templates.length"
          title="No response templates"
          description="Create reusable bodies for static targets, rate limits, WAF blocks, captcha pages, and waiting-room pages."
          action-label="Add Template"
          @action="openCreate()"
        />
      </div>
    </section>

    <section class="layout-grid space-md mq-sm-cols-three">
      <NButton secondary @click="openCreate(PublicResponseTemplateKind.GENERIC_BODY)">New Generic Body</NButton>
      <NButton secondary @click="openCreate(PublicResponseTemplateKind.WAF_CAPTCHA_PAGE)">New Captcha Page</NButton>
      <NButton secondary @click="openCreate(PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE)">New Waiting Room</NButton>
    </section>

    <PublicResponseTemplateEditorModal ref="editor" />
  </div>
</template>
