import { describe, expect, test } from "bun:test";
import {
  DIAGNOSTIC_EXCERPT_LIMIT,
  diagnosticExcerpt,
  diagnosticInspectionText,
} from "@/lib/diagnosticText";

describe("diagnosticText", () => {
  test("makes bidi, invisible, and control characters explicit without losing data", () => {
    const value = "public.example\\literal\u202E\u200B\nend";

    expect(diagnosticInspectionText(value)).toBe(
      "public.example\\\\literal\\u{202E}\\u{200B}\\u{000A}end",
    );
  });

  test("bounds excerpts without splitting escaped control characters", () => {
    const value = `${"a".repeat(DIAGNOSTIC_EXCERPT_LIMIT - 10)}\u202Ehidden`;
    const excerpt = diagnosticExcerpt(value);

    expect(excerpt.truncated).toBe(true);
    expect(Array.from(excerpt.text).length).toBeLessThanOrEqual(DIAGNOSTIC_EXCERPT_LIMIT);
    expect(excerpt.text).toContain("\\u{202E}");
    expect(excerpt.text.endsWith("…")).toBe(true);
  });

  test("preserves complete ordinary values and represents an empty value explicitly", () => {
    expect(diagnosticExcerpt("example.test")).toEqual({ text: "example.test", truncated: false });
    expect(diagnosticExcerpt("x".repeat(DIAGNOSTIC_EXCERPT_LIMIT))).toEqual({
      text: "x".repeat(DIAGNOSTIC_EXCERPT_LIMIT),
      truncated: false,
    });
    expect(diagnosticExcerpt("")).toEqual({ text: "-", truncated: false });
  });

  test("distinguishes literal escape-looking text from an escaped format character", () => {
    expect(diagnosticInspectionText("literal\\u{202E}\u202E")).toBe(
      "literal\\\\u{202E}\\u{202E}",
    );
  });
});
