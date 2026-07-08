import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import {
  PublicCacheRuleSchema,
  PublicCacheSettingsSchema,
  PublicPolicyMatchBooleanOperator,
  PublicPolicyMatchBuilderSchema,
  PublicPolicyMatchConditionOperator,
  PublicPolicyMatchConditionSchema,
  PublicPolicyMatchField,
  PublicPolicyMatchGroupSchema,
  PublicPolicyMatchRuleSchema,
  PublicRateLimitRuleSchema,
  PublicTrafficShaperRuleSchema,
  PublicWafActivationMode,
  PublicWafCaptchaProviderSchema,
  PublicWafRuleAction,
  PublicWafRuleSchema,
  type PublicCacheRule,
  type PublicPolicyMatchCondition,
  type PublicPolicyMatchGroup,
  type PublicPolicyMatchRule,
  type PublicRateLimitRule,
  type PublicTrafficShaperRule,
  type PublicWafCaptchaProvider,
  type PublicWafRule,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  buildTrafficPolicyPlaygroundStages,
  defaultTrafficPolicyPreviewForm,
  evaluateTrafficPolicyMatch,
  previewTrafficPolicyStages,
  runtimeOrderedCacheRules,
  runtimeOrderedRateLimitRules,
  runtimeOrderedTrafficShaperRules,
  runtimeOrderedWafRules,
  trafficPolicyPreviewFormToRequest,
  trafficPolicyAttentionWarnings,
  type TrafficPolicySyntheticRequest,
} from "@/lib/trafficPolicyWorkbench";

