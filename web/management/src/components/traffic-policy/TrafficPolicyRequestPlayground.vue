<script setup lang="ts">
import { RotateCcw as ResetIcon } from "@lucide/vue";
import { NButton, NInput, NSelect, NTag } from "naive-ui";
import type {
  TrafficPolicyAttentionWarning,
  TrafficPolicyMatchState,
} from "@/lib/trafficPolicyWorkbench";

type PreviewForm = {
  method: string;
  protocol: string;
  host: string;
  path: string;
  remoteIp: string;
  headersText: string;
  cookiesText: string;
  queryText: string;
  routeId: string;
  targetId: string;
};

type SelectOption = {
  label: string;
  value: string;
};

type PlaygroundStageItem = {
  id: bigint;
  name: string;
  priority: bigint;
  state: TrafficPolicyMatchState;
  reason: string;
  selected: boolean;
  skipped: boolean;
};

type PlaygroundStage = {
  key: string;
  label: string;
  mode: "first" | "all";
  items: PlaygroundStageItem[];
};

const props = defineProps<{
  modelValue: PreviewForm;
  stages: PlaygroundStage[];
  routeOptions: SelectOption[];
  targetOptions: SelectOption[];
  globalAttention: TrafficPolicyAttentionWarning[];
}>();

const emit = defineEmits<{
  "update:modelValue": [value: PreviewForm];
  reset: [];
}>();

const methodOptions = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"].map((value) => ({ label: value, value }));
const protocolOptions = [
  { label: "HTTPS", value: "https" },
  { label: "HTTP", value: "http" },
];

function updateField<K extends keyof PreviewForm>(key: K, value: PreviewForm[K] | null) {
  emit("update:modelValue", {
    ...props.modelValue,
    [key]: value ?? "",
  });
}

function statusType(status: TrafficPolicyMatchState): "success" | "warning" | "default" {
  if (status === "match") return "success";
  if (status === "unknown") return "warning";
  return "default";
}

function statusLabel(status: TrafficPolicyMatchState): string {
  if (status === "match") return "Match";
  if (status === "unknown") return "Unknown";
  return "No match";
}
</script>

<template>
  <section class="policy-playground" aria-label="Traffic policy request playground">
    <div class="policy-playground-head">
      <div>
        <h4 class="copy-sm weight-semibold base-text">Request Preview</h4>
        <p class="copy-xs muted-text">Builder rules are evaluated locally; CEL-only rules stay marked unknown.</p>
      </div>
      <NButton secondary size="small" @click="emit('reset')">
        <template #icon><ResetIcon class="icon-sm" /></template>
        Reset
      </NButton>
    </div>

    <div class="policy-playground-grid">
      <div class="policy-playground-form">
        <div class="request-fields">
          <label class="field-label">
            Method
            <NSelect
              :value="modelValue.method"
              size="small"
              :options="methodOptions"
              @update:value="(value) => updateField('method', String(value))"
            />
          </label>
          <label class="field-label">
            Protocol
            <NSelect
              :value="modelValue.protocol"
              size="small"
              :options="protocolOptions"
              @update:value="(value) => updateField('protocol', String(value))"
            />
          </label>
          <label class="field-label request-span-two">
            Host
            <NInput :value="modelValue.host" size="small" placeholder="app.example.com" @update:value="(value) => updateField('host', value)" />
          </label>
          <label class="field-label request-span-two">
            Path
            <NInput :value="modelValue.path" size="small" placeholder="/assets/app.js" @update:value="(value) => updateField('path', value)" />
          </label>
          <label class="field-label request-span-two">
            Remote IP
            <NInput :value="modelValue.remoteIp" size="small" placeholder="198.51.100.10" @update:value="(value) => updateField('remoteIp', value)" />
          </label>
          <label class="field-label">
            Route
            <NSelect
              :value="modelValue.routeId"
              size="small"
              clearable
              filterable
              :options="routeOptions"
              placeholder="Any"
              @update:value="(value) => updateField('routeId', value ? String(value) : '')"
            />
          </label>
          <label class="field-label">
            Target
            <NSelect
              :value="modelValue.targetId"
              size="small"
              clearable
              filterable
              :options="targetOptions"
              placeholder="Any"
              @update:value="(value) => updateField('targetId', value ? String(value) : '')"
            />
          </label>
        </div>

        <div class="request-maps">
          <label class="field-label">
            Headers
            <NInput
              :value="modelValue.headersText"
              type="textarea"
              class="request-map-input mono-text"
              placeholder="X-Plan: pro"
              :autosize="{ minRows: 3, maxRows: 6 }"
              @update:value="(value) => updateField('headersText', value)"
            />
          </label>
          <label class="field-label">
            Cookies
            <NInput
              :value="modelValue.cookiesText"
              type="textarea"
              class="request-map-input mono-text"
              placeholder="sid=abc"
              :autosize="{ minRows: 3, maxRows: 6 }"
              @update:value="(value) => updateField('cookiesText', value)"
            />
          </label>
          <label class="field-label">
            Query
            <NInput
              :value="modelValue.queryText"
              type="textarea"
              class="request-map-input mono-text"
              placeholder="debug=1"
              :autosize="{ minRows: 3, maxRows: 6 }"
              @update:value="(value) => updateField('queryText', value)"
            />
          </label>
        </div>
      </div>

      <div class="policy-playground-results">
        <div v-if="globalAttention.length" class="global-attention">
          <NTag
            v-for="(item, index) in globalAttention"
            :key="`${item.code}-${item.ruleId?.toString() || 'global'}-${index.toString()}`"
            size="small"
            :bordered="false"
            :type="item.code === 'cache-settings-disabled' ? 'warning' : 'info'"
          >
            {{ item.message }}
          </NTag>
        </div>

        <div v-for="stage in stages" :key="stage.key" class="stage-preview">
          <div class="stage-preview-head">
            <div>
              <h5>{{ stage.label }}</h5>
              <p>{{ stage.mode === 'all' ? 'All matching rules are considered' : 'First matching candidate wins' }}</p>
            </div>
            <NTag size="small" :bordered="false" type="info">{{ stage.items.length.toString() }}</NTag>
          </div>

          <div v-if="stage.items.length" class="stage-preview-list">
            <div
              v-for="item in stage.items"
              :key="`${stage.key}-${item.id.toString()}`"
              class="stage-preview-row"
              :class="{ 'stage-preview-row--selected': item.selected, 'stage-preview-row--skipped': item.skipped }"
            >
              <div class="min-width-zero">
                <p class="clip-text copy-xs weight-medium base-text">
                  {{ item.name || `#${item.id.toString()}` }}
                </p>
                <p class="clip-text copy-2xs muted-text">
                  P{{ item.priority.toString() }} / {{ item.reason }}
                </p>
              </div>
              <NTag size="small" :bordered="false" :type="statusType(item.state)">
                {{ item.skipped ? 'Skipped' : statusLabel(item.state) }}
              </NTag>
            </div>
          </div>

          <p v-else class="stage-preview-empty">No rules configured.</p>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.policy-playground {
  display: grid;
  gap: 1rem;
  padding: 1rem;
}

