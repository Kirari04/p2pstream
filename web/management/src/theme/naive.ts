import type { GlobalThemeOverrides } from "naive-ui";

export type ThemeMode = "system" | "light" | "dark";
export type ResolvedThemeMode = Exclude<ThemeMode, "system">;

export const themeStorageKey = "p2pstream:management-theme";

const radius = "8px";
const fontFamily = '"IBM Plex Sans", ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif';
const monoFamily = '"IBM Plex Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace';

export const lightThemeOverrides: GlobalThemeOverrides = {
  common: {
    fontFamily,
    fontFamilyMono: monoFamily,
    borderRadius: radius,
    borderRadiusSmall: "6px",
    primaryColor: "#2563eb",
    primaryColorHover: "#1d4ed8",
    primaryColorPressed: "#1e40af",
    primaryColorSuppl: "#3b82f6",
    successColor: "#047857",
    warningColor: "#9a5800",
    errorColor: "#dc2626",
    infoColor: "#2563eb",
    bodyColor: "#fafafb",
    cardColor: "#ffffff",
    modalColor: "#ffffff",
    popoverColor: "#ffffff",
    tableColor: "#ffffff",
    textColorBase: "#111827",
    textColor1: "#111827",
    textColor2: "#374151",
    textColor3: "#6b7280",
    placeholderColor: "#6b7280",
    placeholderColorDisabled: "#98a2b3",
    borderColor: "#e2e5e9",
    dividerColor: "#eceef1",
  },
  Button: {
    fontWeight: "500",
    borderRadiusMedium: radius,
  },
  Card: {
    borderRadius: "10px",
    paddingMedium: "18px 20px",
    color: "#ffffff",
    colorEmbedded: "#f4f5f7",
    borderColor: "#e2e5e9",
  },
  DataTable: {
    thColor: "#ffffff",
    tdColorHover: "#f7f8fa",
    borderColor: "#eceef1",
  },
  Modal: {
    borderRadius: "12px",
  },
  Tag: {
    borderRadius: "6px",
  },
};

export const darkThemeOverrides: GlobalThemeOverrides = {
  common: {
    fontFamily,
    fontFamilyMono: monoFamily,
    borderRadius: radius,
    borderRadiusSmall: "6px",
    primaryColor: "#4f8ef7",
    primaryColorHover: "#6ea2f9",
    primaryColorPressed: "#3b78e0",
    primaryColorSuppl: "#93c5fd",
    successColor: "#34d399",
    warningColor: "#f0b44c",
    errorColor: "#f87171",
    infoColor: "#4f8ef7",
    bodyColor: "#0d0f12",
    cardColor: "#121519",
    modalColor: "#15181d",
    popoverColor: "#171a1f",
    tableColor: "#121519",
    textColorBase: "#e8eaed",
    textColor1: "#e8eaed",
    textColor2: "#c9ced5",
    textColor3: "#8b939e",
    placeholderColor: "#6f7782",
    placeholderColorDisabled: "#555c66",
    borderColor: "#252a31",
    dividerColor: "#1c2026",
  },
  Button: {
    fontWeight: "500",
    borderRadiusMedium: radius,
  },
  Card: {
    borderRadius: "10px",
    paddingMedium: "18px 20px",
    color: "#121519",
    colorEmbedded: "#171a1f",
    borderColor: "#1f242a",
  },
  DataTable: {
    thColor: "#121519",
    tdColorHover: "#161a1f",
    borderColor: "#1c2026",
  },
  Modal: {
    borderRadius: "12px",
  },
  Tag: {
    borderRadius: "6px",
  },
};
