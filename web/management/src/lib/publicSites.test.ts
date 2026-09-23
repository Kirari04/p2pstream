import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import {
  PublicSiteHostBehavior,
  PublicSiteListenerBehavior,
  PublicSiteReadinessSeverity,
  PublicSiteTlsCoverage,
  UpdatePublicRouteRequestSchema,
  type PublicSite,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  publicRouteDefaultScopeLabel,
  publicRouteSiteSelectionState,
  publicSiteTLSCoverage,
  publicSiteLifecycleLabel,
  publicSiteReadinessLabel,
  publicSiteValidationReason,
} from "@/lib/publicSites";

describe("public sites", () => {
  test("requires one exact primary and unique, one-label wildcard aliases", () => {
    const base = {
      listenerId: "1",
      name: "App",
      primaryHostname: "app.example.com",
      aliases: [],
    };
    expect(publicSiteValidationReason(base)).toBe("");
    expect(
      publicSiteValidationReason({ ...base, name: "Customer portal" }),
    ).toContain("only letters");
    expect(
      publicSiteValidationReason({ ...base, primaryHostname: "*.example.com" }),
    ).toContain("must be exact");
    expect(
      publicSiteValidationReason({
        ...base,
        primaryHostname: "app.example.com:443",
      }),
    ).toContain("without a port");
    expect(
      publicSiteValidationReason({ ...base, primaryHostname: "dead.beef:443" }),
    ).toContain("without a port");
    expect(
      publicSiteValidationReason({ ...base, primaryHostname: "2001:db8::1" }),
    ).toBe("");
    expect(
      publicSiteValidationReason({
        ...base,
        primaryHostname: "app\\example.com",
      }),
    ).not.toBe("");
    expect(
      publicSiteValidationReason({ ...base, primaryHostname: "::1]\\example" }),
    ).not.toBe("");
    expect(
      publicSiteValidationReason({ ...base, primaryHostname: "::1]:443\\" }),
    ).not.toBe("");
    expect(
      publicSiteValidationReason({
        ...base,
        aliases: [
          {
            id: "1",
            hostnamePattern: "*.example.com",
            behavior: PublicSiteHostBehavior.SERVE,
          },
        ],
      }),
    ).toBe("");
    expect(
      publicSiteValidationReason({
        ...base,
        aliases: [
          {
            id: "1",
            hostnamePattern: "APP.EXAMPLE.COM.",
            behavior: PublicSiteHostBehavior.REDIRECT,
          },
        ],
      }),
    ).toContain("listed more than once");
    expect(
      publicSiteValidationReason({
        ...base,
        primaryHostname: "züribadi.ch",
        aliases: [
          {
            id: "idn-duplicate",
            hostnamePattern: "xn--zribadi-n2a.ch",
            behavior: PublicSiteHostBehavior.SERVE,
          },
        ],
      }),
    ).toContain("listed more than once");
    expect(
      publicSiteValidationReason({
        ...base,
        primaryHostname: "züribadi.ch。",
        aliases: [
          {
            id: "idn-dot-duplicate",
            hostnamePattern: "xn--zribadi-n2a.ch",
            behavior: PublicSiteHostBehavior.SERVE,
          },
        ],
      }),
    ).toContain("listed more than once");
  });

  test("surfaces the most severe TLS state", () => {
    const site = {
      hosts: [
        { tlsCoverage: PublicSiteTlsCoverage.COVERED },
        { tlsCoverage: PublicSiteTlsCoverage.MISSING },
        { tlsCoverage: PublicSiteTlsCoverage.INVALID },
      ],
    } as PublicSite;
    expect(publicSiteTLSCoverage(site)).toBe(PublicSiteTlsCoverage.INVALID);
  });

  test("allows incomplete drafts while validating any values that are present", () => {
    expect(
      publicSiteValidationReason({
        name: "draft-site",
        aliases: [],
        listenerBindings: [],
      }),
    ).toBe("");
    expect(
      publicSiteValidationReason({
        name: "draft-site",
        aliases: [
          {
            id: "hidden",
            hostnamePattern: "",
            behavior: PublicSiteHostBehavior.SERVE,
          },
        ],
        defaultSite: true,
        canonicalHostname: "also hidden and invalid",
        listenerBindings: [
          {
            id: "one",
            listenerId: "1",
            behavior: PublicSiteListenerBehavior.REDIRECT_HTTPS,
            redirectListenerId: "",
            redirectHostname: "",
          },
        ],
      }),
    ).toBe("");
    expect(
      publicSiteValidationReason({
        name: "draft-site",
        aliases: [],
        listenerBindings: [
          {
            id: "one",
            listenerId: "1",
            behavior: PublicSiteListenerBehavior.SERVE,
            redirectListenerId: "",
            redirectHostname: "",
          },
          {
            id: "two",
            listenerId: "1",
            behavior: PublicSiteListenerBehavior.SERVE,
            redirectListenerId: "",
            redirectHostname: "",
          },
        ],
      }),
    ).toContain("only once");
  });

  test("keeps lifecycle separate from configuration readiness", () => {
    const draft = {
      published: false,
      enabled: true,
      readiness: [{ severity: PublicSiteReadinessSeverity.BLOCKER }],
    } as PublicSite;
    expect(publicSiteLifecycleLabel(draft)).toBe("Draft");
    expect(publicSiteReadinessLabel(draft)).toBe("Needs attention");
    const disabled = {
      published: true,
      enabled: false,
      readiness: [],
    } as unknown as PublicSite;
    expect(publicSiteLifecycleLabel(disabled)).toBe("Published · disabled");
    expect(publicSiteReadinessLabel(disabled)).toBe("Ready");
  });

  test("keeps legacy detach distinct from an old-client omitted site field", () => {
    const preserve = create(UpdatePublicRouteRequestSchema, {});
    const detach = create(UpdatePublicRouteRequestSchema, { siteId: 0n });
    expect(preserve.siteId).toBeUndefined();
    expect(detach.siteId).toBe(0n);
  });

  test("never turns an unavailable Site into an implicit legacy scope", () => {
    const sites = [{ id: 7n, listenerId: 1n }] as PublicSite[];
    expect(publicRouteSiteSelectionState("7", "1", sites)).toBe("site");
    expect(publicRouteSiteSelectionState("7", "2", sites)).toBe("unavailable");
    expect(publicRouteSiteSelectionState("7", "1", [])).toBe("unavailable");
    expect(publicRouteSiteSelectionState("0", "2", sites)).toBe("legacy");
    expect(publicRouteDefaultScopeLabel("site")).toBe("One default per Site.");
    expect(publicRouteDefaultScopeLabel("legacy")).toContain("per listener");
    expect(publicRouteDefaultScopeLabel("unavailable")).toContain(
      "before saving a default route",
    );
  });
});
