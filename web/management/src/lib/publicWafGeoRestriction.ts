import { invalidCountryCodes, normalizeCountryCodes } from "@/lib/countryCatalog";
import {
  PublicWafGeoRestrictionMode,
  PublicWafGeoUnknownBehavior,
  type PublicWafGeoRestriction,
} from "@/gen/proto/p2pstream/v1/management_pb";

export type WafGeoRestrictionForm = {
  mode: PublicWafGeoRestrictionMode;
  countryCodes: string[];
  unknownBehavior: PublicWafGeoUnknownBehavior;
};

export type WafGeoRestrictionPayload = {
  mode: PublicWafGeoRestrictionMode;
  countryCodes: string[];
  unknownBehavior: PublicWafGeoUnknownBehavior;
};

export function defaultWafGeoRestrictionForm(): WafGeoRestrictionForm {
  return {
    mode: PublicWafGeoRestrictionMode.DISABLED,
    countryCodes: [],
    unknownBehavior: PublicWafGeoUnknownBehavior.APPLY_RULE,
  };
}

export function wafGeoRestrictionFormFromProto(
  restriction?: Partial<PublicWafGeoRestriction>,
): WafGeoRestrictionForm {
  if (!restriction) return defaultWafGeoRestrictionForm();
  return {
    mode: normalizedMode(restriction.mode),
    countryCodes: normalizeCountryCodes(restriction.countryCodes ?? []),
    unknownBehavior: restriction.unknownBehavior === PublicWafGeoUnknownBehavior.BYPASS_RULE
      ? PublicWafGeoUnknownBehavior.BYPASS_RULE
      : PublicWafGeoUnknownBehavior.APPLY_RULE,
  };
}

export function wafGeoRestrictionPayloadFromForm(
  form: WafGeoRestrictionForm,
): WafGeoRestrictionPayload {
  const mode = normalizedMode(form.mode);
  return {
    mode,
    countryCodes: mode === PublicWafGeoRestrictionMode.DISABLED
      ? []
      : normalizeCountryCodes(form.countryCodes),
    unknownBehavior: form.unknownBehavior === PublicWafGeoUnknownBehavior.BYPASS_RULE
      ? PublicWafGeoUnknownBehavior.BYPASS_RULE
      : PublicWafGeoUnknownBehavior.APPLY_RULE,
  };
}

export function wafGeoRestrictionValidationReason(form: WafGeoRestrictionForm): string {
  if (normalizedMode(form.mode) === PublicWafGeoRestrictionMode.DISABLED) return "";
  const invalidCodes = invalidCountryCodes(form.countryCodes);
  if (invalidCodes.length) return `Enter a valid two-letter country code instead of ${invalidCodes[0]}.`;
  const countryCodes = normalizeCountryCodes(form.countryCodes);
  if (!countryCodes.length) return "Select at least one country.";
  if (countryCodes.length > 256) return "Select no more than 256 countries.";
  return "";
}

export function wafGeoRestrictionSummary(form?: Partial<WafGeoRestrictionForm>): string {
  const mode = normalizedMode(form?.mode);
  if (mode === PublicWafGeoRestrictionMode.DISABLED) return "All locations";
  const count = normalizeCountryCodes(form?.countryCodes ?? []).length;
  const locationSummary = mode === PublicWafGeoRestrictionMode.OUTSIDE_SELECTED_COUNTRIES
    ? `Outside ${countryCountLabel(count)}`
    : countryCountLabel(count);
  return form?.unknownBehavior === PublicWafGeoUnknownBehavior.BYPASS_RULE
    ? `${locationSummary} · unknown bypasses`
    : `${locationSummary} · unknown applies`;
}

function normalizedMode(mode?: PublicWafGeoRestrictionMode): PublicWafGeoRestrictionMode {
  if (mode === PublicWafGeoRestrictionMode.SELECTED_COUNTRIES) return mode;
  if (mode === PublicWafGeoRestrictionMode.OUTSIDE_SELECTED_COUNTRIES) return mode;
  return PublicWafGeoRestrictionMode.DISABLED;
}

function countryCountLabel(count: number): string {
  return `${count.toString()} ${count === 1 ? "country" : "countries"}`;
}
