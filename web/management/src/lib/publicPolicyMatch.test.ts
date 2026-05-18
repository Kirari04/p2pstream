import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import {
  PublicCacheRuleSchema,
  PublicPolicyMatchBooleanOperator,
  PublicPolicyMatchBuilderSchema,
  PublicPolicyMatchConditionSchema,
  PublicPolicyMatchConditionOperator,
  PublicPolicyMatchField,
  PublicPolicyMatchGroupSchema,
  PublicPolicyMatchRuleSchema,
  PublicRateLimitRuleSchema,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  builderToCEL,
  celString,
  defaultPolicyMatchForm,
  policyMatchFormFromProto,
  policyMatchRulePayload,
  syncGeneratedExpressionForExpertMode,
} from "@/lib/publicPolicyMatch";
import { cacheRuleMatchSummary, publicPolicyMatchSummary } from "@/lib/publicProxyLabels";

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

  test("splits values according to operator semantics", () => {
    const equalsForm = defaultPolicyMatchForm();
    equalsForm.root.conditions.push({
      field: PublicPolicyMatchField.HEADER,
      name: "X-Plan",
      operator: PublicPolicyMatchConditionOperator.EQUALS,
      valuesText: "free,tier",
      negated: false,
    });

    const regexForm = defaultPolicyMatchForm();
    regexForm.root.conditions.push({
      field: PublicPolicyMatchField.PATH,
      name: "",
      operator: PublicPolicyMatchConditionOperator.MATCHES,
      valuesText: "^/items/(a,b)$",
      negated: false,
    });

    const inForm = defaultPolicyMatchForm();
    inForm.root.conditions.push({
      field: PublicPolicyMatchField.METHOD,
      name: "",
      operator: PublicPolicyMatchConditionOperator.IN,
      valuesText: "get, post\nPUT",
      negated: false,
    });

    expect(policyMatchRulePayload(equalsForm)?.builder?.root?.conditions[0]?.values).toEqual(["free,tier"]);
    expect(policyMatchRulePayload(equalsForm)?.celExpression).toContain('"free,tier"');
    expect(policyMatchRulePayload(regexForm)?.builder?.root?.conditions[0]?.values).toEqual(["^/items/(a,b)$"]);
    expect(policyMatchRulePayload(regexForm)?.celExpression).toContain('path.matches("^/items/(a,b)$")');
    expect(policyMatchRulePayload(inForm)?.builder?.root?.conditions[0]?.values).toEqual(["GET", "POST", "PUT"]);
    expect(policyMatchRulePayload(inForm)?.celExpression).toContain('method in ["GET", "POST", "PUT"]');
  });

  test("ignores empty nested groups without widening ANY expressions", () => {
    const form = defaultPolicyMatchForm();
    form.root.operator = PublicPolicyMatchBooleanOperator.ANY;
    form.root.conditions.push({
      field: PublicPolicyMatchField.PATH,
      name: "",
      operator: PublicPolicyMatchConditionOperator.PREFIX,
      valuesText: "/api",
      negated: false,
    });
    form.root.groups.push({
      operator: PublicPolicyMatchBooleanOperator.ALL,
      negated: false,
      conditions: [],
      groups: [],
    });

    const expression = builderToCEL(form.root);

    expect(builderToCEL(defaultPolicyMatchForm().root)).toBe("true");
    expect(expression).not.toContain("|| true");
    expect(expression).not.toBe("true");
    expect(expression).toContain('path_prefix(path, "/api")');
  });

  test("preserves manual CEL when switching to expert mode", () => {
    const form = defaultPolicyMatchForm();
    form.root.conditions.push({
      field: PublicPolicyMatchField.PATH,
      name: "",
      operator: PublicPolicyMatchConditionOperator.PREFIX,
      valuesText: "/assets",
      negated: false,
    });

    syncGeneratedExpressionForExpertMode(form);
    expect(form.expression).toContain('path_prefix(path, "/assets")');

    form.root.conditions[0].valuesText = "/api";
    syncGeneratedExpressionForExpertMode(form);
    expect(form.expression).toContain('path_prefix(path, "/api")');

    form.expression = 'method == "POST"';
    form.root.conditions[0].valuesText = "/admin";
    syncGeneratedExpressionForExpertMode(form);
    expect(form.expression).toBe('method == "POST"');
  });

  test("expert mode stores only the CEL expression", () => {
    const form = defaultPolicyMatchForm();
    form.mode = "expression";
    form.expression = ' method == "GET" ';

    expect(policyMatchRulePayload(form)).toEqual({ celExpression: 'method == "GET"' });

    form.expression = " ";
    expect(policyMatchRulePayload(form)).toBeUndefined();
  });

  test("builder mode preserves CEL-only rules when the builder is empty", () => {
    const form = policyMatchFormFromProto(create(PublicPolicyMatchRuleSchema, {
      celExpression: 'method == "POST"',
    }));

    form.mode = "builder";

    expect(policyMatchRulePayload(form)).toEqual({ celExpression: 'method == "POST"' });
  });

  test("builder proto forms use generated builder CEL as sync baseline", () => {
    const form = policyMatchFormFromProto(create(PublicPolicyMatchRuleSchema, {
      celExpression: 'method == "POST"',
      builder: create(PublicPolicyMatchBuilderSchema, {
        root: create(PublicPolicyMatchGroupSchema, {
          conditions: [create(PublicPolicyMatchConditionSchema, {
            field: PublicPolicyMatchField.METHOD,
            operator: PublicPolicyMatchConditionOperator.EQUALS,
            values: ["GET"],
          })],
        }),
      }),
    }));

    syncGeneratedExpressionForExpertMode(form);

    expect(form.expression).toContain('method == "GET"');
    expect(form.lastGeneratedExpression).toContain('method == "GET"');
  });

  test("empty builder payloads only save when the root is negated", () => {
    const emptyForm = defaultPolicyMatchForm();
    expect(policyMatchRulePayload(emptyForm)).toBeUndefined();

    const negatedForm = defaultPolicyMatchForm();
    negatedForm.root.negated = true;
    const payload = policyMatchRulePayload(negatedForm);

    expect(payload?.builder?.root?.negated).toBe(true);
    expect(payload?.celExpression).toBe("!(true)");
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

  test("summarizes matchRule builder, CEL-only, and empty rules", () => {
    const builderRule = create(PublicRateLimitRuleSchema, {
      matchRule: create(PublicPolicyMatchRuleSchema, {
        builder: create(PublicPolicyMatchBuilderSchema, {
          root: create(PublicPolicyMatchGroupSchema, {
            conditions: [
              create(PublicPolicyMatchConditionSchema, {
                field: PublicPolicyMatchField.METHOD,
                operator: PublicPolicyMatchConditionOperator.IN,
                values: ["GET", "POST"],
              }),
              create(PublicPolicyMatchConditionSchema, {
                field: PublicPolicyMatchField.HEADER,
                name: "X-Plan",
                operator: PublicPolicyMatchConditionOperator.EQUALS,
                values: ["free"],
              }),
              create(PublicPolicyMatchConditionSchema, {
                field: PublicPolicyMatchField.QUERY_PARAM,
                name: "debug",
                operator: PublicPolicyMatchConditionOperator.PRESENT,
              }),
              create(PublicPolicyMatchConditionSchema, {
                field: PublicPolicyMatchField.REMOTE_IP,
                operator: PublicPolicyMatchConditionOperator.CIDR,
                values: ["198.51.100.0/24"],
              }),
            ],
          }),
        }),
      }),
    });
    const expertRule = create(PublicRateLimitRuleSchema, {
      matchRule: create(PublicPolicyMatchRuleSchema, {
        celExpression: 'method == "GET" && path_prefix(path, "/assets")',
      }),
    });
    const complexBuilderRule = create(PublicRateLimitRuleSchema, {
      matchRule: create(PublicPolicyMatchRuleSchema, {
        builder: create(PublicPolicyMatchBuilderSchema, {
          root: create(PublicPolicyMatchGroupSchema, {
            operator: PublicPolicyMatchBooleanOperator.ANY,
            conditions: [
              create(PublicPolicyMatchConditionSchema, {
                field: PublicPolicyMatchField.METHOD,
                operator: PublicPolicyMatchConditionOperator.EQUALS,
                values: ["GET"],
              }),
            ],
          }),
        }),
      }),
    });

    expect(publicPolicyMatchSummary(create(PublicRateLimitRuleSchema))).toBe("Any request");
    expect(publicPolicyMatchSummary(builderRule)).toBe("method in GET,POST / header:X-Plan = free / query:debug present / +1");
    expect(publicPolicyMatchSummary(expertRule)).toBe('CEL: method == "GET" && path_prefix(path, "/assets")');
    expect(publicPolicyMatchSummary(complexBuilderRule)).toBe("Complex builder rule");
  });

  test("summarizes cache matchRule with route and backend filters", () => {
    const cacheRule = create(PublicCacheRuleSchema, {
      matchRule: create(PublicPolicyMatchRuleSchema, {
        celExpression: 'host_match(host, "*.example.com")',
      }),
      routeIds: [1n, 2n],
      backendIds: [3n],
    });

    expect(cacheRuleMatchSummary(create(PublicCacheRuleSchema))).toBe("Any request");
    expect(cacheRuleMatchSummary(cacheRule)).toBe('CEL: host_match(host, "*.example.com") / 2 routes / 1 backend');
  });
});