describe("trafficPolicyWorkbench", () => {
  test("evaluates builder match operators against a synthetic request", () => {
    const rule = builderRule([
      condition({ field: PublicPolicyMatchField.METHOD, operator: PublicPolicyMatchConditionOperator.EQUALS, values: ["get"] }),
      condition({ field: PublicPolicyMatchField.PROTOCOL, operator: PublicPolicyMatchConditionOperator.EQUALS, values: ["HTTPS"] }),
      condition({ field: PublicPolicyMatchField.HOST, operator: PublicPolicyMatchConditionOperator.HOST_PATTERN, values: ["*.example.test."] }),
      condition({ field: PublicPolicyMatchField.PATH, operator: PublicPolicyMatchConditionOperator.PREFIX, values: ["/api"] }),
      condition({ field: PublicPolicyMatchField.PATH, operator: PublicPolicyMatchConditionOperator.SUFFIX, values: [".json"] }),
      condition({ field: PublicPolicyMatchField.PATH, operator: PublicPolicyMatchConditionOperator.CONTAINS, values: ["/users/"] }),
      condition({ field: PublicPolicyMatchField.REMOTE_IP, operator: PublicPolicyMatchConditionOperator.CIDR, values: ["198.51.100.0/24"] }),
      condition({ field: PublicPolicyMatchField.HEADER, name: "X-Plan", operator: PublicPolicyMatchConditionOperator.PRESENT }),
      condition({ field: PublicPolicyMatchField.HEADER, name: "X-Plan", operator: PublicPolicyMatchConditionOperator.IN, values: ["free", "pro"] }),
      condition({ field: PublicPolicyMatchField.COOKIE, name: "sid", operator: PublicPolicyMatchConditionOperator.PREFIX, values: ["abc"] }),
      condition({ field: PublicPolicyMatchField.QUERY_PARAM, name: "role", operator: PublicPolicyMatchConditionOperator.EQUALS, values: ["ops"] }),
    ]);

    expect(evaluateTrafficPolicyMatch(rule, syntheticRequest({ cookies: { sid: "abc-123" } })).state).toBe("match");
    expect(evaluateTrafficPolicyMatch(rule, syntheticRequest({ path: "/apiv2/users/42.json", cookies: { sid: "abc-123" } })).state).toBe("miss");
    expect(evaluateTrafficPolicyMatch(rule, syntheticRequest({ remoteIp: "203.0.113.10", cookies: { sid: "abc-123" } })).state).toBe("miss");
  });

  test("treats invalid CIDR inputs as misses", () => {
    const invalidCidrRule = builderRule([
      condition({ field: PublicPolicyMatchField.REMOTE_IP, operator: PublicPolicyMatchConditionOperator.CIDR, values: ["not-a-cidr"] }),
    ]);
    const invalidIpRule = builderRule([
      condition({ field: PublicPolicyMatchField.REMOTE_IP, operator: PublicPolicyMatchConditionOperator.CIDR, values: ["198.51.100.0/24"] }),
    ]);

    expect(evaluateTrafficPolicyMatch(invalidCidrRule, syntheticRequest()).state).toBe("miss");
    expect(evaluateTrafficPolicyMatch(invalidIpRule, syntheticRequest({ remoteIp: "not-an-ip" })).state).toBe("miss");
  });

  test("returns match for empty rules and unknown for CEL-only or regex rules", () => {
    expect(evaluateTrafficPolicyMatch(undefined, syntheticRequest()).state).toBe("match");
    expect(evaluateTrafficPolicyMatch(create(PublicPolicyMatchRuleSchema), syntheticRequest()).state).toBe("match");
    expect(evaluateTrafficPolicyMatch(celRule('method == "GET"'), syntheticRequest()).state).toBe("unknown");

    const regexRule = builderRule([
      condition({ field: PublicPolicyMatchField.PATH, operator: PublicPolicyMatchConditionOperator.MATCHES, values: ["^/api/users/[0-9]+\\.json$"] }),
    ]);
    expect(evaluateTrafficPolicyMatch(regexRule, syntheticRequest())).toEqual({
      state: "unknown",
      reason: "Regex policy matches are not evaluated in the browser.",
    });

    const anyWithUnknown = builderRule([
      condition({ field: PublicPolicyMatchField.METHOD, operator: PublicPolicyMatchConditionOperator.EQUALS, values: ["POST"] }),
    ], [
      group([
        condition({ field: PublicPolicyMatchField.PATH, operator: PublicPolicyMatchConditionOperator.MATCHES, values: ["["] }),
      ]),
    ], PublicPolicyMatchBooleanOperator.ANY);
    expect(evaluateTrafficPolicyMatch(anyWithUnknown, syntheticRequest()).state).toBe("unknown");
  });

  test("converts preview form text fields into synthetic requests", () => {
    const form = defaultTrafficPolicyPreviewForm();
    Object.assign(form, {
      hasRequestBody: true,
      headersText: "X-Plan: pro\nX-Plan: enterprise\nEmpty",
      cookiesText: "sid=first; sid=second\nmode=dark",
      queryText: "debug=1\nrole=ops",
      routeId: "10",
      targetId: "20",
    });

    const request = trafficPolicyPreviewFormToRequest(form);

    expect(request.hasRequestBody).toBe(true);
    expect(request.headers).toEqual({ "x-plan": ["pro", "enterprise"], empty: [""] });
    expect(request.cookies).toEqual({ sid: "first", mode: "dark" });
    expect(request.query).toEqual({ debug: ["1"], role: ["ops"] });
    expect(request.routeId).toBe("10");
    expect(request.targetId).toBe("20");
  });

  test("sorts all policy kinds by runtime priority then id", () => {
    expect(runtimeOrderedWafRules([
      wafRule({ id: 3n, priority: 2n }),
      wafRule({ id: 2n, priority: 1n }),
      wafRule({ id: 1n, priority: 1n }),
    ]).map((rule) => rule.id)).toEqual([1n, 2n, 3n]);
    expect(runtimeOrderedRateLimitRules([
      rateLimitRule({ id: 3n, priority: 2n }),
      rateLimitRule({ id: 2n, priority: 1n }),
      rateLimitRule({ id: 1n, priority: 1n }),
    ]).map((rule) => rule.id)).toEqual([1n, 2n, 3n]);
    expect(runtimeOrderedTrafficShaperRules([
      trafficShaperRule({ id: 3n, priority: 2n }),
      trafficShaperRule({ id: 2n, priority: 1n }),
      trafficShaperRule({ id: 1n, priority: 1n }),
    ]).map((rule) => rule.id)).toEqual([1n, 2n, 3n]);
    expect(runtimeOrderedCacheRules([
      cacheRule({ id: 3n, priority: 2n }),
      cacheRule({ id: 2n, priority: 1n }),
      cacheRule({ id: 1n, priority: 1n }),
    ]).map((rule) => rule.id)).toEqual([1n, 2n, 3n]);
  });

  test("previews first-match stages and all matching rate limits", () => {
    const pathMatch = builderRule([
      condition({ field: PublicPolicyMatchField.PATH, operator: PublicPolicyMatchConditionOperator.PREFIX, values: ["/api"] }),
    ]);
    const pathMiss = builderRule([
      condition({ field: PublicPolicyMatchField.PATH, operator: PublicPolicyMatchConditionOperator.PREFIX, values: ["/admin"] }),
    ]);
    const preview = previewTrafficPolicyStages({
      wafRules: [
        wafRule({ id: 3n, priority: 0n, enabled: false, matchRule: pathMatch }),
        wafRule({ id: 1n, priority: 1n, matchRule: celRule('path.startsWith("/api")') }),
        wafRule({ id: 2n, priority: 2n, matchRule: pathMatch }),
      ],
      rateLimitRules: [
        rateLimitRule({ id: 10n, priority: 1n, matchRule: pathMatch }),
        rateLimitRule({ id: 11n, priority: 2n, matchRule: celRule('path.startsWith("/api")') }),
        rateLimitRule({ id: 12n, priority: 3n, matchRule: pathMiss }),
      ],
      trafficShaperRules: [
        trafficShaperRule({ id: 20n, priority: 1n, matchRule: pathMiss }),
        trafficShaperRule({ id: 21n, priority: 2n, matchRule: pathMatch }),
      ],
      cacheRules: [
        cacheRule({ id: 30n, priority: 1n, routeIds: [11n], targetIds: [20n], matchRule: pathMatch }),
        cacheRule({ id: 31n, priority: 2n, routeIds: [10n], targetIds: [20n], matchRule: pathMatch }),
      ],
    }, syntheticRequest({ routeId: 10n, targetId: 20n, cookies: {} }));

    expect(preview.waf?.rule.id).toBe(1n);
    expect(preview.waf?.result.state).toBe("unknown");
    expect(preview.rateLimits.map((candidate) => [candidate.rule.id, candidate.result.state])).toEqual([
      [10n, "match"],
      [11n, "unknown"],
    ]);
    expect(preview.trafficShaper?.rule.id).toBe(21n);
    expect(preview.cache?.rule.id).toBe(31n);
  });

  test("keeps later first-match rules uncertain after an unknown selected candidate", () => {
    const stages = buildTrafficPolicyPlaygroundStages({
      wafRules: [
        wafRule({ id: 1n, priority: 1n, activationMode: PublicWafActivationMode.AUTOMATIC, matchRule: pathRule("/api") }),
        wafRule({ id: 2n, priority: 2n, matchRule: pathRule("/api") }),
      ],
    }, syntheticRequest());
    const wafStage = stages.find((stage) => stage.key === "waf");

    expect(wafStage?.items.map((item) => [item.id, item.state, item.selected, item.skipped])).toEqual([
      [1n, "unknown", true, false],
      [2n, "unknown", false, false],
    ]);
    expect(wafStage?.items[1].reason).toBe("May still apply; earlier preview result is unknown.");
  });

  test("skips later first-match rules after a confirmed selected candidate", () => {
    const stages = buildTrafficPolicyPlaygroundStages({
      trafficShaperRules: [
        trafficShaperRule({ id: 20n, priority: 1n, matchRule: pathRule("/api") }),
        trafficShaperRule({ id: 21n, priority: 2n, matchRule: pathRule("/api") }),
      ],
    }, syntheticRequest());
    const trafficShaperStage = stages.find((stage) => stage.key === "traffic-shaper");

    expect(trafficShaperStage?.items.map((item) => [item.id, item.state, item.selected, item.skipped])).toEqual([
      [20n, "match", true, false],
      [21n, "miss", false, true],
    ]);
    expect(trafficShaperStage?.items[1].reason).toBe("Skipped after earlier confirmed match");
  });

  test("treats automatic WAF activation as unknown after a rule match", () => {
    const preview = previewTrafficPolicyStages({
      wafRules: [
        wafRule({ id: 1n, priority: 1n, activationMode: PublicWafActivationMode.AUTOMATIC, matchRule: pathRule("/api") }),
        wafRule({ id: 2n, priority: 2n, matchRule: pathRule("/api") }),
      ],
    }, syntheticRequest());

    expect(preview.waf?.rule.id).toBe(1n);
    expect(preview.waf?.result).toEqual({
      state: "unknown",
      reason: "Automatic WAF activation depends on live trigger state.",
    });
  });

  test("does not select cache rules for runtime cache bypass requests", () => {
    const config = {
      cacheSettings: create(PublicCacheSettingsSchema, { enabled: true }),
      cacheRules: [cacheRule({ id: 30n, priority: 1n, matchRule: pathRule("/api") })],
    };

    expect(previewTrafficPolicyStages(config, syntheticRequest({ method: "POST", cookies: {} })).cache).toBeNull();
    expect(previewTrafficPolicyStages(config, syntheticRequest({ headers: { Authorization: ["Bearer token"] }, cookies: {} })).cache).toBeNull();
    expect(previewTrafficPolicyStages(config, syntheticRequest({ cookies: { sid: "abc-123" } })).cache).toBeNull();
    expect(previewTrafficPolicyStages(config, syntheticRequest({ headers: { Range: ["bytes=0-10"] }, cookies: {} })).cache).toBeNull();
    expect(previewTrafficPolicyStages(config, syntheticRequest({ headers: { Connection: ["upgrade"] }, cookies: {} })).cache).toBeNull();
    expect(previewTrafficPolicyStages(config, syntheticRequest({ hasRequestBody: true, cookies: {} })).cache).toBeNull();
  });

  test("marks encoded path separator cache behavior unknown", () => {
    const config = {
      cacheSettings: create(PublicCacheSettingsSchema, { enabled: true }),
      cacheRules: [
        cacheRule({ id: 30n, priority: 1n, matchRule: builderRule() }),
        cacheRule({ id: 31n, priority: 2n, matchRule: builderRule() }),
      ],
    };
    const request = syntheticRequest({ path: "/api%2fusers/42.json", cookies: {} });
    const preview = previewTrafficPolicyStages(config, request);
    const cacheStage = buildTrafficPolicyPlaygroundStages(config, request).find((stage) => stage.key === "cache");

    expect(preview.cache?.rule.id).toBe(30n);
    expect(preview.cache?.result).toEqual({
      state: "unknown",
      reason: "Encoded path separator cache behavior depends on route path security settings.",
      canFallThrough: false,
    });
    expect(cacheStage?.items[1]).toMatchObject({
      id: 31n,
      state: "unknown",
      selected: false,
      skipped: false,
      reason: "Not evaluated because stage eligibility is unknown.",
    });
  });

  test("emits attention warnings for policy risks", () => {
    const warnings = trafficPolicyAttentionWarnings({
      wafCaptchaProviders: [
        captchaProvider({ id: 7n, enabled: false }),
        captchaProvider({ id: 8n, enabled: true, secretKeySet: false }),
      ],
      wafRules: [
        wafRule({ id: 1n, priority: 1n, action: PublicWafRuleAction.CAPTCHA, captchaProviderId: 99n }),
        wafRule({ id: 2n, priority: 1n, enabled: false, matchRule: pathRule("/waf") }),
        wafRule({ id: 3n, priority: 2n, action: PublicWafRuleAction.CAPTCHA, captchaProviderId: 7n, matchRule: pathRule("/captcha") }),
        wafRule({ id: 4n, priority: 1n, matchRule: pathRule("/waf-enabled") }),
        wafRule({ id: 5n, priority: 3n, action: PublicWafRuleAction.CAPTCHA, captchaProviderId: 8n, matchRule: pathRule("/captcha-secret") }),
      ],
      rateLimitRules: [
        rateLimitRule({ id: 10n, priority: 1n }),
        rateLimitRule({ id: 11n, priority: 1n, enabled: false, matchRule: pathRule("/rate") }),
        rateLimitRule({ id: 12n, priority: 1n, matchRule: pathRule("/rate-enabled") }),
      ],
      trafficShaperRules: [
        trafficShaperRule({ id: 20n, priority: 1n }),
        trafficShaperRule({ id: 21n, priority: 1n, matchRule: pathRule("/shape") }),
      ],
      cacheSettings: create(PublicCacheSettingsSchema, { enabled: false }),
      cacheRules: [
        cacheRule({ id: 30n, priority: 1n, allowCookieRequests: true }),
        cacheRule({ id: 31n, priority: 1n, matchRule: pathRule("/cache") }),
        cacheRule({ id: 32n, priority: 2n, enabled: false, allowCookieRequests: true }),
      ],
    });

    expect(warnings.filter((warning) => warning.code === "duplicate-priority").map((warning) => warning.policyKind).sort()).toEqual([
      "cache",
      "rate-limit",
      "traffic-shaper",
      "waf",
    ]);
    expect(warnings.map((warning) => warning.code)).toContain("disabled-rule");
    expect(warnings.map((warning) => warning.code)).toContain("any-request-rule");
    expect(warnings.map((warning) => warning.code)).toContain("captcha-provider-missing");
    expect(warnings.map((warning) => warning.code)).toContain("captcha-provider-disabled");
    expect(warnings.map((warning) => warning.code)).toContain("captcha-provider-secret-missing");
    expect(warnings.map((warning) => warning.code)).toContain("cache-settings-disabled");
    expect(warnings.map((warning) => warning.code)).toContain("cache-allows-cookie-requests");
    expect(warnings.some((warning) => warning.ruleId === 2n && warning.code === "any-request-rule")).toBe(false);
    expect(warnings.some((warning) => warning.ruleId === 32n && warning.code === "cache-allows-cookie-requests")).toBe(false);
  });
});

