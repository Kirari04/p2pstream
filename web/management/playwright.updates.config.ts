import { defineConfig } from '@playwright/test';
// Browser routes serve a built fixture from disk. No development server starts.
export default defineConfig({
  testDir: './e2e-isolated',
  workers: 1,
  use: { viewport: { width: 1280, height: 900 } },
});
