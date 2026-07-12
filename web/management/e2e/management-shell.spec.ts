import { expect, test, type Locator, type Page, type TestInfo } from "@playwright/test";
import { authenticate } from "./helpers/auth";

const sidebarStorageKey = "p2pstream:management-sidebar-collapsed";
const themeStorageKey = "p2pstream:management-theme";

const navigationGroups = [
  { label: "Observe", destinations: ["Overview", "Monitor"] },
  {
    label: "Configure",
    destinations: ["Proxy", "Agents", "Traffic Policy", "Templates", "TLS"],
  },
  { label: "System", destinations: ["Environments", "API Tokens"] },
] as const;

const allNavigationDestinations = [
  "Overview",
  "Monitor",
  "Traffic",
  "Diagnostics",
  "Proxy",
  "Routes",
  "Listeners",
  "Agents",
  "Traffic Policy",
  "Rate Limits",
  "WAF",
  "Cache",
  "Traffic Shaper",
  "Templates",
  "TLS",
  "Environments",
  "API Tokens",
] as const;

async function openAuthenticatedShell(
  page: Page,
  testInfo: TestInfo,
  options: { waitForEnvironmentSelect?: boolean } = {},
) {
  const baseURL = testInfo.project.use.baseURL as string;
  await authenticate(page, baseURL, options);
}

function managementSidebar(page: Page): Locator {
  return page.getByRole("complementary", { name: "Management navigation" });
}

test("groups desktop navigation and reflects nested active and breadcrumb state", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await openAuthenticatedShell(page, testInfo);

  const sidebar = managementSidebar(page);
  await expect(sidebar).toBeVisible();

  const environmentSelect = page.getByTestId("environment-select").getByRole("combobox", { name: "Environment" });
  await expect(environmentSelect).toBeVisible();
  await expect(environmentSelect).toHaveAttribute("aria-expanded", "false");
  await environmentSelect.click();
  await expect(environmentSelect).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("listbox", { name: "Environment options" })).toBeVisible();
  await page.keyboard.press("Escape");

  const groups = sidebar.locator(".app-sidebar__group");
  await expect(groups).toHaveCount(navigationGroups.length);

  for (const [index, expectedGroup] of navigationGroups.entries()) {
    const group = groups.nth(index);
    await expect(group.getByRole("heading", { name: expectedGroup.label, exact: true })).toBeVisible();
    for (const destination of expectedGroup.destinations) {
      await expect(group.getByRole("link", { name: destination, exact: true })).toBeVisible();
    }
  }

  const overviewLink = sidebar.getByRole("link", { name: "Overview", exact: true });
  await expect(overviewLink).toHaveAttribute("aria-current", "page");
  await expect(overviewLink).toHaveClass(/app-sidebar__link--active/);

  await sidebar.getByRole("link", { name: "Proxy", exact: true }).click();
  await expect(page).toHaveURL(/#\/proxy\/routes$/);
  await sidebar.getByRole("link", { name: "Listeners", exact: true }).click();
  await expect(page).toHaveURL(/#\/proxy\/listeners$/);

  const proxyLink = sidebar.getByRole("link", { name: "Proxy", exact: true });
  const listenersLink = sidebar.getByRole("link", { name: "Listeners", exact: true });
  await expect(proxyLink).toHaveClass(/app-sidebar__link--active/);
  await expect(proxyLink).not.toHaveAttribute("aria-current", "page");
  await expect(listenersLink).toHaveAttribute("aria-current", "page");
  await expect(listenersLink).toHaveClass(/app-sidebar__sublink--active/);

  const breadcrumb = page.getByRole("navigation", { name: "Breadcrumb" });
  await expect(breadcrumb).toBeVisible();
  await expect(breadcrumb.getByText("Configure", { exact: true })).toBeVisible();
  await expect(breadcrumb.getByRole("link", { name: "Proxy", exact: true })).toBeVisible();
  await expect(breadcrumb.getByText("Listeners", { exact: true })).toHaveAttribute("aria-current", "page");

  await sidebar.getByRole("link", { name: "Traffic Policy", exact: true }).click();
  await page.getByRole("button", { name: "Request Tester", exact: true }).click();
  const requestTester = page.getByRole("dialog", { name: "Request Tester" });
  await expect(requestTester).toBeVisible();
  const methodSelect = requestTester.getByRole("combobox", { name: "Request method" });
  const routeSelect = requestTester.getByRole("combobox", { name: "Request route" });
  await expect(methodSelect).toHaveCount(1);
  await expect(routeSelect).toHaveCount(1);
  await routeSelect.focus();
  await expect(routeSelect).toBeFocused();

  await methodSelect.click();
  await expect(methodSelect).toHaveAttribute("aria-expanded", "true");
  await expect(methodSelect).toHaveAttribute("aria-activedescendant", /.+/);
  const initialActiveOption = await methodSelect.getAttribute("aria-activedescendant");
  expect(initialActiveOption).toBeTruthy();
  await methodSelect.press("ArrowDown");
  await expect.poll(() => methodSelect.getAttribute("aria-activedescendant")).not.toBe(initialActiveOption);
  const nextActiveOption = await methodSelect.getAttribute("aria-activedescendant");
  expect(nextActiveOption).toBeTruthy();
  await expect(page.locator(`[id="${nextActiveOption}"]`)).toHaveAttribute("role", "option");
});

