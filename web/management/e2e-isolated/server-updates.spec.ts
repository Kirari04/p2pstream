import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { resolve, extname } from 'node:path';
import { build } from 'vite';

const output = resolve('tmp/server-updates-fixture');
const url = 'https://updates.test/e2e/fixtures/server-updates.html';
function oneLineRecipe(installation: string, migration = false, options = '') {
 return "(set -o pipefail; curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 120 --max-filesize 65536 'https://github.com/server/repository/releases/download/v0.1.53-staging.90/setup-server-updater.sh' | sudo sh -s -- 'server/repository@v0.1.53-staging.90' '"+installation+"' '"+'d'.repeat(64)+"' 'arm64' "+(migration ? '--migrate-deployment ' : '')+'--'+options+')';
}
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
 await expect(page.getByText('Installed on Local')).toBeVisible();
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
 await page.getByRole('button',{name:'Copy setup command',exact:true}).click();
 const copied = await page.evaluate(()=>navigator.clipboard.readText());
 expect(copied).toContain("'server/repository@v0.1.53-staging.90' '11111111-1111-4111-8111-111111111111' '"+'d'.repeat(64)+"' 'arm64'");
 expect(copied).not.toMatch(/[\r\n]/);
 expect(copied).toContain("-- -p 'production' -f 'compose.yaml' -f 'production with spaces.yaml' --env-file '.env.production'");
 expect(copied).not.toContain('./scripts/');
 await expect(page.getByText('host-level Docker authority', {exact:false})).toBeVisible();
 await page.screenshot({path:testInfo.outputPath('release-setup-mobile.png'),fullPage:true,animations:'disabled'});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.locator('#switch').click();
 await expect(page.getByText('Installed on Local')).toBeVisible();
 await page.getByRole('button',{name:'Copy setup command',exact:true}).click();
 const switched = await page.evaluate(()=>navigator.clipboard.readText());
 expect(switched).toContain("'22222222-2222-4222-8222-222222222222'");
 expect(switched).not.toContain("-p 'production'");
});

test('legacy servers show a prerequisite and never a UI-derived command', async ({ page }) => {
 await page.goto(url+'?legacy');
 await expect(page.getByText('Manual release upgrade required', {exact:true})).toBeVisible();
 await expect(page.getByRole('button',{name:'Copy setup command',exact:true})).toHaveCount(0);
 await expect(page.getByText('Updates enabled · updater verified', {exact:true})).toHaveCount(0);
});

test('setup shows blockers before offering a command and recovers after a fresh ready response', async ({ page }, testInfo) => {
 await page.setViewportSize({width:390,height:844});
 await page.goto(url+'?setup&setup-blocked&clear-blockers');
 const prerequisites = page.getByLabel('Setup prerequisites');
 await expect(prerequisites).toContainText('Finish pending agent update recovery');
 await expect(prerequisites).toContainText('Finish management certificate rotation');
 await expect(page.getByRole('button',{name:'Copy setup command',exact:true})).toHaveCount(0);
 await expect(page.getByLabel('Host updater command')).toHaveCount(0);
 await expect(page.getByText('Manual release upgrade required',{exact:true})).toHaveCount(0);
 await page.screenshot({path:testInfo.outputPath('setup-prerequisites-mobile.png'),fullPage:true,animations:'disabled'});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.getByRole('button',{name:'Refresh status',exact:true}).click();
 await expect(prerequisites).toHaveCount(0);
 await expect(page.getByRole('button',{name:'Copy setup command',exact:true})).toBeVisible();
});

test('setup prerequisites belong to the selected environment', async ({ page }) => {
 await page.goto(url+'?setup&setup-blocked');
 await expect(page.getByLabel('Setup prerequisites')).toBeVisible();
 await page.locator('#switch').click();
 await expect(page.getByText('Installed on Local')).toBeVisible();
 await expect(page.getByLabel('Setup prerequisites')).toHaveCount(0);
 await expect(page.getByRole('button',{name:'Copy setup command',exact:true})).toBeVisible();
});

