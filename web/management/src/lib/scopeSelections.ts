export type SelectionID = string | number | bigint;

export function unavailableSelectionIds(
  selectedIds: readonly SelectionID[],
  availableIds: readonly SelectionID[],
): string[] {
  const available = new Set(availableIds.map(String));
  const seen = new Set<string>();
  const unavailable: string[] = [];
  for (const rawId of selectedIds) {
    const id = String(rawId);
    if (!id || available.has(id) || seen.has(id)) continue;
    seen.add(id);
    unavailable.push(id);
  }
  return unavailable;
}