test("persists the desktop sidebar collapse preference across reloads", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await openAuthenticatedShell(page, testInfo);

  const sidebar = managementSidebar(page);
  await page.getByRole("button", { name: "Collapse navigation" }).click();
  await expect(sidebar).toHaveClass(/app-sidebar--collapsed/);
  await expect(page.getByRole("button", { name: "Expand navigation" })).toBeVisible();
  await expect.poll(() => page.evaluate((key) => localStorage.getItem(key), sidebarStorageKey)).toBe("true");

  await page.reload();
  await expect(page.getByTestId("environment-select")).toBeVisible();
  await expect(managementSidebar(page)).toHaveClass(/app-sidebar--collapsed/);
  await expect(page.getByRole("button", { name: "Expand navigation" })).toBeVisible();

  await page.getByRole("button", { name: "Expand navigation" }).click();
  await expect(managementSidebar(page)).not.toHaveClass(/app-sidebar--collapsed/);
  await expect.poll(() => page.evaluate((key) => localStorage.getItem(key), sidebarStorageKey)).toBe("false");

  await page.reload();
  await expect(page.getByTestId("environment-select")).toBeVisible();
  await expect(page.getByRole("button", { name: "Collapse navigation" })).toBeVisible();
  await expect(managementSidebar(page)).not.toHaveClass(/app-sidebar--collapsed/);
});

test("pins sidebar controls while only its navigation scrolls", async ({ page }, testInfo) => {
  const viewportHeight = 460;
  await page.setViewportSize({ width: 1280, height: viewportHeight });
  await openAuthenticatedShell(page, testInfo);

  const sidebar = managementSidebar(page);
  const navigation = sidebar.locator(".app-sidebar__nav");
  const collapseButton = sidebar.getByRole("button", { name: "Collapse navigation" });

  await expect(sidebar).toBeVisible();
  await expect(collapseButton).toBeVisible();
  await expect
    .poll(async () =>
      navigation.evaluate((element) => ({
        clientHeight: element.clientHeight,
        overflowY: getComputedStyle(element).overflowY,
        scrollHeight: element.scrollHeight,
      })),
    )
    .toMatchObject({ overflowY: "auto" });

  const navigationMetrics = await navigation.evaluate((element) => ({
    clientHeight: element.clientHeight,
    scrollHeight: element.scrollHeight,
  }));
  expect(navigationMetrics.scrollHeight).toBeGreaterThan(navigationMetrics.clientHeight);

  const sidebarBox = await sidebar.boundingBox();
  const collapseBox = await collapseButton.boundingBox();
  expect(sidebarBox?.y).toBe(0);
  expect(sidebarBox?.height).toBeLessThanOrEqual(viewportHeight);
  expect((collapseBox?.y ?? viewportHeight) + (collapseBox?.height ?? 1)).toBeLessThanOrEqual(viewportHeight);

  await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
  await expect(collapseButton).toBeVisible();
  await expect
    .poll(async () => Math.abs((await sidebar.boundingBox())?.y ?? Number.POSITIVE_INFINITY))
    .toBeLessThanOrEqual(1);
});

test("keeps the tablet rail named and removes unavailable collapse controls", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 900, height: 900 });
  await openAuthenticatedShell(page, testInfo);

  const sidebar = managementSidebar(page);
  await expect(sidebar).toBeVisible();
  await expect(sidebar.getByRole("link", { name: "Overview", exact: true })).toBeVisible();
  await expect(sidebar.getByRole("link", { name: "Proxy", exact: true })).toHaveAttribute("aria-label", "Proxy");
  await expect(page.getByRole("button", { name: "Collapse navigation" })).toBeHidden();
  await expect(page.getByRole("button", { name: "Expand navigation" })).toBeHidden();
  await expect(page.getByTestId("environment-select-mobile")).toBeHidden();
});

