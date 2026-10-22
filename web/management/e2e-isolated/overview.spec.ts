import { expect, test } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { extname, resolve } from 'node:path';
import { build } from 'vite';

const output = resolve('tmp/overview-fixture');
const url = 'https://overview.test/e2e/fixtures/overview.html';
const rpc = '/p2pstream.v1.AgentManagementService/';

test.beforeAll(async () => {
  await build({ configFile: resolve('vite.config.ts'), logLevel: 'error', build: {
    outDir: output, emptyOutDir: true,
    rollupOptions: { input: resolve('e2e/fixtures/overview.html') },
  } });
});

for (const { width, theme } of [{ width: 1280, theme: 'light' }, { width: 390, theme: 'dark' }]) {
  test(`dashboard absence, empty snapshot, traffic and failed refresh at ${width}px ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    let mode: 'unavailable' | 'empty' | 'traffic' = 'unavailable';
    const browserErrors: string[] = [];
    page.on('pageerror', (error) => browserErrors.push(error.message));
    await page.route('https://overview.test/**', async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.startsWith(rpc)) {
        if (path.endsWith('/GetPublicProxyConfig')) return route.fulfill({ json: {} });
        if (path.endsWith('/GetDashboard')) {
          if (mode === 'unavailable') return route.fulfill({ status: 503, json: { code: 'unavailable', message: 'Dashboard metrics are temporarily unavailable.' } });
          const generated = Date.now();
          const requests = mode === 'traffic' ? '7' : '0';
          return route.fulfill({ json: {
            status: { proxyRunning: true, proxy: { state: 'PROXY_STATE_RUNNING' } },
            generatedAtUnixMillis: generated.toString(),
            windows: ['5m', '1h', '24h', '30d'].map((label) => ({ label, sinceUnixMillis: (generated - 3600000).toString(), proxyRequests: requests, proxySuccess: requests })),
            trafficBuckets: mode === 'traffic' ? [{ bucketUnixMillis: (Math.floor(generated / 300000) * 300000).toString(), requests, success: requests }] : [],
          } });
        }
        throw new Error(`Unexpected management request: ${path}`);
      }
      const file = resolve(output, '.' + path);
      if (!file.startsWith(output + '/')) return route.abort();
      const types: Record<string, string> = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.woff2': 'font/woff2' };
      try { return await route.fulfill({ body: await readFile(file), contentType: types[extname(file)] ?? 'application/octet-stream' }); }
      catch { return route.abort(); }
    });
    await page.goto(url + `?theme=${theme}`);
    await expect(page.locator('html')).toHaveCSS('color-scheme', theme);
    await expect(page.getByRole('heading', { name: 'Proxy Overview', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Waiting for dashboard metrics' })).toBeVisible();
    await expect(page.getByRole('alert')).toContainText('temporarily unavailable');
    await expect(page.locator('.status-summary')).toHaveCount(0);
    await expect(page.locator('.attention-panel')).toHaveCount(0);
    await expect(page.getByText('No proxy requests recorded yet.', { exact: true })).toHaveCount(0);
    await expect(page.getByRole('link', { name: 'Open diagnostics', exact: true })).toHaveAttribute('href', '#/monitor/diagnostics/overview');
    await page.screenshot({ path: testInfo.outputPath('unavailable.png'), fullPage: true });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);

    mode = 'empty';
    await page.getByRole('button', { name: 'Refresh fixture' }).click();
    await expect(page.getByRole('heading', { name: 'Waiting for dashboard metrics' })).toHaveCount(0);
    await expect(page.locator('.status-summary')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'No proxy requests recorded yet.' })).toBeVisible();
    await expect(page.locator('.trend-bars')).toHaveCount(0);
    await expect(page.getByRole('alert')).toHaveCount(0);

    mode = 'traffic';
    await page.getByRole('button', { name: 'Refresh fixture' }).click();
    await expect(page.locator('.trend-bars')).toBeVisible();
    await expect(page.getByLabel('Traffic trend values')).toContainText('7 requests');
    await expect(page.getByRole('heading', { name: 'No proxy requests recorded yet.' })).toHaveCount(0);

    mode = 'unavailable';
    await page.getByRole('button', { name: 'Refresh fixture' }).click();
    await expect(page.getByRole('alert')).toContainText('temporarily unavailable');
    await expect(page.locator('.trend-bars')).toBeVisible();
    await expect(page.getByLabel('Traffic trend values')).toContainText('7 requests');
    await expect(page.getByRole('heading', { name: 'Waiting for dashboard metrics' })).toHaveCount(0);
    expect(browserErrors).toEqual([]);
  });
}
