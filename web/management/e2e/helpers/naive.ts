import { expect, type Locator, type Page } from "@playwright/test";

export async function chooseNaiveSelectOption(page: Page, select: Locator, optionText: string | RegExp) {
  await select.scrollIntoViewIfNeeded();
  await select.click();
  const option = page.locator(".n-base-select-option").filter({ hasText: optionText }).last();
  await expect(option).toBeVisible();
  await option.click();
}

