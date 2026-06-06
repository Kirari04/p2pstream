import { expect, test, type Page } from "@playwright/test";
import { connectRPC, connectRPCResponse } from "./helpers/connect";

const setupToken = "playwright-setup-token";
const username = "admin";
const password = "playwright-password";
const managementPort = process.env.PLAYWRIGHT_MANAGEMENT_PORT ?? "19081";

type CreateManagementAccessTokenResponse = {
  token: string;
};

type GetSetupStateResponse = {
  setupRequired?: boolean;
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

async function authenticate(page: Page, appBaseURL: string) {
  const managementBaseURL = `https://localhost:${managementPort}`;
  const setupState = await connectRPC<GetSetupStateResponse>(
    page.request,
    managementBaseURL,
    "GetSetupState",
    {},
  );
  if (setupState.setupRequired) {
    await connectRPC(page.request, managementBaseURL, "SetupAdmin", {
      username,
      password,
      setupToken,
    });
  }
  const loginResponse = await connectRPCResponse(page.request, managementBaseURL, "Login", {
    username,
    password,
  });
  const sessionCookie = sessionCookieFromHeader(loginResponse.headers()["set-cookie"] ?? "");
  await page.context().addCookies([{
    name: "p2pstream_session",
    value: sessionCookie,
    url: appBaseURL,
    httpOnly: true,
    secure: appBaseURL.startsWith("https:"),
    sameSite: "Lax",
  }]);
  await page.goto("/");
  await expect(page.locator('select[title^="Selected environment:"]')).toBeVisible();
}

function sessionCookieFromHeader(header: string): string {
  const match = /(?:^|,\s*)p2pstream_session=([^;]+)/.exec(header);
  expect(match, `missing session cookie in ${header}`).not.toBeNull();
  return decodeURIComponent(match?.[1] ?? "");
}