test('a stale setup response cannot offer a command after management disconnects', async ({ page }) => {
 await page.goto(url+'?setup&disconnect');
 await expect(page.getByRole('button',{name:'Copy setup command',exact:true})).toBeVisible();
 await page.getByRole('button',{name:'Refresh status',exact:true}).click();
 await expect(page.getByText('Waiting to confirm setup readiness',{exact:true})).toBeVisible();
 await expect(page.getByRole('button',{name:'Copy setup command',exact:true})).toHaveCount(0);
 await expect(page.getByLabel('Host updater command')).toHaveCount(0);
});

test('setup page reconnects and shows enabled only after verified updater response', async ({ page }) => {
 await page.goto(url+'?enroll');
 await expect(page.getByRole('button',{name:'Copy setup command',exact:true})).toBeVisible();
 await expect(page.getByText('Updates enabled · updater verified', {exact:true})).toHaveCount(0);
 await page.getByRole('button',{name:'Refresh status',exact:true}).click();
 await expect(page.getByText('Updates enabled · updater verified', {exact:true})).toBeVisible();
 await expect(page.getByRole('button',{name:'Copy setup command',exact:true})).toHaveCount(0);
});

for (const scenario of [
 {phase:'pulling', title:'Downloading verified images', copy:'before it stops the current server', enabled:false},
 {phase:'validating', title:'Validating the replacement', copy:'page can reconnect before these checks finish', enabled:false},
 {phase:'rolling_back', title:'Restoring the previous version', copy:'Wait for the recovery outcome', enabled:false},
 {phase:'succeeded', title:'Update complete', copy:'operation is complete', enabled:true},
 {phase:'rolled_back', title:'Update failed · previous version restored', copy:'selected release was not retained', enabled:true},
 {phase:'failed', title:'Update stopped before activation', copy:'Resolve the reported cause', enabled:true},
 {phase:'recovery_required', title:'Host recovery required', copy:'resolve the reported host problem first', enabled:false},
]) {
 test(`host outcome ${scenario.phase} remains distinct from management connectivity`, async ({page}, testInfo) => {
  await page.goto(url+'?phase='+scenario.phase+'&detail='+encodeURIComponent(scenario.phase==='succeeded' ? 'Update completed, but old backup cleanup could not finish: permission denied' : scenario.phase.includes('back') || scenario.phase==='failed' || scenario.phase==='recovery_required' ? 'Host error: <img src=x onerror=alert(1)> could not validate deployment' : ''));
  const operation=page.getByRole('region',{name:'Server update operation'});
  await expect(operation.getByRole('status').filter({hasText:scenario.title})).toBeVisible();
  await expect(operation).toContainText(scenario.copy);
  if(scenario.enabled) await expect(page.getByRole('button',{name:'Preview update',exact:true})).toBeEnabled();
  else await expect(page.getByRole('button',{name:'Preview update',exact:true})).toBeDisabled();
  await expect(page.getByText('Management responding',{exact:true})).toBeVisible();
  if (scenario.phase==='succeeded') {
   await expect(operation.getByText('Update completed with a maintenance warning',{exact:true})).toBeVisible();
   await expect(operation).toHaveClass(/update-progress--success/);
   await expect(operation).toContainText('Management connectivity alone does not establish route health');
  }
  if (scenario.phase==='recovery_required' || scenario.phase==='failed' || scenario.phase==='rolled_back') {
   await expect(operation.locator('img')).toHaveCount(0);
   await expect(operation).toContainText('<img src=x onerror=alert(1)>');
  }
  if (scenario.phase==='pulling' || scenario.phase==='validating') {
   await expect(operation.locator('[aria-current="step"]')).toHaveCount(1);
   await expect(operation).toContainText('since stage reported');
  }
  await page.screenshot({path:testInfo.outputPath(scenario.phase+'-desktop.png'),fullPage:true,animations:'disabled'});
 });
}

