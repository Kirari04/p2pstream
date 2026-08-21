import {
  PublicPolicyMatchBooleanOperator,
  PublicPolicyMatchConditionOperator,
  PublicPolicyMatchField,
  PublicWafActivationMode,
  PublicWafGeoRestrictionMode,
  PublicWafGeoUnknownBehavior,
  PublicWafRuleAction,
  type GetPublicProxyConfigResponse,
  type PublicCacheRule,
  type PublicPolicyMatchCondition,
  type PublicPolicyMatchGroup,
  type PublicPolicyMatchRule,
  type PublicRateLimitRule,
  type PublicTrafficShaperRule,
  type PublicWafCaptchaProvider,
  type PublicWafRule,
} from "@/gen/proto/p2pstream/v1/management_pb";

export type TrafficPolicyKind = "waf" | "rate-limit" | "traffic-shaper" | "cache";
export type TrafficPolicyMatchState = "match" | "miss" | "unknown";
export type TrafficPolicyValueMap = Record<string, string | readonly string[] | null | undefined>;
export type TrafficPolicyCookieMap = Record<string, string | null | undefined>;
export type TrafficPolicyId = bigint | number | string;

export type TrafficPolicySyntheticRequest = {
  method: string;
  protocol: string;
  host: string;
  path: string;
  remoteIp: string;
  countryCode?: string;
  hasRequestBody?: boolean;
  headers?: TrafficPolicyValueMap;
  cookies?: TrafficPolicyCookieMap;
  query?: TrafficPolicyValueMap;
  routeId?: TrafficPolicyId | null;
  targetId?: TrafficPolicyId | null;
};

export type TrafficPolicyPreviewForm = {
  method: string;
  protocol: string;
  host: string;
  path: string;
  remoteIp: string;
  countryCode: string;
  hasRequestBody: boolean;
  headersText: string;
  cookiesText: string;
  queryText: string;
  routeId: string;
  targetId: string;
};

export type TrafficPolicyMatchResult = {
  state: TrafficPolicyMatchState;
  reason?: string;
  canFallThrough?: boolean;
};

export type TrafficPolicyStageCandidate<T> = {
  rule: T;
  result: TrafficPolicyMatchResult;
};

export type TrafficPolicyStagePreview = {
  waf: TrafficPolicyStageCandidate<PublicWafRule> | null;
  rateLimits: TrafficPolicyStageCandidate<PublicRateLimitRule>[];
  trafficShaper: TrafficPolicyStageCandidate<PublicTrafficShaperRule> | null;
  cache: TrafficPolicyStageCandidate<PublicCacheRule> | null;
};

export type TrafficPolicyPlaygroundStageItem = {
  id: bigint;
  name: string;
  priority: bigint;
  state: TrafficPolicyMatchState;
  reason: string;
  selected: boolean;
  skipped: boolean;
};

export type TrafficPolicyPlaygroundStage = {
  key: TrafficPolicyKind;
  label: string;
  mode: "first" | "all";
  items: TrafficPolicyPlaygroundStageItem[];
};

export type TrafficPolicyWorkbenchConfig = Partial<Pick<
  GetPublicProxyConfigResponse,
  "rateLimitRules" | "trafficShaperRules" | "wafRules" | "wafCaptchaProviders" | "cacheSettings" | "cacheRules"
>>;

export type TrafficPolicyAttentionWarningCode =
  | "duplicate-priority"
  | "disabled-rule"
  | "any-request-rule"
  | "captcha-provider-missing"
  | "captcha-provider-disabled"
  | "captcha-provider-secret-missing"
  | "cache-settings-disabled"
  | "cache-allows-cookie-requests";

export type TrafficPolicyAttentionWarning = {
  code: TrafficPolicyAttentionWarningCode;
  message: string;
  policyKind?: TrafficPolicyKind;
  ruleId?: bigint;
  ruleIds?: bigint[];
  providerId?: bigint;
  priority?: bigint;
};

type NormalizedTrafficPolicyRequest = {
  method: string;
  protocol: string;
  host: string;
  path: string;
  remoteIp: string;
  countryCode: string;
  hasRequestBody: boolean;
  headers: Map<string, string[]>;
  cookies: Map<string, string>;
  query: Map<string, string[]>;
  routeId: bigint | null;
  targetId: bigint | null;
};

type ConstantMatchState = "always-match" | "always-miss" | "variable";

const MATCH_RESULT: TrafficPolicyMatchResult = { state: "match" };
const MISS_RESULT: TrafficPolicyMatchResult = { state: "miss" };

export function runtimeOrderedWafRules(rules: readonly PublicWafRule[]): PublicWafRule[] {
  return runtimeOrderedRules(rules);
}

export function runtimeOrderedRateLimitRules(rules: readonly PublicRateLimitRule[]): PublicRateLimitRule[] {
  return runtimeOrderedRules(rules);
}

export function runtimeOrderedTrafficShaperRules(rules: readonly PublicTrafficShaperRule[]): PublicTrafficShaperRule[] {
  return runtimeOrderedRules(rules);
}

export function runtimeOrderedCacheRules(rules: readonly PublicCacheRule[]): PublicCacheRule[] {
  return runtimeOrderedRules(rules);
}

export function evaluateTrafficPolicyMatch(
  matchRule: PublicPolicyMatchRule | undefined,
  request: TrafficPolicySyntheticRequest,
): TrafficPolicyMatchResult {
  return evaluateTrafficPolicyMatchForRequest(matchRule, normalizeTrafficPolicyRequest(request));
}

