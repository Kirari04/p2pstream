import { shellQuote } from './agentSetupSnippets';

export interface ComposeSetupContext {
  project: string;
  files: string;
  envFiles: string;
  directory: string;
}

/** Extend only the verified recipe from the selected server. No UI build identity. */
export function serverSetupCommand(recipe: string, context: ComposeSetupContext): string {
  if (!recipe || recipe.split('--COMPOSE_OPTIONS--').length !== 2) return '';
  const options: string[] = [];
  const add = (flag: string, value: string) => { if (value.trim()) options.push(flag, shellQuote(value.trim())); };
  add('-p', context.project);
  for (const file of context.files.split(/\r?\n/)) add('-f', file);
  for (const file of context.envFiles.split(/\r?\n/)) add('--env-file', file);
  add('--project-directory', context.directory);
  return recipe.replace('--COMPOSE_OPTIONS--', '--' + (options.length ? ' ' + options.join(' ') : ''));
}
