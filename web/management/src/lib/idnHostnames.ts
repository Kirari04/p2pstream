import { toUnicode } from "punycode/";

/** Decode canonical DNS names for display; keep malformed or ambiguous ACE unchanged. */
export function displayHostnamePattern(value: string): string {
  const wildcard = value.startsWith("*.") ? "*." : "";
  const ascii = wildcard ? value.slice(2) : value;
  if (!/^[a-z0-9.-]+$/iu.test(ascii) || !/(^|\.)xn--/iu.test(ascii))
    return value;

  try {
    const decoded = ascii
      .split(".")
      .map((label) =>
        label.toLowerCase().startsWith("xn--") ? toUnicode(label) : label,
      )
      .join(".");
    if (new URL(`https://${decoded}/`).hostname !== ascii.toLowerCase())
      return value;
    return `${wildcard}${decoded}`;
  } catch {
    return value;
  }
}

/** Use the browser's IDNA handling for comparisons, while leaving invalid input to server validation. */
export function asciiHostnamePattern(value: string): string {
  const wildcard = value.startsWith("*.") ? "*." : "";
  const hostname = wildcard ? value.slice(2) : value;
  try {
    if (!hostname || /[/?#@\\\s]/u.test(hostname)) return value;
    return `${wildcard}${new URL(`https://${hostname}/`).hostname}`;
  } catch {
    return value;
  }
}

export function displayMigratedSiteName(value: string): string {
  const match = /^(migrated-\d+-)(.+)$/u.exec(value);
  if (!match) return value;
  return `${match[1]}${displayHostnamePattern(match[2])}`;
}