export function trafficPolicyRuleMatchesAnyRequest(matchRule: PublicPolicyMatchRule | undefined): boolean {
  if (!matchRule) return true;
  if (matchRule.builder?.root) return constantGroupState(matchRule.builder.root) === "always-match";
  const expression = matchRule.celExpression.trim();
  return expression === "" || expression === "true";
}

export function defaultTrafficPolicyPreviewForm(): TrafficPolicyPreviewForm {
  return {
    method: "GET",
    protocol: "https",
    host: "app.example.com",
    path: "/",
    remoteIp: "198.51.100.10",
    countryCode: "CH",
    hasRequestBody: false,
    headersText: "X-Plan: pro",
    cookiesText: "",
    queryText: "",
    routeId: "",
    targetId: "",
  };
}

export function trafficPolicyPreviewFormToRequest(form: TrafficPolicyPreviewForm): TrafficPolicySyntheticRequest {
  return {
    method: form.method,
    protocol: form.protocol,
    host: form.host,
    path: form.path,
    remoteIp: form.remoteIp,
    countryCode: form.countryCode,
    hasRequestBody: form.hasRequestBody,
    headers: parseRepeatedMap(form.headersText, true),
    cookies: parseCookieMap(form.cookiesText),
    query: parseRepeatedMap(form.queryText, false),
    routeId: form.routeId || null,
    targetId: form.targetId || null,
  };
}

export function previewTrafficPolicyStages(
  config: TrafficPolicyWorkbenchConfig,
  request: TrafficPolicySyntheticRequest,
): TrafficPolicyStagePreview {
  const normalized = normalizeTrafficPolicyRequest(request);
  return {
    waf: firstMatchingCandidate(runtimeOrderedWafRules(config.wafRules ?? []), (rule) => {
      return evaluateWafRule(rule, normalized);
    }),
    rateLimits: allMatchingCandidates(runtimeOrderedRateLimitRules(config.rateLimitRules ?? []), (rule) => {
      return evaluateTrafficPolicyMatchForRequest(rule.matchRule, normalized);
    }),
    trafficShaper: firstMatchingCandidate(runtimeOrderedTrafficShaperRules(config.trafficShaperRules ?? []), (rule) => {
      return evaluateTrafficPolicyMatchForRequest(rule.matchRule, normalized);
    }),
    cache: firstMatchingCandidate(runtimeOrderedCacheRules(config.cacheRules ?? []), (rule) => {
      return evaluateCacheRule(rule, normalized, config.cacheSettings?.enabled !== false);
    }),
  };
}

export function buildTrafficPolicyPlaygroundStages(
  config: TrafficPolicyWorkbenchConfig,
  request: TrafficPolicySyntheticRequest,
): TrafficPolicyPlaygroundStage[] {
  const preview = previewTrafficPolicyStages(config, request);
  return [
    stageFromFirstCandidate("waf", "WAF", runtimeOrderedWafRules(config.wafRules ?? []), preview.waf),
    stageFromRateLimitCandidates(runtimeOrderedRateLimitRules(config.rateLimitRules ?? []), preview.rateLimits),
    stageFromFirstCandidate("traffic-shaper", "Traffic shaper", runtimeOrderedTrafficShaperRules(config.trafficShaperRules ?? []), preview.trafficShaper),
    stageFromFirstCandidate("cache", "Cache", runtimeOrderedCacheRules(config.cacheRules ?? []), preview.cache),
  ];
}

export function trafficPolicyAttentionWarnings(config: TrafficPolicyWorkbenchConfig): TrafficPolicyAttentionWarning[] {
  const warnings: TrafficPolicyAttentionWarning[] = [];
  const rateLimitRules = config.rateLimitRules ?? [];
  const trafficShaperRules = config.trafficShaperRules ?? [];
  const wafRules = config.wafRules ?? [];
  const cacheRules = config.cacheRules ?? [];

  warnings.push(...duplicatePriorityWarnings("waf", wafRules));
  warnings.push(...duplicatePriorityWarnings("rate-limit", rateLimitRules));
  warnings.push(...duplicatePriorityWarnings("traffic-shaper", trafficShaperRules));
  warnings.push(...duplicatePriorityWarnings("cache", cacheRules));

  warnings.push(...ruleShapeWarnings("waf", wafRules));
  warnings.push(...ruleShapeWarnings("rate-limit", rateLimitRules));
  warnings.push(...ruleShapeWarnings("traffic-shaper", trafficShaperRules));
  warnings.push(...ruleShapeWarnings("cache", cacheRules));

  warnings.push(...captchaProviderWarnings(wafRules, config.wafCaptchaProviders ?? []));

  if ((config.cacheSettings?.enabled === false) && cacheRules.some((rule) => rule.enabled)) {
    warnings.push({
      code: "cache-settings-disabled",
      policyKind: "cache",
      message: "Cache settings are disabled while one or more cache rules are enabled.",
    });
  }

  for (const rule of cacheRules) {
    if (!rule.enabled || !rule.allowCookieRequests) continue;
    warnings.push({
      code: "cache-allows-cookie-requests",
      policyKind: "cache",
      ruleId: rule.id,
      message: `Cache rule ${ruleDisplayName(rule)} preserves the legacy Cookie opt-in flag; Cookie requests still bypass shared cache.`,
    });
  }

  return warnings;
}

function runtimeOrderedRules<T extends { priority: bigint; id: bigint }>(rules: readonly T[]): T[] {
  return [...rules].sort((a, b) => {
    if (a.priority !== b.priority) return a.priority < b.priority ? -1 : 1;
    if (a.id === b.id) return 0;
    return a.id < b.id ? -1 : 1;
  });
}

