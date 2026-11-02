import { randomBytes } from "node:crypto";

const credentialNames = [
  "PLAYWRIGHT_SETUP_TOKEN",
  "PLAYWRIGHT_AGENT_TOKEN",
  "PLAYWRIGHT_ADMIN_PASSWORD",
] as const;
type CredentialName = typeof credentialNames[number];

// Called only by runner configs. Playwright workers inherit this environment,
// including when they reload the config; helpers must only read these values.
export function initializeFixtureCredentials(): void {
  if (!process.env.PLAYWRIGHT_FIXTURE_CREDENTIALS_INITIALIZED) {
    for (const name of credentialNames) process.env[name] = randomBytes(32).toString("hex");
    process.env.PLAYWRIGHT_FIXTURE_CREDENTIALS_INITIALIZED = "1";
  }
  for (const name of credentialNames) fixtureCredential(name);
}

export function fixtureCredential(name: CredentialName): string {
  const value = process.env[name];
  if (!value || !/^[0-9a-f]{64}$/.test(value)) {
    throw new Error(`${name} must be supplied by the Playwright runner config`);
  }
  return value;
}
