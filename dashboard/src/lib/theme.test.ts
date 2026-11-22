import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import { DEFAULT_THEME_PREF, THEME_KEY, applyTheme, loadThemePref, parseThemePref, resolveTheme, saveThemePref } from './theme.js';
import { themePref } from './stores/theme.js';

type Listener = () => void;
let lightOs = false;
let listeners: Listener[] = [];

function stubMatchMedia() {
	const fake = (query: string) => ({
		matches: query.includes('light') && lightOs,
		addEventListener: (_: string, l: Listener) => listeners.push(l),
		removeEventListener: (_: string, l: Listener) => {
			listeners = listeners.filter((x) => x !== l);
		}
	});
	// The inline script runs in the page's own window; the code under test reads the global.
	(document.defaultView as unknown as { matchMedia: typeof fake }).matchMedia = fake;
	vi.stubGlobal('matchMedia', fake);
}

beforeEach(() => {
	lightOs = false;
	listeners = [];
	stubMatchMedia();
	localStorage.clear();
	document.documentElement.removeAttribute('data-theme');
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe('the theme choice', () => {
	it('is Dark by default and for anything unreadable', () => {
		expect(DEFAULT_THEME_PREF).toBe('dark');
		expect(loadThemePref()).toBe('dark');
		for (const bad of [null, undefined, '', 'blue', 'LIGHT']) expect(parseThemePref(bad)).toBe('dark');
	});

	it('persists in localStorage', () => {
		saveThemePref('light');
		expect(localStorage.getItem(THEME_KEY)).toBe('light');
		expect(loadThemePref()).toBe('light');
	});

	it('falls back to Dark when storage throws, and saving does not throw', () => {
		vi.stubGlobal('localStorage', {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('blocked');
			}
		});
		expect(loadThemePref()).toBe('dark');
		expect(() => saveThemePref('light')).not.toThrow();
	});
});

describe('resolving the theme', () => {
	it('Dark and Light ignore the operating system, System follows it', () => {
		expect(resolveTheme('dark', true)).toBe('dark');
		expect(resolveTheme('light', false)).toBe('light');
		expect(resolveTheme('system', true)).toBe('light');
		expect(resolveTheme('system', false)).toBe('dark');
	});

	it('sets data-theme on the page from prefers-color-scheme for System', () => {
		lightOs = true;
		expect(applyTheme('system')).toBe('light');
		expect(document.documentElement.dataset.theme).toBe('light');
		lightOs = false;
		expect(applyTheme('system')).toBe('dark');
		expect(document.documentElement.dataset.theme).toBe('dark');
	});
});

describe('the store', () => {
	it('saves and applies a choice at once', () => {
		themePref.set('light');
		expect(get(themePref)).toBe('light');
		expect(localStorage.getItem(THEME_KEY)).toBe('light');
		expect(document.documentElement.dataset.theme).toBe('light');
	});

	it('applies the stored choice on start', () => {
		localStorage.setItem(THEME_KEY, 'light');
		themePref.start();
		expect(get(themePref)).toBe('light');
		expect(document.documentElement.dataset.theme).toBe('light');
	});

	it('with System, follows the operating system when it changes', () => {
		themePref.set('system');
		expect(document.documentElement.dataset.theme).toBe('dark');
		lightOs = true;
		for (const l of listeners) l();
		expect(document.documentElement.dataset.theme).toBe('light');
		themePref.set('dark');
		expect(listeners).toHaveLength(0);
		expect(document.documentElement.dataset.theme).toBe('dark');
	});
});

// The script element runs the page's own inline script in jsdom, as a browser would.
function runScript(source: string) {
	const el = document.createElement('script');
	el.textContent = source;
	document.head.append(el);
	el.remove();
}

// The inline script in app.html runs before the first paint; it must reach the same theme as resolveTheme.
describe('the script that applies the theme before the first paint', () => {
	const html = readFileSync(join(process.cwd(), 'src', 'app.html'), 'utf8');
	const script = new DOMParser().parseFromString(html, 'text/html').querySelector('script')?.textContent ?? '';

	function run(stored: string | null, osLight: boolean): string | null {
		localStorage.clear();
		if (stored !== null) localStorage.setItem(THEME_KEY, stored);
		lightOs = osLight;
		document.documentElement.removeAttribute('data-theme');
		runScript(script);
		return document.documentElement.getAttribute('data-theme');
	}

	it('is in the page head, before the page content', () => {
		expect(script).toContain(THEME_KEY);
		expect(html.indexOf('<script>')).toBeLessThan(html.indexOf('%sveltekit.body%'));
		expect(html).toContain('data-theme="dark"');
	});

	it('shows Dark with nothing stored, and Light only when chosen', () => {
		expect(run(null, false)).toBe('dark');
		expect(run(null, true)).toBe('dark');
		expect(run('light', false)).toBe('light');
		expect(run('dark', true)).toBe('dark');
	});

	it('follows the operating system for System', () => {
		expect(run('system', true)).toBe('light');
		expect(run('system', false)).toBe('dark');
	});

	it('agrees with resolveTheme for every choice and system setting', () => {
		for (const pref of ['dark', 'light', 'system'] as const) {
			for (const os of [true, false]) expect(run(pref, os)).toBe(resolveTheme(pref, os));
		}
	});

	it('shows Dark when storage throws', () => {
		vi.stubGlobal('localStorage', {
			getItem: () => {
				throw new Error('blocked');
			}
		});
		document.documentElement.removeAttribute('data-theme');
		runScript(script);
		expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
	});
});
