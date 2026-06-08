import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";
import { connectRPC } from "./helpers/connect";

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
  await expect(page.locator('select[title^="Selected environment:"]')).toBeVisible();

  await page.goto("/#/agent");
  await expect(page.getByRole("heading", { name: "Agents", exact: true })).toBeVisible();
  const agentRow = page.getByRole("row").filter({ hasText: agentPublicID }).first();
  await expect(agentRow).toBeVisible();
  await agentRow.getByRole("button", { name: "Edit agent" }).click();
  await expect(page.getByRole("heading", { name: "Edit Agent" })).toBeVisible();
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
  await expect(page.getByRole("heading", { name: "Add Route" })).toBeVisible();
  await page.getByLabel("Path prefix").fill(routePath);
  await page.getByTestId("route-target-row").first().getByLabel("Name").fill(targetName);
  await page.getByTestId("route-target-row").first().getByLabel("Transport").selectOption({ label: "Agent" });

  await page.getByTestId("exact-agent-selector").selectOption(agentPublicID);
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
