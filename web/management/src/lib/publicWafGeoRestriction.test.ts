import { describe, expect, test } from "bun:test";
import {
  PublicWafGeoRestrictionMode,
  PublicWafGeoUnknownBehavior,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  defaultWafGeoRestrictionForm,
  wafGeoRestrictionFormFromProto,
  wafGeoRestrictionPayloadFromForm,
  wafGeoRestrictionSummary,
  wafGeoRestrictionValidationReason,
} from "@/lib/publicWafGeoRestriction";

describe("publicWafGeoRestriction", () => {
  test("defaults to unrestricted and fail-closed unknown handling", () => {
    expect(defaultWafGeoRestrictionForm()).toEqual({
      mode: PublicWafGeoRestrictionMode.DISABLED,
      countryCodes: [],
      unknownBehavior: PublicWafGeoUnknownBehavior.APPLY_RULE,
    });
  });

  test("normalizes a persisted restriction without erasing server-valid future codes", () => {
    expect(wafGeoRestrictionFormFromProto({
      mode: PublicWafGeoRestrictionMode.SELECTED_COUNTRIES,
      countryCodes: [" ch ", "US", "ch", "zz"],
      unknownBehavior: PublicWafGeoUnknownBehavior.BYPASS_RULE,
    })).toEqual({
      mode: PublicWafGeoRestrictionMode.SELECTED_COUNTRIES,
      countryCodes: ["CH", "US", "ZZ"],
      unknownBehavior: PublicWafGeoUnknownBehavior.BYPASS_RULE,
    });
  });

  test("clears stale countries when geo targeting is disabled", () => {
    const payload = wafGeoRestrictionPayloadFromForm({
      mode: PublicWafGeoRestrictionMode.DISABLED,
      countryCodes: ["CH", "US"],
      unknownBehavior: PublicWafGeoUnknownBehavior.BYPASS_RULE,
    });

    expect(payload.countryCodes).toEqual([]);
    expect(payload.unknownBehavior).toBe(PublicWafGeoUnknownBehavior.BYPASS_RULE);
  });

  test("requires a country for either active mode", () => {
    expect(wafGeoRestrictionValidationReason({
      mode: PublicWafGeoRestrictionMode.OUTSIDE_SELECTED_COUNTRIES,
      countryCodes: [],
      unknownBehavior: PublicWafGeoUnknownBehavior.APPLY_RULE,
    })).toBe("Select at least one country.");
  });

  test("rejects malformed values instead of silently changing the submitted rule", () => {
    expect(wafGeoRestrictionValidationReason({
      mode: PublicWafGeoRestrictionMode.SELECTED_COUNTRIES,
      countryCodes: ["CH", "USA"],
      unknownBehavior: PublicWafGeoUnknownBehavior.APPLY_RULE,
    })).toBe("Enter a valid two-letter country code instead of USA.");
  });

  test("summarizes mode, count, and intentional fail-open behavior", () => {
    expect(wafGeoRestrictionSummary({
      mode: PublicWafGeoRestrictionMode.OUTSIDE_SELECTED_COUNTRIES,
      countryCodes: ["CH", "DE"],
      unknownBehavior: PublicWafGeoUnknownBehavior.BYPASS_RULE,
    })).toBe("Outside 2 countries · unknown bypasses");
  });
});
