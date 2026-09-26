import { writable } from 'svelte/store';
import { browser } from '$app/environment';

export const ALL_NAMESPACES = '';
const DEFAULT_NAMESPACE = ALL_NAMESPACES;
const STORAGE_KEY = 'kubemoot-dashboard-namespace';

function createNamespaceStore() {
	// Initialize from localStorage if in browser
	const initial = browser
		? localStorage.getItem(STORAGE_KEY) || DEFAULT_NAMESPACE
		: DEFAULT_NAMESPACE;

	const { subscribe, set, update } = writable<string>(initial);

	return {
		subscribe,
		set: (value: string) => {
			if (browser) {
				localStorage.setItem(STORAGE_KEY, value);
			}
			set(value);
		},
		update,
		reset: () => {
			if (browser) {
				localStorage.setItem(STORAGE_KEY, DEFAULT_NAMESPACE);
			}
			set(DEFAULT_NAMESPACE);
		}
	};
}

export const namespace = createNamespaceStore();
