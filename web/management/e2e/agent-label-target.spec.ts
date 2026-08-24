import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";
import { connectRPC } from "./helpers/connect";
import { chooseNaiveSelectOption } from "./helpers/naive";

const agentPublicID = "playwright-agent";

type GetPublicProxyConfigResponse = {
  agents: Array<{
    id: string;
    publicId: string;
    name: string;
    enabled: boolean;
    labels: Record<string, string>;
  }>;
  routeTargets: Array<{
    name: string;
    agentSelector?: {
      matchLabels?: Record<string, string>;
    };
  }>;
};

test("configures agent labels and an agent-selected route target", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);

  const slug = testInfo.project.name.replace(/[^a-z0-9]+/gi, "-").toLowerCase();
  const targetName = `label-target-${slug}`;
  const routePath = `/e2e-label-${slug}`;

  let cfg = await connectRPC<GetPublicProxyConfigResponse>(page.request, baseURL, "GetPublicProxyConfig", {});
  const agent = cfg.agents.find((item) => item.publicId === agentPublicID);
  expect(agent, `missing ${agentPublicID}`).toBeTruthy();

  await connectRPC(page.request, baseURL, "UpdateAgent", {
    id: agent!.id,
    name: agent!.name,
    enabled: true,
    labels: {},
  });
  await page.reload();
  await expect(page.getByTestId("environment-select")).toBeVisible();

  await page.goto("/#/agent");
  await expect(page.getByRole("heading", { name: "Agents", exact: true })).toBeVisible();
  const agentRow = page.getByRole("row").filter({ hasText: agentPublicID }).first();
  await expect(agentRow).toBeVisible();
  await page.getByLabel("Search agents").fill(agentPublicID);
  await expect(agentRow).toBeVisible();
  await expect(agentRow.getByRole("button", { name: /Investigate agent/ })).toBeVisible();
  await expect(agentRow.getByRole("button", { name: /Edit agent/ })).toBeVisible();
  const moreActions = agentRow.getByRole("button", { name: /More actions for agent/ });
  await moreActions.click();
  await expect(page.getByRole("menuitem", { name: /Disable agent/ })).toBeVisible();
  await page.keyboard.press("Escape");
  await agentRow.getByText(/Identity & selectors/).click();
  await expect(agentRow.getByText(`p2pstream.io/agent-id=${agentPublicID}`, { exact: true })).toBeVisible();
  await agentRow.getByRole("button", { name: /Edit agent/ }).click();
  await expect(page.getByRole("heading", { name: "Edit Agent", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Add Label" }).click();
  await page.getByTestId("agent-label-row").nth(0).getByTestId("agent-label-key").fill("site");
  await page.getByTestId("agent-label-row").nth(0).getByTestId("agent-label-value").fill("loopback");
  await page.getByRole("button", { name: "Add Label" }).click();
  await page.getByTestId("agent-label-row").nth(1).getByTestId("agent-label-key").fill("role");
  await page.getByTestId("agent-label-row").nth(1).getByTestId("agent-label-value").fill("app");
  await expect(page.getByTestId("agent-system-label")).toContainText(`p2pstream.io/agent-id=${agentPublicID}`);
  await Promise.all([
    page.waitForResponse((response) => response.url().includes("/p2pstream.v1.AgentManagementService/UpdateAgent") && response.status() === 200),
    page.getByRole("button", { name: "Save Changes" }).click(),
  ]);

  await expect(page.getByText("site=loopback", { exact: true })).toBeVisible();
  await expect(page.getByText("role=app", { exact: true })).toBeVisible();
  cfg = await connectRPC<GetPublicProxyConfigResponse>(page.request, baseURL, "GetPublicProxyConfig", {});
  const labelledAgent = cfg.agents.find((item) => item.publicId === agentPublicID);
  expect(labelledAgent?.labels.site).toBe("loopback");
  expect(labelledAgent?.labels.role).toBe("app");
  expect(labelledAgent?.labels["p2pstream.io/agent-id"]).toBe(agentPublicID);

  await page.goto("/#/proxy");
  await expect(page.getByRole("heading", { name: "Proxy", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Add Route" }).click();
  await expect(page.getByRole("heading", { name: "Add Route", exact: true })).toBeVisible();
  await page.getByLabel("Path prefix").fill(routePath);
  const targetRow = page.getByTestId("route-target-row").first();
  await targetRow.getByLabel("Name").fill(targetName);
  await chooseNaiveSelectOption(page, targetRow.getByLabel("Transport"), "Agent");

  await chooseNaiveSelectOption(page, page.getByTestId("exact-agent-selector"), "Playwright Agent");
  await expect(page.getByTestId("target-selector-key").first()).toHaveValue("p2pstream.io/agent-id");
  await expect(page.getByTestId("target-selector-value").first()).toHaveValue(agentPublicID);

  await page.getByTestId("target-selector-row").nth(0).getByTestId("target-selector-key").fill("site");
  await page.getByTestId("target-selector-row").nth(0).getByTestId("target-selector-value").fill("loopback");
  await page.getByRole("button", { name: "Add Selector" }).click();
  await page.getByTestId("target-selector-row").nth(1).getByTestId("target-selector-key").fill("role");
  await page.getByTestId("target-selector-row").nth(1).getByTestId("target-selector-value").fill("app");
  await expect(page.getByTestId("selector-match-preview")).toContainText("Matches 1 enabled agents");
  await expect(page.getByTestId("selector-match-preview")).toContainText("Playwright Agent");

  await Promise.all([
    page.waitForResponse((response) => response.url().includes("/p2pstream.v1.AgentManagementService/CreatePublicRoute") && response.status() === 200),
    page.getByRole("button", { name: "Create Route" }).click(),
  ]);

  cfg = await connectRPC<GetPublicProxyConfigResponse>(page.request, baseURL, "GetPublicProxyConfig", {});
  const savedTarget = cfg.routeTargets.find((target) => target.name === targetName);
  expect(savedTarget?.agentSelector?.matchLabels).toEqual({
    site: "loopback",
    role: "app",
  });
});

test("protects an uncopied one-time agent setup command", async ({ page, context }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: new URL(baseURL).origin });
  await authenticate(page, baseURL);
  await page.goto("/#/agent");

  const slug = testInfo.project.name.replace(/[^a-z0-9]+/gi, "-").toLowerCase();
  const agentName = `Setup handoff ${slug}`;
  await page.getByRole("button", { name: "Add Agent" }).click();
  await expect(page.getByRole("heading", { name: "Add Agent", exact: true })).toBeVisible();
  await page.getByLabel("Name").fill(agentName);
  await Promise.all([
    page.waitForResponse((response) => response.url().includes("/p2pstream.v1.AgentManagementService/CreateAgent") && response.status() === 200),
    page.getByRole("button", { name: "Create Agent", exact: true }).click(),
  ]);

  await expect(page.getByRole("heading", { name: "Agent Setup", exact: true })).toBeVisible();
  const advancedSetup = page.locator("details.agent-advanced-options");
  await expect(advancedSetup).not.toHaveAttribute("open", "");
  await expect(page.getByLabel("GitHub Repository")).not.toBeVisible();

  await page.setViewportSize({ width: 962, height: 700 });
  await advancedSetup.click();
  await expect(page.getByLabel("GitHub Repository")).toBeVisible();
  const setupScroller = page.locator(".n-modal.n-card > .n-card-content");
  await expect(setupScroller).toBeVisible();
  const setupScrollRange = await setupScroller.evaluate((element) => element.scrollHeight - element.clientHeight);
  expect(setupScrollRange).toBeGreaterThan(0);
  await setupScroller.hover();
  await page.mouse.wheel(0, 900);
  await expect.poll(() => setupScroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
  await expect(page.getByRole("button", { name: "Copy install command", exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Done", exact: true }).click();
  await expect(page.getByText("Close Without Copying?", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Agent Setup", exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Copy install command", exact: true }).click();
  await expect(page.getByRole("button", { name: "Copied", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Done", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Agent Setup", exact: true })).toBeHidden();

  const cfg = await connectRPC<GetPublicProxyConfigResponse>(page.request, baseURL, "GetPublicProxyConfig", {});
  const createdAgent = cfg.agents.find((agent) => agent.name === agentName);
  expect(createdAgent).toBeTruthy();
  await connectRPC(page.request, baseURL, "DeleteAgent", { id: createdAgent!.id });
});
