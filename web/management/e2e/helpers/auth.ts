import { expect, type Page } from "@playwright/test";
import { connectRPC, connectRPCResponse } from "./connect";

type GetSetupStateResponse = {
  setupRequired?: boolean;
};

export type AuthenticateOptions = {
  managementPort?: string;
  managementBaseURL?: string;
  setupToken?: string;
  username?: string;
  password?: string;
  waitForEnvironmentSelect?: boolean;
};

export const defaultUsername = "admin";
export const defaultPassword = "playwright-password";
export const defaultSetupToken = "playwright-setup-token";

export async function authenticate(page: Page, appBaseURL: string, options: AuthenticateOptions = {}) {
  const managementPort = options.managementPort ?? process.env.PLAYWRIGHT_MANAGEMENT_PORT ?? "19081";
  const managementBaseURL = options.managementBaseURL ?? `https://localhost:${managementPort}`;
  const username = options.username ?? defaultUsername;
  const password = options.password ?? defaultPassword;
  const setupToken = options.setupToken ?? defaultSetupToken;
  const setupState = await connectRPC<GetSetupStateResponse>(
    page.request,
    managementBaseURL,
    "GetSetupState",
    {},
  );
  if (setupState.setupRequired) {
    await connectRPCWithRetry(page, managementBaseURL, "SetupAdmin", {
      username,
      password,
      setupToken,
    });
  }
  const loginResponse = await connectRPCResponseWithRetry(page, managementBaseURL, "Login", {
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
  if (options.waitForEnvironmentSelect !== false) {
    await expect(page.locator('select[title^="Selected environment:"]')).toBeVisible();
  }
}

export function sessionCookieFromHeader(header: string): string {
  const match = /(?:^|,\s*)p2pstream_session=([^;]+)/.exec(header);
  expect(match, `missing session cookie in ${header}`).not.toBeNull();
  return decodeURIComponent(match?.[1] ?? "");
}

async function connectRPCWithRetry<T>(page: Page, baseURL: string, method: string, payload: unknown): Promise<T> {
  const response = await connectRPCResponseWithRetry(page, baseURL, method, payload);
  return await response.json() as T;
}

async function connectRPCResponseWithRetry(page: Page, baseURL: string, method: string, payload: unknown) {
  let lastError: unknown;
  for (let attempt = 0; attempt < 6; attempt += 1) {
    try {
      return await connectRPCResponse(page.request, baseURL, method, payload);
    } catch (error) {
      lastError = error;
      if (!String(error).includes("database is locked")) {
        throw error;
      }
      await page.waitForTimeout(250 * (attempt + 1));
    }
  }
  throw lastError;
}
