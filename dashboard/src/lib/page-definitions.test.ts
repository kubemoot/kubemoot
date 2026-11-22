import { readdirSync, readFileSync } from 'node:fs';
import { join, relative, sep } from 'node:path';
import { describe, expect, it } from 'vitest';
import { PAGE_DEFINITIONS, menuLabel, pageDefinition } from './page-definitions.js';

const routesDir = join(process.cwd(), 'src', 'routes');

function pageFiles(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) return pageFiles(path);
		return entry.name === '+page.svelte' ? [path] : [];
	});
}

function routeId(file: string): string {
	const dir = relative(routesDir, file).split(sep).slice(0, -1).join('/');
	return '/' + dir;
}

const pages = pageFiles(routesDir).map((file) => ({ file, route: routeId(file), source: readFileSync(file, 'utf8') }));
const escapeRegExp = (text: string) => text.replaceAll(/[.*+?^${}()|[\]\\]/g, String.raw`\$&`);
const sentences = (text: string) => text.split(/(?<=\.)\s+/).filter(Boolean);

describe('page definitions', () => {
	it('finds the pages', () => {
		expect(pages.length).toBeGreaterThan(30);
	});

	it.each(pages)('$route has a definition', ({ route }) => {
		expect(Object.keys(PAGE_DEFINITIONS)).toContain(route);
	});

	it.each(pages)('$route renders the page title component', ({ route, source }) => {
		const used = new RegExp(String.raw`<(PageTitle|ResourceList|DetailPanel|ListDetailLayout)[^>]*\sroute="${escapeRegExp(route)}"`);
		expect(source).toMatch(used);
	});

	it('has no definition for a route that does not exist', () => {
		const routes = new Set(pages.map((p) => p.route));
		for (const key of Object.keys(PAGE_DEFINITIONS)) expect(routes.has(key)).toBe(true);
	});

	it.each(Object.entries(PAGE_DEFINITIONS))('%s has a term and a purpose of short plain sentences', (_route, def) => {
		for (const text of [def.term, def.purpose]) {
			expect(sentences(text).length).toBeGreaterThanOrEqual(1);
			for (const sentence of sentences(text)) expect(sentence.split(/\s+/).length).toBeLessThanOrEqual(20);
		}
		const all = `${def.title} ${def.term} ${def.purpose}`;
		expect(all).toMatch(/^[\x20-\x7e]+$/);
		expect(all).not.toMatch(/\bGPUs?\b|consultation/i);
	});

	it.each(pages)('$route does not override the defined title unless it is a resource name', ({ route, source }) => {
		if (route.includes('[')) return;
		expect(source).not.toMatch(/<(PageTitle|ResourceList)[^>]*route="[^"]*"\s+title="/);
	});

	it('uses the menu label when one is set, else the title', () => {
		expect(menuLabel('/nodes')).toBe('Nodes');
		expect(pageDefinition('/nodes').title).toBe('Kubernetes Nodes');
		expect(menuLabel('/agents')).toBe('Agents');
	});
});
