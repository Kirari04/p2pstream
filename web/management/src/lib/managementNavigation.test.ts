import { describe, expect, test } from "bun:test";
import {
  MANAGEMENT_NAVIGATION,
  isManagementNavigationItemActive,
  managementBreadcrumbsForPath,
  managementRouteLabel,
  normalizeManagementPath,
} from "@/lib/managementNavigation";

describe("managementNavigation", () => {
  test("models the complete grouped management information architecture", () => {
    expect(MANAGEMENT_NAVIGATION.map((group) => group.label)).toEqual(["Observe", "Configure", "System"]);
    expect(MANAGEMENT_NAVIGATION[0]?.items.map((item) => item.label)).toEqual(["Overview", "Monitor"]);
    expect(MANAGEMENT_NAVIGATION[0]?.items[1]?.children?.map((item) => item.label)).toEqual(["Traffic", "Diagnostics"]);
    expect(MANAGEMENT_NAVIGATION[1]?.items.map((item) => item.label)).toEqual([
      "Proxy",
      "Agents",
      "Traffic policy",
      "Templates",
      "TLS",
    ]);
    expect(MANAGEMENT_NAVIGATION[1]?.items[0]?.children?.map((item) => item.label)).toEqual(["Sites", "Listeners"]);
    expect(MANAGEMENT_NAVIGATION[1]?.items[1]?.children?.map((item) => item.label)).toEqual(["Fleet", "Activity", "Updates"]);
    expect(MANAGEMENT_NAVIGATION[1]?.items[2]?.children?.map((item) => item.label)).toEqual([
      "Rate limits",
      "WAF",
      "Access",
      "Cache",
      "Retries",
      "Traffic shaper",
      "Visitor identity",
    ]);
    expect(MANAGEMENT_NAVIGATION[2]?.items.map((item) => item.label)).toEqual(["Environments", "API tokens", "Management TLS"]);
  });

  test("normalizes route paths and hash-history hrefs", () => {
    expect(normalizeManagementPath("/#/monitor/traffic?window=1h")).toBe("/monitor/traffic");
    expect(normalizeManagementPath("policies//waf/")).toBe("/policies/waf");
    expect(normalizeManagementPath(" /overview?refresh=1 ")).toBe("/overview");
  });

  test("marks nested parents active without activating sibling leaves", () => {
    const monitor = MANAGEMENT_NAVIGATION[0]?.items[1];
    const traffic = monitor?.children?.[0];
    const diagnostics = monitor?.children?.[1];
    expect(monitor && isManagementNavigationItemActive(monitor, "/monitor/diagnostics/samples")).toBe(true);
    expect(traffic && isManagementNavigationItemActive(traffic, "/monitor/diagnostics/samples")).toBe(false);
    expect(diagnostics && isManagementNavigationItemActive(diagnostics, "/monitor/diagnostics/samples")).toBe(true);
  });

  test("creates group, parent, and leaf breadcrumbs", () => {
    expect(managementBreadcrumbsForPath("/policies/cache")).toEqual([
      { key: "configure", label: "Configure" },
      { key: "traffic-policy", label: "Traffic policy", path: "/policies/rate-limits" },
      { key: "policy-cache", label: "Cache", path: "/policies/cache" },
    ]);
    expect(managementBreadcrumbsForPath("/agent/activity")).toEqual([
      { key: "configure", label: "Configure" },
      { key: "agents", label: "Agents", path: "/agent" },
      { key: "agents-activity", label: "Activity", path: "/agent/activity" },
    ]);
    expect(managementBreadcrumbsForPath("/agent/updates")).toEqual([
      { key: "configure", label: "Configure" },
      { key: "agents", label: "Agents", path: "/agent" },
      { key: "agents-updates", label: "Updates", path: "/agent/updates" },
    ]);
    expect(managementBreadcrumbsForPath("/monitor/diagnostics/samples")).toEqual([
      { key: "observe", label: "Observe" },
      { key: "monitor", label: "Monitor", path: "/monitor/traffic" },
      { key: "monitor-diagnostics", label: "Diagnostics", path: "/monitor/diagnostics/overview" },
    ]);
  });

  test("keeps Management TLS active and labeled on its rotation subpage", () => {
    const system = MANAGEMENT_NAVIGATION.find((group) => group.key === "system");
    const managementTls = system?.items.find((item) => item.key === "management-tls");
    expect(managementTls).toBeDefined();
    expect(isManagementNavigationItemActive(managementTls!, "/settings/management-tls/rotation")).toBe(true);
    expect(managementBreadcrumbsForPath("/settings/management-tls/rotation").map((crumb) => crumb.label)).toEqual(["System", "Management TLS"]);
    expect(isManagementNavigationItemActive(managementTls!, "/settings/management-tls-other")).toBe(false);
  });

  test("keeps WAF active and adds a breadcrumb on its captcha providers sub-page", () => {
    const trafficPolicy = MANAGEMENT_NAVIGATION[1]?.items[2];
    const waf = trafficPolicy?.children?.find((item) => item.key === "policy-waf");
    const rateLimits = trafficPolicy?.children?.find((item) => item.key === "policy-rate-limits");
    expect(waf).toBeDefined();
    expect(waf?.children).toBeUndefined();
    expect(isManagementNavigationItemActive(waf!, "/policies/waf")).toBe(true);
    expect(isManagementNavigationItemActive(waf!, "/policies/waf/captcha-providers")).toBe(true);
    expect(isManagementNavigationItemActive(waf!, "/policies/waffle")).toBe(false);
    expect(rateLimits && isManagementNavigationItemActive(rateLimits, "/policies/waf/captcha-providers")).toBe(false);
    expect(trafficPolicy && isManagementNavigationItemActive(trafficPolicy, "/policies/waf/captcha-providers")).toBe(true);
    expect(managementBreadcrumbsForPath("/policies/waf")).toEqual([
      { key: "configure", label: "Configure" },
      { key: "traffic-policy", label: "Traffic policy", path: "/policies/rate-limits" },
      { key: "policy-waf", label: "WAF", path: "/policies/waf" },
    ]);
    expect(managementBreadcrumbsForPath("/policies/waf/captcha-providers")).toEqual([
      { key: "configure", label: "Configure" },
      { key: "traffic-policy", label: "Traffic policy", path: "/policies/rate-limits" },
      { key: "policy-waf", label: "WAF", path: "/policies/waf" },
      { key: "policy-waf-captcha-providers", label: "Captcha providers", path: "/policies/waf/captcha-providers" },
    ]);
    expect(managementRouteLabel("/policies/waf/captcha-providers")).toBe("Captcha providers");
    expect(managementRouteLabel("/policies/visitor-identity")).toBe("Visitor identity");
  });

  test("returns the deepest route label with a safe fallback", () => {
    expect(managementRouteLabel("/proxy/sites")).toBe("Sites");
    expect(managementRouteLabel("/proxy/listeners")).toBe("Listeners");
    expect(managementRouteLabel("/unknown", "Not found")).toBe("Not found");
    expect(managementBreadcrumbsForPath("/unknown")).toEqual([]);
  });
});
