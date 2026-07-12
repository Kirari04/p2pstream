import {
  PublicTrustedProxyHeaderMode,
  type PublicGeoIpSettings,
  type PublicTrustedProxySource,
} from "@/gen/proto/p2pstream/v1/management_pb";

export type GeoIpSettingsForm = {
  enabled: boolean;
  maxmindAccountId: string;
  maxmindLicenseKey: string;
  licenseKeySaved: boolean;
  clearLicenseKey: boolean;
};

export type GeoIpSettingsPayload = {
  enabled: boolean;
  maxmindAccountId: string;
  maxmindLicenseKey: string;
  clearLicenseKey: boolean;
};

export type CustomTrustedProxyForm = {
  id: string;
  name: string;
  enabled: boolean;
  cidrsText: string;
  headerName: string;
  headerMode: PublicTrustedProxyHeaderMode;
};

export type CustomTrustedProxyPayload = {
  name: string;
  enabled: boolean;
  cidrs: string[];
  headerName: string;
  headerMode: PublicTrustedProxyHeaderMode;
};

export function geoIpSettingsFormFromProto(settings?: PublicGeoIpSettings): GeoIpSettingsForm {
  return {
    enabled: settings?.enabled ?? false,
    maxmindAccountId: settings?.maxmindAccountId ?? "",
    maxmindLicenseKey: "",
    licenseKeySaved: settings?.maxmindLicenseKeySet ?? false,
    clearLicenseKey: false,
  };
}

export function geoIpSettingsPayloadFromForm(form: GeoIpSettingsForm): GeoIpSettingsPayload {
  return {
    enabled: form.enabled,
    maxmindAccountId: form.maxmindAccountId.trim(),
    maxmindLicenseKey: form.clearLicenseKey ? "" : form.maxmindLicenseKey.trim(),
    clearLicenseKey: form.clearLicenseKey,
  };
}

export function geoIpSettingsValidationReason(form: GeoIpSettingsForm): string {
  const accountId = form.maxmindAccountId.trim();
  const licenseKey = form.maxmindLicenseKey.trim();
  if (accountId.length > 64) return "Use a MaxMind account ID no longer than 64 digits.";
  if (accountId && !/^\d+$/.test(accountId)) return "Enter the numeric MaxMind account ID.";
  if (licenseKey.length > 256) return "Use a MaxMind license key no longer than 256 characters.";
  if (licenseKey && !/^[\x21-\x7e]+$/.test(licenseKey)) return "Use only visible ASCII characters in the MaxMind license key.";
  if (!form.enabled) return "";
  if (!accountId) return "Enter the MaxMind account ID.";
  if (form.clearLicenseKey) return "A license key is required while GeoIP is enabled.";
  if (!form.licenseKeySaved && !licenseKey) return "Enter a MaxMind license key.";
  return "";
}

export function defaultCustomTrustedProxyForm(): CustomTrustedProxyForm {
  return {
    id: "",
    name: "",
    enabled: false,
    cidrsText: "",
    headerName: "X-Forwarded-For",
    headerMode: PublicTrustedProxyHeaderMode.TRUSTED_CHAIN,
  };
}

export function customTrustedProxyFormFromProto(source: PublicTrustedProxySource): CustomTrustedProxyForm {
  return {
    id: source.id.toString(),
    name: source.name,
    enabled: source.enabled,
    cidrsText: source.cidrs.join("\n"),
    headerName: source.headerName,
    headerMode: source.headerMode === PublicTrustedProxyHeaderMode.TRUSTED_CHAIN
      ? PublicTrustedProxyHeaderMode.TRUSTED_CHAIN
      : PublicTrustedProxyHeaderMode.SINGLE_IP,
  };
}

export function customTrustedProxyPayloadFromForm(form: CustomTrustedProxyForm): CustomTrustedProxyPayload {
  return {
    name: form.name.trim(),
    enabled: form.enabled,
    cidrs: trustedProxyCidrsFromText(form.cidrsText),
    headerName: form.headerName.trim(),
    headerMode: form.headerMode === PublicTrustedProxyHeaderMode.TRUSTED_CHAIN
      ? PublicTrustedProxyHeaderMode.TRUSTED_CHAIN
      : PublicTrustedProxyHeaderMode.SINGLE_IP,
  };
}

