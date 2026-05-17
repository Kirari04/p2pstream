import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import {
  PublicListenerProtocol,
  PublicPolicyMatchBooleanOperator,
  PublicPolicyMatchConditionOperator,
  PublicPolicyMatchField,
  PublicRateLimitMatchSchema,
  PublicRateLimitMatchOperator,
  PublicRateLimitValueMatcherSchema,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  builderToCEL,
  celString,
  defaultPolicyMatchForm,
  policyMatchFormFromProto,
  policyMatchRulePayload,
} from "@/lib/publicPolicyMatch";

describe("publicPolicyMatch", () => {
  test("generates CEL from nested builder groups", () => {
    const form = defaultPolicyMatchForm();
    form.root.operator = PublicPolicyMatchBooleanOperator.ALL;
    form.root.conditions.push({
      field: PublicPolicyMatchField.METHOD,
      name: "",
      operator: PublicPolicyMatchConditionOperator.IN,
      valuesText: "GET\nPOST",
      negated: false,
    });
    form.root.groups.push({
      operator: PublicPolicyMatchBooleanOperator.ANY,
      negated: true,
      conditions: [
        {
          field: PublicPolicyMatchField.QUERY_PARAM,
          name: "debug",
          operator: PublicPolicyMatchConditionOperator.PRESENT,
          valuesText: "",
          negated: false,
        },
        {
          field: PublicPolicyMatchField.COOKIE,
          name: "sid",
          operator: PublicPolicyMatchConditionOperator.PREFIX,
          valuesText: "test-",
          negated: false,
        },
      ],
      groups: [],
    });

    const expression = builderToCEL(form.root);

    expect(expression).toContain('method in ["GET", "POST"]');
    expect(expression).toContain('"debug" in query');
    expect(expression).toContain('cookies["sid"].startsWith("test-")');
    expect(expression).toContain("!(");
    expect(expression).toContain(" || ");
    expect(expression).toContain(" && ");
  });

  test("escapes CEL string literals", () => {
    expect(celString('a"b\\c\n')).toBe(JSON.stringify('a"b\\c\n'));

    const form = defaultPolicyMatchForm();
    form.root.conditions.push({
      field: PublicPolicyMatchField.HEADER,
      name: "X-Plan",
      operator: PublicPolicyMatchConditionOperator.EQUALS,
      valuesText: 'free"tier',
      negated: false,
    });

    expect(policyMatchRulePayload(form)?.celExpression).toContain('"free\\"tier"');
  });

  test("converts legacy matches into builder rules", () => {
    const form = policyMatchFormFromProto(undefined, create(PublicRateLimitMatchSchema, {
      methods: ["GET"],
      protocols: [PublicListenerProtocol.HTTPS],
      hostPatterns: ["*.example.com"],
      pathPrefixes: ["/api"],
      pathSuffixes: [".json"],
      headers: [create(PublicRateLimitValueMatcherSchema, { name: "X-Plan", operator: PublicRateLimitMatchOperator.EQUALS, value: "free" })],
      cookies: [create(PublicRateLimitValueMatcherSchema, { name: "session", operator: PublicRateLimitMatchOperator.PRESENT, value: "" })],
      queryParams: [create(PublicRateLimitValueMatcherSchema, { name: "page", operator: PublicRateLimitMatchOperator.PREFIX, value: "1" })],
    }));

    const payload = policyMatchRulePayload(form);

    expect(form.mode).toBe("builder");
    expect(form.root.conditions).toHaveLength(8);
    expect(payload?.builder?.root?.conditions).toHaveLength(8);
    expect(payload?.celExpression).toContain('host_match(host, "*.example.com")');
    expect(payload?.celExpression).toContain('path_prefix(path, "/api")');
    expect(payload?.celExpression).toContain('"x-plan" in headers');
    expect(payload?.celExpression).toContain('"session" in cookies');
    expect(payload?.celExpression).toContain('query["page"].exists');
  });

  test("expert mode stores only the CEL expression", () => {
    const form = defaultPolicyMatchForm();
    form.mode = "expression";
    form.expression = ' method == "GET" ';

    expect(policyMatchRulePayload(form)).toEqual({ celExpression: 'method == "GET"' });

    form.expression = " ";
    expect(policyMatchRulePayload(form)).toBeUndefined();
  });

  test("normalizes invalid present operators before payload generation", () => {
    const form = defaultPolicyMatchForm();
    form.root.conditions.push({
      field: PublicPolicyMatchField.METHOD,
      name: "",
      operator: PublicPolicyMatchConditionOperator.PRESENT,
      valuesText: "GET",
      negated: false,
    });

    const condition = policyMatchRulePayload(form)?.builder?.root?.conditions[0];

    expect(condition?.operator).toBe(PublicPolicyMatchConditionOperator.EQUALS);
    expect(condition?.values).toEqual(["GET"]);
  });

  test("builder payloads use the shared matchRule shape for all policy editors", () => {
    const form = defaultPolicyMatchForm();
    form.root.conditions.push({
      field: PublicPolicyMatchField.PATH,
      name: "",
      operator: PublicPolicyMatchConditionOperator.PREFIX,
      valuesText: "/assets",
      negated: false,
    });
    const matchRule = policyMatchRulePayload(form);

    const saves = [
      { kind: "rate-limit", matchRule },
      { kind: "traffic-shaper", matchRule },
      { kind: "waf", matchRule },
      { kind: "cache", matchRule, routeIds: [1n], backendIds: [2n] },
    ];

    expect(matchRule?.builder?.root?.conditions[0]?.values).toEqual(["/assets"]);
    expect(saves.every((payload) => payload.matchRule === matchRule)).toBe(true);
  });
});
