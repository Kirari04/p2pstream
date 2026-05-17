import {
  PublicPolicyMatchBooleanOperator,
  PublicPolicyMatchConditionOperator,
  PublicPolicyMatchField,
  type PublicPolicyMatchCondition,
  type PublicPolicyMatchGroup,
  type PublicPolicyMatchRule,
} from "@/gen/proto/p2pstream/v1/management_pb";

export type PolicyMatchMode = "builder" | "expression";

export type PolicyMatchConditionForm = {
  field: PublicPolicyMatchField;
  name: string;
  operator: PublicPolicyMatchConditionOperator;
  valuesText: string;
  negated: boolean;
};

export type PolicyMatchGroupForm = {
  operator: PublicPolicyMatchBooleanOperator;
  negated: boolean;
  conditions: PolicyMatchConditionForm[];
  groups: PolicyMatchGroupForm[];
};

export type PolicyMatchForm = {
  mode: PolicyMatchMode;
  expression: string;
  lastGeneratedExpression?: string;
  root: PolicyMatchGroupForm;
};

export type PublicPolicyMatchRulePayload = {
  celExpression: string;
  builder?: {
    root?: PublicPolicyMatchGroupPayload;
  };
};

export type PublicPolicyMatchGroupPayload = {
  operator: PublicPolicyMatchBooleanOperator;
  conditions: PublicPolicyMatchConditionPayload[];
  groups: PublicPolicyMatchGroupPayload[];
  negated: boolean;
};

export type PublicPolicyMatchConditionPayload = {
  field: PublicPolicyMatchField;
  name: string;
  operator: PublicPolicyMatchConditionOperator;
  values: string[];
  negated: boolean;
};

export function defaultPolicyMatchForm(): PolicyMatchForm {
  return {
    mode: "builder",
    expression: "",
    lastGeneratedExpression: "",
    root: emptyGroup(),
  };
}

export function emptyGroup(): PolicyMatchGroupForm {
  return {
    operator: PublicPolicyMatchBooleanOperator.ALL,
    negated: false,
    conditions: [],
    groups: [],
  };
}

export function emptyCondition(field = PublicPolicyMatchField.PATH): PolicyMatchConditionForm {
  return {
    field,
    name: "",
    operator: PublicPolicyMatchConditionOperator.PREFIX,
    valuesText: "",
    negated: false,
  };
}

export function policyMatchFormFromProto(rule?: PublicPolicyMatchRule): PolicyMatchForm {
  if (rule?.builder?.root) {
    const root = groupFormFromProto(rule.builder.root);
    const generatedExpression = builderToCEL(root);
    return {
      mode: "builder",
      expression: rule.celExpression || generatedExpression,
      lastGeneratedExpression: generatedExpression,
      root,
    };
  }
  if (rule?.celExpression?.trim()) {
    return {
      mode: "expression",
      expression: rule.celExpression,
      lastGeneratedExpression: "",
      root: emptyGroup(),
    };
  }
  return {
    mode: "builder",
    expression: "",
    lastGeneratedExpression: "",
    root: emptyGroup(),
  };
}

export function policyMatchRulePayload(form: PolicyMatchForm): PublicPolicyMatchRulePayload | undefined {
  if (form.mode === "expression") {
    const expression = form.expression.trim();
    return expression ? { celExpression: expression } : undefined;
  }
  const root = groupPayloadFromForm(form.root);
  if (!groupHasContent(root)) return undefined;
  const builder = { root };
  return {
    celExpression: builderToCEL(builderFormFromPayload(builder)),
    builder,
  };
}

export function builderToCEL(builder: PolicyMatchGroupForm | { root?: PolicyMatchGroupForm }): string {
  const root = "conditions" in builder ? builder : builder.root;
  if (!root) return "";
  return groupToCEL(root);
}

export function splitValues(text: string): string[] {
  return splitValuesForOperator(text, PublicPolicyMatchConditionOperator.IN);
}