function evaluateTrafficPolicyMatchForRequest(
  matchRule: PublicPolicyMatchRule | undefined,
  request: NormalizedTrafficPolicyRequest,
): TrafficPolicyMatchResult {
  if (!matchRule) return MATCH_RESULT;
  if (matchRule.builder?.root) return evaluateGroup(matchRule.builder.root, request);
  if (matchRule.celExpression.trim()) return unknownResult("CEL-only rules cannot be evaluated in the browser.");
  return MATCH_RESULT;
}

function evaluateWafRule(rule: PublicWafRule, request: NormalizedTrafficPolicyRequest): TrafficPolicyMatchResult {
  const match = andResults([
    evaluateTrafficPolicyMatchForRequest(rule.matchRule, request),
    evaluateWafGeoRestriction(rule, request),
  ]);
  if (match.state !== "match") return match;
  if (rule.activationMode === PublicWafActivationMode.AUTOMATIC) {
    return unknownResult("Automatic WAF activation depends on live trigger state.");
  }
  return match;
}

function evaluateWafGeoRestriction(rule: PublicWafRule, request: NormalizedTrafficPolicyRequest): TrafficPolicyMatchResult {
  const restriction = rule.geoRestriction;
  if (!restriction || restriction.mode === PublicWafGeoRestrictionMode.DISABLED || restriction.mode === PublicWafGeoRestrictionMode.UNSPECIFIED) {
    return MATCH_RESULT;
  }
  if (!request.countryCode) {
    return restriction.unknownBehavior === PublicWafGeoUnknownBehavior.BYPASS_RULE
      ? missResult("Unknown country bypasses this WAF rule.")
      : MATCH_RESULT;
  }
  const selected = restriction.countryCodes.some((code) => code.trim().toUpperCase() === request.countryCode);
  if (restriction.mode === PublicWafGeoRestrictionMode.OUTSIDE_SELECTED_COUNTRIES) {
    return selected ? missResult("Synthetic country is inside the allow-only selection.") : MATCH_RESULT;
  }
  return selected ? MATCH_RESULT : missResult("Synthetic country is not selected by this WAF rule.");
}

function evaluateCacheRule(rule: PublicCacheRule, request: NormalizedTrafficPolicyRequest, cacheSettingsEnabled: boolean): TrafficPolicyMatchResult {
  const bypass = cacheRequestBypassResult(request, cacheSettingsEnabled);
  if (bypass) return bypass;
  return andResults([
    evaluateTrafficPolicyMatchForRequest(rule.matchRule, request),
    evaluateIdFilter(rule.routeIds, request.routeId, "Route filter cannot be evaluated without a synthetic route id."),
    evaluateIdFilter(rule.targetIds, request.targetId, "Target filter cannot be evaluated without a synthetic target id."),
  ]);
}

function evaluateGroup(group: PublicPolicyMatchGroup, request: NormalizedTrafficPolicyRequest): TrafficPolicyMatchResult {
  const parts = [
    ...group.conditions.map((condition) => evaluateCondition(condition, request)),
    ...group.groups.map((child) => evaluateGroup(child, request)),
  ];
  const joined = group.operator === PublicPolicyMatchBooleanOperator.ANY
    ? orResults(parts)
    : andResults(parts);
  return group.negated ? negateResult(joined) : joined;
}

function evaluateCondition(
  condition: PublicPolicyMatchCondition,
  request: NormalizedTrafficPolicyRequest,
): TrafficPolicyMatchResult {
  switch (condition.field) {
    case PublicPolicyMatchField.HEADER:
      return evaluateMultiValueCondition(
        request.headers,
        condition.name.trim().toLowerCase(),
        condition.operator,
        condition.values,
        condition.negated,
        condition,
      );
    case PublicPolicyMatchField.QUERY_PARAM:
      return evaluateMultiValueCondition(
        request.query,
        condition.name.trim(),
        condition.operator,
        condition.values,
        condition.negated,
        condition,
      );
    case PublicPolicyMatchField.COOKIE:
      return evaluateSingleValueCondition(
        request.cookies,
        condition.name.trim(),
        condition.operator,
        condition.values,
        condition.negated,
        condition,
      );
    case PublicPolicyMatchField.METHOD:
      return evaluateScalarCondition(request.method, condition, condition.values.map(normalizeMethodValue));
    case PublicPolicyMatchField.PROTOCOL:
      return evaluateScalarCondition(request.protocol, condition, condition.values.map(normalizeProtocolValue));
    case PublicPolicyMatchField.HOST:
      return evaluateScalarCondition(request.host, condition, condition.values.map((value) => normalizeHostValue(value, condition.operator)));
    case PublicPolicyMatchField.PATH:
      return evaluateScalarCondition(request.path, condition, condition.values.map((value) => value.trim()));
    case PublicPolicyMatchField.REMOTE_IP:
      return evaluateScalarCondition(request.remoteIp, condition, condition.values.map((value) => value.trim()));
    default:
      return unknownResult("Policy match field is not supported by the workbench.");
  }
}

function evaluateMultiValueCondition(
  valuesByName: Map<string, string[]>,
  name: string,
  operator: PublicPolicyMatchConditionOperator,
  expectedValues: readonly string[],
  negated: boolean,
  condition: PublicPolicyMatchCondition,
): TrafficPolicyMatchResult {
  if (!name) return unknownResult("Header, cookie, and query conditions require a name.");
  if (operator === PublicPolicyMatchConditionOperator.PRESENT) {
    return maybeNegate(valuesByName.has(name) ? MATCH_RESULT : MISS_RESULT, negated);
  }
  if (!operatorAllowedForStringField(operator)) return unknownResult("Policy match operator is not valid for this field.");
  const actualValues = valuesByName.get(name) ?? [];
  const result = actualValues.length
    ? evaluateAnyActualValue(actualValues, operator, normalizeExpectedValues(condition, expectedValues), condition.field)
    : MISS_RESULT;
  return maybeNegate(result, negated);
}

