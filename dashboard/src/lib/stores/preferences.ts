import { writable } from 'svelte/store';
import { browser } from '$app/environment';

/**
 * User display preferences, persisted to localStorage so a choice sticks across
 * views and reloads. Shared by the Discussions and Fitness conversation views.
 */

const STAND_ASIDE_KEY = 'kubemoot-dashboard-show-standasides';

function createBoolPref(key: string, fallback: boolean) {
	const initial = browser ? localStorage.getItem(key) === 'true' : fallback;
	const { subscribe, set, update } = writable<boolean>(initial);
	return {
		subscribe,
		set: (value: boolean) => {
			if (browser) localStorage.setItem(key, String(value));
			set(value);
		},
		update,
		toggle: () =>
			update((v) => {
				const next = !v;
				if (browser) localStorage.setItem(key, String(next));
				return next;
			})
	};
}

// A stand-aside is real consensus signal (which specialists saw the question and
// declined), but it's noisy in bulk - most agents stand aside on any given query.
// One global setting (Config → Display Preferences) drives every view: Discussions,
// Fitness, and the span graph. Default OFF: stand-aside / no-work agents are hidden
// (a small "N hidden" note remains so nothing is silently dropped); ON shows them.
export const showStandAsides = createBoolPref(STAND_ASIDE_KEY, false);
