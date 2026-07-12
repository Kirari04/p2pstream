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
  await expect(page.getByText(/Keyboard node list/)).toBeVisible();
});

test("waits for authoritative trace settings before enabling controls", async ({ page }, testInfo) => {
  let releaseSettings = () => {};
  const settingsGate = new Promise<void>((resolve) => {
    releaseSettings = resolve;
  });

  await page.route("**/p2pstream.v1.AgentManagementService/GetTrafficTraceSettings", async (route) => {
    await settingsGate;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        settings: {
          enabled: false,
          level: "TRAFFIC_TRACE_LEVEL_BASIC",
          updatedAtUnixMillis: "1700000000000",
          emittedEvents: "12",
          droppedEvents: "0",
          subscriberCount: "0",
        },
      }),
    });
  });

  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  await page.goto("/#/monitor/traffic");

  const tracing = page.getByRole("checkbox", { name: "Tracing" });
  await expect(page.getByText("Loading settings", { exact: true }).first()).toBeVisible();
  await expect(tracing).toBeDisabled();
  await expect(page.getByRole("radio", { name: "Basic" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Pause live updates" })).toBeDisabled();

  releaseSettings();
  await expect(page.getByText("Disabled", { exact: true }).first()).toBeVisible();
  await expect(tracing).toBeEnabled();
  await expect(tracing).not.toBeChecked();
  await expect(page.getByText("12", { exact: true })).toBeVisible();
});

test("routes monitor traffic and diagnostics subpages", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);

  await page.goto("/#/monitor");
  await expect(page).toHaveURL(/#\/monitor\/traffic$/);
  const monitorNav = page.getByLabel("Management navigation").getByRole("link", { name: "Monitor" });
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

test("keeps traffic and diagnostics workflows contained on mobile", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL, { waitForEnvironmentSelect: false });

  await page.goto("/#/monitor/traffic");
  await expect(page.getByRole("textbox", { name: "Search recent traces" })).toBeVisible();
  await expect(page.getByRole("combobox", { name: "Filter recent traces" })).toBeVisible();
  await expect(page.getByText(/Keyboard node list/)).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);

  await page.goto("/#/monitor/diagnostics");
  await expect(page.getByRole("group", { name: "Diagnostics window" })).toBeVisible();
  await expect(page.getByRole("combobox", { name: "Sample limit" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Recent Samples", exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});