function syntheticRequest(overrides: Partial<TrafficPolicySyntheticRequest> = {}): TrafficPolicySyntheticRequest {
  return {
    method: "GET",
    protocol: "https",
    host: "api.example.test:443",
    path: "/api/users/42.json",
    remoteIp: "198.51.100.23",
    headers: { "X-Plan": ["trial", "pro"] },
    cookies: {},
    query: { role: ["viewer", "ops"] },
    routeId: 10n,
    targetId: 20n,
    ...overrides,
  };
}

function pathRule(prefix: string): PublicPolicyMatchRule {
  return builderRule([
    condition({ field: PublicPolicyMatchField.PATH, operator: PublicPolicyMatchConditionOperator.PREFIX, values: [prefix] }),
  ]);
}

function celRule(expression: string): PublicPolicyMatchRule {
  return create(PublicPolicyMatchRuleSchema, { celExpression: expression });
}

function builderRule(
  conditions: PublicPolicyMatchCondition[] = [],
  groups: PublicPolicyMatchGroup[] = [],
  operator = PublicPolicyMatchBooleanOperator.ALL,
): PublicPolicyMatchRule {
  return create(PublicPolicyMatchRuleSchema, {
    builder: create(PublicPolicyMatchBuilderSchema, {
      root: group(conditions, groups, operator),
    }),
  });
}