function evaluateSingleValueCondition(
  valuesByName: Map<string, string>,
  name: string,
  operator: PublicPolicyMatchConditionOperator,
  expectedValues: readonly string[],
  negated: boolean,
  condition: PublicPolicyMatchCondition,
): TrafficPolicyMatchResult {
  if (!name) return unknownResult("Header, cookie, and query conditions require a name.");
  if (operator === PublicPolicyMatchConditionOperator.PRESENT) {
    return maybeNegate(valuesByName.has(name) ? MATCH_RESULT : MISS_RESULT, negated);
  }
  if (!operatorAllowedForStringField(operator)) return unknownResult("Policy match operator is not valid for this field.");
  const value = valuesByName.get(name);
  const result = value === undefined
    ? MISS_RESULT
    : evaluateAnyActualValue([value], operator, normalizeExpectedValues(condition, expectedValues), condition.field);
  return maybeNegate(result, negated);
}

function evaluateScalarCondition(
  actualValue: string,
  condition: PublicPolicyMatchCondition,
  normalizedExpectedValues: readonly string[],
): TrafficPolicyMatchResult {
  if (condition.operator === PublicPolicyMatchConditionOperator.PRESENT) {
    return unknownResult("The present operator is only supported for headers, cookies, and query params.");
  }
  if (!operatorAllowedForScalarField(condition.operator, condition.field)) {
    return unknownResult("Policy match operator is not valid for this field.");
  }
  const result = evaluateAnyActualValue([actualValue], condition.operator, normalizedExpectedValues, condition.field);
  return maybeNegate(result, condition.negated);
}

function normalizeExpectedValues(
  condition: PublicPolicyMatchCondition,
  expectedValues: readonly string[],
): string[] {
  switch (condition.field) {
    case PublicPolicyMatchField.METHOD:
      return expectedValues.map(normalizeMethodValue);
    case PublicPolicyMatchField.PROTOCOL:
      return expectedValues.map(normalizeProtocolValue);
    case PublicPolicyMatchField.HOST:
      return expectedValues.map((value) => normalizeHostValue(value, condition.operator));
    case PublicPolicyMatchField.PATH:
    case PublicPolicyMatchField.REMOTE_IP:
      return expectedValues.map((value) => value.trim());
    default:
      return [...expectedValues];
  }
}

function evaluateAnyActualValue(
  actualValues: readonly string[],
  operator: PublicPolicyMatchConditionOperator,
  expectedValues: readonly string[],
  field: PublicPolicyMatchField,
): TrafficPolicyMatchResult {
  if (!expectedValues.length) return unknownResult("Comparison conditions require at least one value.");
  if (operator === PublicPolicyMatchConditionOperator.IN) {
    const expected = new Set(expectedValues);
    return actualValues.some((value) => expected.has(value)) ? MATCH_RESULT : MISS_RESULT;
  }
  const results: TrafficPolicyMatchResult[] = [];
  for (const actualValue of actualValues) {
    for (const expectedValue of expectedValues) {
      results.push(evaluateSingleComparison(actualValue, operator, expectedValue, field));
    }
  }
  return orResults(results);
}

function evaluateSingleComparison(
  actualValue: string,
  operator: PublicPolicyMatchConditionOperator,
  expectedValue: string,
  field: PublicPolicyMatchField,
): TrafficPolicyMatchResult {
  switch (operator) {
    case PublicPolicyMatchConditionOperator.EQUALS:
      return actualValue === expectedValue ? MATCH_RESULT : MISS_RESULT;
    case PublicPolicyMatchConditionOperator.PREFIX:
      return field === PublicPolicyMatchField.PATH
        ? (pathPrefixMatches(actualValue, expectedValue) ? MATCH_RESULT : MISS_RESULT)
        : (actualValue.startsWith(expectedValue) ? MATCH_RESULT : MISS_RESULT);
    case PublicPolicyMatchConditionOperator.SUFFIX:
      return actualValue.endsWith(expectedValue) ? MATCH_RESULT : MISS_RESULT;
    case PublicPolicyMatchConditionOperator.CONTAINS:
      return actualValue.includes(expectedValue) ? MATCH_RESULT : MISS_RESULT;
    case PublicPolicyMatchConditionOperator.MATCHES:
      return evaluateRegex(actualValue, expectedValue);
    case PublicPolicyMatchConditionOperator.CIDR:
      return evaluateCidr(actualValue, expectedValue);
    case PublicPolicyMatchConditionOperator.HOST_PATTERN:
      return expectedValue ? (hostMatchesPattern(actualValue, expectedValue) ? MATCH_RESULT : MISS_RESULT) : unknownResult("Host pattern is empty.");
    default:
      return unknownResult("Policy match operator is not supported by the workbench.");
  }
}

function operatorAllowedForScalarField(
  operator: PublicPolicyMatchConditionOperator,
  field: PublicPolicyMatchField,
): boolean {
  if (operator === PublicPolicyMatchConditionOperator.CIDR) return field === PublicPolicyMatchField.REMOTE_IP;
  if (operator === PublicPolicyMatchConditionOperator.HOST_PATTERN) return field === PublicPolicyMatchField.HOST;
  return operatorAllowedForStringField(operator);
}

