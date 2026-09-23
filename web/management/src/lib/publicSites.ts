import {
  PublicSiteHostBehavior,
  PublicSiteListenerBehavior,
  PublicSiteReadinessSeverity,
  PublicSiteTlsCoverage,
  type PublicSite,
  type PublicSiteHost,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { asciiHostnamePattern } from "@/lib/idnHostnames";

export type SiteAliasForm = {
  id: string;
  hostnamePattern: string;
  behavior: PublicSiteHostBehavior;
};

export type SiteListenerBindingForm = {
  id: string;
  listenerId: string;
  behavior: PublicSiteListenerBehavior;
  redirectListenerId: string;
  redirectHostname: string;
};

export type PublicRouteSiteSelectionState = "legacy" | "site" | "unavailable";

export function publicRouteSiteSelectionState(
  siteId: string,
  listenerId: string,
  sites: PublicSite[],
): PublicRouteSiteSelectionState {
  if (siteId === "0") return "legacy";
  return sites.some(
    (site) =>
      site.id.toString() === siteId &&
      (site.listenerBindings?.some(
        (binding) => binding.listenerId.toString() === listenerId,
      ) ||
        site.listenerId.toString() === listenerId),
  )
    ? "site"
    : "unavailable";
}

export function publicRouteDefaultScopeLabel(
  state: PublicRouteSiteSelectionState,
): string {
  if (state === "site") return "One default per Site.";
  if (state === "legacy")
    return "One default per listener for listener-wide routes.";
  return "Choose an available scope before saving a default route.";
}

export function publicSitePrimary(
  site: PublicSite,
): PublicSiteHost | undefined {
  return site.hosts.find((host) => host.primary);
}

export function publicSiteAliases(site: PublicSite): PublicSiteHost[] {
  return site.hosts.filter((host) => !host.primary);
}

export function publicSiteTLSCoverage(site: PublicSite): PublicSiteTlsCoverage {
  const coverage = site.hosts.map((host) => host.tlsCoverage);
  if (coverage.includes(PublicSiteTlsCoverage.INVALID))
    return PublicSiteTlsCoverage.INVALID;
  if (coverage.includes(PublicSiteTlsCoverage.MISSING))
    return PublicSiteTlsCoverage.MISSING;
  if (
    coverage.length &&
    coverage.every((value) => value === PublicSiteTlsCoverage.COVERED)
  )
    return PublicSiteTlsCoverage.COVERED;
  if (
    coverage.length &&
    coverage.every((value) => value === PublicSiteTlsCoverage.NOT_APPLICABLE)
  )
    return PublicSiteTlsCoverage.NOT_APPLICABLE;
  return PublicSiteTlsCoverage.UNSPECIFIED;
}

export function publicSiteTLSLabel(coverage: PublicSiteTlsCoverage): string {
  switch (coverage) {
    case PublicSiteTlsCoverage.COVERED:
      return "TLS covered";
    case PublicSiteTlsCoverage.MISSING:
      return "TLS missing";
    case PublicSiteTlsCoverage.INVALID:
      return "TLS invalid";
    case PublicSiteTlsCoverage.NOT_APPLICABLE:
      return "HTTP";
    default:
      return "TLS unknown";
  }
}

export function publicSiteTLSDetail(site: PublicSite): string {
  const problem = site.hosts.find(
    (host) =>
      host.tlsCoverage === PublicSiteTlsCoverage.INVALID ||
      host.tlsCoverage === PublicSiteTlsCoverage.MISSING,
  );
  return problem
    ? `${problem.hostnamePattern}: ${problem.tlsDetail}`
    : (site.hosts[0]?.tlsDetail ?? "");
}

export function publicSiteValidationReason(input: {
  listenerId?: string;
  name: string;
  primaryHostname?: string;
  aliases: SiteAliasForm[];
  defaultSite?: boolean;
  canonicalHostname?: string;
  listenerBindings?: SiteListenerBindingForm[];
}): string {
  const bindings =
    input.listenerBindings ??
    (input.listenerId
      ? [
          {
            id: "legacy",
            listenerId: input.listenerId,
            behavior: PublicSiteListenerBehavior.SERVE,
            redirectListenerId: "",
            redirectHostname: "",
          },
        ]
      : []);
  if (!input.name.trim()) return "Enter a site name.";
  if (input.name.trim().length > 64)
    return "Site name must be 64 characters or fewer.";
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/u.test(input.name.trim())) {
    return "Site name must start with a letter or number and use only letters, numbers, dots, underscores, or hyphens.";
  }
  const primaryHostname = input.defaultSite
    ? ""
    : (input.primaryHostname ?? "");
  const aliases = input.defaultSite ? [] : input.aliases;
  if (primaryHostname) {
    const primaryError = hostnamePatternValidationReason(
      primaryHostname,
      false,
    );
    if (primaryError) return `Primary hostname: ${primaryError}`;
  }
  if (aliases.length + (primaryHostname ? 1 : 0) > 64)
    return "A site supports at most 64 hostnames.";
  const seen = new Set(
    primaryHostname ? [normalizedHostnameKey(primaryHostname)] : [],
  );
  for (const [index, alias] of aliases.entries()) {
    const aliasError = hostnamePatternValidationReason(
      alias.hostnamePattern,
      true,
    );
    if (aliasError) return `Alias ${index + 1}: ${aliasError}`;
    const key = normalizedHostnameKey(alias.hostnamePattern);
    if (seen.has(key))
      return `Hostname ${alias.hostnamePattern.trim()} is listed more than once.`;
    seen.add(key);
    if (
      alias.behavior !== PublicSiteHostBehavior.SERVE &&
      alias.behavior !== PublicSiteHostBehavior.REDIRECT
    ) {
      return `Alias ${index + 1}: choose Serve or Redirect.`;
    }
  }
  const canonicalHostname = input.defaultSite
    ? ""
    : (input.canonicalHostname?.trim() ?? "");
  if (canonicalHostname) {
    const canonicalError = hostnamePatternValidationReason(
      canonicalHostname,
      false,
    );
    if (canonicalError) return `Canonical hostname: ${canonicalError}`;
  }
  const bindingListeners = new Set<string>();
  for (const [index, binding] of bindings.entries()) {
    if (!binding.listenerId) continue;
    if (bindingListeners.has(binding.listenerId))
      return "Each listener can be assigned only once.";
    bindingListeners.add(binding.listenerId);
    if (
      binding.behavior !== PublicSiteListenerBehavior.SERVE &&
      binding.behavior !== PublicSiteListenerBehavior.REDIRECT_HTTPS
    ) {
      return `Listener assignment ${index + 1}: choose Serve Site or Redirect to HTTPS.`;
    }
    if (binding.behavior === PublicSiteListenerBehavior.REDIRECT_HTTPS) {
      if (
        binding.redirectListenerId &&
        binding.redirectListenerId === binding.listenerId
      )
        return `Listener assignment ${index + 1}: a listener cannot redirect to itself.`;
      if (binding.redirectHostname.trim()) {
        const redirectError = hostnamePatternValidationReason(
          binding.redirectHostname,
          false,
        );
        if (redirectError)
          return `Listener assignment ${index + 1} redirect hostname: ${redirectError}`;
      }
    }
  }
  return "";
}

