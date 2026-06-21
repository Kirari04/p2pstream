<script setup lang="ts">
import { NButton, NButtonGroup, NInput } from "naive-ui";
import PolicyMatchGroupEditor from "@/components/editors/PolicyMatchGroupEditor.vue";
import {
  policyMatchValidationReason,
  syncGeneratedExpressionForExpertMode,
  type PolicyMatchForm,
} from "@/lib/publicPolicyMatch";

const props = defineProps<{
  form: PolicyMatchForm;
}>();

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
  <section class="policy-match">
    <div class="match-head">
      <h4>Match</h4>
      <NButtonGroup size="small" role="tablist" aria-label="Match editor mode">
        <NButton :type="form.mode === 'builder' ? 'primary' : 'default'" @click="switchMode('builder')">Builder</NButton>
        <NButton :type="form.mode === 'expression' ? 'primary' : 'default'" @click="switchMode('expression')">CEL</NButton>
      </NButtonGroup>
    </div>

    <div v-if="form.mode === 'expression'" class="expression-panel">
      <NInput
        v-model:value="form.expression"
        type="textarea"
        class="expression-input"
        spellcheck="false"
        placeholder='method == "POST" && path_prefix(path, "/login")'
      />
    </div>

    <div v-else class="builder-panel">
      <PolicyMatchGroupEditor :group="form.root" root />
      <p v-if="validationReason()" class="field-error">{{ validationReason() }}</p>
    </div>
  </section>
</template>

<style scoped>
.policy-match {
  display: grid;
  gap: 1rem;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
  padding: 1rem;
}

.match-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.match-head h4 {
  color: var(--app-text);
  font-size: 0.92rem;
  font-weight: 650;
}

.builder-panel,
.expression-panel {
  display: grid;
  gap: 0.75rem;
}

.expression-input {
  min-height: 9rem;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 0.82rem;
  letter-spacing: 0;
  text-transform: none;
}

.field-error {
  color: var(--app-error);
  font-size: 0.78rem;
}
</style>
