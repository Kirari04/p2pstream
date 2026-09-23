import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";
import { connectRPC } from "./helpers/connect";

// Backend migration equivalence and rollback are exercised through the Go
// management API tests. This fixture isolates review, scope and consent UI.
test("reviews explicit route copies and migrates only selected listeners", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  const existing = await connectRPC<{ listeners: Array<{ id: string; name: string }>; routes: Array<Record<string, unknown>> }>(page.request, baseURL, "GetPublicProxyConfig", {});
  const listener = existing.listeners[0]!;
  const blockedId = "97002";
  let applied = false;
  const previewScopes: string[][] = [];
  const appliedPayloads: Array<{ revision: string; listenerIds: string[]; acceptedWarningCodes: string[] }> = [];
  const routeBase = { enabled: true, siteId: "0", action: "PUBLIC_ROUTE_ACTION_REDIRECT", redirectTargetMode: "PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH", redirectTarget: "/welcome", redirectStatusCode: "302" };
  await page.route("**/p2pstream.v1.AgentManagementService/GetPublicProxyConfig", async (route) => {
    const response = await route.fetch();
    const config = await response.json();
    if (!applied) {
      config.listeners.push({ ...listener, id: blockedId, name: "Needs wildcard review", protocol: "PUBLIC_LISTENER_PROTOCOL_HTTP", port: "39002", enabled: true });
      config.routes.push(
        { ...routeBase, id: "97011", listenerId: listener.id, priority: "10", hostPattern: "app.example.test", pathPrefix: "/app" },
        { ...routeBase, id: "97012", listenerId: listener.id, priority: "100", pathPrefix: "", isDefault: true },
        { ...routeBase, id: "97013", listenerId: blockedId, priority: "10", hostPattern: "*.example.test", pathPrefix: "/" },
      );
    }
    await route.fulfill({ response, json: config });
  });
  await page.route("**/p2pstream.v1.AgentManagementService/PreviewPublicSiteMigration", async (route) => {
    const payload = route.request().postDataJSON() as { listenerIds: string[] };
    previewScopes.push(payload.listenerIds);
    const blocked = payload.listenerIds.includes(blockedId);
    await route.fulfill({ status: 200, contentType: "application/json", json: {
      revision: blocked ? "blocked-preview" : "selected-preview",
      canApply: !blocked,
      groups: [{
        key: "application", listenerId: listener.id, listenerName: listener.name,
        proposedSiteName: "Application", hostnameMode: "PUBLIC_SITE_MIGRATION_HOSTNAME_MODE_SPECIFIC",
        hostnamePatterns: ["app.example.test"], sourceRouteIds: ["97011"],
        routeCopies: [{ sourceRouteId: "97012", destinationGroupKey: "application", reason: "Preserve the listener-wide fallback" }],
      }],
      issues: blocked ? [{ code: "legacy_wildcard_semantics", severity: "PUBLIC_SITE_MIGRATION_SEVERITY_BLOCKER", listenerId: blockedId, routeIds: ["97013"], summary: "Wildcard route needs review", detail: "Review wildcard depth before migrating this listener." }] : [{
        code: "strict_authority_parsing", severity: "PUBLIC_SITE_MIGRATION_SEVERITY_WARNING",
        listenerId: listener.id, routeIds: ["97011", "97012"], summary: "Site authority parsing is stricter",
        detail: "Sites reject malformed authorities and canonicalize DNS names.", requiresAcknowledgement: true,
      }],
    } });
  });
  await page.route("**/p2pstream.v1.AgentManagementService/ApplyPublicSiteMigration", async (route) => {
    appliedPayloads.push(route.request().postDataJSON());
    if (appliedPayloads.length === 1) {
      await route.fulfill({ status: 400, contentType: "application/json", json: { code: "failed_precondition", message: "Configuration changed since this preview." } });
      return;
    }
    applied = true;
    await route.fulfill({ status: 200, contentType: "application/json", json: { revision: "applied-preview" } });
  });

  await page.goto("/#/proxy/sites");
  const panel = page.locator("section").filter({ has: page.getByRole("heading", { name: "3 standalone routes need a Site", exact: true }) });
  await panel.getByRole("button", { name: "Preview migration", exact: true }).click();
  await expect(panel.getByText("Blocked", { exact: true })).toBeVisible();
  await expect(panel.getByRole("alert")).toContainText("1 blocker must be resolved");
  await expect(panel.getByText("Resolve before migration", { exact: true })).toBeVisible();
  await expect(panel.getByText("*.example.test · /", { exact: true })).toBeVisible();
  await expect(panel.getByRole("button", { name: "Apply migration", exact: true })).toBeDisabled();
  await panel.getByRole("checkbox", { name: "Needs wildcard review", exact: true }).uncheck();
  await expect(panel.getByText("Blocked", { exact: true })).toHaveCount(0);
  await panel.getByRole("button", { name: "Preview migration", exact: true }).click();
  await expect(panel.getByText("Can apply", { exact: true })).toBeVisible();
  await panel.getByText("Review routes and copies", { exact: true }).click();
  await expect(panel.getByText("Keep route #97011 · /app", { exact: true })).toBeVisible();
  await expect(panel.getByText("Copy route #97012 · Default path", { exact: true })).toBeVisible();
  const applyButton = panel.getByRole("button", { name: "Apply migration", exact: true });
  await expect(applyButton).toBeDisabled();
  await panel.getByRole("checkbox", { name: "Site authority parsing is stricter", exact: true }).check();
  await expect(applyButton).toBeEnabled();
  await panel.screenshot({ path: testInfo.outputPath("migration-preview-desktop.png") });
  await page.setViewportSize({ width: 375, height: 812 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);
  await panel.screenshot({ path: testInfo.outputPath("migration-preview-mobile.png") });
  await applyButton.click();
  await page.getByRole("button", { name: "Apply migration", exact: true }).last().click();
  await expect(panel.getByRole("alert")).toContainText("Configuration changed since this preview.");
  await expect(panel.getByText("Keep route #97011 · /app", { exact: true })).toBeVisible();
  await panel.getByRole("button", { name: "Refresh preview", exact: true }).click();
  await expect(panel.getByRole("alert")).toHaveCount(0);
  await expect(applyButton).toBeDisabled();
  await panel.getByRole("checkbox", { name: "Site authority parsing is stricter", exact: true }).check();
  await applyButton.click();
  await page.getByRole("button", { name: "Apply migration", exact: true }).last().click();
  await expect(panel).toBeHidden();
  expect(previewScopes).toEqual([[listener.id, blockedId], [listener.id], [listener.id]]);
  expect(appliedPayloads).toEqual(Array(2).fill({ revision: "selected-preview", listenerIds: [listener.id], acceptedWarningCodes: ["strict_authority_parsing"] }));
});
