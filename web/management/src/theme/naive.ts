import type { GlobalThemeOverrides } from "naive-ui";

export type ThemeMode = "light" | "dark";

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
    successColor: "#059669",
    warningColor: "#d97706",
    errorColor: "#dc2626",
    infoColor: "#2563eb",
    bodyColor: "#f5f7fb",
    cardColor: "#ffffff",
    modalColor: "#ffffff",
    popoverColor: "#ffffff",
    tableColor: "#ffffff",
    textColorBase: "#142033",
    textColor1: "#142033",
    textColor2: "#344054",
    textColor3: "#667085",
    borderColor: "#d8e0ea",
    dividerColor: "#e4eaf2",
  },
  Button: {
    fontWeight: "600",
    borderRadiusMedium: radius,
  },
  Card: {
    borderRadius: "10px",
    color: "#ffffff",
    colorEmbedded: "#f8fafc",
    borderColor: "#d8e0ea",
  },
  DataTable: {
    thColor: "#f8fafc",
    tdColorHover: "#f5f7fb",
    borderColor: "#e4eaf2",
  },
  Modal: {
    borderRadius: "12px",
  },
};

export const darkThemeOverrides: GlobalThemeOverrides = {
  common: {
    fontFamily,
    fontFamilyMono: monoFamily,
    borderRadius: radius,
    borderRadiusSmall: "6px",
    primaryColor: "#14b8a6",
    primaryColorHover: "#2dd4bf",
    primaryColorPressed: "#0f766e",
    primaryColorSuppl: "#5eead4",
    successColor: "#10b981",
    warningColor: "#f59e0b",
    errorColor: "#f87171",
    infoColor: "#38bdf8",
    bodyColor: "#111315",
    cardColor: "#181b1f",
    modalColor: "#181b1f",
    popoverColor: "#181b1f",
    tableColor: "#181b1f",
    textColorBase: "#f4f6f8",
    textColor1: "#f4f6f8",
    textColor2: "#d0d5dd",
    textColor3: "#98a2b3",
    borderColor: "#343a42",
    dividerColor: "#2b3037",
  },
  Button: {
    fontWeight: "600",
    borderRadiusMedium: radius,
  },
  Card: {
    borderRadius: "10px",
    color: "#181b1f",
    colorEmbedded: "#20242a",
    borderColor: "#343a42",
  },
  DataTable: {
    thColor: "#20242a",
    tdColorHover: "#20242a",
    borderColor: "#2b3037",
  },
  Modal: {
    borderRadius: "12px",
  },
};
