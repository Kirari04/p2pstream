export const DIAGNOSTIC_EXCERPT_LIMIT = 72;

export type DiagnosticExcerpt = Readonly<{
  text: string;
  truncated: boolean;
}>;

const controlOrFormatCharacter = /[\p{Cc}\p{Cf}]/u;

/**
 * Produces a reversible, non-executable representation for public-request text.
 * Invisible control/format characters and literal backslashes are made visible so
 * bidi controls cannot disguise neighboring management UI.
 */
export function diagnosticInspectionText(value: string): string {
  return Array.from(value, inspectionSegment).join("");
}

/**
 * Returns a bounded table excerpt without splitting a visible escape sequence.
 * The exact, unbounded representation remains available in the sample drawer.
 */
export function diagnosticExcerpt(
  value: string,
  limit = DIAGNOSTIC_EXCERPT_LIMIT,
): DiagnosticExcerpt {
  if (!value) return { text: "-", truncated: false };

  const safeLimit = Math.max(2, Math.trunc(limit));
  const segments: string[] = [];
  let excerptLength = 0;
  for (const character of value) {
    const segment = inspectionSegment(character);
    const segmentLength = Array.from(segment).length;
    if (excerptLength + segmentLength > safeLimit) {
      while (excerptLength > safeLimit - 1) {
        const removed = segments.pop();
        if (!removed) break;
        excerptLength -= Array.from(removed).length;
      }
      return { text: `${segments.join("")}…`, truncated: true };
    }
    segments.push(segment);
    excerptLength += segmentLength;
  }
  return { text: segments.join(""), truncated: false };
}

function inspectionSegment(character: string): string {
  if (character === "\\") return "\\\\";
  if (!controlOrFormatCharacter.test(character)) return character;
  const codePoint = character.codePointAt(0) ?? 0;
  return `\\u{${codePoint.toString(16).toUpperCase().padStart(4, "0")}}`;
}
