<script setup lang="ts">
import { Plus as PlusIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NCheckbox, NInput, NSelect } from "naive-ui";
import {
  PublicPolicyMatchBooleanOperator,
  PublicPolicyMatchConditionOperator,
  PublicPolicyMatchField,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  conditionNeedsName,
  conditionUsesValues,
  emptyCondition,
  emptyGroup,
  normalizeConditionForField,
  type PolicyMatchConditionForm,
  type PolicyMatchGroupForm,
} from "@/lib/publicPolicyMatch";

withDefaults(defineProps<{
  group: PolicyMatchGroupForm;
  root?: boolean;
  depth?: number;
}>(), {
  root: false,
  depth: 0,
});

const emit = defineEmits<{
  (event: "remove"): void;
}>();

const booleanOperatorOptions = [
  { label: "All", value: PublicPolicyMatchBooleanOperator.ALL },
  { label: "Any", value: PublicPolicyMatchBooleanOperator.ANY },
];

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

function addGroup(group: PolicyMatchGroupForm) {
  const child = emptyGroup();
  child.operator = PublicPolicyMatchBooleanOperator.ANY;
  child.conditions.push(emptyCondition(PublicPolicyMatchField.HEADER));
  group.groups.push(child);
}

function removeGroup(group: PolicyMatchGroupForm, index: number) {
  group.groups.splice(index, 1);
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
</script>

<template>
  <div class="policy-match-group" :class="{ nested: !root }">
    <div class="builder-toolbar">
      <NSelect v-model:value="group.operator" class="compact-select" size="small" :options="booleanOperatorOptions" />
      <NCheckbox v-model:checked="group.negated" class="negate-toggle">
        Not
      </NCheckbox>
      <NButton secondary size="small" @click="addCondition(group)">
        <PlusIcon class="h-3.5 w-3.5" />
        <span>Condition</span>
      </NButton>
      <NButton secondary size="small" @click="addGroup(group)">
        <PlusIcon class="h-3.5 w-3.5" />
        <span>Group</span>
      </NButton>
      <NButton v-if="!root" type="error" size="small" aria-label="Remove group" title="Remove group" @click="emit('remove')">
        <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
      </NButton>
    </div>

    <div class="condition-list">
      <p v-if="root && !group.conditions.length && !group.groups.length" class="empty-match">No request match conditions.</p>
      <div
        v-for="(condition, index) in group.conditions"
        :key="`condition-${depth}-${index}`"
        class="condition-row"
        :class="{
          'needs-name': conditionNeedsName(condition.field),
          'uses-values': conditionUsesValues(condition.operator),
        }"
      >
        <NSelect
          :value="condition.field"
          size="small"
          :options="fieldOptions"
          @update:value="(value) => { condition.field = Number(value) as PublicPolicyMatchField; onFieldChange(condition); }"
        />
        <NInput v-if="conditionNeedsName(condition.field)" v-model:value="condition.name" size="small" :placeholder="namePlaceholder(condition)" />
        <NSelect
          :value="condition.operator"
          class="operator-select"
          size="small"
          :options="operatorOptions"
          @update:value="(value) => { condition.operator = Number(value) as PublicPolicyMatchConditionOperator; onOperatorChange(condition); }"
        />
        <NInput
          v-if="conditionUsesValues(condition.operator)"
          v-model:value="condition.valuesText"
          type="textarea"
          class="value-input"
          :placeholder="valuePlaceholder(condition)"
        />
        <NCheckbox v-model:checked="condition.negated" class="negate-toggle small condition-negate">
          Not
        </NCheckbox>
        <NButton type="error" size="small" aria-label="Remove condition" title="Remove condition" @click="removeCondition(group, index)">
          <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
        </NButton>
      </div>
    </div>

    <div v-if="group.groups.length" class="child-groups">
      <PolicyMatchGroupEditor
        v-for="(child, groupIndex) in group.groups"
        :key="`group-${depth}-${groupIndex}`"
        :group="child"
        :depth="depth + 1"
        @remove="removeGroup(group, groupIndex)"
      />
    </div>
  </div>
</template>

<style scoped>
.policy-match-group,
.condition-list,
.child-groups {
  display: grid;
  gap: 0.75rem;
}

.policy-match-group.nested {
  border: 1px solid #222;
  border-radius: 6px;
  background: #030303;
  padding: 0.75rem;
}

.builder-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.compact-select {
  width: 7.5rem;
}

.tool-button,
.icon-button {
  border-radius: 5px;
  color: #d4d4d8;
  font-size: 0.75rem;
  font-weight: 650;
  transition: background 140ms ease, color 140ms ease, border-color 140ms ease;
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
  gap: 0.5rem;
}

.condition-row {
  display: grid;
  grid-template-columns: minmax(7.5rem, 0.9fr) minmax(0, 1fr) minmax(7.5rem, 0.8fr) minmax(0, 1.35fr) auto 2.25rem;
  gap: 0.5rem;
  align-items: start;
  min-width: 0;
}

.condition-row:not(.needs-name) .operator-select {
  grid-column: span 2;
}

.condition-row:not(.uses-values) .condition-negate {
  grid-column: span 2;
  justify-self: start;
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

.empty-match {
  display: grid;
  min-height: 4.5rem;
  place-items: center;
  border: 1px dashed #333;
  border-radius: 6px;
  color: #777;
  font-size: 0.82rem;
}

@media (max-width: 860px) {
  .condition-row {
    grid-template-columns: 1fr;
  }

  .condition-row:not(.needs-name) .operator-select,
  .condition-row:not(.uses-values) .condition-negate {
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
