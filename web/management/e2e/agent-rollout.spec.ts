import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "vite-direct", "The isolated view fixture uses Vite source modules.");
  await page.route("**/p2pstream.v1.AgentManagementService/**", (route) => route.abort());
});

test("campaign errors remain inside the modal, preserve the plan, and support retry", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/e2e/fixtures/agent-rollout.html#/agent/updates");
  await page.getByRole("button", { name: "Plan rollout", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox", { name: "Campaign name", exact: true }).fill("My rollout");
  await dialog.getByRole("button", { name: "Preview safety", exact: true }).click();
  const start = dialog.getByRole("button", { name: "Start campaign", exact: true });
  await expect(start).toBeEnabled();
  await start.click();
  await expect(dialog.getByRole("button", { name: "Cancel", exact: true })).toBeDisabled();
  const error = dialog.getByRole("alert").filter({ hasText: "Unable to start campaign" });
  await expect(error).toContainText("[internal] 21 values for 20 columns");
  await expect(error).toBeInViewport();
  await expect(start).toBeInViewport();
  await expect(dialog.getByRole("textbox", { name: "Campaign name", exact: true })).toHaveValue("My rollout");
  await expect(dialog.getByRole("checkbox", { checked: true })).toHaveCount(8);
  expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
  await expect(page.getByLabel("Fixture requests")).toHaveText("Preview requests: 1; Create requests: 1; Global errors: 0");
  await start.click();
  await expect(dialog).toBeHidden();
  await expect(page.getByText("My rollout", { exact: true })).toBeVisible();
  await expect(page.getByLabel("Fixture requests")).toHaveText("Preview requests: 1; Create requests: 2; Global errors: 0");
});

test("failed safety refresh invalidates the previous preview and displays its error in the modal", async ({ page }) => {
  await page.goto("/e2e/fixtures/agent-rollout.html?failure=preview#/agent/updates");
  await page.getByRole("button", { name: "Plan rollout", exact: true }).click();
  const dialog = page.getByRole("dialog");
  const preview = dialog.getByRole("button", { name: "Preview safety", exact: true });
  const start = dialog.getByRole("button", { name: "Start campaign", exact: true });
  await preview.click();
  await expect(start).toBeEnabled();
  await preview.click();
  await expect(dialog.getByRole("alert").filter({ hasText: "Safety preview failed" })).toContainText("could not reach the selected environment");
  await expect(start).toBeDisabled();
  await preview.click();
  await expect(start).toBeEnabled();
  await expect(dialog.getByText("Safety preview failed", { exact: true })).toBeHidden();
  await dialog.getByRole("textbox", { name: "Campaign name", exact: true }).fill("Changed plan");
  await expect(start).toBeDisabled();
  await expect(dialog.getByText("Rollout settings changed after the last safety preview. Preview again before starting.")).toBeVisible();
});
