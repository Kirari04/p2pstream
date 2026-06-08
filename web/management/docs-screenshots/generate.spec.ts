import { expect, test, type Page } from "@playwright/test";
import { existsSync, mkdirSync, statSync } from "fs";
import { resolve } from "path";
import { authenticate } from "../e2e/helpers/auth";
import { connectRPC } from "../e2e/helpers/connect";
import { repoDir } from "../playwright.shared";
import {
  seedDocsFixture,
  sendPublicProxyTraffic,
  startFixtureUpstream,
  type FixtureUpstream,
} from "./fixtures";

const setupToken = "docs-screenshot-setup-token";
const username = "admin";
const password = "docs-screenshot-password";
const managementPort = process.env.DOCS_SCREENSHOT_MANAGEMENT_PORT ?? "19091";
const frontendPort = process.env.DOCS_SCREENSHOT_FRONTEND_PORT ?? "5173";
const httpPort = process.env.DOCS_SCREENSHOT_HTTP_PORT ?? "19080";
const httpsPort = process.env.DOCS_SCREENSHOT_HTTPS_PORT ?? "19443";
const appBaseURL = `http://127.0.0.1:${frontendPort}`;
const managementBaseURL = `https://127.0.0.1:${managementPort}`;
const databasePath = resolve(repoDir, "tmp/docs-screenshots-data/p2pstream.db");
const screenshotsDir = resolve(repoDir, "docs/assets/new");

const screenshotNames = [
  "dashboard_overview.png",
  "live_traffic_diagram_tracing.png",
  "traffic_trace_request_details.png",
  "proxy_listeners.png",
  "proxy_edit_interface_listener_modal.png",
  "proxy_backends_and_routes.png",
  "proxy_edit_backend_modal.png",
  "proxy_edit_route_modal.png",
  "agents_page.png",
  "new_agent_modal_setup.png",
  "traffic_policies_waf_and_ratelimits.png",
  "edit_waf_modal.png",
  "edit_ratelimit_modal.png",
  "traffic_policies_cache_and_trafficshaper.png",
  "edit_cache_modal.png",
  "edit_traffic_shaper.png",
  "response_template_page.png",
  "edit_template_modal.png",
  "edit_template_modal_with_dynamic_values_waf.png",
  "tls_page.png",
  "tls_httpchallenge_letsencrypt_modal.png",
  "settings_api_tokens.png",
  "environment_settings_page.png",
  "environment_trust_certificate.png",
  "first_login_setup_admin.png",
  "login_page.png",
  "proxy_direct_route_modal.png",
  "proxy_agent_route_target_modal.png",
  "proxy_redirect_route_modal.png",
  "proxy_static_response_target_modal.png",
  "agent_edit_labels_modal.png",
  "tls_dns_credential_modal.png",
  "tls_dnschallenge_cloudflare_modal.png",
  "cache_settings_section.png",
  "waf_captcha_provider_modal.png",
  "settings_environment_editor_modal.png",
];

