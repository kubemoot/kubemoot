import { derived, writable } from 'svelte/store';

/** The deployment mode the server reports; the root layout sets it from its load data. */
export const dashboardMode = writable({ readOnly: false, scoped: false });

/**
 * True when the dashboard runs read-only (KUBEMOOT_DASHBOARD_READ_ONLY). Pages read
 * this one flag to hide or disable every control that writes.
 */
export const readOnly = derived(dashboardMode, (m) => m.readOnly);

/** True when the dashboard is limited to namespaces matching a label selector. */
export const scoped = derived(dashboardMode, (m) => m.scoped);
