import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";
import { connectRPC } from "./helpers/connect";
import { chooseNaiveSelectOption } from "./helpers/naive";

type ProxyConfig = {
  sites: Array<{
    id: string;
    name: string;
    published: boolean;
    listenerBindings: Array<{ listenerId: string }>;
    hosts: Array<{ hostnamePattern: string }>;
  }>;
  routes: Array<{
    id: string;
    siteId: string;
    pathPrefix: string;
    listenerId: string;
    isDefault: boolean;
  }>;
};
type CreateListenerResponse = { listener?: { id: string; name: string } };

test("keeps the Site workspace open through draft, route, clone, failure, keyboard, and mobile flows", async ({
  page,
}, testInfo) => {
  test.setTimeout(120_000);
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  const slug = testInfo.project.name.replace(/[^a-z0-9]+/gi, "-").toLowerCase();
  const siteName = `e2e-site-${slug}`;
  const primary = `${slug}.sites.e2e.example`;
  const listenerName = `site-http-${slug}`;
  const httpsListenerName = `site-https-${slug}`;
  const routeCreatePayloads: Array<{ listenerId?: string; siteId?: string }> =
    [];
  page.on("request", (request) => {
    if (request.url().includes("/CreatePublicRoute"))
      routeCreatePayloads.push(
        request.postDataJSON() as { listenerId?: string; siteId?: string },
      );
  });
  const listenerResponse = await connectRPC<CreateListenerResponse>(
    page.request,
    baseURL,
    "CreatePublicListener",
    {
      name: listenerName,
      bindAddress: "127.0.0.1",
      port: "39181",
      protocol: "PUBLIC_LISTENER_PROTOCOL_HTTP",
      enabled: true,
    },
  );
  expect(listenerResponse.listener?.id).toBeTruthy();
  const httpsListenerResponse = await connectRPC<CreateListenerResponse>(
    page.request,
    baseURL,
    "CreatePublicListener",
    {
      name: httpsListenerName,
      bindAddress: "127.0.0.1",
      port: "39443",
      protocol: "PUBLIC_LISTENER_PROTOCOL_HTTPS",
      enabled: true,
    },
  );
  expect(httpsListenerResponse.listener?.id).toBeTruthy();

  await page.goto("/#/proxy/sites");
  await expect(
    page.getByRole("heading", { name: "Sites", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("tab", { name: /routes configured/i }),
  ).toHaveCount(0);
  const sitesSurface = page
    .locator("section.surface-card")
    .filter({ has: page.getByRole("heading", { name: "Sites", exact: true }) });
  await sitesSurface
    .locator(".workbench-section-header")
    .getByRole("button", { name: "Add Site", exact: true })
    .click();

  let workspace = page.getByRole("dialog", { name: "Create Site workspace" });
  await expect(workspace).toBeVisible();
  await workspace.getByLabel("Site name").fill(siteName);
  await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().includes("/CreatePublicSite") &&
        response.status() === 200,
    ),
    workspace.getByRole("button", { name: "Create draft Site" }).click(),
  ]);

  workspace = page.getByRole("dialog", { name: "Site workspace" });
  await expect(workspace).toBeVisible();
  await expect(workspace.getByText("Draft", { exact: true })).toBeVisible();
  await expect(
    workspace.getByRole("button", { name: "Add the first route" }),
  ).toBeVisible();

  await workspace.getByRole("button", { name: "Add hostname" }).click();
  await workspace.getByRole("textbox", { name: /^Hostname 1\b/ }).fill(primary);
  await workspace.getByLabel("Canonical hostname").fill(primary);
  await workspace
    .getByRole("button", { name: "Any unmatched hostname" })
    .click();
  await workspace.getByRole("button", { name: "Specific hostnames" }).click();
  await expect(workspace.getByLabel("Canonical hostname")).toHaveValue(primary);
  await workspace.getByRole("button", { name: "Add listener" }).click();
  await chooseNaiveSelectOption(
    page,
    workspace.getByRole("combobox", {
      name: "Listener assignment 1",
      exact: true,
    }),
    listenerName,
  );
  await workspace.getByRole("button", { name: "Add listener" }).click();
  await chooseNaiveSelectOption(
    page,
    workspace.getByRole("combobox", {
      name: "Listener assignment 2",
      exact: true,
    }),
    httpsListenerName,
  );
  await workspace.getByLabel("Site name").fill(`${siteName}-unsaved`);

  await workspace.getByRole("button", { name: "Add the first route" }).click();
  let routeEditor = page.getByRole("dialog", { name: "Add Route" });
  await expect(routeEditor.getByText(siteName)).toBeVisible();
  await expect(
    routeEditor.getByRole("combobox", { name: "Route listener" }),
  ).toHaveCount(0);
  await routeEditor.getByLabel("Path prefix").fill("/home");
  await chooseNaiveSelectOption(
    page,
    routeEditor.getByRole("combobox", { name: "Route action" }),
    "Redirect",
  );
  await routeEditor.locator('input[placeholder="/new-path"]').fill("/welcome");
  await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().includes("/CreatePublicRoute") &&
        response.status() === 200,
    ),
    routeEditor.getByRole("button", { name: "Create Route" }).click(),
  ]);
  await expect(routeEditor).toBeHidden();
  await expect(workspace).toBeVisible();
  await expect(workspace.getByLabel("Site name")).toHaveValue(
    `${siteName}-unsaved`,
  );
  await expect(
    workspace.getByText(
      "Route saved. Your unsaved Site settings are still here.",
    ),
  ).toBeVisible();
  await expect(workspace.getByText("/home", { exact: true })).toBeVisible();

  await workspace.getByRole("button", { name: "Add route" }).click();
  routeEditor = page.getByRole("dialog", { name: "Add Route" });
  await routeEditor.getByLabel("Path prefix").fill("/api");
  await chooseNaiveSelectOption(
    page,
    routeEditor.getByRole("combobox", { name: "Target 1 type" }),
    "Static",
  );
  await routeEditor.getByRole("button", { name: "Add Target" }).click();
  await chooseNaiveSelectOption(
    page,
    routeEditor.getByRole("combobox", { name: "Target 2 type" }),
    "Static",
  );
  await routeEditor.locator("textarea").first().fill("primary static response");
  await routeEditor.locator("textarea").nth(1).fill("fallback static response");
  await routeEditor.getByText("Advanced target settings").first().click();
  const firstTargetAdvanced = routeEditor
    .locator("details.target-advanced")
    .first();
  await firstTargetAdvanced.getByRole("button", { name: "Add header" }).click();
  await firstTargetAdvanced.locator("input").first().fill("X-Workspace-Test");
  await firstTargetAdvanced.locator("input").nth(1).fill("nested");
  await routeEditor.screenshot({
    path: testInfo.outputPath("site-route-editor-desktop.png"),
  });
  await routeEditor.getByRole("button", { name: "Create Route" }).click();
  await expect(routeEditor).toBeHidden();
  await expect(workspace.getByText("/api", { exact: true })).toBeVisible();

  await workspace.getByRole("button", { name: "Add route" }).click();
  routeEditor = page.getByRole("dialog", { name: "Add Route" });
  await routeEditor.getByLabel("Path prefix").fill("/cancelled");
  await routeEditor.getByRole("button", { name: "Cancel" }).click();
  await page.getByRole("button", { name: "Discard changes" }).click();
  await expect(routeEditor).toBeHidden();
  await expect(workspace).toBeVisible();

  await workspace.getByRole("button", { name: "Add route" }).click();
  routeEditor = page.getByRole("dialog", { name: "Add Route" });
  await page.keyboard.press("Escape");
  await expect(routeEditor).toBeHidden();
  await expect(workspace).toBeVisible();

  await workspace.screenshot({
    path: testInfo.outputPath("site-workspace-desktop.png"),
  });
  await page.setViewportSize({ width: 375, height: 812 });
  await expect(workspace).toBeVisible();
  await workspace.getByLabel("Site name").scrollIntoViewIfNeeded();
  const mobileGeometry = await workspace.evaluate((element) => ({
    left: element.getBoundingClientRect().left,
    right: element.getBoundingClientRect().right,
    viewport: window.innerWidth,
    overflow:
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  }));
  expect(mobileGeometry.left).toBeGreaterThanOrEqual(-1);
  expect(mobileGeometry.right).toBeLessThanOrEqual(mobileGeometry.viewport + 1);
  expect(mobileGeometry.overflow).toBeLessThanOrEqual(1);
  const headerTitle = workspace.locator(".site-workspace__header h2");
  const lifecycle = workspace.getByText("Draft", { exact: true });
  const workspaceHeaderClose = workspace.locator(".n-drawer-header__close");
  for (const control of [headerTitle, lifecycle, workspaceHeaderClose]) {
    const bounds = await control.boundingBox();
    expect(bounds).not.toBeNull();
    expect(bounds?.x ?? -1).toBeGreaterThanOrEqual(0);
    expect((bounds?.x ?? 0) + (bounds?.width ?? 0)).toBeLessThanOrEqual(375);
  }
  await workspace.screenshot({
    path: testInfo.outputPath("site-workspace-mobile-top-375.png"),
  });

  const mobileRouteTrigger = workspace
    .getByRole("button", { name: "Edit route" })
    .first();
  await mobileRouteTrigger.click();
  routeEditor = page.getByRole("dialog", { name: "Edit Route" });
  await expect(routeEditor).toBeVisible();
  await expect
    .poll(() =>
      routeEditor.evaluate(
        (element) => element.getBoundingClientRect().right <= window.innerWidth + 1,
      ),
    )
    .toBe(true);
  const mobileChildGeometry = await routeEditor.evaluate((element) => ({
    left: element.getBoundingClientRect().left,
    right: element.getBoundingClientRect().right,
    viewport: window.innerWidth,
    overflow:
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  }));
  expect(mobileChildGeometry.left).toBeGreaterThanOrEqual(-1);
  expect(mobileChildGeometry.right).toBeLessThanOrEqual(
    mobileChildGeometry.viewport + 1,
  );
  expect(mobileChildGeometry.overflow).toBeLessThanOrEqual(1);
  await routeEditor.screenshot({
    path: testInfo.outputPath("site-route-editor-mobile-375.png"),
  });
  const routeHeaderClose = routeEditor.locator(".n-drawer-header__close");
  await routeHeaderClose.focus();
  await page.keyboard.press("Shift+Tab");
  expect(
    await routeEditor.evaluate((element) =>
      element.contains(document.activeElement),
    ),
  ).toBe(true);
  const routeSave = routeEditor.getByRole("button", { name: "Save Changes" });
  await routeSave.focus();
  await page.keyboard.press("Tab");
  expect(
    await routeEditor.evaluate((element) =>
      element.contains(document.activeElement),
    ),
  ).toBe(true);
  await page.keyboard.press("Escape");
  await expect(routeEditor).toBeHidden();
  await expect(workspace).toBeVisible();
  await expect(mobileRouteTrigger).toBeFocused();
  await page.setViewportSize({ width: 1280, height: 900 });

  await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().includes("/UpdatePublicSite") &&
        response.status() === 200,
    ),
    workspace.getByRole("button", { name: "Save Site settings" }).click(),
  ]);
  await expect(workspace.getByText("Site settings saved")).toBeVisible();
  const readinessToggle = workspace.locator(".site-readiness__summary");
  await expect(readinessToggle).toHaveAttribute("aria-expanded", "false");
  await readinessToggle.click();
  await expect(readinessToggle).toHaveAttribute("aria-expanded", "true");
  const configureTls = workspace.getByRole("button", {
    name: "Configure TLS",
  });
  await expect(configureTls).toBeVisible();
  await configureTls.click();
  const tlsEditor = page.getByRole("dialog", {
    name: "Configure TLS for Site",
  });
  await expect(tlsEditor).toBeVisible();
  await expect(
    tlsEditor.getByRole("combobox", { name: "HTTPS listener" }),
  ).toContainText(httpsListenerName);
  await expect(tlsEditor.getByLabel("Hostname pattern")).toHaveValue(primary);
  await tlsEditor.screenshot({
    path: testInfo.outputPath("site-tls-editor-desktop.png"),
  });
  await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().includes("/CreatePublicTlsCertificate") &&
        response.status() === 200,
    ),
    tlsEditor.getByRole("button", { name: "Create TLS Mapping" }).click(),
  ]);
  await expect(tlsEditor).toBeHidden();
  await expect(workspace).toBeVisible();
  await expect(
    workspace.getByRole("button", { name: "Publish Site" }),
  ).toBeEnabled();
  await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().includes("/PublishPublicSite") &&
        response.status() === 200,
    ),
    workspace.getByRole("button", { name: "Publish Site" }).click(),
  ]);
  await expect(workspace.getByText("Published", { exact: true })).toBeVisible();

  const editRouteButton = workspace
    .getByRole("button", { name: "Edit route" })
    .first();
  await editRouteButton.click();
  routeEditor = page.getByRole("dialog", { name: "Edit Route" });
  await routeEditor.getByLabel("Priority", { exact: true }).fill("42");
  await routeEditor.getByRole("button", { name: "Save Changes" }).click();
  await expect(routeEditor).toBeHidden();
  await expect(workspace.getByText("Priority 42")).toBeVisible();

  await workspace.getByRole("button", { name: "Clone route" }).first().click();
  routeEditor = page.getByRole("dialog", { name: "Clone Route" });
  await expect(routeEditor.getByLabel("Default route")).not.toBeChecked();
  await routeEditor.getByLabel("Path prefix").fill("/clone");
  await routeEditor.getByRole("button", { name: "Create Clone" }).click();
  await expect(workspace.getByText("/clone", { exact: true })).toBeVisible();
  const cloneRow = workspace.locator("[data-testid^='site-route-']").filter({
    hasText: "/clone",
  });
  await cloneRow.getByRole("button", { name: "Disable route /clone" }).click();
  await expect(cloneRow.getByText("Disabled", { exact: true })).toBeVisible();
  await cloneRow.getByRole("button", { name: "Move route earlier" }).click();
  await expect(
    cloneRow.getByText("Priority 41", { exact: true }),
  ).toBeVisible();

  await workspace.getByRole("button", { name: "Add route" }).click();
  routeEditor = page.getByRole("dialog", { name: "Add Route" });
  await routeEditor.getByLabel("Path prefix").fill("/retry-after-failure");
  await page.route(
    "**/p2pstream.v1.AgentManagementService/CreatePublicRoute",
    async (route) => route.abort("failed"),
  );
  await routeEditor.getByRole("button", { name: "Create Route" }).click();
  await expect(routeEditor).toBeVisible();
  await expect(routeEditor.getByLabel("Path prefix")).toHaveValue(
    "/retry-after-failure",
  );
  await expect(routeEditor.getByRole("alert")).toBeVisible();
  await page.unroute(
    "**/p2pstream.v1.AgentManagementService/CreatePublicRoute",
  );
  await routeEditor.getByRole("button", { name: "Cancel" }).click();
  await page.getByRole("button", { name: "Discard changes" }).click();

  const config = await connectRPC<ProxyConfig>(
    page.request,
    baseURL,
    "GetPublicProxyConfig",
    {},
  );
  const site = config.sites.find((item) => item.name === `${siteName}-unsaved`);
  expect(site?.published).toBe(true);
  expect(site?.hosts.map((host) => host.hostnamePattern)).toEqual([primary]);
  expect(site?.listenerBindings.map((binding) => binding.listenerId)).toContain(
    listenerResponse.listener?.id,
  );
  expect(
    config.routes
      .filter((route) => route.siteId === site?.id)
      .map((route) => route.pathPrefix)
      .sort(),
  ).toEqual(["/api", "/clone", "/home"]);
  expect(routeCreatePayloads.length).toBeGreaterThanOrEqual(3);
  expect(
    routeCreatePayloads.every(
      (payload) =>
        payload.listenerId === undefined || payload.listenerId === "0",
    ),
  ).toBe(true);
  expect(
    routeCreatePayloads.every((payload) => payload.siteId === site?.id),
  ).toBe(true);
});
