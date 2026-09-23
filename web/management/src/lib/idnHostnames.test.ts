import { describe, expect, test } from "bun:test";
import {
  asciiHostnamePattern,
  displayHostnamePattern,
  displayMigratedSiteName,
} from "@/lib/idnHostnames";

describe("internationalized hostname presentation", () => {
  test("shows a stored IDN in Unicode without changing its DNS identifier", () => {
    expect(displayHostnamePattern("xn--zribadi-n2a.ch")).toBe("züribadi.ch");
    expect(displayHostnamePattern("*.xn--bcher-kva.example")).toBe("*.bücher.example");
    expect(asciiHostnamePattern("züribadi.ch")).toBe("xn--zribadi-n2a.ch");
    expect(asciiHostnamePattern("*.bücher.example")).toBe("*.xn--bcher-kva.example");
  });

  test("keeps ordinary and invalid hostname text unchanged", () => {
    expect(displayHostnamePattern("example.com")).toBe("example.com");
    expect(displayHostnamePattern("xn--a.example")).toBe("xn--a.example");
    expect(displayHostnamePattern("xn--zribadi-n2a.ch/path")).toBe(
      "xn--zribadi-n2a.ch/path",
    );
    expect(asciiHostnamePattern("host.example/path")).toBe("host.example/path");
  });

  test("decodes generated Site names while preserving custom identifiers", () => {
    expect(displayMigratedSiteName("migrated-2-xn--zribadi-n2a.ch")).toBe(
      "migrated-2-züribadi.ch",
    );
    expect(displayMigratedSiteName("my-xn--zribadi-n2a.ch")).toBe(
      "my-xn--zribadi-n2a.ch",
    );
  });
});
