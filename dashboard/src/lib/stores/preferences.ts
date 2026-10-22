import { writable } from 'svelte/store';
import { browser } from '$app/env';

/**
 * User display preferences, persisted to localStorage so a choice sticks across
 * views and reloads. Shared by the Discussions and Fitness conversation views.
 */

const STAND_ASIDE_KEY = 'kubemoot-dashboard-show-standasides';

/** The stored flag, or the fallback when storage is absent, blocked or throws. */
export function readBoolPref(key: string, fallback: boolean): boolean {
	if (!browser) return fallback;
	try {
		const stored = localStorage.getItem(key);
		return stored === null ? fallback : stored === 'true';
	} catch {
		return fallback;
	}
}

/** Remembers the flag; a blocked or full store leaves the choice for this page view only. */
export function writeBoolPref(key: string, value: boolean): void {
	if (!browser) return;
	try {
		localStorage.setItem(key, String(value));
	} catch {
		// The choice still applies to this page view.
	}
}

function createBoolPref(key: string, fallback: boolean) {
	const { subscribe, set, update } = writable<boolean>(readBoolPref(key, fallback));
	return {
		subscribe,
		set: (value: boolean) => {
			writeBoolPref(key, value);
			set(value);
		},
		update,
		toggle: () =>
			update((v) => {
				const next = !v;
				writeBoolPref(key, next);
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

// The conversation view shows the conversation. Token counts, timings, placement,
// predictions and the work after the answer sit in a debug layer the viewer opens on
// purpose. Default OFF; the choice is remembered per viewer.
export const showDebugDetail = createBoolPref('kubemoot-dashboard-show-debug-detail', false);
