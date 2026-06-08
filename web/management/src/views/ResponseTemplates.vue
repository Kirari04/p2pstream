<script setup lang="ts">
import PencilIcon from "@primevue/icons/pencil";
import PlusIcon from "@primevue/icons/plus";
import TrashIcon from "@primevue/icons/trash";
import { computed, ref } from "vue";
import { useManagementClient } from "@/composables/useManagementClient";
import ConfirmDialog from "@/components/ConfirmDialog.vue";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import PublicResponseTemplateEditorModal from "@/components/editors/PublicResponseTemplateEditorModal.vue";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementContext } from "@/composables/useManagementContext";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import Tag from "@/volt/Tag.vue";
import {
  PublicResponseBodyMode,
  PublicResponseTemplateKind,
  type PublicResponseTemplate,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

const { publicProxyConfig, isBusy, runManagementAction } = useManagementContext();
const editor = ref<InstanceType<typeof PublicResponseTemplateEditorModal> | null>(null);
const { state: confirmState, confirm, handleConfirm: onConfirm, handleCancel: onCancel } = useConfirmDialog();

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
  const config = publicProxyConfig.value;
  if (!config) return 0;
  const id = template.id;
  let count = 0;
  count += config.routeTargets.filter((target) => target.staticResponseBodyMode === PublicResponseBodyMode.TEMPLATE && target.staticResponseTemplateId === id).length;
  count += config.rateLimitRules.filter((rule) => rule.responseBodyMode === PublicResponseBodyMode.TEMPLATE && rule.responseBodyTemplateId === id).length;
  count += config.wafRules.filter((rule) => (
    (rule.blockResponseBodyMode === PublicResponseBodyMode.TEMPLATE && rule.blockResponseTemplateId === id) ||
    rule.captchaPageTemplateId === id ||
    rule.waitingRoomPageTemplateId === id
  )).length;
  return count;
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
  <div class="space-y-8">
    <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
      <div>
        <h3 class="mb-2 text-xl font-bold">Response Templates</h3>
        <p class="text-sm text-[#888]">Reusable static bodies and validated WAF HTML pages.</p>
      </div>
      <Button label="Add Template" @click="openCreate()">
        <template #icon><PlusIcon class="h-3.5 w-3.5" /></template>
      </Button>
    </div>

    <section class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div v-for="card in summaryCards" :key="card.label" class="vercel-card p-4">
        <p class="text-xs font-semibold uppercase tracking-widest text-[#666]">{{ card.label }}</p>
        <p class="mt-2 text-2xl font-semibold text-white">{{ card.value }}</p>
        <p class="mt-1 text-xs text-[#777]">{{ card.detail }}</p>
      </div>
    </section>

    <section class="vercel-card overflow-hidden">
      <div class="border-b border-[#333] px-5 py-4">
        <h4 class="text-sm font-semibold uppercase tracking-widest text-[#888]">Templates</h4>
      </div>
      <div class="divide-y divide-[#1f1f1f]">
        <div v-for="template in templates" :key="template.id.toString()" class="grid gap-3 px-5 py-4 lg:grid-cols-[1fr_auto]">
          <div class="min-w-0">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <p class="truncate text-sm font-medium text-white">{{ template.name }}</p>
              <Tag :value="kindLabel(template.kind)" severity="info" />
              <Tag :value="`${usageCount(template).toString()} uses`" :severity="usageCount(template) ? 'warn' : 'info'" />
            </div>
            <p class="mt-1 truncate text-xs text-[#888]">{{ template.description || template.contentType || "No description" }}</p>
            <p class="mt-1 truncate font-mono text-xs text-[#666]">Required: {{ requiredPlaceholderLabel(template.kind) }} / updated {{ formatUpdatedAt(template) }}</p>
          </div>
          <div class="flex gap-2 lg:justify-end">
            <SecondaryButton size="small" aria-label="Edit template" title="Edit template" @click="openEdit(template)">
              <template #icon><PencilIcon class="h-3.5 w-3.5" /></template>
            </SecondaryButton>
            <DisabledHint :disabled="usageCount(template) > 0 || isBusy" :reason="usageCount(template) > 0 ? 'Remove all references before deleting this template.' : ''">
              <DangerButton
                size="small"
                aria-label="Delete template"
                title="Delete template"
                :disabled="usageCount(template) > 0 || isBusy"
                @click="deleteTemplate(template)"
              >
                <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
              </DangerButton>
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

    <section class="grid gap-3 sm:grid-cols-3">
      <SecondaryButton label="New Generic Body" @click="openCreate(PublicResponseTemplateKind.GENERIC_BODY)" />
      <SecondaryButton label="New Captcha Page" @click="openCreate(PublicResponseTemplateKind.WAF_CAPTCHA_PAGE)" />
      <SecondaryButton label="New Waiting Room" @click="openCreate(PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE)" />
    </section>

    <PublicResponseTemplateEditorModal ref="editor" />
    <ConfirmDialog :state="confirmState" @confirm="onConfirm" @cancel="onCancel" />
  </div>
</template>
