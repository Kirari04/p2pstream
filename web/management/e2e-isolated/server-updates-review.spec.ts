import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { resolve, extname } from 'node:path';
import { build } from 'vite';

const output = resolve('tmp/server-updates-review-fixture');
const url = 'https://updates-review.test/e2e/fixtures/server-updates.html';

// Compile the production view/routed client into the existing fixture, then
// replace only its fake host methods with manually controlled network replies.
const reviewHarness = `
const visible = ref(true);
const review = window.__updateReview = { previews: [], overviews: [], starts: [], startReplies: [], holdOverview: false, failOverview: false, unavailableExecutor: false, loseStart: false, holdStart: false, mount: (value) => { visible.value = value; } };
for (const id of ['1', '2']) {
 const overview = clients[id].getServerUpdateOverview;
 clients[id].getServerUpdateOverview = async () => {
  if (review.failOverview) throw new Error('server restarting');
  const value = await overview();
  if (review.unavailableExecutor) { value.executorAvailable = false; value.warning = 'Executor is restarting'; }
  if (!review.holdOverview) return value;
  return new Promise(resolve => review.overviews.push({ id, resolve: () => resolve(value) }));
 };
 clients[id].previewServerUpdate = () => new Promise(resolve => review.previews.push({
  id, resolve: (version) => resolve(create(schemas.PreviewServerUpdateResponseSchema, {
   instanceId: ids[id], currentVersion: 'v0.1.53-staging.90', target: {...target, version},
   planToken: 'opaque-' + id + '-' + version, expiresAtUnixMillis: BigInt(Date.now() + 600000)
  }))
 }));
 clients[id].startServerUpdate = async (request) => {
  review.starts.push({ environment: id, ...request });
  if (review.loseStart) throw new Error('response lost');
  const value = {operation:create(schemas.ServerUpdateOperationSchema,{id:request.operationId,phase:'accepted',previousVersion:'v0.1.53-staging.90',targetVersion:target.version})};
  if (review.holdStart) return new Promise((resolve, reject) => review.startReplies.push({resolve: () => resolve(value), reject}));
  return value;
 };
}
`;

test.beforeAll(async () => {
  await build({ configFile: resolve('vite.config.ts'), logLevel: 'error',
    plugins: [{ name: 'server-updates-review-harness', transformIndexHtml: { order: 'pre', handler: (html) => html.replace('const app=createApp', reviewHarness + '\nconst app=createApp').replace('default:()=>h(ServerUpdates)', 'default:()=>visible.value ? h(ServerUpdates) : null') } }],
    build: { outDir: output, emptyOutDir: true, rollupOptions: { input: resolve('e2e/fixtures/server-updates.html') } },
  });
});

test.beforeEach(async ({ page }) => {
  await page.route('https://updates-review.test/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    const types: Record<string,string> = { '.html':'text/html', '.js':'text/javascript', '.css':'text/css', '.woff2':'font/woff2', '.woff':'font/woff' };
    const file = resolve(output, '.' + path);
    if (!file.startsWith(output + '/')) return route.abort();
    try { await route.fulfill({body:await readFile(file), contentType:types[extname(file)] ?? 'application/octet-stream'}); }
    catch { await route.abort(); }
  });
});

test('a late preview cannot replace a newer preview after environment A to B to A', async ({page}) => {
  await page.goto(url);
  await page.getByRole('button', {name:'Preview update', exact:true}).click();
  await expect.poll(() => page.evaluate(() => (window as any).__updateReview.previews.length)).toBe(1);
  await page.locator('#switch').click();
  await expect(page.getByText('INSTALLED ON Local')).toBeVisible();
  await page.locator('#switch').click();
  await expect(page.getByText('INSTALLED ON fireani.me')).toBeVisible();
  await page.getByRole('button', {name:'Preview update', exact:true}).click();
  await expect.poll(() => page.evaluate(() => (window as any).__updateReview.previews.length)).toBe(2);
  await page.evaluate(() => (window as any).__updateReview.previews[1].resolve('v0.1.53-staging.93'));
  await expect(page.getByRole('dialog')).toContainText('v0.1.53-staging.93');
  await page.evaluate(() => (window as any).__updateReview.previews[0].resolve('v0.1.53-staging.91'));
  await expect(page.getByRole('dialog')).toContainText('v0.1.53-staging.93');
  await page.getByRole('dialog').getByRole('button', {name:'Update fireani.me',exact:true}).click();
  expect(await page.evaluate(() => (window as any).__updateReview.starts[0].planToken)).toContain('staging.93');
});

