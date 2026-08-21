export type ManagementNavigationItem = Readonly<{
  key: string;
  label: string;
  path: string;
  activePrefix?: string;
  children?: readonly ManagementNavigationItem[];
}>;

export type ManagementNavigationGroup = Readonly<{
  key: string;
  label: string;
  items: readonly ManagementNavigationItem[];
}>;

export type ManagementBreadcrumb = Readonly<{
  key: string;
  label: string;
  path?: string;
}>;

/**
 * The management information architecture. Paths intentionally mirror the
 * existing hash-router routes so the shell can change without route aliases or
 * backend changes.
 */
export const MANAGEMENT_NAVIGATION: readonly ManagementNavigationGroup[] = [
  {
    key: "observe",
    label: "Observe",
    items: [
      { key: "overview", label: "Overview", path: "/overview" },
      {
        key: "monitor",
        label: "Monitor",
        path: "/monitor/traffic",
        activePrefix: "/monitor",
        children: [
          { key: "monitor-traffic", label: "Traffic", path: "/monitor/traffic" },
          { key: "monitor-diagnostics", label: "Diagnostics", path: "/monitor/diagnostics" },
        ],
      },
    ],
  },
  {
    key: "configure",
    label: "Configure",
    items: [
      {
        key: "proxy",
        label: "Proxy",
        path: "/proxy/routes",
        activePrefix: "/proxy",
        children: [
          { key: "proxy-routes", label: "Routes", path: "/proxy/routes" },
          { key: "proxy-listeners", label: "Listeners", path: "/proxy/listeners" },
        ],
      },
      { key: "agents", label: "Agents", path: "/agent", activePrefix: "/agent" },
      {
        key: "traffic-policy",
        label: "Traffic Policy",
        path: "/policies/rate-limits",
        activePrefix: "/policies",
        children: [
          { key: "policy-rate-limits", label: "Rate Limits", path: "/policies/rate-limits" },
          { key: "policy-waf", label: "WAF", path: "/policies/waf" },
          { key: "policy-access", label: "Access", path: "/policies/access" },
          { key: "policy-cache", label: "Cache", path: "/policies/cache" },
          { key: "policy-retries", label: "Retries", path: "/policies/retries" },
          { key: "policy-traffic-shaper", label: "Traffic Shaper", path: "/policies/traffic-shaper" },
        ],
      },
      { key: "templates", label: "Templates", path: "/templates" },
      { key: "tls", label: "TLS", path: "/tls" },
    ],
  },
  {
    key: "system",
    label: "System",
    items: [
      { key: "environments", label: "Environments", path: "/settings/environments" },
      { key: "api-tokens", label: "API Tokens", path: "/settings/api-tokens" },
      { key: "management-tls", label: "Management TLS", path: "/settings/management-tls" },
    ],
  },
] as const;

/** Normalize route.path values as well as hash-history hrefs for pure tests. */
export function normalizeManagementPath(value: string): string {
  let path = value.trim();
  const routeHashIndex = path.indexOf("#/");
  if (routeHashIndex >= 0) {
    path = path.slice(routeHashIndex + 1);
  }
  path = path.split(/[?#]/, 1)[0] ?? "";
  if (!path.startsWith("/")) path = `/${path}`;
  path = path.replace(/\/{2,}/g, "/");
  if (path.length > 1) path = path.replace(/\/+$/, "");
  return path || "/";
}

export function isManagementNavigationItemActive(
  item: ManagementNavigationItem,
  currentPath: string,
): boolean {
  const path = normalizeManagementPath(currentPath);
  if (path === normalizeManagementPath(item.path)) return true;
  if (item.children?.some((child) => isManagementNavigationItemActive(child, path))) return true;
  if (!item.activePrefix) return false;
  const prefix = normalizeManagementPath(item.activePrefix);
  return path === prefix || path.startsWith(`${prefix}/`);
}

export function managementBreadcrumbsForPath(currentPath: string): ManagementBreadcrumb[] {
  const path = normalizeManagementPath(currentPath);
  for (const group of MANAGEMENT_NAVIGATION) {
    const itemTrail = findItemTrail(group.items, path);
    if (itemTrail) {
      return [
        { key: group.key, label: group.label },
        ...itemTrail.map((item) => ({ key: item.key, label: item.label, path: item.path })),
      ];
    }
  }
  return [];
}

export function managementRouteLabel(currentPath: string, fallback = "Management"): string {
  const breadcrumbs = managementBreadcrumbsForPath(currentPath);
  return breadcrumbs.at(-1)?.label ?? fallback;
}

function findItemTrail(
  items: readonly ManagementNavigationItem[],
  currentPath: string,
): readonly ManagementNavigationItem[] | null {
  for (const item of items) {
    const childTrail = item.children ? findItemTrail(item.children, currentPath) : null;
    if (childTrail) return [item, ...childTrail];
    if (isManagementNavigationItemActive(item, currentPath)) return [item];
  }
  return null;
}
