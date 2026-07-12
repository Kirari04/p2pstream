import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";

test("keeps policy rules close to navigation and scopes filters to each policy", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  await page.goto("/#/policies/rate-limits");

  await expect(page.getByRole("heading", { name: "Traffic Policy", exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: /Rate Limits, \d+ of \d+ enabled/ })).toBeVisible();
  await expect(page.locator('.policy-local-filter[aria-label="Filter rate-limit rules"]')).toBeVisible();

  const executionOrder = page.locator(".policy-execution-disclosure");
  await expect(executionOrder).not.toHaveAttribute("open", "");
  await executionOrder.locator("summary").click();
  await expect(executionOrder).toHaveAttribute("open", "");
  await expect(executionOrder.getByLabel("Traffic policy execution order")).toBeVisible();

  await page.getByRole("tab", { name: /WAF, \d+ of \d+ enabled/ }).click();
  await expect(page).toHaveURL(/#\/policies\/waf$/);
  await expect(page.locator('.policy-local-filter[aria-label="Filter WAF rules"]')).toBeVisible();
  await expect(page.getByRole("heading", { name: "Visitor identity & GeoIP" })).toBeVisible();
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
