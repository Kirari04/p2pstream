import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";

test("keeps unsaved GeoIP credentials through automatic configuration refresh", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  await page.goto("/#/policies/waf");
  await expect(page.getByRole("heading", { name: "Visitor identity & GeoIP" })).toBeVisible();

  const accountId = page.getByLabel("MaxMind account ID");
  await accountId.fill("987654321");
  await expect(page.getByRole("button", { name: "Discard changes" })).toBeVisible();

  // Management configuration polls every five seconds. The in-progress secret
  // draft must not be replaced by the next authoritative response.
  await page.waitForTimeout(5_500);
  await expect(accountId).toHaveValue("987654321");

  await page.getByRole("button", { name: "Discard changes" }).click();
  await expect(accountId).not.toHaveValue("987654321");
});
