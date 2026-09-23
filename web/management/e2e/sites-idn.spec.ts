import { expect, test } from "@playwright/test";
import { authenticate } from "./helpers/auth";
import { connectRPC } from "./helpers/connect";

type ProxyConfig = {
  sites: Array<{
    id: string;
    name: string;
    canonicalHostname: string;
    hosts: Array<{ hostnamePattern: string }>;
  }>;
};

test("shows IDN Site hostnames in Unicode while retaining ASCII DNS values", async ({
  page,
}, testInfo) => {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL);
  await page.goto("/#/proxy/sites");

  const surface = page
    .locator("section.surface-card")
    .filter({ has: page.getByRole("heading", { name: "Sites", exact: true }) });
  await surface
    .locator(".workbench-section-header")
    .getByRole("button", { name: "Add Site", exact: true })
    .click();

  let workspace = page.getByRole("dialog", { name: "Create Site workspace" });
  await workspace.getByLabel("Site name").fill("idn-site-e2e");
  await workspace.getByRole("button", { name: "Create draft Site" }).click();
  workspace = page.getByRole("dialog", { name: "Site workspace" });
  await workspace.getByRole("button", { name: "Add hostname" }).click();
  await workspace.getByRole("textbox", { name: /^Hostname 1\b/ }).fill("züribadi.example");
  await workspace.getByLabel("Canonical hostname").fill("züribadi.example");
  await workspace.getByRole("button", { name: "Save Site settings" }).click();
  await expect(workspace.getByText("Site settings saved")).toBeVisible();

  const config = await connectRPC<ProxyConfig>(
    page.request,
    baseURL,
    "GetPublicProxyConfig",
    {},
  );
  const site = config.sites.find((item) => item.name === "idn-site-e2e");
  expect(site?.canonicalHostname).toBe("xn--zribadi-n2a.example");
  expect(site?.hosts.map((host) => host.hostnamePattern)).toEqual([
    "xn--zribadi-n2a.example",
  ]);

  await workspace.locator(".n-drawer-header__close").click();
  const row = page.getByTestId(`site-row-${site?.id}`);
  await expect(row.locator(".site-table__primary-host bdi")).toHaveText(
    "züribadi.example",
  );
  await expect(row.locator(".site-table__primary-host bdi")).toHaveAttribute(
    "title",
    "xn--zribadi-n2a.example",
  );
  await expect(row.locator(".site-table__alias bdi")).toHaveText(
    "züribadi.example",
  );

  await row.getByRole("button", { name: "Edit site idn-site-e2e" }).click();
  workspace = page.getByRole("dialog", { name: "Site workspace" });
  await expect(workspace.getByRole("textbox", { name: /^Hostname 1\b/ })).toHaveValue(
    "züribadi.example",
  );
  await expect(workspace.getByLabel("Canonical hostname")).toHaveValue(
    "züribadi.example",
  );
});