test('management disconnection preserves progress and never creates a failed outcome', async ({page},testInfo) => {
 await page.goto(url+'?phase=validating&disconnect');
 await expect(page.getByRole('status').filter({hasText:'Validating the replacement'})).toBeVisible();
 await page.getByRole('button',{name:'Refresh status',exact:true}).click();
 await expect(page.getByText('Waiting for management',{exact:true})).toBeVisible();
 await expect(page.getByText('Last confirmed host stage',{exact:true})).toBeVisible();
 await expect(page.getByRole('status').filter({hasText:'Validating the replacement'})).toBeVisible();
 await expect(page.getByRole('button',{name:'Preview update',exact:true})).toBeDisabled();
 await expect(page.getByText('Update stopped before activation',{exact:true})).toHaveCount(0);
 await expect(page.getByText('Updates enabled · updater verified',{exact:true})).toHaveCount(0);
 await page.screenshot({path:testInfo.outputPath('management-disconnected.png'),fullPage:true,animations:'disabled'});
});

test('a completed outcome survives later management disconnection', async ({page}) => {
 await page.goto(url+'?phase=succeeded&disconnect');
 const duration=await page.getByTestId('update-total-elapsed').textContent();
 await page.getByRole('button',{name:'Refresh status',exact:true}).click();
 await expect(page.getByText('Waiting for management',{exact:true})).toBeVisible();
 await expect(page.getByRole('status').filter({hasText:'Update complete'})).toBeVisible();
 await expect(page.getByText('An unavailable management or updater connection does not change this recorded state.',{exact:false})).toBeVisible();
 await expect(page.getByText('A connection interruption does not establish whether this update succeeded or failed.',{exact:false})).toHaveCount(0);
 await expect(page.getByTestId('update-total-elapsed')).toHaveText(duration || '');
});

test('switching environment clears operation, freshness, and diagnostic identity', async ({page}) => {
 await page.goto(url+'?phase=validating');
 await expect(page.getByRole('region',{name:'Server update operation'})).toBeVisible();
 await page.locator('#switch').click();
 await expect(page.getByText('Installed on Local',{exact:true})).toBeVisible();
 await expect(page.getByRole('region',{name:'Server update operation'})).toHaveCount(0);
 await expect(page.getByRole('button',{name:'Preview update',exact:true})).toBeEnabled();
 await expect(page.getByText('33333333-3333-4333-8333-333333333333',{exact:false})).toHaveCount(0);
});

