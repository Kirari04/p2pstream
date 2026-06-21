import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";
import { connectRPC } from "./helpers/connect";
import { chooseNaiveSelectOption } from "./helpers/naive";

const managementPort = process.env.PLAYWRIGHT_MANAGEMENT_PORT ?? "19081";

type CreateManagementAccessTokenResponse = {
  token: string;
};

type CreateEnvironmentResponse = {
  environment: {
    id: string;
    name: string;
  };
};

type DiscoverEnvironmentCertificateResponse = {
  certificate: {
    sha256Fingerprint: string;
  };
};

test("switches to a trusted loopback environment through scoped Connect proxy", async ({ page }, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);

  const accessToken = await connectRPC<CreateManagementAccessTokenResponse>(
    page.request,
    baseURL,
    "CreateManagementAccessToken",
    {
      name: `Loopback E2E ${testInfo.project.name}`,
      enabled: true,
    },
  );

  const environmentName = `Loopback ${testInfo.project.name}`;
  const createEnvironment = await connectRPC<CreateEnvironmentResponse>(
    page.request,
    baseURL,
    "CreateEnvironment",
    {
      name: environmentName,
      managementUrl: `https://127.0.0.1:${managementPort}`,
      transport: "ENVIRONMENT_TRANSPORT_DIRECT",
      accessToken: accessToken.token,
      responseHeaderTimeoutMillis: "10000",
      enabled: true,
    },
  );
  const environmentID = createEnvironment.environment.id;

  const discovery = await connectRPC<DiscoverEnvironmentCertificateResponse>(
    page.request,
    baseURL,
    "DiscoverEnvironmentCertificate",
    { id: environmentID },
  );
  await connectRPC(page.request, baseURL, "TrustEnvironmentCertificate", {
    id: environmentID,
    sha256Fingerprint: discovery.certificate.sha256Fingerprint,
  });

  await page.reload();
  const environmentSelect = page.getByTestId("environment-select");
  await expect(environmentSelect).toBeVisible();
  await environmentSelect.click();
  await expect(page.locator(".n-base-select-option").filter({ hasText: environmentName })).toBeVisible();
  await page.keyboard.press("Escape");

  const dashboardResponse = page.waitForResponse((response) =>
    response.url().includes(`/environments/${environmentID}/p2pstream.v1.AgentManagementService/GetDashboard`) &&
    response.status() === 200
  );
  const publicConfigResponse = page.waitForResponse((response) =>
    response.url().includes(`/environments/${environmentID}/p2pstream.v1.AgentManagementService/GetPublicProxyConfig`) &&
    response.status() === 200
  );
  await chooseNaiveSelectOption(page, environmentSelect, environmentName);
  await Promise.all([dashboardResponse, publicConfigResponse]);

  await expect(environmentSelect).toContainText(environmentName);
  await expect(environmentSelect).toHaveAttribute("title", `Selected environment: ${environmentName}`);
  await expect(page.getByText(/404|not implemented/i)).toHaveCount(0);

  const localDashboardResponse = page.waitForResponse((response) =>
    !response.url().includes("/environments/") &&
    response.url().includes("/p2pstream.v1.AgentManagementService/GetDashboard") &&
    response.status() === 200
  );
  await chooseNaiveSelectOption(page, environmentSelect, "Local");
  await localDashboardResponse;
  await expect(environmentSelect).toContainText("Local");
  await expect(environmentSelect).toHaveAttribute("title", "Selected environment: Local");
});
