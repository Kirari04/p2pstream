<script setup lang="ts">
import { computed, defineAsyncComponent, inject, reactive, ref } from "vue";
import { NButton, NDrawer, NDrawerContent, NInput } from "naive-ui";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { editorDrawerWidth } from "@/lib/naiveUi";
import {
  PublicResponseTemplateKind,
  type PublicResponseTemplate,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();

const HtmlTemplateEditor = defineAsyncComponent(() => import("@/components/editors/HtmlTemplateEditor.vue"));

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));

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
  { label: "Local access sign-in page", value: PublicResponseTemplateKind.LOCAL_ACCESS_LOGIN_PAGE },
];

const modalTitle = computed(() => form.id ? "Edit Response Template" : "Add Response Template");
const submitLabel = computed(() => form.id ? "Save Changes" : "Create Template");
const requiredPlaceholders = computed(() => {
  switch (form.kind) {
    case PublicResponseTemplateKind.WAF_CAPTCHA_PAGE:
      return ["captcha_element_html"];
    case PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE:
      return ["queue_position", "retry_after_seconds"];
    case PublicResponseTemplateKind.LOCAL_ACCESS_LOGIN_PAGE:
      return ["login_action", "csrf_field_name", "csrf_token", "username_field_name", "password_field_name"];
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
    case PublicResponseTemplateKind.LOCAL_ACCESS_LOGIN_PAGE:
      return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Sign in · {{ .page_title }}</title>
  <style>
    :root { color-scheme: dark; --ink: #f4f0e8; --muted: #9aa3ad; --panel: #11171c; --line: #303942; --accent: #35d399; }
    * { box-sizing: border-box; }
    body { min-height: 100vh; margin: 0; display: grid; place-items: center; padding: 28px; background: #080b0e; color: var(--ink); font-family: "Trebuchet MS", Verdana, sans-serif; }
    main { width: min(100%, 430px); border: 1px solid var(--line); background: var(--panel); box-shadow: 18px 18px 0 #050708; padding: 34px; }
    h1 { margin: 0; font-size: 34px; letter-spacing: -0.035em; }
    .sub { margin: 10px 0 28px; color: var(--muted); line-height: 1.55; }
    .error { border-left: 3px solid #fb7185; background: #28151a; color: #fecdd3; padding: 11px 13px; line-height: 1.45; }
    .error:empty { display: none; }
    label { display: grid; gap: 8px; margin-top: 18px; color: #c7cdd3; font-size: 12px; font-weight: 700; letter-spacing: .08em; text-transform: uppercase; }
    input { width: 100%; border: 1px solid #3a4650; background: #090d10; color: var(--ink); padding: 13px 14px; font: inherit; font-size: 16px; }
    input:focus { border-color: var(--accent); outline: 2px solid rgba(53, 211, 153, .16); }
    button { width: 100%; margin-top: 24px; border: 0; background: var(--accent); color: #042218; padding: 14px 16px; cursor: pointer; font: inherit; font-weight: 800; }
  </style>
</head>
<body>
  <main>
    <h1>Sign in</h1>
    <p class="sub">Use your local account to continue to {{ .provider_name }}.</p>
    <p class="error" role="alert">{{ .error_message }}</p>
    <form method="post" action="{{ .login_action }}">
      <input type="hidden" name="{{ .csrf_field_name }}" value="{{ .csrf_token }}">
      <label>Username
        <input name="{{ .username_field_name }}" value="{{ .username }}" autocomplete="username" autocapitalize="none" spellcheck="false" required autofocus>
      </label>
      <label>Password
        <input type="password" name="{{ .password_field_name }}" autocomplete="current-password" required>
      </label>
      <button type="submit">Continue</button>
    </form>
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
  <NDrawer
    v-model:show="isOpen"
    placement="right"
    :width="editorDrawerWidth('72rem')"
    :aria-label="modalTitle"
    class="editor-drawer"
  >
    <NDrawerContent :title="modalTitle" closable>
    <form class="editor-drawer-form layout-grid space-xl" @submit.prevent="submit">
      <section class="layout-grid space-lg response-template-form-grid">
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Name
          <NInput v-model:value="form.name" size="small" required />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Kind
          <AccessibleSelect
            :value="form.kind"
            accessible-label="Response template kind"
            size="small"
            :options="kindOptions"
            :disabled="Boolean(form.id)"
            @update:value="applyKind(Number($event) as PublicResponseTemplateKind)"
          />
          <span v-if="form.id" class="normal-text letter-normal">Kind cannot be changed after creation.</span>
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Description
          <NInput v-model:value="form.description" size="small" />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Content type
          <NInput v-model:value="form.contentType" size="small" />
        </label>
      </section>

      <HtmlTemplateEditor v-model="form.body" :kind="form.kind" :content-type="form.contentType" />

      <div class="editor-drawer-actions layout-row align-end-row space-md">
        <NButton secondary @click="close">Cancel</NButton>
        <DisabledHint :disabled="Boolean(disabledReason)" :reason="disabledReason">
          <NButton type="primary" attr-type="submit" :disabled="Boolean(disabledReason)">
            {{ submitLabel }}
          </NButton>
        </DisabledHint>
      </div>
    </form>
    </NDrawerContent>
  </NDrawer>
</template>