export function customTrustedProxyValidationReason(form: CustomTrustedProxyForm): string {
  const name = form.name.trim();
  if (!name) return "Enter a source name.";
  if (!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(name)) {
    return "Use 1–64 letters, numbers, dots, dashes, or underscores for the source name.";
  }
  if (!form.headerName.trim()) return "Enter the client-IP header name.";
  if (form.headerName.trim().length > 256) return "Use a client-IP header name no longer than 256 characters.";
  if (!isHttpHeaderName(form.headerName.trim())) return "Enter a valid HTTP header name.";
  const cidrs = trustedProxyCidrsFromText(form.cidrsText);
  if (form.enabled && !cidrs.length) return "Add at least one trusted peer CIDR before enabling trust.";
  if (cidrs.length > 4096) return "Add no more than 4096 trusted peer CIDRs.";
  const tooLong = cidrs.find((cidr) => cidr.length > 64);
  if (tooLong) return "Use trusted peer CIDRs no longer than 64 characters.";
  const invalid = cidrs.find((cidr) => !isValidCidr(cidr));
  if (invalid) return `Enter a valid IPv4 or IPv6 CIDR instead of ${invalid}.`;
  const defaultRoute = cidrs.find((cidr) => cidr.endsWith("/0"));
  if (defaultRoute) return `Replace ${defaultRoute}; default routes cannot be trusted.`;
  if (trustedProxyCidrsCoverAddressFamily(cidrs)) {
    return "Narrow the trusted peer CIDRs; together they cannot cover every IPv4 or IPv6 address.";
  }
  return "";
}

export function trustedProxyCidrsFromText(value: string): string[] {
  const cidrs = value
    .split(/[\n,]/)
    .map((cidr) => cidr.trim())
    .filter(Boolean);
  return [...new Set(cidrs)];
}

export function isValidCidr(value: string): boolean {
  const separator = value.lastIndexOf("/");
  if (separator < 1 || value.indexOf("/") !== separator) return false;
  const address = value.slice(0, separator);
  const prefixText = value.slice(separator + 1);
  if (!/^(0|[1-9]\d{0,2})$/.test(prefixText)) return false;
  const prefix = Number(prefixText);
  if (isValidIpv4(address)) return prefix <= 32;
  if (isValidIpv6(address)) return prefix <= 128;
  return false;
}

export function trustedProxyCidrsCoverAddressFamily(cidrs: readonly string[]): boolean {
  const families = new Map<number, Map<number, Set<bigint>>>([
    [32, new Map()],
    [128, new Map()],
  ]);
  for (const cidr of cidrs) {
    const parsed = parseCidrValue(cidr);
    if (!parsed) continue;
    const byBits = families.get(parsed.width);
    if (!byBits) continue;
    const networks = byBits.get(parsed.bits) ?? new Set<bigint>();
    networks.add(maskAddress(parsed.address, parsed.width, parsed.bits));
    byBits.set(parsed.bits, networks);
  }
  for (const [width, byBits] of families) {
    for (let bits = width; bits > 0; bits--) {
      const networks = byBits.get(bits);
      if (!networks?.size) continue;
      const siblingBit = 1n << BigInt(width - bits);
      for (const network of [...networks]) {
        if (!networks.has(network)) continue;
        const sibling = network ^ siblingBit;
        if (!networks.has(sibling)) continue;
        networks.delete(network);
        networks.delete(sibling);
        const parent = maskAddress(network, width, bits - 1);
        const parents = byBits.get(bits - 1) ?? new Set<bigint>();
        parents.add(parent);
        byBits.set(bits - 1, parents);
      }
    }
    if (byBits.get(0)?.size) return true;
  }
  return false;
}