function group(
  conditions: PublicPolicyMatchCondition[] = [],
  groups: PublicPolicyMatchGroup[] = [],
  operator = PublicPolicyMatchBooleanOperator.ALL,
): PublicPolicyMatchGroup {
  return create(PublicPolicyMatchGroupSchema, {
    operator,
    conditions,
    groups,
  });
}

function condition(overrides: Partial<PublicPolicyMatchCondition>): PublicPolicyMatchCondition {
  return create(PublicPolicyMatchConditionSchema, {
    field: overrides.field ?? PublicPolicyMatchField.PATH,
    name: overrides.name ?? "",
    operator: overrides.operator ?? PublicPolicyMatchConditionOperator.EQUALS,
    values: overrides.values ? [...overrides.values] : [],
    negated: overrides.negated ?? false,
  });
}

function rateLimitRule(overrides: Partial<PublicRateLimitRule>): PublicRateLimitRule {
  return create(PublicRateLimitRuleSchema, {
    id: overrides.id ?? 1n,
    name: overrides.name ?? `rate-${overrides.id?.toString() ?? "1"}`,
    priority: overrides.priority ?? 1n,
    enabled: overrides.enabled ?? true,
    matchRule: overrides.matchRule,
  });
}

function trafficShaperRule(overrides: Partial<PublicTrafficShaperRule>): PublicTrafficShaperRule {
  return create(PublicTrafficShaperRuleSchema, {
    id: overrides.id ?? 1n,
    name: overrides.name ?? `shape-${overrides.id?.toString() ?? "1"}`,
    priority: overrides.priority ?? 1n,
    enabled: overrides.enabled ?? true,
    matchRule: overrides.matchRule,
  });
}

