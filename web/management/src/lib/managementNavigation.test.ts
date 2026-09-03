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
      "Traffic Policy",
      "Templates",
      "TLS",
    ]);
    expect(MANAGEMENT_NAVIGATION[1]?.items[0]?.children?.map((item) => item.label)).toEqual(["Routes", "Listeners"]);
    expect(MANAGEMENT_NAVIGATION[1]?.items[1]?.children?.map((item) => item.label)).toEqual(["Fleet", "Activity", "Updates"]);
    expect(MANAGEMENT_NAVIGATION[1]?.items[2]?.children?.map((item) => item.label)).toEqual([
      "Rate Limits",
      "WAF",
      "Access",
      "Cache",
      "Retries",
      "Traffic Shaper",
    ]);
    expect(MANAGEMENT_NAVIGATION[2]?.items.map((item) => item.label)).toEqual(["Environments", "API Tokens", "Management TLS"]);
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
      { key: "traffic-policy", label: "Traffic Policy", path: "/policies/rate-limits" },
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

  test("returns the deepest route label with a safe fallback", () => {
    expect(managementRouteLabel("/proxy/listeners")).toBe("Listeners");
    expect(managementRouteLabel("/unknown", "Not Found")).toBe("Not Found");
    expect(managementBreadcrumbsForPath("/unknown")).toEqual([]);
  });
});
