import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";
import { connectRPC } from "./helpers/connect";

type CreateEnvironmentResponse = {
  environment: {
    id: string;
    name: string;
  };
};

type CreateManagementAccessTokenResponse = {
  token: string;
};

test("keeps API token creation focused in a dismissible editor drawer", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  await page.goto("/#/settings/api-tokens");

  await expect(page.getByRole("heading", { name: "API Tokens", exact: true }).last()).toBeVisible();
  await expect(page.getByRole("dialog", { name: "Create API Token" })).toHaveCount(0);

  await page.getByRole("button", { name: "Create Token", exact: true }).first().click();
  const drawer = page.getByRole("dialog", { name: "Create API Token" });
  await expect(drawer).toBeVisible();
  await expect(drawer.getByLabel("Name", { exact: true })).toBeFocused();
  await drawer.getByLabel("Name", { exact: true }).fill("Deployment automation");

  await page.keyboard.press("Escape");
  await expect(drawer).toBeHidden();
  await expect(page.getByText("Deployment automation", { exact: true })).toHaveCount(0);
});

test("keeps environment lifecycle actions behind a labeled row menu", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);

  const fixtureSuffix = `${testInfo.project.name}-${Date.now()}`;
  const accessToken = await connectRPC<CreateManagementAccessTokenResponse>(
    page.request,
    baseURL,
    "CreateManagementAccessToken",
    {
      name: `Environment menu E2E ${fixtureSuffix}`,
      enabled: true,
    },
  );
  const environmentName = `Menu environment ${fixtureSuffix}`;
  await connectRPC<CreateEnvironmentResponse>(page.request, baseURL, "CreateEnvironment", {
    name: environmentName,
    managementUrl: "https://127.0.0.1:65534",
    transport: "ENVIRONMENT_TRANSPORT_DIRECT",
    accessToken: accessToken.token,
    responseHeaderTimeoutMillis: "1000",
    enabled: false,
  });

  await page.goto("/#/settings/environments");
  const row = page.locator("tbody tr").filter({ hasText: environmentName });
  await expect(row).toBeVisible();
  await expect(row.getByRole("button", { name: `More actions for ${environmentName}` })).toBeVisible();
  await expect(row.getByRole("button", { name: /Delete environment/ })).toHaveCount(0);

  await row.getByRole("button", { name: `More actions for ${environmentName}` }).click();
  await page.getByRole("menuitem", { name: "Edit environment", exact: true }).click();

  const drawer = page.getByRole("dialog", { name: "Edit Environment" });
  await expect(drawer).toBeVisible();
  await expect(drawer.getByLabel("Name", { exact: true })).toHaveValue(environmentName);
});
