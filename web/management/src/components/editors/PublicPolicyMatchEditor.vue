<script setup lang="ts">
import PlusIcon from "@primevue/icons/plus";
import TrashIcon from "@primevue/icons/trash";
import {
  PublicPolicyMatchBooleanOperator,
  PublicPolicyMatchConditionOperator,
  PublicPolicyMatchField,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  builderToCEL,
  conditionNeedsName,
  conditionUsesValues,
  emptyCondition,
  emptyGroup,
  normalizeConditionForField,
  policyMatchValidationReason,
  type PolicyMatchConditionForm,
  type PolicyMatchForm,
  type PolicyMatchGroupForm,
} from "@/lib/publicPolicyMatch";

const props = defineProps<{
  form: PolicyMatchForm;
}>();

const fieldOptions = [
  { label: "Method", value: PublicPolicyMatchField.METHOD },
  { label: "Protocol", value: PublicPolicyMatchField.PROTOCOL },
  { label: "Host", value: PublicPolicyMatchField.HOST },
  { label: "Path", value: PublicPolicyMatchField.PATH },
  { label: "Remote IP", value: PublicPolicyMatchField.REMOTE_IP },
  { label: "Header", value: PublicPolicyMatchField.HEADER },
  { label: "Cookie", value: PublicPolicyMatchField.COOKIE },
  { label: "Query", value: PublicPolicyMatchField.QUERY_PARAM },
];

const operatorOptions = [
  { label: "Present", value: PublicPolicyMatchConditionOperator.PRESENT },
  { label: "Equals", value: PublicPolicyMatchConditionOperator.EQUALS },
  { label: "Prefix", value: PublicPolicyMatchConditionOperator.PREFIX },
  { label: "Suffix", value: PublicPolicyMatchConditionOperator.SUFFIX },
  { label: "Contains", value: PublicPolicyMatchConditionOperator.CONTAINS },
  { label: "Regex", value: PublicPolicyMatchConditionOperator.MATCHES },
  { label: "In", value: PublicPolicyMatchConditionOperator.IN },
  { label: "CIDR", value: PublicPolicyMatchConditionOperator.CIDR },
  { label: "Host pattern", value: PublicPolicyMatchConditionOperator.HOST_PATTERN },
];

function addCondition(group: PolicyMatchGroupForm) {
  group.conditions.push(emptyCondition());
}

function removeCondition(group: PolicyMatchGroupForm, index: number) {
  group.conditions.splice(index, 1);
}

function addGroup() {
  const group = emptyGroup();
  group.operator = PublicPolicyMatchBooleanOperator.ANY;
  group.conditions.push(emptyCondition(PublicPolicyMatchField.HEADER));
  props.form.root.groups.push(group);
}

function removeGroup(index: number) {
  props.form.root.groups.splice(index, 1);
}

function switchMode(mode: "builder" | "expression") {
  if (mode === props.form.mode) return;
  if (mode === "expression") {
    props.form.expression = builderToCEL(props.form.root);
  }
  props.form.mode = mode;
}

function onFieldChange(condition: PolicyMatchConditionForm) {
  if (condition.field === PublicPolicyMatchField.REMOTE_IP) condition.operator = PublicPolicyMatchConditionOperator.CIDR;
  if (condition.field === PublicPolicyMatchField.HOST) condition.operator = PublicPolicyMatchConditionOperator.HOST_PATTERN;
  if (condition.field === PublicPolicyMatchField.PATH) condition.operator = PublicPolicyMatchConditionOperator.PREFIX;
  normalizeConditionForField(condition);
}

function onOperatorChange(condition: PolicyMatchConditionForm) {
  normalizeConditionForField(condition);
}

function namePlaceholder(condition: PolicyMatchConditionForm): string {
  switch (condition.field) {
    case PublicPolicyMatchField.HEADER:
      return "x-plan";
    case PublicPolicyMatchField.COOKIE:
      return "session";
    case PublicPolicyMatchField.QUERY_PARAM:
      return "version";
    default:
      return "";
  }
}