function parseCidrValue(value: string): { address: bigint; bits: number; width: 32 | 128 } | null {
  const separator = value.lastIndexOf("/");
  if (separator < 1) return null;
  const rawAddress = value.slice(0, separator);
  let bits = Number(value.slice(separator + 1));
  const ipv4 = parseIpv4Value(rawAddress);
  if (ipv4 !== null && bits >= 0 && bits <= 32) return { address: ipv4, bits, width: 32 };
  const mapped = rawAddress.match(/^::ffff:(\d+\.\d+\.\d+\.\d+)$/i);
  if (mapped && bits >= 96 && bits <= 128) {
    const mappedAddress = parseIpv4Value(mapped[1]);
    if (mappedAddress !== null) return { address: mappedAddress, bits: bits - 96, width: 32 };
  }
  const ipv6 = parseIpv6Value(rawAddress);
  if (ipv6 !== null && bits >= 96 && bits <= 128 && (ipv6 >> 32n) === 0xffffn) {
    return { address: ipv6 & 0xffffffffn, bits: bits - 96, width: 32 };
  }
  if (ipv6 !== null && bits >= 0 && bits <= 128) return { address: ipv6, bits, width: 128 };
  return null;
}

function maskAddress(address: bigint, width: number, bits: number): bigint {
  if (bits === 0) return 0n;
  const hostBits = BigInt(width - bits);
  return (address >> hostBits) << hostBits;
}

function parseIpv4Value(value: string): bigint | null {
  if (!isValidIpv4(value)) return null;
  return value.split(".").reduce((address, octet) => (address << 8n) | BigInt(octet), 0n);
}

function parseIpv6Value(value: string): bigint | null {
  let normalized = value;
  if (normalized.includes(".")) {
    const lastColon = normalized.lastIndexOf(":");
    const ipv4 = lastColon >= 0 ? parseIpv4Value(normalized.slice(lastColon + 1)) : null;
    if (ipv4 === null) return null;
    const high = Number((ipv4 >> 16n) & 0xffffn).toString(16);
    const low = Number(ipv4 & 0xffffn).toString(16);
    normalized = `${normalized.slice(0, lastColon)}:${high}:${low}`;
  }
  const halves = normalized.split("::");
  if (halves.length > 2) return null;
  const left = halves[0] ? halves[0].split(":") : [];
  const right = halves[1] ? halves[1].split(":") : [];
  if (![...left, ...right].every((segment) => /^[0-9a-fA-F]{1,4}$/.test(segment))) return null;
  const missing = 8 - left.length - right.length;
  if ((halves.length === 1 && missing !== 0) || (halves.length === 2 && missing < 1)) return null;
  const segments = [...left, ...Array.from({ length: missing }, () => "0"), ...right];
  return segments.reduce((address, segment) => (address << 16n) | BigInt(`0x${segment}`), 0n);
}

function isHttpHeaderName(value: string): boolean {
  return /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(value);
}

function isValidIpv4(value: string): boolean {
  const octets = value.split(".");
  if (octets.length !== 4) return false;
  return octets.every((octet) => {
    if (!/^(0|[1-9]\d{0,2})$/.test(octet)) return false;
    return Number(octet) <= 255;
  });
}

function isValidIpv6(value: string): boolean {
  if (!value.includes(":") || value.includes("%")) return false;
  let normalized = value;
  if (normalized.includes(".")) {
    const lastColon = normalized.lastIndexOf(":");
    if (lastColon < 0 || !isValidIpv4(normalized.slice(lastColon + 1))) return false;
    normalized = `${normalized.slice(0, lastColon)}:0:0`;
  }

  const halves = normalized.split("::");
  if (halves.length > 2) return false;
  const left = ipv6Segments(halves[0] ?? "");
  const right = ipv6Segments(halves[1] ?? "");
  if (!left || !right) return false;
  const count = left.length + right.length;
  return halves.length === 2 ? count < 8 : count === 8;
}

function ipv6Segments(value: string): string[] | null {
  if (!value) return [];
  const segments = value.split(":");
  return segments.every((segment) => /^[0-9a-fA-F]{1,4}$/.test(segment)) ? segments : null;
}
