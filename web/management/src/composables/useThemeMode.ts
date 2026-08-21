import { computed, ref, watch } from "vue";
import {
  themeStorageKey,
  type ResolvedThemeMode,
  type ThemeMode,
} from "@/theme/naive";

const systemDarkModeQuery = "(prefers-color-scheme: dark)";

export function normalizeThemeMode(value: string | null): ThemeMode {
  return value === "system" || value === "light" || value === "dark" ? value : "system";
}

export function resolveThemeMode(mode: ThemeMode, systemPrefersDark: boolean): ResolvedThemeMode {
  if (mode === "system") return systemPrefersDark ? "dark" : "light";
  return mode;
}

export function observeSystemDarkMode(
  mediaQuery: MediaQueryList,
  onChange: (matches: boolean) => void,
): () => void {
  const handleChange = (event: MediaQueryListEvent) => onChange(event.matches);

  onChange(mediaQuery.matches);

  if (typeof mediaQuery.addEventListener === "function") {
    mediaQuery.addEventListener("change", handleChange);
    return () => mediaQuery.removeEventListener("change", handleChange);
  }

  // Older WebKit versions only expose the deprecated listener API.
  mediaQuery.addListener(handleChange);
  return () => mediaQuery.removeListener(handleChange);
}

function loadInitialThemeMode(): ThemeMode {
  if (typeof window === "undefined") return "system";

  try {
    return normalizeThemeMode(window.localStorage.getItem(themeStorageKey));
  } catch {
    return "system";
  }
}

const themeMode = ref<ThemeMode>(loadInitialThemeMode());
const systemPrefersDark = ref(false);
const resolvedThemeMode = computed<ResolvedThemeMode>(() => (
  resolveThemeMode(themeMode.value, systemPrefersDark.value)
));

function syncDocumentThemeClass(mode: ResolvedThemeMode) {
  if (typeof document === "undefined") return;
  document.documentElement.classList.toggle("dark", mode === "dark");
}

let stopObservingSystemTheme: (() => void) | undefined;

if (typeof window !== "undefined" && typeof window.matchMedia === "function") {
  stopObservingSystemTheme = observeSystemDarkMode(
    window.matchMedia(systemDarkModeQuery),
    (matches) => {
      systemPrefersDark.value = matches;
    },
  );
}

syncDocumentThemeClass(resolvedThemeMode.value);

watch(resolvedThemeMode, (mode) => {
  syncDocumentThemeClass(mode);
});

watch(themeMode, (mode) => {
  if (typeof window === "undefined") return;

  try {
    window.localStorage.setItem(themeStorageKey, mode);
  } catch {
    // Storage is optional; keep the in-memory theme even if persistence fails.
  }
});

if (import.meta.hot) {
  import.meta.hot.dispose(() => stopObservingSystemTheme?.());
}

export function useThemeMode() {
  const isDarkTheme = computed(() => resolvedThemeMode.value === "dark");

  function setThemeMode(mode: ThemeMode) {
    themeMode.value = mode;
  }

  function toggleTheme() {
    themeMode.value = resolvedThemeMode.value === "dark" ? "light" : "dark";
  }

  return {
    themeMode,
    resolvedThemeMode,
    isDarkTheme,
    setThemeMode,
    toggleTheme,
  };
}
