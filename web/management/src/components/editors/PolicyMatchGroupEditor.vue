<script setup lang="ts">
import { Plus as PlusIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NCheckbox, NInput } from "naive-ui";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
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
  <div class="policy-match-group" :class="{ nested: !root, 'nested--alt': !root && depth % 2 === 0 }">
    <div class="builder-toolbar">
      <AccessibleSelect
        v-model:value="group.operator"
        :accessible-label="root ? 'Root match operator' : `Match group at depth ${depth} operator`"
        class="compact-select"
        size="small"
        :options="booleanOperatorOptions"
      />
      <NCheckbox v-model:checked="group.negated" class="negate-toggle">
        Not
      </NCheckbox>
      <span class="builder-toolbar__spacer" aria-hidden="true"></span>
      <NButton secondary size="small" attr-type="button" @click="addCondition(group)">
        <template #icon><PlusIcon class="icon-sm" /></template>
        Condition
      </NButton>
      <NButton secondary size="small" attr-type="button" @click="addGroup(group)">
        <template #icon><PlusIcon class="icon-sm" /></template>
        Group
      </NButton>
      <NButton v-if="!root" type="error" secondary size="small" attr-type="button" aria-label="Remove group" title="Remove group" @click="emit('remove')">
        <template #icon><TrashIcon class="icon-sm" /></template>
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
        <AccessibleSelect
          :value="condition.field"
          :accessible-label="`Condition ${index + 1} field`"
          size="small"
          :options="fieldOptions"
          @update:value="(value) => { condition.field = Number(value) as PublicPolicyMatchField; onFieldChange(condition); }"
        />
        <NInput v-if="conditionNeedsName(condition.field)" v-model:value="condition.name" size="small" :placeholder="namePlaceholder(condition)" />
        <AccessibleSelect
          :value="condition.operator"
          :accessible-label="`Condition ${index + 1} operator`"
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
        <NButton type="error" secondary size="small" attr-type="button" class="condition-remove" aria-label="Remove condition" title="Remove condition" @click="removeCondition(group, index)">
          <template #icon><TrashIcon class="icon-sm" /></template>
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

/* Nested groups read as indented blocks: a muted fill, alternating with the panel color at deeper levels. */
.policy-match-group.nested {
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 0.75rem 0.875rem;
}

.policy-match-group.nested--alt {
  background: var(--app-panel);
}

.builder-toolbar {
  display: flex;
  align-items: center;
  gap: 0.5rem 0.75rem;
  flex-wrap: wrap;
}

.builder-toolbar__spacer {
  flex: 1 1 auto;
}

.compact-select {
  width: 7.5rem;
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
  font-size: 0.8125rem;
}

.negate-toggle {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  min-height: 2.25rem;
  color: var(--app-text);
  font-size: 0.8125rem;
}

.negate-toggle.small {
  justify-self: center;
}

.condition-remove {
  width: 2.25rem;
}

.empty-match {
  display: grid;
  min-height: 4rem;
  place-items: center;
  border-radius: 8px;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.8125rem;
}

@media (max-width: 860px) {
  .condition-row {
    grid-template-columns: minmax(0, 1fr) auto;
    padding-bottom: 0.75rem;
  }

  .condition-row + .condition-row {
    border-top: 1px solid var(--app-border-subtle);
    padding-top: 0.75rem;
  }

  .condition-row > *,
  .condition-row:not(.needs-name) > .operator-select,
  .condition-row:not(.uses-values) > .condition-negate {
    grid-column: 1 / -1;
  }

  .condition-row > .condition-negate,
  .condition-row:not(.uses-values) > .condition-negate {
    grid-column: 1;
    justify-self: start;
  }

  .condition-row > .condition-remove {
    grid-column: 2;
  }
}
</style>
