export type CountryOption = {
  label: string;
  value: string;
};

/**
 * ISO 3166-1 alpha-2 codes accepted by MaxMind's country.iso_code field,
 * plus MaxMind's user-assigned XK code for Kosovo.
 * Keep this list as codes rather than localized names so persisted values stay
 * stable when the management UI locale changes.
 */
export const ISO_COUNTRY_CODES = [
  "AD", "AE", "AF", "AG", "AI", "AL", "AM", "AO", "AQ", "AR", "AS", "AT",
  "AU", "AW", "AX", "AZ", "BA", "BB", "BD", "BE", "BF", "BG", "BH", "BI",
  "BJ", "BL", "BM", "BN", "BO", "BQ", "BR", "BS", "BT", "BV", "BW", "BY",
  "BZ", "CA", "CC", "CD", "CF", "CG", "CH", "CI", "CK", "CL", "CM", "CN",
  "CO", "CR", "CU", "CV", "CW", "CX", "CY", "CZ", "DE", "DJ", "DK", "DM",
  "DO", "DZ", "EC", "EE", "EG", "EH", "ER", "ES", "ET", "FI", "FJ", "FK",
  "FM", "FO", "FR", "GA", "GB", "GD", "GE", "GF", "GG", "GH", "GI", "GL",
  "GM", "GN", "GP", "GQ", "GR", "GS", "GT", "GU", "GW", "GY", "HK", "HM",
  "HN", "HR", "HT", "HU", "ID", "IE", "IL", "IM", "IN", "IO", "IQ", "IR",
  "IS", "IT", "JE", "JM", "JO", "JP", "KE", "KG", "KH", "KI", "KM", "KN",
  "KP", "KR", "KW", "KY", "KZ", "LA", "LB", "LC", "LI", "LK", "LR", "LS",
  "LT", "LU", "LV", "LY", "MA", "MC", "MD", "ME", "MF", "MG", "MH", "MK",
  "ML", "MM", "MN", "MO", "MP", "MQ", "MR", "MS", "MT", "MU", "MV", "MW",
  "MX", "MY", "MZ", "NA", "NC", "NE", "NF", "NG", "NI", "NL", "NO", "NP",
  "NR", "NU", "NZ", "OM", "PA", "PE", "PF", "PG", "PH", "PK", "PL", "PM",
  "PN", "PR", "PS", "PT", "PW", "PY", "QA", "RE", "RO", "RS", "RU", "RW",
  "SA", "SB", "SC", "SD", "SE", "SG", "SH", "SI", "SJ", "SK", "SL", "SM",
  "SN", "SO", "SR", "SS", "ST", "SV", "SX", "SY", "SZ", "TC", "TD", "TF",
  "TG", "TH", "TJ", "TK", "TL", "TM", "TN", "TO", "TR", "TT", "TV", "TW",
  "TZ", "UA", "UG", "UM", "US", "UY", "UZ", "VA", "VC", "VE", "VG", "VI",
  "VN", "VU", "WF", "WS", "XK", "YE", "YT", "ZA", "ZM", "ZW",
] as const;

const countryCodeSet = new Set<string>(ISO_COUNTRY_CODES);
const optionsByLocale = new Map<string, readonly CountryOption[]>();

export function isIsoCountryCode(value: string): boolean {
  return countryCodeSet.has(value.trim().toUpperCase());
}

export function isGeoCountryCode(value: string): boolean {
  return /^[A-Z]{2}$/.test(value.trim().toUpperCase());
}

export function normalizeCountryCodes(values: readonly string[]): string[] {
  const normalized = values
    .map((value) => value.trim().toUpperCase())
    .filter(isGeoCountryCode);
  return [...new Set(normalized)].sort();
}

export function invalidCountryCodes(values: readonly string[]): string[] {
  return [...new Set(values
    .map((value) => value.trim().toUpperCase())
    .filter((value) => !isGeoCountryCode(value)))].sort();
}

export function countryName(code: string, locale = "en"): string {
  const normalized = code.trim().toUpperCase();
  if (!countryCodeSet.has(normalized)) return normalized;
  try {
    return new Intl.DisplayNames([locale], { type: "region" }).of(normalized) || normalized;
  } catch {
    return normalized;
  }
}

export function countryOptions(locale = "en"): readonly CountryOption[] {
  const cached = optionsByLocale.get(locale);
  if (cached) return cached;

  const collator = new Intl.Collator(locale, { sensitivity: "base" });
  const options = ISO_COUNTRY_CODES
    .map((code) => ({
      label: `${countryName(code, locale)} · ${code}`,
      value: code,
    }))
    .sort((left, right) => collator.compare(left.label, right.label));
  optionsByLocale.set(locale, options);
  return options;
}
