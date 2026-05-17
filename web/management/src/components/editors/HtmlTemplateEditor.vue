<script lang="ts">
export default {
  name: "HtmlTemplateEditor",
};
</script>

<script setup lang="ts">
import { autocompletion, type Completion, type CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { html, htmlLanguage } from "@codemirror/lang-html";
import { EditorState, type Extension } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { abbreviationTracker, emmetConfig, EmmetKnownSyntax, expandAbbreviation } from "@emmetio/codemirror6-plugin";
import { basicSetup } from "codemirror";
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import { PublicResponseTemplateKind } from "@/gen/proto/p2pstream/v1/management_pb";

type PlaceholderInfo = {
  name: string;
  label: string;
  description: string;
  requiredFor: PublicResponseTemplateKind[];
  trustedHtml?: boolean;
};

const props = withDefaults(defineProps<{
  modelValue: string;
  kind: PublicResponseTemplateKind;
  contentType?: string;
}>(), {
  contentType: "text/html; charset=utf-8",
});

const emit = defineEmits<{
  (event: "update:modelValue", value: string): void;
}>();

const editorHost = ref<HTMLDivElement | null>(null);
const editorView = shallowRef<EditorView | null>(null);
const isUpdatingFromEditor = ref(false);

const placeholders: PlaceholderInfo[] = [
  {
    name: "captcha_element_html",
    label: "{{ .captcha_element_html }}",
    description: "Server-generated captcha widget and submit form.",
    requiredFor: [PublicResponseTemplateKind.WAF_CAPTCHA_PAGE],
    trustedHtml: true,
  },
  {
    name: "queue_position",
    label: "{{ .queue_position }}",
    description: "Current queue position for waiting-room clients.",
    requiredFor: [PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE],
  },
  {
    name: "retry_after_seconds",
    label: "{{ .retry_after_seconds }}",
    description: "Seconds until the browser should check again.",
    requiredFor: [PublicResponseTemplateKind.WAF_WAITING_ROOM_PAGE],
  },
  { name: "host", label: "{{ .host }}", description: "Request host shown to the visitor.", requiredFor: [] },
  { name: "rule_name", label: "{{ .rule_name }}", description: "Name of the WAF rule rendering the page.", requiredFor: [] },
  { name: "reference_id", label: "{{ .reference_id }}", description: "Short support reference for the WAF decision.", requiredFor: [] },
  { name: "page_title", label: "{{ .page_title }}", description: "Configured page title.", requiredFor: [] },
  { name: "page_body", label: "{{ .page_body }}", description: "Configured body copy.", requiredFor: [] },
  { name: "status_url", label: "{{ .status_url }}", description: "Waiting-room status endpoint or captcha form endpoint.", requiredFor: [] },
];

const availablePlaceholders = computed(() => {
  if (props.kind === PublicResponseTemplateKind.GENERIC_BODY) return [];
  return placeholders.filter((placeholder) => (
    !placeholder.requiredFor.length ||
    placeholder.requiredFor.includes(props.kind) ||
    placeholder.name === "host" ||
    placeholder.name === "rule_name" ||
    placeholder.name === "reference_id" ||
    placeholder.name === "page_title" ||
    placeholder.name === "page_body" ||
    placeholder.name === "status_url"
  ));
});

const requiredPlaceholders = computed(() => placeholders.filter((placeholder) => placeholder.requiredFor.includes(props.kind)));
const referencedPlaceholders = computed(() => {
  const names = new Set<string>();
  const pattern = /{{\s*\.([A-Za-z_][A-Za-z0-9_]*)/g;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(props.modelValue)) !== null) {
    names.add(match[1]);
  }
  return names;
});
const unknownPlaceholders = computed(() => {
  const allowed = new Set(availablePlaceholders.value.map((placeholder) => placeholder.name));
  return [...referencedPlaceholders.value].filter((name) => props.kind !== PublicResponseTemplateKind.GENERIC_BODY && !allowed.has(name));
});
const unknownPlaceholderLabels = computed(() => unknownPlaceholders.value.map((name) => `{{ .${name} }}`).join(", "));
const missingRequiredPlaceholders = computed(() => requiredPlaceholders.value.filter((placeholder) => !referencedPlaceholders.value.has(placeholder.name)));
const previewSource = computed(() => renderPreview(props.modelValue, props.kind, props.contentType));

function placeholderCompletionSource(context: CompletionContext): CompletionResult | null {
  if (props.kind === PublicResponseTemplateKind.GENERIC_BODY) return null;
  const match = context.matchBefore(/\{\{\s*\.?[A-Za-z0-9_]*$/);
  if (!match && !context.explicit) return null;
  const from = match?.from ?? context.pos;
  const options: Completion[] = availablePlaceholders.value.map((placeholder) => ({
    label: placeholder.label,
    detail: placeholder.requiredFor.includes(props.kind) ? "required" : "optional",
    info: placeholder.description,
    type: placeholder.trustedHtml ? "keyword" : "variable",
    apply: placeholder.label,
  }));
  return {
    from,
    options,
    validFor: /^[{\s.A-Za-z0-9_]*$/,
  };
}

function editorExtensions(): Extension[] {
  return [
    basicSetup,
    html({ autoCloseTags: true }),
    htmlLanguage.data.of({ autocomplete: placeholderCompletionSource }),
    emmetConfig.of({ syntax: EmmetKnownSyntax.html }),
    abbreviationTracker({ syntax: EmmetKnownSyntax.html }),
    keymap.of([{ key: "Tab", run: expandAbbreviation }]),
    autocompletion({ activateOnTyping: true }),
    EditorView.lineWrapping,
    EditorView.theme({
      "&": {
        minHeight: "22rem",
        backgroundColor: "#030303",
        color: "#ededed",
        fontFamily: "var(--font-mono)",
        fontSize: "13px",
      },
      ".cm-scroller": {
        minHeight: "22rem",
        fontFamily: "var(--font-mono)",
      },
      ".cm-gutters": {
        backgroundColor: "#050505",
        color: "#666",
        borderRight: "1px solid #222",
      },
      ".cm-activeLine": {
        backgroundColor: "#0c0c0c",
      },
      ".cm-activeLineGutter": {
        backgroundColor: "#101010",
        color: "#aaa",
      },
      ".cm-cursor": {
        borderLeftColor: "#fff",
      },
      ".cm-selectionBackground": {
        backgroundColor: "#334155 !important",
      },
      ".cm-tooltip": {
        backgroundColor: "#080808",
        border: "1px solid #333",
        color: "#ededed",
      },
      ".cm-tooltip-autocomplete ul li[aria-selected]": {
        backgroundColor: "#fff",
        color: "#000",
      },
    }, { dark: true }),
    EditorView.updateListener.of((update) => {
      if (!update.docChanged) return;
      isUpdatingFromEditor.value = true;
      emit("update:modelValue", update.state.doc.toString());
      queueMicrotask(() => {
        isUpdatingFromEditor.value = false;
      });
    }),
  ];
}

function renderPreview(source: string, kind: PublicResponseTemplateKind, contentType: string): string {
  if (kind === PublicResponseTemplateKind.GENERIC_BODY && !contentType.toLowerCase().includes("html")) {
    return `<!doctype html><html><body><pre>${escapeHTML(source)}</pre></body></html>`;
  }
  let rendered = source;
  for (const placeholder of placeholders) {
    const sample = samplePlaceholderValue(placeholder.name, placeholder.trustedHtml === true);
    rendered = rendered.replace(new RegExp(`{{\\s*\\.${placeholder.name}\\s*}}`, "g"), sample);
  }
  return rendered;
}

function samplePlaceholderValue(name: string, trustedHtml: boolean): string {
  if (trustedHtml) {
    return `<form class="preview-captcha" method="post" action="/.p2pstream/waf/captcha/verify"><div class="cf-turnstile" data-sitekey="preview"></div><button type="submit">Continue</button></form>`;
  }
  const values: Record<string, string> = {
    host: "app.example.test",
    rule_name: "waf-rule",
    reference_id: "waf-42-preview",
    page_title: "Security check",
    page_body: "Traffic is being verified before continuing.",
    queue_position: "12",
    retry_after_seconds: "5",
    status_url: "/.p2pstream/waf/waiting-room/status?rule_id=42",
  };
  return escapeHTML(values[name] ?? "");
}

function escapeHTML(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

onMounted(() => {
  if (!editorHost.value) return;
  editorView.value = new EditorView({
    state: EditorState.create({
      doc: props.modelValue,
      extensions: editorExtensions(),
    }),
    parent: editorHost.value,
  });
});

watch(
  () => props.modelValue,
  (value) => {
    const view = editorView.value;
    if (!view || isUpdatingFromEditor.value) return;
    const current = view.state.doc.toString();
    if (current === value) return;
    view.dispatch({ changes: { from: 0, to: current.length, insert: value } });
  },
);

onBeforeUnmount(() => {
  editorView.value?.destroy();
  editorView.value = null;
});
</script>

<template>
  <div class="template-editor-shell">
    <div class="template-editor-toolbar">
      <div class="placeholder-list">
        <span
          v-for="placeholder in availablePlaceholders"
          :key="placeholder.name"
          class="placeholder-chip"
          :class="{ required: placeholder.requiredFor.includes(kind) }"
          :title="placeholder.description"
        >
          {{ placeholder.label }}
        </span>
        <span v-if="kind === PublicResponseTemplateKind.GENERIC_BODY" class="placeholder-muted">
          Raw body template
        </span>
      </div>
    </div>

    <div class="template-editor-grid">
      <section class="editor-pane" aria-label="Template editor">
        <div ref="editorHost" class="codemirror-host"></div>
      </section>
      <section class="preview-pane" aria-label="Template preview">
        <div class="preview-header">
          <span>Preview</span>
          <span>Sample data</span>
        </div>
        <iframe class="template-preview-frame" title="Template preview" sandbox="" :srcdoc="previewSource"></iframe>
      </section>
    </div>

    <div v-if="missingRequiredPlaceholders.length || unknownPlaceholders.length" class="validation-strip">
      <span v-if="missingRequiredPlaceholders.length">
        Missing required:
        {{ missingRequiredPlaceholders.map((placeholder) => placeholder.label).join(", ") }}
      </span>
      <span v-if="unknownPlaceholders.length">
        Unknown:
        {{ unknownPlaceholderLabels }}
      </span>
    </div>
  </div>
</template>

<style scoped>
.template-editor-shell {
  display: grid;
  gap: 0.75rem;
  min-width: 0;
}

.template-editor-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
}

.placeholder-list {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  gap: 0.4rem;
}

.placeholder-chip,
.placeholder-muted {
  border: 1px solid #333;
  border-radius: 5px;
  padding: 0.25rem 0.45rem;
  color: #a1a1aa;
  font-family: var(--font-mono);
  font-size: 0.7rem;
  line-height: 1.15;
}

.placeholder-chip.required {
  border-color: #4b5563;
  color: #f4f4f5;
}

.placeholder-muted {
  font-family: var(--font-body);
}

.template-editor-grid {
  display: grid;
  gap: 0.75rem;
}

@media (min-width: 1024px) {
  .template-editor-grid {
    grid-template-columns: minmax(0, 1.05fr) minmax(22rem, 0.95fr);
  }
}

.editor-pane,
.preview-pane {
  min-width: 0;
  overflow: hidden;
  border: 1px solid #222;
  border-radius: 6px;
  background: #030303;
}

.codemirror-host {
  min-height: 22rem;
}

.preview-pane {
  display: grid;
  grid-template-rows: auto minmax(22rem, 1fr);
}

.preview-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid #222;
  padding: 0.55rem 0.75rem;
  color: #888;
  font-size: 0.72rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.template-preview-frame {
  min-height: 22rem;
  width: 100%;
  border: 0;
  background: #fff;
}

.validation-strip {
  display: grid;
  gap: 0.35rem;
  border: 1px solid #3f3220;
  border-radius: 6px;
  background: #140f06;
  padding: 0.6rem 0.75rem;
  color: #facc15;
  font-size: 0.78rem;
  line-height: 1.45;
}
</style>
