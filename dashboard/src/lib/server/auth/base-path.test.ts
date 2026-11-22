import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

// The sign-in gate builds its redirects and cookie path from resolve(); a path relative to the
// request (SvelteKit's default) sends /dashboard/signin to signin/signin and loops.
describe('base path for sign-in redirects', () => {
	it('makes resolve() answer absolute paths', () => {
		const config = readFileSync(join(process.cwd(), 'vite.config.ts'), 'utf8');
		expect(config).toMatch(/base:\s*'\/dashboard'/);
		expect(config).toMatch(/relative:\s*false/);
	});
});
