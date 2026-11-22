// The dashboard's appearance: Dark (the default), Light, or System, which follows the
// operating system. The choice is per viewer, kept in localStorage, and applied as
// data-theme on <html>. The inline script in app.html applies it before the first paint with
// the same rules as resolveTheme, so a viewer never sees the wrong theme flash.

export const THEME_KEY = 'kubemoot-dashboard-theme';

export const THEME_PREFS = ['dark', 'light', 'system'] as const;
export type ThemePref = (typeof THEME_PREFS)[number];
export type Theme = 'dark' | 'light';

export const DEFAULT_THEME_PREF: ThemePref = 'dark';

export const THEME_LABELS: Record<ThemePref, string> = { dark: 'Dark', light: 'Light', system: 'System' };

/** A stored value as a choice; anything else is the default, Dark. */
export function parseThemePref(value: string | null | undefined): ThemePref {
	return THEME_PREFS.find((p) => p === value) ?? DEFAULT_THEME_PREF;
}

/** The theme a choice shows; System follows the operating system's light or dark setting. */
export function resolveTheme(pref: ThemePref, systemPrefersLight: boolean): Theme {
	if (pref === 'system') return systemPrefersLight ? 'light' : 'dark';
	return pref;
}

const LIGHT_QUERY = '(prefers-color-scheme: light)';

function systemPrefersLight(): boolean {
	try {
		return typeof matchMedia === 'function' && matchMedia(LIGHT_QUERY).matches;
	} catch {
		return false;
	}
}

/** The stored choice, or Dark when storage is absent, blocked or throws. */
export function loadThemePref(): ThemePref {
	try {
		return parseThemePref(localStorage.getItem(THEME_KEY));
	} catch {
		return DEFAULT_THEME_PREF;
	}
}

/** Remembers the choice; a blocked store leaves it for this page view only. */
export function saveThemePref(pref: ThemePref): void {
	try {
		localStorage.setItem(THEME_KEY, pref);
	} catch {
		// The choice still applies to this page view.
	}
}

/** Sets data-theme on <html> for a choice and returns the theme shown. */
export function applyTheme(pref: ThemePref): Theme {
	const theme = resolveTheme(pref, systemPrefersLight());
	document.documentElement.dataset.theme = theme;
	return theme;
}

/** Re-applies System whenever the operating system changes; returns the way to stop. */
export function followSystem(onchange: () => void): () => void {
	if (typeof matchMedia !== 'function') return () => {};
	const query = matchMedia(LIGHT_QUERY);
	query.addEventListener('change', onchange);
	return () => query.removeEventListener('change', onchange);
}
