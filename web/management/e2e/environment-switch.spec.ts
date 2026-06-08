import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";
import { connectRPC } from "./helpers/connect";

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
  const environmentSelect = page.locator('select[title^="Selected environment:"]');
  await expect(environmentSelect).toBeVisible();
  await expect(environmentSelect.locator(`option[value="${environmentID}"]`)).toHaveText(environmentName);

  const dashboardResponse = page.waitForResponse((response) =>
    response.url().includes(`/environments/${environmentID}/p2pstream.v1.AgentManagementService/GetDashboard`) &&
    response.status() === 200
  );
  const publicConfigResponse = page.waitForResponse((response) =>
    response.url().includes(`/environments/${environmentID}/p2pstream.v1.AgentManagementService/GetPublicProxyConfig`) &&
    response.status() === 200
  );
  await environmentSelect.selectOption(environmentID);
  await Promise.all([dashboardResponse, publicConfigResponse]);

  await expect(environmentSelect).toHaveValue(environmentID);
  await expect(environmentSelect).toHaveAttribute("title", `Selected environment: ${environmentName}`);
  await expect(page.getByText(/404|not implemented/i)).toHaveCount(0);

  const localDashboardResponse = page.waitForResponse((response) =>
    !response.url().includes("/environments/") &&
    response.url().includes("/p2pstream.v1.AgentManagementService/GetDashboard") &&
    response.status() === 200
  );
  await environmentSelect.selectOption("0");
  await localDashboardResponse;
  await expect(environmentSelect).toHaveValue("0");
  await expect(environmentSelect).toHaveAttribute("title", "Selected environment: Local");
});
