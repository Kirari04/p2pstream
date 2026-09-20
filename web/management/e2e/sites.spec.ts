import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";
import { connectRPC } from "./helpers/connect";
import { chooseNaiveSelectOption } from "./helpers/naive";

type ProxyConfig = {
  sites: Array<{ id: string; name: string; hosts: Array<{ hostnamePattern: string }> }>;
  routes: Array<{ id: string; siteId: string; pathPrefix: string }>;
};

test("creates a Site and explicitly binds a path route to it", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  const slug = testInfo.project.name.replace(/[^a-z0-9]+/gi, "-").toLowerCase();
  const siteName = `e2e-site-${slug}`;
  const primary = `${slug}.sites.e2e.example`;
  const alias = `www.${primary}`;
  const pathPrefix = `/bound-${slug}`;

  await page.goto("/#/proxy/sites");
  await expect(page.getByRole("heading", { name: "Sites", exact: true })).toBeVisible();
  const sitesSurface = page.locator("section").filter({ has: page.getByRole("heading", { name: "Sites", exact: true }) });
  await sitesSurface.locator(".workbench-section-header").getByRole("button", { name: "Add Site", exact: true }).click();
  const editor = page.getByRole("dialog", { name: "Add Site" });
  await expect(editor).toBeVisible();
  await expect(editor.getByText(/Saving claims every hostname immediately/)).toBeVisible();
  await editor.getByLabel("Site name").fill(siteName);
  await editor.getByLabel("Exact hostname").fill(primary);
  await editor.getByRole("button", { name: "Add alias", exact: true }).click();
  await editor.getByLabel("Alias 1 hostname").fill(alias);
  await chooseNaiveSelectOption(page, editor.getByRole("combobox", { name: "Alias 1 behavior" }), "Redirect to primary · 308");
  await Promise.all([
    page.waitForResponse((response) => response.url().includes("/p2pstream.v1.AgentManagementService/CreatePublicSite") && response.status() === 200),
    editor.getByRole("button", { name: "Create site", exact: true }).click(),
  ]);

  const siteRow = page.locator("[data-testid^='site-row-']").filter({ hasText: siteName });
  await expect(siteRow).toBeVisible();
  await expect(siteRow).toContainText(primary);
  await expect(siteRow).toContainText("→308");

  for (const width of [375, 900, 1100, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    const geometry = await siteRow.evaluate((row) => {
      const surface = row.closest("section");
      const actions = row.querySelector(".site-table__actions");
      if (!surface || !actions) return null;
      const surfaceRect = surface.getBoundingClientRect();
      const rowRect = row.getBoundingClientRect();
      const actionsRect = actions.getBoundingClientRect();
      return {
        surfaceLeft: surfaceRect.left,
        surfaceRight: surfaceRect.right,
        rowLeft: rowRect.left,
        rowRight: rowRect.right,
        actionsRight: actionsRect.right,
        documentOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      };
    });
    expect(geometry, `missing Sites geometry at ${width}px`).not.toBeNull();
    expect(geometry!.rowLeft, `row clipped left at ${width}px`).toBeGreaterThanOrEqual(geometry!.surfaceLeft - 1);
    expect(geometry!.rowRight, `row clipped right at ${width}px`).toBeLessThanOrEqual(geometry!.surfaceRight + 1);
    expect(geometry!.actionsRight, `actions clipped at ${width}px`).toBeLessThanOrEqual(geometry!.surfaceRight + 1);
    expect(geometry!.documentOverflow, `page overflows at ${width}px`).toBeLessThanOrEqual(1);
  }
  await page.setViewportSize({ width: 1280, height: 900 });

  await page.getByRole("tab", { name: /routes configured/i }).click();
  await expect(page).toHaveURL(/#\/proxy\/routes$/);
  const routesSurface = page.locator("section").filter({ has: page.getByRole("heading", { name: "Routes", exact: true }) });
  await routesSurface.locator(".workbench-section-header").getByRole("button", { name: "Add Route", exact: true }).click();
  const routeEditor = page.getByRole("dialog", { name: "Add Route" });
  await chooseNaiveSelectOption(page, routeEditor.getByRole("combobox", { name: "Route site or legacy host rule" }), siteName);
  await expect(routeEditor.getByText(/Requests for claimed hosts never fall through/)).toBeVisible();
  await expect(routeEditor.getByLabel("Legacy host pattern")).toHaveCount(0);
  await routeEditor.getByLabel("Path prefix").fill(pathPrefix);
  await chooseNaiveSelectOption(page, routeEditor.getByRole("combobox", { name: "Route action" }), "Redirect");
  await routeEditor.getByRole("textbox", { name: /^Target\b/ }).fill("/site-home");
  await Promise.all([
    page.waitForResponse((response) => response.url().includes("/p2pstream.v1.AgentManagementService/CreatePublicRoute") && response.status() === 200),
    routeEditor.getByRole("button", { name: "Create Route", exact: true }).click(),
  ]);

  const config = await connectRPC<ProxyConfig>(page.request, baseURL, "GetPublicProxyConfig", {});
  const site = config.sites.find((item) => item.name === siteName);
  expect(site?.hosts.map((host) => host.hostnamePattern)).toEqual([primary, alias]);
  const route = config.routes.find((item) => item.pathPrefix === pathPrefix);
  expect(route?.siteId).toBe(site?.id);
});
