import { describe, expect, test } from "bun:test";
import {
  normalizeThemeMode,
  observeSystemDarkMode,
  resolveThemeMode,
} from "./useThemeMode";

describe("theme mode", () => {
  test("preserves supported stored preferences and defaults invalid values to system", () => {
    expect(normalizeThemeMode("system")).toBe("system");
    expect(normalizeThemeMode("light")).toBe("light");
    expect(normalizeThemeMode("dark")).toBe("dark");
    expect(normalizeThemeMode(null)).toBe("system");
    expect(normalizeThemeMode("sepia")).toBe("system");
  });

  test("resolves system mode without changing explicit preferences", () => {
    expect(resolveThemeMode("system", false)).toBe("light");
    expect(resolveThemeMode("system", true)).toBe("dark");
    expect(resolveThemeMode("light", true)).toBe("light");
    expect(resolveThemeMode("dark", false)).toBe("dark");
  });

  test("observes and cleans up operating-system theme changes", () => {
    const listeners = new Set<(event: MediaQueryListEvent) => void>();
    const mediaQuery = {
      matches: true,
      addEventListener: (_type: string, listener: (event: MediaQueryListEvent) => void) => {
        listeners.add(listener);
      },
      removeEventListener: (_type: string, listener: (event: MediaQueryListEvent) => void) => {
        listeners.delete(listener);
      },
    } as unknown as MediaQueryList;
    const changes: boolean[] = [];

    const stop = observeSystemDarkMode(mediaQuery, (matches) => changes.push(matches));
    expect(changes).toEqual([true]);

    for (const listener of listeners) {
      listener({ matches: false } as MediaQueryListEvent);
    }
    expect(changes).toEqual([true, false]);

    stop();
    expect(listeners.size).toBe(0);
  });
});
