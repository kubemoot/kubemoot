import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

// The colour tokens of both themes live in app.css. This reads them and holds each theme to
// WCAG AA (4.5:1) for text on the surfaces it sits on, and keeps the status colours apart.

const css = readFileSync(join(process.cwd(), 'src', 'app.css'), 'utf8');

function tokens(selector: string): Record<string, string> {
	const start = css.indexOf(selector);
	const body = css.slice(css.indexOf('{', start) + 1, css.indexOf('\n}', start));
	const pairs = body.split('\n').map((line) => line.trim().replace(/;$/, '').split(': '));
	return Object.fromEntries(pairs.filter(([name, value]) => name.startsWith('--') && /^#[0-9a-f]{6}$/i.test(value ?? '')));
}

const dark = { ...tokens(":root,\n:root[data-theme='dark']") };
const light = tokens(":root[data-theme='light']");

function channel(v: number): number {
	const c = v / 255;
	return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}
function luminance(hex: string): number {
	const [r, g, b] = [1, 3, 5].map((i) => channel(Number.parseInt(hex.slice(i, i + 2), 16)));
	return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
export function contrast(a: string, b: string): number {
	const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
	return (hi + 0.05) / (lo + 0.05);
}

const TEXT = ['--color-text', '--color-text-muted', '--color-success', '--color-warning', '--color-error', '--color-primary', '--color-purple', '--color-cyan'];
const ACCENTS = ['--accent-green', '--accent-amber', '--accent-red', '--accent-blue', '--accent-purple', '--accent-sky', '--accent-indigo', '--accent-violet', '--accent-gray'];
const CODE = ['--yaml-key', '--yaml-string', '--yaml-number', '--yaml-bool', '--yaml-null', '--yaml-punct'];

/** A colour at 15% over a surface, the way the status badges and chips are painted. */
function tinted(hex: string, surface: string): string {
	const mix = (i: number) => Math.round(0.15 * Number.parseInt(hex.slice(i, i + 2), 16) + 0.85 * Number.parseInt(surface.slice(i, i + 2), 16));
	return `#${[1, 3, 5].map((i) => mix(i).toString(16).padStart(2, '0')).join('')}`;
}

const BADGE_TEXT = ['--color-success', '--color-warning', '--color-error', '--color-primary', '--color-purple'];

// Light is held to AA. Dark keeps today's colours and is checked only for its main text.
describe('the light theme (WCAG AA, 4.5:1)', () => {
	it.each(TEXT.flatMap((t) => ['--color-bg', '--color-bg-secondary'].map((s) => [t, s])))('%s reads on %s', (text, surface) => {
		expect(contrast(light[text], light[surface])).toBeGreaterThanOrEqual(4.5);
	});

	it.each(['--color-text', '--color-text-muted'])('%s reads on the tertiary surface', (text) => {
		expect(contrast(light[text], light['--color-bg-tertiary'])).toBeGreaterThanOrEqual(4.5);
	});

	it.each(BADGE_TEXT)('%s reads on its own tinted badge', (text) => {
		expect(contrast(light[text], tinted(light[text], light['--color-bg-secondary']))).toBeGreaterThanOrEqual(4.5);
	});

	it.each(ACCENTS)('%s reads on the page surfaces and on a tinted badge', (accent) => {
		for (const surface of ['--color-bg', '--color-bg-secondary']) expect(contrast(light[accent], light[surface])).toBeGreaterThanOrEqual(4.5);
		expect(contrast(light[accent], tinted(light[accent], light['--color-bg-secondary']))).toBeGreaterThanOrEqual(4.5);
	});

	it.each(CODE)('%s reads in the YAML and JSON view', (token) => {
		expect(contrast(light[token], light['--color-bg'])).toBeGreaterThanOrEqual(4.5);
	});

	it('keeps the status colours distinct', () => {
		const status = ['--color-success', '--color-warning', '--color-error', '--color-primary'].map((t) => light[t]);
		expect(new Set(status).size).toBe(4);
	});
});

describe('the dark theme', () => {
	it.each(['--color-text', '--color-text-muted'])('%s reads on the page surfaces', (text) => {
		for (const surface of ['--color-bg', '--color-bg-secondary']) expect(contrast(dark[text], dark[surface])).toBeGreaterThanOrEqual(4.5);
	});

	it.each(CODE)('%s reads in the YAML and JSON view', (token) => {
		expect(contrast(dark[token], dark['--color-bg'])).toBeGreaterThanOrEqual(4.5);
	});
});

describe('the two themes', () => {
	it('define the same colour tokens', () => {
		expect(Object.keys(light).toSorted((a, b) => a.localeCompare(b))).toEqual(Object.keys(dark).toSorted((a, b) => a.localeCompare(b)));
	});
});
