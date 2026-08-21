import { describe, expect, test } from "bun:test";
import { PublicTrustedProxyHeaderMode } from "@/gen/proto/p2pstream/v1/management_pb";
import {
  customTrustedProxyPayloadFromForm,
  customTrustedProxyValidationReason,
  defaultCustomTrustedProxyForm,
  geoIpSettingsFormFromProto,
  geoIpSettingsPayloadFromForm,
  geoIpSettingsValidationReason,
  isValidCidr,
  trustedProxyCidrsCoverAddressFamily,
  trustedProxyCidrsFromText,
} from "@/lib/publicVisitorIdentityForm";

describe("publicVisitorIdentityForm", () => {
  test("never places an existing MaxMind key back into the form", () => {
    const form = geoIpSettingsFormFromProto({
      $typeName: "p2pstream.v1.PublicGeoIpSettings",
      enabled: true,
      maxmindAccountId: "12345",
      maxmindLicenseKeySet: true,
      createdAtUnixMillis: 0n,
      updatedAtUnixMillis: 0n,
    });

    expect(form.maxmindLicenseKey).toBe("");
    expect(form.licenseKeySaved).toBe(true);
  });

  test("requires credentials before GeoIP can be enabled", () => {
    const form = geoIpSettingsFormFromProto();
    form.enabled = true;
    expect(geoIpSettingsValidationReason(form)).toBe("Enter the MaxMind account ID.");
    form.maxmindAccountId = " 12345 ";
    expect(geoIpSettingsValidationReason(form)).toBe("Enter a MaxMind license key.");
    form.maxmindLicenseKey = " secret ";
    expect(geoIpSettingsValidationReason(form)).toBe("");
    expect(geoIpSettingsPayloadFromForm(form)).toEqual({
      enabled: true,
      maxmindAccountId: "12345",
      maxmindLicenseKey: "secret",
      clearLicenseKey: false,
    });
  });

  test("matches MaxMind input bounds before submitting", () => {
    const form = geoIpSettingsFormFromProto();
    form.maxmindAccountId = "account-name";
    expect(geoIpSettingsValidationReason(form)).toBe("Enter the numeric MaxMind account ID.");
    form.maxmindAccountId = "1".repeat(65);
    expect(geoIpSettingsValidationReason(form)).toBe("Use a MaxMind account ID no longer than 64 digits.");
    form.maxmindAccountId = "12345";
    form.maxmindLicenseKey = "x".repeat(257);
    expect(geoIpSettingsValidationReason(form)).toBe("Use a MaxMind license key no longer than 256 characters.");
    form.maxmindLicenseKey = "clé";
    expect(geoIpSettingsValidationReason(form)).toBe("Use only visible ASCII characters in the MaxMind license key.");
  });

  test("uses an explicit clear flag without sending placeholder secret text", () => {
    const form = geoIpSettingsFormFromProto();
    form.maxmindAccountId = "12345";
    form.maxmindLicenseKey = "must-not-leak";
    form.clearLicenseKey = true;

    expect(geoIpSettingsPayloadFromForm(form).maxmindLicenseKey).toBe("");
    expect(geoIpSettingsPayloadFromForm(form).clearLicenseKey).toBe(true);
  });

  test("parses comma and newline separated CIDRs without changing their text", () => {
    expect(trustedProxyCidrsFromText(" 192.0.2.0/24, 2001:db8::/32\n192.0.2.0/24 ")).toEqual([
      "192.0.2.0/24",
      "2001:db8::/32",
    ]);
  });

  test("validates IPv4, IPv6, and IPv4-mapped CIDRs", () => {
    expect(isValidCidr("192.0.2.0/24")).toBe(true);
    expect(isValidCidr("2001:db8::/32")).toBe(true);
    expect(isValidCidr("::ffff:192.0.2.0/120")).toBe(true);
    expect(isValidCidr("192.0.2.1/33")).toBe(false);
    expect(isValidCidr("2001:db8::/129")).toBe(false);
    expect(isValidCidr("010.0.0.0/8")).toBe(false);
    expect(isValidCidr("proxy.example/24")).toBe(false);
  });

  test("builds a normalized custom trusted-proxy payload", () => {
    const form = defaultCustomTrustedProxyForm();
    form.name = " office-ingress ";
    form.enabled = true;
    form.cidrsText = "192.0.2.0/24\n2001:db8::/32";
    form.headerName = " X-Forwarded-For ";

    expect(customTrustedProxyValidationReason(form)).toBe("");
    expect(customTrustedProxyPayloadFromForm(form)).toEqual({
      name: "office-ingress",
      enabled: true,
      cidrs: ["192.0.2.0/24", "2001:db8::/32"],
      headerName: "X-Forwarded-For",
      headerMode: PublicTrustedProxyHeaderMode.TRUSTED_CHAIN,
    });
  });

  test("allows empty disabled staging but rejects unsafe enabled proxy ranges", () => {
    const form = defaultCustomTrustedProxyForm();
    form.name = "office-ingress";
    form.cidrsText = "";
    expect(customTrustedProxyValidationReason(form)).toBe("");

    form.enabled = true;
    expect(customTrustedProxyValidationReason(form)).toBe("Add at least one trusted peer CIDR before enabling trust.");
    form.cidrsText = "0.0.0.0/0";
    expect(customTrustedProxyValidationReason(form)).toBe("Replace 0.0.0.0/0; default routes cannot be trusted.");
    form.cidrsText = "::/0";
    expect(customTrustedProxyValidationReason(form)).toBe("Replace ::/0; default routes cannot be trusted.");
    form.name = "office ingress";
    expect(customTrustedProxyValidationReason(form)).toContain("letters, numbers");
  });

  test("rejects CIDR sets that collectively trust an entire address family", () => {
    expect(trustedProxyCidrsCoverAddressFamily(["0.0.0.0/1", "128.0.0.0/1"])).toBe(true);
    expect(trustedProxyCidrsCoverAddressFamily(["0.0.0.0/2", "64.0.0.0/2", "128.0.0.0/2", "192.0.0.0/2"])).toBe(true);
    expect(trustedProxyCidrsCoverAddressFamily(["::/1", "8000::/1"])).toBe(true);
    expect(trustedProxyCidrsCoverAddressFamily(["::ffff:0.0.0.0/97", "::ffff:128.0.0.0/97"])).toBe(true);
    expect(trustedProxyCidrsCoverAddressFamily(["10.0.0.0/8", "192.168.0.0/16"])).toBe(false);

    const form = defaultCustomTrustedProxyForm();
    form.name = "catch-all";
    form.enabled = true;
    form.cidrsText = "0.0.0.0/1\n128.0.0.0/1";
    expect(customTrustedProxyValidationReason(form)).toContain("cannot cover every IPv4 or IPv6 address");
  });
});