function operatorAllowedForStringField(operator: PublicPolicyMatchConditionOperator): boolean {
  switch (operator) {
    case PublicPolicyMatchConditionOperator.EQUALS:
    case PublicPolicyMatchConditionOperator.PREFIX:
    case PublicPolicyMatchConditionOperator.SUFFIX:
    case PublicPolicyMatchConditionOperator.CONTAINS:
    case PublicPolicyMatchConditionOperator.MATCHES:
    case PublicPolicyMatchConditionOperator.IN:
      return true;
    default:
      return false;
  }
}

function evaluateRegex(actualValue: string, pattern: string): TrafficPolicyMatchResult {
  void actualValue;
  void pattern;
  return unknownResult("Regex policy matches are not evaluated in the browser.");
}

function evaluateCidr(actualIp: string, rawCidr: string): TrafficPolicyMatchResult {
  const parsedIp = parseIpAddress(actualIp);
  const parsedCidr = parseCidr(rawCidr);
  if (!parsedIp || !parsedCidr) return MISS_RESULT;
  if (parsedIp.length !== parsedCidr.address.length) return MISS_RESULT;
  return ipBytesInPrefix(parsedIp, parsedCidr.address, parsedCidr.prefixBits) ? MATCH_RESULT : MISS_RESULT;
}

function evaluateIdFilter(ids: readonly bigint[], actualId: bigint | null, unknownReason: string): TrafficPolicyMatchResult {
  if (!ids.length) return MATCH_RESULT;
  if (actualId === null) return unknownResult(unknownReason);
  return ids.some((id) => id === actualId) ? MATCH_RESULT : MISS_RESULT;
}

function firstMatchingCandidate<T extends { enabled: boolean }>(
  rules: readonly T[],
  evaluate: (rule: T) => TrafficPolicyMatchResult,
): TrafficPolicyStageCandidate<T> | null {
  for (const rule of rules) {
    if (!rule.enabled) continue;
    const result = evaluate(rule);
    if (result.state !== "miss") return { rule, result };
  }
  return null;
}

function allMatchingCandidates<T extends { enabled: boolean }>(
  rules: readonly T[],
  evaluate: (rule: T) => TrafficPolicyMatchResult,
): TrafficPolicyStageCandidate<T>[] {
  const candidates: TrafficPolicyStageCandidate<T>[] = [];
  for (const rule of rules) {
    if (!rule.enabled) continue;
    const result = evaluate(rule);
    if (result.state !== "miss") candidates.push({ rule, result });
  }
  return candidates;
}

function stageFromFirstCandidate<T extends { id: bigint; name: string; priority: bigint; enabled: boolean }>(
  key: Exclude<TrafficPolicyKind, "rate-limit">,
  label: string,
  rules: readonly T[],
  candidate: TrafficPolicyStageCandidate<T> | null,
): TrafficPolicyPlaygroundStage {
  const selectedId = candidate?.rule.id;
  const selectedResult = candidate?.result ?? null;
  const selectedResultCanFallThrough = selectedResult?.state === "unknown" && selectedResult.canFallThrough !== false;
  const selectedResultBlocksLater = selectedResult?.state === "match";
  let passedSelected = false;
  return {
    key,
    label,
    mode: "first",
    items: rules.map((rule) => {
      const selected = selectedId !== undefined && rule.id === selectedId;
      const selectedRuleResult = selected ? selectedResult : null;
      const afterSelected = Boolean(rule.enabled && passedSelected && !selected);
      if (selected) passedSelected = true;

      let state: TrafficPolicyMatchState = selectedRuleResult?.state ?? "miss";
      let reason = selectedRuleResult ? (selectedRuleResult.reason || "Selected by preview") : "No match in preview";
      let skipped = false;

      if (!rule.enabled) {
        reason = "Rule is disabled";
      } else if (afterSelected && selectedResultBlocksLater) {
        skipped = true;
        reason = "Skipped after earlier confirmed match";
      } else if (afterSelected && selectedResultCanFallThrough) {
        state = "unknown";
        reason = "May still apply; earlier preview result is unknown.";
      } else if (afterSelected) {
        state = "unknown";
        reason = "Not evaluated because stage eligibility is unknown.";
      }

      return {
        id: rule.id,
        name: rule.name,
        priority: rule.priority,
        state,
        reason,
        selected,
        skipped,
      };
    }),
  };
}

function stageFromRateLimitCandidates(
  rules: readonly PublicRateLimitRule[],
  candidates: readonly TrafficPolicyStageCandidate<PublicRateLimitRule>[],
): TrafficPolicyPlaygroundStage {
  const candidateById = new Map(candidates.map((candidate) => [candidate.rule.id.toString(), candidate]));
  return {
    key: "rate-limit",
    label: "Rate limits",
    mode: "all",
    items: rules.map((rule) => {
      const candidate = candidateById.get(rule.id.toString());
      return {
        id: rule.id,
        name: rule.name,
        priority: rule.priority,
        state: candidate?.result.state ?? "miss",
        reason: candidate?.result.reason || (rule.enabled ? "No match in preview" : "Rule is disabled"),
        selected: Boolean(candidate),
        skipped: false,
      };
    }),
  };
}

function andResults(results: readonly TrafficPolicyMatchResult[]): TrafficPolicyMatchResult {
  if (!results.length) return MATCH_RESULT;
  const miss = results.find((result) => result.state === "miss");
  if (miss) return MISS_RESULT;
  const unknown = results.find((result) => result.state === "unknown");
  return unknown ?? MATCH_RESULT;
}

function orResults(results: readonly TrafficPolicyMatchResult[]): TrafficPolicyMatchResult {
  if (!results.length) return MATCH_RESULT;
  const match = results.find((result) => result.state === "match");
  if (match) return MATCH_RESULT;
  const unknown = results.find((result) => result.state === "unknown");
  return unknown ?? MISS_RESULT;
}

