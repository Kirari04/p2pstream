import { defineConfig } from '@playwright/test';
// Browser routes serve a built fixture from disk. No development server starts.
export default defineConfig({
  // Archived benchmark sources are not needed in browser-report Git diffs.
  captureGitInfo: { diff: false },
  testDir: './e2e-isolated',
  workers: 1,
  use: { viewport: { width: 1280, height: 900 } },
});