.policy-playground-head,
.stage-preview-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
  min-width: 0;
}

.policy-playground-grid {
  display: grid;
  gap: 1rem;
}

.policy-playground-form,
.policy-playground-results {
  display: grid;
  align-content: start;
  gap: 1rem;
  min-width: 0;
}

.request-fields,
.request-maps {
  display: grid;
  gap: 0.75rem;
}

.request-fields {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.request-span-two {
  grid-column: 1 / -1;
}

.field-label {
  display: grid;
  gap: 0.375rem;
  min-width: 0;
  color: var(--app-text-muted);
  font-size: 0.72rem;
  font-weight: 600;
  letter-spacing: 0.03em;
  text-transform: uppercase;
}

.request-map-input {
  font-size: 0.75rem;
  letter-spacing: 0;
  text-transform: none;
}

.global-attention {
  display: flex;
  flex-wrap: wrap;
  gap: 0.375rem;
}

.global-attention :deep(.n-tag) {
  max-width: 100%;
  height: auto;
}

.global-attention :deep(.n-tag__content) {
  overflow-wrap: anywhere;
  white-space: normal;
}

.stage-preview {
  display: grid;
  gap: 0.625rem;
  min-width: 0;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
  padding: 0.75rem;
}

.stage-preview-head h5 {
  color: var(--app-text);
  font-size: 0.78rem;
  font-weight: 650;
}

.stage-preview-head p,
.stage-preview-empty {
  color: var(--app-text-muted);
  font-size: 0.7rem;
}

.stage-preview-list {
  display: grid;
  gap: 0.375rem;
}

.stage-preview-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 0.75rem;
  min-height: 2.5rem;
  border: 1px solid var(--app-border-subtle);
  border-radius: 5px;
  background: var(--app-panel);
  padding: 0.5rem 0.625rem;
}

.stage-preview-row--selected {
  border-color: color-mix(in srgb, var(--app-success) 34%, var(--app-border));
}

.stage-preview-row--skipped {
  opacity: 0.68;
}

@media (min-width: 1100px) {
  .policy-playground-grid {
    grid-template-columns: minmax(20rem, 0.85fr) minmax(28rem, 1.15fr);
  }

  .request-maps {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
</style>
