import type { CSSProperties } from "vue";

export function naiveButtonType(severity?: string): "primary" | "default" | "success" | "info" | "warning" | "error" {
  switch (severity) {
    case "success":
      return "success";
    case "info":
      return "info";
    case "warn":
    case "warning":
      return "warning";
    case "danger":
    case "error":
      return "error";
    case "secondary":
    case "contrast":
      return "default";
    default:
      return "primary";
  }
}

export function naiveTagType(severity?: string): "default" | "success" | "warning" | "error" | "info" {
  switch (severity) {
    case "success":
      return "success";
    case "warn":
    case "warning":
      return "warning";
    case "danger":
    case "error":
      return "error";
    case "info":
      return "info";
    default:
      return "default";
  }
}

export function modalCardStyle(maxWidth = "42rem"): CSSProperties {
  return {
    width: `min(calc(100vw - 2rem), ${maxWidth})`,
    maxHeight: "min(calc(100vh - 3rem), calc(100dvh - 3rem))",
    overflow: "hidden",
  };
}

export function modalScrollableContentStyle(): CSSProperties {
  return {
    minHeight: "0",
    overflowY: "auto",
    overscrollBehavior: "contain",
    scrollbarGutter: "stable",
  };
}

export function editorDrawerWidth(maxWidth = "42rem"): string {
  return `min(100vw, ${maxWidth})`;
}
