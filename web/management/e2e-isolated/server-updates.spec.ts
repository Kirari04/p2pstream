import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { resolve, extname } from 'node:path';
import { build } from 'vite';

const output = resolve('tmp/server-updates-fixture');
const url = 'https://updates.test/e2e/fixtures/server-updates.html';
test.beforeAll(async () => {
  await build({ configFile: resolve('vite.config.ts'), logLevel: 'error', build: { outDir: output, emptyOutDir: true, rollupOptions: { input: resolve('e2e/fixtures/server-updates.html') } } });
});
test.beforeEach(async ({ page }) => {
  await page.route('https://updates.test/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    const types: Record<string,string> = {'.html':'text/html','.js':'text/javascript','.css':'text/css','.woff2':'font/woff2','.woff':'font/woff'};
    const file = resolve(output,'.'+path);
    if (!file.startsWith(output+'/')) return route.abort();
    try { await route.fulfill({ body:await readFile(file), contentType:types[extname(file)] ?? 'application/octet-stream' }); }
    catch { await route.abort(); }
  });
});

test('mobile preview names the selected server and retries a lost response with the same operation ID',async({page},testInfo)=>{
 await page.setViewportSize({width:390,height:844});
 await page.goto(url+'?lost');
 await page.getByRole('button',{name:'Preview update',exact:true}).click();
 const dialog=page.getByRole('dialog');
 await expect(dialog).toContainText('fireani.me');
 await expect(dialog).toContainText('Public requests and agent tunnels will be interrupted');
 await page.screenshot({path:testInfo.outputPath('preview-mobile.png'),fullPage:true,animations:'disabled'});
 expect(await dialog.evaluate(el=>el.scrollWidth<=el.clientWidth+1)).toBe(true);
 await dialog.getByRole('button',{name:'Update fireani.me',exact:true}).click();
 await expect(page.getByRole('button',{name:'Retry same request'})).toBeVisible();
 await page.getByRole('button',{name:'Retry same request'}).click();
 await expect(page.getByRole('button',{name:'Retry same request'})).toBeHidden();
 const requests=JSON.parse(await page.locator('#requests').textContent() ?? '[]');
 expect(requests).toHaveLength(2);
 expect(requests[0]).toEqual(requests[1]);
 expect(requests[0].environment).toBe('1');
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

test('switching environments discards an in-flight preview and routes the next one correctly',async({page},testInfo)=>{
 await page.goto(url+'?slow');
 await page.getByRole('button',{name:'Preview update',exact:true}).click();
 await page.locator('#switch').click();
 await expect(page.getByText('INSTALLED ON Local')).toBeVisible();
 // The second preview is deliberately started before the first completes.
 await page.getByRole('button',{name:'Preview update',exact:true}).click();
 const dialog=page.getByRole('dialog');
 await expect(dialog.getByRole('button',{name:'Update Local',exact:true})).toBeEnabled();
 await page.screenshot({path:testInfo.outputPath('preview-desktop.png'),fullPage:true,animations:'disabled'});
 await dialog.getByRole('button',{name:'Update Local',exact:true}).click();
 const requests=JSON.parse(await page.locator('#requests').textContent() ?? '[]');
 expect(requests).toHaveLength(1);
 expect(requests[0].environment).toBe('2');
 expect(requests[0].instanceId).toBe('22222222-2222-4222-8222-222222222222');
});

test('expired previews cannot start an update',async({page})=>{
 await page.goto(url+'?expired');
 await page.getByRole('button',{name:'Preview update',exact:true}).click();
 await expect(page.getByRole('dialog').getByRole('button',{name:'Update fireani.me',exact:true})).toBeDisabled();
 await expect(page.getByText('This preview expired. Close it and preview the release again.')).toBeVisible();
});


test('setup uses selected server identity and supports custom ordered Compose inputs', async ({ page }, testInfo) => {
 await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
 await page.setViewportSize({width:390,height:844});
 await page.goto(url+'?setup');
 await page.getByText('Custom Compose project or files', {exact:true}).click();
 await page.getByLabel('Compose project name', {exact:true}).fill('production');
 await page.getByLabel('Compose files', {exact:true}).fill('compose.yaml\nproduction with spaces.yaml');
 await page.getByLabel('Compose environment files', {exact:true}).fill('.env.production');
 await page.getByRole('button',{name:'Copy complete setup',exact:true}).click();
 const copied = await page.evaluate(()=>navigator.clipboard.readText());
 expect(copied).toContain("--expect-installation '11111111-1111-4111-8111-111111111111'");
 expect(copied).toContain("--expect-version 'v0.1.53-staging.90' --expect-arch 'arm64'");
 expect(copied).toContain("-- -p 'production' -f 'compose.yaml' -f 'production with spaces.yaml' --env-file '.env.production'");
 expect(copied).not.toContain('./scripts/');
 await expect(page.getByText('host-level Docker authority', {exact:false})).toBeVisible();
 await page.screenshot({path:testInfo.outputPath('release-setup-mobile.png'),fullPage:true,animations:'disabled'});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.locator('#switch').click();
 await expect(page.getByText('INSTALLED ON Local')).toBeVisible();
 await page.getByRole('button',{name:'Copy complete setup',exact:true}).click();
 const switched = await page.evaluate(()=>navigator.clipboard.readText());
 expect(switched).toContain("--expect-installation '22222222-2222-4222-8222-222222222222'");
 expect(switched).not.toContain("-p 'production'");
});

test('legacy servers show a prerequisite and never a UI-derived command', async ({ page }) => {
 await page.goto(url+'?legacy');
 await expect(page.getByText('Manual release upgrade required', {exact:true})).toBeVisible();
 await expect(page.getByRole('button',{name:'Copy complete setup',exact:true})).toHaveCount(0);
 await expect(page.getByText('Updates enabled · updater verified', {exact:true})).toHaveCount(0);
});

test('setup page reconnects and shows enabled only after verified updater response', async ({ page }) => {
 await page.goto(url+'?enroll');
 await expect(page.getByRole('button',{name:'Copy complete setup',exact:true})).toBeVisible();
 await expect(page.getByText('Updates enabled · updater verified', {exact:true})).toHaveCount(0);
 await page.getByRole('button',{name:'Refresh status',exact:true}).click();
 await expect(page.getByText('Updates enabled · updater verified', {exact:true})).toBeVisible();
 await expect(page.getByRole('button',{name:'Copy complete setup',exact:true})).toHaveCount(0);
});
