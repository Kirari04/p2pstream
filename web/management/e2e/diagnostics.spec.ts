import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";
import {
  DIAGNOSTIC_EXCERPT_LIMIT,
  diagnosticExcerpt,
  diagnosticInspectionText,
} from "../src/lib/diagnosticText";

test("exposes window state and safely discloses exact public-request samples", async ({ page }, testInfo) => {
  const host = `evil.example\\literal\u202E<img src=x onerror=alert(1)>${"h".repeat(96)}`;
  const pathPrefix = ` /api/\u2066${"segment/".repeat(20)} `;
  const errorKind = "direct_proxy_failed\u202Emasked";

  await page.route("**/p2pstream.v1.AgentManagementService/GetDashboardDiagnostics", async (route) => {
    const request = route.request().postDataJSON() as { windowLabel?: string };
    const label = request.windowLabel ?? "1h";
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        label,
        sinceUnixMillis: "0",
        generatedAtUnixMillis: "1700000000000",
        outcome: {
          label,
          sinceUnixMillis: "0",
          requests: "1",
          success: "0",
          clientError: "0",
          serverError: "1",
          nonSuccess: "1",
          proxyFailure: "1",
          avgDurationMs: "17",
          maxDurationMs: "17",
        },
        statusCodes: [],
        errorKinds: [{
          dimension: "DASHBOARD_PROXY_DIMENSION_ERROR_KIND",
          id: "0",
          label: errorKind,
          requests: "1",
          success: "0",
          clientError: "0",
          serverError: "1",
          internalError: "1",
          avgDurationMs: "17",
          requestBytes: "3",
          responseBytes: "5",
        }],
        problemListeners: [],
        problemRoutes: [],
        problemRouteTargets: [],
        problemAgents: [],
        recentSamples: [{
          occurredAtUnixMillis: "1700000000000",
          method: "GET",
          host,
          pathPrefix,
          statusCode: "502",
          errorKind,
          listenerLabel: "",
          routeLabel: "",
          routeTargetLabel: "",
          agentLabel: "",
          durationMs: "17",
          requestBytes: "3",
          responseBytes: "5",
        }],
      }),
    });
  });

  await authenticate(page, testInfo.project.use.baseURL as string);
  await page.goto("/#/monitor/diagnostics");

  const group = page.getByRole("group", { name: "Diagnostics window" });
  await expect(group.getByRole("button")).toHaveCount(4);
  for (const label of ["5m", "1h", "24h", "30d"]) {
    await expect(group.getByRole("button", { name: label, exact: true })).toHaveAttribute(
      "aria-pressed",
      label === "1h" ? "true" : "false",
    );
  }
  await expect(page.getByRole("combobox", { name: "Sample limit" })).toBeVisible();

  const request24h = page.waitForRequest((request) => (
    request.url().endsWith("/GetDashboardDiagnostics")
    && (request.postDataJSON() as { windowLabel?: string }).windowLabel === "24h"
  ));
  await group.getByRole("button", { name: "24h", exact: true }).click();
  await request24h;
  await expect(group.getByRole("button", { name: "24h", exact: true })).toHaveAttribute("aria-pressed", "true");
  await expect(group.getByRole("button", { name: "1h", exact: true })).toHaveAttribute("aria-pressed", "false");

  const view = page.getByRole("button", { name: /^View diagnostic sample from / });
  const row = view.locator("xpath=ancestor::tr");
  const excerpts = row.locator(".diagnostic-request-stack bdi.diagnostic-attacker-excerpt");
  await expect(excerpts).toHaveCount(2);
  await expect(excerpts.nth(0)).toHaveText(diagnosticExcerpt(host).text);
  await expect(excerpts.nth(1)).toHaveText(diagnosticExcerpt(pathPrefix).text);
  for (const excerpt of [excerpts.nth(0), excerpts.nth(1)]) {
    await expect(excerpt).toHaveAttribute("dir", "ltr");
    await expect(excerpt).toHaveCSS("unicode-bidi", "isolate");
    expect(Array.from((await excerpt.textContent()) ?? "").length).toBeLessThanOrEqual(DIAGNOSTIC_EXCERPT_LIMIT);
  }
  await expect(row.locator("img, script")).toHaveCount(0);

  const dimension = page.getByRole("button", { name: new RegExp("direct_proxy_failed") });
  const dimensionValue = dimension.locator("bdi.dimension-name");
  await expect(dimensionValue).toHaveText(diagnosticExcerpt(errorKind, 56).text);
  await expect(dimensionValue).toHaveCSS("unicode-bidi", "isolate");
  await dimension.click();
  await expect(dimension).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByText("Filtered by Error kinds", { exact: true })).toBeVisible();
  await expect(view).toHaveCount(1);

  await view.click();
  const drawer = page.getByRole("dialog", { name: "Diagnostic sample details" });
  const hostFull = drawer.locator("dt", { hasText: /^Host$/ }).locator("xpath=following-sibling::dd/bdi");
  const pathFull = drawer.locator("dt", { hasText: /^Path prefix$/ }).locator("xpath=following-sibling::dd/bdi");
  const errorFull = drawer.locator("dt", { hasText: /^Error kind$/ }).locator("xpath=following-sibling::dd/bdi");
  await expect(hostFull).toHaveText(diagnosticInspectionText(host));
  await expect(pathFull).toHaveText(diagnosticInspectionText(pathPrefix));
  await expect(errorFull).toHaveText(diagnosticInspectionText(errorKind));
  for (const fullValue of [hostFull, pathFull]) {
    await expect(fullValue).toHaveAttribute("dir", "ltr");
    await expect(fullValue).toHaveCSS("unicode-bidi", "isolate");
  }
  await expect(hostFull).toContainText("\\u{202E}");
  await expect(pathFull).toContainText("\\u{2066}");
});
