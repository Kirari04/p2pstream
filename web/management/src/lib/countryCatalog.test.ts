import { describe, expect, test } from "bun:test";
import {
  ISO_COUNTRY_CODES,
  countryName,
  countryOptions,
  invalidCountryCodes,
  isGeoCountryCode,
  isIsoCountryCode,
  normalizeCountryCodes,
} from "@/lib/countryCatalog";

describe("countryCatalog", () => {
  test("contains every ISO assignment and MaxMind's XK assignment without duplicates", () => {
    expect(ISO_COUNTRY_CODES).toHaveLength(250);
    expect(new Set(ISO_COUNTRY_CODES).size).toBe(250);
    expect(ISO_COUNTRY_CODES).toContain("XK");
    expect(ISO_COUNTRY_CODES.every((code) => /^[A-Z]{2}$/.test(code))).toBe(true);
  });

  test("normalizes, deduplicates, sorts, and preserves server-valid future assignments", () => {
    expect(normalizeCountryCodes([" ch ", "US", "ch", "zz", "de", "USA"])).toEqual(["CH", "DE", "US", "ZZ"]);
    expect(invalidCountryCodes([" ch ", "zz", "USA", "USA", ""])).toEqual(["", "USA"]);
  });

  test("recognizes country codes case-insensitively", () => {
    expect(isIsoCountryCode("ch")).toBe(true);
    expect(isIsoCountryCode("ZZ")).toBe(false);
    expect(isGeoCountryCode("ZZ")).toBe(true);
    expect(isGeoCountryCode("USA")).toBe(false);
  });

  test("builds searchable labels with stable code values", () => {
    const switzerland = countryOptions("en").find((option) => option.value === "CH");

    expect(switzerland?.label).toContain("Switzerland");
    expect(switzerland?.label).toContain("CH");
    expect(countryName("not-a-code", "en")).toBe("NOT-A-CODE");
  });
});