test.describe("docs screenshots", () => {
  test("generates the committed docs PNG assets", async ({ page }) => {
    test.setTimeout(180_000);
    mkdirSync(screenshotsDir, { recursive: true });

    let upstream: FixtureUpstream | null = null;
    try {
      upstream = await startFixtureUpstream();

      await page.goto(`/?setup_token=${encodeURIComponent(setupToken)}`);
      await expect(page.getByRole("heading", { name: "Setup Admin" })).toBeVisible();
      await capture(page, "first_login_setup_admin.png");

      await authenticate(page, appBaseURL, {
        managementPort,
        managementBaseURL,
        setupToken,
        username,
        password,
      });

      await logOut(page);
      await expect(page.getByRole("heading", { name: "Login" })).toBeVisible();
      await capture(page, "login_page.png");

      await authenticate(page, appBaseURL, {
        managementPort,
        managementBaseURL,
        setupToken,
        username,
        password,
      });

      const state = await seedDocsFixture(page.request, appBaseURL, {
        httpPort,
        httpsPort,
        upstreamURL: upstream.url,
        databasePath,
        managementBaseURL,
      });
      await connectRPC(page.request, appBaseURL, "StartPublicListener", { id: state.listeners.http.id });
      await connectRPC(page.request, appBaseURL, "SetTrafficTraceSettings", {
        enabled: true,
        level: "TRAFFIC_TRACE_LEVEL_DETAILED",
      });

      await page.waitForTimeout(6_000);
      await gotoApp(page, "/#/overview", "Proxy Overview");
      await capture(page, "dashboard_overview.png");

      await gotoApp(page, "/#/traffic", "Traffic Flow");
      await sendPublicProxyTraffic(page.request, httpPort);
      await expect(page.getByText("app.example.test").first()).toBeVisible({ timeout: 20_000 });
      await capture(page, "live_traffic_diagram_tracing.png");
      await openTraceDetails(page);
      await capture(page, "traffic_trace_request_details.png");
      await closeModal(page);

      await gotoApp(page, "/#/proxy", "Proxy");
      await capture(page, "proxy_listeners.png");
      await openFirstButton(page, "Edit listener", "Edit Listener");
      await capture(page, "proxy_edit_interface_listener_modal.png");
      await closeModal(page);

      await page.getByRole("heading", { name: "Routes" }).scrollIntoViewIfNeeded();
      await capture(page, "proxy_backends_and_routes.png");

      await openRoute(page, "app.example.test");
      await capture(page, "proxy_edit_route_modal.png");
      await capture(page, "proxy_direct_route_modal.png");
      await page.locator('[data-testid="route-target-row"]').first().scrollIntoViewIfNeeded();
      await capture(page, "proxy_edit_backend_modal.png");
      await closeModal(page);

      await openRoute(page, "api.example.test");
      await page.locator('[data-testid="route-target-row"]').first().scrollIntoViewIfNeeded();
      await capture(page, "proxy_agent_route_target_modal.png");
      await closeModal(page);

      await openRoute(page, "old.example.test");
      await capture(page, "proxy_redirect_route_modal.png");
      await closeModal(page);

      await openRoute(page, "maintenance.example.test");
      await page.locator('[data-testid="route-target-row"]').first().scrollIntoViewIfNeeded();
      await capture(page, "proxy_static_response_target_modal.png");
      await closeModal(page);

      await gotoApp(page, "/#/agent", "Agents");
      await capture(page, "agents_page.png");
      await openFirstButton(page, "Edit agent", "Edit Agent");
      await capture(page, "agent_edit_labels_modal.png");
      await closeModal(page);
      await createAgentSetupScreenshot(page);

      await gotoApp(page, "/#/policies", "Traffic Policy");
      await capture(page, "traffic_policies_waf_and_ratelimits.png");
      await openFirstButton(page, "Edit captcha provider", "Edit Captcha Provider");
      await capture(page, "waf_captcha_provider_modal.png");
      await closeModal(page);
      await openFirstButton(page, "Edit WAF rule", "Edit WAF Rule");
      await capture(page, "edit_waf_modal.png");
      await closeModal(page);
      await openFirstButton(page, "Edit rate-limit rule", "Edit Rate Limit");
      await capture(page, "edit_ratelimit_modal.png");
      await closeModal(page);

      await page.getByRole("heading", { name: "Cache", exact: true }).first().scrollIntoViewIfNeeded();
      await capture(page, "cache_settings_section.png");
      await page.getByRole("heading", { name: "Traffic Shaper", exact: true }).first().scrollIntoViewIfNeeded();
      await capture(page, "traffic_policies_cache_and_trafficshaper.png");
      await openFirstButton(page, "Edit cache rule", "Edit Cache Rule");
      await capture(page, "edit_cache_modal.png");
      await closeModal(page);
      await openFirstButton(page, "Edit traffic-shaper rule", "Edit Traffic Shaper");
      await capture(page, "edit_traffic_shaper.png");
      await closeModal(page);

      await gotoApp(page, "/#/templates", "Response Templates");
      await capture(page, "response_template_page.png");
      await openTemplate(page, "docs-maintenance-page");
      await capture(page, "edit_template_modal.png");
      await closeModal(page);
      await openTemplate(page, "docs-captcha-page");
      await capture(page, "edit_template_modal_with_dynamic_values_waf.png");
      await closeModal(page);

      await gotoApp(page, "/#/tls", "TLS");
      await capture(page, "tls_page.png");
      await openFirstButton(page, "Edit DNS credential", "Edit DNS Credential");
      await capture(page, "tls_dns_credential_modal.png");
      await closeModal(page);
      await openTlsMapping(page, "app.example.test");
      await capture(page, "tls_httpchallenge_letsencrypt_modal.png");
      await closeModal(page);
      await openTlsMapping(page, "*.example.test");
      await capture(page, "tls_dnschallenge_cloudflare_modal.png");
      await closeModal(page);

      await gotoApp(page, "/#/settings/api-tokens", "API Tokens");
      await capture(page, "settings_api_tokens.png");

      await gotoApp(page, "/#/settings/environments", "Environments");
      await capture(page, "environment_settings_page.png");
      await openFirstButton(page, "Edit environment", "Edit Environment");
      await capture(page, "settings_environment_editor_modal.png");
      await closeModal(page);
      await openTrustCertificate(page);
      await capture(page, "environment_trust_certificate.png");
      await closeModal(page);

      for (const name of screenshotNames) {
        const path = resolve(screenshotsDir, name);
        expect(existsSync(path), `${name} was not generated`).toBeTruthy();
        expect(statSync(path).size, `${name} is empty`).toBeGreaterThan(0);
      }
    } finally {
      await upstream?.close();
    }
  });
});

