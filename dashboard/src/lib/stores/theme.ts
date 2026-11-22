import { get, writable } from 'svelte/store';
import { browser } from '$app/env';
import { applyTheme, followSystem, loadThemePref, saveThemePref, DEFAULT_THEME_PREF, type ThemePref } from '#lib/theme.js';

/** The viewer's appearance choice; setting it saves it and applies it at once. */
function createThemePref() {
	const store = writable<ThemePref>(browser ? loadThemePref() : DEFAULT_THEME_PREF);
	const { subscribe, set } = store;
	let stop: () => void = () => {};

	function apply(pref: ThemePref) {
		applyTheme(pref);
		stop();
		stop = pref === 'system' ? followSystem(() => applyTheme(get(store))) : () => {};
	}

	return {
		subscribe,
		/** Reads the stored choice and applies it; the layout calls this once in the browser. */
		start() {
			const pref = loadThemePref();
			set(pref);
			apply(pref);
		},
		set(pref: ThemePref) {
			saveThemePref(pref);
			set(pref);
			apply(pref);
		}
	};
}

export const themePref = createThemePref();
