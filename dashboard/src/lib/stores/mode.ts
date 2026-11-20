import { derived, writable } from 'svelte/store';

/** The deployment mode the server reports; the root layout sets it from its load data. */
export const dashboardMode = writable({ scoped: false });

/** True when the dashboard is limited to namespaces matching a label selector. */
export const scoped = derived(dashboardMode, (m) => m.scoped);