function maybeNegate(result: TrafficPolicyMatchResult, negated: boolean): TrafficPolicyMatchResult {
  return negated ? negateResult(result) : result;
}

function negateResult(result: TrafficPolicyMatchResult): TrafficPolicyMatchResult {
  if (result.state === "match") return MISS_RESULT;
  if (result.state === "miss") return MATCH_RESULT;
  return result;
}

function unknownResult(reason: string, canFallThrough = true): TrafficPolicyMatchResult {
  return canFallThrough ? { state: "unknown", reason } : { state: "unknown", reason, canFallThrough: false };
}

function cacheRequestBypassResult(request: NormalizedTrafficPolicyRequest, cacheSettingsEnabled: boolean): TrafficPolicyMatchResult | null {
  if (!cacheSettingsEnabled) return MISS_RESULT;
  if (request.method !== "GET" && request.method !== "HEAD") return missResult("Cache bypass: only GET and HEAD requests are cacheable.");
  if (hasHeader(request.headers, "authorization")) return missResult("Cache bypass: Authorization requests are never cached.");
  if (request.cookies.size || hasHeader(request.headers, "cookie")) return missResult("Cache bypass: Cookie requests are never cached.");
  if (hasHeader(request.headers, "range")) return missResult("Cache bypass: Range requests are never cached.");
  if (headerEquals(request.headers, "connection", "upgrade") || hasHeader(request.headers, "upgrade")) {
    return missResult("Cache bypass: upgraded requests are never cached.");
  }
  if (request.hasRequestBody) return missResult("Cache bypass: requests with a body are never cached.");
  if (hasEncodedPathSeparator(request.path)) {
    return unknownResult("Encoded path separator cache behavior depends on route path security settings.", false);
  }
  return null;
}

function missResult(reason: string): TrafficPolicyMatchResult {
  return { state: "miss", reason };
}

function hasHeader(headers: Map<string, string[]>, name: string): boolean {
  return (headers.get(name.toLowerCase()) ?? []).some((value) => value.trim() !== "");
}

function headerEquals(headers: Map<string, string[]>, name: string, expected: string): boolean {
  return (headers.get(name.toLowerCase()) ?? []).some((value) => value.trim().toLowerCase() === expected);
}

function hasEncodedPathSeparator(path: string): boolean {
  const lowerPath = path.toLowerCase();
  return lowerPath.includes("%2f") || lowerPath.includes("%5c");
}

function normalizeTrafficPolicyRequest(request: TrafficPolicySyntheticRequest): NormalizedTrafficPolicyRequest {
  return {
    method: normalizeMethodValue(request.method),
    protocol: normalizeProtocolValue(request.protocol),
    host: normalizeRequestHost(request.host),
    path: request.path.trim() || "/",
    remoteIp: request.remoteIp.trim(),
    countryCode: (request.countryCode ?? "").trim().toUpperCase() === "__UNKNOWN__" ? "" : (request.countryCode ?? "").trim().toUpperCase(),
    hasRequestBody: request.hasRequestBody === true,
    headers: normalizeMultiValueMap(request.headers, true),
    cookies: normalizeCookieMap(request.cookies),
    query: normalizeMultiValueMap(request.query, false),
    routeId: normalizePolicyId(request.routeId),
    targetId: normalizePolicyId(request.targetId),
  };
}

function normalizeMultiValueMap(values: TrafficPolicyValueMap | undefined, lowerCaseKeys: boolean): Map<string, string[]> {
  const map = new Map<string, string[]>();
  for (const [rawName, rawValue] of Object.entries(values ?? {})) {
    if (rawValue === null || rawValue === undefined) continue;
    const name = lowerCaseKeys ? rawName.trim().toLowerCase() : rawName.trim();
    if (!name) continue;
    const normalizedValue = Array.isArray(rawValue) ? [...rawValue] : [rawValue];
    map.set(name, normalizedValue.map((value) => value));
  }
  return map;
}

function parseRepeatedMap(text: string, lowerCaseKeys: boolean): Record<string, string[]> {
  const parsed: Record<string, string[]> = {};
  for (const line of text.split(/\r?\n/)) {
    const item = parseKeyValueLine(line);
    if (!item) continue;
    const key = lowerCaseKeys ? item.key.toLowerCase() : item.key;
    const values = parsed[key] ?? [];
    values.push(item.value);
    parsed[key] = values;
  }
  return parsed;
}

function parseCookieMap(text: string): Record<string, string> {
  const parsed: Record<string, string> = {};
  for (const part of text.split(/\r?\n|;/)) {
    const item = parseKeyValueLine(part);
    if (!item) continue;
    if (parsed[item.key] !== undefined) continue;
    parsed[item.key] = item.value;
  }
  return parsed;
}

function parseKeyValueLine(line: string): { key: string; value: string } | null {
  const trimmed = line.trim();
  if (!trimmed) return null;
  const colon = trimmed.indexOf(":");
  const equals = trimmed.indexOf("=");
  const separator = colon >= 0 && (equals < 0 || colon < equals) ? colon : equals;
  if (separator < 0) return { key: trimmed, value: "" };
  const key = trimmed.slice(0, separator).trim();
  if (!key) return null;
  return { key, value: trimmed.slice(separator + 1).trim() };
}

function normalizeCookieMap(values: TrafficPolicyCookieMap | undefined): Map<string, string> {
  const map = new Map<string, string>();
  for (const [rawName, rawValue] of Object.entries(values ?? {})) {
    if (rawValue === null || rawValue === undefined) continue;
    const name = rawName.trim();
    if (!name) continue;
    if (!map.has(name)) map.set(name, rawValue);
  }
  return map;
}