export function splitValuesForOperator(text: string, operator: PublicPolicyMatchConditionOperator): string[] {
  const separator = operator === PublicPolicyMatchConditionOperator.IN ? /\r?\n|,/ : /\r?\n/;
  return text
    .split(separator)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function syncGeneratedExpressionForExpertMode(form: PolicyMatchForm): void {
  const generatedExpression = builderToCEL(form.root);
  const currentExpression = form.expression.trim();
  const lastGeneratedExpression = (form.lastGeneratedExpression ?? "").trim();
  if (!currentExpression || currentExpression === lastGeneratedExpression) {
    form.expression = generatedExpression;
  }
  form.lastGeneratedExpression = generatedExpression;
}

export function celString(value: string): string {
  return JSON.stringify(value);
}

export function conditionNeedsName(field: PublicPolicyMatchField): boolean {
  return field === PublicPolicyMatchField.HEADER ||
    field === PublicPolicyMatchField.COOKIE ||
    field === PublicPolicyMatchField.QUERY_PARAM;
}

export function conditionUsesValues(operator: PublicPolicyMatchConditionOperator): boolean {
  return operator !== PublicPolicyMatchConditionOperator.PRESENT;
}

export function normalizeConditionForField(condition: PolicyMatchConditionForm): void {
  if (!conditionNeedsName(condition.field)) condition.name = "";
  if (condition.operator === PublicPolicyMatchConditionOperator.PRESENT && !conditionNeedsName(condition.field)) {
    if (condition.field === PublicPolicyMatchField.REMOTE_IP) {
      condition.operator = PublicPolicyMatchConditionOperator.CIDR;
    } else if (condition.field === PublicPolicyMatchField.HOST) {
      condition.operator = PublicPolicyMatchConditionOperator.HOST_PATTERN;
    } else if (condition.field === PublicPolicyMatchField.PATH) {
      condition.operator = PublicPolicyMatchConditionOperator.PREFIX;
    } else {
      condition.operator = PublicPolicyMatchConditionOperator.EQUALS;
    }
  }
  if (condition.field === PublicPolicyMatchField.REMOTE_IP && condition.operator !== PublicPolicyMatchConditionOperator.CIDR) {
    condition.operator = PublicPolicyMatchConditionOperator.CIDR;
  }
  if (condition.field === PublicPolicyMatchField.HOST && condition.operator === PublicPolicyMatchConditionOperator.CIDR) {
    condition.operator = PublicPolicyMatchConditionOperator.HOST_PATTERN;
  }
  if (condition.field === PublicPolicyMatchField.PATH && condition.operator === PublicPolicyMatchConditionOperator.HOST_PATTERN) {
    condition.operator = PublicPolicyMatchConditionOperator.PREFIX;
  }
  if (condition.field !== PublicPolicyMatchField.HOST && condition.operator === PublicPolicyMatchConditionOperator.HOST_PATTERN) {
    condition.operator = PublicPolicyMatchConditionOperator.EQUALS;
  }
  if (condition.field !== PublicPolicyMatchField.REMOTE_IP && condition.operator === PublicPolicyMatchConditionOperator.CIDR) {
    condition.operator = PublicPolicyMatchConditionOperator.EQUALS;
  }
  if (!conditionUsesValues(condition.operator)) condition.valuesText = "";
}

export function policyMatchValidationReason(form: PolicyMatchForm): string {
  if (form.mode === "expression") return "";
  return groupValidationReason(form.root);
}

function groupValidationReason(group: PolicyMatchGroupForm): string {
  for (const condition of group.conditions) {
    const normalized = { ...condition };
    normalizeConditionForField(normalized);
    if (conditionNeedsName(normalized.field) && !normalized.name.trim()) return "Name is required for header, cookie, and query match conditions.";
    if (conditionUsesValues(normalized.operator) && splitValuesForOperator(normalized.valuesText, normalized.operator).length === 0) return "Comparison conditions require at least one value.";
  }
  for (const child of group.groups) {
    const reason = groupValidationReason(child);
    if (reason) return reason;
  }
  return "";
}

function groupFormFromProto(group: PublicPolicyMatchGroup): PolicyMatchGroupForm {
  return {
    operator: group.operator || PublicPolicyMatchBooleanOperator.ALL,
    negated: group.negated,
    conditions: group.conditions.map(conditionFormFromProto),
    groups: group.groups.map(groupFormFromProto),
  };
}

function conditionFormFromProto(condition: PublicPolicyMatchCondition): PolicyMatchConditionForm {
  return {
    field: condition.field || PublicPolicyMatchField.PATH,
    name: condition.name,
    operator: condition.operator || PublicPolicyMatchConditionOperator.EQUALS,
    valuesText: condition.values.join("\n"),
    negated: condition.negated,
  };
}

function groupPayloadFromForm(group: PolicyMatchGroupForm): PublicPolicyMatchGroupPayload {
  return {
    operator: group.operator || PublicPolicyMatchBooleanOperator.ALL,
    negated: group.negated,
    conditions: group.conditions
      .map(conditionPayloadFromForm)
      .filter((condition) => condition.operator === PublicPolicyMatchConditionOperator.PRESENT || condition.values.length > 0),
    groups: group.groups.map(groupPayloadFromForm).filter(groupHasContent),
  };
}

function conditionPayloadFromForm(condition: PolicyMatchConditionForm): PublicPolicyMatchConditionPayload {
  const normalized = { ...condition };
  normalizeConditionForField(normalized);
  const values = conditionUsesValues(normalized.operator)
    ? normalizeConditionValues(normalized, splitValuesForOperator(normalized.valuesText, normalized.operator))
    : [];
  return {
    field: normalized.field,
    name: conditionNeedsName(normalized.field) ? normalized.name.trim() : "",
    operator: normalized.operator,
    values,
    negated: normalized.negated,
  };
}

function builderFormFromPayload(builder: { root?: PublicPolicyMatchGroupPayload }): { root?: PolicyMatchGroupForm } {
  return {
    root: builder.root ? groupFormFromPayload(builder.root) : undefined,
  };
}

function groupFormFromPayload(group: PublicPolicyMatchGroupPayload): PolicyMatchGroupForm {
  return {
    operator: group.operator,
    negated: group.negated,
    conditions: group.conditions.map((condition) => ({
      field: condition.field,
      name: condition.name,
      operator: condition.operator,
      valuesText: condition.values.join("\n"),
      negated: condition.negated,
    })),
    groups: group.groups.map(groupFormFromPayload),
  };
}

function groupHasContent(group: PublicPolicyMatchGroupPayload): boolean {
  return group.conditions.length > 0 || group.groups.length > 0;
}

export function groupHasFormContent(group: PolicyMatchGroupForm): boolean {
  return group.conditions.some(conditionHasFormContent) || group.groups.some(groupHasFormContent);
}

function conditionHasFormContent(condition: PolicyMatchConditionForm): boolean {
  const normalized = { ...condition };
  normalizeConditionForField(normalized);
  return !conditionUsesValues(normalized.operator) || splitValuesForOperator(normalized.valuesText, normalized.operator).length > 0;
}

function groupToCEL(group: PolicyMatchGroupForm): string {
  const parts = [
    ...group.conditions.filter(conditionHasFormContent).map(conditionToCEL),
    ...group.groups.filter(groupHasFormContent).map(groupToCEL),
  ].filter(Boolean);
  const joiner = group.operator === PublicPolicyMatchBooleanOperator.ANY ? " || " : " && ";
  let expression = parts.length ? `(${parts.join(joiner)})` : "true";
  if (group.negated) expression = `!(${expression})`;
  return expression;
}

function conditionToCEL(condition: PolicyMatchConditionForm): string {
  const normalized = { ...condition };
  normalizeConditionForField(normalized);
  const values = normalizeConditionValues(normalized, splitValuesForOperator(normalized.valuesText, normalized.operator));
  let expression = "";
  switch (normalized.field) {
    case PublicPolicyMatchField.HEADER:
      expression = repeatedMapCondition("headers", normalized.name.trim().toLowerCase(), normalized.operator, values);
      break;
    case PublicPolicyMatchField.QUERY_PARAM:
      expression = repeatedMapCondition("query", normalized.name.trim(), normalized.operator, values);
      break;
    case PublicPolicyMatchField.COOKIE:
      expression = stringMapCondition("cookies", normalized.name.trim(), normalized.operator, values);
      break;
    case PublicPolicyMatchField.HOST:
      expression = normalized.operator === PublicPolicyMatchConditionOperator.HOST_PATTERN
        ? anyValue(values, (value) => `host_match(host, ${celString(value)})`)
        : scalarCondition("host", normalized.operator, values);
      break;
    case PublicPolicyMatchField.PATH:
      expression = normalized.operator === PublicPolicyMatchConditionOperator.PREFIX
        ? anyValue(values, (value) => `path_prefix(path, ${celString(value)})`)
        : scalarCondition("path", normalized.operator, values);
      break;
    case PublicPolicyMatchField.REMOTE_IP:
      expression = normalized.operator === PublicPolicyMatchConditionOperator.CIDR
        ? anyValue(values, (value) => `cidr(remote_ip, ${celString(value)})`)
        : scalarCondition("remote_ip", normalized.operator, values);
      break;
    case PublicPolicyMatchField.METHOD:
      expression = scalarCondition("method", normalized.operator, values);
      break;
    case PublicPolicyMatchField.PROTOCOL:
      expression = scalarCondition("protocol", normalized.operator, values);
      break;
    default:
      expression = "false";
  }
  if (normalized.negated) expression = `!(${expression})`;
  return `(${expression})`;
}

function normalizeConditionValues(condition: PolicyMatchConditionForm, values: string[]): string[] {
  switch (condition.field) {
    case PublicPolicyMatchField.METHOD:
      return values.map((value) => value.trim().toUpperCase());
    case PublicPolicyMatchField.PROTOCOL:
      return values.map((value) => value.trim().toLowerCase());
    case PublicPolicyMatchField.HOST:
      return values.map((value) => {
        const host = value.trim().toLowerCase();
        return condition.operator === PublicPolicyMatchConditionOperator.HOST_PATTERN ? host.replace(/\.$/, "") : host;
      });
    case PublicPolicyMatchField.PATH:
    case PublicPolicyMatchField.REMOTE_IP:
      return values.map((value) => value.trim());
    default:
      return values;
  }
}

function scalarCondition(source: string, operator: PublicPolicyMatchConditionOperator, values: string[]): string {
  if (operator === PublicPolicyMatchConditionOperator.IN) return `${source} in ${stringList(values)}`;
  return anyValue(values, (value) => stringCompare(source, operator, value));
}

function repeatedMapCondition(mapName: string, name: string, operator: PublicPolicyMatchConditionOperator, values: string[]): string {
  const key = celString(name);
  const present = `${key} in ${mapName}`;
  if (operator === PublicPolicyMatchConditionOperator.PRESENT) return present;
  const comparison = operator === PublicPolicyMatchConditionOperator.IN
    ? `v in ${stringList(values)}`
    : anyValue(values, (value) => stringCompare("v", operator, value));
  return `(${present} && ${mapName}[${key}].exists(v, ${comparison}))`;
}

function stringMapCondition(mapName: string, name: string, operator: PublicPolicyMatchConditionOperator, values: string[]): string {
  const key = celString(name);
  const present = `${key} in ${mapName}`;
  if (operator === PublicPolicyMatchConditionOperator.PRESENT) return present;
  const source = `${mapName}[${key}]`;
  const comparison = operator === PublicPolicyMatchConditionOperator.IN
    ? `${source} in ${stringList(values)}`
    : anyValue(values, (value) => stringCompare(source, operator, value));
  return `(${present} && (${comparison}))`;
}

function stringCompare(source: string, operator: PublicPolicyMatchConditionOperator, value: string): string {
  const quoted = celString(value);
  switch (operator) {
    case PublicPolicyMatchConditionOperator.PREFIX:
      return `${source}.startsWith(${quoted})`;
    case PublicPolicyMatchConditionOperator.SUFFIX:
      return `${source}.endsWith(${quoted})`;
    case PublicPolicyMatchConditionOperator.CONTAINS:
      return `${source}.contains(${quoted})`;
    case PublicPolicyMatchConditionOperator.MATCHES:
      return `${source}.matches(${quoted})`;
    default:
      return `${source} == ${quoted}`;
  }
}

function anyValue(values: string[], fn: (value: string) => string): string {
  if (!values.length) return "false";
  return `(${values.map(fn).join(" || ")})`;
}

function stringList(values: string[]): string {
  return `[${values.map(celString).join(", ")}]`;
}