export function publicSiteLifecycleLabel(site: PublicSite): string {
  if (!site.published) return "Draft";
  return site.enabled ? "Published" : "Published · disabled";
}

export function publicSiteReadinessLabel(site: PublicSite): string {
  if (
    site.readiness.some(
      (item) => item.severity === PublicSiteReadinessSeverity.BLOCKER,
    )
  )
    return "Needs attention";
  if (
    site.readiness.some(
      (item) => item.severity === PublicSiteReadinessSeverity.WARNING,
    )
  )
    return "Ready with warnings";
  if (
    site.readiness.some(
      (item) => item.severity === PublicSiteReadinessSeverity.UNKNOWN,
    )
  )
    return "Checks incomplete";
  return "Ready";
}

export function publicSiteListenerIDs(site: PublicSite): bigint[] {
  if (site.listenerBindings.length)
    return site.listenerBindings.map((binding) => binding.listenerId);
  return site.listenerId > 0n ? [site.listenerId] : [];
}

export function hostnamePatternValidationReason(
  value: string,
  allowWildcard: boolean,
): string {
  const hostname = value.trim();
  if (!hostname) return "Enter a hostname.";
  if (hostname !== value || /[\u0000-\u001f\u007f\s]/u.test(hostname))
    return "Do not include whitespace or control characters.";
  if (hostname.includes("://") || /[/?#@\\]/u.test(hostname))
    return "Enter only a hostname, without a scheme, path, credentials, query, or fragment.";
  if (hostname.includes(":") && !isIPLiteral(hostname))
    return "Enter only a hostname, without a port.";
  if (hostname.endsWith("..")) return "Use at most one trailing dot.";
  const wildcardCount = (hostname.match(/\*/gu) ?? []).length;
  if (!allowWildcard && wildcardCount)
    return "The primary hostname must be exact; wildcards are aliases only.";
  if (wildcardCount && (wildcardCount !== 1 || !hostname.startsWith("*.")))
    return "A wildcard must be the complete left-most label, such as *.example.com.";
  const withoutDot = hostname.endsWith(".") ? hostname.slice(0, -1) : hostname;
  const suffix = withoutDot.startsWith("*.") ? withoutDot.slice(2) : withoutDot;
  if (withoutDot.startsWith("*.") && suffix.split(".").length < 2)
    return "Wildcard aliases need a registrable suffix, such as *.example.com.";
  if (!suffix.includes(".") && !isIPLiteral(suffix))
    return "Use a fully qualified hostname with at least two labels.";
  return "";
}

function normalizedHostnameKey(value: string): string {
  return asciiHostnamePattern(value.trim().toLowerCase()).replace(/\.$/u, "");
}

function isIPLiteral(value: string): boolean {
  const octets = value.split(".");
  if (
    octets.length === 4 &&
    octets.every((octet) => /^\d{1,3}$/u.test(octet) && Number(octet) <= 255)
  )
    return true;
  if (!value.includes(":") || !/^[0-9a-f:.]+$/iu.test(value)) return false;
  try {
    return new URL(`http://[${value}]/`).hostname.startsWith("[");
  } catch {
    return false;
  }
}
