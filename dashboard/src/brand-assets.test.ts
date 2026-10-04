// @vitest-environment node
import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const dashboard = (path: string) => fileURLToPath(new URL(`../${path}`, import.meta.url));
const appHtml = readFileSync(dashboard('src/app.html'), 'utf8');
const lockedFiles = readFileSync(dashboard('brand.lock'), 'utf8')
	.split('\n')
	.filter((line) => /^[0-9a-f]{64} /.test(line))
	.map((line) => line.split(' ')[1]);

describe('the page shell icons', () => {
	const icons = [...appHtml.matchAll(/<link rel="icon" href="%sveltekit\.assets%\/([^"]+)"/g)].map((m) => m[1]);

	it('links the brand favicon set: the .ico and the adaptive SVG', () => {
		expect(icons).toEqual(['favicon.ico', 'kubemoot-favicon.svg']);
	});

	it('links only files the dashboard ships and brand.lock pins', () => {
		for (const icon of icons) {
			expect(existsSync(dashboard(`static/${icon}`)), icon).toBe(true);
			expect(lockedFiles, icon).toContain(`static/${icon}`);
		}
	});

	it('ships a real icon file, not text', () => {
		const ico = readFileSync(dashboard('static/favicon.ico'));
		expect([...ico.subarray(0, 4)]).toEqual([0, 0, 1, 0]);
		expect(readFileSync(dashboard('static/kubemoot-favicon.svg'), 'utf8')).toMatch(/^<svg /);
	});
});

describe('the sidebar logo', () => {
	it('is the white-text lockup, shipped and pinned by brand.lock', () => {
		const logo = readFileSync(dashboard('src/lib/components/layout/KubemootLogo.svelte'), 'utf8');
		expect(logo).toContain("asset('kubemoot-horizontal-white-text.svg')");
		expect(existsSync(dashboard('static/kubemoot-horizontal-white-text.svg'))).toBe(true);
		expect(lockedFiles).toContain('static/kubemoot-horizontal-white-text.svg');
	});
});
