/// <reference types="vitepress/client" />

import DefaultTheme from "vitepress/theme";
import type { Theme } from "vitepress";
import { inBrowser, useRoute } from "vitepress";
import mediumZoom from "medium-zoom";
import { nextTick, watch } from "vue";
import "./custom.css";

let zoom: ReturnType<typeof mediumZoom> | undefined;

function refreshZoom() {
  zoom?.detach();
  zoom = mediumZoom(".vp-doc .doc-screenshot img, .vp-doc .architecture-frame img", {
    background: "var(--vp-c-bg)",
    margin: 24,
    scrollOffset: 0
  });
}

export default {
  extends: DefaultTheme,
  setup() {
    if (!inBrowser) return;

    const route = useRoute();

    watch(
      () => route.path,
      () => nextTick(refreshZoom),
      { immediate: true }
    );
  }
} satisfies Theme;
