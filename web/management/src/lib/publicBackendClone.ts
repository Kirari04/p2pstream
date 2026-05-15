const backendNameMaxLength = 64;

export function suggestBackendCloneName(sourceName: string, existingNames: readonly string[]): string {
  const existing = new Set(existingNames);
  const base = sourceName.trim() || "backend";

  for (let index = 1; ; index += 1) {
    const suffix = index === 1 ? "-copy" : `-copy-${index.toString()}`;
    const candidate = `${base.slice(0, Math.max(1, backendNameMaxLength - suffix.length))}${suffix}`;
    if (!existing.has(candidate)) return candidate;
  }
}
