import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import {
  PublicSiteHostBehavior,
  PublicSiteListenerBehavior,
  PublicSiteReadinessSeverity,
  PublicSiteTlsCoverage,
  PublicSiteSchema,
  UpdatePublicRouteRequestSchema,
  type PublicSite,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  emptyPublicSiteFilter,
  filterPublicSites,
  isPublicSiteFilterActive,
  publicSiteLifecycleState,
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

  test("filters sites by search text, lifecycle, readiness, and listener", () => {
    const listenerNames = new Map([
      ["1", "edge-http"],
      ["2", "Edge-TLS"],
    ]);
    const published = create(PublicSiteSchema, {
      id: 1n,
      name: "shop",
      published: true,
      enabled: true,
      canonicalHostname: "xn--zribadi-n2a.example",
      hosts: [{ hostnamePattern: "xn--zribadi-n2a.example" }],
      listenerBindings: [{ listenerId: 2n }],
    });
    const draft = create(PublicSiteSchema, {
      id: 2n,
      name: "migrated-3-xn--bcher-kva.example",
      published: false,
      enabled: true,
      readiness: [{ severity: PublicSiteReadinessSeverity.BLOCKER }],
      listenerId: 1n,
    });
    const disabled = create(PublicSiteSchema, {
      id: 3n,
      name: "Legacy-API",
      published: true,
      enabled: false,
      readiness: [{ severity: PublicSiteReadinessSeverity.WARNING }],
      hosts: [{ hostnamePattern: "*.api.example.com" }],
      listenerBindings: [{ listenerId: 1n }, { listenerId: 2n }],
    });
    const sites = [published, draft, disabled];
    const ids = (filter: Partial<ReturnType<typeof emptyPublicSiteFilter>>) =>
      filterPublicSites(
        sites,
        { ...emptyPublicSiteFilter(), ...filter },
        listenerNames,
      ).map((site) => site.id);

    expect(ids({})).toEqual([1n, 2n, 3n]);
    expect(isPublicSiteFilterActive(emptyPublicSiteFilter())).toBe(false);
    expect(
      isPublicSiteFilterActive({ ...emptyPublicSiteFilter(), text: "  " }),
    ).toBe(false);
    expect(
      isPublicSiteFilterActive({ ...emptyPublicSiteFilter(), listenerId: "1" }),
    ).toBe(true);

    // Name, case-insensitive, and the decoded migrated-site name.
    expect(ids({ text: "legacy-api" })).toEqual([3n]);
    expect(ids({ text: "BÜCHER" })).toEqual([2n]);
    // Hostnames match in both the stored ASCII form and the Unicode display form.
    expect(ids({ text: "xn--zribadi" })).toEqual([1n]);
    expect(ids({ text: "züribadi" })).toEqual([1n]);
    expect(ids({ text: "*.api." })).toEqual([3n]);
    // Assigned listener names, including the legacy single-listener field.
    expect(ids({ text: "edge-tls" })).toEqual([1n, 3n]);
    expect(ids({ text: "edge-http" })).toEqual([2n, 3n]);
    expect(ids({ text: "missing" })).toEqual([]);

    expect(publicSiteLifecycleState(draft)).toBe("draft");
    expect(ids({ state: "published" })).toEqual([1n]);
    expect(ids({ state: "draft" })).toEqual([2n]);
    expect(ids({ state: "disabled" })).toEqual([3n]);

    // Only blockers need attention; warnings still count as ready, like the Readiness column.
    expect(ids({ readiness: "attention" })).toEqual([2n]);
    expect(ids({ readiness: "ready" })).toEqual([1n, 3n]);

    expect(ids({ listenerId: "1" })).toEqual([2n, 3n]);
    expect(ids({ listenerId: "2", state: "published" })).toEqual([1n]);
    expect(ids({ listenerId: "9" })).toEqual([]);
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
    expect(publicRouteDefaultScopeLabel("site")).toBe("One default per site.");
    expect(publicRouteDefaultScopeLabel("legacy")).toContain("per listener");
    expect(publicRouteDefaultScopeLabel("unavailable")).toContain(
      "before saving a default route",
    );
  });
});
