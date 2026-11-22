import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { parse } from 'svelte/compiler';
import { describe, expect, it } from 'vitest';
import { ACTIONS } from './action-definitions.js';

// Walks every Svelte file the dashboard ships and enforces where a tooltip goes: an icon-only
// button or clickable icon needs an aria-label and a tooltip from ACTIONS; a text button needs
// neither; tiles, list rows, menu items, tabs, fields and titles carry none.

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

const NO_TOOLTIP_TAGS = new Set(['select', 'input', 'summary', 'tr', 'li', 'h1', 'h2', 'span', 'td']);
const NO_TOOLTIP_ROLES = /^(menuitem|menuitemcheckbox|menuitemradio|tab)$/;

type Attrs = (name: string) => string | undefined;
const attrsOf = (el: Node): { get: Attrs; has: (name: string) => boolean } => {
	const list = el.attributes ?? [];
	const find = (name: string) => list.find((a) => a.type === 'Attribute' && a.name === name);
	return { get: (name) => attrText(find(name)), has: (name) => find(name) !== undefined };
};

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

function hasAriaLabel(el: Node): boolean {
	return (el.attributes ?? []).some((a) => a.type === 'Attribute' && (a.name === 'aria-label' || a.name === 'aria-labelledby'));
}

// A button or acting link whose only content is a glyph or an icon.
function isIconOnly(el: Node, source: string): boolean {
	const a = attrsOf(el);
	const acts = el.name === 'button' || (el.name === 'a' && (a.has('onclick') || a.has('download')));
	return acts && !/[A-Za-z]{2,}/.test(visibleWords(el.fragment, source));
}

// The (i) info icon opens the help popover on click; it has an aria-label and, by design, no tooltip.
function isInfoIcon(el: Node): boolean {
	return attrsOf(el).get('aria-haspopup') === 'dialog';
}

// A terse checkbox such as Mine may carry a short tooltip, on the checkbox or its label.
function isCheckbox(el: Node): boolean {
	return el.name === 'input' && attrsOf(el).get('type') === 'checkbox';
}

function forbiddenReason(el: Node, file: string): string | null {
	const a = attrsOf(el);
	if (NO_TOOLTIP_TAGS.has(el.name ?? '') && !isCheckbox(el)) return `<${el.name}> is a field, row, title or label and carries no tooltip`;
	if (NO_TOOLTIP_ROLES.test(a.get('role') ?? '')) return 'a menu item or tab carries no tooltip';
	if (el.name === 'a' && TILE_FILES.has(file)) return 'a tile carries no tooltip';
	if (a.get('class')?.split(/\s+/).includes('card')) return 'a card or list row carries no tooltip';
	return null;
}

const files = svelteFiles(srcDir).map((path) => ({
	file: relative(srcDir, path).replaceAll('\\', '/'),
	source: readFileSync(path, 'utf8')
}));

function iconOnlyProblems(el: Node, source: string, where: string, hasUse: boolean): string[] {
	if (!isIconOnly(el, source)) return [];
	const problems: string[] = [];
	if (!hasUse && !isInfoIcon(el)) problems.push(`${where}: icon-only <${el.name}> has no use:tooltip`);
	if (!hasAriaLabel(el)) problems.push(`${where}: icon-only <${el.name}> has no aria-label`);
	return problems;
}

function tooltipTextProblems(el: Node, use: Attr, source: string, where: string): string[] {
	const problems: string[] = [];
	const expr = use.expression ? source.slice(use.expression.start, use.expression.end) : '';
	// A component may pass the tooltip text to its button as `tip`; its callers take it from ACTIONS.
	if (!(expr.startsWith('ACTIONS.') || expr === 'tip')) problems.push(`${where}: tooltip text must come from ACTIONS (got ${expr})`);
	if ((el.attributes ?? []).some((a) => a.type === 'Attribute' && a.name === 'title')) problems.push(`${where}: <${el.name}> keeps a native title beside the tooltip`);
	if (isInfoIcon(el)) problems.push(`${where}: the (i) info icon carries no tooltip`);
	return problems;
}

function checkTooltipUse(el: Node, file: string, source: string, line: number): string[] {
	const where = `${file}:${line}`;
	const use = (el.attributes ?? []).find((a) => a.type === 'UseDirective' && a.name === 'tooltip');
	const problems = iconOnlyProblems(el, source, where, use !== undefined);
	if (!use) return problems;
	const forbidden = forbiddenReason(el, file);
	if (forbidden) problems.push(`${where}: ${forbidden}`);
	return [...problems, ...tooltipTextProblems(el, use, source, where)];
}

describe('tooltips go only on icon-only buttons', () => {
	it('finds the Svelte files', () => {
		expect(files.length).toBeGreaterThan(60);
	});

	it.each(files)('$file', ({ file, source }) => {
		const ast = parse(source, { modern: true }) as unknown as { fragment: unknown };
		const problems = elements(ast.fragment).flatMap((el) => checkTooltipUse(el, file, source, source.slice(0, el.start).split('\n').length));
		for (const c of wrappers(ast.fragment)) problems.push(`${file}: <${c.name} onclick> wraps a control; give the component its own use:tooltip`);
		expect(problems).toEqual([]);
	});

	it('keeps every action text short, plain and ASCII', () => {
		for (const [key, value] of Object.entries(ACTIONS)) {
			const text = typeof value === 'function' ? (value as (...a: unknown[]) => string)('x', 1) : value;
			expect(text, key).toMatch(/^[\x20-\x7e]+$/);
			expect(text, key).not.toMatch(/\bGPUs?\b|consultation/i);
			expect(text.split(/\s+/).length, key).toBeLessThanOrEqual(25);
		}
	});

	it('defines no action that no page uses', () => {
		const used = files.map((f) => f.source).join('\n');
		for (const key of Object.keys(ACTIONS)) expect(used, key).toContain(`ACTIONS.${key}`);
	});
});
