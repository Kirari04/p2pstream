import { describe, expect, test } from "bun:test";
import { suggestBackendCloneName } from "@/lib/publicBackendClone";

describe("publicBackendClone", () => {
  test("uses the basic copy suffix", () => {
    expect(suggestBackendCloneName("api", ["api"])).toBe("api-copy");
  });

  test("increments when the first clone name exists", () => {
    expect(suggestBackendCloneName("api", ["api", "api-copy"])).toBe("api-copy-2");
  });

  test("increments through multiple existing clone names", () => {
    expect(suggestBackendCloneName("api", ["api", "api-copy", "api-copy-2"])).toBe("api-copy-3");
  });

  test("keeps long clone names within backend name limits", () => {
    const source = "a".repeat(70);
    const clone = suggestBackendCloneName(source, []);

    expect(clone.length).toBeLessThanOrEqual(64);
    expect(clone.endsWith("-copy")).toBe(true);
  });

  test("falls back to backend for empty source names", () => {
    expect(suggestBackendCloneName("", [])).toBe("backend-copy");
  });
});