test('mobile light progress fits and copies only the selected operation diagnostics', async ({page},testInfo) => {
 await page.context().grantPermissions(['clipboard-read','clipboard-write']);
 await page.setViewportSize({width:390,height:844});
 await page.goto(url+'?phase=validating&light');
 await page.getByRole('button',{name:'Copy diagnostics',exact:true}).click();
 const diagnostics=JSON.parse(await page.evaluate(()=>navigator.clipboard.readText()));
 expect(diagnostics.environment).toBe('fireani.me');
 expect(diagnostics.instanceId).toBe('11111111-1111-4111-8111-111111111111');
 expect(diagnostics.phase).toBe('validating');
 expect(diagnostics.planToken).toBeUndefined();
 expect(diagnostics.setupCommand).toBeUndefined();
 await page.screenshot({path:testInfo.outputPath('validating-mobile-light.png'),fullPage:true,animations:'disabled'});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

test('old executor timestamps remain unavailable instead of inventing elapsed progress', async ({page}) => {
 await page.goto(url+'?phase=validating&legacy-time');
 await expect(page.getByTestId('update-total-elapsed')).toHaveText('Unavailable');
 await expect(page.getByText('since stage reported',{exact:false})).toHaveCount(0);
 await expect(page.getByRole('status').filter({hasText:'Validating the replacement'})).toBeVisible();
});

for (const theme of ['light','dark']) {
 test(`validating progress is readable on ${theme==='light' ? 'desktop light' : 'mobile dark'}`, async ({page},testInfo) => {
  await page.setViewportSize(theme==='light' ? {width:1280,height:900} : {width:390,height:844});
  await page.goto(url+'?phase=validating'+(theme==='light' ? '&light' : ''));
  await expect(page.getByRole('status').filter({hasText:'Validating the replacement'})).toBeVisible();
  await page.screenshot({path:testInfo.outputPath('validating-'+(theme==='light' ? 'desktop-light' : 'mobile-dark')+'.png'),fullPage:true,animations:'disabled'});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 });
}

test('stale download progress describes sequencing without asserting current availability', async ({page}) => {
 await page.goto(url+'?phase=pulling&disconnect');
 await expect(page.getByText('Download before restart',{exact:true})).toBeVisible();
 await page.getByRole('button',{name:'Refresh status',exact:true}).click();
 await expect(page.getByText('Last confirmed host stage',{exact:true})).toBeVisible();
 await expect(page.getByText('Download before restart',{exact:true})).toBeVisible();
 await expect(page.getByText('Server stays online',{exact:false})).toHaveCount(0);
 await expect(page.getByText('current server stays online',{exact:false})).toHaveCount(0);
});

test('recovery blocks applying settings and uses a neutral connected-updater banner', async ({page}) => {
 await page.goto(url);
 const enabled=page.locator('.n-alert').filter({hasText:'Updates enabled · updater verified'});
 await expect(enabled).toBeVisible();
 const successColor=await enabled.locator('.n-alert__icon').evaluate((icon)=>getComputedStyle(icon).color);
 await page.goto(url+'?phase=recovery_required');
 const banner=page.locator('.n-alert').filter({hasText:'Host updater connected · operation recorded'});
 await expect(banner).toContainText('Resolve the host recovery above');
 await expect(banner).not.toContainText('manage apply');
 expect(await banner.locator('.n-alert__icon').evaluate((icon)=>getComputedStyle(icon).color)).not.toBe(successColor);
 await expect(page.getByRole('button',{name:'Preview update',exact:true})).toBeDisabled();
});

test('configured legacy deployment offers an exact, environment-bound migration command with Compose options', async ({page},testInfo) => {
 await page.context().grantPermissions(['clipboard-read','clipboard-write']);
 await page.goto(url+'?migration&light');
 const card=page.locator('.update-migration');
 await expect(card.getByText('Upgrade host updater',{exact:true})).toBeVisible();
 await expect(card).toContainText('pinned host updater');
 await expect(card).toContainText('Migration restarts the server.');
 await expect(card).toContainText('Restoring a disconnected updater may require an additional restart.');
 await expect(card).not.toContainText('Migration restarts the server once');
 await expect(card.getByText('Preserved',{exact:true})).toHaveCount(3);
 await expect(card).toContainText('original Compose files become bridges');
 await expect(card).toContainText('*.p2pstream-input-*');
 await expect(card).toContainText('paths printed by the installer');
 const enabled=page.locator('.n-alert').filter({hasText:'Updates enabled · updater verified'});
 await expect(enabled).toContainText('Complete the one-time host updater migration above to enable ordinary Compose recreation.');
 await expect(enabled).toContainText('Until then, apply settings through your existing host controller.');
 await expect(enabled).not.toContainText('Use the editable input paths recorded by your host controller');
 await card.getByText('Custom Compose project or files',{exact:true}).click();
 await page.getByLabel('Compose project name',{exact:true}).fill('production');
 await page.getByLabel('Compose files',{exact:true}).fill('compose.yaml\nproduction with spaces.yaml');
 await page.getByLabel('Compose environment files',{exact:true}).fill('.env.production');
 await page.getByLabel('Compose project directory',{exact:true}).fill('/srv/my deployment');
 await card.getByRole('button',{name:'Copy migration command',exact:true}).click();
 const copied=await page.evaluate(()=>navigator.clipboard.readText());
 const expected=oneLineRecipe('11111111-1111-4111-8111-111111111111', true, " -p 'production' -f 'compose.yaml' -f 'production with spaces.yaml' --env-file '.env.production' --project-directory '/srv/my deployment'");
 expect(copied).toBe(expected);
 await page.screenshot({path:testInfo.outputPath('deployment-migration-desktop-light.png'),fullPage:true,animations:'disabled'});
 await page.locator('#switch').click();
 await expect(page.getByText('Installed on Local',{exact:true})).toBeVisible();
 await page.getByRole('button',{name:'Copy migration command',exact:true}).click();
 const switched=await page.evaluate(()=>navigator.clipboard.readText());
 expect(switched).toContain("'22222222-2222-4222-8222-222222222222'");
 expect(switched).not.toContain('--project-directory');
 expect(switched).not.toContain("-p 'production'");
});

test('migration remains readable on mobile dark and supports copying after clipboard failure', async ({page},testInfo) => {
 await page.setViewportSize({width:390,height:844});
 await page.addInitScript(() => Object.defineProperty(navigator,'clipboard',{value:{writeText:async()=>{throw new Error('clipboard denied');}}}));
 await page.goto(url+'?migration');
 await page.getByRole('button',{name:'Copy migration command',exact:true}).click();
 await expect(page.getByText('Select and copy the migration command below.',{exact:true})).toBeVisible();
 await expect(page.locator('.update-command pre')).toContainText("'arm64' --migrate-deployment --");
 await page.screenshot({path:testInfo.outputPath('deployment-migration-mobile-dark.png'),fullPage:true,animations:'disabled'});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

test('migration prerequisites preserve the reported reason without inventing a command', async ({page},testInfo) => {
 const reason='Release metadata expired: <img src=x onerror=alert(1)>';
 await page.goto(url+'?migration&migration-unavailable&unavailable-detail='+encodeURIComponent(reason));
 const card=page.locator('.update-migration');
 await expect(card.getByText('Migration command unavailable',{exact:true})).toBeVisible();
 await expect(card).toContainText(reason);
 await expect(card.locator('img')).toHaveCount(0);
 await expect(page.getByRole('button',{name:'Copy migration command',exact:true})).toHaveCount(0);
 await expect(page.getByRole('button',{name:'Copy setup command',exact:true})).toHaveCount(0);
 await page.screenshot({path:testInfo.outputPath('deployment-migration-unavailable.png'),fullPage:true,animations:'disabled'});
});

test('older configured remotes without a migration offer retain their existing controls', async ({page}) => {
 await page.goto(url+'?migration&migration-first-only');
 await expect(page.getByText('Upgrade host updater',{exact:true})).toBeVisible();
 await page.locator('#switch').click();
 await expect(page.getByText('Installed on Local',{exact:true})).toBeVisible();
 await expect(page.getByText('Upgrade host updater',{exact:true})).toHaveCount(0);
 await expect(page.getByText('ordinary docker compose',{exact:false})).toHaveCount(0);
 await expect(page.getByText('Updates enabled · updater verified',{exact:true})).toBeVisible();
 await expect(page.getByRole('button',{name:'Preview update',exact:true})).toBeEnabled();
});

test('an active update hides a previous migration offer', async ({page}) => {
 await page.goto(url+'?migration&phase=validating');
 await expect(page.getByRole('status').filter({hasText:'Validating the replacement'})).toBeVisible();
 await expect(page.getByText('Upgrade host updater',{exact:true})).toHaveCount(0);
 await expect(page.getByRole('button',{name:'Copy migration command',exact:true})).toHaveCount(0);
});

test('an unconfirmed update request hides migration until its outcome is known', async ({page}) => {
 await page.addInitScript(() => sessionStorage.setItem('p2pstream:server-update:1',JSON.stringify({instanceId:'11111111-1111-4111-8111-111111111111',operationId:'33333333-3333-4333-8333-333333333333',planToken:'pending-plan'})));
 await page.goto(url+'?migration');
 await expect(page.getByRole('button',{name:'Retry same request',exact:true})).toBeVisible();
 await expect(page.getByText('Upgrade host updater',{exact:true})).toHaveCount(0);
 await page.getByRole('button',{name:'Clear saved request',exact:true}).click();
 await expect(page.getByText('Upgrade host updater',{exact:true})).toBeVisible();
});

test('one-line installer copies the exact server recipe with shell-safe custom context', async ({page}) => {
 await page.context().grantPermissions(['clipboard-read','clipboard-write']);
 await page.goto(url+'?setup');
 await expect(page.getByText('One-line command',{exact:true})).toBeVisible();
 await page.getByText('Custom Compose project or files',{exact:true}).click();
 await page.getByLabel('Compose project name',{exact:true}).fill("prod 'west'");
 await page.getByLabel('Compose files',{exact:true}).fill("compose.yaml\nprod 'quoted' $(touch never).yaml");
 await page.getByLabel('Compose environment files',{exact:true}).fill('.env.production');
 await page.getByLabel('Compose project directory',{exact:true}).fill('/srv/my deployment');
 const expected=oneLineRecipe('11111111-1111-4111-8111-111111111111',false," -p 'prod '\\''west'\\''' -f 'compose.yaml' -f 'prod '\\''quoted'\\'' $(touch never).yaml' --env-file '.env.production' --project-directory '/srv/my deployment'");
 await expect(page.getByLabel('Host updater command',{exact:true})).toHaveText(expected);
 await page.getByRole('button',{name:'Copy setup command',exact:true}).click();
 expect(await page.evaluate(()=>navigator.clipboard.readText())).toBe(expected);
 expect(expected).not.toMatch(/[\r\n]/);
 await expect(page.getByRole('button',{name:'Command copied',exact:true})).toBeVisible();
});

test('older server multiline recipes remain complete and are never relabeled one-line', async ({page}) => {
 await page.context().grantPermissions(['clipboard-read','clipboard-write']);
 await page.goto(url+'?setup&multiline');
 await expect(page.getByText('Complete setup block',{exact:true})).toBeVisible();
 await expect(page.getByText('One-line command',{exact:true})).toHaveCount(0);
 await expect(page.getByText('Run the complete setup block',{exact:false})).toBeVisible();
 const expected="(\n  curl --proto '=https' https://github.com/server/repository/releases/download/v0.1.53-staging.90/p2pstream_v0.1.53-staging.90_docker.tar.gz\n  sudo python3 -I helper.py install --bundle /tmp/verified-release --expect-installation '11111111-1111-4111-8111-111111111111' --expect-version 'v0.1.53-staging.90' --expect-arch 'arm64' --\n)";
 await page.getByRole('button',{name:'Copy setup command',exact:true}).click();
 expect(await page.evaluate(()=>navigator.clipboard.readText())).toBe(expected);
});

test('configured host explains pinned updater upgrades even without a migration offer', async ({page}) => {
 await page.goto(url);
 await expect(page.getByText('Upgrade host updater',{exact:true})).toHaveCount(0);
 const policy=page.locator('.update-host-policy');
 await expect(policy).toContainText('Server updates leave the pinned host updater unchanged.');
 await expect(policy).toContainText('It does not self-update.');
 await expect(policy).toContainText('Rerunning regular setup checks its health without refreshing it.');
 await expect(policy).toContainText('Future host updater upgrades require a separate host procedure; this page does not provide one.');
 await expect(page.getByRole('button',{name:'Preview update',exact:true})).toBeEnabled();
});

for (const theme of ['light','dark']) {
 for (const width of [390,1280]) {
  test(`one-line setup fits ${width===390 ? 'mobile' : 'desktop'} ${theme}`,async({page},testInfo)=>{
   await page.setViewportSize({width,height:width===390 ? 844 : 900});
   await page.goto(url+'?setup'+(theme==='light' ? '&light' : ''));
   const command=page.getByLabel('Host updater command',{exact:true});
   await expect(command).toBeVisible();
   expect(await command.evaluate(el=>el.scrollWidth<=el.clientWidth+1)).toBe(true);
   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
   expect(await command.textContent()).not.toMatch(/[\r\n]/);
   await expect(page.getByRole('button',{name:'Copy setup command',exact:true})).toBeVisible();
   await page.screenshot({path:testInfo.outputPath('setup-'+width+'-'+theme+'.png'),fullPage:true,animations:'disabled'});
  });
 }
}