async function gotoApp(page: Page, path: string, heading: string) {
  await page.goto(path);
  await expect(page.getByRole("heading", { name: heading, exact: true }).first()).toBeVisible({ timeout: 20_000 });
  await waitForSettled(page);
}

async function capture(page: Page, filename: string) {
  await disableAnimations(page);
  await waitForSettled(page);
  await page.screenshot({
    path: resolve(screenshotsDir, filename),
    animations: "disabled",
  });
}

async function disableAnimations(page: Page) {
  await page.addStyleTag({
    content: `
      *, *::before, *::after {
        animation-duration: 0s !important;
        animation-delay: 0s !important;
        transition-duration: 0s !important;
        transition-delay: 0s !important;
        scroll-behavior: auto !important;
        caret-color: transparent !important;
      }
    `,
  }).catch(() => {});
}

async function waitForSettled(page: Page) {
  await page.waitForLoadState("domcontentloaded").catch(() => {});
  await page.waitForLoadState("networkidle", { timeout: 2_000 }).catch(() => {});
  await page.waitForTimeout(250);
}

async function closeModal(page: Page) {
  await page.keyboard.press("Escape");
  await page.waitForTimeout(250);
}

async function logOut(page: Page) {
  await page.getByRole("button", { name: "Log out" }).first().click();
  await expect(page.getByText("Log out of p2pstream?")).toBeVisible();
  await page.getByRole("button", { name: "Log out" }).last().click();
}

async function openFirstButton(page: Page, buttonName: string, modalTitle: string) {
  await page.getByRole("button", { name: buttonName }).first().click();
  await expect(page.getByText(modalTitle)).toBeVisible({ timeout: 10_000 });
  await waitForSettled(page);
}

async function openRoute(page: Page, routeText: string) {
  await page.getByText(routeText).first().scrollIntoViewIfNeeded();
  const routeRow = page.locator(".grid").filter({
    hasText: routeText,
    has: page.getByRole("button", { name: "Edit route" }),
  }).first();
  await routeRow.getByRole("button", { name: "Edit route" }).click();
  await expect(page.getByText("Edit Route")).toBeVisible({ timeout: 10_000 });
  await waitForSettled(page);
}

async function openTemplate(page: Page, templateName: string) {
  await page.getByText(templateName).first().scrollIntoViewIfNeeded();
  const templateRow = page.locator(".grid").filter({
    hasText: templateName,
    has: page.getByRole("button", { name: "Edit template" }),
  }).first();
  await templateRow.getByRole("button", { name: "Edit template" }).click();
  await expect(page.getByRole("heading", { name: "Edit Response Template", exact: true })).toBeVisible({ timeout: 10_000 });
  await waitForSettled(page);
}

async function openTlsMapping(page: Page, hostname: string) {
  await page.getByText(hostname).first().scrollIntoViewIfNeeded();
  const tlsRow = page.locator(".grid").filter({
    hasText: hostname,
    has: page.getByRole("button", { name: "Edit TLS mapping" }),
  }).first();
  await tlsRow.getByRole("button", { name: "Edit TLS mapping" }).click();
  await expect(page.getByText("Edit TLS Mapping")).toBeVisible({ timeout: 10_000 });
  await waitForSettled(page);
}

async function openTraceDetails(page: Page) {
  const detailsButton = page.getByRole("button", { name: /Open trace details/i }).first();
  if (await detailsButton.isVisible().catch(() => false)) {
    await detailsButton.click();
  } else {
    const traceRow = page.locator("tbody tr").filter({ hasText: "app.example.test" }).first();
    await traceRow.click();
  }
  await expect(page.getByText("Trace details")).toBeVisible({ timeout: 10_000 });
  await waitForSettled(page);
}

async function createAgentSetupScreenshot(page: Page) {
  await page.getByRole("button", { name: "Add Agent" }).click();
  await expect(page.getByRole("heading", { name: "Add Agent", exact: true })).toBeVisible({ timeout: 10_000 });
  await page.getByLabel("Name").fill("Docs Install Agent");
  await page.getByRole("button", { name: "Create Agent" }).click();
  await expect(page.getByRole("heading", { name: "Agent Setup", exact: true })).toBeVisible({ timeout: 10_000 });
  await capture(page, "new_agent_modal_setup.png");
  await closeModal(page);
}

async function openTrustCertificate(page: Page) {
  const trustButton = page.getByRole("button", { name: "Trust certificate" }).first();
  if (await trustButton.isDisabled().catch(() => false)) {
    await page.getByRole("button", { name: "Discover certificate" }).first().click();
    await expect(trustButton).toBeEnabled({ timeout: 15_000 });
  }
  await trustButton.click();
  await expect(page.getByRole("heading", { name: "Trust Certificate", exact: true })).toBeVisible({ timeout: 10_000 });
  await waitForSettled(page);
}