function wafRule(overrides: Partial<PublicWafRule>): PublicWafRule {
  return create(PublicWafRuleSchema, {
    id: overrides.id ?? 1n,
    name: overrides.name ?? `waf-${overrides.id?.toString() ?? "1"}`,
    priority: overrides.priority ?? 1n,
    enabled: overrides.enabled ?? true,
    activationMode: overrides.activationMode ?? PublicWafActivationMode.ALWAYS,
    action: overrides.action ?? PublicWafRuleAction.BLOCK,
    captchaProviderId: overrides.captchaProviderId ?? 0n,
    matchRule: overrides.matchRule,
  });
}

function cacheRule(overrides: Partial<PublicCacheRule>): PublicCacheRule {
  return create(PublicCacheRuleSchema, {
    id: overrides.id ?? 1n,
    name: overrides.name ?? `cache-${overrides.id?.toString() ?? "1"}`,
    priority: overrides.priority ?? 1n,
    enabled: overrides.enabled ?? true,
    routeIds: overrides.routeIds ?? [],
    targetIds: overrides.targetIds ?? [],
    matchRule: overrides.matchRule,
    allowCookieRequests: overrides.allowCookieRequests ?? false,
  });
}

function captchaProvider(overrides: Partial<PublicWafCaptchaProvider>): PublicWafCaptchaProvider {
  return create(PublicWafCaptchaProviderSchema, {
    id: overrides.id ?? 1n,
    name: overrides.name ?? `provider-${overrides.id?.toString() ?? "1"}`,
    enabled: overrides.enabled ?? true,
    secretKeySet: overrides.secretKeySet ?? true,
  });
}
