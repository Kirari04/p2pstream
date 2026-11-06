import { onBeforeUnmount, onMounted, ref, type Ref } from "vue";

/** Reactive `matchMedia` result; false during SSR and before mount. */
export function useMediaQuery(query: string): Ref<boolean> {
  const matches = ref(false);
  let mediaQuery: MediaQueryList | undefined;

  function sync(event: MediaQueryList | MediaQueryListEvent) {
    matches.value = event.matches;
  }

  onMounted(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    mediaQuery = window.matchMedia(query);
    sync(mediaQuery);
    mediaQuery.addEventListener("change", sync);
  });

  onBeforeUnmount(() => {
    mediaQuery?.removeEventListener("change", sync);
  });

  return matches;
}

/** Matches the phone breakpoint used across the management UI (below 640px). */
export function useNarrowScreen(): Ref<boolean> {
  return useMediaQuery("(max-width: 639px)");
}
