import { describe, expect, test } from "bun:test";
import { createRouter, createWebHashHistory } from "vue-router";
import { consumeManagementSetupToken, stripManagementSetupToken } from "./managementSetupToken";

describe("management setup token URLs", () => {
  test("removes every token copy while preserving unrelated URL bytes and anchors", () => {
    for (const [input, clean, token] of [
      ["https://app.test/?setup_token=OUT#/overview", "https://app.test/#/overview", "OUT"],
      ["https://app.test/#/overview?setup_token=HASH", "https://app.test/#/overview", "HASH"],
      ["https://app.test/?theme=a%20b&setup_token=OUT&setup_token=MORE#/overview?tag=a+b&setup_token=HASH&tag=c%2fd#panel",
        "https://app.test/?theme=a%20b#/overview?tag=a+b&tag=c%2fd#panel", "HASH"],
      ["https://app.test/?%73etup_token=+%20&setup_token=%20OUT%2B%2F%20#/overview?%73etup_token=+&setup_token=%20HASH%2B%2F%20&setup_token=LAST",
        "https://app.test/#/overview", "HASH+/"],
      ["https://app.test/?setup_token=&keep=1#/overview?setup_token=+%20&keep=2", "https://app.test/?keep=1#/overview?keep=2", ""],
      ["https://app.test/?theme=dark#/overview?keep=a%20b#panel", "https://app.test/?theme=dark#/overview?keep=a%20b#panel", ""],
    ]) {
      const result = stripManagementSetupToken(input!);
      expect(result.location).toBe(clean);
      expect(result.token).toBe(token);
      expect(stripManagementSetupToken(result.location).changed).toBe(false);
    }
  });

  test("scrubs browser and real hash-router state before subsequent navigation", async () => {
    // A minimal browser adapter exercises Vue Router's actual hash history,
    // including its private cached state and replace-before-push behavior.
    for (const readyBeforeMount of [true, false]) {
      for (const entry of [
        "https://app.test/manage?setup_token=OUT&theme=a%20b#/overview?keep=1",
        "https://app.test/manage?theme=a%20b#/overview?setup_token=HASH&setup_token=OTHER&keep=1",
        "https://app.test/manage?setup_token=OUT&setup_token=OTHER&theme=a%20b#/overview?setup_token=HASH&keep=1",
        "https://app.test/manage?%73etup_token=+%20&theme=a%20b#/overview?%73etup_token=%20HASH%2B%2F%20&keep=1",
        "https://app.test/manage?setup_token=+%20&theme=a%20b#/overview?setup_token=&keep=1",
        "https://app.test/manage?theme=a%20b#/overview?keep=a%20b&setup_token=HASH&tag=a+b&tag=c%2fd#panel",
      ]) {
        const location = new URL(entry);
        const routeEntry = location.hash.slice(1);
        const entries = [entry];
        const savedState = {
          back: "/before?keep=0", current: routeEntry, forward: "/after?keep=2",
          position: 4, replaced: false, scroll: { left: 11, top: 22 }, custom: { keep: "navigation-state" },
        };
        const history = {
          length: 5,
          state: savedState,
          replaceState(state: typeof savedState, _title: string, url?: string | URL | null) {
            this.state = state;
            if (url) location.href = new URL(url, location.href).href;
            entries[entries.length - 1] = location.href;
          },
          pushState(state: typeof savedState, _title: string, url?: string | URL | null) {
            this.state = state;
            if (url) location.href = new URL(url, location.href).href;
            entries.push(location.href);
          },
          go() {},
        };
        const browser = {
          location, history, pageXOffset: 0, pageYOffset: 0,
          addEventListener() {}, removeEventListener() {},
        } as unknown as Window;
        const replacements: Record<string, unknown> = {
          window: browser, location, history,
          document: { querySelector: () => null, addEventListener() {}, removeEventListener() {} },
        };
        const original = new Map(Object.keys(replacements).map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
        for (const [key, value] of Object.entries(replacements)) {
          Object.defineProperty(globalThis, key, { configurable: true, value });
        }
        const routerHistory = createWebHashHistory();
        try {
          const router = createRouter({ history: routerHistory, routes: [{ path: "/:pathMatch(.*)*", component: {} }] });
          const initialNavigation = router.push(routeEntry);
          if (readyBeforeMount) await initialNavigation;
          // Set unrelated state after initial navigation, as a running app would.
          routerHistory.replace(routeEntry, savedState);
          const expected = stripManagementSetupToken(entry);
          expect(await consumeManagementSetupToken(router, browser)).toBe(expected.token);
          expect(location.href).toBe(expected.location);
          expect(entries).toEqual([expected.location]);
          const cleanRoute = new URL(expected.location).hash.slice(1);
          expect(router.currentRoute.value.query).toEqual(router.resolve(cleanRoute).query);
          expect(router.currentRoute.value.hash).toBe(router.resolve(cleanRoute).hash);
          expect(routerHistory.location).toBe(cleanRoute);
          expect(history.state.custom).toEqual(savedState.custom);
          // The SSR-loaded router clears initial scroll during its first
          // navigation; an already-running navigation retains its saved scroll.
          expect(history.state.scroll).toEqual(readyBeforeMount ? savedState.scroll : undefined);
          expect(history.state.position).toBe(4);
          expect(history.state.back).toBe(savedState.back);
          expect(history.state.forward).toBe(savedState.forward);
          expect(await consumeManagementSetupToken(router, browser)).toBe("");
          // Reusing reactive route.query previously resurrected the hash secret.
          await router.push({ path: "/next", query: router.currentRoute.value.query });
          const nextHref = new URL(router.resolve({ path: "/next", query: router.currentRoute.value.query }).href, expected.location).href;
          expect(entries).toEqual([expected.location, nextHref]);
          expect(JSON.stringify(history.state)).not.toContain("setup_token");
        } finally {
          routerHistory.destroy();
          for (const [key, descriptor] of original) {
            if (descriptor) Object.defineProperty(globalThis, key, descriptor);
            else Reflect.deleteProperty(globalThis, key);
          }
        }
      }
    }
  });
});