function normalizePolicyId(value: TrafficPolicyId | null | undefined): bigint | null {
  if (value === null || value === undefined || value === "") return null;
  if (typeof value === "bigint") return value;
  if (typeof value === "number") {
    return Number.isSafeInteger(value) ? BigInt(value) : null;
  }
  try {
    return BigInt(value.trim());
  } catch {
    return null;
  }
}

function normalizeMethodValue(value: string): string {
  return value.trim().toUpperCase();
}

function normalizeProtocolValue(value: string): string {
  return value.trim().toLowerCase();
}

function normalizeHostValue(value: string, operator: PublicPolicyMatchConditionOperator): string {
  const host = value.trim().toLowerCase();
  return operator === PublicPolicyMatchConditionOperator.HOST_PATTERN
    ? host.replace(/\.$/, "")
    : normalizeRequestHost(host);
}

function normalizeRequestHost(value: string): string {
  let host = value.trim().toLowerCase();
  if (host.startsWith("[")) {
    const end = host.indexOf("]");
    if (end >= 0) return normalizeBareRequestHost(host.slice(1, end));
  }
  const lastColon = host.lastIndexOf(":");
  if (lastColon > -1 && host.indexOf(":") === lastColon) {
    host = host.slice(0, lastColon);
  }
  return normalizeBareRequestHost(host);
}

function normalizeBareRequestHost(value: string): string {
  const host = value.trim().toLowerCase().replace(/^\[|\]$/g, "");
  return host.includes(":") ? host : host.replace(/\.$/, "");
}

function pathPrefixMatches(requestPath: string, pathPrefix: string): boolean {
  const prefix = pathPrefix.trim();
  const path = requestPath || "/";
  if (!prefix || prefix === "/") return true;
  if (!path.startsWith(prefix)) return false;
  if (path.length === prefix.length) return true;
  if (prefix.endsWith("/")) return true;
  return path[prefix.length] === "/";
}

function hostMatchesPattern(host: string, pattern: string): boolean {
  const normalizedHost = normalizeRequestHost(host);
  const normalizedPattern = pattern.trim().toLowerCase().replace(/\.$/, "");
  if (!normalizedPattern) return true;
  if (!normalizedPattern.startsWith("*.")) return normalizedHost === normalizedPattern;
  const suffix = normalizedPattern.slice(1);
  return normalizedHost.endsWith(suffix) && normalizedHost.length > suffix.length - 1;
}

function parseCidr(value: string): { address: Uint8Array; prefixBits: number } | null {
  const raw = value.trim();
  const slash = raw.indexOf("/");
  if (slash < 1 || slash !== raw.lastIndexOf("/")) return null;
  const address = parseIpAddress(raw.slice(0, slash));
  if (!address) return null;
  const prefixText = raw.slice(slash + 1);
  if (!/^\d+$/.test(prefixText)) return null;
  const prefixBits = Number(prefixText);
  if (!Number.isInteger(prefixBits) || prefixBits < 0 || prefixBits > address.length * 8) return null;
  return { address, prefixBits };
}

function parseIpAddress(value: string): Uint8Array | null {
  const raw = value.trim().replace(/^\[|\]$/g, "");
  return raw.includes(":") ? parseIpv6Address(raw) : parseIpv4Address(raw);
}

function parseIpv4Address(value: string): Uint8Array | null {
  const parts = value.split(".");
  if (parts.length !== 4) return null;
  const bytes = parts.map((part) => {
    if (!/^\d{1,3}$/.test(part)) return null;
    const parsed = Number(part);
    return parsed >= 0 && parsed <= 255 ? parsed : null;
  });
  if (bytes.some((byte) => byte === null)) return null;
  return Uint8Array.from(bytes as number[]);
}

function parseIpv6Address(value: string): Uint8Array | null {
  let raw = value.trim().toLowerCase();
  if (!raw || raw.includes("%")) return null;
  if (raw.includes(".")) {
    const lastColon = raw.lastIndexOf(":");
    if (lastColon < 0) return null;
    const ipv4 = parseIpv4Address(raw.slice(lastColon + 1));
    if (!ipv4) return null;
    const high = ((ipv4[0] << 8) | ipv4[1]).toString(16);
    const low = ((ipv4[2] << 8) | ipv4[3]).toString(16);
    raw = `${raw.slice(0, lastColon)}:${high}:${low}`;
  }
  const compressed = raw.split("::");
  if (compressed.length > 2) return null;
  const left = compressed[0] ? compressed[0].split(":") : [];
  const right = compressed.length === 2 && compressed[1] ? compressed[1].split(":") : [];
  if (left.some((part) => part === "") || right.some((part) => part === "")) return null;
  const missing = compressed.length === 2 ? 8 - left.length - right.length : 0;
  if (missing < 0) return null;
  const parts = compressed.length === 2
    ? [...left, ...Array.from({ length: missing }, () => "0"), ...right]
    : left;
  if (parts.length !== 8) return null;
  const bytes = new Uint8Array(16);
  for (let index = 0; index < parts.length; index += 1) {
    const part = parts[index];
    if (!/^[0-9a-f]{1,4}$/.test(part)) return null;
    const value16 = Number.parseInt(part, 16);
    bytes[index * 2] = value16 >> 8;
    bytes[index * 2 + 1] = value16 & 0xff;
  }
  return bytes;
}

