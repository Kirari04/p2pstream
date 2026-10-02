import { expect, test } from 'bun:test';
import { serverSetupCommand } from './serverSetup';

const recipe = '(\n  sudo python3 -I helper.py --expect-installation server-id --expect-version v2.3.4 --COMPOSE_OPTIONS--\n)';
test('uses the selected server recipe and preserves ordered shell-safe Compose options', () => {
  const command = serverSetupCommand(recipe, { project: 'production', files: "compose.yaml\nprod 'quoted' $(touch never).yaml", envFiles: '.env.prod', directory: '/srv/my deployment' });
  expect(command).toContain('--expect-version v2.3.4');
  expect(command).toContain("-- -p 'production' -f 'compose.yaml' -f 'prod '\\''quoted'\\'' $(touch never).yaml' --env-file '.env.prod' --project-directory '/srv/my deployment'");
  expect(command).not.toContain('--COMPOSE_OPTIONS--');
});
test('a missing or legacy server recipe cannot fabricate an install command', () => {
  const defaults = { project: '', files: '', envFiles: '', directory: '' };
  expect(serverSetupCommand('', defaults)).toBe('');
  expect(serverSetupCommand('sudo ./scripts/install-server-updater.sh', defaults)).toBe('');
  expect(serverSetupCommand(recipe, defaults)).toContain('v2.3.4 --\n');
});
