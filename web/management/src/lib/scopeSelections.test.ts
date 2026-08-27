import { describe, expect, test } from "bun:test";
import { unavailableSelectionIds } from "@/lib/scopeSelections";

describe("unavailableSelectionIds", () => {
  test("keeps missing saved references visible without duplicating them", () => {
    expect(unavailableSelectionIds([21n, 19n, 23n, 19n], [21n, 23n, 25n])).toEqual(["19"]);
  });

  test("normalizes identifier representations", () => {
    expect(unavailableSelectionIds(["21", 23, 25n], [21n, "23", 25])).toEqual([]);
  });
});
