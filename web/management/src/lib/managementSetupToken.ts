import type { HistoryState, Router } from "vue-router";

function stripSetupTokenQuery(location: string): { location: string; token: string; changed: boolean } {
  const hashIndex = location.indexOf("#");
  const end = hashIndex < 0 ? location.length : hashIndex;
  const queryIndex = location.indexOf("?");
  if (queryIndex < 0 || queryIndex > end) return { location, token: "", changed: false };

  let token = "";
  let changed = false;
  const kept = location.slice(queryIndex + 1, end).split("&").filter((part) => {
    const params = new URLSearchParams(part);
    if (!params.has("setup_token")) return true;
    token ||= params.get("setup_token")?.trim() ?? "";
    changed = true;
    return false;
  });
  if (!changed) return { location, token, changed };
  const query = kept.join("&");
  return {
    location: location.slice(0, queryIndex) + (query ? `?${query}` : "") + location.slice(end),
    token,
    changed,
  };
}

// Keep unrelated query bytes, duplicate values, and the route's own anchor.
// Hash query tokens take precedence, as they did in useManagementSession.
export function stripManagementSetupToken(location: string) {
  const outer = stripSetupTokenQuery(location);
  const hashIndex = outer.location.indexOf("#");
  if (hashIndex < 0) return outer;
  const hash = stripSetupTokenQuery(outer.location.slice(hashIndex + 1));
  return {
    location: outer.location.slice(0, hashIndex + 1) + hash.location,
    token: hash.token || outer.token,
    changed: outer.changed || hash.changed,
  };
}

export async function consumeManagementSetupToken(router: Router, browser: Pick<Window, "location" | "history">): Promise<string> {
  const result = stripManagementSetupToken(browser.location.href);
  // Scrub immediately, then wait for the router's initial navigation. The app
  // shell can mount before that navigation has populated currentRoute.
  if (result.changed) replaceBrowserSetupToken(browser);
  await router.isReady();
  const route = stripSetupTokenQuery(router.currentRoute.value.fullPath);
  const current = stripManagementSetupToken(browser.location.href);
  if (!result.changed && !current.changed && !route.changed) return "";

  const state = replaceBrowserSetupToken(browser);
  router.options.history.replace(route.location, state);
  if (route.changed) {
    // The string form preserves unrelated query escaping and the route anchor.
    await router.replace(route.location);
    router.options.history.replace(route.location, state);
  }
  return result.token;
}

function replaceBrowserSetupToken(browser: Pick<Window, "location" | "history">): HistoryState {
  const state: HistoryState = { ...browser.history.state };
  // Vue Router retains these route strings separately from the address bar.
  // A later push must not put the old query back into the current entry.
  for (const key of ["back", "current", "forward"]) {
    if (typeof state[key] === "string") state[key] = stripSetupTokenQuery(state[key]).location;
  }
  browser.history.replaceState(state, "", stripManagementSetupToken(browser.location.href).location);
  return state;
}
