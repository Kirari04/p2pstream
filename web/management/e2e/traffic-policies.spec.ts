import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";
import { connectRPC } from "./helpers/connect";

test("keeps policy rules close to navigation and scopes filters to each policy", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  await page.goto("/#/policies/rate-limits");

  await expect(page.getByRole("heading", { name: "Traffic policy", exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: /Rate limits, \d+ of \d+ enabled/ })).toBeVisible();
  await expect(page.locator('.policy-local-filter[aria-label="Filter rate-limit rules"]')).toBeVisible();

  const executionOrder = page.locator(".policy-execution-disclosure");
  await expect(executionOrder).not.toHaveAttribute("open", "");
  await executionOrder.locator("summary").click();
  await expect(executionOrder).toHaveAttribute("open", "");
  await expect(executionOrder.getByLabel("Traffic policy execution order")).toBeVisible();

  await page.getByRole("tab", { name: /WAF, \d+ of \d+ enabled/ }).click();
  await expect(page).toHaveURL(/#\/policies\/waf$/);
  await expect(page.locator('.policy-local-filter[aria-label="Filter WAF rules"]')).toBeVisible();
  const wafPages = page.getByRole("navigation", { name: "WAF pages" });
  await expect(wafPages.getByRole("link", { name: "Rules", exact: true })).toHaveAttribute("aria-current", "page");
  await expect(page.locator(".page-header").getByRole("button", { name: "Add WAF rule", exact: true })).toBeVisible();

  await wafPages.getByRole("link", { name: "Captcha providers", exact: true }).click();
  await expect(page).toHaveURL(/#\/policies\/waf\/captcha-providers$/);
  await expect(wafPages.getByRole("link", { name: "Captcha providers", exact: true })).toHaveAttribute("aria-current", "page");
  await expect(page.getByRole("tab", { name: /WAF, \d+ of \d+ enabled/ })).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".page-header").getByRole("button", { name: "Add captcha provider", exact: true })).toBeVisible();
  const breadcrumb = page.getByRole("navigation", { name: "Breadcrumb" });
  await expect(breadcrumb.getByRole("link", { name: "WAF", exact: true })).toBeVisible();
  await expect(breadcrumb.getByText("Captcha providers", { exact: true })).toHaveAttribute("aria-current", "page");

  await page.getByRole("tab", { name: "Visitor identity", exact: true }).click();
  await expect(page).toHaveURL(/#\/policies\/visitor-identity$/);
  await expect(page.getByRole("heading", { name: "GeoLite2 Country" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Trusted proxies & CDNs" })).toBeVisible();
  await page.locator(".page-header").getByRole("button", { name: "Add custom source", exact: true }).click();
  const proxyEditor = page.getByRole("dialog", { name: "Add trusted proxy source" });
  await expect(proxyEditor).toBeVisible();
  await proxyEditor.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(proxyEditor).toBeHidden();
});

test("opens exact values as a full-width row without growing the rule row", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  const name = `exact-values-${Date.now().toString(36)}`;
  const celExpression = 'host == "exact.example.test" && path_prefix(path, "/api/v1/very/long/path/segment")';
  const created = await connectRPC<{ rule: { id: string } }>(page.request, baseURL, "CreatePublicRateLimitRule", {
    name,
    enabled: false,
    priority: 9000,
    algorithm: "PUBLIC_RATE_LIMIT_ALGORITHM_FIXED_WINDOW",
    limit: 100,
    windowMillis: "60000",
    matchRule: { celExpression },
    keyParts: [{ source: "PUBLIC_RATE_LIMIT_KEY_SOURCE_REMOTE_IP" }],
    responseStatusCode: 429,
  });

  try {
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto("/#/policies/rate-limits");
    await page.getByRole("textbox", { name: /Filter rate-limit rules/ }).fill(name);
    const table = page.getByRole("table", { name: "Rate-limit rules" });
    const dataRow = table.locator(".policy-data-row").filter({ hasText: name });
    await expect(dataRow).toHaveCount(1);
    const rowHeight = (await dataRow.boundingBox())?.height ?? 0;

    const toggle = dataRow.getByRole("button", { name: `Exact values for ${name}` });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    const panelId = await toggle.getAttribute("aria-controls");
    expect(panelId).toBeTruthy();
    const panel = page.locator(`[id="${panelId}"]`);
    await expect(panel).toHaveAttribute("role", "row");
    await expect(panel.getByText(celExpression, { exact: true })).toBeVisible();
    await expect(panel).not.toContainText("PUBLIC_POLICY_MATCH");
    expect(Math.abs(((await dataRow.boundingBox())?.height ?? 0) - rowHeight)).toBeLessThanOrEqual(1);
    const [panelWidth, tableWidth] = await Promise.all([
      panel.evaluate((element) => element.getBoundingClientRect().width),
      table.evaluate((element) => element.getBoundingClientRect().width),
    ]);
    expect(panelWidth).toBeGreaterThan(tableWidth - 2);

    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(panel).toHaveCount(0);
  } finally {
    await connectRPC(page.request, baseURL, "DeletePublicRateLimitRule", { id: created.rule.id });
  }
});

test("protects a cache settings draft from refresh and navigation", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  await page.goto("/#/policies/cache");

  const diskBudget = page.getByLabel("Disk MiB");
  await diskBudget.fill("2048");
  await diskBudget.press("Tab");
  await expect(page.getByText("Unsaved changes", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Discard changes" })).toBeVisible();

  // Management configuration polls every five seconds. A local cache draft
  // must remain authoritative in the form until the operator resolves it.
  await page.waitForTimeout(5_500);
  await expect(diskBudget).toHaveValue("2048");

  await page.getByRole("tab", { name: /WAF, \d+ of \d+ enabled/ }).click();
  const discardDialog = page.getByRole("dialog");
  await expect(discardDialog.getByText("Discard unsaved cache settings?", { exact: true })).toBeVisible();
  await discardDialog.getByRole("button", { name: "Cancel" }).click();
  await expect(page).toHaveURL(/#\/policies\/cache$/);
  await expect(diskBudget).toHaveValue("2048");

  await page.getByRole("tab", { name: /WAF, \d+ of \d+ enabled/ }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Discard changes" }).click();
  await expect(page).toHaveURL(/#\/policies\/waf$/);
});

test("describes the consequence before purging every cached object", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  await page.goto("/#/policies/cache");

  await page.getByRole("button", { name: "Purge all cached objects" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("Purge all cached objects?", { exact: true })).toBeVisible();
  await expect(dialog.getByText(/Requests will miss the cache until eligible responses are stored again/)).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Purge all cached objects" })).toBeVisible();
  await dialog.getByRole("button", { name: "Cancel" }).click();
});
