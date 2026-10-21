<script setup lang="ts">
import { computed } from "vue";
import { NInput } from "naive-ui";
import PolicyMatchGroupEditor from "@/components/editors/PolicyMatchGroupEditor.vue";
import SegmentedControl from "@/components/ui/SegmentedControl.vue";
import {
  builderToCEL,
  policyMatchValidationReason,
  syncGeneratedExpressionForExpertMode,
  type PolicyMatchForm,
} from "@/lib/publicPolicyMatch";

const props = defineProps<{
  form: PolicyMatchForm;
}>();

const matchSummary = computed(() => {
  if (props.form.mode === "expression") {
    const expression = props.form.expression.trim();
    return expression ? expression : "true";
  }
  return builderToCEL(props.form.root) || "true";
});

const modeOptions = [
  { label: "Builder", value: "builder" },
  { label: "CEL", value: "expression" },
] as const;

function switchMode(mode: "builder" | "expression") {
  if (mode === props.form.mode) return;
  if (mode === "expression") {
    syncGeneratedExpressionForExpertMode(props.form);
  }
  props.form.mode = mode;
}

function validationReason(): string {
  return policyMatchValidationReason(props.form);
}

defineExpose({ validationReason });
</script>

<template>
  <section class="policy-match editor-group">
    <div class="match-head">
      <div class="layout-grid space-2xs min-width-zero">
        <h3 class="match-title">Match</h3>
        <p class="copy-xs line-normal muted-text">Requests that match these conditions are handled by this rule. An empty match applies to every request.</p>
      </div>
      <SegmentedControl
        :model-value="form.mode"
        :options="modeOptions"
        label="Match editor mode"
        @update:model-value="switchMode"
      />
    </div>

    <div v-if="form.mode === 'expression'" class="expression-panel">
      <NInput
        v-model:value="form.expression"
        type="textarea"
        class="expression-input"
        spellcheck="false"
        placeholder='method == "POST" && path_prefix(path, "/login")'
      />
      <p class="match-expression-summary mono-text">{{ matchSummary }}</p>
    </div>

    <div v-else class="builder-panel">
      <PolicyMatchGroupEditor :group="form.root" root />
      <p class="match-expression-summary mono-text">{{ matchSummary }}</p>
      <p v-if="validationReason()" class="field-error">{{ validationReason() }}</p>
    </div>
  </section>
</template>

<style scoped>
.policy-match {
  display: grid;
  gap: 1rem;
}

.editor-group {
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 1.25rem;
}

.match-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 0.75rem 1rem;
  flex-wrap: wrap;
}

.match-title {
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
}

.builder-panel,
.expression-panel {
  display: grid;
  gap: 0.75rem;
}

.expression-input {
  min-height: 9rem;
  font-family: var(--font-mono);
  font-size: 0.8125rem;
}

.field-error {
  color: var(--app-error);
  font-size: 0.75rem;
}

.match-expression-summary {
  max-height: 4.5rem;
  overflow: auto;
  overflow-wrap: anywhere;
  border-radius: 6px;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.45;
  padding: 0.5rem 0.625rem;
}
</style>
