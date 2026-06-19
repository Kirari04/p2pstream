import { computed, ref, watch } from "vue";
import { themeStorageKey, type ThemeMode } from "@/theme/naive";

function loadInitialThemeMode(): ThemeMode {
  try {
    return window.localStorage.getItem(themeStorageKey) === "dark" ? "dark" : "light";
  } catch {
    return "light";
  }
}

const themeMode = ref<ThemeMode>(loadInitialThemeMode());

function syncDocumentThemeClass(mode: ThemeMode) {
  if (typeof document === "undefined") return;
  document.documentElement.classList.toggle("dark", mode === "dark");
}

syncDocumentThemeClass(themeMode.value);

watch(themeMode, (mode) => {
  syncDocumentThemeClass(mode);
  try {
    window.localStorage.setItem(themeStorageKey, mode);
  } catch {
    // Storage is optional; keep the in-memory theme even if persistence fails.
  }
});

export function useThemeMode() {
  const isDarkTheme = computed(() => themeMode.value === "dark");

  function setThemeMode(mode: ThemeMode) {
    themeMode.value = mode;
  }

  function toggleTheme() {
    themeMode.value = themeMode.value === "dark" ? "light" : "dark";
  }

  return {
    themeMode,
    isDarkTheme,
    setThemeMode,
    toggleTheme,
  };
}
