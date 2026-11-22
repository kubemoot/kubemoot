import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { parse } from 'svelte/compiler';
import { describe, expect, it } from 'vitest';
import { ACTIONS } from './action-definitions.js';

// Walks every Svelte file the dashboard ships and fails when an action has no
// shared tooltip: a button, a link that acts, a menu item, a toggle or a tab.

const srcDir = join(process.cwd(), 'src');

function svelteFiles(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) return svelteFiles(path);
		return entry.name.endsWith('.svelte') ? [path] : [];
	});
}

interface Attr {
	type: string;
	name: string;
	value?: unknown;
	expression?: { type: string; start: number; end: number };
}
interface Node {
	type: string;
	name?: string;
	attributes?: Attr[];
	fragment?: unknown;
	data?: string;
	start: number;
	end: number;
	[key: string]: unknown;
}

function attrText(attr: Attr | undefined): string | undefined {
	if (!attr) return undefined;
	const v = attr.value;
	if (Array.isArray(v)) return v.map((p: { data?: string }) => p.data ?? '').join('');
	if (v && typeof v === 'object' && 'data' in v) return String((v as { data: string }).data);
	return undefined;
}

function walk(node: unknown, visit: (n: Node) => void) {
	if (Array.isArray(node)) {
		for (const child of node) walk(child, visit);
		return;
	}
	if (!node || typeof node !== 'object') return;
	visit(node as Node);
	for (const [key, value] of Object.entries(node)) if (key !== 'parent') walk(value, visit);
}

function elements(node: unknown): Node[] {
	const out: Node[] = [];
	walk(node, (n) => n.type === 'RegularElement' && out.push(n));
	return out;
}

// A component that takes onclick wraps a control the test cannot see; it must own its tooltip instead.
function wrappers(node: unknown): Node[] {
	const out: Node[] = [];
	walk(node, (n) => n.type === 'Component' && (n.attributes ?? []).some((a) => a.name === 'onclick') && out.push(n));
	return out;
}

// Pages whose every link opens a page and is a tile: the Overview.
const TILE_FILES = new Set(['routes/+page.svelte']);

const ACTION_ROLES = /^(menuitem|menuitemcheckbox|menuitemradio|tab|button)$/;
const ACTION_TAGS = new Set(['button', 'summary', 'select']);

// Popup bodies that only stop a click from reaching the close-on-outside-click handler.
const GUARD_CLASSES = new Set(['help-popup', 'sysinfo-popup']);

type Attrs = (name: string) => string | undefined;
const attrsOf = (el: Node): { get: Attrs; has: (name: string) => boolean } => {
	const list = el.attributes ?? [];
	const find = (name: string) => list.find((a) => a.type === 'Attribute' && a.name === name);
	return { get: (name) => attrText(find(name)), has: (name) => find(name) !== undefined };
};

function isControl(el: Node, a: ReturnType<typeof attrsOf>): boolean {
	if (ACTION_TAGS.has(el.name ?? '') || ACTION_ROLES.test(a.get('role') ?? '')) return true;
	return el.name === 'input' && ['checkbox', 'radio'].includes(a.get('type') ?? '');
}

function isActing(el: Node, file: string, a: ReturnType<typeof attrsOf>): boolean {
	if (el.name === 'a') return TILE_FILES.has(file) || a.has('onclick') || a.has('download');
	return a.has('onclick') && !GUARD_CLASSES.has(a.get('class') ?? '');
}

function isAction(el: Node, file: string): boolean {
	const a = attrsOf(el);
	return isControl(el, a) || isActing(el, file, a);
}

// A control named only by a glyph or an icon needs an aria-label, since the tooltip only describes it.
function expressionWords(n: Node, source: string): string {
	const literals = [...source.slice(n.start, n.end).matchAll(/'([^']*)'/g)].map((m) => m[1]);
	return literals.length > 0 ? literals.join(' ') : 'dynamic text';
}

function visibleWords(root: unknown, source: string): string {
	const words: string[] = [];
	walk(root, (n) => {
		if (n.type === 'Text') words.push(String(n.data ?? ''));
		else if (n.type === 'ExpressionTag') words.push(expressionWords(n, source));
	});
	return words.join(' ');
}

function hasName(el: Node, source: string): boolean {
	const attrs = el.attributes ?? [];
	if (attrs.some((a) => a.type === 'Attribute' && (a.name === 'aria-label' || a.name === 'aria-labelledby'))) return true;
	if (el.name === 'input' || el.name === 'select') return true;
	return /[A-Za-z]{3,}/.test(visibleWords(el.fragment, source));
}

const files = svelteFiles(srcDir).map((path) => ({
	file: relative(srcDir, path).replaceAll('\\','/'),
	source: readFileSync(path, 'utf8')
}));

describe('every action has a shared tooltip', () => {
	it('finds the Svelte files', () => {
		expect(files.length).toBeGreaterThan(60);
	});

	it.each(files)('$file', ({ file, source }) => {
		const ast = parse(source, { modern: true }) as unknown as { fragment: unknown };
		const missing: string[] = [];
		for (const el of elements(ast.fragment)) {
			if (!isAction(el, file)) continue;
			const attrs = el.attributes ?? [];
			const use = attrs.find((a) => a.type === 'UseDirective' && a.name === 'tooltip');
			const line = source.slice(0, el.start).split('\n').length;
			if (!use) {
				missing.push(`${file}:${line}: <${el.name}> has no use:tooltip`);
				continue;
			}
			if (!hasName(el, source)) missing.push(`${file}:${line}: <${el.name}> shows only a glyph or icon and has no aria-label`);
			const expr = use.expression ? source.slice(use.expression.start, use.expression.end) : '';
			if (!/^ACTIONS\.\w+/.test(expr)) missing.push(`${file}:${line}: tooltip text must come from ACTIONS (got ${expr})`);
			if (attrs.some((a) => a.type === 'Attribute' && a.name === 'title')) {
				missing.push(`${file}:${line}: <${el.name}> keeps a native title beside the tooltip`);
			}
		}
		for (const c of wrappers(ast.fragment)) missing.push(`${file}: <${c.name} onclick> wraps a control; give the component its own use:tooltip`);
		expect(missing).toEqual([]);
	});

	it('keeps every action text short, plain and ASCII', () => {
		for (const [key, value] of Object.entries(ACTIONS)) {
			const text = typeof value === 'function' ? (value as (...a: unknown[]) => string)('x', 1) : value;
			expect(text, key).toMatch(/^[\x20-\x7e]+$/);
			expect(text, key).not.toMatch(/\bGPUs?\b|consultation/i);
			expect(text.split(/\s+/).length, key).toBeLessThanOrEqual(25);
		}
	});
});