function valuePlaceholder(condition: PolicyMatchConditionForm): string {
  switch (condition.operator) {
    case PublicPolicyMatchConditionOperator.CIDR:
      return "203.0.113.0/24";
    case PublicPolicyMatchConditionOperator.HOST_PATTERN:
      return "api.example.com";
    case PublicPolicyMatchConditionOperator.MATCHES:
      return "^/api/(v1|v2)";
    case PublicPolicyMatchConditionOperator.IN:
      return "GET, POST";
    default:
      return "value";
  }
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
      <textarea v-model="form.expression" class="vercel-input expression-input" spellcheck="false" placeholder='method == "POST" && path_prefix(path, "/login")' />
    </div>

    <div v-else class="builder-panel">
      <div class="builder-toolbar">
        <select v-model="form.root.operator" class="vercel-input compact-select">
          <option :value="PublicPolicyMatchBooleanOperator.ALL">All</option>
          <option :value="PublicPolicyMatchBooleanOperator.ANY">Any</option>
        </select>
        <label class="negate-toggle">
          <input v-model="form.root.negated" type="checkbox" />
          Not
        </label>
        <button type="button" class="tool-button" @click="addCondition(form.root)">
          <PlusIcon class="h-3.5 w-3.5" />
          <span>Condition</span>
        </button>
        <button type="button" class="tool-button" @click="addGroup">
          <PlusIcon class="h-3.5 w-3.5" />
          <span>Group</span>
        </button>
      </div>

      <div class="condition-list">
        <p v-if="!form.root.conditions.length && !form.root.groups.length" class="empty-match">No request match conditions.</p>
        <div v-for="(condition, index) in form.root.conditions" :key="`root-${index}`" class="condition-row">
          <select v-model="condition.field" class="vercel-input" @change="onFieldChange(condition)">
            <option v-for="option in fieldOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>
          <input v-if="conditionNeedsName(condition.field)" v-model="condition.name" class="vercel-input" :placeholder="namePlaceholder(condition)" />
          <select v-model="condition.operator" class="vercel-input" @change="onOperatorChange(condition)">
            <option v-for="option in operatorOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>
          <textarea
            v-if="conditionUsesValues(condition.operator)"
            v-model="condition.valuesText"
            class="vercel-input value-input"
            :placeholder="valuePlaceholder(condition)"
          />
          <label class="negate-toggle small">
            <input v-model="condition.negated" type="checkbox" />
            Not
          </label>
          <button type="button" class="icon-button" aria-label="Remove condition" title="Remove condition" @click="removeCondition(form.root, index)">
            <TrashIcon class="h-3.5 w-3.5" />
          </button>
        </div>
      </div>

      <div v-for="(group, groupIndex) in form.root.groups" :key="`group-${groupIndex}`" class="nested-group">
        <div class="builder-toolbar">
          <select v-model="group.operator" class="vercel-input compact-select">
            <option :value="PublicPolicyMatchBooleanOperator.ALL">All</option>
            <option :value="PublicPolicyMatchBooleanOperator.ANY">Any</option>
          </select>
          <label class="negate-toggle">
            <input v-model="group.negated" type="checkbox" />
            Not
          </label>
          <button type="button" class="tool-button" @click="addCondition(group)">
            <PlusIcon class="h-3.5 w-3.5" />
            <span>Condition</span>
          </button>
          <button type="button" class="icon-button" aria-label="Remove group" title="Remove group" @click="removeGroup(groupIndex)">
            <TrashIcon class="h-3.5 w-3.5" />
          </button>
        </div>
        <div class="condition-list">
          <div v-for="(condition, index) in group.conditions" :key="`nested-${groupIndex}-${index}`" class="condition-row">
            <select v-model="condition.field" class="vercel-input" @change="onFieldChange(condition)">
              <option v-for="option in fieldOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
            <input v-if="conditionNeedsName(condition.field)" v-model="condition.name" class="vercel-input" :placeholder="namePlaceholder(condition)" />
            <select v-model="condition.operator" class="vercel-input" @change="onOperatorChange(condition)">
              <option v-for="option in operatorOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
            <textarea
              v-if="conditionUsesValues(condition.operator)"
              v-model="condition.valuesText"
              class="vercel-input value-input"
              :placeholder="valuePlaceholder(condition)"
            />
            <label class="negate-toggle small">
              <input v-model="condition.negated" type="checkbox" />
              Not
            </label>
            <button type="button" class="icon-button" aria-label="Remove condition" title="Remove condition" @click="removeCondition(group, index)">
              <TrashIcon class="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
      </div>

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

.match-head,
.builder-toolbar {
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

.mode-tabs button,
.tool-button,
.icon-button {
  border-radius: 5px;
  color: #d4d4d8;
  font-size: 0.75rem;
  font-weight: 650;
  transition: background 140ms ease, color 140ms ease, border-color 140ms ease;
}

.mode-tabs button {
  height: 2rem;
}

.mode-tabs button.active {
  background: #fff;
  color: #000;
}

.builder-panel,
.expression-panel,
.nested-group {
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

.compact-select {
  width: 7.5rem;
}

.tool-button {
  display: inline-flex;
  height: 2rem;
  align-items: center;
  gap: 0.4rem;
  border: 1px solid #333;
  background: #080808;
  padding: 0 0.65rem;
}

.tool-button:hover,
.icon-button:hover {
  border-color: #666;
  background: #111;
  color: #fff;
}

.condition-list {
  display: grid;
  gap: 0.5rem;
}

.condition-row {
  display: grid;
  grid-template-columns: minmax(7.5rem, 0.9fr) minmax(0, 1fr) minmax(7.5rem, 0.8fr) minmax(0, 1.35fr) auto 2.25rem;
  gap: 0.5rem;
  align-items: start;
  min-width: 0;
}

.condition-row > input:nth-child(2) + select {
  grid-column: auto;
}

.condition-row > select:nth-child(1) + select {
  grid-column: span 2;
}

.value-input {
  min-height: 2.25rem;
  max-height: 6rem;
  resize: vertical;
  font-size: 0.8rem;
  letter-spacing: 0;
  text-transform: none;
}

.negate-toggle {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  color: #d4d4d8;
  font-size: 0.78rem;
  min-height: 2.25rem;
}

.negate-toggle.small {
  justify-self: center;
}

.icon-button {
  display: inline-grid;
  width: 2.25rem;
  height: 2.25rem;
  place-items: center;
  border: 1px solid #333;
  background: #080808;
}

.nested-group {
  border: 1px solid #222;
  border-radius: 6px;
  background: #030303;
  padding: 0.75rem;
}

.empty-match {
  display: grid;
  min-height: 4.5rem;
  place-items: center;
  border: 1px dashed #333;
  border-radius: 6px;
  color: #777;
  font-size: 0.82rem;
}

.field-error {
  color: #f87171;
  font-size: 0.78rem;
}

@media (max-width: 860px) {
  .condition-row {
    grid-template-columns: 1fr;
  }

  .condition-row > select:nth-child(1) + select {
    grid-column: auto;
  }

  .negate-toggle.small,
  .icon-button {
    justify-self: stretch;
  }

  .icon-button {
    width: 100%;
  }
}
</style>
