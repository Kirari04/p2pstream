import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";

test("renders the traffic flow diagram through the management page", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);

  await page.goto("/#/traffic");
  await expect(page.getByRole("heading", { name: "Traffic Flow", exact: true })).toBeVisible();

  const diagram = page.locator(".traffic-flow-shell");
  await expect(diagram).toBeVisible();
  await expect(diagram.locator(".traffic-flow-node-ingress")).toBeVisible();
  await expect(diagram.locator(".traffic-flow-node-response")).toBeVisible();
  expect(await diagram.locator(".traffic-flow-node").count()).toBeGreaterThanOrEqual(2);
});
