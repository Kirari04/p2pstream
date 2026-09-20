import {
  PublicSiteHostBehavior,
  PublicSiteTlsCoverage,
  type PublicSite,
  type PublicSiteHost,
} from "@/gen/proto/p2pstream/v1/management_pb";

export type SiteAliasForm = {
  id: string;
  hostnamePattern: string;
  behavior: PublicSiteHostBehavior;
};

export type PublicRouteSiteSelectionState = "legacy" | "site" | "unavailable";

export function publicRouteSiteSelectionState(siteId: string, listenerId: string, sites: PublicSite[]): PublicRouteSiteSelectionState {
  if (siteId === "0") return "legacy";
  return sites.some((site) => site.id.toString() === siteId && site.listenerId.toString() === listenerId) ? "site" : "unavailable";
}

export function publicRouteDefaultScopeLabel(state: PublicRouteSiteSelectionState): string {
  if (state === "site") return "One default per Site.";
  if (state === "legacy") return "One default per listener for Legacy rules.";
  return "Choose an available scope before saving a default route.";
}

export function publicSitePrimary(site: PublicSite): PublicSiteHost | undefined {
  return site.hosts.find((host) => host.primary);
}

export function publicSiteAliases(site: PublicSite): PublicSiteHost[] {
  return site.hosts.filter((host) => !host.primary);
}

export function publicSiteTLSCoverage(site: PublicSite): PublicSiteTlsCoverage {
  const coverage = site.hosts.map((host) => host.tlsCoverage);
  if (coverage.includes(PublicSiteTlsCoverage.INVALID)) return PublicSiteTlsCoverage.INVALID;
  if (coverage.includes(PublicSiteTlsCoverage.MISSING)) return PublicSiteTlsCoverage.MISSING;
  if (coverage.length && coverage.every((value) => value === PublicSiteTlsCoverage.COVERED)) return PublicSiteTlsCoverage.COVERED;
  if (coverage.length && coverage.every((value) => value === PublicSiteTlsCoverage.NOT_APPLICABLE)) return PublicSiteTlsCoverage.NOT_APPLICABLE;
  return PublicSiteTlsCoverage.UNSPECIFIED;
}

export function publicSiteTLSLabel(coverage: PublicSiteTlsCoverage): string {
  switch (coverage) {
    case PublicSiteTlsCoverage.COVERED: return "TLS covered";
    case PublicSiteTlsCoverage.MISSING: return "TLS missing";
    case PublicSiteTlsCoverage.INVALID: return "TLS invalid";
    case PublicSiteTlsCoverage.NOT_APPLICABLE: return "HTTP";
    default: return "TLS unknown";
  }
}

export function publicSiteTLSDetail(site: PublicSite): string {
  const problem = site.hosts.find((host) => (
    host.tlsCoverage === PublicSiteTlsCoverage.INVALID || host.tlsCoverage === PublicSiteTlsCoverage.MISSING
  ));
  return problem ? `${problem.hostnamePattern}: ${problem.tlsDetail}` : site.hosts[0]?.tlsDetail ?? "";
}

export function publicSiteValidationReason(input: {
  listenerId: string;
  name: string;
  primaryHostname: string;
  aliases: SiteAliasForm[];
}): string {
  if (!input.listenerId) return "Choose a listener.";
  if (!input.name.trim()) return "Enter a site name.";
  if (input.name.trim().length > 64) return "Site name must be 64 characters or fewer.";
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/u.test(input.name.trim())) {
    return "Site name must start with a letter or number and use only letters, numbers, dots, underscores, or hyphens.";
  }
  const primaryError = hostnamePatternValidationReason(input.primaryHostname, false);
  if (primaryError) return `Primary hostname: ${primaryError}`;
  if (input.aliases.length > 63) return "A site supports at most 63 aliases.";
  const seen = new Set([normalizedHostnameKey(input.primaryHostname)]);
  for (const [index, alias] of input.aliases.entries()) {
    const aliasError = hostnamePatternValidationReason(alias.hostnamePattern, true);
    if (aliasError) return `Alias ${index + 1}: ${aliasError}`;
    const key = normalizedHostnameKey(alias.hostnamePattern);
    if (seen.has(key)) return `Hostname ${alias.hostnamePattern.trim()} is listed more than once.`;
    seen.add(key);
    if (alias.behavior !== PublicSiteHostBehavior.SERVE && alias.behavior !== PublicSiteHostBehavior.REDIRECT) {
      return `Alias ${index + 1}: choose Serve or Redirect.`;
    }
  }
  return "";
}

export function hostnamePatternValidationReason(value: string, allowWildcard: boolean): string {
  const hostname = value.trim();
  if (!hostname) return "Enter a hostname.";
  if (hostname !== value || /[\u0000-\u001f\u007f\s]/u.test(hostname)) return "Do not include whitespace or control characters.";
  if (hostname.includes("://") || /[/?#@]/u.test(hostname)) return "Enter only a hostname, without a scheme, path, credentials, query, or fragment.";
  if (hostname.includes(":") && !isIPLiteral(hostname)) return "Enter only a hostname, without a port.";
  if (hostname.endsWith("..")) return "Use at most one trailing dot.";
  const wildcardCount = (hostname.match(/\*/gu) ?? []).length;
  if (!allowWildcard && wildcardCount) return "The primary hostname must be exact; wildcards are aliases only.";
  if (wildcardCount && (wildcardCount !== 1 || !hostname.startsWith("*."))) return "A wildcard must be the complete left-most label, such as *.example.com.";
  const withoutDot = hostname.endsWith(".") ? hostname.slice(0, -1) : hostname;
  const suffix = withoutDot.startsWith("*.") ? withoutDot.slice(2) : withoutDot;
  if (withoutDot.startsWith("*.") && suffix.split(".").length < 2) return "Wildcard aliases need a registrable suffix, such as *.example.com.";
  if (!suffix.includes(".") && !isIPLiteral(suffix)) return "Use a fully qualified hostname with at least two labels.";
  return "";
}

function normalizedHostnameKey(value: string): string {
  return value.trim().toLowerCase().replace(/\.$/u, "");
}

function isIPLiteral(value: string): boolean {
  const octets = value.split(".");
  if (octets.length === 4 && octets.every((octet) => /^\d{1,3}$/u.test(octet) && Number(octet) <= 255)) return true;
  if (!value.includes(":")) return false;
  try {
    return new URL(`http://[${value}]/`).hostname.startsWith("[");
  } catch {
    return false;
  }
}