test("opens complete mobile navigation, restores focus, and closes after navigation", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await openAuthenticatedShell(page, testInfo, { waitForEnvironmentSelect: false });

  const menuButton = page.getByRole("button", { name: "Open navigation" });
  const sidebar = managementSidebar(page);
  await expect(menuButton).toBeVisible();
  await expect(sidebar).toBeHidden();
  await expect(menuButton).toHaveAttribute("aria-expanded", "false");

  for (const controlName of ["System theme", "Light theme", "Dark theme", "Refresh management data", "Log out"]) {
    const controlBox = await page.getByRole("button", { name: controlName, exact: true }).boundingBox();
    expect(controlBox?.width).toBeGreaterThanOrEqual(44);
    expect(controlBox?.height).toBeGreaterThanOrEqual(44);
  }

  await menuButton.click();
  await expect(menuButton).toHaveAttribute("aria-expanded", "true");
  await expect(sidebar).toBeVisible();
  await expect(page.getByTestId("environment-select-mobile")).toBeVisible();
  await expect(sidebar.getByRole("link", { name: "p2pstream overview" })).toBeFocused();
  await expect(sidebar.getByRole("combobox", { name: "Environment" })).toBeVisible();

  for (const destination of allNavigationDestinations) {
    await expect(sidebar.getByRole("link", { name: destination, exact: true })).toBeVisible();
  }

  await page.keyboard.press("Shift+Tab");
  await expect(sidebar.getByRole("link", { name: "API Tokens", exact: true })).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(sidebar.getByRole("link", { name: "p2pstream overview" })).toBeFocused();

  await sidebar.getByRole("button", { name: "Close navigation" }).click();
  await expect(sidebar).toBeHidden();
  await expect(menuButton).toHaveAttribute("aria-expanded", "false");
  await expect(menuButton).toBeFocused();

  await menuButton.click();
  await sidebar.getByRole("link", { name: "Diagnostics", exact: true }).click();
  await expect(page).toHaveURL(/#\/monitor\/diagnostics$/);
  await expect(sidebar).toBeHidden();
  await expect(menuButton).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator("#management-main")).toBeFocused();
  await expect(page.locator(".app-header__mobile-title")).toHaveText("Diagnostics");
});

test("supports and persists system, light, and dark theme modes", async ({ page }, testInfo) => {
  await page.emulateMedia({ colorScheme: "light" });
  await openAuthenticatedShell(page, testInfo);

  const systemTheme = page.getByRole("button", { name: "System theme" });
  const lightTheme = page.getByRole("button", { name: "Light theme" });
  const darkTheme = page.getByRole("button", { name: "Dark theme" });
  const documentElement = page.locator("html");

  await expect(systemTheme).toHaveAttribute("aria-pressed", "true");
  await expect(lightTheme).toHaveAttribute("aria-pressed", "false");
  await expect(darkTheme).toHaveAttribute("aria-pressed", "false");

  await darkTheme.click();
  await expect(darkTheme).toHaveAttribute("aria-pressed", "true");
  await expect(systemTheme).toHaveAttribute("aria-pressed", "false");
  await expect(documentElement).toHaveClass(/dark/);
  await expect.poll(() => page.evaluate((key) => localStorage.getItem(key), themeStorageKey)).toBe("dark");

  await page.reload();
  await expect(page.getByTestId("environment-select")).toBeVisible();
  await expect(page.getByRole("button", { name: "Dark theme" })).toHaveAttribute("aria-pressed", "true");
  await expect(documentElement).toHaveClass(/dark/);

  await page.getByRole("button", { name: "Light theme" }).click();
  await expect(page.getByRole("button", { name: "Light theme" })).toHaveAttribute("aria-pressed", "true");
  await expect(documentElement).not.toHaveClass(/dark/);
  await expect.poll(() => page.evaluate((key) => localStorage.getItem(key), themeStorageKey)).toBe("light");

  await page.reload();
  await expect(page.getByTestId("environment-select")).toBeVisible();
  await expect(page.getByRole("button", { name: "Light theme" })).toHaveAttribute("aria-pressed", "true");
  await expect(documentElement).not.toHaveClass(/dark/);

  await page.emulateMedia({ colorScheme: "dark" });
  await page.getByRole("button", { name: "System theme" }).click();
  await expect(page.getByRole("button", { name: "System theme" })).toHaveAttribute("aria-pressed", "true");
  await expect(documentElement).toHaveClass(/dark/);
  await expect.poll(() => page.evaluate((key) => localStorage.getItem(key), themeStorageKey)).toBe("system");

  await page.reload();
  await expect(page.getByTestId("environment-select")).toBeVisible();
  await expect(page.getByRole("button", { name: "System theme" })).toHaveAttribute("aria-pressed", "true");
  await expect(documentElement).toHaveClass(/dark/);

  await page.emulateMedia({ colorScheme: "light" });
  await expect(documentElement).not.toHaveClass(/dark/);
});