test('an overview begun before Start cannot erase an accepted operation during downtime', async ({page}) => {
  await page.goto(url);
  await page.getByRole('button', {name:'Preview update', exact:true}).click();
  await page.evaluate(() => (window as any).__updateReview.previews[0].resolve('v0.1.53-staging.91'));
  await expect(page.getByRole('dialog')).toBeVisible();
  // Close/reopen is unnecessary: the status refresh button is under the modal,
  // so issue its click directly as an already queued timer could do naturally.
  await page.evaluate(() => { (window as any).__updateReview.holdOverview = true; });
  await page.getByRole('button',{name:'Refresh status',exact:true}).evaluate((button: HTMLElement) => button.click());
  await expect.poll(() => page.evaluate(() => (window as any).__updateReview.overviews.length)).toBe(1);
  await page.getByRole('dialog').getByRole('button', {name:'Update fireani.me',exact:true}).click();
  await expect(page.getByText('Update queued', {exact:true})).toBeVisible();
  await page.evaluate(() => { const review = (window as any).__updateReview; review.failOverview = true; review.overviews[0].resolve(); });
  await expect(page.getByText('server restarting', {exact:false})).toBeVisible();
  await expect(page.getByText('Update queued', {exact:true})).toBeVisible();
  await expect(page.getByRole('button', {name:'Preview update',exact:true})).toBeDisabled();
});

test('reloading after a lost response retries the exact saved request', async ({page}) => {
  await page.goto(url);
  await page.getByRole('button', {name:'Preview update', exact:true}).click();
  await page.evaluate(() => { const review = (window as any).__updateReview; review.loseStart = true; review.previews[0].resolve('v0.1.53-staging.91'); });
  await page.getByRole('dialog').getByRole('button', {name:'Update fireani.me',exact:true}).click();
  await expect(page.getByRole('button',{name:'Retry same request'})).toBeVisible();
  const original = await page.evaluate(() => (window as any).__updateReview.starts[0]);
  await page.reload();
  await page.getByRole('button',{name:'Retry same request'}).click();
  await expect(page.getByRole('button',{name:'Retry same request'})).toBeHidden();
  expect(await page.evaluate(() => (window as any).__updateReview.starts[0])).toEqual(original);
});


test('executor restart preserves the last confirmed operation', async ({page}) => {
  await page.goto(url);
  await page.getByRole('button', {name:'Preview update',exact:true}).click();
  await page.evaluate(() => {
    const review = (window as any).__updateReview;
    review.unavailableExecutor = true;
    review.previews[0].resolve('v0.1.53-staging.91');
  });
  await page.getByRole('dialog').getByRole('button', {name:'Update fireani.me',exact:true}).click();
  await expect(page.getByText('Executor is restarting', {exact:true})).toBeVisible();
  await expect(page.getByText('Update queued', {exact:true})).toBeVisible();
  await expect(page.getByRole('button', {name:'Preview update',exact:true})).toBeDisabled();
});

test('a rejected old Start cannot dismiss a newer preview after A to B to A', async ({page}) => {
  await page.goto(url);
  await page.getByRole('button', {name:'Preview update',exact:true}).click();
  await page.evaluate(() => {
    const review = (window as any).__updateReview;
    review.holdStart = true;
    review.previews[0].resolve('v0.1.53-staging.91');
  });
  await page.getByRole('dialog').getByRole('button', {name:'Update fireani.me',exact:true}).click();
  await expect.poll(() => page.evaluate(() => (window as any).__updateReview.startReplies.length)).toBe(1);
  await page.locator('#switch').evaluate((button: HTMLElement) => button.click());
  await expect(page.getByText('INSTALLED ON Local')).toBeVisible();
  await page.locator('#switch').click();
  await page.getByRole('button', {name:'Clear saved request'}).click();
  await page.getByRole('button', {name:'Preview update',exact:true}).click();
  await page.evaluate(() => (window as any).__updateReview.previews[1].resolve('v0.1.53-staging.93'));
  await expect(page.getByRole('dialog')).toContainText('v0.1.53-staging.93');
  await page.evaluate(() => (window as any).__updateReview.startReplies[0].reject(new Error('preview superseded')));
  await expect(page.getByRole('dialog')).toContainText('v0.1.53-staging.93');
  await expect(page.getByText('preview superseded', {exact:false})).toBeHidden();
});

test('unmounted Start completion cannot rewrite the saved request of a replacement view', async ({page}) => {
  await page.goto(url);
  await page.getByRole('button', {name:'Preview update',exact:true}).click();
  await page.evaluate(() => {
    const review = (window as any).__updateReview;
    review.holdStart = true;
    review.previews[0].resolve('v0.1.53-staging.91');
  });
  await page.getByRole('dialog').getByRole('button', {name:'Update fireani.me',exact:true}).click();
  await expect.poll(() => page.evaluate(() => (window as any).__updateReview.startReplies.length)).toBe(1);
  const record = await page.evaluate(() => sessionStorage.getItem('p2pstream:server-update:1'));
  await page.evaluate(() => (window as any).__updateReview.mount(false));
  await expect(page.getByText('Keep your server current', {exact:true})).toBeHidden();
  await page.evaluate(() => (window as any).__updateReview.mount(true));
  await expect(page.getByRole('button', {name:'Retry same request'})).toBeVisible();
  await page.evaluate(() => (window as any).__updateReview.startReplies[0].resolve());
  expect(await page.evaluate(() => sessionStorage.getItem('p2pstream:server-update:1'))).toBe(record);
  await expect(page.getByRole('button', {name:'Retry same request'})).toBeVisible();
});
