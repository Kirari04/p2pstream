<script setup lang="ts">
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
      <div class="mode-tabs" role="tablist" aria-label="Match editor mode">
        <button type="button" :class="{ active: form.mode === 'builder' }" @click="switchMode('builder')">Builder</button>
        <button type="button" :class="{ active: form.mode === 'expression' }" @click="switchMode('expression')">CEL</button>
      </div>
    </div>

    <div v-if="form.mode === 'expression'" class="expression-panel">
      <textarea v-model="form.expression" class="app-control expression-input" spellcheck="false" placeholder='method == "POST" && path_prefix(path, "/login")' />
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
  border: 1px solid #222;
  border-radius: 6px;
  background: #050505;
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
  color: #fff;
  font-size: 0.92rem;
  font-weight: 650;
}

.mode-tabs {
  display: inline-grid;
  grid-template-columns: repeat(2, minmax(4.5rem, 1fr));
  border: 1px solid #333;
  border-radius: 6px;
  background: #080808;
  padding: 0.2rem;
}

.mode-tabs button {
  border-radius: 5px;
  color: #d4d4d8;
  font-size: 0.75rem;
  font-weight: 650;
  transition: background 140ms ease, color 140ms ease, border-color 140ms ease;
}

.mode-tabs button.active {
  background: #fff;
  color: #000;
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
  color: #f87171;
  font-size: 0.78rem;
}
</style>