function ipBytesInPrefix(ip: Uint8Array, prefix: Uint8Array, prefixBits: number): boolean {
  const fullBytes = Math.floor(prefixBits / 8);
  for (let index = 0; index < fullBytes; index += 1) {
    if (ip[index] !== prefix[index]) return false;
  }
  const remainingBits = prefixBits % 8;
  if (remainingBits === 0) return true;
  const mask = (0xff << (8 - remainingBits)) & 0xff;
  return (ip[fullBytes] & mask) === (prefix[fullBytes] & mask);
}

function duplicatePriorityWarnings<T extends { id: bigint; priority: bigint; enabled?: boolean }>(
  kind: TrafficPolicyKind,
  rules: readonly T[],
): TrafficPolicyAttentionWarning[] {
  const byPriority = new Map<string, T[]>();
  for (const rule of rules.filter((rule) => rule.enabled !== false)) {
    const key = rule.priority.toString();
    const bucket = byPriority.get(key) ?? [];
    bucket.push(rule);
    byPriority.set(key, bucket);
  }
  const warnings: TrafficPolicyAttentionWarning[] = [];
  for (const bucket of byPriority.values()) {
    if (bucket.length < 2) continue;
    warnings.push({
      code: "duplicate-priority",
      policyKind: kind,
      priority: bucket[0].priority,
      ruleIds: bucket.map((rule) => rule.id),
      message: `${policyKindLabel(kind)} priority ${bucket[0].priority.toString()} is used by ${bucket.length.toString()} enabled rules.`,
    });
  }
  return warnings;
}

function ruleShapeWarnings<T extends { id: bigint; name: string; enabled: boolean; matchRule?: PublicPolicyMatchRule | undefined }>(
  kind: TrafficPolicyKind,
  rules: readonly T[],
): TrafficPolicyAttentionWarning[] {
  const warnings: TrafficPolicyAttentionWarning[] = [];
  for (const rule of rules) {
    if (!rule.enabled) {
      warnings.push({
        code: "disabled-rule",
        policyKind: kind,
        ruleId: rule.id,
        message: `${policyKindLabel(kind)} rule ${ruleDisplayName(rule)} is disabled.`,
      });
    }
    if (rule.enabled && trafficPolicyRuleMatchesAnyRequest(rule.matchRule)) {
      warnings.push({
        code: "any-request-rule",
        policyKind: kind,
        ruleId: rule.id,
        message: `${policyKindLabel(kind)} rule ${ruleDisplayName(rule)} applies to any request.`,
      });
    }
  }
  return warnings;
}

function captchaProviderWarnings(
  rules: readonly PublicWafRule[],
  providers: readonly PublicWafCaptchaProvider[],
): TrafficPolicyAttentionWarning[] {
  const providerById = new Map(providers.map((provider) => [provider.id.toString(), provider]));
  const warnings: TrafficPolicyAttentionWarning[] = [];
  for (const rule of rules) {
    if (!rule.enabled) continue;
    if (rule.action !== PublicWafRuleAction.CAPTCHA) continue;
    const provider = providerById.get(rule.captchaProviderId.toString());
    if (!provider) {
      warnings.push({
        code: "captcha-provider-missing",
        policyKind: "waf",
        ruleId: rule.id,
        providerId: rule.captchaProviderId,
        message: `WAF captcha rule ${ruleDisplayName(rule)} references a missing captcha provider.`,
      });
    } else if (!provider.enabled) {
      warnings.push({
        code: "captcha-provider-disabled",
        policyKind: "waf",
        ruleId: rule.id,
        providerId: provider.id,
        message: `WAF captcha rule ${ruleDisplayName(rule)} uses disabled captcha provider ${provider.name || provider.id.toString()}.`,
      });
    } else if (!provider.secretKeySet) {
      warnings.push({
        code: "captcha-provider-secret-missing",
        policyKind: "waf",
        ruleId: rule.id,
        providerId: provider.id,
        message: `WAF captcha rule ${ruleDisplayName(rule)} uses captcha provider ${provider.name || provider.id.toString()} without a saved secret.`,
      });
    }
  }
  return warnings;
}

function constantGroupState(group: PublicPolicyMatchGroup): ConstantMatchState {
  const parts: ConstantMatchState[] = [
    ...group.conditions.map((): ConstantMatchState => "variable"),
    ...group.groups.map(constantGroupState),
  ];
  const joined = group.operator === PublicPolicyMatchBooleanOperator.ANY
    ? constantOrState(parts)
    : constantAndState(parts);
  return group.negated ? negateConstantState(joined) : joined;
}

function constantAndState(parts: readonly ConstantMatchState[]): ConstantMatchState {
  if (!parts.length) return "always-match";
  if (parts.some((part) => part === "always-miss")) return "always-miss";
  return parts.every((part) => part === "always-match") ? "always-match" : "variable";
}

function constantOrState(parts: readonly ConstantMatchState[]): ConstantMatchState {
  if (!parts.length) return "always-match";
  if (parts.some((part) => part === "always-match")) return "always-match";
  return parts.every((part) => part === "always-miss") ? "always-miss" : "variable";
}

function negateConstantState(state: ConstantMatchState): ConstantMatchState {
  if (state === "always-match") return "always-miss";
  if (state === "always-miss") return "always-match";
  return "variable";
}

function ruleDisplayName(rule: { id: bigint; name: string }): string {
  return rule.name.trim() || `#${rule.id.toString()}`;
}

function policyKindLabel(kind: TrafficPolicyKind): string {
  switch (kind) {
    case "rate-limit":
      return "Rate limit";
    case "traffic-shaper":
      return "Traffic shaper";
    case "cache":
      return "Cache";
    default:
      return "WAF";
  }
}
