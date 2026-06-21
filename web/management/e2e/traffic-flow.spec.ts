import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";

test("renders the traffic flow diagram through the management page", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);

  await page.goto("/#/monitor/traffic");
  await expect(page.getByRole("heading", { name: "Traffic Flow", exact: true })).toBeVisible();

  const diagram = page.locator(".traffic-flow-shell");
  await expect(diagram).toBeVisible();
  await expect(diagram.locator(".traffic-flow-node-ingress")).toBeVisible();
  await expect(diagram.locator(".traffic-flow-node-response")).toBeVisible();
  expect(await diagram.locator(".traffic-flow-node").count()).toBeGreaterThanOrEqual(2);
});

test("routes monitor traffic and diagnostics subpages", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);

  await page.goto("/#/monitor");
  await expect(page).toHaveURL(/#\/monitor\/traffic$/);
  const monitorNav = page.getByRole("link", { name: "Monitor" });
  await expect(monitorNav).toHaveClass(/app-nav__link--active/);
  await expect(page.getByRole("heading", { name: "Traffic Flow", exact: true })).toBeVisible();
  await expect(page.locator(".traffic-flow-shell")).toBeVisible();

  await page.locator(".monitor-tabs").getByText("Diagnostics", { exact: true }).click();
  await expect(page).toHaveURL(/#\/monitor\/diagnostics$/);
  await expect(monitorNav).toHaveClass(/app-nav__link--active/);
  await expect(page.getByRole("heading", { name: "Diagnostics", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Status Codes", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Recent Samples", exact: true })).toBeVisible();
});

test("removed legacy traffic and diagnostics URLs show not found", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  const legacyHashPath = (pageName: "traffic" | "diagnostics") => `/#/${pageName}`;

  await page.goto(legacyHashPath("traffic"));
  await expect(page.getByText("Page not found", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Traffic Flow", exact: true })).toBeHidden();

  await page.goto(legacyHashPath("diagnostics"));
  await expect(page.getByText("Page not found", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Diagnostics", exact: true })).toBeHidden();
});
