<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import HtmlTemplateEditor from "@/components/editors/HtmlTemplateEditor.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/components/ui/Button.vue";
import Modal from "@/components/ui/Modal.vue";
import SecondaryButton from "@/components/ui/SecondaryButton.vue";
import {
  PublicResponseTemplateKind,
  type PublicResponseTemplate,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

type Runner = (action: () => Promise<void>, successMessage?: string) => Promise<boolean>;

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject<Runner>("runManagementAction");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const isOpen = ref(false);
const form = reactive({
  id: "",
  name: "",
  kind: PublicResponseTemplateKind.GENERIC_BODY,
  description: "",
  contentType: "text/html; charset=utf-8",
  body: "",
});

const kindOptions = [
  { label: "Generic body", value: PublicResponseTemplateKind.GENERIC_BODY },
  { label: "WAF captcha page", value: PublicResponseTemplateKind.WAF_CAPTCHA_PAGE },
  { label: "WAF waiting room", value: PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE },
];

const modalTitle = computed(() => form.id ? "Edit Response Template" : "Add Response Template");
const submitLabel = computed(() => form.id ? "Save Changes" : "Create Template");
const requiredPlaceholders = computed(() => {
  switch (form.kind) {
    case PublicResponseTemplateKind.WAF_CAPTCHA_PAGE:
      return ["captcha_element_html"];
    case PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE:
      return ["queue_position", "retry_after_seconds"];
    default:
      return [];
  }
});
const referencedPlaceholders = computed(() => {
  const names = new Set<string>();
  const pattern = /{{\s*\.([A-Za-z_][A-Za-z0-9_]*)/g;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(form.body)) !== null) {
    names.add(match[1]);
  }
  return names;
});
const missingRequired = computed(() => requiredPlaceholders.value.filter((name) => !referencedPlaceholders.value.has(name)));
const disabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!form.name.trim()) return "Enter a template name.";
  if (missingRequired.value.length) return `Missing required placeholder ${missingRequired.value.map((name) => `{{ .${name} }}`).join(", ")}.`;
  return "";
});

function resetForm(kind = PublicResponseTemplateKind.GENERIC_BODY) {
  form.id = "";
  form.name = "";
  form.kind = kind;
  form.description = "";
  form.contentType = "text/html; charset=utf-8";
  form.body = defaultBodyForKind(kind);
}

function openCreate(kind = PublicResponseTemplateKind.GENERIC_BODY) {
  resetForm(kind);
  isOpen.value = true;
}

function openEdit(template: PublicResponseTemplate) {
  form.id = template.id.toString();
  form.name = template.name;
  form.kind = template.kind || PublicResponseTemplateKind.GENERIC_BODY;
  form.description = template.description;
  form.contentType = template.contentType || "text/html; charset=utf-8";
  form.body = template.body;
  isOpen.value = true;
}

function close() {
  isOpen.value = false;
}

function defaultBodyForKind(kind: PublicResponseTemplateKind): string {
  switch (kind) {
    case PublicResponseTemplateKind.WAF_CAPTCHA_PAGE:
      return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .page_title }}</title>
</head>
<body>
  <main>
    <h1>{{ .host }} security check</h1>
    <p>{{ .page_body }}</p>
    {{ .captcha_element_html }}
    <footer>Reference ID: {{ .reference_id }}</footer>
  </main>
</body>
</html>
`;
    case PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE:
      return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta http-equiv="refresh" content="{{ .retry_after_seconds }}">
  <title>{{ .page_title }}</title>
</head>
<body>
  <main>
    <h1>{{ .page_title }}</h1>
    <p>{{ .page_body }}</p>
    <p>Queue position: {{ .queue_position }}</p>
    <p>Next check: {{ .retry_after_seconds }} seconds</p>
    <footer>Reference ID: {{ .reference_id }}</footer>
  </main>
</body>
</html>
`;
    default:
      return "";
  }
}

function applyKind(value: PublicResponseTemplateKind) {
  form.kind = value;
  if (!form.id && !form.body.trim()) {
    form.body = defaultBodyForKind(value);
  }
}

function handleKindChange(event: Event) {
  applyKind(Number((event.target as HTMLSelectElement).value) as PublicResponseTemplateKind);
}

async function submit() {
  if (disabledReason.value || !runManagementAction) return;
  const payload = {
    name: form.name.trim(),
    kind: form.kind,
    description: form.description.trim(),
    contentType: form.contentType.trim(),
    body: form.body,
  };
  const ok = await runManagementAction(async () => {
    if (form.id) {
      await managementClient.updatePublicResponseTemplate({ id: BigInt(form.id), ...payload });
    } else {
      await managementClient.createPublicResponseTemplate(payload);
    }
  }, form.id ? "Response template updated" : "Response template created");
  if (ok) {
    isOpen.value = false;
    emit("saved");
  }
}

defineExpose({ openCreate, openEdit, close });
</script>

<template>
  <Modal v-model="isOpen" :title="modalTitle" max-width="72rem">
    <form class="grid gap-5" @submit.prevent="submit">
      <section class="grid gap-4 md:grid-cols-[1fr_14rem]">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Name
          <input v-model="form.name" class="app-control text-sm normal-case tracking-normal" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Kind
          <select :value="form.kind" class="app-control text-sm normal-case tracking-normal" @change="handleKindChange">
            <option v-for="option in kindOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Description
          <input v-model="form.description" class="app-control text-sm normal-case tracking-normal" />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Content type
          <input v-model="form.contentType" class="app-control text-sm normal-case tracking-normal" />
        </label>
      </section>

      <HtmlTemplateEditor v-model="form.body" :kind="form.kind" :content-type="form.contentType" />

      <div class="flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="close" />
        <DisabledHint :disabled="Boolean(disabledReason)" :reason="disabledReason">
          <Button :label="submitLabel" type="submit" :disabled="Boolean(disabledReason)" />
        </DisabledHint>
      </div>
    </form>
  </Modal>
</template>
