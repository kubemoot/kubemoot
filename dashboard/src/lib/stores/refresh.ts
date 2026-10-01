import { writable, derived } from 'svelte/store';

const DEFAULT_INTERVAL = 15000; // 15 seconds (poll fallback; live pages use watch→SSE)

interface RefreshState {
	enabled: boolean;
	interval: number;
	lastRefresh: number;
}

function createRefreshStore() {
	const { subscribe, update } = writable<RefreshState>({
		enabled: true,
		interval: DEFAULT_INTERVAL,
		lastRefresh: Date.now()
	});

	return {
		subscribe,
		enable: () => update((s) => ({ ...s, enabled: true })),
		disable: () => update((s) => ({ ...s, enabled: false })),
		toggle: () => update((s) => ({ ...s, enabled: !s.enabled })),
		setInterval: (interval: number) => update((s) => ({ ...s, interval })),
		trigger: () => update((s) => ({ ...s, lastRefresh: Date.now() }))
	};
}

export const refresh = createRefreshStore();

// Derived store for the refresh trigger timestamp
export const refreshTrigger = derived(refresh, ($refresh) => $refresh.lastRefresh);
