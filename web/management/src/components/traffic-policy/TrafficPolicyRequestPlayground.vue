<script setup lang="ts">
import { RotateCcw as ResetIcon } from "@lucide/vue";
import { NButton, NCheckbox, NInput, NTag } from "naive-ui";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import { countryOptions } from "@/lib/countryCatalog";
import type {
  TrafficPolicyAttentionWarning,
  TrafficPolicyMatchState,
  TrafficPolicyPlaygroundStage,
  TrafficPolicyPreviewForm,
} from "@/lib/trafficPolicyWorkbench";

type SelectOption = {
  label: string;
  value: string;
};

const props = defineProps<{
  modelValue: TrafficPolicyPreviewForm;
  stages: TrafficPolicyPlaygroundStage[];
  routeOptions: SelectOption[];
  targetOptions: SelectOption[];
  globalAttention: TrafficPolicyAttentionWarning[];
}>();

const emit = defineEmits<{
  "update:modelValue": [value: TrafficPolicyPreviewForm];
  reset: [];
}>();

const methodOptions = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"].map((value) => ({ label: value, value }));
const protocolOptions = [
  { label: "HTTPS", value: "https" },
  { label: "HTTP", value: "http" },
];
const countryCodeOptions = [
  { label: "Unknown / unresolved", value: "__unknown__" },
  ...countryOptions("en"),
];

function updateField<K extends keyof TrafficPolicyPreviewForm>(key: K, value: TrafficPolicyPreviewForm[K]) {
  emit("update:modelValue", {
    ...props.modelValue,
    [key]: value,
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
        <h3 class="copy-sm weight-semibold base-text">Request preview</h3>
        <p class="copy-xs muted-text">Builder rules are evaluated locally in this browser; CEL-only rules stay marked unknown.</p>
      </div>
      <NButton secondary size="small" @click="emit('reset')">
        <template #icon><ResetIcon class="icon-sm" /></template>
        Reset
      </NButton>
    </div>

    <div class="policy-playground-grid">
      <div class="policy-playground-form">
        <div class="request-fields">
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Method
            <AccessibleSelect
              :value="modelValue.method"
              accessible-label="Request method"
              size="small"
              :options="methodOptions"
              @update:value="(value) => updateField('method', String(value))"
            />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Protocol
            <AccessibleSelect
              :value="modelValue.protocol"
              accessible-label="Request protocol"
              size="small"
              :options="protocolOptions"
              @update:value="(value) => updateField('protocol', String(value))"
            />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text request-span-two">
            Host
            <NInput :value="modelValue.host" size="small" placeholder="app.example.com" @update:value="(value) => updateField('host', value)" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text request-span-two">
            Path
            <NInput :value="modelValue.path" size="small" placeholder="/assets/app.js" @update:value="(value) => updateField('path', value)" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Remote IP
            <NInput :value="modelValue.remoteIp" size="small" placeholder="198.51.100.10" @update:value="(value) => updateField('remoteIp', value)" />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Country result
            <AccessibleSelect
              :value="modelValue.countryCode"
              accessible-label="Synthetic GeoIP country result"
              size="small"
              filterable
              :options="countryCodeOptions"
              @update:value="(value) => updateField('countryCode', String(value))"
            />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text request-body-field">
            Request
            <NCheckbox
              :checked="modelValue.hasRequestBody"
              @update:checked="(value) => updateField('hasRequestBody', Boolean(value))"
            >
              Has body
            </NCheckbox>
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Route
            <AccessibleSelect
              :value="modelValue.routeId"
              accessible-label="Request route"
              size="small"
              clearable
              filterable
              :options="routeOptions"
              placeholder="Any"
              @update:value="(value) => updateField('routeId', value ? String(value) : '')"
            />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Target
            <AccessibleSelect
              :value="modelValue.targetId"
              accessible-label="Request target"
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
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
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
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
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
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
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
              <h4>{{ stage.label }}</h4>
              <p>{{ stage.mode === 'all' ? 'All matching rules are considered' : 'First matching candidate wins' }}</p>
            </div>
            <NTag size="small" :bordered="false">{{ stage.items.length.toString() }}</NTag>
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
  gap: 1.25rem;
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
  gap: 1.5rem;
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
  gap: 0.875rem;
}

.request-fields {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.request-span-two {
  grid-column: 1 / -1;
}

.request-map-input {
  font-size: 0.75rem;
}

.request-body-field {
  align-content: end;
}

.request-body-field :deep(.n-checkbox__label) {
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 400;
}

.global-attention {
  display: flex;
  flex-wrap: wrap;
  gap: 0.375rem;
  padding-bottom: 1rem;
}

.global-attention :deep(.n-tag) {
  max-width: 100%;
  height: auto;
}

.global-attention :deep(.n-tag__content) {
  overflow-wrap: anywhere;
  white-space: normal;
}

.policy-playground-results {
  gap: 0;
}

.stage-preview {
  display: grid;
  gap: 0.5rem;
  min-width: 0;
  padding: 0.875rem 0;
}

.stage-preview + .stage-preview {
  border-top: 1px solid var(--app-border-subtle);
}

.policy-playground-results > .stage-preview:first-child {
  padding-top: 0;
}

.stage-preview-head h4 {
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 600;
}

.stage-preview-head p,
.stage-preview-empty {
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.stage-preview-list {
  display: grid;
  gap: 0.125rem;
}

.stage-preview-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 0.75rem;
  min-height: 2.5rem;
  border-radius: 6px;
  padding: 0.375rem 0.625rem;
}

.stage-preview-row--selected {
  background: var(--app-panel-muted);
}

.stage-preview-row--skipped {
  opacity: 0.68;
}

@media (min-width: 1100px) {
  .policy-playground-grid {
    grid-template-columns: minmax(20rem, 0.85fr) minmax(28rem, 1.15fr);
    gap: 2rem;
  }

  .request-maps {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
</style>
